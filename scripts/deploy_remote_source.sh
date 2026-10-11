#!/usr/bin/env bash
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/deployment_target.sh"
version_name="${VERSION_NAME:-dev}"
branch="${BRANCH:-$(git rev-parse --abbrev-ref HEAD)}"
git_remote="${GIT_REMOTE:-origin}"
extra_git_remote="${EXTRA_GIT_REMOTE:-github}"
deployment_stage="${ATLAS_DEPLOYMENT_STAGE:-development}"
bootstrap="${ATLAS_BOOTSTRAP:-0}"

if [[ "$bootstrap" != 0 && "$bootstrap" != 1 ]]; then
  echo "ATLAS_BOOTSTRAP must be 0 or 1" >&2
  exit 1
fi
if [[ ! "$version_name" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "VERSION_NAME contains unsafe characters" >&2
  exit 1
fi
if [[ "$bootstrap" == 1 && "$deployment_stage" != development ]]; then
  echo "First deployment requires development stage; production requires an existing backup-ready database." >&2
  exit 1
fi

if [[ "$deployment_stage" != "development" && "$deployment_stage" != "production" ]]; then
  echo "ATLAS_DEPLOYMENT_STAGE must be development or production, got: $deployment_stage" >&2
  exit 1
fi

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"
if [[ -n "$(git status --porcelain)" ]]; then
  echo "Working tree must be clean before deployment." >&2
  exit 1
fi

commit="$(git rev-parse --short HEAD)"
release_id="${version_name}-${commit}-$(date -u +%Y%m%dT%H%M%SZ)"
temporary_dir="$(mktemp -d)"
cleanup() {
  rm -rf "$temporary_dir"
}
trap cleanup EXIT

ensure_rsync_available() {
  for command_name in rsync curl jq ssh npm; do
    command -v "$command_name" >/dev/null || { echo "Missing local command: $command_name" >&2; exit 1; }
  done
  if ! atlas_ssh "$remote_ssh" "for tool in rsync docker curl jq flock systemctl; do command -v \"\$tool\" >/dev/null || exit 1; done; docker info >/dev/null 2>&1"; then
    echo "Remote dependency/connection preflight failed: $remote_ssh (no packages installed)" >&2
    exit 1
  fi
}

sync_to_remote() {
  local source_path="$1"
  local destination_path="$2"
  rsync --archive --checksum --partial --human-readable --progress \
    --rsh="$rsync_rsh" \
    "$source_path" "$remote_ssh:$destination_path"
}

ensure_rsync_available
if [[ "$bootstrap" == 1 ]]; then
  [[ -f "$repo_root/$config_template" ]] || { echo "Missing cluster configuration" >&2; exit 1; }
  atlas_ssh "$remote_ssh" "bash -s -- check '$remote_root'" < "$repo_root/scripts/bootstrap_remote.sh"
else
  atlas_ssh "$remote_ssh" "test -f '$remote_root/configs/config.yaml' && test -f /etc/atlas/atlas.env && test \"\$(systemctl show atlas.service --property=LoadState --value)\" = loaded" || {
    echo "Existing Atlas service/configuration not found; inspect the host before opting into ATLAS_BOOTSTRAP=1." >&2
    exit 1
  }
fi
echo "Deployment target: $deployment_target ($remote_ssh), bootstrap=$bootstrap, stage=$deployment_stage"

if [[ "${SKIP_GIT_PUSH:-0}" != "1" ]]; then
  git push "$git_remote" "$branch"
  if [[ -n "$extra_git_remote" ]] && git remote get-url "$extra_git_remote" >/dev/null 2>&1; then
    git push "$extra_git_remote" "$branch"
  fi
fi

echo "Building web assets locally without replacing current page data"
if [[ "${SKIP_NPM_INSTALL:-1}" != "1" ]]; then
  npm --prefix "$repo_root/web" ci
fi
npm --prefix "$repo_root/web" run build

git archive --format=tar.gz --output="$temporary_dir/$release_id.source.tar.gz" HEAD -- \
  . \
  ':(exclude)bin' \
  ':(exclude)dist.tar.gz' \
  ':(exclude)atlas.db'
tar -C "$repo_root/web" -zcf "$temporary_dir/$release_id.web.tar.gz" dist
cp "$repo_root/scripts/remote_build_release.sh" "$temporary_dir/remote_build_release.sh"

atlas_ssh "$remote_ssh" "mkdir -p '$remote_root/incoming' '$remote_root/scripts'"
sync_to_remote "$temporary_dir/$release_id.source.tar.gz" "$remote_root/incoming/"
sync_to_remote "$temporary_dir/$release_id.web.tar.gz" "$remote_root/incoming/"
sync_to_remote "$temporary_dir/remote_build_release.sh" "$remote_root/scripts/remote_build_release.sh"
sync_to_remote "$repo_root/scripts/postgres_backup.sh" "$remote_root/scripts/postgres_backup.sh"

atlas_ssh "$remote_ssh" "chmod 755 '$remote_root/scripts/remote_build_release.sh' '$remote_root/scripts/postgres_backup.sh' &&
ATLAS_REMOTE_ROOT='$remote_root' ATLAS_BOOTSTRAP='$bootstrap' ATLAS_CONFIG_TEMPLATE='$config_template' ATLAS_DEPLOYMENT_STAGE='$deployment_stage' '$remote_root/scripts/remote_build_release.sh' '$release_id' '$version_name' '$commit'"

echo "Verifying deployed service"
status="$(curl -fsS --max-time 8 "${remote_url%/}/api/v1/status")"
health="$(curl -fsS --max-time 8 "${remote_url%/}/health")"
echo "$status"
echo "$health"
if ! printf '%s' "$status" | jq -e --arg commit "$commit" --arg version "$version_name" '.commit == $commit and .version == $version' >/dev/null; then
  echo "Deployed commit does not match $commit" >&2
  exit 1
fi

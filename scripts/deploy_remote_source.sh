#!/usr/bin/env bash
set -euo pipefail

remote_ssh="${REMOTE_SSH:-root@10.111.201.1}"
remote_root="${REMOTE_ROOT:-/ops/atlas}"
remote_url="${REMOTE_URL:-http://10.111.201.1:7077}"
version_name="${VERSION_NAME:-dev}"
branch="${BRANCH:-$(git rev-parse --abbrev-ref HEAD)}"
git_remote="${GIT_REMOTE:-origin}"
extra_git_remote="${EXTRA_GIT_REMOTE:-github}"
rsync_rsh="${RSYNC_RSH:-ssh}"

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
  if ! command -v rsync >/dev/null 2>&1; then
    echo "rsync is required on the deployment host." >&2
    exit 1
  fi
  if ! ssh "$remote_ssh" "command -v rsync >/dev/null 2>&1"; then
    echo "rsync is required on the remote host: $remote_ssh" >&2
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

ssh "$remote_ssh" "mkdir -p '$remote_root/incoming' '$remote_root/scripts'"
sync_to_remote "$temporary_dir/$release_id.source.tar.gz" "$remote_root/incoming/"
sync_to_remote "$temporary_dir/$release_id.web.tar.gz" "$remote_root/incoming/"
sync_to_remote "$temporary_dir/remote_build_release.sh" "$remote_root/scripts/remote_build_release.sh"
sync_to_remote "$repo_root/scripts/postgres_backup.sh" "$remote_root/scripts/postgres_backup.sh"

ssh "$remote_ssh" "chmod 755 '$remote_root/scripts/remote_build_release.sh' '$remote_root/scripts/postgres_backup.sh' &&
ATLAS_REMOTE_ROOT='$remote_root' '$remote_root/scripts/remote_build_release.sh' '$release_id' '$version_name' '$commit'"

echo "Verifying deployed service"
status="$(curl -fsS --max-time 8 "${remote_url%/}/api/v1/status")"
health="$(curl -fsS --max-time 8 "${remote_url%/}/health")"
echo "$status"
echo "$health"
if [[ "$status" != *"\"commit\":\"$commit\""* ]]; then
  echo "Deployed commit does not match $commit" >&2
  exit 1
fi

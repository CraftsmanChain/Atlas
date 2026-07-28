#!/usr/bin/env bash
set -euo pipefail

remote_ssh="${REMOTE_SSH:-root@10.111.201.1}"
remote_root="${REMOTE_ROOT:-/ops/atlas}"
remote_url="${REMOTE_URL:-http://10.111.201.1:7077}"
version_name="${VERSION_NAME:-dev}"
branch="${BRANCH:-$(git rev-parse --abbrev-ref HEAD)}"
git_remote="${GIT_REMOTE:-origin}"
extra_git_remote="${EXTRA_GIT_REMOTE:-github}"

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

git push "$git_remote" "$branch"
if [[ -n "$extra_git_remote" ]] && git remote get-url "$extra_git_remote" >/dev/null 2>&1; then
  git push "$extra_git_remote" "$branch"
fi

echo "Building web assets locally without replacing current page data"
if [[ "${SKIP_NPM_INSTALL:-1}" != "1" ]]; then
  npm --prefix "$repo_root/web" install
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
scp "$temporary_dir/$release_id.source.tar.gz" "$remote_ssh:$remote_root/incoming/"
scp "$temporary_dir/$release_id.web.tar.gz" "$remote_ssh:$remote_root/incoming/"
scp "$temporary_dir/remote_build_release.sh" "$remote_ssh:$remote_root/scripts/remote_build_release.sh"
scp "$repo_root/scripts/postgres_backup.sh" "$remote_ssh:$remote_root/scripts/postgres_backup.sh"

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

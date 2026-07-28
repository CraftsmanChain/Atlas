#!/usr/bin/env bash
set -euo pipefail

release_id="${1:?release id is required}"
version_name="${2:?version is required}"
commit="${3:?commit is required}"
remote_root="${ATLAS_REMOTE_ROOT:-/ops/atlas}"
service_name="${ATLAS_SERVICE_NAME:-atlas.service}"
build_image="${ATLAS_GO_BUILD_IMAGE:-docker.m.daocloud.io/library/golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac}"

if [[ ! "$release_id" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "Invalid release id: $release_id" >&2
  exit 1
fi

incoming_dir="$remote_root/incoming"
release_dir="$remote_root/releases/$release_id"
source_archive="$incoming_dir/$release_id.source.tar.gz"
web_archive="$incoming_dir/$release_id.web.tar.gz"
backup_script="$remote_root/scripts/postgres_backup.sh"
lock_file="$remote_root/.release.lock"

exec 9>"$lock_file"
if ! flock -n 9; then
  echo "Another Atlas release is already running." >&2
  exit 1
fi

for required in "$source_archive" "$web_archive" "$backup_script"; do
  if [[ ! -f "$required" ]]; then
    echo "Required release input is missing: $required" >&2
    exit 1
  fi
done

mkdir -p "$remote_root/releases" "$remote_root/build-cache/go-build" "$remote_root/build-cache/go-mod"
if [[ -e "$release_dir" ]]; then
  echo "Release directory already exists: $release_dir" >&2
  exit 1
fi
mkdir -p "$release_dir/source" "$release_dir/output" "$release_dir/web"
tar -xzf "$source_archive" -C "$release_dir/source"
tar -xzf "$web_archive" -C "$release_dir/web"

if ! docker image inspect "$build_image" >/dev/null 2>&1; then
  echo "Pulling one-time Go build image: $build_image"
  docker pull "$build_image"
fi

build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "Building Atlas on target: version=$version_name commit=$commit"
docker run --rm \
  --volume "$release_dir/source:/src" \
  --volume "$release_dir/output:/out" \
  --volume "$remote_root/build-cache/go-build:/root/.cache/go-build" \
  --volume "$remote_root/build-cache/go-mod:/go/pkg/mod" \
  --workdir /src \
  --env CGO_ENABLED=1 \
  "$build_image" \
  bash -lc "set -euo pipefail
go build -ldflags '-X main.Version=$version_name -X main.Commit=$commit -X main.BuildTime=$build_time' -o /out/atlas-server ./cmd/server
go build -o /out/atlas-db-migrate ./cmd/dbmigrate
"

test -x "$release_dir/output/atlas-server"
test -x "$release_dir/output/atlas-db-migrate"
test -f "$release_dir/web/dist/index.html"

echo "Backing up PostgreSQL before release"
"$backup_script"

timestamp="$(date +%Y%m%d%H%M%S)"
server_backup="$remote_root/atlas-server.bak.$timestamp"
web_backup="$remote_root/web/dist.bak.$timestamp"
cp "$remote_root/atlas-server" "$server_backup"
if [[ -d "$remote_root/web/dist" ]]; then
  mv "$remote_root/web/dist" "$web_backup"
fi

rollback() {
  echo "Release verification failed; rolling back binaries and web assets" >&2
  install -m 755 "$server_backup" "$remote_root/atlas-server"
  if [[ -d "$web_backup" ]]; then
    rm -rf "$remote_root/web/dist"
    mv "$web_backup" "$remote_root/web/dist"
  fi
  systemctl restart "$service_name"
}
trap rollback ERR

install -m 755 "$release_dir/output/atlas-server" "$remote_root/atlas-server"
install -m 755 "$release_dir/output/atlas-db-migrate" "$remote_root/atlas-db-migrate"
mv "$release_dir/web/dist" "$remote_root/web/dist"
systemctl restart "$service_name"

for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if curl -fsS --max-time 3 "http://127.0.0.1:7077/health" >/dev/null; then
    break
  fi
  if [[ "$attempt" == "10" ]]; then
    false
  fi
  sleep 2
done
systemctl is-active "$service_name"
trap - ERR

rm -f "$source_archive" "$web_archive"
find "$remote_root/releases" -mindepth 1 -maxdepth 1 -type d -mtime +14 -exec rm -rf {} +
find "$remote_root" -maxdepth 1 -type f -name 'atlas-server.bak.*' -mtime +14 -delete
find "$remote_root/web" -maxdepth 1 -type d -name 'dist.bak.*' -mtime +14 -exec rm -rf {} +

echo "Atlas release completed: $release_id"

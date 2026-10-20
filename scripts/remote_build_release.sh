#!/usr/bin/env bash
set -euo pipefail

release_id="${1:?release id is required}"
version_name="${2:?version is required}"
commit="${3:?commit is required}"
remote_root="${ATLAS_REMOTE_ROOT:-/ops/atlas}"
service_name="${ATLAS_SERVICE_NAME:-atlas.service}"
build_image="${ATLAS_GO_BUILD_IMAGE:-m.daocloud.io/docker.io/library/golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac}"
go_proxy="${ATLAS_GOPROXY:-https://goproxy.cn|https://proxy.golang.org|direct}"
go_sumdb="${ATLAS_GOSUMDB:-sum.golang.google.cn}"
deployment_stage="${ATLAS_DEPLOYMENT_STAGE:-development}"
bootstrap="${ATLAS_BOOTSTRAP:-0}"
config_template="${ATLAS_CONFIG_TEMPLATE:-configs/clusters/xiaozhen-zhiyuan.yaml}"
deployment_runtime="${ATLAS_DEPLOY_RUNTIME:-systemd}"

case "$deployment_stage" in
  development)
    database_backup_enabled=0
    ;;
  production)
    database_backup_enabled=1
    ;;
  *)
    echo "ATLAS_DEPLOYMENT_STAGE must be development or production, got: $deployment_stage" >&2
    exit 1
    ;;
esac

if [[ ! "$release_id" =~ ^[A-Za-z0-9._-]+$ || "$release_id" == .* || "$release_id" == *..* ]]; then
  echo "Invalid release id: $release_id" >&2
  exit 1
fi
if [[ ! "$version_name" =~ ^[A-Za-z0-9._-]+$ || ! "$commit" =~ ^[a-f0-9]{7,40}$ ]]; then
  echo "Invalid version or commit" >&2
  exit 1
fi
if [[ ! "$remote_root" =~ ^/[A-Za-z0-9_./-]+$ || "$remote_root" == / || "$remote_root" == *..* ]]; then
  echo "Invalid application root" >&2
  exit 1
fi
if [[ "$bootstrap" != 0 && "$bootstrap" != 1 ]]; then
  echo "ATLAS_BOOTSTRAP must be 0 or 1" >&2
  exit 1
fi
if [[ "$bootstrap" == 1 && ( "$deployment_stage" != development || "$service_name" != atlas.service ) ]]; then
  echo "Bootstrap requires the standard Atlas service in development stage" >&2
  exit 1
fi
if [[ "$deployment_runtime" != systemd && "$deployment_runtime" != docker ]]; then
  echo "ATLAS_DEPLOY_RUNTIME must be systemd or docker" >&2
  exit 1
fi
for command_name in flock curl jq install systemctl; do
  command -v "$command_name" >/dev/null || { echo "Missing required command: $command_name" >&2; exit 1; }
done

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

required_inputs=("$source_archive" "$web_archive")
if [[ "$database_backup_enabled" == "1" ]]; then
  required_inputs+=("$backup_script")
fi
for required in "${required_inputs[@]}"; do
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

if [[ "$deployment_runtime" == docker ]] && ! docker image inspect "$build_image" >/dev/null 2>&1; then
  echo "Pulling one-time Go build image: $build_image"
  if ! docker pull "$build_image"; then
    if [[ "$build_image" == *@sha256:* && ( "$build_image" == docker.m.daocloud.io/* || "$build_image" == m.daocloud.io/docker.io/* ) ]]; then
      if [[ "$build_image" == m.daocloud.io/docker.io/* ]]; then
        build_image="${build_image#m.daocloud.io/}"
      else
        build_image="docker.io/${build_image#docker.m.daocloud.io/}"
      fi
      echo "Mirror unavailable; using the official registry with the identical pinned digest: $build_image"
      if ! docker image inspect "$build_image" >/dev/null 2>&1; then
        docker pull "$build_image"
      fi
    else
      exit 1
    fi
  fi
fi

build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "Building Atlas on target: version=$version_name commit=$commit"
if [[ "$deployment_runtime" == systemd ]]; then
  go_bin="${ATLAS_GO_BIN:-$remote_root/toolchains/go1.24.10/bin/go}"
  test -x "$go_bin"
  command -v gcc >/dev/null
  systemd-run --quiet --scope --unit "atlas-build-$release_id" \
    --property=CPUQuota=200% --property=MemoryMax=3G \
    env CGO_ENABLED=1 GOMAXPROCS=2 GOMEMLIMIT=2GiB GOTOOLCHAIN=local \
    "GOCACHE=$remote_root/build-cache/go-build" "GOPATH=$remote_root/build-cache" \
    "GOPROXY=$go_proxy" "GOSUMDB=$go_sumdb" \
    bash -c 'set -euo pipefail
cd "$1"
"$2" build -ldflags "$3" -o "$4/atlas-server" ./cmd/server
"$2" build -o "$4/atlas-db-migrate" ./cmd/dbmigrate
"$2" build -o "$4/hardware-health-replay" ./cmd/hardware-health-replay
"$2" build -o "$4/disk-installation-reconcile" ./cmd/disk-installation-reconcile
' _ "$release_dir/source" "$go_bin" "-X main.Version=$version_name -X main.Commit=$commit -X main.BuildTime=$build_time" "$release_dir/output"
else
docker run --rm \
  --cpus "${ATLAS_BUILD_CPUS:-2}" --memory "${ATLAS_BUILD_MEMORY:-3g}" \
  --volume "$release_dir/source:/src" \
  --volume "$release_dir/output:/out" \
  --volume "$remote_root/build-cache/go-build:/root/.cache/go-build" \
  --volume "$remote_root/build-cache/go-mod:/go/pkg/mod" \
  --workdir /src \
  --env CGO_ENABLED=1 \
  --env GOMAXPROCS=2 \
  --env "GOPROXY=$go_proxy" \
  --env "GOSUMDB=$go_sumdb" \
  "$build_image" \
  bash -c "set -euo pipefail
go build -ldflags '-X main.Version=$version_name -X main.Commit=$commit -X main.BuildTime=$build_time' -o /out/atlas-server ./cmd/server
go build -o /out/atlas-db-migrate ./cmd/dbmigrate
go build -o /out/hardware-health-replay ./cmd/hardware-health-replay
go build -o /out/disk-installation-reconcile ./cmd/disk-installation-reconcile
"
fi

test -x "$release_dir/output/atlas-server"
test -x "$release_dir/output/atlas-db-migrate"
test -x "$release_dir/output/hardware-health-replay"
test -x "$release_dir/output/disk-installation-reconcile"
test -f "$release_dir/web/dist/index.html"

if [[ "$bootstrap" == 1 ]]; then
  bash "$release_dir/source/scripts/bootstrap_remote.sh" apply "$remote_root" "$release_dir/source" "$config_template"
elif [[ ! -f "$remote_root/configs/config.yaml" ]] || [[ "$(systemctl show "$service_name" --property=LoadState --value)" != loaded ]]; then
  echo "Existing service/configuration missing; first deployment must be explicitly bootstrapped" >&2
  exit 1
fi

if [[ "$database_backup_enabled" == "1" ]]; then
  echo "Backing up PostgreSQL before production release"
  "$backup_script"
else
  echo "Skipping PostgreSQL backup for development release"
fi

timestamp="$(date +%Y%m%d%H%M%S)"
mkdir -p "$remote_root/web"
server_backup="$remote_root/atlas-server.bak.$timestamp"
replay_backup="$remote_root/hardware-health-replay.bak.$timestamp"
installation_backup="$remote_root/disk-installation-reconcile.bak.$timestamp"
web_backup="$remote_root/web/dist.bak.$timestamp"
previous_server=0
previous_replay=0
previous_installation=0
if [[ -f "$remote_root/atlas-server" ]]; then
  cp "$remote_root/atlas-server" "$server_backup"
  previous_server=1
fi
if [[ -f "$remote_root/hardware-health-replay" ]]; then
  cp "$remote_root/hardware-health-replay" "$replay_backup"
  previous_replay=1
fi
if [[ -f "$remote_root/disk-installation-reconcile" ]]; then
  cp "$remote_root/disk-installation-reconcile" "$installation_backup"
  previous_installation=1
fi

rollback() {
  trap - ERR
  echo "Release verification failed; retaining release artifacts and database" >&2
  if [[ "$previous_server" == 1 ]]; then
    install -m 755 "$server_backup" "$remote_root/atlas-server.rollback"
    mv -f "$remote_root/atlas-server.rollback" "$remote_root/atlas-server"
  fi
  if [[ "$previous_replay" == 1 ]]; then
    install -m 755 "$replay_backup" "$remote_root/hardware-health-replay.rollback"
    mv -f "$remote_root/hardware-health-replay.rollback" "$remote_root/hardware-health-replay"
  else
    rm -f "$remote_root/hardware-health-replay"
  fi
  if [[ "$previous_installation" == 1 ]]; then
    install -m 755 "$installation_backup" "$remote_root/disk-installation-reconcile.rollback"
    mv -f "$remote_root/disk-installation-reconcile.rollback" "$remote_root/disk-installation-reconcile"
  else
    rm -f "$remote_root/disk-installation-reconcile"
  fi
  if [[ -d "$web_backup" ]]; then
    if [[ -d "$remote_root/web/dist" ]]; then
      mv "$remote_root/web/dist" "$release_dir/failed-web"
    fi
    mv "$web_backup" "$remote_root/web/dist"
  fi
  if [[ "$previous_server" == 1 ]]; then
    systemctl restart "$service_name"
  else
    systemctl stop "$service_name"
    echo "First deployment failed: no previous Atlas binary exists; new Atlas stopped, files/data retained." >&2
  fi
}
trap rollback ERR

if [[ -d "$remote_root/web/dist" ]]; then
  mv "$remote_root/web/dist" "$web_backup"
fi
install -m 755 "$release_dir/output/atlas-server" "$remote_root/atlas-server.next"
mv -f "$remote_root/atlas-server.next" "$remote_root/atlas-server"
install -m 755 "$release_dir/output/atlas-db-migrate" "$remote_root/atlas-db-migrate"
install -m 755 "$release_dir/output/hardware-health-replay" "$remote_root/hardware-health-replay.next"
mv -f "$remote_root/hardware-health-replay.next" "$remote_root/hardware-health-replay"
install -m 755 "$release_dir/output/disk-installation-reconcile" "$remote_root/disk-installation-reconcile.next"
mv -f "$remote_root/disk-installation-reconcile.next" "$remote_root/disk-installation-reconcile"
mv "$release_dir/web/dist" "$remote_root/web/dist"
if [[ "$previous_server" == 1 ]]; then
  systemctl restart "$service_name"
else
  systemctl start "$service_name"
fi

for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if curl -fsS --max-time 3 "http://127.0.0.1:7077/health" >/dev/null && \
    curl -fsS --max-time 3 "http://127.0.0.1:7077/api/v1/status" | \
      jq -e --arg commit "$commit" --arg version "$version_name" '.commit == $commit and .version == $version' >/dev/null; then
    break
  fi
  if [[ "$attempt" == "10" ]]; then
    false
  fi
  sleep 2
done
systemctl is-active "$service_name"
trap - ERR

echo "Source archives, releases and rollback files retained; no automatic pruning"

echo "Atlas release completed: $release_id"

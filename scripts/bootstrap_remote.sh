#!/usr/bin/env bash
set -euo pipefail

mode="${1:?check or apply is required}"
remote_root="${2:?application root is required}"
source_dir="${3:-}"
config_template="${4:-}"

fail() { echo "First-deployment preflight: $*" >&2; exit 1; }
[[ "$mode" == check || "$mode" == apply ]] || fail "invalid mode"
[[ "$remote_root" == /ops/atlas ]] || fail "bootstrap supports /ops/atlas only"
[[ "$(id -u)" == 0 ]] || fail "root is required for systemd bootstrap"
for command_name in docker systemctl rsync flock curl jq openssl ss install; do
  command -v "$command_name" >/dev/null || fail "missing $command_name; no packages will be installed"
done
docker info >/dev/null 2>&1 || fail "Docker is not available; it will not be installed or restarted"
systemctl is-active --quiet docker.service || fail "docker.service is not active; bootstrap will not change it"
for unit in atlas.service atlas-postgres.service; do
  load_state="$(systemctl show "$unit" --property=LoadState --value)"
  [[ "$load_state" == not-found ]] || fail "$unit already exists; use the existing-service release path"
done
for path in /etc/atlas/atlas.env /etc/atlas/postgres.env \
  "$remote_root/atlas-server" "$remote_root/configs/config.yaml" "$remote_root/postgres-data"; do
  [[ ! -e "$path" && ! -L "$path" ]] || fail "$path already exists; refusing to overwrite"
done
docker container inspect atlas-postgres >/dev/null 2>&1 && fail "atlas-postgres container already exists"
listeners="$(ss -H -lnt)"
if awk '$4 ~ /:(7077|15432)$/ {found=1} END {exit !found}' <<< "$listeners"; then
  fail "port 7077 or loopback PostgreSQL port 15432 is occupied"
fi
if [[ "$mode" == check ]]; then
  echo "First-deployment preflight passed; no remote files or services changed"
  exit 0
fi

[[ "$config_template" =~ ^configs/[A-Za-z0-9_./-]+\.yaml$ && "$config_template" != *..* ]] || fail "invalid config template"
for path in "$source_dir/$config_template" "$source_dir/deploy/atlas.service" \
  "$source_dir/deploy/postgres/atlas-postgres-persistent.service"; do
  [[ -f "$path" ]] || fail "missing release template"
done
postgres_image="${ATLAS_POSTGRES_IMAGE:-postgres:17-alpine}"
if ! docker image inspect "$postgres_image" >/dev/null 2>&1; then
  docker pull "$postgres_image"
fi

# Generate independent cluster secrets remotely; never copy the old database or env.
umask 077
install -d -m 700 /etc/atlas "$remote_root/postgres-data"
install -d -m 755 "$remote_root/configs" "$remote_root/web" "$remote_root/logs" "$remote_root/training"
database_password="$(openssl rand -hex 32)"
master_key="$(openssl rand -base64 32)"
admin_token="$(openssl rand -hex 32)"
printf 'POSTGRES_DB=atlas\nPOSTGRES_USER=atlas\nPOSTGRES_PASSWORD=%s\nATLAS_PG_BACKUP_DIR=/ops/atlas/postgres-backups\n' "$database_password" > /etc/atlas/postgres.env
printf 'ATLAS_DATABASE_DRIVER=postgres\nATLAS_DATABASE_DSN="host=127.0.0.1 port=15432 user=atlas password=%s dbname=atlas sslmode=disable"\nATLAS_NODE_CREDENTIAL_MASTER_KEY=%s\nATLAS_PLATFORM_SECRET_MASTER_KEY=%s\nATLAS_NODE_CREDENTIAL_ADMIN_TOKEN=%s\n' \
  "$database_password" "$master_key" "$master_key" "$admin_token" > /etc/atlas/atlas.env
unset database_password master_key admin_token
install -m 600 "$source_dir/$config_template" "$remote_root/configs/config.yaml"

docker create --name atlas-postgres --env-file /etc/atlas/postgres.env \
  --publish 127.0.0.1:15432:5432 \
  --volume "$remote_root/postgres-data:/var/lib/postgresql/data" \
  --health-cmd 'pg_isready -U atlas -d atlas' --health-interval 10s --health-timeout 5s --health-retries 6 \
  "$postgres_image" >/dev/null
install -m 644 "$source_dir/deploy/postgres/atlas-postgres-persistent.service" /etc/systemd/system/atlas-postgres.service
install -m 644 "$source_dir/deploy/atlas.service" /etc/systemd/system/atlas.service
systemctl daemon-reload
systemctl enable atlas-postgres.service atlas.service
systemctl start atlas-postgres.service
for attempt in {1..30}; do
  if docker exec atlas-postgres pg_isready -U atlas -d atlas >/dev/null 2>&1; then
    echo "New independent PostgreSQL ready; Atlas will start after release files are installed"
    exit 0
  fi
  sleep 2
done
fail "new PostgreSQL did not become ready; generated configuration/data are retained for investigation"

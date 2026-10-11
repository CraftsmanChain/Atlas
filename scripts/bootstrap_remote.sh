#!/usr/bin/env bash
set -euo pipefail

mode="${1:?check or apply is required}"
remote_root="${2:?application root is required}"
source_dir="${3:-}"
config_template="${4:-}"
deployment_runtime="${ATLAS_DEPLOY_RUNTIME:-systemd}"

fail() { echo "First-deployment preflight: $*" >&2; exit 1; }
[[ "$mode" == check || "$mode" == apply ]] || fail "invalid mode"
[[ "$remote_root" == /ops/atlas ]] || fail "bootstrap supports /ops/atlas only"
[[ "$(id -u)" == 0 ]] || fail "root is required for systemd bootstrap"
for command_name in systemctl rsync flock curl jq openssl ss install; do
  command -v "$command_name" >/dev/null || fail "missing $command_name; no packages will be installed"
done
case "$deployment_runtime" in
  docker)
    docker info >/dev/null 2>&1 || fail "Docker is not available; it will not be installed or restarted"
    systemctl is-active --quiet docker.service || fail "docker.service is not active; bootstrap will not change it"
    docker container inspect atlas-postgres >/dev/null 2>&1 && fail "atlas-postgres container already exists"
    ;;
  systemd)
    [[ -x /usr/lib/postgresql/17/bin/initdb ]] || fail "native PostgreSQL 17 is missing"
    command -v runuser >/dev/null || fail "runuser is missing"
    getent passwd postgres >/dev/null || fail "postgres system account is missing"
    ;;
  *) fail "invalid deployment runtime" ;;
esac
for unit in atlas.service atlas-postgres.service; do
  load_state="$(systemctl show "$unit" --property=LoadState --value)"
  [[ "$load_state" == not-found ]] || fail "$unit already exists; use the existing-service release path"
done
for path in /etc/atlas/atlas.env /etc/atlas/postgres.env \
  "$remote_root/atlas-server" "$remote_root/configs/config.yaml" "$remote_root/postgres-data"; do
  [[ ! -e "$path" && ! -L "$path" ]] || fail "$path already exists; refusing to overwrite"
done
listeners="$(ss -H -lnt)"
if awk '$4 ~ /:(7077|15432)$/ {found=1} END {exit !found}' <<< "$listeners"; then
  fail "port 7077 or loopback PostgreSQL port 15432 is occupied"
fi
if [[ "$mode" == check ]]; then
  echo "First-deployment preflight passed; no remote files or services changed"
  exit 0
fi

[[ "$config_template" =~ ^configs/[A-Za-z0-9_./-]+\.yaml$ && "$config_template" != *..* ]] || fail "invalid config template"
postgres_unit="$source_dir/deploy/postgres/atlas-postgres-native.service"
if [[ "$deployment_runtime" == docker ]]; then
  postgres_unit="$source_dir/deploy/postgres/atlas-postgres-persistent.service"
fi
for path in "$source_dir/$config_template" "$source_dir/deploy/atlas.service" "$postgres_unit"; do
  [[ -f "$path" ]] || fail "missing release template"
done
postgres_image="${ATLAS_POSTGRES_IMAGE:-m.daocloud.io/docker.io/library/postgres:17-alpine}"
if [[ "$deployment_runtime" == docker ]] && ! docker image inspect "$postgres_image" >/dev/null 2>&1; then
  docker pull "$postgres_image"
fi

# Generate independent cluster secrets remotely; never copy the old database or env.
umask 077
install -d -m 700 /etc/atlas "$remote_root/postgres-data"
install -d -m 755 "$remote_root/configs" "$remote_root/web" "$remote_root/logs" "$remote_root/training"
database_password="$(openssl rand -hex 32)"
master_key="$(openssl rand -base64 32)"
admin_token="$(openssl rand -hex 32)"
if [[ "$deployment_runtime" == docker ]]; then
  printf 'POSTGRES_DB=atlas\nPOSTGRES_USER=atlas\nPOSTGRES_PASSWORD=%s\nATLAS_PG_BACKUP_DIR=/ops/atlas/postgres-backups\n' "$database_password" > /etc/atlas/postgres.env
else
  printf 'POSTGRES_DB=atlas\nPOSTGRES_USER=postgres\nATLAS_PG_RUNTIME=systemd\nATLAS_PG_SOCKET=/run/atlas-postgres\nATLAS_PG_PORT=15432\nATLAS_PG_BACKUP_DIR=/ops/atlas/postgres-backups\n' > /etc/atlas/postgres.env
fi
printf 'ATLAS_DATABASE_DRIVER=postgres\nATLAS_DATABASE_DSN="host=127.0.0.1 port=15432 user=atlas password=%s dbname=atlas sslmode=disable"\nATLAS_NODE_CREDENTIAL_MASTER_KEY=%s\nATLAS_PLATFORM_SECRET_MASTER_KEY=%s\nATLAS_NODE_CREDENTIAL_ADMIN_TOKEN=%s\n' \
  "$database_password" "$master_key" "$master_key" "$admin_token" > /etc/atlas/atlas.env
unset master_key admin_token
install -m 600 "$source_dir/$config_template" "$remote_root/configs/config.yaml"

if [[ "$deployment_runtime" == docker ]]; then
  docker create --name atlas-postgres --env-file /etc/atlas/postgres.env \
  --publish 127.0.0.1:15432:5432 \
  --volume "$remote_root/postgres-data:/var/lib/postgresql/data" \
  --health-cmd 'pg_isready -U atlas -d atlas' --health-interval 10s --health-timeout 5s --health-retries 6 \
  "$postgres_image" >/dev/null
else
  chown postgres:postgres "$remote_root/postgres-data"
  runuser -u postgres -- /usr/lib/postgresql/17/bin/initdb -D "$remote_root/postgres-data" \
    --encoding=UTF8 --locale=C.UTF-8 --auth-local=peer --auth-host=scram-sha-256 --no-instructions >/dev/null
fi
install -m 644 "$postgres_unit" /etc/systemd/system/atlas-postgres.service
install -m 644 "$source_dir/deploy/atlas.service" /etc/systemd/system/atlas.service
systemctl daemon-reload
systemctl enable atlas-postgres.service atlas.service
systemctl start atlas-postgres.service
for attempt in {1..30}; do
  ready=0
  if [[ "$deployment_runtime" == docker ]]; then
    docker exec atlas-postgres pg_isready -U atlas -d atlas >/dev/null 2>&1 && ready=1
  else
    runuser -u postgres -- /usr/lib/postgresql/17/bin/pg_isready -h /run/atlas-postgres -p 15432 -U postgres -d postgres >/dev/null 2>&1 && ready=1
  fi
  if [[ "$ready" == 1 ]]; then
    if [[ "$deployment_runtime" == systemd ]]; then
      printf "CREATE ROLE atlas LOGIN PASSWORD '%s';\nCREATE DATABASE atlas OWNER atlas;\n" "$database_password" | \
        runuser -u postgres -- /usr/lib/postgresql/17/bin/psql -h /run/atlas-postgres -p 15432 -d postgres --no-psqlrc --set=ON_ERROR_STOP=1 >/dev/null
    fi
    unset database_password
    echo "New independent PostgreSQL ready; Atlas will start after release files are installed"
    exit 0
  fi
  sleep 2
done
fail "new PostgreSQL did not become ready; generated configuration/data are retained for investigation"

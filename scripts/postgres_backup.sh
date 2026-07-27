#!/usr/bin/env bash
set -euo pipefail

backup_dir="${ATLAS_PG_BACKUP_DIR:-/mnt/public/pg}"
retention_days="${ATLAS_PG_BACKUP_RETENTION_DAYS:-14}"
container_name="${ATLAS_POSTGRES_CONTAINER:-atlas-postgres}"
postgres_env="${ATLAS_POSTGRES_ENV:-/etc/atlas/postgres.env}"

if [[ ! "$retention_days" =~ ^[1-9][0-9]*$ ]]; then
  echo "ATLAS_PG_BACKUP_RETENTION_DAYS must be a positive integer." >&2
  exit 1
fi
if [[ ! -r "$postgres_env" ]]; then
  echo "PostgreSQL environment file is not readable: $postgres_env" >&2
  exit 1
fi

# The environment file is root-only and generated on the server. It must
# contain shell-safe POSTGRES_USER and POSTGRES_DB values.
set -a
# shellcheck disable=SC1090
source "$postgres_env"
set +a
: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"

install -d -m 0700 "$backup_dir"
exec 9>"$backup_dir/.atlas-postgres-backup.lock"
if ! flock -n 9; then
  echo "Another Atlas PostgreSQL backup is already running."
  exit 0
fi

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
final_file="$backup_dir/atlas_${timestamp}.dump"
temporary_file="$backup_dir/.atlas_${timestamp}.dump.tmp"
checksum_file="${final_file}.sha256"

cleanup() {
  rm -f "$temporary_file"
}
trap cleanup EXIT
umask 077

docker exec "$container_name" pg_dump \
  --username="$POSTGRES_USER" \
  --dbname="$POSTGRES_DB" \
  --format=custom \
  --compress=6 \
  --no-owner \
  --no-privileges >"$temporary_file"

docker exec -i "$container_name" pg_restore --list <"$temporary_file" >/dev/null
mv "$temporary_file" "$final_file"
sha256sum "$final_file" >"$checksum_file"

find "$backup_dir" -maxdepth 1 -type f \
  \( -name 'atlas_*.dump' -o -name 'atlas_*.dump.sha256' \) \
  -mmin "+$((retention_days * 24 * 60))" -delete

echo "Atlas PostgreSQL backup completed: $final_file"

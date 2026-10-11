#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

remote_root="${ATLAS_REMOTE_ROOT:-/ops/atlas}"
[[ "$remote_root" == /ops/atlas && "$(id -u)" == 0 ]] || { echo "Native preparation requires root and /ops/atlas" >&2; exit 1; }
for tool in curl jq sha256sum tar gcc gpg apt-get systemctl; do
  command -v "$tool" >/dev/null || { echo "Native preparation requires existing $tool" >&2; exit 1; }
done

go_version=go1.24.10
go_sha=dd52b974e3d9c5a7bbfb222c685806def6be5d6f7efd10f9caa9ca1fa2f47955
go_root="$remote_root/toolchains/$go_version"
go_archive="$remote_root/toolchains/incoming/$go_version.linux-amd64.tar.gz"
[[ "$(uname -m)" == x86_64 ]] || { echo "Pinned native toolchain requires x86_64" >&2; exit 1; }
if [[ ! -x "$go_root/bin/go" ]]; then
  install -d -m 755 "$remote_root/toolchains/incoming"
  if [[ ! -f "$go_archive" ]]; then
    curl -fL --connect-timeout 10 --max-time 180 -o "$go_archive" "https://go.dev/dl/$go_version.linux-amd64.tar.gz"
  fi
  printf '%s  %s\n' "$go_sha" "$go_archive" | sha256sum --check --status
  [[ ! -e "$go_root" ]] || { echo "Toolchain destination exists but is incomplete; retained for investigation" >&2; exit 1; }
  install -d -m 755 "$go_root"
  tar -xzf "$go_archive" --strip-components=1 -C "$go_root"
fi
"$go_root/bin/go" version

if [[ -x /usr/lib/postgresql/17/bin/postgres ]]; then
  echo "PostgreSQL 17 binaries already available; no package changes"
  exit 0
fi
. /etc/os-release
[[ "$ID" == ubuntu && "$VERSION_CODENAME" == jammy ]] || { echo "Native package preparation supports Ubuntu jammy only; do not guess a distribution" >&2; exit 1; }
if command -v pg_lsclusters >/dev/null && [[ -n "$(pg_lsclusters -h)" ]]; then
  echo "Existing PostgreSQL clusters require a separate reviewed dependency installation" >&2
  exit 1
fi

install -d -m 755 "$remote_root/native-setup" /etc/postgresql-common
key_file="$remote_root/native-setup/postgresql.asc"
curl -fsS --connect-timeout 10 --max-time 30 -o "$key_file" https://www.postgresql.org/media/keys/ACCC4CF8.asc
fingerprint="$(gpg --show-keys --with-colons "$key_file" 2>/dev/null | awk -F: '$1=="fpr" {print $10; exit}')"
[[ "$fingerprint" == B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8 ]] || { echo "Unexpected PostgreSQL signing key" >&2; exit 1; }
chmod 644 "$key_file"
source_template="$remote_root/native-setup/atlas-pgdg.sources"
cluster_template="$remote_root/native-setup/createcluster.conf"
printf 'Types: deb\nURIs: https://apt.postgresql.org/pub/repos/apt\nSuites: jammy-pgdg\nComponents: main\nArchitectures: amd64\nSigned-By: %s\n' "$key_file" > "$source_template"
# Only the dedicated Atlas instance is created; package post-install must not start main.
printf 'create_main_cluster = false\n' > "$cluster_template"
for pair in "$source_template:/etc/apt/sources.list.d/atlas-pgdg.sources" "$cluster_template:/etc/postgresql-common/createcluster.conf"; do
  template="${pair%%:*}"
  destination="${pair#*:}"
  if [[ -e "$destination" || -L "$destination" ]]; then
    cmp -s "$template" "$destination" || { echo "Existing PostgreSQL setup differs: $destination; human review required" >&2; exit 1; }
  else
    install -m 644 "$template" "$destination"
  fi
done
apt_options=(-o Acquire::https::Timeout=20 -o Acquire::http::Timeout=20 -o Acquire::Retries=1)
apt-get "${apt_options[@]}" update
preview="$remote_root/native-setup/postgresql-install-preview.txt"
apt-get "${apt_options[@]}" --simulate --no-install-recommends --no-upgrade install postgresql-17 postgresql-client-17 > "$preview"
cat "$preview"
if ! grep -Eq '^0 upgraded, [0-9]+ newly installed, 0 to remove' "$preview"; then
  echo "Package preview would upgrade/remove existing components; human review required, installation not executed" >&2
  exit 1
fi
DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get "${apt_options[@]}" \
  -o Dpkg::Options::=--force-confold --no-install-recommends --no-upgrade --yes install postgresql-17 postgresql-client-17
/usr/lib/postgresql/17/bin/postgres --version
echo "Native dependencies ready; no Docker or host reboot used"

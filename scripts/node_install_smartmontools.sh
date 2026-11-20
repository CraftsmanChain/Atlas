#!/usr/bin/env bash
# Install smartmontools (only; no recommends) on fleet nodes for READ-ONLY SMART use.  2026-11-20.
# User authorization 2026-11-20 ("允许安装"). Per node:
#   - skip if smartctl already present
#   - apt-get install --no-install-recommends smartmontools from the local mirror (10.80.32.232)
#     with needrestart in list-only mode (no service restarts)
#   - stop + disable smartd (no background polling / self-tests); collector calls smartctl itself
#   - verify smartctl --version
# Usage: node_install_smartmontools.sh <ip-list-file> [parallel]
# Output: /data/atlas-research/node-hw-install/<ts>/result.tsv  (ip, rc, status, detail)
set -uo pipefail
LIST="$1"; PAR="${2:-8}"
TS="$(TZ=Asia/Shanghai date +%Y%m%dT%H%M%S)"
OUT="/data/atlas-research/node-hw-install/$TS"
mkdir -p "$OUT/log"
DEB="${ATLAS_SMART_DEB:-/ops/atlas/pkg/smartmontools_7.2-1ubuntu0.1_amd64.deb}"
DEB_SHA=14be951157b8c2924b4044f58a68eb1472f35bc65698c3a9d721a9cc161ca671
export OUT DEB DEB_SHA
one() {
  local n=$1
  timeout 300 ssh -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new "root@$n" 'bash -s' > "$OUT/log/$n.txt" 2>&1 <<'REMOTE'
if command -v smartctl >/dev/null 2>&1; then echo "RESULT already_present $(smartctl --version | head -1 | cut -d" " -f2)"; exit 0; fi
for i in $(seq 1 30); do fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1 || break; sleep 5; done
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l NEEDRESTART_SUSPEND=1
timeout 200 apt-get install -y -q --no-install-recommends smartmontools || { echo "RESULT apt_failed"; exit 3; }
systemctl disable --now smartmontools.service >/dev/null 2>&1
pgrep -x smartd >/dev/null && { echo "RESULT smartd_still_running"; exit 4; }
v=$(smartctl --version 2>/dev/null | head -1 | cut -d" " -f2)
[ -n "$v" ] && echo "RESULT installed $v" || { echo "RESULT verify_failed"; exit 5; }
REMOTE
  local rc=$?
  # Fallback for nodes whose apt mirror lacks the package: copy the identical .deb from the
  # deploy node (sha256 pinned) and dpkg -i it (deps are base libs already present on 22.04).
  if grep -q '^RESULT apt_failed' "$OUT/log/$n.txt" && [ -f "$DEB" ]; then
    if timeout 60 scp -q -o BatchMode=yes -o ConnectTimeout=8 "$DEB" "root@$n:/tmp/smartmontools_atlas.deb" >> "$OUT/log/$n.txt" 2>&1; then
      timeout 120 ssh -o BatchMode=yes -o ConnectTimeout=8 "root@$n" "bash -s" >> "$OUT/log/$n.txt" 2>&1 <<REMOTE2
echo "$DEB_SHA  /tmp/smartmontools_atlas.deb" | sha256sum -c --quiet || { echo "RESULT deb_sha_mismatch"; exit 6; }
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l NEEDRESTART_SUSPEND=1
dpkg -i /tmp/smartmontools_atlas.deb || { echo "RESULT dpkg_failed"; rm -f /tmp/smartmontools_atlas.deb; exit 7; }
rm -f /tmp/smartmontools_atlas.deb
systemctl disable --now smartmontools.service >/dev/null 2>&1
v=\$(smartctl --version 2>/dev/null | head -1 | cut -d" " -f2)
[ -n "\$v" ] && echo "RESULT installed_deb \$v" || echo "RESULT verify_failed"
REMOTE2
      rc=$?
    fi
  fi
  local r; r=$(grep -a '^RESULT ' "$OUT/log/$n.txt" | tail -1 | cut -d' ' -f2-)
  if [ -z "$r" ]; then
    if grep -qE "Permission denied" "$OUT/log/$n.txt"; then r="ssh_auth_failed"
    elif grep -qE "timed out|No route|refused|unreachable" "$OUT/log/$n.txt"; then r="ssh_unreachable"
    elif [ $rc = 124 ]; then r="timeout"; else r="unknown_rc$rc"; fi
  fi
  printf '%s\t%s\t%s\n' "$n" "$rc" "$r" >> "$OUT/result.tsv"
}
export -f one
grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' "$LIST" | xargs -P "$PAR" -I{} bash -c 'one "$1"' _ {}
echo "$OUT"; cut -f3 "$OUT/result.tsv" | cut -d' ' -f1 | sort | uniq -c

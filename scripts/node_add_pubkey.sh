#!/usr/bin/env bash
# Add the deploy node's root public key (/root/.ssh/id_rsa.pub) to nodes that reject it.  2026-11-21.
# Run INTERACTIVELY on the deploy node (10.80.192.232) as root. The password is read with `read -s`,
# kept only in this shell's environment for sshpass, never written to disk, argv or logs.
# Only appends one line to ~/.ssh/authorized_keys on each node (ssh-copy-id skips if already present).
# Usage: node_add_pubkey.sh [ip-list]   (default /ops/atlas/reports/node-hw/auth-failed-nodes.txt)
#        SSH_USER=root PAR=10 node_add_pubkey.sh
set -uo pipefail
LIST="${1:-/ops/atlas/reports/node-hw/auth-failed-nodes.txt}"
USER_="${SSH_USER:-root}"; PAR="${PAR:-10}"
KEY=/root/.ssh/id_rsa.pub
OUT="/data/atlas-research/node-hw-install/pubkey-$(TZ=Asia/Shanghai date +%Y%m%dT%H%M%S)"
mkdir -p "$OUT"
command -v sshpass >/dev/null || { echo "sshpass not installed" >&2; exit 1; }
read -r -s -p "Password for ${USER_}@<nodes> (input hidden): " SSHPASS; echo
export SSHPASS KEY USER_ OUT
one() {
  local n=$1 r
  timeout 40 sshpass -e ssh-copy-id -i "$KEY" -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new \
    -o PreferredAuthentications=password,keyboard-interactive -o PubkeyAuthentication=no "${USER_}@$n" > "$OUT/$n.log" 2>&1
  r=$?
  if timeout 15 ssh -o BatchMode=yes -o ConnectTimeout=8 "${USER_}@$n" true 2>/dev/null; then s=key_ok
  elif grep -q "Permission denied" "$OUT/$n.log"; then s=password_rejected
  elif grep -qE "timed out|No route|refused" "$OUT/$n.log"; then s=unreachable
  else s="failed_rc$r"; fi
  printf '%s\t%s\n' "$n" "$s" >> "$OUT/result.tsv"
}
export -f one
grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' "$LIST" | xargs -P "$PAR" -I{} bash -c 'one "$1"' _ {}
unset SSHPASS
echo "result: $OUT/result.tsv"; cut -f2 "$OUT/result.tsv" | sort | uniq -c

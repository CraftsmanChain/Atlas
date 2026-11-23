#!/usr/bin/env bash
# Add the deploy node's root public key (/root/.ssh/id_rsa.pub) to root's authorized_keys on nodes that reject it.
# 2026-11-21; 2026-11-23: per-line login user (sudo path for nodes with PermitRootLogin prohibit-password).
# Run INTERACTIVELY on the deploy node (10.80.192.232) as root. The password is read with `read -s`,
# kept only in this shell's environment for sshpass, never written to disk, argv or logs.
# Only appends one line to /root/.ssh/authorized_keys on each node (skipped if already present);
# no service is restarted, sshd config is not touched.
# List format: one node per line, "ip" or "ip<space|tab>user". user=root → ssh-copy-id as root;
# any other user → password login as that user, then `sudo -S` (same password) appends the key for root.
# Usage: node_add_pubkey.sh [list]   (default /ops/atlas/reports/node-hw/auth-failed-nodes.txt)
#        PAR=10 node_add_pubkey.sh /ops/atlas/reports/node-hw/pubkey-b-2026-11-23.txt
set -uo pipefail
LIST="${1:-/ops/atlas/reports/node-hw/auth-failed-nodes.txt}"
PAR="${PAR:-10}"
KEY=/root/.ssh/id_rsa.pub
OUT="/data/atlas-research/node-hw-install/pubkey-$(TZ=Asia/Shanghai date +%Y%m%dT%H%M%S)"
mkdir -p "$OUT"
command -v sshpass >/dev/null || { echo "sshpass not installed" >&2; exit 1; }
[ -s "$KEY" ] || { echo "missing $KEY" >&2; exit 1; }
read -r -s -p "Password for the login user on <nodes> (input hidden): " SSHPASS; echo
KEYB64="$(base64 -w0 < "$KEY")"
export SSHPASS KEY KEYB64 OUT
one() {
  local n=$1 u=${2:-root} r s
  local o=(-o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new -o PreferredAuthentications=password,keyboard-interactive -o PubkeyAuthentication=no)
  if [ "$u" = root ]; then
    timeout 40 sshpass -e ssh-copy-id -i "$KEY" "${o[@]}" "root@$n" > "$OUT/$n.log" 2>&1
  else
    # sudo reads the password from the first stdin line; the key (public) is passed base64 in the command.
    printf '%s\n' "$SSHPASS" | timeout 40 sshpass -e ssh "${o[@]}" "$u@$n" \
      "sudo -S -p '' sh -c 'umask 077; mkdir -p /root/.ssh; touch /root/.ssh/authorized_keys; k=\$(echo $KEYB64 | base64 -d); grep -qxF \"\$k\" /root/.ssh/authorized_keys || echo \"\$k\" >> /root/.ssh/authorized_keys; chmod 700 /root/.ssh; chmod 600 /root/.ssh/authorized_keys; echo appended_or_present'" \
      > "$OUT/$n.log" 2>&1
  fi
  r=$?
  if timeout 15 ssh -o BatchMode=yes -o ConnectTimeout=8 "root@$n" true 2>/dev/null; then s=key_ok
  elif grep -qiE "Permission denied|incorrect password|not in the sudoers" "$OUT/$n.log"; then s=password_or_sudo_rejected
  elif grep -qE "timed out|No route|refused" "$OUT/$n.log"; then s=unreachable
  else s="failed_rc$r"; fi
  printf '%s\t%s\t%s\n' "$n" "$u" "$s" >> "$OUT/result.tsv"
}
export -f one
grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+([[:space:]]+[A-Za-z_][A-Za-z0-9_-]*)?[[:space:]]*$' "$LIST" \
  | xargs -P "$PAR" -L1 bash -c 'one "$@"' _
unset SSHPASS
echo "result: $OUT/result.tsv"; cut -f3 "$OUT/result.tsv" | sort | uniq -c

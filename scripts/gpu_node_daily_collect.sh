#!/usr/bin/env bash
# GPU node daily read-only collector (plan v5 S1).
#
# Runs on the Atlas deploy node. For every node of the newest frozen GPU risk
# query it opens one SSH session and runs READ-ONLY commands only (grep/awk on
# kernel logs, ipmitool sel elist, lspci -vvv). Nothing on the node is changed:
# no reboot, no BMC/SEL clear, no driver or config change. Kernel logs are
# filtered and aggregated on the node (a single kern.log can reach GBs).
#
# Output: $ATLAS_NODE_COLLECT_ROOT/<YYYY-MM-DD>/
#   nodes/<ip>.txt   per-node filtered text (sections ##XID ##GHES ##HWLINES ##SEL ##AER)
#   xid.tsv          ip, syslog local timestamp ("Nov 10 09:22:26", CST), pci, xid code, lines
#   ghes.tsv         ip, day, severity, section, device, count
#   _status          ip, ssh exit code
#   SHA256SUMS
# Then imports kernel critical XIDs into the core-event ledger unless
# ATLAS_NODE_COLLECT_IMPORT=0.
#
# Usage: gpu_node_daily_collect.sh [days]   (days of kernel log to read, default 5)
set -euo pipefail
DAYS="${1:-5}"
ROOT="${ATLAS_NODE_COLLECT_ROOT:-/data/atlas-research/node-daily}"
PAR="${ATLAS_NODE_COLLECT_PARALLEL:-24}"
ENV_FILE="${ATLAS_ENV_FILE:-/etc/atlas/atlas.env}"
GPU_RISK="${ATLAS_GPU_RISK_BIN:-/ops/atlas/gpu-risk}"
PSQL="${ATLAS_PSQL:-/usr/lib/postgresql/17/bin/psql}"
DAY="$(TZ=Asia/Shanghai date +%F)"
OUT="$ROOT/$DAY"
mkdir -p "$OUT/nodes"
chmod 700 "$ROOT"
exec 9>"$ROOT/.lock"
flock -n 9 || { echo "another collection is running" >&2; exit 0; }

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a
# libpq environment instead of argv so the password never shows in ps.
for kv in $ATLAS_DATABASE_DSN; do
  case "$kv" in
    host=*) export PGHOST="${kv#host=}" ;; port=*) export PGPORT="${kv#port=}" ;;
    user=*) export PGUSER="${kv#user=}" ;; password=*) export PGPASSWORD="${kv#password=}" ;;
    dbname=*) export PGDATABASE="${kv#dbname=}" ;; sslmode=*) export PGSSLMODE="${kv#sslmode=}" ;;
  esac
done
[ -n "${PGPASSWORD:-}" ] && [ -n "${PGDATABASE:-}" ] || { echo "cannot parse ATLAS_DATABASE_DSN (key=value form expected)" >&2; exit 1; }
"$PSQL" -Atc "select distinct m.node_ip from gpu_risk_members m where m.query_id=(select id from gpu_risk_queries where status='completed' order by cutoff desc, id desc limit 1) and m.node_ip<>'' order by 1" > "$OUT/nodes.txt"

# Syslog day prefixes ("Nov 15", "Nov  5") for the last $DAYS days, CST.
PREFIXES="$(for i in $(seq 0 $((DAYS - 1))); do TZ=Asia/Shanghai LC_ALL=C date -d "-$i day" '+%b %e'; done | paste -sd'|')"
YEAR="$(TZ=Asia/Shanghai date +%Y)"
ALL_LOGS=0
[ "$DAYS" -gt 12 ] && ALL_LOGS=1
export OUT PREFIXES YEAR ALL_LOGS

one() {
  local n=$1
  # The remote program is static text; PREFIXES is passed as an argument.
  timeout 180 ssh -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new "root@$n" "bash -s -- '$PREFIXES' '$ALL_LOGS'" > "$OUT/nodes/$n.txt" 2>&1 <<'REMOTE'
P="$1"
if [ "$2" = 1 ]; then cat_logs() { zcat -f /var/log/kern.log* 2>/dev/null; }; else cat_logs() { cat /var/log/kern.log /var/log/kern.log.1 2>/dev/null; }; fi
filt() { cat_logs | LC_ALL=C awk -v p="$P" 'BEGIN{n=split(p,a,"|"); for(i=1;i<=n;i++) k[a[i]]=1} k[substr($0,1,6)]'; }
echo "##XID"
# One row per (day, pci, code): earliest timestamp and line count; no truncation.
filt | LC_ALL=C grep -a "NVRM: Xid" | LC_ALL=C sed -nE 's/^([A-Z][a-z]{2} +[0-9]+) ([0-9:]{8}) .*NVRM: Xid \(PCI:([0-9a-fA-F:.]+)\): ([0-9]+).*/\1\t\2\t\3\t\4/p' | LC_ALL=C awk -F'\t' '{k=$1"\t"$3"\t"$4; n[k]++; if(!(k in t) || $2<t[k]) t[k]=$2} END{for(k in n){split(k,a,"\t"); print a[1]" "t[k]"\t"a[2]"\t"a[3]"\t"n[k]}}'
echo "##COVER"
# oldest line actually read (first line of the oldest file in the read set)
if [ "$2" = 1 ]; then oldest=$(ls -1v /var/log/kern.log* 2>/dev/null | tail -1); else oldest=$(ls -1v /var/log/kern.log /var/log/kern.log.1 2>/dev/null | tail -1); fi
[ -n "$oldest" ] && zcat -f "$oldest" 2>/dev/null | head -1 | LC_ALL=C grep -aoE '^[A-Z][a-z]{2} +[0-9]+ [0-9:]{8}'
echo "##GHES"
filt | LC_ALL=C grep -a "\[Hardware Error\]:" | LC_ALL=C awk '
  /type: /&&/Error [0-9]+,/{sev=$NF; next}
  /section_type/{sec=($0 ~ /PCIe/)?"pcie":"other"; if(sec=="other"){s=$0; sub(/.*section_type: /,"",s); gsub(/\t/," ",s); k[$1" "$2"\t"sev"\t"s"\t-"]++}; next}
  /device_id/{if(sec=="pcie"){k[$1" "$2"\t"sev"\tpcie\t"$NF]++}}
  END{for(x in k) print x"\t"k[x]}'
echo "##HWLINES"
filt | LC_ALL=C grep -aE "BERT|mce:|EDAC|PCIe Bus Error|fallen off|pciehp|Link Down|link down|AER:" | LC_ALL=C grep -v "PME: Signaling" | cut -c1-300 | tail -300
echo "##SEL"
timeout 30 ipmitool sel elist last 200 2>&1 | LC_ALL=C grep -v "Session Audit" | tail -200
echo "##AER"
for d in $(lspci -D -d 10de: 2>/dev/null | awk '/VGA|3D/{print $1}'); do
  p=$(basename "$(dirname "$(readlink -f /sys/bus/pci/devices/$d)")")
  for x in $d $p; do echo "$x $(lspci -vvv -s $x 2>/dev/null | grep -E 'CESta|UESta|LnkSta:' | tr -s '\t ' ' ' | tr '\n' ';')"; done
done
REMOTE
  echo "$n $?" >> "$OUT/_status"
}
export -f one
: > "$OUT/_status"
grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' "$OUT/nodes.txt" | xargs -P "$PAR" -I{} bash -c 'one "$1"' _ {}

# Collate (syslog timestamps stay as written; the importer infers the year).
set +e +o pipefail
: > "$OUT/xid.tsv"
: > "$OUT/ghes.tsv"
for f in "$OUT"/nodes/*.txt; do
  ip=$(basename "$f" .txt)
  LC_ALL=C sed -n '/^##XID$/,/^##COVER$/p' "$f" | LC_ALL=C grep -vE '^##' | LC_ALL=C awk -F'\t' -v ip="$ip" 'NF>=4{print ip"\t"$0}' >> "$OUT/xid.tsv"
  LC_ALL=C sed -n '/^##COVER$/,/^##GHES$/p' "$f" | LC_ALL=C grep -vE '^##' | LC_ALL=C awk -v ip="$ip" 'NF>0{print ip"\t"$0"\t-\tcover\t0"}' >> "$OUT/xid.tsv"
  LC_ALL=C sed -n '/^##GHES$/,/^##HWLINES$/p' "$f" | LC_ALL=C grep -vE '^##' | LC_ALL=C awk -v ip="$ip" 'NF>0{print ip"\t"$0}' >> "$OUT/ghes.tsv"
done
set -e -o pipefail
(cd "$OUT" && sha256sum nodes.txt _status xid.tsv ghes.tsv nodes/*.txt > SHA256SUMS)
echo "collected $(wc -l < "$OUT/_status") nodes: $(cut -d' ' -f2 "$OUT/_status" | sort | uniq -c | tr '\n' ' ')"
echo "xid lines $(wc -l < "$OUT/xid.tsv"), ghes groups $(wc -l < "$OUT/ghes.tsv")"

if [ "${ATLAS_NODE_COLLECT_IMPORT:-1}" = 1 ]; then
  ATLAS_GPU_RISK_DSN="$ATLAS_DATABASE_DSN" "$GPU_RISK" core-events-kernel -input "$OUT/xid.tsv" -window-days "$DAYS"
fi

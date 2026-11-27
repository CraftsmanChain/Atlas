#!/usr/bin/env bash
# SEL rotation: archive -> import into ledger -> verify -> clear. 2026-11-27 (user decision D-M3: SEL clearing is
# now done by Atlas, not ad hoc by ops). Runs on the deploy node right after node_hw_daily_collect.sh has produced
# today's day dir (the collector calls it when ATLAS_HW_SEL_ROTATE=1). DRY RUN unless --execute is given.
#
# Guarantees (a node is cleared only if all hold):
#   1. the full current SEL (text + raw event data of every memory record) is archived on the deploy node
#      ($ARCH/<ip>/<ts>.part1.txt.gz + sha256) before anything is cleared;
#   2. every memory SEL record of that archive is present in the ledger (node-hw-ledger/mem_events.tsv);
#   3. the final listing is taken in the same SSH session immediately before `ipmitool sel clear`; memory records
#      that appeared after part1 are saved as part2 (with raw data via `sel get`) and imported after the clear;
#   4. one row per clear goes to $LEDGER/sel_reset_events.tsv (by=atlas) so windows and the page know the reset.
# Only the IPMI SEL is cleared. BMC vendor event logs (iBMC Log1, XCC EventLog, Redfish) are never touched.
#
# Usage: node_hw_sel_rotate.sh <day_dir> [--execute] [--max N] [--min-used PCT] [--only IP]
set -euo pipefail
DAY_DIR="${1:?day dir}"; shift
EXECUTE=0; MAX=20; MIN_USED=80; ONLY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --execute) EXECUTE=1 ;; --max) MAX=$2; shift ;; --min-used) MIN_USED=$2; shift ;; --only) ONLY=$2; shift ;;
    *) echo "unknown arg $1" >&2; exit 2 ;;
  esac; shift
done
SCRIPTS="${ATLAS_SCRIPTS:-/ops/atlas/scripts}"
LEDGER="${ATLAS_HW_LEDGER_ROOT:-/data/atlas-research/node-hw-ledger}"
ARCH="${ATLAS_HW_SEL_ARCHIVE:-/data/atlas-research/node-hw-sel-archive}"
TS="$(TZ=Asia/Shanghai date +%Y%m%dT%H%M%S)"
WORK="$DAY_DIR/rotate"; mkdir -p "$WORK" "$ARCH"; chmod 700 "$ARCH"
LOG="$WORK/rotate-$TS.log"
log() { echo "$(date +%T) $*" | tee -a "$LOG" >&2; }
# mawk has no strtonum: portable hex parser for SEL record ids
HEXFN='function hex(s,  i,c,v){v=0; s=tolower(s); for(i=1;i<=length(s);i++){c=index("0123456789abcdef",substr(s,i,1)); if(c==0) return -1; v=v*16+c-1}; return v}'
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new)
[ -f "$LEDGER/sel_reset_events.tsv" ] || printf 'ip\ttime\tby\tmode\tpre_entries\tpre_used_pct\tpost_entries\tmem_part1\tmem_part2\tverified\tarchive\tsha256\n' > "$LEDGER/sel_reset_events.tsv"

# ---- 1. candidates: SEL full / overflow, or Percent Used >= MIN_USED, from today's per-node SELINFO ----
sec() { LC_ALL=C awk -v s="##$2" '/^##[A-Z]+$/{on=($0==s); next} on' "$1"; }
: > "$WORK/candidates.tsv"
for gz in "$DAY_DIR"/nodes/*.txt.gz; do
  ip=$(basename "$gz" .txt.gz); [ -n "$ONLY" ] && [ "$ip" != "$ONLY" ] && continue
  info=$(zcat "$gz" | LC_ALL=C awk '/^##[A-Z]+$/{on=($0=="##SELINFO"); next} on')
  [ -z "$info" ] && continue
  used=$(echo "$info" | sed -nE 's/^Percent Used +: *([0-9]+)%.*/\1/p' | head -1)
  ent=$(echo "$info" | sed -nE 's/^Entries +: *([0-9]+).*/\1/p' | head -1)
  full=$(echo "$info" | grep -qiE "Free Space +: 0 bytes|Overflow +: true" && echo 1 || echo 0)
  if [ "$full" = 1 ] || { [ -n "$used" ] && [ "$used" -ge "$MIN_USED" ]; }; then
    printf '%s\t%s\t%s\t%s\n' "$ip" "${ent:-}" "${used:-}" "$full" >> "$WORK/candidates.tsv"
  fi
done
n=$(wc -l < "$WORK/candidates.tsv"); log "candidates: $n (min_used=$MIN_USED%, max=$MAX, execute=$EXECUTE)"
[ "$n" -eq 0 ] && exit 0
head -n "$MAX" "$WORK/candidates.tsv" > "$WORK/batch.tsv"
[ "$EXECUTE" = 1 ] || { log "dry run; would rotate:"; cat "$WORK/batch.tsv" | tee -a "$LOG" >&2; exit 0; }

# ---- 2. part1 archive: full SEL text + raw data of all memory records (no tail limits) ----
while IFS=$'\t' read -r ip ent used full; do
  d="$ARCH/$ip"; mkdir -p "$d"; f="$d/$TS.part1.txt"
  timeout 600 "${SSH[@]}" "root@$ip" 'export LC_ALL=C; echo "##SELINFO"; timeout 30 ipmitool sel info 2>&1;
    echo "##SEL"; timeout 240 ipmitool sel elist 2>&1;
    echo "##SELRAW"; timeout 300 ipmitool -v sel elist 2>/dev/null | awk -F" *: " "/^SEL Record ID/{id=\$2; st=sn=\"\"} /^ Sensor Type/{st=\$2} /^ Sensor Number/{sn=\$2} /^ Event Data/{if(st ~ /^Memory/) print id\"\t\"sn\"\t\"\$2}";
    echo "##END"' > "$f" 2>&1 || true
  if grep -q '^##END$' "$f" && [ "$(sec "$f" SEL | grep -c '|')" -gt 0 ]; then
    sha256sum "$f" | cut -d' ' -f1 > "$f.sha256"; log "$ip part1 archived: $(sec "$f" SEL | grep -c '|') records"
    printf '%s\t%s\t%s\t%s\t%s\n' "$ip" "$ent" "$used" "$full" "$f" >> "$WORK/archived.tsv"
  else
    log "$ip part1 FAILED (incomplete archive) -> not cleared"; mv "$f" "$f.failed"
  fi
done < "$WORK/batch.tsv"
[ -s "$WORK/archived.tsv" ] || { log "nothing archived"; exit 0; }

# merge an archive's SEL / SELRAW into today's sel.tsv / selraw.tsv (replacing that node's rows), then the fleet
# report re-imports into the ledger (idempotent: same ip|sel_id|date|time|kind key).
merge() { # ip file
  local ip=$1 f=$2
  LC_ALL=C awk -F'\t' -v ip="$ip" '$1!=ip' "$DAY_DIR/sel.tsv" > "$WORK/sel.tmp"
  { cat "$WORK/sel.tmp"; for x in $(ls "$ARCH/$ip"/$TS.part*.txt 2>/dev/null); do sec "$x" SEL; done | sort -u | LC_ALL=C awk -F' *\\| *' -v ip="$ip" 'NF>=5{gsub(/^ +/,"",$1); print ip"\t"$1"\t"$2"\t"$3"\t"$4"\t"$5"\t"$6}'; } > "$DAY_DIR/sel.tsv"
  LC_ALL=C awk -F'\t' -v ip="$ip" '$1!=ip' "$DAY_DIR/selraw.tsv" > "$WORK/selraw.tmp"
  { cat "$WORK/selraw.tmp"; for x in $(ls "$ARCH/$ip"/$TS.part*.txt 2>/dev/null); do sec "$x" SELRAW; done | sort -u | LC_ALL=C awk -F'\t' -v ip="$ip" 'NF>=3 && $1 !~ /^#/{print ip"\t"$0}'; } > "$DAY_DIR/selraw.tsv"
}
report() { python3 "$SCRIPTS/node_hw_fleet_report.py" "$DAY_DIR" >> "$WORK/fleet-hw-rotate.log" 2>&1; }
verify() { # ip file -> prints "<mem_records> <missing>"
  python3 - "$1" "$2" "$LEDGER/mem_events.tsv" "$SCRIPTS" <<'PY'
import re, sys
ip, f, led, scripts = sys.argv[1:5]
sys.path.insert(0, scripts)
import node_hw_ledger as L
keys = {L.mem_key(r) for r in L._read("mem_events.tsv", L.MEM_COLS)}
on, n, miss = False, 0, 0
for line in open(f, errors="replace"):
    line = line.rstrip("\n")
    if re.fullmatch(r"##[A-Z]+", line):
        on = line == "##SEL"; continue
    if not on:
        continue
    c = [x.strip() for x in line.split("|")]
    if len(c) < 5 or not c[3].startswith("Memory") or (len(c) > 5 and c[5] == "Deasserted"):
        continue
    m = re.fullmatch(r"(\d\d)/(\d\d)/(\d{4})", c[1])
    if not m:
        continue
    ev = c[4]
    kind = "ue" if "Uncorrectable ECC" in ev else "limit" if "logging limit reached" in ev else \
        "ce" if "Correctable ECC" in ev else "disabled" if "Memory Device Disabled" in ev else None
    if not kind:
        continue
    n += 1
    k = L.mem_key({"ip": ip, "sel_id": c[0], "date": "%s-%s-%s" % (m.group(3), m.group(1), m.group(2)), "time": c[2], "kind": kind})
    miss += k not in keys
print(n, miss)
PY
}

# ---- 3. import part1 and verify ----
while IFS=$'\t' read -r ip _ _ _ f; do merge "$ip" "$f"; done < "$WORK/archived.tsv"
report || { log "fleet report failed after part1 merge -> abort, nothing cleared"; exit 1; }
: > "$WORK/verified.tsv"
while IFS=$'\t' read -r ip ent used full f; do
  read -r nm miss < <(verify "$ip" "$f")
  if [ "$miss" = 0 ]; then printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$ip" "$ent" "$used" "$full" "$f" "$nm" >> "$WORK/verified.tsv"; log "$ip part1 verified ($nm memory records in ledger)"
  else log "$ip part1 NOT verified ($miss of $nm memory records missing from ledger) -> not cleared"; fi
done < "$WORK/archived.tsv"

# ---- 4. final listing + clear in one session; part2 = memory records newer than part1 (raw via sel get) ----
while IFS=$'\t' read -r ip ent used full f nm; do
  maxid=$(sec "$f" SEL | LC_ALL=C awk -F'|' "$HEXFN"' {x=$1; gsub(/ /,"",x); v=hex(x); if(v>m)m=v} END{print m+0}')
  f2="${f%.part1.txt}.part2.txt"
  timeout 300 "${SSH[@]}" "root@$ip" "bash -s -- $maxid" > "$f2" 2>&1 <<REMOTE || true
export LC_ALL=C; M=\$1
HEXFN='$HEXFN'
timeout 240 ipmitool sel elist > /tmp/.atlas_rot 2>&1 || exit 3
echo '##SEL'; awk -F'|' -v m=\$M "\$HEXFN"' {x=\$1; gsub(/ /,"",x); if(hex(x)>m) print}' /tmp/.atlas_rot
echo '##SELRAW'
awk -F'|' -v m=\$M "\$HEXFN"' {x=\$1; gsub(/ /,"",x); if(hex(x)>m && \$4 ~ /Memory/) print x}' /tmp/.atlas_rot | head -500 | while read id; do
  ipmitool sel get 0x\$id 2>/dev/null | awk -F' *: ' -v id=\$id '/^ Sensor Number/{sn=\$2} /^ Event Data/{ed=\$2} END{if(ed!="") print id"\t"sn"\t"ed}'
done
echo '##CLEAR'; timeout 60 ipmitool sel clear 2>&1; sleep 3
echo '##SELINFO'; timeout 30 ipmitool sel info 2>&1; rm -f /tmp/.atlas_rot; echo '##END'
REMOTE
  post=$(sec "$f2" SELINFO | sed -nE 's/^Entries +: *([0-9]+).*/\1/p' | head -1)
  if sec "$f2" CLEAR | grep -qiE "clearing sel|clear.*(complete|initiated)" || { [ -n "$post" ] && [ "$post" -lt "${ent:-999999}" ]; }; then
    sha=$(cat "$f.sha256"); sha256sum "$f2" | cut -d' ' -f1 > "$f2.sha256"
    np2=$(sec "$f2" SEL | grep -c 'Memory' || true)
    printf '%s\t%s\tatlas\tipmitool_inband\t%s\t%s\t%s\t%s\t%s\tpending\t%s\t%s\n' "$ip" "$(TZ=Asia/Shanghai date '+%F %T')" "$ent" "$used" "${post:-}" "$nm" "$np2" "$f" "$sha" >> "$LEDGER/sel_reset_events.tsv"
    log "$ip CLEARED (pre $ent entries / $used%, post ${post:-?}); part2 memory records: $np2"
    printf '%s\t%s\n' "$ip" "$f2" >> "$WORK/cleared.tsv"
  else
    log "$ip clear NOT confirmed (see $f2)"
  fi
done < "$WORK/verified.tsv"

# ---- 5. import part2 (archive part1 + part2 union), mark verified ----
if [ -s "$WORK/cleared.tsv" ]; then
  while IFS=$'\t' read -r ip f2; do merge "$ip" "$f2"; done < "$WORK/cleared.tsv"
  report || log "fleet report failed after part2 merge (archives kept; re-run report)"
  while IFS=$'\t' read -r ip f2; do
    read -r nm miss < <(verify "$ip" "$f2")
    st=$([ "$miss" = 0 ] && echo ok || echo "missing_$miss")
    python3 - "$LEDGER/sel_reset_events.tsv" "$ip" "$TS" "$st" <<'PY'
import sys
p, ip, ts, st = sys.argv[1:5]
rows = open(p).read().splitlines()
for i in range(len(rows) - 1, 0, -1):
    c = rows[i].split("\t")
    if c[0] == ip and c[9] == "pending" and ts in c[10]:
        c[9] = st; rows[i] = "\t".join(c); break
open(p, "w").write("\n".join(rows) + "\n")
PY
    gzip -f "$ARCH/$ip/$TS".part*.txt
    log "$ip part2 import: $st"
  done < "$WORK/cleared.tsv"
fi
log "done: cleared $( [ -s "$WORK/cleared.tsv" ] && wc -l < "$WORK/cleared.tsv" || echo 0 ) of $(wc -l < "$WORK/batch.tsv")"

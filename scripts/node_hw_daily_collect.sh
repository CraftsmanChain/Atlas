#!/usr/bin/env bash
# Fleet hardware (CPU / memory / disk) daily READ-ONLY collector.  2026-11-20.
#
# Runs on the Atlas deploy node. Fleet = infrastructure_assets present gpu_node+cpu_node
# with an IP (H200 is not in that table and is out of scope; ATLAS_HW_EXCLUDE_RE can add
# exclusions). One SSH session per node; only read-only commands run on the node:
#   ipmitool sel info / sel elist (in-band, read), storcli64 show all (if present), dmidecode -t 17, lsblk, cat /sys, /proc,
#   awk/grep on kernel logs (journalctl -k when no kern.log), smartctl -x (smartmontools installed 2026-11-20 with user approval,
#   smartd disabled). Nothing is installed, changed, cleared or rebooted.
#
# Output $ATLAS_HW_COLLECT_ROOT/<YYYY-MM-DD>/
#   nodes.tsv     ip, entity_kind, name
#   nodes/<ip>.txt raw per-node sections (##META ##TOOLS ##SELINFO ##SEL ##DIMM ##EDAC ##MEM
#                 ##KERNCNT ##KERNLINES ##DISK ##NVME ##RAID ##IOERR ##SMART ##END)
#   status.tsv    ip, kind, ssh_rc, status, flags      (one row per node; collection anomalies)
#   smart.tsv     one row per physical disk (see node_hw_smart_parse.py header)
#   storcli.tsv   ip, drive, state, media_err, other_err, pred_fail, smart_alert, sn, temp, model
#   sel.tsv       ip, sel_id, date, time, sensor, event, direction   (Session Audit dropped)
#   kern.tsv      ip, day, class, count
#   dimm.tsv      ip, locator, size, manufacturer, serial, part, speed
#   disk.tsv      ip, name, rota, tran, model, serial, size
#   followup.tsv  ip, kind, category, detail  (nodes to handle later: collect failures, missing/broken
#                 smartmontools, smartd active, SEL full/unreadable, device-access hangs)
#   summary.md    collection anomaly statistics + lists
#   SHA256SUMS
# Usage: node_hw_daily_collect.sh [days]   (days of kernel log, default 2)
set -euo pipefail
DAYS="${1:-2}"
ROOT="${ATLAS_HW_COLLECT_ROOT:-/data/atlas-research/node-hw-daily}"
PAR="${ATLAS_HW_COLLECT_PARALLEL:-32}"
ENV_FILE="${ATLAS_ENV_FILE:-/etc/atlas/atlas.env}"
PSQL="${ATLAS_PSQL:-/usr/lib/postgresql/17/bin/psql}"
EXCL="${ATLAS_HW_EXCLUDE_RE:-^$}"
REPORT_DIR="${ATLAS_HW_REPORT_DIR:-/ops/atlas/reports/node-hw}"
DAY="$(TZ=Asia/Shanghai date +%F)"
OUT="$ROOT/$DAY"
mkdir -p "$OUT/nodes" "$REPORT_DIR"
chmod 700 "$ROOT"
exec 9>"$ROOT/.lock"
flock -n 9 || { echo "another collection is running" >&2; exit 0; }
START=$(date +%s)

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a
for kv in $ATLAS_DATABASE_DSN; do
  case "$kv" in
    host=*) export PGHOST="${kv#host=}" ;; port=*) export PGPORT="${kv#port=}" ;;
    user=*) export PGUSER="${kv#user=}" ;; password=*) export PGPASSWORD="${kv#password=}" ;;
    dbname=*) export PGDATABASE="${kv#dbname=}" ;; sslmode=*) export PGSSLMODE="${kv#sslmode=}" ;;
  esac
done
PGOPTIONS="-c default_transaction_read_only=on" "$PSQL" -AtF $'\t' -c "select ip_address, entity_kind, coalesce(name,'') from infrastructure_assets where present and entity_kind in ('gpu_node','cpu_node') and ip_address ~ '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' order by 1" \
  | LC_ALL=C grep -vE "$EXCL" | LC_ALL=C grep -E "${ATLAS_HW_ONLY_RE:-.}" | sort -u -t$'\t' -k1,1 > "$OUT/nodes.tsv"

PREFIXES="$(for i in $(seq 0 $((DAYS - 1))); do TZ=Asia/Shanghai LC_ALL=C date -d "-$i day" '+%b %e'; done | paste -sd'|')"
export OUT PREFIXES DAYS

one() {
  local n=$1
  timeout 420 ssh -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new "root@$n" "bash -s -- '$PREFIXES' '$DAYS'" > "$OUT/nodes/$n.txt" 2>&1 <<'REMOTE'
P="$1"; D="$2"
export LC_ALL=C
has() { command -v "$1" >/dev/null 2>&1; }
# per-command timeout; records "#CMD_TIMEOUT name" so a hung device/BMC is visible instead of losing the node
T() { local s=$1 nm=$2; shift 2; timeout -k 5 "$s" "$@"; local r=$?; { [ $r = 124 ] || [ $r = 137 ]; } && echo "#CMD_TIMEOUT $nm"; return 0; }
echo "##META"
echo "hostname=$(hostname)"; echo "kernel=$(uname -r)"; echo "uptime_s=$(cut -d' ' -f1 /proc/uptime)"
echo "product=$(cat /sys/class/dmi/id/product_name 2>/dev/null)"; echo "vendor=$(cat /sys/class/dmi/id/sys_vendor 2>/dev/null)"
echo "loadavg=$(cut -d" " -f1-3 /proc/loadavg)"; echo "dstate_procs=$(timeout 10 ps -eo stat= 2>/dev/null | grep -c "^D")"
echo "bios=$(cat /sys/class/dmi/id/bios_version 2>/dev/null)"; echo "cpu=$(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //')"
echo "##TOOLS"
for t in ipmitool dmidecode smartctl nvme storcli storcli64 perccli64 arcconf; do has $t && echo "$t=y" || echo "$t=n"; done
[ -e /dev/ipmi0 ] || [ -e /dev/ipmi/0 ] || [ -e /dev/ipmidev/0 ] && echo "ipmidev=y" || echo "ipmidev=n"
[ -f /var/log/kern.log ] && echo "kernlog=y" || echo "kernlog=n"
echo "smartpkg=$(dpkg-query -W -f='${db:Status-Abbrev}' smartmontools 2>/dev/null | tr -d ' ')"
echo "smartd=$(systemctl is-active smartmontools 2>/dev/null)"
echo "##SELINFO"
has ipmitool && T 30 sel_info ipmitool sel info 2>&1 | head -20
echo "##SEL"
has ipmitool && T 120 sel_elist ipmitool sel elist 2>&1 | grep -v "Session Audit" | tail -5000
echo "##DIMM"
has dmidecode && T 20 dmidecode dmidecode -t 17 2>/dev/null | awk -F': ' '
  /^Memory Device/{if(l!="") print l"\t"s"\t"m"\t"sn"\t"pn"\t"sp; l=s=m=sn=pn=sp=""}
  /^\tLocator:/{l=$2} /^\tSize:/{s=$2} /^\tManufacturer:/{m=$2} /^\tSerial Number:/{sn=$2} /^\tPart Number:/{pn=$2} /^\tSpeed:/{sp=$2}
  END{if(l!="") print l"\t"s"\t"m"\t"sn"\t"pn"\t"sp}' | grep -v "No Module Installed"
echo "##EDAC"
for f in /sys/devices/system/edac/mc/mc*/dimm*/dimm_ce_count; do
  [ -f "$f" ] || continue; d=$(dirname "$f"); echo "$(cat $d/dimm_label 2>/dev/null)	ce=$(cat $f)	ue=$(cat $d/dimm_ue_count 2>/dev/null)"
done
echo "##MEM"
grep -E "^(MemTotal|HardwareCorrupted)" /proc/meminfo
if [ -f /var/log/kern.log ]; then
  klog() { timeout 90 cat /var/log/kern.log.1 /var/log/kern.log 2>/dev/null | awk -v p="$P" 'BEGIN{n=split(p,a,"|"); for(i=1;i<=n;i++) k[a[i]]=1} k[substr($0,1,6)]'; }
else
  klog() { timeout 90 journalctl -k --since "-${D} days" -o short --no-pager 2>/dev/null; }
fi
PAT='mce:|Machine check|MCE|EDAC|\[Hardware Error\]|Memory failure|soft offline|offlin(e|ing) page|Corrected error|Uncorrected|I/O error|critical medium error|Medium Error|Sense Key|blk_update_request|nvme.*(timeout|reset|I/O|failed)|ata[0-9.]+:.*(error|failed|exception)|megaraid|mpt3sas.*(fault|error)|Package temperature above|core temperature above|CPU[0-9]+: .*throttled|soft lockup|hard LOCKUP|hung_task|blocked for more than'
echo "##KERNCNT"
klog | grep -aE "$PAT" | awk '
  {d=$1" "$2; c="other"}
  /mce:|Machine check|MCE/{c="mce"} /EDAC/{c="edac"} /\[Hardware Error\]/{c="ghes"}
  /Memory failure|soft offline|offlin(e|ing) page/{c="mem_offline"}
  /I\/O error|critical medium error|Medium Error|Sense Key|blk_update_request/{c="disk_io"}
  /nvme/{c="nvme"} /ata[0-9.]+:/{c="ata"} /megaraid|mpt3sas/{c="raid"}
  /temperature above|throttled/{c="cpu_thermal"} /soft lockup|hard LOCKUP/{c="lockup"} /hung_task|blocked for more than/{c="hung"}
  {n[d"\t"c]++} END{for(k in n) print k"\t"n[k]}'
echo "##KERNLINES"
klog | grep -aE "$PAT" | grep -avE "temperature above|throttled|temperature/speed normal" | cut -c1-300 | tail -300
echo "##DISK"
T 20 lsblk lsblk -dnpP -o NAME,ROTA,TRAN,MODEL,SERIAL,SIZE -e 7,1 2>&1
echo "##NVME"
for c in /sys/class/nvme/nvme*; do [ -d "$c" ] && echo "$(basename $c)	$(cat $c/model 2>/dev/null|xargs)	$(cat $c/serial 2>/dev/null|xargs)	$(cat $c/firmware_rev 2>/dev/null|xargs)"; done
echo "##RAID"
T 20 lspci lspci 2>&1 | grep -iE "raid|sas|megaraid|storage controller|CMD_TIMEOUT" | cut -c1-200
echo "##STORCLI"
SC=$(command -v storcli64 || ls /opt/MegaRAID/storcli/storcli64 2>/dev/null | head -1)
if [ -n "$SC" ]; then T 60 storcli "$SC" /call/eall/sall show all 2>&1 | grep -aE "^Drive /c[0-9]+/e[0-9]+/s[0-9]+ :|Media Error Count|Other Error Count|Predictive Failure Count|S.M.A.R.T alert|^SN =|^Model Number|^Drive Temperature|^[0-9]+:[0-9]+ +[0-9]+ +[A-Za-z]+ |CMD_TIMEOUT"; else echo "storcli_missing"; fi
echo "##IOERR"
for b in /sys/block/sd*; do [ -f $b/device/ioerr_cnt ] && echo "$(basename $b)	ioerr=$(cat $b/device/ioerr_cnt)	iodone=$(cat $b/device/iodone_cnt 2>/dev/null)"; done
echo "##SMART"
# smartctl only reads (-x = SMART/health logs, no self-test). --scan-open also finds disks behind MegaRAID
# (sat+megaraid,N / megaraid,N). USB bridges (snt*/usb*) and USB-attached disks are skipped.
if has smartctl; then
  usb=" $(lsblk -dnpo NAME,TRAN 2>/dev/null | awk '$2=="usb"{printf "%s ", $1}')"
  s0=$SECONDS
  T 60 smartscan smartctl --scan-open 2>/dev/null | awk '$1 ~ /^\/dev/ && $2=="-d"{print $1, $3}' | while read -r dev ty; do
    # section budget: overloaded nodes (load >1000, D-state storms) make each read slow; keep what we have
    [ $((SECONDS - s0)) -gt 150 ] && { echo "#CMD_TIMEOUT smart_budget"; break; }
    case "$ty" in snt*|usb*) continue ;; esac
    case "$usb" in *" $dev "*) continue ;; esac
    echo "#dev $dev $ty"
    timeout 30 smartctl -x -j -d "$ty" "$dev" 2>/dev/null | tr -d '\n'; echo
  done
else echo "smartctl_missing"; fi
echo "##END"
REMOTE
  echo -e "$n\t$?" >> "$OUT/_rc"
}
export -f one
: > "$OUT/_rc"
cut -f1 "$OUT/nodes.tsv" | xargs -P "$PAR" -I{} bash -c 'one "$1"' _ {}

# ---------- collate + anomaly classification ----------
set +e +o pipefail
: > "$OUT/sel.tsv"; : > "$OUT/storcli.tsv"; : > "$OUT/kern.tsv"; : > "$OUT/dimm.tsv"; : > "$OUT/disk.tsv"
printf 'ip\tkind\tssh_rc\tstatus\tflags\n' > "$OUT/status.tsv"
sec() { LC_ALL=C awk -v s="##$2" '/^##[A-Z]+$/{on=($0==s); next} on' "$1"; }
while IFS=$'\t' read -r ip kind name; do
  f="$OUT/nodes/$ip.txt"; rc=$(awk -F'\t' -v ip="$ip" '$1==ip{print $2}' "$OUT/_rc" | tail -1)
  status=ok; flags=""
  if ! grep -q '^##META$' "$f" 2>/dev/null; then
    if grep -qE "Permission denied|Host key verification failed|Too many authentication" "$f"; then status=ssh_auth_failed
    elif grep -qE "timed out|No route to host|Connection refused|Could not resolve|Network is unreachable|closed by remote host" "$f" && ! grep -q '^##META$' "$f"; then status=ssh_unreachable
    elif [ "$rc" = 124 ]; then status=timeout
    else status=incomplete_rc$rc; fi
  else
    # session reached the node: partial output is still collated; status records where it stopped
    if ! grep -q '^##END$' "$f"; then
      last=$(grep -E '^##[A-Z]+$' "$f" | tail -1 | tr -d '#' | tr 'A-Z' 'a-z')
      if [ "$rc" = 124 ]; then status=partial_timeout; else status=partial_rc$rc; fi
      flags="$flags,stopped_in:$last"
    fi
    t() { sec "$f" TOOLS | grep -q "^$1=y"; }
    tn() { sec "$f" TOOLS | grep -q "^$1=n"; }
    tn ipmitool && flags="$flags,no_ipmitool"
    tn ipmidev && flags="$flags,no_ipmi_dev"
    if t ipmitool && t ipmidev; then
      sec "$f" SELINFO | grep -qiE "error|unable|could not|timeout" && flags="$flags,sel_read_error"
      sec "$f" SELINFO | grep -qiE "Free Space +: 0 bytes|Overflow +: true" && flags="$flags,sel_full"
      [ -z "$(sec "$f" SELINFO | grep -v CMD_TIMEOUT)" ] && flags="$flags,sel_no_output"
    fi
    for c in $(grep -oE '^#CMD_TIMEOUT [a-z_0-9]+' "$f" | awk '{print $2}' | sort -u); do flags="$flags,cmd_timeout:$c"; done
    tn dmidecode && flags="$flags,no_dmidecode"
    { grep -q "^##DIMM$" "$f" && grep -q "^##EDAC$" "$f" && [ -z "$(sec "$f" DIMM)" ]; } && flags="$flags,no_dimm_inventory"
    tn kernlog && flags="$flags,no_kernlog(journal)"
    tn smartctl && flags="$flags,no_smartctl"
    sp=$(sec "$f" TOOLS | sed -n 's/^smartpkg=//p'); { [ -n "$sp" ] && [ "$sp" != ii ]; } && flags="$flags,smartmontools_pkg_$sp"
    sec "$f" TOOLS | grep -q '^smartd=active' && flags="$flags,smartd_active"
  fi
  printf '%s\t%s\t%s\t%s\t%s\n' "$ip" "$kind" "${rc:-NA}" "$status" "${flags#,}" >> "$OUT/status.tsv"
  case "$status" in ok|partial_*) ;; *) continue ;; esac
  sec "$f" SEL | LC_ALL=C awk -F' *\\| *' -v ip="$ip" 'NF>=5{gsub(/^ +/,"",$1); d=$6; print ip"\t"$1"\t"$2"\t"$3"\t"$4"\t"$5"\t"d}' >> "$OUT/sel.tsv"
  sec "$f" STORCLI | LC_ALL=C awk -v ip="$ip" '
    function out(){ if(d!="") print ip"\t"d"\t"st"\t"me"\t"oe"\t"pf"\t"sa"\t"sn"\t"tp"\t"md }
    /^Drive \/c/{out(); d=$2; st=me=oe=pf=sa=sn=tp=md=""; next}
    /^[0-9]+:[0-9]+ /{st=$3}
    /Media Error Count/{me=$NF} /Other Error Count/{oe=$NF} /Predictive Failure Count/{pf=$NF}
    /S.M.A.R.T alert/{sa=$NF} /^SN =/{sn=$3} /^Drive Temperature/{tp=$4}
    /^Model Number/{m=$0; sub(/^Model Number = */,"",m); gsub(/ +/," ",m); md=m}
    END{out()}' >> "$OUT/storcli.tsv"
  sec "$f" KERNCNT | LC_ALL=C awk -v ip="$ip" 'NF>0{print ip"\t"$0}' >> "$OUT/kern.tsv"
  sec "$f" DIMM | LC_ALL=C awk -v ip="$ip" 'NF>0{print ip"\t"$0}' >> "$OUT/dimm.tsv"
  sec "$f" DISK | LC_ALL=C sed -nE 's/^NAME="([^"]*)" ROTA="([^"]*)" TRAN="([^"]*)" MODEL="([^"]*)" SERIAL="([^"]*)" SIZE="([^"]*)".*/\1\t\2\t\3\t\4\t\5\t\6/p' | LC_ALL=C awk -v ip="$ip" '{print ip"\t"$0}' >> "$OUT/disk.tsv"
done < "$OUT/nodes.tsv"

# SMART -> smart.tsv (python3 parser on the deploy node)
python3 /ops/atlas/scripts/node_hw_smart_parse.py "$OUT" > "$OUT/smart.tsv" 2> "$OUT/smart_parse.err"

# Per-node SEL span (device-resident history; may pre-date monitoring) + full/stopped state.
LC_ALL=C awk -F'\t' '$3 ~ /^[0-9][0-9]\/[0-9][0-9]\/[0-9][0-9][0-9][0-9]$/ && substr($3,7,4)>="2015" {split($3,a,"/"); d=a[3]"-"a[1]"-"a[2]; if(!($1 in lo)||d<lo[$1]) lo[$1]=d; if(!($1 in hi)||d>hi[$1]) hi[$1]=d; n[$1]++; if($6 ~ /Uncorrectable ECC/) ue[$1]++; if($6 ~ /Correctable ECC/ && $6 !~ /Uncorrectable/) ce[$1]++}
  END{print "ip\tsel_first\tsel_last\tentries\tmem_ce\tmem_ue"; for(k in n) print k"\t"lo[k]"\t"hi[k]"\t"n[k]"\t"ce[k]+0"\t"ue[k]+0}' "$OUT/sel.tsv" > "$OUT/sel_span.tsv"

# ---------- follow-up list: nodes needing later handling (not acted on automatically) ----------
LC_ALL=C awk -F'\t' 'BEGIN{OFS="\t"; print "ip","kind","category","detail"}
  NR==1{next}
  $4!="ok" && $4 !~ /^partial_/ {print $1,$2,"collect_failed",$4; next}
  $4 ~ /^partial_/ {print $1,$2,"collect_partial",$4" "$5}
  { n=split($5,a,","); for(i=1;i<=n;i++){ f=a[i]
      if(f=="no_smartctl") print $1,$2,"smartmontools_missing",f
      else if(f ~ /^smartmontools_pkg_/) print $1,$2,"smartmontools_pkg_broken",f
      else if(f=="smartd_active") print $1,$2,"smartd_active",f
      else if(f=="sel_full") print $1,$2,"sel_full",f
      else if(f ~ /^sel_(read_error|no_output)$/ || f=="no_ipmitool" || f=="no_ipmi_dev") print $1,$2,"sel_unreadable",f
      else if(f ~ /^cmd_timeout:(lsblk|lspci|storcli)$/) print $1,$2,"device_access_hang",f } }' "$OUT/status.tsv" \
  | LC_ALL=C awk -F'\t' 'BEGIN{OFS="\t"} NR==1{print; next} {k=$1 FS $3; if(k in d){d[k]=d[k]","$4} else {o[++n]=k; r[k]=$1 OFS $2 OFS $3; d[k]=$4}} END{for(i=1;i<=n;i++) print r[o[i]], d[o[i]]}' > "$OUT/followup.tsv"

# ---------- summary ----------
DUR=$(( $(date +%s) - START ))
{
  echo "# 机队硬件只读采集 $DAY"
  echo
  echo "范围：infrastructure_assets present 的 gpu_node + cpu_node（H200 不在其中）；内核日志窗口 ${DAYS} 天；耗时 ${DUR}s；并发 ${PAR}。"
  echo
  echo "## 采集状态（按节点类型）"
  echo
  echo "| 状态 | gpu_node | cpu_node | 合计 |"; echo "|---|---|---|---|"
  awk -F'\t' 'NR>1{c[$4"\t"$2]++; s[$4]++; t[$2]++} END{for(x in s){printf "| %s | %d | %d | %d |\n", x, c[x"\tgpu_node"], c[x"\tcpu_node"], s[x]}; printf "| **合计** | %d | %d | %d |\n", t["gpu_node"], t["cpu_node"], t["gpu_node"]+t["cpu_node"]}' "$OUT/status.tsv" | sort
  echo
  echo "## 已连上节点（ok + partial）的采集缺项（flag → 节点数）"
  echo
  echo "| flag | gpu_node | cpu_node |"; echo "|---|---|---|"
  awk -F'\t' 'NR>1 && ($4=="ok" || $4 ~ /^partial_/) && $5!=""{n=split($5,a,","); for(i=1;i<=n;i++) c[a[i]"\t"$2]++; for(i=1;i<=n;i++) f[a[i]]=1} END{for(x in f) printf "| %s | %d | %d |\n", x, c[x"\tgpu_node"], c[x"\tcpu_node"]}' "$OUT/status.tsv" | sort
  echo
  echo "## 采集失败节点（按网段）"
  echo
  awk -F'\t' 'NR>1 && $4!="ok"{split($1,a,"."); k=$4" "a[1]"."a[2]"."a[3]".x"; c[k]++} END{for(k in c) print "- "k": "c[k]}' "$OUT/status.tsv" | sort
  echo
  echo "## 待后续处理清单（followup.tsv，按类别，节点数）"
  echo
  awk -F'\t' 'NR>1{c[$3]++} END{for(k in c) print "- "k": "c[k]}' "$OUT/followup.tsv" | sort
  echo
  echo "完整清单：status.tsv（每节点一行：ip / 类型 / ssh 返回码 / 状态 / 缺项）。"
  echo
  echo "## 数据量"
  echo
  echo "- SEL 条目（不含 Session Audit）：$(wc -l < "$OUT/sel.tsv")，涉及节点 $(cut -f1 "$OUT/sel.tsv" | sort -u | wc -l)"
  echo "- SEL 内存事件：CE 节点 $(awk -F'\t' 'NR>1&&$5>0' "$OUT/sel_span.tsv" | wc -l)，UE 节点 $(awk -F'\t' 'NR>1&&$6>0' "$OUT/sel_span.tsv" | wc -l)；SEL 最早记录早于 2026-07-01 的节点 $(awk -F'\t' 'NR>1&&$2<"2026-07-01"' "$OUT/sel_span.tsv" | wc -l)"
  echo "- RAID 后物理盘（storcli）：$(wc -l < "$OUT/storcli.tsv") 块，介质错误>0 $(awk -F'\t' '$4+0>0' "$OUT/storcli.tsv" | wc -l)，预测失败>0 $(awk -F'\t' '$6+0>0' "$OUT/storcli.tsv" | wc -l)，SMART 告警 $(awk -F'\t' '$7=="Yes"' "$OUT/storcli.tsv" | wc -l)"
  echo "- SMART：$(awk -F'\t' 'NR>1' "$OUT/smart.tsv" | wc -l) 块盘（$(awk -F'\t' 'NR>1{print $1}' "$OUT/smart.tsv" | sort -u | wc -l) 个节点）；SMART 判定失败 $(awk -F'\t' 'NR>1&&$11=="false"' "$OUT/smart.tsv" | wc -l)，读取失败 $(awk -F'\t' 'NR>1&&$11=="read_error"' "$OUT/smart.tsv" | wc -l)"
  echo "- 内存条记录：$(wc -l < "$OUT/dimm.tsv")；磁盘记录：$(wc -l < "$OUT/disk.tsv")；内核硬件日志分组：$(wc -l < "$OUT/kern.tsv")"
} > "$OUT/summary.md"
set -e -o pipefail
(cd "$OUT" && sha256sum nodes.tsv status.tsv followup.tsv sel.tsv sel_span.tsv storcli.tsv smart.tsv kern.tsv dimm.tsv disk.tsv summary.md nodes/*.txt > SHA256SUMS)
gzip -f "$OUT"/nodes/*.txt   # SHA256SUMS refers to the uncompressed text (zcat | sha256sum)
# Fleet disk/memory snapshot for the web page (/api/v1/hardware/fleet); failure must not break collection.
python3 /ops/atlas/scripts/node_hw_fleet_report.py "$OUT" > "$OUT/fleet-hw.log" 2>&1 || echo "fleet-hw report failed (see $OUT/fleet-hw.log)" >&2
cp "$OUT/summary.md" "$REPORT_DIR/daily-latest.md"
cp "$OUT/status.tsv" "$REPORT_DIR/status-latest.tsv"
cp "$OUT/followup.tsv" "$REPORT_DIR/followup-latest.tsv"
echo "done in ${DUR}s: $(awk -F'\t' 'NR>1{print $4}' "$OUT/status.tsv" | sort | uniq -c | tr '\n' ' ')"

#!/usr/bin/env bash
# verify smartctl present; ensure smartd disabled/stopped (idempotent)
OUT=/data/atlas-research/node-hw-install/verify-$(date +%Y%m%dT%H%M%S); mkdir -p $OUT; export OUT
one(){ n=$1; r=$(timeout 150 ssh -o BatchMode=yes -o ConnectTimeout=8 root@$n 'v=$(smartctl --version 2>/dev/null|head -1|cut -d" " -f2); e=$(systemctl is-enabled smartmontools 2>/dev/null); a=$(systemctl is-active smartmontools 2>/dev/null); fix=no; if [ "$e" = enabled ] || [ "$a" = active ] || pgrep -x smartd >/dev/null; then systemctl disable --now smartmontools >/dev/null 2>&1; fix=yes; fi; echo "${v:-none} ${e:-na} ${a:-na} $fix $(dpkg-query -W -f="\${db:Status-Abbrev}" smartmontools 2>/dev/null)"' 2>/dev/null); echo -e "$n\t$?\t$r" >> $OUT/verify.tsv; }
export -f one
awk -F"\t" 'NR>1&&$4=="ok"{print $1}' /data/atlas-research/node-hw-daily/2026-11-20/status.tsv | xargs -P 32 -I{} bash -c 'one {}'
echo $OUT

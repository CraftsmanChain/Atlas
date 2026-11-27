#!/usr/bin/env bash
# Daily read-only Redfish memory poll (memory plan v1.2 section 3.5). Runs on 10.80.32.234 (holds the ipmi_exporter
# config with the BMC credentials and reaches every BMC) at 01:00, before the 01:40 fleet collection on the deploy node.
# Only HTTP GET; one credential try per BMC; no retry; <= 8 BMCs in parallel; 10.80.168.14 excluded by default.
# Output: $OUT_DIR/redfish-mem-<day>.json, then pushed (rsync over ssh) to the deploy node when ATLAS_REDFISH_PUSH is set
# (e.g. root@10.80.192.232:/data/atlas-research/node-hw-redfish/ ; needs a key from 234 to the deploy node).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONF="${ATLAS_IPMI_EXPORTER_CONFIG:-/usr/local/exporters/ipmi-exporter/ipmi_remote.yml}"
OUT_DIR="${ATLAS_REDFISH_OUT:-/var/lib/atlas-redfish}"
DAY="$(TZ=Asia/Shanghai date +%F)"
mkdir -p "$OUT_DIR"; chmod 700 "$OUT_DIR"
OUT="$OUT_DIR/redfish-mem-$DAY.json"
python3 "$HERE/node_hw_redfish_probe.py" --daily --config "$CONF" --out "$OUT" --targets-from-vm \
  --exclude "${ATLAS_REDFISH_EXCLUDE:-10.80.168.14}" --parallel "${ATLAS_REDFISH_PARALLEL:-8}" 2>> "$OUT_DIR/redfish-daily.log"
sha256sum "$OUT" > "$OUT.sha256"
if [ -n "${ATLAS_REDFISH_PUSH:-}" ]; then
  rsync -a --rsh="ssh -o BatchMode=yes -o ConnectTimeout=10" "$OUT" "$OUT.sha256" "$ATLAS_REDFISH_PUSH" || echo "push to $ATLAS_REDFISH_PUSH failed" >&2
fi
find "$OUT_DIR" -name 'redfish-mem-*.json*' -mtime +60 -delete

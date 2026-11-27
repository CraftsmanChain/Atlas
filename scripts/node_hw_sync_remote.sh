#!/usr/bin/env bash
# Sync the fleet-hardware collector scripts + exclude list to the deploy node, then (optionally)
# regenerate fleet-hw.json for given days so the 硬件健康 page reflects the current scope.  2026-11-23.
# The normal release (deploy_remote_source.sh) does not ship these scripts, so run this after it.
# Read-only towards nodes: regeneration only re-reads files already on the deploy node.
# 2026-11-27 (memory plan v1.2): also ships node_hw_mem_rules.py, node_hw_swaplist.py, node_hw_redfish_daily.sh,
# configs/node-hw-mem-platforms.tsv and the replacement template. Regenerate days in ascending order (the ledger,
# mem_state.tsv history and replacement detection build on the previous day): e.g. 2026-11-20 ... 2026-11-27.
# Usage (from the repo root, on a machine with SSH access to the deploy node):
#   bash scripts/node_hw_sync_remote.sh [day ...]       e.g. 2026-11-20 2026-11-21 2026-11-22 2026-11-23
#   bash scripts/node_hw_sync_remote.sh all             regenerate every day that has fleet-hw.json
set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$script_dir/deployment_target.sh"
repo_root="$(cd "$script_dir/.." && pwd)"
cd "$repo_root"
for d in "$@"; do [[ "$d" == all || "$d" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || { echo "bad day: $d" >&2; exit 1; }; done

rsync -a --rsh="$rsync_rsh" scripts/node_hw_daily_collect.sh scripts/node_hw_fleet_report.py scripts/node_hw_ledger.py scripts/node_hw_mem_rules.py scripts/node_hw_smart_parse.py scripts/node_hw_sel_rotate.sh scripts/node_hw_redfish_probe.py \
  scripts/node_hw_redfish_daily.sh scripts/node_hw_swaplist.py scripts/node_add_pubkey.sh "$remote_ssh:$remote_root/scripts/"
rsync -a --rsh="$rsync_rsh" deploy/node-hw/node-hw-exclude.tsv "$remote_ssh:$remote_root/configs/node-hw-exclude.tsv"
rsync -a --rsh="$rsync_rsh" configs/node-hw-mem-platforms.tsv "$remote_ssh:$remote_root/configs/node-hw-mem-platforms.tsv"
# memory plan v1.2: replacement template + Redfish poll units (to be installed on 10.80.32.234, see the plan doc)
atlas_ssh "$remote_ssh" "mkdir -p '$remote_root/deploy/node-hw' /data/atlas-research/node-hw-redfish"
rsync -a --rsh="$rsync_rsh" deploy/node-hw/dimm-replacement-template.tsv deploy/node-hw/atlas-redfish-mem.service deploy/node-hw/atlas-redfish-mem.timer \
  deploy/node-hw/atlas-node-hw-collect.service deploy/node-hw/atlas-node-hw-collect.timer "$remote_ssh:$remote_root/deploy/node-hw/"
rsync -a --rsh="$rsync_rsh" deploy/node-hw/pubkey-b-2026-11-23.txt "$remote_ssh:$remote_root/reports/node-hw/"
atlas_ssh "$remote_ssh" "chmod 755 '$remote_root'/scripts/node_hw_daily_collect.sh '$remote_root'/scripts/node_add_pubkey.sh '$remote_root'/scripts/node_hw_redfish_daily.sh; \
  echo exclude: \$(grep -vc '^#' '$remote_root/configs/node-hw-exclude.tsv') ips"

if [[ $# -gt 0 ]]; then
  atlas_ssh "$remote_ssh" "set -e; R=/data/atlas-research/node-hw-daily; \
    if [ '$*' = all ]; then days=\$(ls -d \$R/20??-??-??/fleet-hw.json 2>/dev/null | xargs -n1 dirname | xargs -n1 basename | sort); else days='$*'; fi; \
    for d in \$days; do [ -d \$R/\$d ] || { echo \"skip \$d (no dir)\"; continue; }; \
      [ -f \$R/\$d/fleet-hw.json ] && cp -pn \$R/\$d/fleet-hw.json \$R/\$d/fleet-hw.json.bak-\$(date +%Y%m%d) 2>/dev/null || true; \
      python3 '$remote_root/scripts/node_hw_fleet_report.py' \$R/\$d; done"
fi
curl -fsS -m 20 "$remote_url/api/v1/hardware/fleet" | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; c=d["coverage"]; print("api", d["day"], "nodes_total", c["nodes_total"], "collected", c["collected"], "excluded", c.get("excluded"))'

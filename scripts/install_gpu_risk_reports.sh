#!/usr/bin/env bash
# Install (or --uninstall) the read-only GPU risk report timers on the Atlas host.
# usage: install_gpu_risk_reports.sh [SOURCE_DIR]   (SOURCE_DIR contains deploy/gpu-risk;
#        default: newest /ops/atlas/releases/*/source by mtime)
#        install_gpu_risk_reports.sh --uninstall
# Idempotent. The reports only read the database; they never alert or act.
set -euo pipefail

unit_dir=/etc/systemd/system
report_dir=/ops/atlas/reports/gpu-risk
timers=(atlas-gpu-risk-report-daily.timer atlas-gpu-risk-report-weekly.timer)
units=(atlas-gpu-risk-report-daily.service atlas-gpu-risk-report-daily.timer atlas-gpu-risk-report-weekly.service atlas-gpu-risk-report-weekly.timer)

if [[ "$(id -u)" != 0 ]]; then
  echo "install_gpu_risk_reports.sh must run as root" >&2
  exit 1
fi

if [[ "${1:-}" == "--uninstall" ]]; then
  [[ $# -eq 1 ]] || { echo "--uninstall takes no other arguments" >&2; exit 1; }
  for timer in "${timers[@]}"; do
    systemctl disable --now "$timer" 2>/dev/null || true
  done
  for unit in "${units[@]}"; do
    rm -f "$unit_dir/$unit"
  done
  systemctl daemon-reload
  echo "GPU risk report timers removed; reports in $report_dir are kept."
  exit 0
fi

[[ $# -le 1 ]] || { echo "usage: $0 [SOURCE_DIR] | --uninstall" >&2; exit 1; }
source_dir="${1:-}"
if [[ -z "$source_dir" ]]; then
  source_dir="$(ls -1dt /ops/atlas/releases/*/source 2>/dev/null | head -n 1 || true)"
fi
if [[ -z "$source_dir" || ! -d "$source_dir/deploy/gpu-risk" ]]; then
  echo "source directory with deploy/gpu-risk not found: ${source_dir:-<none>}" >&2
  exit 1
fi
for unit in "${units[@]}"; do
  [[ -f "$source_dir/deploy/gpu-risk/$unit" ]] || { echo "missing unit: $source_dir/deploy/gpu-risk/$unit" >&2; exit 1; }
done
[[ -x /ops/atlas/gpu-risk ]] || echo "warning: /ops/atlas/gpu-risk is not installed yet; timers will fail until it is" >&2

for unit in "${units[@]}"; do
  install -m 0644 "$source_dir/deploy/gpu-risk/$unit" "$unit_dir/$unit"
done
mkdir -p -m 0750 "$report_dir"
chmod 0750 "$report_dir"
systemctl daemon-reload
systemctl enable --now "${timers[@]}"
echo "installed from $source_dir"
systemctl list-timers --all --no-pager | grep -E 'atlas-gpu-risk-report-(daily|weekly)\.timer' || true

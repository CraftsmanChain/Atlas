#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

config_path="${ATLAS_CONFIG:-${1:-configs/config.yaml}}"

if [[ ! -f "$config_path" ]]; then
  echo "未找到配置文件: $config_path"
  exit 1
fi

echo "使用配置文件: $config_path"
if [[ -n "${ATLAS_PORT:-}" ]]; then
  echo "使用端口覆盖: $ATLAS_PORT"
fi

go run ./cmd/server --config "$config_path"

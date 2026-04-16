#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root/web"

web_port="${ATLAS_WEB_PORT:-${1:-4173}}"
api_target="${ATLAS_API_TARGET:-http://127.0.0.1:8080}"

echo "前端端口: $web_port"
echo "代理后端: $api_target"

ATLAS_API_TARGET="$api_target" npm run dev -- --host 0.0.0.0 --port "$web_port"

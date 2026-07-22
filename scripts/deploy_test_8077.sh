#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
remote_ssh="${REMOTE_SSH:-root@10.111.201.1}"
remote_root="${REMOTE_ROOT:-/ops/atlas-test}"
service_name="${SERVICE_NAME:-atlas-test.service}"
release_id="$(date +%Y%m%d%H%M%S)"
archive="/tmp/atlas-test-web-${release_id}.tar.gz"
server_binary="/tmp/atlas-test-server-${release_id}"

echo "[1/6] 构建前端与 Linux 测试后端"
(cd "$repo_root/web" && npm run build)
(cd "$repo_root" && ZIG_GLOBAL_CACHE_DIR=/tmp/atlas-zig-global ZIG_LOCAL_CACHE_DIR=/tmp/atlas-zig-local GOCACHE=/tmp/atlas-go-build CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC="zig cc -target x86_64-linux-gnu" go build -o "$server_binary" ./cmd/server)
gzip -f -k "$server_binary"

echo "[2/6] 打包静态文件"
tar -C "$repo_root/web/dist" -zcf "$archive" .

echo "[3/6] 上传测试环境文件"
scp -O "$archive" "$remote_ssh:/tmp/atlas-test-web.tar.gz"
scp -O "$server_binary.gz" "$remote_ssh:/tmp/atlas-test-server.gz"
scp -O "$repo_root/configs/config.test.yaml" "$remote_ssh:/tmp/atlas-test-config.yaml"
scp -O "$repo_root/docs/assets/asset-monitor-compare-2026-07-17.csv" "$remote_ssh:/tmp/atlas-test-assets.csv"
scp -O "$repo_root/deploy/atlas-test.service" "$remote_ssh:/tmp/atlas-test.service"

echo "[4/6] 发布独立 8077 服务"
ssh "$remote_ssh" "set -euo pipefail
gunzip -f /tmp/atlas-test-server.gz
install -d -m 755 '$remote_root/releases/$release_id' '$remote_root/logs'
tar -xzf /tmp/atlas-test-web.tar.gz -C '$remote_root/releases/$release_id'
install -m 644 /tmp/atlas-test-config.yaml '$remote_root/config.test.yaml'
install -m 644 /tmp/atlas-test-assets.csv '$remote_root/asset-monitor-compare.csv'
install -m 644 /tmp/atlas-test.service '/etc/systemd/system/$service_name'
install -m 755 /tmp/atlas-test-server '$remote_root/atlas-server'
if [[ ! -f '$remote_root/atlas-test.db' ]]; then
  if command -v sqlite3 >/dev/null 2>&1; then
    sqlite3 /ops/atlas/atlas.db \".backup '$remote_root/atlas-test.db'\"
  else
    cp -p /ops/atlas/atlas.db '$remote_root/atlas-test.db'
  fi
fi
ln -sfn '$remote_root/releases/$release_id' '$remote_root/web-current'
systemctl daemon-reload
systemctl enable --now '$service_name'
systemctl restart '$service_name'
systemctl is-active '$service_name'
"

echo "[5/6] 验证服务与资产同步"
ssh "$remote_ssh" "set -e
curl -fsS http://127.0.0.1:8077/health
curl -fsS http://127.0.0.1:8077/api/v1/status
curl -fsS http://127.0.0.1:8077/api/v1/fleet/summary
curl -fsS http://127.0.0.1:7077/api/v1/status
"

echo "[6/6] 清理本地临时构建"
rm -f "$archive" "$server_binary" "$server_binary.gz"

echo "测试环境发布完成: http://10.111.201.1:8077/"

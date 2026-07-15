#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

output_dir="${OUTPUT_DIR:-$repo_root/bin/linux-amd64}"
version="${VERSION:-dev}"
commit="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
build_time="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
mkdir -p "$output_dir"

if command -v zig >/dev/null 2>&1; then
  export CC="zig cc -target x86_64-linux-gnu"
elif command -v x86_64-linux-gnu-gcc >/dev/null 2>&1; then
  export CC="x86_64-linux-gnu-gcc"
else
  echo "未找到可用的 Linux amd64 C 交叉编译器。"
  echo "由于项目使用 SQLite（CGO），请先安装 zig 或 x86_64-linux-gnu-gcc。"
  exit 1
fi

export CGO_ENABLED=1
export GOOS=linux
export GOARCH=amd64

echo "使用 CC=$CC"
echo "输出目录: $output_dir"
echo "版本信息: version=$version commit=$commit build_time=$build_time"

server_ldflags="-X main.Version=$version -X main.Commit=$commit -X main.BuildTime=$build_time"
go build -ldflags "$server_ldflags" -o "$output_dir/atlas-server" ./cmd/server
go build -o "$output_dir/atlas-agent" ./cmd/agent

echo "构建完成:"
echo "  - $output_dir/atlas-server"
echo "  - $output_dir/atlas-agent"

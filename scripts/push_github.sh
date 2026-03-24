#!/usr/bin/env bash
set -euo pipefail

remote_name="${1:-github}"
branch="${2:-$(git rev-parse --abbrev-ref HEAD)}"

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$repo_root" ]]; then
  echo "当前目录不是 Git 仓库。"
  exit 1
fi

cd "$repo_root"

if ! git remote get-url "$remote_name" >/dev/null 2>&1; then
  echo "未找到远端: $remote_name"
  echo "请先添加 GitHub 远端，例如："
  echo "git remote add github git@github.com:<your-org-or-user>/<repo>.git"
  exit 1
fi

config_path="configs/config.yaml"
if [[ ! -f "$config_path" ]]; then
  echo "未找到 $config_path"
  exit 1
fi

cat > "$config_path" <<'EOF'
gateway:
  port: ":8080"
storage:
  dsn: "atlas.db"
feishu:
  bots:
    - enabled: false
      webhook_url: "https://open.feishu.cn/open-apis/bot/v2/hook/YOUR_WEBHOOK_URL"
      enable_signature: false
      secret: ""
EOF

git add "$config_path"
git add -A

if git diff --cached --quiet; then
  echo "没有可提交的变更。"
  exit 0
fi

read -r -p "请输入本次提交信息: " commit_message
if [[ -z "${commit_message// }" ]]; then
  echo "提交信息不能为空。"
  exit 1
fi

git commit -m "$commit_message"
git push "$remote_name" "$branch"

echo "已推送到 $remote_name/$branch，并已使用默认配置覆盖 configs/config.yaml。"

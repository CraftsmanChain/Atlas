#!/usr/bin/env bash
set -euo pipefail

gitlab_remote="${1:-origin}"
github_remote="${2:-github}"
branch="${3:-$(git rev-parse --abbrev-ref HEAD)}"
github_url="https://github.com/CraftsmanChain/Atlas.git"

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$repo_root" ]]; then
  echo "当前目录不是 Git 仓库。"
  exit 1
fi

cd "$repo_root"

if ! git remote get-url "$gitlab_remote" >/dev/null 2>&1; then
  echo "未找到 GitLab 远端: $gitlab_remote"
  exit 1
fi

if ! git remote get-url "$github_remote" >/dev/null 2>&1; then
  git remote add "$github_remote" "$github_url"
  echo "已添加 GitHub 远端: $github_remote -> $github_url"
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
git push "$gitlab_remote" "$branch"
git push "$github_remote" "$branch"

echo "已完成推送: $gitlab_remote/$branch + $github_remote/$branch"
echo "本次提交已覆盖 configs/config.yaml 为默认配置。"

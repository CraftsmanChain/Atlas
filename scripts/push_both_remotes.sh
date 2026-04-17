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

# 添加 GitHub remote（如果不存在）
if ! git remote get-url "$github_remote" >/dev/null 2>&1; then
  git remote add "$github_remote" "$github_url"
  echo "已添加 GitHub 远端: $github_remote -> $github_url"
fi

# ====================== 强制覆盖默认配置 ======================
config_path="configs/config.yaml"
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

# ====================== 输入提交信息 + 确认 ======================
while true; do
  # 【关键修复】把 -p 改成 printf + read，避免中文提示符导致的输入截断
  printf "请输入本次提交信息: "
  read -r commit_message

  # 去掉前后空格后检查是否为空
  if [[ -z "${commit_message// }" ]]; then
    echo "提交信息不能为空，请重新输入。"
    continue
  fi

  # 打印出来让用户确认（加了调试信息，后面可以删掉）
  echo "────────────────────────"
  echo "本次提交信息将为："
  echo "    [$commit_message]"
  echo "────────────────────────"

  read -r -p "确认无误？(y/n): " confirm
  if [[ "$confirm" =~ ^[Yy]$ ]]; then
    break
  else
    echo "已取消，请重新输入提交信息。"
  fi
done

git commit -m "$commit_message"

git push "$gitlab_remote" "$branch"
git push "$github_remote" "$branch"

echo "已完成推送: $gitlab_remote/$branch + $github_remote/$branch"
echo "本次提交已覆盖 configs/config.yaml 为默认配置。"

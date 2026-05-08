#!/usr/bin/env bash
set -euo pipefail

gitlab_remote="${1:-origin}"
github_remote="${2:-github}"
branch="${3:-$(git rev-parse --abbrev-ref HEAD)}"
commit_message_arg="${4:-${COMMIT_MESSAGE:-}}"
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
trim_outer_whitespace() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

read_commit_message_from_tty() {
  local input=""
  printf "请输入本次提交信息: " > /dev/tty
  IFS= read -r input < /dev/tty || true
  input="${input%$'\r'}"
  printf '%s' "$input"
}

commit_message="$(trim_outer_whitespace "$commit_message_arg")"

while true; do
  if [[ -z "$commit_message" ]]; then
    commit_message="$(read_commit_message_from_tty)"
    commit_message="$(trim_outer_whitespace "$commit_message")"
  fi

  if [[ -z "$commit_message" ]]; then
    echo "提交信息不能为空，请重新输入。"
    continue
  fi

  echo "────────────────────────"
  echo "本次提交信息将为："
  echo "    [$commit_message]"
  echo "────────────────────────"

  printf "确认无误？(y/n): " > /dev/tty
  IFS= read -r confirm < /dev/tty || true
  if [[ "$confirm" =~ ^[Yy]$ ]]; then
    break
  else
    echo "已取消，请重新输入提交信息。"
    commit_message=""
  fi
done

git commit -m "$commit_message"

git push "$gitlab_remote" "$branch"
git push "$github_remote" "$branch"

echo "已完成推送: $gitlab_remote/$branch + $github_remote/$branch"
echo "本次提交已覆盖 configs/config.yaml 为默认配置。"

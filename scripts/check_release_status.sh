#!/usr/bin/env bash
set -euo pipefail

mode="${1:-check}"
remote_url="${REMOTE_URL:-http://10.111.201.1:7077}"
remote_ssh="${REMOTE_SSH:-root@10.111.201.1}"
remote_root="${REMOTE_ROOT:-/ops/atlas}"
service_name="${SERVICE_NAME:-atlas.service}"
git_remote="${GIT_REMOTE:-origin}"
extra_git_remote="${EXTRA_GIT_REMOTE:-github}"
branch="${BRANCH:-$(git rev-parse --abbrev-ref HEAD)}"
skip_npm_install="${SKIP_NPM_INSTALL:-0}"
version_name="${VERSION_NAME:-prod}"

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$repo_root" ]]; then
  echo "当前目录不是 Git 仓库。"
  exit 1
fi
cd "$repo_root"

api_status_url="${remote_url%/}/api/v1/status"
health_url="${remote_url%/}/health"
dist_tarball="$repo_root/dist.tar.gz"

usage() {
  cat <<EOF
用法:
  bash scripts/check_release_status.sh [check|deploy|all]

模式说明:
  check   仅检查本地 Git 状态、远端提交差异、线上服务接口
  deploy  检查 -> 推送 Git -> 构建产物 -> 上传 -> 重启 atlas.service -> 验证接口
  all     等同 deploy

常用环境变量:
  REMOTE_URL=http://10.111.201.1:7077
  REMOTE_SSH=root@10.111.201.1
  REMOTE_ROOT=/ops/atlas
  SERVICE_NAME=atlas.service
  GIT_REMOTE=origin
  EXTRA_GIT_REMOTE=github
  BRANCH=main
  VERSION_NAME=prod
  SKIP_NPM_INSTALL=1
EOF
}

case "$mode" in
  check|deploy|all)
    ;;
  -h|--help|help)
    usage
    exit 0
    ;;
  *)
    echo "未知模式: $mode"
    usage
    exit 1
    ;;
esac

remote_commit=""
ahead_count=""
behind_count=""
status_body=""
health_body=""

print_section() {
  echo
  echo "== $1 =="
}

collect_git_state() {
  git_status="$(git status --porcelain)"
  local_commit="$(git rev-parse --short HEAD)"
  local_commit_full="$(git rev-parse HEAD)"
  local_commit_subject="$(git log -1 --pretty=%s)"

  if git remote get-url "$git_remote" >/dev/null 2>&1; then
    git fetch "$git_remote" "$branch" --quiet || true
    remote_ref="$git_remote/$branch"
    if git rev-parse --verify "$remote_ref" >/dev/null 2>&1; then
      remote_commit="$(git rev-parse --short "$remote_ref")"
      counts="$(git rev-list --left-right --count "$remote_ref...HEAD")"
      behind_count="$(printf '%s' "$counts" | awk '{print $1}')"
      ahead_count="$(printf '%s' "$counts" | awk '{print $2}')"
    fi
  fi
}

print_check_summary() {
  print_section "本地仓库状态"
  if [[ -z "$git_status" ]]; then
    echo "工作区干净：是"
  else
    echo "工作区干净：否"
    echo "$git_status"
  fi

  print_section "本地提交"
  echo "分支: $branch"
  echo "commit(short): $local_commit"
  echo "commit(full):  $local_commit_full"
  echo "message:       $local_commit_subject"

  if [[ -n "$remote_commit" ]]; then
    print_section "与远端对比"
    echo "remote:         $git_remote"
    echo "remote commit:  $remote_commit"
    echo "ahead:          $ahead_count"
    echo "behind:         $behind_count"
    if [[ "$ahead_count" == "0" && "$behind_count" == "0" ]]; then
      echo "本地是否已全部提交并推送：是"
    else
      echo "本地是否已全部提交并推送：否"
    fi
  fi
}

query_remote_service() {
  print_section "远端服务接口"
  echo "GET $api_status_url"
  status_body="$(curl -sS --max-time 8 "$api_status_url" || true)"
  if [[ -n "$status_body" ]]; then
    echo "$status_body"
  else
    echo "调用失败"
  fi

  echo
  echo "GET $health_url"
  health_body="$(curl -sS --max-time 8 "$health_url" || true)"
  if [[ -n "$health_body" ]]; then
    echo "$health_body"
  else
    echo "调用失败"
  fi

  print_section "结论"
  echo "本地 commit: $local_commit"
  if [[ -n "$remote_commit" ]]; then
    echo "远端 Git commit: $remote_commit"
  fi
  if [[ "$status_body" == *"\"commit\""* ]]; then
    echo "远端服务状态接口已暴露版本信息。"
  else
    echo "远端服务状态接口暂未暴露 commit 字段，说明线上服务大概率还没更新到最新版本。"
  fi
}

ensure_ready_for_deploy() {
  if [[ -n "$git_status" ]]; then
    echo "工作区存在未提交变更，已停止部署。"
    exit 1
  fi
  if [[ -n "$behind_count" && "$behind_count" != "0" ]]; then
    echo "本地分支落后于 $git_remote/$branch，请先同步再部署。"
    exit 1
  fi
}

push_git_updates() {
  print_section "推送 Git 更新"
  if [[ -n "$ahead_count" && "$ahead_count" != "0" ]]; then
    git push "$git_remote" "$branch"
  else
    echo "$git_remote/$branch 已是最新，无需再次推送。"
  fi

  if [[ -n "$extra_git_remote" ]] && git remote get-url "$extra_git_remote" >/dev/null 2>&1; then
    git push "$extra_git_remote" "$branch"
  fi
}

build_artifacts() {
  print_section "构建前端产物"
  if [[ "$skip_npm_install" != "1" ]]; then
    (cd "$repo_root/web" && npm install)
  fi
  (cd "$repo_root/web" && npm run build)

  print_section "构建 Linux 后端产物"
  build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  VERSION="$version_name" COMMIT="$local_commit" BUILD_TIME="$build_time" bash "$repo_root/scripts/build_linux_amd64.sh"

  print_section "打包前端静态文件"
  rm -f "$dist_tarball"
  tar -C "$repo_root/web" -zcf "$dist_tarball" dist
  echo "生成: $dist_tarball"
}

deploy_remote() {
  print_section "上传并更新线上服务"
  scp "$repo_root/bin/linux-amd64/atlas-server" "$remote_ssh:$remote_root/atlas-server.new"
  scp "$repo_root/bin/linux-amd64/atlas-db-migrate" "$remote_ssh:$remote_root/atlas-db-migrate.new"
  scp "$repo_root/scripts/postgres_backup.sh" "$remote_ssh:$remote_root/postgres_backup.sh.new"
  scp "$dist_tarball" "$remote_ssh:$remote_root/dist.tar.gz"

  ssh "$remote_ssh" "set -euo pipefail
mkdir -p '$remote_root/web'
mkdir -p '$remote_root/scripts'
timestamp=\$(date +%Y%m%d%H%M%S)
if [[ -f '$remote_root/atlas-server' ]]; then
  cp '$remote_root/atlas-server' '$remote_root/atlas-server.bak.'\"\$timestamp\"
fi
install -m 755 '$remote_root/atlas-server.new' '$remote_root/atlas-server'
install -m 755 '$remote_root/atlas-db-migrate.new' '$remote_root/atlas-db-migrate'
install -m 755 '$remote_root/postgres_backup.sh.new' '$remote_root/scripts/postgres_backup.sh'
rm -f '$remote_root/atlas-server.new'
rm -f '$remote_root/atlas-db-migrate.new' '$remote_root/postgres_backup.sh.new'
rm -rf '$remote_root/web/dist'
tar -xzf '$remote_root/dist.tar.gz' -C '$remote_root/web'
rm -f '$remote_root/dist.tar.gz'
systemctl restart '$service_name'
systemctl is-active '$service_name'
"
}

verify_remote_after_deploy() {
  print_section "回查线上服务"
  local attempt
  for attempt in 1 2 3 4 5; do
    status_body="$(curl -sS --max-time 8 "$api_status_url" || true)"
    health_body="$(curl -sS --max-time 8 "$health_url" || true)"
    if [[ -n "$status_body" && -n "$health_body" ]]; then
      break
    fi
    sleep 2
  done

  echo "GET $api_status_url"
  if [[ -n "$status_body" ]]; then
    echo "$status_body"
  else
    echo "调用失败"
  fi

  echo
  echo "GET $health_url"
  if [[ -n "$health_body" ]]; then
    echo "$health_body"
  else
    echo "调用失败"
  fi
}

collect_git_state
print_check_summary
query_remote_service

if [[ "$mode" == "deploy" || "$mode" == "all" ]]; then
  ensure_ready_for_deploy
  push_git_updates
  build_artifacts
  deploy_remote
  verify_remote_after_deploy
fi

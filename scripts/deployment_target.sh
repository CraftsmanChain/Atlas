#!/usr/bin/env bash

# Shared by deployment and read-only verification; never contains credentials.
deployment_target="${ATLAS_DEPLOY_TARGET:-xiaozhen-zhiyuan}"
case "$deployment_target" in
  xiaozhen-zhiyuan)
    target_ssh="root@10.80.192.232"
    target_url="http://10.80.192.232:7077"
    target_config="configs/clusters/xiaozhen-zhiyuan.yaml"
    ;;
  legacy-zhiyuan)
    target_ssh="root@10.111.201.1"
    target_url="http://10.111.201.1:7077"
    target_config="configs/config.yaml"
    ;;
  *)
    echo "Unknown ATLAS_DEPLOY_TARGET: $deployment_target" >&2
    return 1
    ;;
esac

if [[ -n "${REMOTE_SSH:-}" && "$REMOTE_SSH" != "$target_ssh" && -z "${REMOTE_URL:-}" ]]; then
  echo "A custom REMOTE_SSH requires REMOTE_URL to avoid verifying a different cluster." >&2
  return 1
fi
remote_ssh="${REMOTE_SSH:-$target_ssh}"
remote_url="${REMOTE_URL:-$target_url}"
remote_root="${REMOTE_ROOT:-/ops/atlas}"
config_template="${ATLAS_CONFIG_TEMPLATE:-$target_config}"
if [[ ! "$remote_url" =~ ^https?://[^[:space:]\'\"]+$ || "$remote_url" == *[@\?\#]* ]]; then
  echo "REMOTE_URL must be a credential-free HTTP(S) base URL." >&2
  return 1
fi

if [[ ! "$remote_root" =~ ^/[A-Za-z0-9_./-]+$ || "$remote_root" == / || "$remote_root" == *..* ]]; then
  echo "REMOTE_ROOT must be a safe absolute application directory." >&2
  return 1
fi
if [[ ! "$config_template" =~ ^configs/[A-Za-z0-9_./-]+\.yaml$ || "$config_template" == *..* ]]; then
  echo "ATLAS_CONFIG_TEMPLATE must identify a repository YAML file under configs/." >&2
  return 1
fi

atlas_ssh_options=(-o ConnectTimeout=10 -o ServerAliveInterval=10 -o ServerAliveCountMax=3)
if [[ -n "${ATLAS_SSH_CONFIG:-}" ]]; then
  if [[ ! -f "$ATLAS_SSH_CONFIG" || "$ATLAS_SSH_CONFIG" == *[[:space:]\'\"]* ]]; then
    echo "ATLAS_SSH_CONFIG must be an existing file with a shell-safe path." >&2
    return 1
  fi
  atlas_ssh_options+=(-F "$ATLAS_SSH_CONFIG")
fi
rsync_rsh="${RSYNC_RSH:-ssh ${atlas_ssh_options[*]}}"
atlas_ssh() { command ssh "${atlas_ssh_options[@]}" "$@"; }

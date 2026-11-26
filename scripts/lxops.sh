#!/usr/bin/env bash
set -euo pipefail
umask 077

out=${1:-/tmp/lxops-assets.json}
if [[ -n ${LXOPS_TOKEN_FILE:-} ]]; then
  token=$(cat "$LXOPS_TOKEN_FILE")
else
  read -rsp 'Bearer Token: ' token </dev/tty
  printf '\n' >&2
fi

url='https://10.255.171.88:17090/api/v1/asset-new/machine/list'
dc='386df4b2-6a65-40e3-801e-86b472d2d071'
tmp=$(mktemp -d "$(dirname "$out")/.lxops.XXXXXXXX")
trap 'rm -rf "$tmp"' EXIT
page=1
size=200
total=-1
fetched=0
while :; do
  printf 'Authorization: Bearer %s\n' "$token" |
    curl -kfsS --compressed --max-time 60 --retry 2 -H @- \
      -H "X-Dc-Id: $dc" -H 'Accept: application/json' \
      "$url?current=$page&pageSize=$size" -o "$tmp/page"
  # Some responses contain gzip bytes even after HTTP content decoding.
  if [[ $(od -An -tx1 -N2 "$tmp/page" | tr -d ' \n') == 1f8b ]]; then
    gzip -dc "$tmp/page" > "$tmp/json"
  else
    mv "$tmp/page" "$tmp/json"
  fi
  if ! jq -e --argjson p "$page" --argjson s "$size" '
      .success == true and .errorCode == 0 and .data.current == $p and
      .data.pageSize == $s and (.data.total | type == "number" and . >= 0 and . == floor)
      and (.data.list | type == "array")
    ' "$tmp/json" >/dev/null; then
    echo "第 $page 页响应格式异常" >&2
    exit 1
  fi
  page_total=$(jq '.data.total' "$tmp/json")
  count=$(jq '.data.list | length' "$tmp/json")
  if (( total < 0 )); then total=$page_total; fi
  expected=$((total - fetched))
  if (( expected > size )); then expected=$size; fi
  if (( total != page_total || count != expected )); then
    echo "第 $page 页数量变化或分页不完整" >&2
    exit 1
  fi
  jq -c '.data.list[]' "$tmp/json" >> "$tmp/rows"
  fetched=$((fetched + count))
  echo "第 $page 页，累计 $fetched/$total 条" >&2
  if (( fetched == total )); then break; fi
  page=$((page + 1))
done
jq -s 'if all(.[]; .id | type == "string" and length > 0) and
  (map(.id) | unique | length) == length then . else error("资产 ID 缺失或重复") end' \
  "$tmp/rows" > "$tmp/assets.json"
mv "$tmp/assets.json" "$out"
printf '%s\n' "$out"

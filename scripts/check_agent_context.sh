#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$repo_root" ]]; then
  echo "agent-context-check must run inside the Atlas repository" >&2
  exit 1
fi
agents_file="$repo_root/AGENTS.md"
rtk_file="$repo_root/RTK.md"
docs_root="${ATLAS_DOCS_DIR:-$(dirname "$repo_root")/Atlas-Docs}"
wiki_file="$docs_root/docs/llm-wiki.md"
roadmap_file="$docs_root/docs/hardware-failure-prediction-capability-roadmap.md"

require_file() {
  if [[ ! -f "$1" ]]; then
    echo "missing required context file: $1" >&2
    exit 1
  fi
}

require_pattern() {
  if ! grep -Eq "$2" "$1"; then
    echo "missing required context marker '$2' in $1" >&2
    exit 1
  fi
}

require_file "$agents_file"
require_file "$rtk_file"
require_pattern "$agents_file" '^@RTK\.md$'
require_pattern "$rtk_file" 'llm-wiki\.md'
require_pattern "$rtk_file" 'hardware-failure-prediction-capability-roadmap\.md'
require_pattern "$rtk_file" '^## Prediction safety$'
require_pattern "$rtk_file" '^## Verification$'

if [[ ! -e "$docs_root/.git" ]]; then
  echo "agent context OK: Atlas entry and contract valid; Atlas-Docs checkout absent, external link checks skipped"
  exit 0
fi

require_file "$wiki_file"
require_file "$roadmap_file"
require_pattern "$wiki_file" '^## 1\. Mandatory reading route$'
require_pattern "$wiki_file" '^## 4\. Product objective and current capability capsule$'
require_pattern "$wiki_file" '^## 5\. Active prediction state$'
require_pattern "$roadmap_file" '^# '
echo "agent context OK: AGENTS.md -> RTK.md -> LLM Wiki -> prediction roadmap"

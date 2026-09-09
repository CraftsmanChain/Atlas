#!/usr/bin/env bash
set -euo pipefail

context_base_url="${ATLAS_CONTEXT_BASE_URL:-http://10.111.201.1:7077}"
if [[ "${1:-}" == "--base-url" ]]; then
  context_base_url="${2:-}"
elif [[ $# -gt 0 ]]; then
  echo "usage: $0 [--base-url http://host:port]" >&2
  exit 2
fi
if [[ ! "$context_base_url" =~ ^https?://[^[:space:]]+$ ]]; then
  echo "base URL must be an http(s) URL without whitespace" >&2
  exit 2
fi
for command_name in curl jq; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "$command_name is required" >&2
    exit 1
  fi
done

fetch_json() {
  curl -fsS --max-time 15 "${context_base_url%/}$1"
}

status_json="$(fetch_json /api/v1/status)"
health_text="$(fetch_json /health)"
matrices_json="$(fetch_json '/api/v1/prediction/history/training-matrices?limit=5')"
models_json="$(fetch_json '/api/v1/prediction/history/baseline-models?limit=10')"

# Emit an allowlisted, read-only snapshot. Paths, credentials, raw incidents,
# labels, feature values and prediction rows are deliberately excluded.
jq -n \
  --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson status "$status_json" \
  --arg health "$health_text" \
  --argjson matrices "$matrices_json" \
  --argjson models "$models_json" '
  {
    version: "atlas-llm-context-v1",
    generated_at: $generated_at,
    source: "read_only_runtime_apis",
    safety: {credentials_included: false, raw_incidents_included: false, feature_values_included: false},
    runtime: {
      version: ($status.version // $status.data.version // null),
      commit: ($status.commit // $status.data.commit // null),
      build_time: ($status.build_time // $status.data.build_time // null),
      health: $health
    },
    matrices: [($matrices.data // [])[] | {
      id, training_matrix_key, version, status, feature_contract_version,
      sample_count, positive_count, control_count, matrix_sha256,
      duplicate_count, entity_split_conflict_count, point_in_time_violation_count,
      pairing_violation_count, contract_violation_count, finished_at
    }],
    model_builds: [($models.data // [])[] | {
      id, baseline_model_key, version, status, algorithm, source_matrix_build_id,
      source_training_matrix_key, feature_contract_version, scope_event_type,
      scope_model_name, feature_audit_status, prohibited_feature_count,
      statistically_stable_count, shadow_candidate_count, horizon_count,
      train_count, validation_count, test_count, test_macro_roc_auc,
      test_macro_pr_auc, test_macro_precision, test_macro_recall,
      artifact_sha256, finished_at
    }]
  }'

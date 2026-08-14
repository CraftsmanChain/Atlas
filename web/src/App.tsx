import { Component, Fragment, useCallback, useEffect, useMemo, useRef, useState, type ErrorInfo, type ReactNode } from 'react';
import { AnimatePresence, motion } from 'framer-motion';
import { useTranslation } from 'react-i18next';
import { useTheme } from 'next-themes';
import {
  Activity, AlertTriangle, BarChart3, Bell, BookOpen, BrainCircuit, CheckCircle2,
  ChevronLeft, ChevronRight, CircleGauge, ClipboardList, Command, Cpu, Database, Download, Eye, Filter, Gauge,
  Languages, Layers3, MemoryStick, Menu, Moon, Network,
  Palette, RefreshCw, Save, Search, Server, ShieldAlert, ShieldCheck, Sun,
  Thermometer, Trash2, X, Zap,
} from 'lucide-react';
import './App.css';

type PageId = 'overview' | 'gpus' | 'issues' | 'incidents' | 'validations' | 'quality' | 'models' | 'about';
type SubPage = { id: string; label: string };
type Tx = (zh: string, en: string) => string;

type Ingestion = {
  id: number; event_id: string; source: string; host: string; level: string; message: string;
  process_status: string; callback_status: string; raw_payload?: string;
  labels?: Record<string, string>; created_at: string; ai_report_status?: string;
  event_timestamp?: string; ai_report_summary?: string; ai_report_confidence?: number;
};
type IngestionMeta = {
  total: number; all_total: number; limit: number; has_more: boolean; next_before_id: number;
  latest_received_at?: string; received_5m: number; received_1h: number;
  source_mode: string; stream_status: string; server_time?: string;
};
type Failure = { id: number; source: string; level: string; message: string; process_status: string; callback_status: string; process_last_error: string; callback_last_error: string; updated_at: string };
type Report = { status: string; model: string; severity: string; summary: string; probable_causes?: string[]; recommended_actions?: string[]; confidence?: number };
type ServerStatus = { status: string; version: string; commit: string; build_time: string; server_time: string };
type PlatformConfig = { instance_name: string; product_name: string; product_tagline: string; environment: string; updated_at?: string };
type LXOPAssetConfig = {
  ops_host_url: string; asset_machine_url: string; data_center_id: string;
  insecure_skip_verify: boolean; enabled: boolean; api_key_configured: boolean;
  last_sync_status: string; last_sync_error?: string; last_sync_at?: string;
  last_ops_host_count: number; last_machine_count: number; updated_at?: string;
};
type ReconciliationSource = { asset_key: string; ip_address: string; name: string; type: string; model: string; state: string; sn: string; in_use: boolean };
type ReconciliationRow = {
  key: string; scope: 'both' | 'ops_only' | 'asset_only'; category: string; type: string; gpu_model?: string;
  ip_address: string; name: string; sn: string; ops_host?: ReconciliationSource; asset_machine?: ReconciliationSource;
};
type NamedCount = { name: string; count: number };
type ReconciliationSummary = {
  total: number; by_scope: Record<string, number>; by_category: NamedCount[];
  by_type: NamedCount[]; gpu_models: NamedCount[]; generated_at: string;
};
type FreshnessSource = { status: string; observed_at?: string; age_seconds?: number; stale_after_seconds: number; source_mode?: string; collection_status?: string; collection_started_at?: string; collection_age_seconds?: number; collection_interval_seconds?: number; message?: string };
type DataFreshness = { overall_status: string; server_time: string; sources: Record<string, FreshnessSource> };
type GPUAsset = { id: number; asset_key: string; node_id: number; node_ip: string; gpu_index: number; gpu_uuid: string; device: string; model: string; model_name: string; pci_bus_id: string; host_serial: string; driver_version: string; state: string; sample_state: string; last_seen_at: string; last_synced_at: string };
type CollectorTarget = { id: number; job: string; instance: string; target_ip: string; node_ip: string; health: string; reason_code?: string; suppressed: boolean; suppression_reason?: string; last_error?: string; last_scrape_at?: string; last_synced_at: string };
type SyncRun = { id: number; task_type: string; status: string; node_count: number; gpu_count: number; known_uuid_count: number; target_count: number; change_count: number; started_at: string; finished_at?: string; error_message?: string };
type AssetChange = { id: number; sync_run_id: number; event_type: string; node_ip: string; asset_key: string; old_value: string; new_value: string; source: string; created_at: string };
type GPUHealthScore = { id: number; gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; score: number | null; level: string; data_confidence: string; stability_score: number; memory_score: number; thermal_score: number; power_score: number; interconnect_score: number; performance_score: number; evidence: string[]; metric_sources?: Record<string, string>; sources_available?: string[]; fallback_metric_count?: number; consistency_issues?: string[]; consistency_issue_count?: number; rule_version: string; evaluated_at: string };
type HealthSummary = { total: number; scored: number; unknown: number; average_score: number; by_level: Record<string, number>; by_confidence: Record<string, number>; latest_run: { id: number; rule_version: string; status: string; rule_hit_count: number; finished_at?: string } | null };
type FaultEvent = { id: number; source: string; state: string; gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; rule_code: string; domain: string; severity: string; evidence: string; observed_value: number; threshold: string; occurrence_count: number; rule_version: string; first_observed_at: string; last_observed_at: string; recovered_at?: string; issue_id?: number; workflow_status?: string; latest_resolution_id?: number };
type LocalizedText = { zh: string; en: string };
type FaultReportFinding = { code: string; severity: string; summary: LocalizedText; evidence_ids: string[] };
type FaultReportHypothesis = { code: string; status: string; title: LocalizedText; reason: LocalizedText; evidence_ids: string[] };
type FaultReportGap = { code: string; detail: LocalizedText };
type FaultReportTimeline = { at: string; evidence_id: string; label: LocalizedText };
type FaultAnalysisReport = {
  schema_version: string; report_version: string; generated_at: string; analysis_mode: string; event_id: number;
  title: LocalizedText; summary: LocalizedText; severity: string;
  affected_entity: { type: string; gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string };
  timeline: FaultReportTimeline[]; findings: FaultReportFinding[]; hypotheses: FaultReportHypothesis[];
  missing_evidence: FaultReportGap[]; recommended_readonly_checks: LocalizedText[];
  operator_actions: LocalizedText[]; limitations: LocalizedText[]; no_action_executed: boolean;
};
type CursorMeta = { total: number; limit: number; has_more: boolean; next_before_id: number };
type FaultEventSummary = { total: number; open: number; by_state: Record<string, number>; open_by_severity: Record<string, number> };
type PlatformIssue = { id: number; issue_key: string; category: string; issue_type: string; title: string; description: string; entity_type: string; entity_key: string; node_ip: string; gpu_uuid: string; severity: string; status: string; detection_state: string; detection_source: string; source_record_id: number; first_detected_at: string; last_detected_at: string; source_recovered_at?: string; resolved_at?: string; latest_resolution_id: number };
type IssueResolution = { id: number; issue_id: number; status: string; root_cause: string; solution: string; resolution_process: string; result: string; evidence: string[]; operator: string; training_eligible: boolean; created_at: string };
type IssueDetail = { issue: PlatformIssue; resolutions: IssueResolution[]; node_evidence_collection?: NodeEvidenceCollection | null };
type IssueSummary = { discovered: number; resolved: number; remaining: number; ignored: number; active_detection: number; training_eligible: number; by_category: Record<string, number>; resolved_by_category: Record<string, number>; remaining_by_category: Record<string, number>; active_by_category: Record<string, number>; by_status: Record<string, number>; by_severity: Record<string, number>; generated_at: string };
type TelemetryQualityItem = { gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; status: string; sample_count_1h?: number; presence_ratio_1h?: number; sample_age_seconds?: number; uuid_presence_flap_count_1h?: number; metric_gap_max_seconds_1h?: number; target_scrape_success_ratio_5m?: number; target_scrape_samples_ratio_5m?: number; target_scrape_duration_ratio_5m?: number; observed_at: string };
type TelemetryQualitySummary = { total: number; by_status: Record<string, number>; average_presence_ratio_1h: number; max_sample_age_seconds: number; max_metric_gap_seconds_1h?: number; max_uuid_flap_count_1h?: number; min_target_scrape_success_ratio_5m?: number; min_target_scrape_samples_ratio_5m?: number; max_target_scrape_duration_ratio_5m?: number; feature_catalog_version: string; evaluation_run_id: number; evaluated_at?: string };
type NodeAccessCommand = { id: string; category: string; approval_class: string; planning_status: string; collection_mode: string; purpose: LocalizedText; preview: string };
type NodeAccessSkill = { id: string; version: string; class: string; status: string; purpose: LocalizedText };
type AlertEvidencePolicy = { category: string; issue_types: string[]; semantics: string; collection_trigger: string; purpose: LocalizedText };
type NodeCredentialStatus = { id: string; priority: number; username_masked: string; auth_type: string; secret_provider: string; enabled: boolean; secret_available: boolean; status: string };
type NodeAccessCheck = { id: number; node_ip: string; status: string; credential_profile_id?: string; attempts: string[]; alert_required: boolean; no_credential_disclosed: boolean; no_command_executed: boolean; started_at: string; finished_at: string };
type NodeEvidenceRecord = { id: number; collection_id: number; command_id: string; kind: string; status: string; output?: string; output_bytes: number; truncated: boolean; observed_at: string };
type NodeEvidenceCollection = {
  id: number; fault_event_id?: number; platform_issue_id?: number; retry_of_collection_id?: number;
  node_ip: string; trigger: string; status: string; credential_profile_id?: string;
  command_count: number; output_bytes: number; output_truncated: boolean; failure_code?: string;
  no_credential_disclosed: boolean; read_only: boolean; started_at: string; finished_at: string;
  records?: NodeEvidenceRecord[];
};
type NodeAccessOverview = {
  skill_id: string; skill_version: string; status: string; enabled: boolean; execution_enabled: boolean;
  no_arbitrary_shell: boolean; no_change_executed: boolean; encryption_ready: boolean; management_ready: boolean; secure_write_only: boolean; insecure_http_allowed: boolean; connectivity_check_enabled: boolean; known_hosts_ready: boolean; default_read_only_collection: boolean;
  budget: { connect_timeout_seconds: number; command_timeout_seconds: number; max_output_bytes: number; max_concurrent_nodes: number; max_commands_per_node: number; max_log_lines: number; default_window_minutes: number };
  credential_profiles: NodeCredentialStatus[]; skills: NodeAccessSkill[]; commands: NodeAccessCommand[]; alert_evidence_policies: AlertEvidencePolicy[]; collection_summary: { waiting_recovery: number; completed: number; partial: number; failed: number }; boundaries: LocalizedText[]; generated_at: string;
};
type DegradationSummary = { version: string; mode: string; evaluated_gpus: number; eligible_gpus: number; baseline_ready_gpus: number; historical_baseline_gpus: number; candidate_gpus: number; insufficient_gpus: number; minimum_utilization: number; ratio_threshold: number; freshness_sla_seconds: number; by_model: Record<string, number>; latest_observed_at?: string; evaluated_at: string };
type DegradationCandidate = { gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; status: string; metric: string; observed_value: number; baseline_value: number; performance_ratio: number; gpu_utilization: number; peer_count: number; baseline_id?: number; baseline_scope: string; baseline_maturity: string; data_confidence: string; evidence: string[]; recommended_action: string; feature_version: string; observed_at: string; evaluated_at: string };
type PredictionModelSpec = { id: number; model_key: string; version: string; hardware_class: string; entity_type: string; task: string; horizon_minutes: number; algorithm: string; runtime: string; mode: string; status: string; feature_contract_version: string; label_contract_version: string; dataset_version?: string; source_baseline_build_id?: number; scope_event_type?: string; scope_model_name?: string; artifact_uri?: string; artifact_sha256?: string; registry_gate_version?: string; decision_threshold?: number; current: boolean };
type PredictionFeatureParityAudit = { id: number; model_spec_id: number; model_key: string; model_version: string; source_baseline_build_id: number; artifact_sha256: string; feature_contract_version: string; transformation_contract_version: string; status: string; training_feature_count: number; contract_matched_count: number; source_metric_count: number; missing_source_count: number; unsupported_transform_count: number; replay_verified_count: number; source_metrics: string[]; contract_matched_columns: string[]; missing_source_columns: string[]; unsupported_transform_columns: string[]; blocking_reasons: string[]; scoring_allowed: boolean; audited_at: string };
type PredictionFeatureReplayRun = { id: number; replay_key: string; version: string; status: string; model_spec_id: number; model_key: string; model_version: string; source_baseline_build_id: number; source_matrix_build_id: number; source_key: string; transformation_contract_version: string; requested_sample_count: number; selected_sample_count: number; completed_sample_count: number; failed_sample_count: number; training_feature_count: number; compared_value_count: number; verified_column_count: number; mismatch_count: number; missing_training_value_count: number; missing_replay_value_count: number; maximum_absolute_error: number; maximum_relative_error: number; failed_columns: string[]; blocking_reasons: string[]; output_dir: string; report_sha256?: string; error_message?: string; started_at: string; finished_at?: string };
type PredictionLiveCoverageAudit = { id: number; audit_key: string; version: string; status: string; model_spec_id: number; model_key: string; model_version: string; source_key: string; scope_model_name: string; window_minutes: number; query_step_seconds: number; expected_samples: number; minimum_samples: number; freshness_sla_seconds: number; source_metric_count: number; target_gpu_count: number; eligible_gpu_count: number; blocked_gpu_count: number; metric_pair_count: number; passing_metric_pair_count: number; missing_metric_pair_count: number; sparse_metric_pair_count: number; stale_metric_pair_count: number; eligible_ratio: number; blocking_reasons: string[]; report_sha256?: string; error_message?: string; started_at: string; finished_at?: string };
type PredictionShadowScoringRun = { id: number; run_key: string; version: string; status: string; trigger: string; model_spec_id: number; model_key: string; model_version: string; artifact_sha256: string; source_key: string; scope_model_name: string; transformation_contract_version: string; window_minutes: number; query_step_seconds: number; target_gpu_count: number; scored_gpu_count: number; blocked_gpu_count: number; positive_gpu_count: number; positive_ratio: number; minimum_probability?: number; maximum_probability?: number; mean_probability?: number; median_probability?: number; p90_probability?: number; p95_probability?: number; p99_probability?: number; maximum_node_mean?: number; all_above_threshold_nodes: number; distribution_status: string; blocking_reasons: string[]; no_alert_emitted: boolean; no_action_executed: boolean; report_sha256?: string; error_message?: string; started_at: string; finished_at?: string };
type HardwareRiskPrediction = { id: number; shadow_run_id?: number; model_spec_id: number; model_version?: string; entity_key: string; gpu_asset_id: number; gpu_uuid: string; node_ip: string; horizon_minutes: number; probability?: number; risk_level: string; status: string; feature_vector_sha256?: string; transformation_contract_version?: string; explanations: string[]; observed_at: string; evaluated_at: string; expires_at: string };
type PredictionReadinessSummary = { total: number; ready_for_dataset: number; blocked: number; by_reason: Record<string, number>; latest_observed_at?: string; probability_emitted: boolean; evaluated_at: string };
type PredictionReadinessItem = { hardware_class: string; entity_type: string; entity_key: string; gpu_asset_id: number; gpu_uuid: string; node_ip: string; gpu_index: number; model_name: string; status: string; data_confidence: string; feature_coverage: number; feature_snapshot_id: number; feature_catalog_version: string; observed_at: string; blocking_reasons: string[]; probability_emitted: boolean; no_action_executed: boolean };
type MonitoringHistoryAudit = { id: number; source_key: string; source_name: string; source_type: string; base_url: string; status: string; source_version?: string; configured_retention?: string; dcgm_target_count: number; gpu_exporter_target_count: number; current_gpu_series: number; scrape_interval_seconds?: number; metric_families: string[]; required_metric_families: string[]; missing_metric_families: string[]; capabilities: string[]; earliest_sample_at?: string; latest_sample_at?: string; error_message?: string; finished_at: string };
type MonitoringHistorySource = { id: string; name: string; type: string; base_url: string; tenant_id?: string; enabled: boolean; read_only: boolean; execution: string; dataset_dir: string; latest_audit?: MonitoringHistoryAudit };
type HistoryBackfillRun = { id: number; source_key: string; job_type: string; status: string; query_version: string; range_start: string; range_end: string; step_seconds: number; chunk_hours: number; chunks_total: number; chunks_completed: number; series_scanned: number; signal_points: number; candidates_created: number; candidates_updated: number; records_created: number; records_updated: number; records_annotated: number; error_message?: string; started_at: string; finished_at?: string };
type HistoricalFaultCandidate = { id: number; candidate_key: string; source_key: string; gpu_uuid: string; node_ip: string; hostname: string; model_name: string; pci_bus_id: string; event_type: string; event_code: string; event_message: string; severity: string; quality_tier: string; operational_priority: string; hardware_certainty: string; training_disposition: string; recommended_action: string; recovery_aware: boolean; review_status: string; reviewed_at?: string; reviewed_by?: string; review_note?: string; identity_evidence_status?: string; identity_interval_id?: number; identity_evidence?: Record<string, string>; rule_decision?: string; rule_decision_version?: string; rule_decision_reason?: string; rule_confidence?: number; rule_decided_at?: string; source_metric: string; source_alert_name: string; signal_samples: number; onset_at: string; detection_window_end_at: string };
type HistoricalCandidateSummary = { total: number; pending_review: number; by_review_status: Record<string, number>; by_rule_decision: Record<string, number>; by_event_code: Record<string, number>; by_quality_tier: Record<string, number>; by_operational_priority: Record<string, number>; by_hardware_certainty: Record<string, number>; by_training_disposition: Record<string, number>; by_model: Record<string, number>; earliest_onset_at?: string; latest_onset_at?: string };
type HistoricalGPUIdentityInterval = { id: number; interval_key: string; source_key: string; backfill_run_id: number; node_ip: string; host_id?: string; host_serial?: string; hostname?: string; data_center_id?: string; gpu_index: number; gpu_uuid: string; pci_bus_id?: string; model_name?: string; driver_version?: string; first_seen_at: string; last_seen_at: string; observation_count: number; transition_type: string; predecessor_uuid?: string; transition_at?: string; evidence_strength: string };
type HistoricalIdentitySummary = { total: number; by_transition_type: Record<string, number>; by_evidence_strength: Record<string, number>; candidate_evidence: Record<string, number>; earliest_seen_at?: string; latest_seen_at?: string };
type TrainingDatasetBuild = { id: number; dataset_key: string; version: string; status: string; source_key: string; horizons: string[]; candidate_count: number; eligible_candidate_count: number; episode_count: number; window_count: number; pending_review_count: number; identity_missing_count: number; context_only_count: number; excluded_count: number; output_dir: string; manifest_path?: string; window_manifest_path?: string; window_manifest_sha256?: string; error_message?: string; started_at: string; finished_at?: string };
type TrainingFeatureBuild = { id: number; feature_dataset_key: string; version: string; status: string; source_key: string; source_dataset_build_id: number; source_dataset_key: string; feature_contract_version: string; lookback_minutes: number; query_step_seconds: number; episode_count: number; window_count: number; processed_episodes: number; completed_windows: number; failed_windows: number; metric_count: number; feature_column_count: number; average_metric_coverage: number; minimum_metric_coverage: number; output_dir: string; feature_path?: string; feature_sha256?: string; quality_report_path?: string; error_message?: string; started_at: string; finished_at?: string };
type TrainingPreparationBuild = { id: number; prepared_dataset_key: string; version: string; status: string; source_feature_build_id: number; source_feature_dataset_key: string; minimum_metric_coverage: number; source_window_count: number; eligible_positive_count: number; telemetry_censored_count: number; low_coverage_count: number; extraction_failed_count: number; positive_discontinuous_count: number; label_ineligible_count: number; correlated_event_count: number; entity_time_conflict_count: number; train_count: number; validation_count: number; test_count: number; control_request_count: number; control_shortfall_count: number; train_end_at?: string; validation_end_at?: string; output_dir: string; manifest_path?: string; prepared_samples_path?: string; prepared_samples_sha256?: string; control_requests_path?: string; control_requests_sha256?: string; error_message?: string; started_at: string; finished_at?: string };
type TrainingControlFeatureBuild = { id: number; control_feature_dataset_key: string; version: string; status: string; source_preparation_build_id: number; source_prepared_dataset_key: string; source_key: string; feature_contract_version: string; request_count: number; unique_window_count: number; processed_unique_windows: number; completed_request_count: number; eligible_request_count: number; telemetry_censored_count: number; low_coverage_count: number; discontinuous_count: number; load_unknown_count: number; load_mismatch_count: number; extraction_failed_count: number; output_dir: string; feature_path?: string; feature_sha256?: string; quality_report_path?: string; error_message?: string; started_at: string; finished_at?: string };
type TrainingMatrixBuild = { id: number; training_matrix_key: string; version: string; status: string; source_preparation_build_id: number; source_prepared_dataset_key: string; source_control_build_id: number; source_control_dataset_key: string; feature_contract_version: string; feature_column_count: number; positive_count: number; control_count: number; sample_count: number; train_positive_count: number; train_control_count: number; validation_positive_count: number; validation_control_count: number; test_positive_count: number; test_control_count: number; duplicate_count: number; entity_split_conflict_count: number; point_in_time_violation_count: number; pairing_violation_count: number; contract_violation_count: number; output_dir: string; matrix_path?: string; matrix_sha256?: string; manifest_path?: string; error_message?: string; started_at: string; finished_at?: string };
type CohortSplitReadiness = { positive_count: number; control_count: number; positive_gpus: number; control_gpus: number };
type CohortStratumReadiness = { event_type: string; model_name: string; horizon_minutes: number; status: string; blocking_reasons: string[]; splits: Record<string, CohortSplitReadiness> };
type MatrixReadinessReport = { matrix_key: string; policy: Record<string, number>; ready_strata: number; insufficient_strata: number; strata: CohortStratumReadiness[] };
type BaselineModelBuild = { id: number; baseline_model_key: string; version: string; status: string; algorithm: string; source_matrix_build_id: number; feature_contract_version: string; scope_event_type?: string; scope_model_name?: string; readiness_gate_version?: string; feature_column_count: number; feature_audit_status?: string; excluded_feature_count: number; prohibited_feature_count: number; statistically_stable_count: number; shadow_candidate_count: number; horizon_count: number; trained_model_count: number; train_count: number; validation_count: number; test_count: number; test_macro_roc_auc: number; test_macro_pr_auc: number; test_macro_precision: number; test_macro_recall: number; output_dir: string; artifact_sha256?: string; error_message?: string };
type BaselineMetrics = { count: number; positive: number; control: number; roc_auc: number; pr_auc: number; precision: number; recall: number; f1: number; brier: number };
type BaselineUncertainty = { version: string; method: string; resamples: number; confidence_level: number; entity_count: number; roc_auc_lower: number; roc_auc_upper: number; pr_auc_lower: number; pr_auc_upper: number; null_pr_auc: number; status: string };
type BaselineCalibration = { version: string; status: string; bin_count: number; ece: number; model_brier: number; null_brier: number; brier_skill_score: number };
type BaselineEvaluationReport = { matrix_key: string; horizons: { horizon_minutes: number; test_by_event_type: Record<string, BaselineMetrics>; validation_uncertainty?: BaselineUncertainty; test_uncertainty?: BaselineUncertainty; cross_split_status?: string; raw_test_calibration?: BaselineCalibration; test_calibration?: BaselineCalibration; release_readiness?: string }[]; by_test_event_type: Record<string, BaselineMetrics>; by_test_driver_version: Record<string, BaselineMetrics>; by_test_label_source: Record<string, BaselineMetrics>; by_test_hardware_certainty: Record<string, BaselineMetrics>; by_test_rule_version: Record<string, BaselineMetrics> };
type TrainingCohortPolicy = { version: string; positive_horizons_minutes: number[]; healthy_censor_before_hours: number; healthy_censor_after_hours: number; control_match_dimensions: string[]; normal_range_statistics: string[]; positive_candidate_policy: string; healthy_window_policy: string; replacement_evidence_policy: string };
type FailureLabel = { id: number; label_key: string; hardware_class: string; entity_type: string; entity_key: string; gpu_asset_id?: number; gpu_uuid?: string; node_ip?: string; model_name?: string; event_type: string; rule_version?: string; label_value: number; quality_tier: string; source_type: string; source_record_id: number; confirmation_resolution_id?: number; label_contract_version: string; occurred_at: string; available_at: string; confirmed_at?: string; excluded: boolean; exclusion_reason?: string };
type RankingAtK = { k: number; eligible: number; positives: number; hits: number; precision?: number; recall?: number; ndcg?: number; lift?: number };
type AccuracyMetrics = { tp: number; fp: number; fn: number; tn: number; evaluated: number; precision?: number; recall?: number; specificity?: number; false_positive_rate?: number; false_negative_rate?: number; accuracy?: number; ranking_at_k?: RankingAtK[]; node_ranking_at_k?: RankingAtK[] };
type PredictionAccuracy = { rule: AccuracyMetrics; final: AccuracyMetrics; pending: number; censored: number; human_overrides: number; rule_decision_version: string; by_model: Array<{ model_key: string; model_version: string; horizon_minutes: number; rule: AccuracyMetrics; final: AccuracyMetrics }>; evaluated_at: string };
type BaselineComparison = { name: string; prediction_policy: string; rule: AccuracyMetrics; final: AccuracyMetrics };
type PredictionOutcomeReport = { version: string; framework_version: string; mode: string; safety: { read_only_shadow: boolean; no_alert_emitted: boolean; no_action_taken: boolean; probability_use: string }; sample_maturity: { total: number; matured: number; pending: number; censored: number; matured_ratio: number; probability_scored: number; node_eligible: number }; accuracy: PredictionAccuracy; baseline_comparisons?: BaselineComparison[]; interpretation: string[]; recommended_next_run: string[]; generated_at: string };
type PredictionModelGovernance = { version: string; framework_version: string; mode: string; dataset: { version?: string; dataset_key?: string; source_key?: string; candidate_count: number; eligible_candidate_count: number; episode_count: number; window_count: number; pending_review_count: number; identity_missing_count: number; context_only_count: number; excluded_count: number; feature_dataset_key?: string; feature_contract_version?: string; feature_column_count: number; average_metric_coverage?: number; minimum_metric_coverage?: number; prepared_dataset_key?: string; eligible_positive_count: number; telemetry_censored_count: number; low_coverage_count: number; extraction_failed_count: number; train_count: number; validation_count: number; test_count: number; matrix_key?: string; matrix_sha256?: string; matrix_sample_count: number; matrix_positive_count: number; matrix_control_count: number; duplicate_count: number; point_in_time_violation_count: number; entity_split_conflict_count: number; contract_violation_count: number; finished_at?: string }; models: Array<{ model_key: string; model_version: string; horizon_minutes: number; algorithm: string; runtime: string; mode: string; status: string; feature_contract_version: string; label_contract_version: string; source_baseline_build_id?: number; artifact_sha256?: string; registry_gate_version?: string; decision_threshold?: number; baseline_version?: string; baseline_status?: string; feature_audit_status?: string; prohibited_feature_count: number; statistically_stable_count: number; shadow_candidate_count: number; train_count: number; validation_count: number; test_count: number; test_macro_roc_auc?: number; test_macro_pr_auc?: number; test_macro_precision?: number; test_macro_recall?: number }>; shadow_gates: { feature_parity_status?: string; replay_status?: string; replay_verified_columns: number; replay_compared_values: number; replay_mismatch_count: number; live_coverage_status?: string; live_coverage_eligible_ratio?: number; shadow_run_status?: string; shadow_distribution_status?: string; scored_gpu_count: number; positive_gpu_count: number; positive_ratio?: number; no_alert_emitted: boolean; no_action_executed: boolean; evaluated_at?: string }; limitations: string[]; recommended_next_run: string[]; generated_at: string };
type HeaRankChallengerReport = { version: string; framework_version: string; mode: string; status: string; target_horizon_minutes: number; sample_summary: { total: number; matured: number; pending: number; censored: number; matured_ratio: number; probability_scored: number; node_eligible: number }; all_matured: Array<{ policy: string; description: string; rows: number; nodes: number; positives: number; ranking_at_k?: RankingAtK[] }>; seven_day: Array<{ policy: string; description: string; rows: number; nodes: number; positives: number; ranking_at_k?: RankingAtK[] }>; blocking_reasons: string[]; interpretation: string[]; recommended_next_run: string[]; generated_at: string };
type PredictionOutcome = { id: number; prediction_id: number; model_key: string; model_version: string; gpu_uuid?: string; node_ip?: string; horizon_minutes: number; probability?: number; decision_threshold?: number; predicted_positive: boolean; prediction_evaluated_at: string; window_end_at: string; maturity_status: string; maturity_reason?: string; rule_actual_value?: number; rule_outcome: string; rule_label_quality?: string; human_actual_value?: number; human_outcome?: string; human_reason?: string; human_decided_by?: string; human_decided_at?: string; final_actual_value?: number; final_outcome: string; final_source: string };
type PredictionOverview = { framework_version: string; phase: string; mode: string; scoring_enabled: boolean; probability_emitted: boolean; no_action_executed: boolean; feature_contract_version: string; label_contract_version: string; feature_catalog_version: string; horizons_minutes: number[]; models: PredictionModelSpec[]; readiness: PredictionReadinessSummary; results: { total: number; current: number; probability_emitted: boolean }; labels: { total: number; confirmed: number; strong_proxy: number; weak_proxy: number; excluded: number; affected_gpus: number; by_event_type: Record<string, number>; by_model: Record<string, number>; materialized: boolean; latest_available_at?: string }; retention: { online_retention_days: number; cold_archive_status: string; training_history_safe: boolean }; label_policy: { confirmed_positive: string[]; weak_positive: string[]; excluded_as_positive: string[]; point_in_time_rule: string; entity_isolation: string }; release_gates: { minimum_precision: number; minimum_recall: number; calibration: string; time_split: boolean; entity_split: boolean; shadow_required: boolean; auto_action: boolean } };
type FleetSummary = {
  nodes: { total: number; by_state: Record<string, number> };
  gpus: { total: number; known_uuid: number; unknown_uuid: number; by_state: Record<string, number> };
  targets: { by_health: Record<string, number> };
  latest_sync: SyncRun | null;
};

const pageIcons = { overview: BarChart3, gpus: Cpu, issues: ClipboardList, incidents: Bell, validations: Gauge, quality: Database, models: BrainCircuit, about: BookOpen };
const pages: PageId[] = ['overview', 'gpus', 'incidents', 'validations', 'issues', 'quality', 'models', 'about'];
const pageCopy = (tx: Tx): Record<PageId, { label: string; group: string; title: string; desc: string }> => ({
  overview: { label: tx('集群总览', 'Overview'), group: tx('运行', 'OPERATIONS'), title: tx('GPU 集群', 'GPU Fleet'), desc: tx('资产、状态、事件与交付进度', 'Assets, status, incidents and delivery') },
  gpus: { label: tx('GPU 资产', 'GPU Assets'), group: tx('运行', 'OPERATIONS'), title: tx('GPU 资产', 'GPU Assets'), desc: tx('健康、异常、性能与取值来源', 'Health, anomaly, performance and source lineage') },
  issues: { label: tx('数据统计', 'Data Statistics'), group: tx('运行', 'OPERATIONS'), title: tx('数据问题统计与处置', 'Data Issue Analytics & Resolution'), desc: tx('数据、资产与可用性问题的发现、分类、状态及训练记录', 'Discovery, categories, status and training records for data, inventory and availability issues') },
  incidents: { label: tx('告警中心', 'Alert Center'), group: tx('运行', 'OPERATIONS'), title: tx('告警中心', 'Alert Center'), desc: tx('硬件告警、接收记录、详情与人工处置', 'Hardware alerts, ingestion records, details and operator resolution') },
  validations: { label: tx('性能验证', 'Validation'), group: tx('运行', 'OPERATIONS'), title: tx('性能验证', 'Performance Validation'), desc: tx('算力衰减检测与维护窗口复测', 'Degradation detection and maintenance validation') },
  quality: { label: tx('数据质量', 'Data Quality'), group: tx('系统', 'SYSTEM'), title: tx('数据质量', 'Data Quality'), desc: tx('采集覆盖、身份映射与能力矩阵', 'Coverage, identity mapping and capability matrix') },
  models: { label: tx('规则与模型', 'Rules & Models'), group: tx('系统', 'SYSTEM'), title: tx('规则与模型', 'Rules & Models'), desc: tx('健康评分、异常检测与故障预测', 'Health score, anomaly detection and failure prediction') },
  about: { label: tx('平台概览', 'Platform Overview'), group: tx('系统', 'SYSTEM'), title: tx('平台概览', 'Platform Overview'), desc: tx('定位、能力模块、版本与路线', 'Positioning, capability modules, versions and roadmap') },
});
const subPages = (page: PageId, tx: Tx): SubPage[] => ({
  overview: [],
  gpus: [{ id: 'health', label: tx('GPU 健康', 'GPU Health') }, { id: 'inventory', label: tx('资产清单', 'Inventory') }],
  issues: [{ id: 'ledger', label: tx('问题台账', 'Issue Ledger') }, { id: 'assets', label: tx('资产统计', 'Asset Statistics') }],
  incidents: [{ id: 'hardware', label: tx('硬件事件', 'Hardware Events') }, { id: 'ingestion', label: tx('接收记录', 'Ingestion Records') }, { id: 'workflow', label: tx('处理流程', 'Workflow') }],
  validations: [{ id: 'degradation', label: tx('衰减检测', 'Degradation') }, { id: 'records', label: tx('验证记录', 'Records') }],
  quality: [{ id: 'targets', label: tx('采集覆盖', 'Target Coverage') }, { id: 'continuity', label: tx('指标连续性', 'Metric Continuity') }, { id: 'issues', label: tx('问题统计', 'Issue Statistics') }, { id: 'node-access', label: tx('节点访问', 'Node Access') }, { id: 'identity', label: tx('身份与带外', 'Identity & BMC') }, { id: 'audit', label: tx('同步审计', 'Sync Audit') }],
  models: [{ id: 'stack', label: tx('决策分层', 'Decision Stack') }, { id: 'prediction', label: tx('故障预测', 'Failure Prediction') }, { id: 'algorithms', label: tx('算法与约束', 'Algorithms & Gates') }],
  about: [{ id: 'definition', label: tx('能力与里程碑', 'Capabilities & Milestones') }, { id: 'architecture', label: tx('架构与边界', 'Architecture & Boundaries') }, { id: 'settings', label: tx('平台配置', 'Platform Settings') }],
}[page]);

function time(value?: string, lang = 'zh-CN') {
  if (!value) return '—';
  const date = new Date(value); if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(lang, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(date);
}
function compareIP(left: string, right: string) { const a = left.split('.').map(Number); const b = right.split('.').map(Number); for (let i = 0; i < Math.max(a.length, b.length); i += 1) { const delta = (a[i] || 0) - (b[i] || 0); if (delta) return delta; } return left.localeCompare(right); }
function tone(level?: string) { const v = (level || '').toLowerCase(); return v === 'critical' || v === 'error' ? 'danger' : v === 'warning' || v === 'attention' ? 'warning' : v === 'info' ? 'info' : v === 'healthy' || v === 'recovered' ? 'healthy' : 'neutral'; }
function issueTone(status?: string) { const value = (status || '').toLowerCase(); return value === 'resolved' ? 'healthy' : value === 'in_progress' ? 'info' : value === 'open' ? 'warning' : 'neutral'; }
function Badge({ value, kind = 'neutral' }: { value: string; kind?: string }) { return <span className={`badge ${kind}`}><i />{value}</span>; }
function Card({ children, className = '' }: { children: ReactNode; className?: string }) { return <section className={`card ${className}`}>{children}</section>; }
function CardHead({ code, title, action }: { code?: string; title: string; action?: ReactNode }) { return <div className="card-head"><div>{code && <span>{code}</span>}<h2>{title}</h2></div>{action}</div>; }
class PageErrorBoundary extends Component<{ children: ReactNode; title: string }, { error: Error | null }> {
  state = { error: null as Error | null };
  static getDerivedStateFromError(error: Error) { return { error }; }
  componentDidCatch(error: Error, info: ErrorInfo) { console.error('Atlas page render failed', error, info.componentStack); }
  render() {
    if (this.state.error) return <Card className="span-12"><CardHead code="RENDER ERROR" title={this.props.title} /><p className="node-access-note">{this.state.error.message || 'Unknown page rendering error'}</p></Card>;
    return this.props.children;
  }
}

export default function App() {
  const { i18n } = useTranslation();
  const zh = i18n.language.startsWith('zh');
  const tx: Tx = (cn, en) => zh ? cn : en;
  const copy = pageCopy(tx);
  const initial = window.location.hash.replace('#/', '') as PageId;
  const [page, setPage] = useState<PageId>(pages.includes(initial) ? initial : 'overview');
  const [sidebar, setSidebar] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [ingestions, setIngestions] = useState<Ingestion[]>([]);
  const [ingestionMeta, setIngestionMeta] = useState<IngestionMeta>({ total: 0, all_total: 0, limit: 100, has_more: false, next_before_id: 0, received_5m: 0, received_1h: 0, source_mode: 'unknown', stream_status: 'empty' });
  const [ingestionBeforeID, setIngestionBeforeID] = useState(0);
  const [ingestionCursorHistory, setIngestionCursorHistory] = useState<number[]>([]);
  const [ingestionQuery, setIngestionQuery] = useState('');
  const [ingestionLevel, setIngestionLevel] = useState('');
  const [ingestionLoading, setIngestionLoading] = useState(false);
  const [ingestionRefresh, setIngestionRefresh] = useState(0);
  const [failures, setFailures] = useState<Failure[]>([]);
  const [status, setStatus] = useState<ServerStatus | null>(null);
  const [platformConfig, setPlatformConfig] = useState<PlatformConfig>({ instance_name: 'Atlas Cluster', product_name: 'ATLAS', product_tagline: 'GPU RELIABILITY', environment: 'LOCAL' });
  const [freshness, setFreshness] = useState<DataFreshness | null>(null);
  const [summary, setSummary] = useState<FleetSummary | null>(null);
  const [gpuAssets, setGPUAssets] = useState<GPUAsset[]>([]);
  const [targets, setTargets] = useState<CollectorTarget[]>([]);
  const [syncRuns, setSyncRuns] = useState<SyncRun[]>([]);
  const [assetChanges, setAssetChanges] = useState<AssetChange[]>([]);
  const [healthScores, setHealthScores] = useState<GPUHealthScore[]>([]);
  const [healthSummary, setHealthSummary] = useState<HealthSummary | null>(null);
  const [faultEvents, setFaultEvents] = useState<FaultEvent[]>([]);
  const [faultMeta, setFaultMeta] = useState<CursorMeta>({ total: 0, limit: 100, has_more: false, next_before_id: 0 });
  const [faultBeforeID, setFaultBeforeID] = useState(0);
  const [faultCursorHistory, setFaultCursorHistory] = useState<number[]>([]);
  const [faultLoading, setFaultLoading] = useState(false);
  const [faultSummary, setFaultSummary] = useState<FaultEventSummary | null>(null);
  const [inventoryError, setInventoryError] = useState(false);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const initialLoadComplete = useRef(false);
  const [selected, setSelected] = useState<number | null>(null);
  const [report, setReport] = useState<Report | null>(null);
  const [level, setLevel] = useState('');
  const [issueSummary, setIssueSummary] = useState<IssueSummary | null>(null);
  const [issues, setIssues] = useState<PlatformIssue[]>([]);
  const [issueMeta, setIssueMeta] = useState<CursorMeta>({ total: 0, limit: 50, has_more: false, next_before_id: 0 });
  const [issueBeforeID, setIssueBeforeID] = useState(0);
  const [issueCursorHistory, setIssueCursorHistory] = useState<number[]>([]);
  const [issueCategory, setIssueCategory] = useState('');
  const [issueStatus, setIssueStatus] = useState('');
  const [issueQuery, setIssueQuery] = useState('');
  const [issueLoading, setIssueLoading] = useState(false);
  const [selectedIssueID, setSelectedIssueID] = useState<number | null>(null);
  const [issueDetail, setIssueDetail] = useState<IssueDetail | null>(null);
  const [issueRefresh, setIssueRefresh] = useState(0);
  const [selectedFaultReportID, setSelectedFaultReportID] = useState<number | null>(null);
  const [faultReport, setFaultReport] = useState<FaultAnalysisReport | null>(null);
  const [faultReportLoading, setFaultReportLoading] = useState(false);
  const [faultReportError, setFaultReportError] = useState('');
  const [telemetryQuality, setTelemetryQuality] = useState<TelemetryQualityItem[]>([]);
  const [telemetryQualitySummary, setTelemetryQualitySummary] = useState<TelemetryQualitySummary | null>(null);
  const [degradationSummary, setDegradationSummary] = useState<DegradationSummary | null>(null);
  const [degradationCandidates, setDegradationCandidates] = useState<DegradationCandidate[]>([]);
  const [nodeAccess, setNodeAccess] = useState<NodeAccessOverview | null>(null);
  const [subPage, setSubPage] = useState<Record<PageId, string>>({ overview: '', gpus: 'health', issues: 'ledger', incidents: 'hardware', validations: 'degradation', quality: 'targets', models: 'stack', about: 'definition' });

  const navigate = (id: PageId) => { setPage(id); setSidebar(false); window.location.hash = `/${id}`; window.scrollTo(0, 0); };
  const load = async () => {
    if (!initialLoadComplete.current) setLoading(true);
    setRefreshing(true);
    try {
      const [s, df, f, fs, ga, ct, sr, ac, hs, hg, fes, is, pc, ds, dc, tq, na] = await Promise.all([
        fetch('/api/v1/status'), fetch('/api/v1/data-freshness'), fetch('/api/v1/alerts/failures?limit=8'),
        fetch('/api/v1/fleet/summary'), fetch('/api/v1/gpus?limit=2000'), fetch('/api/v1/targets?limit=2000'),
        fetch('/api/v1/sync-runs?limit=20'), fetch('/api/v1/inventory/changes?limit=50'),
        fetch('/api/v1/health/summary'), fetch('/api/v1/health/gpus?limit=1000'), fetch('/api/v1/fault-events/summary'), fetch('/api/v1/issues/summary'),
        fetch('/api/v1/platform-config'),
        fetch('/api/v1/degradation/summary'), fetch('/api/v1/degradation/candidates'), fetch('/api/v1/health/telemetry-quality'),
        fetch('/api/v1/node-access/overview'),
      ]);
      if (s.ok) setStatus(await s.json());
      if (df.ok) setFreshness(await df.json()); else setFreshness({ overall_status: 'error', server_time: new Date().toISOString(), sources: {} });
      if (f.ok) { const data = await f.json(); setFailures(Array.isArray(data.items) ? data.items : []); }
      if (fs.ok && ga.ok && ct.ok) {
        const [fleetData, gpuData, targetData] = await Promise.all([fs.json(), ga.json(), ct.json()]);
        setSummary(fleetData.data || null);
        setGPUAssets(Array.isArray(gpuData.data) ? gpuData.data : []);
        setTargets(Array.isArray(targetData.data) ? targetData.data : []);
        setInventoryError(false);
      } else setInventoryError(true);
      if (sr.ok) { const data = await sr.json(); setSyncRuns(Array.isArray(data.data) ? data.data : []); }
      if (ac.ok) { const data = await ac.json(); setAssetChanges(Array.isArray(data.data) ? data.data : []); }
      if (hs.ok) { const data = await hs.json(); setHealthSummary(data.data || null); }
      if (hg.ok) { const data = await hg.json(); setHealthScores(Array.isArray(data.data) ? data.data : []); }
      if (fes.ok) { const data = await fes.json(); setFaultSummary(data.data || null); }
      if (is.ok) { const data = await is.json(); setIssueSummary(data.data || null); }
      if (pc.ok) { const data = await pc.json(); if (data.data) setPlatformConfig(data.data); }
      if (ds.ok) { const data = await ds.json(); setDegradationSummary(data.data || null); }
      if (dc.ok) { const data = await dc.json(); setDegradationCandidates(Array.isArray(data.data) ? data.data : []); }
      if (tq.ok) { const data = await tq.json(); setTelemetryQuality(Array.isArray(data.data) ? data.data : []); setTelemetryQualitySummary(data.summary || null); }
      if (na.ok) { const data = await na.json(); setNodeAccess(data.data || null); }
    } catch { setInventoryError(true); } finally {
      initialLoadComplete.current = true;
      setLoading(false);
      setRefreshing(false);
    }
  };
  useEffect(() => { load(); const id = window.setInterval(load, 30000); return () => window.clearInterval(id); }, []);
  useEffect(() => { document.title = `${platformConfig.instance_name} · ${platformConfig.product_name}`; }, [platformConfig]);
  useEffect(() => {
    let cancelled = false;
    const loadPage = async () => {
      setIngestionLoading(true);
      try {
        const params = new URLSearchParams({ limit: '100' });
        if (ingestionBeforeID > 0) params.set('before_id', String(ingestionBeforeID));
        if (ingestionQuery.trim()) params.set('query', ingestionQuery.trim());
        if (ingestionLevel) params.set('level', ingestionLevel);
        const response = await fetch(`/api/v1/alerts/ingestions?${params.toString()}`);
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const data = await response.json();
        if (cancelled) return;
        setIngestions(Array.isArray(data.items) ? data.items : []);
        setIngestionMeta({
          total: Number(data.total || 0), all_total: Number(data.all_total ?? data.total ?? 0), limit: Number(data.limit || 100),
          has_more: Boolean(data.has_more), next_before_id: Number(data.next_before_id || 0),
          latest_received_at: data.latest_received_at || undefined, received_5m: Number(data.received_5m || 0), received_1h: Number(data.received_1h || 0),
          source_mode: String(data.source_mode || 'unknown'), stream_status: String(data.stream_status || 'unknown'), server_time: data.server_time || undefined,
        });
      } catch {
        if (!cancelled) setIngestionMeta(current => ({ ...current, stream_status: 'error' }));
      } finally {
        if (!cancelled) setIngestionLoading(false);
      }
    };
    const debounce = window.setTimeout(loadPage, ingestionQuery ? 250 : 0);
    const interval = window.setInterval(loadPage, 30000);
    return () => { cancelled = true; window.clearTimeout(debounce); window.clearInterval(interval); };
  }, [ingestionBeforeID, ingestionQuery, ingestionLevel, ingestionRefresh]);
  useEffect(() => { setIngestionBeforeID(0); setIngestionCursorHistory([]); }, [ingestionQuery, ingestionLevel]);
  useEffect(() => {
    let cancelled = false;
    const loadFaultPage = async () => {
      setFaultLoading(true);
      try {
        const params = new URLSearchParams({ limit: '100' });
        if (faultBeforeID > 0) params.set('before_id', String(faultBeforeID));
        if (query.trim()) params.set('q', query.trim());
        if (level) params.set('severity', level);
        const response = await fetch(`/api/v1/fault-events?${params.toString()}`);
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const payload = await response.json();
        if (cancelled) return;
        setFaultEvents(Array.isArray(payload.data) ? payload.data : []);
        setFaultMeta({ total: Number(payload.meta?.total || 0), limit: Number(payload.meta?.limit || 100), has_more: Boolean(payload.meta?.has_more), next_before_id: Number(payload.meta?.next_before_id || 0) });
      } catch { if (!cancelled) setFaultMeta(current => ({ ...current, has_more: false })); }
      finally { if (!cancelled) setFaultLoading(false); }
    };
    const debounce = window.setTimeout(loadFaultPage, query ? 250 : 0);
    return () => { cancelled = true; window.clearTimeout(debounce); };
  }, [faultBeforeID, query, level, ingestionRefresh, issueRefresh]);
  useEffect(() => { setFaultBeforeID(0); setFaultCursorHistory([]); }, [query, level]);
  useEffect(() => {
    let cancelled = false;
    const loadIssues = async () => {
      setIssueLoading(true);
      try {
        const params = new URLSearchParams({ limit: '50' });
        if (issueBeforeID > 0) params.set('before_id', String(issueBeforeID));
        if (issueCategory) params.set('category', issueCategory);
        if (issueStatus) params.set('status', issueStatus);
        if (issueQuery.trim()) params.set('q', issueQuery.trim());
        const response = await fetch(`/api/v1/issues?${params.toString()}`);
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const payload = await response.json();
        if (cancelled) return;
        setIssues(Array.isArray(payload.data) ? payload.data : []);
        setIssueMeta({ total: Number(payload.meta?.total || 0), limit: Number(payload.meta?.limit || 50), has_more: Boolean(payload.meta?.has_more), next_before_id: Number(payload.meta?.next_before_id || 0) });
      } catch { if (!cancelled) setIssueMeta(current => ({ ...current, has_more: false })); }
      finally { if (!cancelled) setIssueLoading(false); }
    };
    const debounce = window.setTimeout(loadIssues, issueQuery ? 250 : 0);
    return () => { cancelled = true; window.clearTimeout(debounce); };
  }, [issueBeforeID, issueCategory, issueStatus, issueQuery, issueRefresh]);
  useEffect(() => { setIssueBeforeID(0); setIssueCursorHistory([]); }, [issueCategory, issueStatus, issueQuery]);
  useEffect(() => {
    if (!selectedIssueID) { setIssueDetail(null); return; }
    let cancelled = false;
    fetch(`/api/v1/issues/${selectedIssueID}`).then(response => response.ok ? response.json() : null).then(payload => { if (!cancelled) setIssueDetail(payload?.data || null); }).catch(() => { if (!cancelled) setIssueDetail(null); });
    return () => { cancelled = true; };
  }, [selectedIssueID, issueRefresh]);
  useEffect(() => {
    if (!selectedFaultReportID) { setFaultReport(null); setFaultReportError(''); setFaultReportLoading(false); return; }
    let cancelled = false;
    setFaultReport(null); setFaultReportError(''); setFaultReportLoading(true);
    fetch(`/api/v1/fault-events/${selectedFaultReportID}/report`)
      .then(async response => {
        const payload = await response.json().catch(() => null);
        if (!response.ok) throw new Error(payload?.error?.message || `HTTP ${response.status}`);
        return payload?.data as FaultAnalysisReport;
      })
      .then(payload => { if (!cancelled) setFaultReport(payload); })
      .catch(reason => { if (!cancelled) setFaultReportError(reason instanceof Error ? reason.message : String(reason)); })
      .finally(() => { if (!cancelled) setFaultReportLoading(false); });
    return () => { cancelled = true; };
  }, [selectedFaultReportID]);
  useEffect(() => {
    const key = (e: KeyboardEvent) => { if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setSearchOpen(true); } if (e.key === '/' && !['INPUT', 'TEXTAREA'].includes((e.target as HTMLElement).tagName)) { e.preventDefault(); setSearchOpen(true); } };
    window.addEventListener('keydown', key); return () => window.removeEventListener('keydown', key);
  }, []);
  useEffect(() => {
    if (!selected) { setReport(null); return; }
    fetch(`/api/v1/alerts/ingestions/${selected}/analysis`).then(r => r.ok ? r.json() : null).then(setReport).catch(() => setReport(null));
  }, [selected]);

  const selectedItem = ingestions.find(x => x.id === selected) || null;
  const fleetModels = useMemo(() => {
    const counts = new Map<string, number>();
    gpuAssets.forEach(x => { const name = x.model_name || x.model || 'UNKNOWN'; counts.set(name, (counts.get(name) || 0) + 1); });
    return [...counts.entries()].map(([model, count]) => ({ model, short: model.replace(/^NVIDIA\s+/i, '').replace('GeForce ', ''), count })).sort((a, b) => b.count - a.count);
  }, [gpuAssets]);
  const filtered = ingestions;
  const openFaults = faultEvents.filter(x => x.state === 'open');
  const hosts = new Set(openFaults.map(x => x.node_ip).filter(Boolean)).size;
  const lang = zh ? 'zh-CN' : 'en-US';
  const freshnessLatest = useMemo(() => {
    const values = Object.values(freshness?.sources || {}).map(x => x.observed_at).filter((x): x is string => Boolean(x));
    return values.sort((a, b) => new Date(b).getTime() - new Date(a).getTime())[0];
  }, [freshness]);
  const freshnessStatus = (freshness?.overall_status || (loading ? 'syncing' : 'unknown')).toUpperCase();
  const freshnessTone = freshnessStatus === 'FRESH' ? 'healthy' : freshnessStatus === 'PARTIAL' || freshnessStatus === 'SNAPSHOT' ? 'info' : freshnessStatus === 'STALE' || freshnessStatus === 'OVERDUE' ? 'warning' : freshnessStatus === 'ERROR' ? 'danger' : 'neutral';
  const collectionSources = Object.entries(freshness?.sources || {}).filter(([, source]) => source.collection_status === 'running' || source.collection_status === 'overdue');
  const overdueSources = collectionSources.filter(([, source]) => source.collection_status === 'overdue');
  const currentSubPages = subPages(page, tx);
  const nextIngestionPage = () => {
    if (!ingestionMeta.has_more || !ingestionMeta.next_before_id) return;
    setIngestionCursorHistory(current => [...current, ingestionBeforeID]);
    setIngestionBeforeID(ingestionMeta.next_before_id);
  };
  const previousIngestionPage = () => {
    if (ingestionCursorHistory.length === 0) return;
    const previous = ingestionCursorHistory[ingestionCursorHistory.length - 1];
    setIngestionCursorHistory(current => current.slice(0, -1));
    setIngestionBeforeID(previous);
  };
  const nextFaultPage = () => { if (faultMeta.has_more && faultMeta.next_before_id) { setFaultCursorHistory(current => [...current, faultBeforeID]); setFaultBeforeID(faultMeta.next_before_id); } };
  const previousFaultPage = () => { if (faultCursorHistory.length) { const previous = faultCursorHistory[faultCursorHistory.length - 1]; setFaultCursorHistory(current => current.slice(0, -1)); setFaultBeforeID(previous); } };
  const nextIssuePage = () => { if (issueMeta.has_more && issueMeta.next_before_id) { setIssueCursorHistory(current => [...current, issueBeforeID]); setIssueBeforeID(issueMeta.next_before_id); } };
  const previousIssuePage = () => { if (issueCursorHistory.length) { const previous = issueCursorHistory[issueCursorHistory.length - 1]; setIssueCursorHistory(current => current.slice(0, -1)); setIssueBeforeID(previous); } };
  const refresh = () => { void load(); setIngestionRefresh(value => value + 1); setIssueRefresh(value => value + 1); };

  return <div className="app">
    <aside className={`sidebar ${sidebar ? 'open' : ''}`}>
      <div className="brand"><span className="brand-icon"><Layers3 size={19} /></span><div><b>{platformConfig.instance_name}</b><small>{platformConfig.product_name}</small></div><button className="mobile-only icon-btn" onClick={() => setSidebar(false)}><X size={17} /></button></div>
      <button className="cluster-switch" onClick={() => { navigate('about'); setSubPage(current => ({ ...current, about: 'settings' })); }}><span><i /> {platformConfig.environment}</span><small>{platformConfig.product_tagline}</small><ChevronRight size={14} /></button>
      <nav>{['运行', '系统'].map(group => <div className="nav-group" key={group}><label>{tx(group, group === '运行' ? 'OPERATIONS' : 'SYSTEM')}</label>{pages.filter(id => copy[id].group === tx(group, group === '运行' ? 'OPERATIONS' : 'SYSTEM')).map(id => { const Icon = pageIcons[id]; const count = id === 'issues' ? issueSummary?.remaining : id === 'incidents' ? faultSummary?.open : id === 'quality' ? issueSummary?.remaining_by_category?.data_quality : 0; return <button key={id} className={page === id ? 'active' : ''} onClick={() => navigate(id)}><Icon size={16} /><span>{copy[id].label}</span>{(count || 0) > 0 && <em>{count}</em>}</button>; })}</div>)}</nav>
      <div className="sidebar-footer"><span><i className={status?.status === 'ok' ? 'online' : ''} />API {status?.status === 'ok' ? 'ONLINE' : 'WAITING'}</span><small>{status?.version || 'dev'} · {status?.commit || 'local'}</small></div>
    </aside>
    {sidebar && <button className="scrim" onClick={() => setSidebar(false)} />}
    <main>
      <header className="topbar">
        <button className="mobile-only icon-btn" onClick={() => setSidebar(true)}><Menu size={18} /></button>
        <div className="crumb"><span>{platformConfig.instance_name}</span><ChevronRight size={13} /><b>{copy[page].label}</b></div>
        <button className="global-search" onClick={() => setSearchOpen(true)}><Search size={15} /><span>{tx('搜索资源', 'Search resources')}</span><kbd>⌘ K</kbd></button>
        <div className="top-actions"><LanguageButton i18n={i18n} zh={zh} /><ThemeButton tx={tx} /><button className="icon-btn" onClick={refresh} title={tx('刷新', 'Refresh')}><RefreshCw size={16} className={refreshing || ingestionLoading ? 'spin' : ''} /></button></div>
      </header>
      <div className="content">
        <div className="page-head"><div><h1>{copy[page].title}</h1><p>{copy[page].desc}</p></div><div className="page-meta"><Badge value={platformConfig.environment} kind="info" /><Badge value={`DATA ${freshnessStatus}`} kind={freshnessTone} />{collectionSources.length > 0 && <Badge value={overdueSources.length > 0 ? tx('采集超时', 'COLLECTION OVERDUE') : tx('采集中', 'COLLECTING')} kind={overdueSources.length > 0 ? 'warning' : 'info'} />}<span title={tx('源数据最新观测时间；不是页面刷新时间', 'Latest source observation; not the page refresh time')}>{freshnessLatest ? time(freshnessLatest, lang) : '—'}</span></div></div>
        {overdueSources.length > 0 && <div className="collection-alert"><AlertTriangle size={17} /><div><b>{tx('监控数据超过 10 分钟仍未获取完成', 'Monitoring collection has not completed within 10 minutes')}</b><span>{overdueSources.map(([name, source]) => `${name.toUpperCase()} ${Math.floor((source.collection_age_seconds || 0) / 60)}m`).join(' · ')}</span></div></div>}
        {currentSubPages.length > 0 && <nav className="subnav" aria-label={tx('页面分区', 'Page sections')}>{currentSubPages.map(item => <button key={item.id} className={subPage[page] === item.id ? 'active' : ''} onClick={() => setSubPage(current => ({ ...current, [page]: item.id }))}>{item.label}</button>)}</nav>}
        <AnimatePresence mode="wait"><motion.div key={page} className={page === 'overview' ? 'overview-page' : 'secondary-page'} initial={{ opacity: 0, y: 5 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }} transition={{ duration: .12 }}>
          {page === 'overview' && <Overview tx={tx} faults={openFaults} faultSummary={faultSummary} hosts={hosts} loading={loading} summary={summary} models={fleetModels} inventoryError={inventoryError} navigate={navigate} />}
          {page === 'gpus' && <Gpus tx={tx} view={subPage.gpus} assets={gpuAssets} models={fleetModels} loading={loading} inventoryError={inventoryError} healthScores={healthScores} healthSummary={healthSummary} lang={lang} />}
          {page === 'issues' && <Issues tx={tx} view={subPage.issues} summary={issueSummary} rows={issues} meta={issueMeta} page={issueCursorHistory.length + 1} loading={issueLoading} category={issueCategory} setCategory={setIssueCategory} status={issueStatus} setStatus={setIssueStatus} query={issueQuery} setQuery={setIssueQuery} previousPage={previousIssuePage} nextPage={nextIssuePage} select={setSelectedIssueID} lang={lang} />}
          {page === 'incidents' && <Incidents tx={tx} view={subPage.incidents} rows={filtered} ingestionMeta={ingestionMeta} ingestionPage={ingestionCursorHistory.length + 1} ingestionLoading={ingestionLoading} previousIngestionPage={previousIngestionPage} nextIngestionPage={nextIngestionPage} faultRows={faultEvents} faultMeta={faultMeta} faultPage={faultCursorHistory.length + 1} faultLoading={faultLoading} previousFaultPage={previousFaultPage} nextFaultPage={nextFaultPage} query={subPage.incidents === 'ingestion' ? ingestionQuery : query} setQuery={subPage.incidents === 'ingestion' ? setIngestionQuery : setQuery} level={subPage.incidents === 'ingestion' ? ingestionLevel : level} setLevel={subPage.incidents === 'ingestion' ? setIngestionLevel : setLevel} failures={failures} select={setSelected} selectIssue={setSelectedIssueID} selectReport={setSelectedFaultReportID} lang={lang} />}
          {page === 'validations' && <Validations tx={tx} view={subPage.validations} summary={degradationSummary} candidates={degradationCandidates} lang={lang} />}
          {page === 'quality' && <Quality tx={tx} view={subPage.quality} targets={targets} summary={summary} issueSummary={issueSummary} inventoryError={inventoryError} syncRuns={syncRuns} assetChanges={assetChanges} telemetry={telemetryQuality} telemetrySummary={telemetryQualitySummary} nodeAccess={nodeAccess} reloadNodeAccess={() => void load()} openIssues={() => { setIssueCategory('data_quality'); setIssueStatus(''); setIssueQuery(''); setIssueBeforeID(0); setIssueCursorHistory([]); navigate('issues'); }} lang={lang} />}
          {page === 'models' && <PageErrorBoundary key={subPage.models} title={tx('故障预测页面数据异常', 'Failure-prediction page data error')}><Models tx={tx} view={subPage.models} lang={lang} /></PageErrorBoundary>}
          {page === 'about' && <About tx={tx} view={subPage.about} platformConfig={platformConfig} onPlatformConfig={setPlatformConfig} />}
        </motion.div></AnimatePresence>
      </div>
    </main>
    <AnimatePresence>{searchOpen && <GlobalSearch tx={tx} query={query} setQuery={setQuery} pagesCopy={copy} items={ingestions} assets={gpuAssets} close={() => setSearchOpen(false)} navigate={navigate} select={id => { setSelected(id); navigate('incidents'); setSearchOpen(false); }} />}</AnimatePresence>
    <AnimatePresence>{selectedItem && <Drawer tx={tx} item={selectedItem} report={report} lang={lang} close={() => setSelected(null)} />}</AnimatePresence>
    <AnimatePresence>{selectedIssueID && issueDetail && <IssueDrawer tx={tx} detail={issueDetail} lang={lang} close={() => setSelectedIssueID(null)} saved={() => { setIssueRefresh(value => value + 1); void load(); }} />}</AnimatePresence>
    <AnimatePresence>{selectedFaultReportID && <FaultReportDrawer tx={tx} report={faultReport} loading={faultReportLoading} error={faultReportError} lang={lang} close={() => setSelectedFaultReportID(null)} />}</AnimatePresence>
  </div>;
}

function LanguageButton({ i18n, zh }: { i18n: { changeLanguage: (lang: string) => Promise<unknown> }; zh: boolean }) {
  const change = () => { const next = zh ? 'en' : 'zh'; localStorage.setItem('atlas_lang', next); void i18n.changeLanguage(next); };
  return <button className="icon-btn lang-btn" onClick={change} title={zh ? 'English' : '中文'}><Languages size={16} /><span>{zh ? '中' : 'EN'}</span></button>;
}
function ThemeButton({ tx }: { tx: Tx }) {
  const { theme, setTheme } = useTheme(); const [open, setOpen] = useState(false);
  const themes = [{ id: 'system', icon: CircleGauge, label: tx('跟随系统', 'System') }, { id: 'light', icon: Sun, label: tx('亮色', 'Light') }, { id: 'dark', icon: Moon, label: tx('暗色', 'Dark') }, { id: 'color', icon: Palette, label: tx('彩色', 'Color') }];
  return <div className="theme-menu"><button className="icon-btn" onClick={() => setOpen(!open)} title={tx('外观', 'Appearance')}><Palette size={16} /></button>{open && <div className="popover">{themes.map(x => <button key={x.id} className={theme === x.id ? 'active' : ''} onClick={() => { setTheme(x.id); setOpen(false); }}><x.icon size={15} />{x.label}{theme === x.id && <CheckCircle2 size={14} />}</button>)}</div>}</div>;
}

function Overview({ tx, faults, faultSummary, hosts, loading, summary, models, inventoryError, navigate }: { tx: Tx; faults: FaultEvent[]; faultSummary: FaultEventSummary | null; hosts: number; loading: boolean; summary: FleetSummary | null; models: { model: string; short: string; count: number }[]; inventoryError: boolean; navigate: (p: PageId) => void }) {
  const gpuTotal = summary?.gpus.total;
  const nodeTotal = summary?.nodes.total;
  return <div className="grid">
    {[['GPU', loading ? '—' : (gpuTotal == null ? '—' : String(gpuTotal)), inventoryError ? tx('资产 API 不可用', 'Inventory API unavailable') : tx(`${nodeTotal ?? '—'} 节点 · ${summary?.gpus.known_uuid ?? '—'} UUID`, `${nodeTotal ?? '—'} nodes · ${summary?.gpus.known_uuid ?? '—'} UUIDs`), Cpu], [tx('未恢复事件', 'Open events'), loading ? '—' : String(faultSummary?.open ?? 0), `${faultSummary?.open_by_severity.critical || 0} CRITICAL`, Bell], [tx('涉及主机', 'Affected hosts'), loading ? '—' : String(hosts), tx('未恢复事件范围', 'Open event scope'), Server], [tx('已恢复事件', 'Recovered events'), loading ? '—' : String(faultSummary?.by_state.recovered || 0), `${faultSummary?.total || 0} TOTAL EPISODES`, CheckCircle2]].map(([label, value, note, Icon]) => { const I = Icon as typeof Cpu; return <Card className="metric" key={String(label)}><I size={18} /><span>{String(label)}</span><strong>{String(value)}</strong><small>{String(note)}</small></Card>; })}
    <Card className="span-8 fleet-card"><CardHead code="FLEET" title={tx('GPU 型号', 'GPU Models')} action={<Badge value={summary?.latest_sync ? tx('已同步', 'SYNCED') : tx('等待同步', 'WAITING')} kind={summary?.latest_sync ? 'healthy' : 'warning'} />} /><div className="fleet-list">{models.map(x => <div key={x.model}><span><b>{x.short}</b><small>{x.model}</small></span><div><i style={{ width: `${gpuTotal ? x.count / gpuTotal * 100 : 0}%` }} /></div><strong>{x.count}</strong></div>)}{models.length === 0 && <Empty tx={tx} title={tx('无资产数据', 'No inventory data')} />}</div></Card>
    <Card className="span-4"><CardHead code="STATUS" title={tx('交付状态', 'Delivery')} /><div className="delivery">{[[tx('数据治理', 'Data governance'), 'P0', tx('基线完成', 'BASELINE')], [tx('健康评分', 'Health scoring'), 'P1', tx('基线完成', 'BASELINE')], [tx('故障闭环', 'Incident workflow'), 'P2', tx('进行中', 'ACTIVE')], [tx('性能验证', 'Performance validation'), 'P2.5', tx('规划', 'PLANNED')]].map(x => <div key={x[1]}><code>{x[1]}</code><b>{x[0]}</b><Badge value={x[2]} kind={x[1] === 'P2' ? 'healthy' : 'neutral'} /></div>)}</div></Card>
    <Card className="span-8"><CardHead code="LIVE" title={tx('未恢复硬件事件', 'Open Hardware Events')} action={<button className="link" onClick={() => navigate('incidents')}>{tx('全部', 'View all')}<ChevronRight size={13} /></button>} /><FaultEventList tx={tx} items={faults.slice(0, 6)} /></Card>
    <Card className="span-4"><CardHead code="SCOPE" title={tx('检测范围', 'Detection Scope')} /><div className="scope-list">{[[ShieldAlert, tx('硬故障', 'Hard failure'), 'XID · DBE · DROP'], [Gauge, tx('性能衰减', 'Degradation'), 'COMPUTE · MEMORY · LINK'], [Activity, tx('异常', 'Anomaly'), 'PEER · HISTORY · DRIFT'], [BrainCircuit, tx('预测', 'Prediction'), '1H · 6H · 24H']].map(([Icon, title, code]) => { const I = Icon as typeof Cpu; return <div key={String(code)}><I size={16} /><span><b>{String(title)}</b><small>{String(code)}</small></span></div>; })}</div></Card>
  </div>;
}

function Issues({ tx, view, summary, rows, meta, page, loading, category, setCategory, status, setStatus, query, setQuery, previousPage, nextPage, select, lang }: { tx: Tx; view: string; summary: IssueSummary | null; rows: PlatformIssue[]; meta: CursorMeta; page: number; loading: boolean; category: string; setCategory: (value: string) => void; status: string; setStatus: (value: string) => void; query: string; setQuery: (value: string) => void; previousPage: () => void; nextPage: () => void; select: (id: number) => void; lang: string }) {
  if (view === 'assets') return <AssetStatistics tx={tx} lang={lang} />;
  const categories = [
    ['availability', tx('节点可用性', 'Node Availability'), Server],
    ['inventory', tx('资产与身份', 'Inventory & Identity'), Cpu],
    ['data_quality', tx('数据质量', 'Data Quality'), Activity],
    ['access', tx('节点访问', 'Node Access'), ShieldCheck],
  ] as const;
  const categoryName = (value: string) => categories.find(item => item[0] === value)?.[1] || value;
  const start = meta.total > 0 ? (page - 1) * meta.limit + 1 : 0;
  const end = start > 0 ? start + rows.length - 1 : 0;
  return <div className="grid issue-center">
    {[
      [tx('发现问题', 'Discovered'), summary?.discovered ?? '—', tx('历史累计', 'ALL TIME'), ClipboardList, ''],
      [tx('已解决', 'Resolved'), summary?.resolved ?? '—', tx('自动恢复或人工解决', 'AUTO OR MANUAL'), CheckCircle2, 'resolved'],
      [tx('遗留问题', 'Remaining'), summary?.remaining ?? '—', tx('待处理与处理中', 'OPEN + IN PROGRESS'), AlertTriangle, 'remaining'],
      [tx('当前仍被检测', 'Actively Detected'), summary?.active_detection ?? '—', tx('自动检测源仍异常', 'SOURCE STILL ACTIVE'), ShieldAlert, ''],
    ].map(([label, value, note, Icon, statusFilter]) => { const I = Icon as typeof ClipboardList; return <button className="issue-stat" key={String(label)} onClick={() => { setStatus(String(statusFilter)); setCategory(''); }}><I size={18} /><span>{String(label)}</span><strong>{String(value)}</strong><small>{String(note)}</small></button>; })}
    <Card className="span-12"><CardHead code="CATEGORIES" title={tx('问题分类', 'Issue Categories')} action={<div className="table-actions"><Badge value={`${summary?.discovered || 0} TOTAL`} kind="info" /><Badge value={`${summary?.training_eligible || 0} TRAINING`} kind={(summary?.training_eligible || 0) > 0 ? 'healthy' : 'neutral'} /><a className="link training-export" href="/api/v1/issues/training-data?download=1" download><Download size={13} />{tx('导出训练数据', 'Export training data')}</a></div>} /><div className="issue-categories"><button className={!category ? 'active' : ''} onClick={() => setCategory('')}><span className="issue-category-icon"><Layers3 size={21} /></span><b>{tx('全部问题', 'All Issues')}</b><strong>{summary?.discovered || 0}</strong><small>all</small></button>{categories.map(([key, label, Icon]) => <button key={key} className={category === key ? 'active' : ''} onClick={() => setCategory(key)}><span className="issue-category-icon"><Icon size={21} /></span><b>{label}</b><strong>{summary?.by_category[key] || 0}</strong><small>{key}</small></button>)}</div></Card>
    <Card className="span-12"><div className="toolbar issue-toolbar"><label><Search size={15} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder={tx('节点 / GPU / 类型 / 描述', 'Node / GPU / type / description')} /></label><label className="select"><Filter size={15} /><select value={status} onChange={event => setStatus(event.target.value)}><option value="">{tx('全部状态', 'All status')}</option><option value="remaining">{tx('遗留问题', 'REMAINING')}</option><option value="open">OPEN</option><option value="in_progress">IN PROGRESS</option><option value="resolved">RESOLVED</option><option value="ignored">IGNORED</option></select></label><span>{loading ? tx('同步中', 'Syncing') : `${start}–${end} / ${meta.total}`}</span></div><div className="table-wrap"><table className="issue-table"><thead><tr><th>{tx('分类 / 类型', 'Category / Type')}</th><th>{tx('问题', 'Issue')}</th><th>{tx('对象', 'Entity')}</th><th>{tx('级别', 'Severity')}</th><th>{tx('处理状态', 'Workflow')}</th><th>{tx('检测状态', 'Detection')}</th><th>{tx('最近发现', 'Last Seen')}</th><th /></tr></thead><tbody>{rows.map(issue => <tr key={issue.id}><td><b>{categoryName(issue.category)}</b><small><code>{issue.issue_type}</code></small></td><td><b>{issue.title}</b><small>{issue.description || '—'}</small></td><td><b>{issue.node_ip || issue.entity_key}</b><small><code>{issue.gpu_uuid || issue.entity_type}</code></small></td><td><Badge value={issue.severity.toUpperCase()} kind={tone(issue.severity)} /></td><td><Badge value={issue.status.toUpperCase()} kind={issueTone(issue.status)} /></td><td><Badge value={issue.detection_state.toUpperCase()} kind={issue.detection_state === 'active' ? 'warning' : 'healthy'} /></td><td>{time(issue.last_detected_at, lang)}</td><td><button className="link" onClick={() => select(issue.id)}>{tx('详情与处置', 'Resolve')}<ChevronRight size={13} /></button></td></tr>)}</tbody></table>{rows.length === 0 && <Empty tx={tx} title={tx('没有匹配的问题', 'No matching issues')} />}</div><div className="ingestion-pagination"><span>{tx(`第 ${page} 页`, `Page ${page}`)}</span><div><button onClick={previousPage} disabled={page <= 1}><ChevronLeft size={15} /></button><button onClick={nextPage} disabled={!meta.has_more}><ChevronRight size={15} /></button></div></div></Card>
  </div>;
}

function AssetStatistics({ tx, lang }: { tx: Tx; lang: string }) {
  const [rows, setRows] = useState<ReconciliationRow[]>([]);
  const [summary, setSummary] = useState<ReconciliationSummary | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [scope, setScope] = useState('');
  const [category, setCategory] = useState('');
  const [query, setQuery] = useState('');
  const initialLoadComplete = useRef(false);
  useEffect(() => {
    let cancelled = false;
    const loadAssets = async () => {
      if (!initialLoadComplete.current) setLoading(true);
      setError('');
      try {
        const response = await fetch('/api/v1/inventory/reconciliation?limit=1000');
        const payload = await response.json();
        if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
        if (!cancelled) { setRows(Array.isArray(payload.data) ? payload.data : []); setSummary(payload.summary || null); }
      } catch (reason) {
        if (!cancelled) setError(reason instanceof Error ? reason.message : tx('资产对账加载失败', 'Failed to load asset reconciliation'));
      } finally {
        if (!cancelled) {
          initialLoadComplete.current = true;
          setLoading(false);
        }
      }
    };
    void loadAssets();
    const timer = window.setInterval(loadAssets, 60000);
    return () => { cancelled = true; window.clearInterval(timer); };
  }, []);
  const categoryLabels: Record<string, string> = {
    gpu: tx('GPU 设备', 'GPU Devices'), network: tx('交换机', 'Switches'), firewall: tx('UFM / 防火墙', 'UFM / Firewall'),
    cloud: tx('腾讯云设备', 'Tencent Cloud'), operations: tx('监控运维', 'Monitoring Operations'),
    storage: tx('存储设备', 'Storage'), other: tx('其他设备', 'Other'),
  };
  const maxCategory = Math.max(1, ...(summary?.by_category || []).map(item => item.count));
  const maxGPUModel = Math.max(1, ...(summary?.gpu_models || []).map(item => item.count));
  const visible = rows.filter(row => (!scope || (row.scope === scope && row.category === 'gpu')) && (!category || row.category === category) && (!query.trim() || [row.ip_address, row.name, row.sn, row.type, row.gpu_model].join(' ').toLowerCase().includes(query.trim().toLowerCase())));
  const sourceLabel = (value: string) => value === 'both' ? tx('双边共有', 'In both') : value === 'ops_only' ? tx('仅运维主机', 'Ops only') : tx('仅资产设备', 'Asset only');
  return <div className="grid asset-statistics">
    <Card className="span-12 capability-note"><Layers3 size={19} /><div><CardHead code="LXOP CROSS VALIDATION" title={tx('实时在用资产分类与双接口交叉验证', 'Live In-use Asset Classification & Cross-validation')} /><p>{tx('仅统计状态为 on 或已上架使用中的实时资产；停机、下架、非在线状态以及已从接口移除的 IP 不进入监控和异常统计。按 SN 优先、IP 补充关联同一设备，点击图形可下钻。', 'Only live assets in on or 已上架使用中 state are counted. Stopped, retired, non-online, or API-removed IPs are excluded from monitoring and anomaly statistics. Devices are linked by serial first and IP second; click a chart to drill down.')}</p></div><Badge value={loading ? 'SYNCING' : `${summary?.total || 0} IN USE`} kind={error ? 'warning' : 'info'} /></Card>
    <Card className="span-7 asset-category-chart">
      <CardHead code="DEVICE TYPES" title={tx('设备类型分布', 'Device Type Distribution')} action={category ? <button className="link" onClick={() => setCategory('')}>{tx('显示全部', 'Show all')}<X size={12} /></button> : undefined} />
      <div className="asset-bars">{(summary?.by_category || []).map(item => <button key={item.name} className={category === item.name ? 'active' : ''} onClick={() => setCategory(current => current === item.name ? '' : item.name)}><span><b>{categoryLabels[item.name] || item.name}</b><strong>{item.count}</strong></span><i><em style={{ width: `${Math.max(4, item.count / maxCategory * 100)}%` }} /></i></button>)}</div>
    </Card>
    <Card className="span-5 gpu-model-chart">
      <CardHead code="GPU MODELS" title={tx('GPU 型号分布', 'GPU Model Distribution')} action={<Badge value={`${(summary?.by_category || []).find(item => item.name === 'gpu')?.count || 0} GPU`} kind="healthy" />} />
      <div className="gpu-model-bars">{(summary?.gpu_models || []).map(item => <div key={item.name}><span><b>{item.name}</b><strong>{item.count}</strong></span><i><em style={{ width: `${Math.max(5, item.count / maxGPUModel * 100)}%` }} /></i></div>)}</div>
    </Card>
    <Card className="span-12 asset-difference-card">
      <CardHead code="SOURCE DIFFERENCE" title={tx('GPU 双接口差值', 'GPU Cross-source Difference')} action={scope ? <button className="link" onClick={() => setScope('')}>{tx('取消筛选', 'Clear filter')}<X size={12} /></button> : undefined} />
      <div className="source-diff-grid">
        {[['both', tx('双边共有', 'In both'), tx('SN 或 IP 匹配', 'Matched by SN or IP'), CheckCircle2], ['ops_only', tx('仅运维主机接口', 'Ops host only'), tx('资产设备接口不存在', 'Missing from asset machines'), Server], ['asset_only', tx('仅资产设备接口', 'Asset machine only'), tx('运维主机接口不存在', 'Missing from ops hosts'), Database]].map(([key, label, note, Icon]) => { const I = Icon as typeof Server; const count = summary?.by_scope[String(key)] || 0; return <button key={String(key)} className={scope === key ? 'active' : ''} onClick={() => { setCategory(''); setScope(current => current === key ? '' : String(key)); }}><I size={19} /><span><b>{String(label)}</b><small>{String(note)}</small></span><strong>{count}</strong></button>; })}
      </div>
    </Card>
    <Card className="span-12">
      <div className="toolbar asset-reconcile-toolbar"><label><Search size={15} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder={tx('IP / 名称 / SN / 类型 / GPU 型号', 'IP / name / SN / type / GPU model')} /></label><span>{visible.length} / {rows.length}</span></div>
      {error && <p className="form-error">{error}</p>}
      <div className="table-wrap"><table className="asset-reconcile-table"><thead><tr><th>{tx('对账结果', 'Result')}</th><th>{tx('分类 / 类型', 'Category / Type')}</th><th>{tx('设备', 'Device')}</th><th>{tx('运维主机接口', 'Ops Host API')}</th><th>{tx('资产设备接口', 'Asset Machine API')}</th><th>{tx('数据时间', 'Data Time')}</th></tr></thead><tbody>{visible.map(row => <tr key={row.key}><td><Badge value={sourceLabel(row.scope)} kind={row.scope === 'both' ? 'healthy' : 'warning'} /></td><td><b>{categoryLabels[row.category] || row.category}</b><small>{row.gpu_model || row.type || 'UNKNOWN'}</small></td><td><b>{row.ip_address || row.name || '—'}</b><small>{row.name || '—'} · <code>{row.sn || 'NO SN'}</code></small></td><td>{row.ops_host ? <><Badge value="PRESENT" kind="healthy" /><small>{row.ops_host.ip_address || 'NO IP'} · {row.ops_host.state}</small></> : <Badge value="MISSING" kind="warning" />}</td><td>{row.asset_machine ? <><Badge value="PRESENT" kind="healthy" /><small>{row.asset_machine.type || 'UNKNOWN'} · {row.asset_machine.state}</small></> : <Badge value="MISSING" kind="warning" />}</td><td>{time(summary?.generated_at, lang)}</td></tr>)}</tbody></table>{!loading && visible.length === 0 && <Empty tx={tx} title={tx('没有匹配的资产记录', 'No matching asset records')} />}</div>
    </Card>
  </div>;
}

function Gpus({ tx, view, assets, models, loading, inventoryError, healthScores, healthSummary, lang }: { tx: Tx; view: string; assets: GPUAsset[]; models: { model: string; short: string; count: number }[]; loading: boolean; inventoryError: boolean; healthScores: GPUHealthScore[]; healthSummary: HealthSummary | null; lang: string }) {
  const dims = [
    { code: 'S', name: tx('稳定性', 'Stability'), detail: 'XID / Reset / Drop', icon: ShieldAlert, score: 'stability_score' },
    { code: 'M', name: tx('显存', 'Memory'), detail: 'ECC / SRAM / Row Remap', icon: MemoryStick, score: 'memory_score' },
    { code: 'T', name: tx('散热', 'Thermal'), detail: 'Temperature / Cooling', icon: Thermometer, score: 'thermal_score' },
    { code: 'P', name: tx('供电', 'Power'), detail: 'Power / Clock / Throttle', icon: Zap, score: 'power_score' },
    { code: 'I', name: tx('互联', 'Interconnect'), detail: 'PCIe / NVLink / NVSwitch', icon: Network, score: 'interconnect_score' },
    { code: 'F', name: tx('性能', 'Performance'), detail: 'Compute / Bandwidth / Straggler', icon: Gauge, score: 'performance_score' },
  ] as const;
  const [nodePage, setNodePage] = useState(0);
  const [expandedNodes, setExpandedNodes] = useState<Set<string>>(() => new Set());
  const nodePageSize = 20;
  const groupedNodes = [...assets.reduce((groups, asset) => {
    const cards = groups.get(asset.node_ip) || [];
    cards.push(asset);
    groups.set(asset.node_ip, cards);
    return groups;
  }, new Map<string, GPUAsset[]>()).entries()].map(([nodeIP, cards]) => {
    const sortedCards = [...cards].sort((a, b) => Number(a.state === 'active') - Number(b.state === 'active') || a.gpu_index - b.gpu_index);
    const active = cards.filter(card => card.state === 'active').length;
    const modelsOnNode = [...new Set(cards.map(card => card.model_name || card.model || 'UNKNOWN'))];
    const lastSynced = cards.reduce((latest, card) => !latest || card.last_synced_at > latest ? card.last_synced_at : latest, '');
    return { nodeIP, cards: sortedCards, active, abnormal: cards.length - active, models: modelsOnNode, lastSynced };
  }).sort((a, b) => b.abnormal - a.abnormal || compareIP(a.nodeIP, b.nodeIP));
  const nodePageCount = Math.max(1, Math.ceil(groupedNodes.length / nodePageSize));
  const safeNodePage = Math.min(nodePage, nodePageCount - 1);
  const visibleNodes = groupedNodes.slice(safeNodePage * nodePageSize, (safeNodePage + 1) * nodePageSize);
  const abnormalCards = assets.filter(asset => asset.state !== 'active').length;
  const toggleNode = (nodeIP: string) => setExpandedNodes(current => { const next = new Set(current); if (next.has(nodeIP)) next.delete(nodeIP); else next.add(nodeIP); return next; });
  if (view === 'health') {
    const risky = healthScores.filter(x => x.score == null || x.level !== 'healthy' || x.data_confidence !== 'A' || (x.fallback_metric_count || 0) > 0).sort((a, b) => {
      if (a.score == null && b.score == null) return compareIP(a.node_ip, b.node_ip) || a.gpu_index - b.gpu_index;
      if (a.score == null) return 1;
      if (b.score == null) return -1;
      return a.score - b.score || compareIP(a.node_ip, b.node_ip) || a.gpu_index - b.gpu_index;
    }).slice(0, 100);
    const riskCount = (healthSummary?.by_level.attention || 0) + (healthSummary?.by_level.warning || 0) + (healthSummary?.by_level.critical || 0);
    const scoredCoverage = healthSummary ? `${healthSummary.scored} / ${healthSummary.total} GPU` : '—';
    return <div className="grid">
      <section className="health-kpi-panel">
        <header><div><code>HEALTH OUTCOME</code><h2>{tx('核心健康', 'Core Health')}</h2></div><small>{tx('规则评分结果；UNKNOWN 不参与平均分', 'Rule outcomes; UNKNOWN is excluded from the average')}</small></header>
        <div className="health-kpi-grid">
          {[[tx('已评分平均分', 'Scored average'), healthSummary ? healthSummary.average_score.toFixed(1) : '—', `${scoredCoverage} · ${tx('未知已排除', 'UNKNOWN EXCLUDED')}`, CircleGauge], [tx('风险卡', 'At-risk GPUs'), String(riskCount), 'ATTENTION + WARNING + CRITICAL', ShieldAlert], [tx('数据未知', 'Unknown'), String(healthSummary?.unknown ?? '—'), tx('指标不足，分数为空', 'INSUFFICIENT DATA · SCORE NULL'), AlertTriangle], [tx('规则命中', 'Rule hits'), String(healthSummary?.latest_run?.rule_hit_count ?? '—'), healthSummary?.latest_run?.rule_version || 'WAITING', Activity]].map(([label, value, note, Icon]) => { const I = Icon as typeof Cpu; return <Card className="metric" key={String(label)}><I size={18} /><span>{String(label)}</span><strong>{String(value)}</strong><small>{String(note)}</small></Card>; })}
        </div>
      </section>
      <Card className="span-12"><CardHead code="RISK QUEUE" title={tx('GPU 健康风险', 'GPU Health Risks')} action={<Badge value={`${risky.length} / ${healthScores.length}`} kind={risky.length ? 'warning' : 'healthy'} />} /><div className="table-wrap"><table className="health-table"><thead><tr><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('型号', 'Model')}</th><th>{tx('分数', 'Score')}</th><th>{tx('级别', 'Level')}</th><th>{tx('置信度', 'Confidence')}</th><th>{tx('健康领域分数', 'Health Domain Scores')}</th><th>{tx('详情', 'Details')}</th><th>{tx('计算时间', 'Evaluated')}</th></tr></thead><tbody>{risky.map(score => <tr key={score.gpu_asset_id}><td><b>{score.node_ip} · GPU {score.gpu_index}</b><small><code>{score.gpu_uuid}</code></small></td><td>{score.model_name}</td><td className="score-cell"><strong className="score-value">{score.score ?? '—'}</strong></td><td><Badge value={score.level.toUpperCase()} kind={tone(score.level)} /></td><td><Badge value={score.data_confidence} kind={score.data_confidence === 'A' ? 'healthy' : score.data_confidence === 'B' ? 'info' : score.data_confidence === 'C' ? 'warning' : 'danger'} /></td><td><div className="domain-scores">{dims.map(domain => <span key={domain.code}><b>{domain.code} {domain.name}</b><strong>{score[domain.score]}</strong></span>)}</div></td><td><small>{(score.evidence || []).join(' · ')}</small><small>{tx('取值来源', 'Selected sources')}: {(score.sources_available || []).join(' + ') || '—'} · fallback {score.fallback_metric_count || 0}</small></td><td>{time(score.evaluated_at, lang)}</td></tr>)}</tbody></table>{!loading && healthScores.length === 0 && <Empty tx={tx} title={tx('等待首次健康评分', 'Waiting for first health evaluation')} />}</div></Card>
      <Card className="span-12"><CardHead code="DOMAINS" title={tx('GPU 健康领域', 'GPU Health Domains')} /><div className="domain-grid">{dims.map(domain => { const Icon = domain.icon; return <div key={domain.code}><Icon size={17} /><b>{domain.code} · {domain.name}</b><span>{domain.detail}</span></div>; })}</div></Card>
    </div>;
  }
  return <div className="grid"><Card className="span-12"><CardHead code="INVENTORY" title={tx('资产基线', 'Inventory Baseline')} /><div className="inventory inventory-models">{models.map(x => <div key={x.model}><i /><span><b>{x.short}</b><small>{x.model}</small></span><strong>{x.count}</strong></div>)}{models.length === 0 && <Empty tx={tx} title={tx('等待资产同步', 'Waiting for inventory sync')} />}</div></Card><Card className="span-12"><CardHead code="GPU NODES" title={tx('GPU 资产清单', 'GPU Inventory')} action={<div className="table-actions"><Badge value={inventoryError ? tx('接口异常', 'API ERROR') : tx(`总卡数 ${assets.length}`, `${assets.length} TOTAL GPUS`)} kind={inventoryError ? 'warning' : 'info'} /><Badge value={tx(`异常卡数 ${abnormalCards}`, `${abnormalCards} NON-ACTIVE`)} kind={abnormalCards ? 'warning' : 'healthy'} /><button disabled={safeNodePage === 0} onClick={() => setNodePage(page => page - 1)} aria-label={tx('上一页', 'Previous page')}><ChevronLeft size={15} /></button><span>{safeNodePage + 1} / {nodePageCount}</span><button disabled={safeNodePage + 1 >= nodePageCount} onClick={() => setNodePage(page => page + 1)} aria-label={tx('下一页', 'Next page')}><ChevronRight size={15} /></button></div>} /><div className="table-wrap"><table className="node-inventory-table"><thead><tr><th>{tx('节点 IP', 'Node IP')}</th><th>{tx('GPU 型号', 'GPU Models')}</th><th>{tx('卡状态', 'GPU State')}</th><th>{tx('主机状态', 'Node State')}</th><th>{tx('数据时间', 'Data Time')}</th></tr></thead><tbody>{visibleNodes.map(node => <Fragment key={node.nodeIP}><tr className={node.abnormal ? 'node-abnormal' : ''}><td><button className="node-expand" onClick={() => toggleNode(node.nodeIP)}><ChevronRight className={expandedNodes.has(node.nodeIP) ? 'expanded' : ''} size={15} /><b>{node.nodeIP}</b></button><small>{node.cards.length} GPU</small></td><td>{node.models.map(model => <small key={model}>{model}</small>)}</td><td><b>{node.active} ACTIVE</b>{node.abnormal > 0 && <small className="state-warning">{node.abnormal} NON-ACTIVE</small>}</td><td><Badge value={node.abnormal ? `${node.active} ACTIVE · ${node.abnormal} NON-ACTIVE` : `${node.active} ACTIVE`} kind={node.abnormal ? 'warning' : 'healthy'} /></td><td>{time(node.lastSynced, lang)}</td></tr>{expandedNodes.has(node.nodeIP) && <tr className="gpu-detail-row"><td colSpan={5}><div className="table-wrap"><table className="gpu-detail-table"><thead><tr><th>GPU</th><th>GPU UUID</th><th>{tx('型号', 'Model')}</th><th>PCI BUS ID</th><th>{tx('状态', 'State')}</th><th>{tx('数据时间', 'Data Time')}</th></tr></thead><tbody>{node.cards.map(card => <tr key={card.asset_key}><td><b>GPU {card.gpu_index}</b><small>{card.device || '—'}</small></td><td><code>{card.gpu_uuid || 'UUID UNKNOWN'}</code></td><td>{card.model_name || card.model || 'UNKNOWN'}</td><td><code>{card.pci_bus_id || '—'}</code></td><td><Badge value={card.state.toUpperCase()} kind={card.state === 'active' ? 'healthy' : 'warning'} /></td><td>{time(card.last_synced_at, lang)}</td></tr>)}</tbody></table></div></td></tr>}</Fragment>)}</tbody></table>{!loading && assets.length === 0 && <Empty tx={tx} title={inventoryError ? tx('资产 API 不可用', 'Inventory API unavailable') : tx('等待首次同步', 'Waiting for first sync')} />}</div></Card></div>;
}

function Incidents({ tx, view, rows, ingestionMeta, ingestionPage, ingestionLoading, previousIngestionPage, nextIngestionPage, faultRows, faultMeta, faultPage, faultLoading, previousFaultPage, nextFaultPage, query, setQuery, level, setLevel, failures, select, selectIssue, selectReport, lang }: { tx: Tx; view: string; rows: Ingestion[]; ingestionMeta: IngestionMeta; ingestionPage: number; ingestionLoading: boolean; previousIngestionPage: () => void; nextIngestionPage: () => void; faultRows: FaultEvent[]; faultMeta: CursorMeta; faultPage: number; faultLoading: boolean; previousFaultPage: () => void; nextFaultPage: () => void; query: string; setQuery: (v: string) => void; level: string; setLevel: (v: string) => void; failures: Failure[]; select: (id: number) => void; selectIssue: (id: number) => void; selectReport: (id: number) => void; lang: string }) {
  if (view === 'workflow') return <div className="grid"><Card className="span-12"><CardHead code="WORKFLOW" title={tx('处理状态', 'Workflow State')} action={failures.length ? <Badge value={`${failures.length} INGEST FAILED`} kind="warning" /> : undefined} /><div className="workflow">{[tx('发现', 'DETECTED'), tx('分诊', 'TRIAGED'), tx('待维护', 'PENDING'), tx('已隔离', 'ISOLATED'), tx('诊断', 'DIAGNOSIS'), tx('修复', 'REPAIRED'), tx('复测', 'VALIDATED'), tx('关闭', 'CLOSED')].map((x, i) => <div key={x}><small>{String(i + 1).padStart(2, '0')}</small><b>{x}</b>{i < 7 && <ChevronRight size={13} />}</div>)}</div></Card></div>;
  if (view === 'ingestion') {
    const status = ingestionMeta.stream_status.toUpperCase();
    const statusTone = status === 'LIVE' ? 'healthy' : status === 'SNAPSHOT' ? 'info' : status === 'ERROR' ? 'danger' : status === 'STALE' ? 'warning' : 'neutral';
    const start = ingestionMeta.total > 0 ? (ingestionPage - 1) * ingestionMeta.limit + 1 : 0;
    const end = start > 0 ? start + rows.length - 1 : 0;
    return <div className="grid">
      <Card className="span-12 ingestion-stream"><div><CardHead code="INGESTION STREAM" title={tx('告警接收链路', 'Alert Ingestion Stream')} /><p>{tx('原始监控告警接收与持久化审计；不等同于 Atlas 硬件事件。', 'Raw monitoring alert ingestion and persistence audit; separate from Atlas hardware events.')}</p></div><div className="ingestion-stream-metrics"><span><small>{tx('状态', 'Status')}</small><Badge value={ingestionLoading ? 'SYNCING' : status} kind={ingestionLoading ? 'info' : statusTone} /></span><span><small>{tx('数据模式', 'Data mode')}</small><b>{ingestionMeta.source_mode.toUpperCase()}</b></span><span><small>{tx('最新接收', 'Latest received')}</small><b>{ingestionMeta.latest_received_at ? time(ingestionMeta.latest_received_at, lang) : '—'}</b></span><span><small>{tx('近 5 分钟', 'Last 5m')}</small><b>{ingestionMeta.received_5m}</b></span><span><small>{tx('近 1 小时', 'Last 1h')}</small><b>{ingestionMeta.received_1h}</b></span><span><small>{tx('全部记录', 'All records')}</small><b>{ingestionMeta.all_total}</b></span></div></Card>
      <Card className="span-12"><div className="toolbar"><label><Search size={15} /><input value={query} onChange={e => setQuery(e.target.value)} placeholder={tx('主机 / UUID / XID / 消息', 'Host / UUID / XID / message')} /></label><label className="select"><Filter size={15} /><select value={level} onChange={e => setLevel(e.target.value)}><option value="">{tx('全部级别', 'All levels')}</option>{['critical', 'error', 'warning', 'info'].map(x => <option key={x}>{x}</option>)}</select></label><span>{start}–{end} / {ingestionMeta.total}</span></div><div className="table-wrap"><table className="incident-table"><thead><tr><th>{tx('级别', 'Level')}</th><th>{tx('事件', 'Incident')}</th><th>{tx('主机 / GPU', 'Host / GPU')}</th><th>{tx('来源', 'Source')}</th><th>{tx('处理', 'State')}</th><th>{tx('告警时间', 'Alert Time')}</th><th /></tr></thead><tbody>{rows.map(x => <tr key={x.id}><td><Badge value={x.level || 'unknown'} kind={tone(x.level)} /></td><td><b>{x.message}</b><small>#{x.id} · {x.labels?.err_msg || x.event_id}</small></td><td>{x.host || '—'}<small><code>{x.labels?.UUID || x.labels?.uuid || x.labels?.pci_bus_id || 'UNMAPPED'}</code></small></td><td>{x.source}</td><td><Badge value={x.process_status} kind={x.process_status === 'success' ? 'healthy' : 'warning'} /></td><td>{time(x.event_timestamp || x.created_at, lang)}<small>{x.event_timestamp ? tx('源事件时间', 'source event') : tx('接收时间', 'received')}</small></td><td><button className="link" onClick={() => select(x.id)}>{tx('详情', 'Details')}<Eye size={13} /></button></td></tr>)}</tbody></table>{rows.length === 0 && <Empty tx={tx} title={tx('无匹配接收记录', 'No matching ingestion records')} />}</div><div className="ingestion-pagination"><span>{tx(`第 ${ingestionPage} 页`, `Page ${ingestionPage}`)}</span><div><button onClick={previousIngestionPage} disabled={ingestionPage <= 1} title={tx('上一页', 'Previous page')}><ChevronLeft size={15} /></button><button onClick={nextIngestionPage} disabled={!ingestionMeta.has_more} title={tx('下一页', 'Next page')}><ChevronRight size={15} /></button></div></div></Card>
    </div>;
  }
  const faultStart = faultMeta.total > 0 ? (faultPage - 1) * faultMeta.limit + 1 : 0;
  const faultEnd = faultStart > 0 ? faultStart + faultRows.length - 1 : 0;
  return <div className="grid"><Card className="span-12"><div className="toolbar"><label><Search size={15} /><input value={query} onChange={e => setQuery(e.target.value)} placeholder={tx('节点 / UUID / 规则 / 详情', 'Node / UUID / rule / details')} /></label><label className="select"><Filter size={15} /><select value={level} onChange={e => setLevel(e.target.value)}><option value="">{tx('全部级别', 'All levels')}</option>{['critical', 'warning', 'attention'].map(x => <option key={x}>{x}</option>)}</select></label><span>{faultLoading ? tx('加载中', 'Loading') : `${faultStart}–${faultEnd} / ${faultMeta.total}`}</span></div><div className="table-wrap"><table className="incident-table"><thead><tr><th>{tx('级别', 'Level')}</th><th>{tx('规则 / 详情', 'Rule / Details')}</th><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('维度', 'Domain')}</th><th>{tx('告警状态', 'Alert')}</th><th>{tx('处置状态', 'Workflow')}</th><th>{tx('次数', 'Count')}</th><th>{tx('首次 / 最近', 'First / Last')}</th><th /></tr></thead><tbody>{faultRows.map(x => <tr key={x.id}><td><Badge value={x.severity.toUpperCase()} kind={tone(x.severity)} /></td><td><b>{x.rule_code}</b><small>{x.evidence} · {tx('阈值', 'threshold')} {x.threshold}</small></td><td><b>{x.node_ip} · GPU {x.gpu_index}</b><small><code>{x.gpu_uuid}</code></small></td><td><code>{x.domain.toUpperCase()}</code></td><td><Badge value={x.state.toUpperCase()} kind={tone(x.state)} /></td><td><Badge value={(x.workflow_status || 'syncing').toUpperCase()} kind={x.workflow_status ? issueTone(x.workflow_status) : 'neutral'} /></td><td>{x.occurrence_count}</td><td>{time(x.first_observed_at, lang)}<small>{time(x.last_observed_at, lang)}</small></td><td><div className="event-actions"><button className="link" onClick={() => selectReport(x.id)}>{tx('分析报告', 'Report')}<BookOpen size={13} /></button>{x.issue_id ? <button className="link" onClick={() => selectIssue(x.issue_id!)}>{tx('详情与处置', 'Resolve')}<ChevronRight size={13} /></button> : <small>{tx('台账同步中', 'Syncing')}</small>}</div></td></tr>)}</tbody></table>{faultRows.length === 0 && <Empty tx={tx} title={tx('无匹配硬件告警', 'No matching hardware alerts')} />}</div><div className="ingestion-pagination"><span>{tx(`第 ${faultPage} 页`, `Page ${faultPage}`)}</span><div><button onClick={previousFaultPage} disabled={faultPage <= 1} title={tx('上一页', 'Previous page')}><ChevronLeft size={15} /></button><button onClick={nextFaultPage} disabled={!faultMeta.has_more} title={tx('下一页', 'Next page')}><ChevronRight size={15} /></button></div></div></Card></div>;
}

function Validations({ tx, view, summary, candidates, lang }: { tx: Tx; view: string; summary: DegradationSummary | null; candidates: DegradationCandidate[]; lang: string }) {
  if (view === 'records') return <div className="grid">
    <Card className="span-12 safety"><ShieldAlert size={21} /><div><CardHead code="SAFETY" title={tx('主动测试前置条件', 'Active Test Gates')} /><p>{tx('人工确认维护窗口 · 无 GPU 计算进程 · 操作确认 · 超时与温控保护', 'Maintenance confirmed · no GPU compute process · operator confirmation · timeout and thermal guard')}</p></div><Badge value="NO AUTO STRESS" kind="warning" /></Card>
    <Card className="span-12"><CardHead code="QUEUE" title={tx('验证记录', 'Validation Records')} /><Empty tx={tx} title={tx('主动验证接口尚未启用', 'Active validation API is not enabled')} /></Card>
  </div>;
  const scope = (value: string) => value === 'same_model_high_load_7d' ? tx('同型号高负载 7 天', 'Same-model high-load 7d') : value === 'same_node_model' ? tx('同节点同型号', 'Same node/model') : tx('同型号集群', 'Same-model fleet');
  return <div className="grid">
    <Card className="span-12 degradation-guard"><div><Eye size={21} /><div><CardHead code="SHADOW MODE" title={tx('被动衰减候选', 'Passive Degradation Candidates')} /><p>{tx('只读观察：不扣减健康分、不输出故障概率、不自动隔离或压测。候选必须先确认负载可比性，再进入维护窗口复测。', 'Read-only observation: no health deduction, failure probability, automatic isolation or stress. Confirm workload comparability before maintenance validation.')}</p></div></div><Badge value={(summary?.mode || 'shadow').toUpperCase()} kind="info" /></Card>
    <Card className="span-3 degradation-stat"><small>{tx('已评估', 'EVALUATED')}</small><strong>{summary?.evaluated_gpus ?? '—'}</strong><span>{tx('当前健康快照', 'current health snapshots')}</span></Card>
    <Card className="span-3 degradation-stat"><small>{tx('高负载可评估', 'LOAD ELIGIBLE')}</small><strong>{summary?.eligible_gpus ?? '—'}</strong><span>{tx(`利用率 ≥ ${summary?.minimum_utilization ?? 80}%`, `utilization ≥ ${summary?.minimum_utilization ?? 80}%`)}</span></Card>
    <Card className="span-3 degradation-stat"><small>{tx('基线就绪', 'BASELINE READY')}</small><strong>{summary?.baseline_ready_gpus ?? '—'}</strong><span>{tx(`历史基线 ${summary?.historical_baseline_gpus ?? 0}`, `${summary?.historical_baseline_gpus ?? 0} historical`)}</span></Card>
    <Card className="span-3 degradation-stat candidate"><small>{tx('衰减候选', 'CANDIDATES')}</small><strong>{summary?.candidate_gpus ?? '—'}</strong><span>{tx(`性能比 < ${Math.round((summary?.ratio_threshold ?? .85) * 100)}%`, `performance ratio < ${Math.round((summary?.ratio_threshold ?? .85) * 100)}%`)}</span></Card>
    <Card className="span-12 degradation-table-card"><CardHead code={summary?.version || 'degradation-v0.2.0'} title={tx('候选明细', 'Candidate Details')} action={<span className="table-observed">{tx('最近观测', 'LATEST OBSERVED')} {time(summary?.latest_observed_at, lang)}</span>} />
      {candidates.length > 0 ? <div className="table-wrap"><table><thead><tr><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('型号', 'Model')}</th><th>{tx('负载', 'Load')}</th><th>{tx('时钟观测 / 基线', 'Clock observed / baseline')}</th><th>{tx('性能比', 'Ratio')}</th><th>{tx('基线', 'Baseline')}</th><th>{tx('置信度', 'Confidence')}</th><th>{tx('建议', 'Recommendation')}</th></tr></thead><tbody>{candidates.map(item => <tr key={item.gpu_asset_id}><td><b>{item.node_ip}</b><small>GPU {item.gpu_index} · {item.gpu_uuid || 'UUID UNKNOWN'}</small></td><td>{item.model_name || 'UNKNOWN'}</td><td>{item.gpu_utilization.toFixed(1)}%</td><td><b>{item.observed_value.toFixed(0)} MHz</b><small>{item.baseline_value.toFixed(0)} MHz</small></td><td><Badge value={`${(item.performance_ratio * 100).toFixed(1)}%`} kind="warning" /></td><td><b>{scope(item.baseline_scope)}</b><small>{item.peer_count} {tx('个对照 GPU', 'peer GPUs')}</small></td><td><Badge value={item.data_confidence} kind={item.data_confidence === 'A' ? 'healthy' : 'info'} /></td><td><span className="recommendation">{tx('确认工作负载可比性后，在维护窗口执行 DCGM / SuperBench 复测。', 'Confirm workload comparability, then run DCGM / SuperBench in a maintenance window.')}</span><small>{tx('源时间', 'source time')} {time(item.observed_at, lang)}</small></td></tr>)}</tbody></table></div> : <Empty tx={tx} title={tx('当前没有被动衰减候选', 'No passive degradation candidates')} />}
    </Card>
    <Card className="span-12 degradation-method"><CardHead code="METHOD" title={tx('当前判定口径', 'Current Decision Contract')} /><div className="stages"><div><b>01 / ELIGIBILITY</b><h3>{tx('高负载新鲜观测', 'Fresh High-load Observation')}</h3><p>gpu_util_avg_15m ≥ {summary?.minimum_utilization ?? 80}% · confidence A/B · freshness ≤ {Math.round((summary?.freshness_sla_seconds ?? 3600) / 60)}m</p><Badge value="NON-INTRUSIVE" kind="healthy" /></div><ChevronRight /><div><b>02 / BASELINE</b><h3>{tx('稳健历史与同类基线', 'Robust Historical & Peer Baseline')}</h3><p>{tx('成熟的同型号高负载 7 天基线优先 · 同节点/集群实时中位数兜底', 'mature same-model high-load 7d first · live node/fleet median fallback')}</p><Badge value="FEATURE BASELINE v1" kind="info" /></div><ChevronRight /><div><b>03 / VALIDATE</b><h3>{tx('人工维护复测', 'Human-gated Validation')}</h3><p>DCGM · SuperBench · workload context</p><Badge value="NOT AUTOMATED" kind="warning" /></div></div></Card>
  </div>;
}
function Quality({ tx, view, targets, summary, issueSummary, inventoryError, syncRuns, assetChanges, telemetry, telemetrySummary, nodeAccess, reloadNodeAccess, openIssues, lang }: { tx: Tx; view: string; targets: CollectorTarget[]; summary: FleetSummary | null; issueSummary: IssueSummary | null; inventoryError: boolean; syncRuns: SyncRun[]; assetChanges: AssetChange[]; telemetry: TelemetryQualityItem[]; telemetrySummary: TelemetryQualitySummary | null; nodeAccess: NodeAccessOverview | null; reloadNodeAccess: () => void; openIssues: () => void; lang: string }) {
  const [credentialDraft, setCredentialDraft] = useState({ profile_id: '', priority: 10, username: '', password: '', enabled: true });
  const [credentialSaving, setCredentialSaving] = useState(false);
  const [credentialMessage, setCredentialMessage] = useState('');
  const [credentialError, setCredentialError] = useState('');
  const [checkNodeIP, setCheckNodeIP] = useState('');
  const [connectivityChecks, setConnectivityChecks] = useState<NodeAccessCheck[]>([]);
  const [connectivityLimit, setConnectivityLimit] = useState(5);
  const [connectivityHasMore, setConnectivityHasMore] = useState(false);
  const [connectivityLoadingMore, setConnectivityLoadingMore] = useState(false);
  const [connectivityChecking, setConnectivityChecking] = useState(false);
  const [connectivityError, setConnectivityError] = useState('');
  const [evidenceCollections, setEvidenceCollections] = useState<NodeEvidenceCollection[]>([]);
  const [collectionLimit, setCollectionLimit] = useState(5);
  const [collectionHasMore, setCollectionHasMore] = useState(false);
  const [collectionHistory, setCollectionHistory] = useState(false);
  const [collectionLoadingMore, setCollectionLoadingMore] = useState(false);
  const [retryingCollectionID, setRetryingCollectionID] = useState<number | null>(null);
  const [collectionError, setCollectionError] = useState('');
  const [collectionMessage, setCollectionMessage] = useState('');
  const loadConnectivityChecks = useCallback(async (limit: number) => {
    try {
      const response = await fetch(`/api/v1/node-access/checks?limit=${limit}`);
      if (!response.ok) return;
      const payload = await response.json();
      setConnectivityChecks(payload.data || []);
      setConnectivityHasMore(Boolean(payload.meta?.has_more));
    } catch { /* overview remains usable when the check ledger is unavailable */ }
  }, []);
  const loadEvidenceCollections = useCallback(async (limit: number, history: boolean) => {
    try {
      const response = await fetch(`/api/v1/node-access/collections?limit=${limit}&history=${history ? 1 : 0}`);
      if (!response.ok) return;
      const payload = await response.json();
      setEvidenceCollections(payload.data || []);
      setCollectionHasMore(Boolean(payload.meta?.has_more));
    } catch { /* node access remains usable when the collection ledger is unavailable */ }
  }, []);
  useEffect(() => {
    if (view !== 'node-access') return;
    setConnectivityLimit(5);
    setCollectionLimit(5);
    setCollectionHistory(false);
    void loadConnectivityChecks(5);
    void loadEvidenceCollections(5, false);
  }, [view, loadConnectivityChecks, loadEvidenceCollections]);
  const loadMoreConnectivityChecks = async () => {
    const nextLimit = Math.min(connectivityLimit + 25, 100);
    setConnectivityLoadingMore(true);
    await loadConnectivityChecks(nextLimit);
    setConnectivityLimit(nextLimit);
    setConnectivityLoadingMore(false);
  };
  const loadMoreEvidenceCollections = async () => {
    const nextLimit = Math.min(collectionLimit + 25, 100);
    setCollectionLoadingMore(true);
    await loadEvidenceCollections(nextLimit, collectionHistory);
    setCollectionLimit(nextLimit);
    setCollectionLoadingMore(false);
  };
  const toggleCollectionHistory = async () => {
    const next = !collectionHistory;
    setCollectionLoadingMore(true);
    setCollectionHistory(next);
    setCollectionLimit(5);
    await loadEvidenceCollections(5, next);
    setCollectionLoadingMore(false);
  };
  const runConnectivityCheck = async () => {
    setConnectivityChecking(true); setConnectivityError('');
    try {
      const response = await fetch('/api/v1/node-access/checks', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ node_ip: checkNodeIP }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setCheckNodeIP('');
      await loadConnectivityChecks(connectivityLimit);
      reloadNodeAccess();
    } catch (reason) {
      setConnectivityError(reason instanceof Error ? reason.message : tx('检查失败', 'Check failed'));
    } finally { setConnectivityChecking(false); }
  };
  const retryEvidenceCollection = async (collectionID: number) => {
    setRetryingCollectionID(collectionID); setCollectionError(''); setCollectionMessage('');
    try {
      const response = await fetch('/api/v1/node-access/collections', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ retry_collection_id: collectionID }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setCollectionMessage(tx(`采集任务 #${collectionID} 已重试并保留原审计记录`, `Collection #${collectionID} retried with the original audit record preserved`));
      await loadEvidenceCollections(collectionLimit, collectionHistory);
      reloadNodeAccess();
    } catch (reason) {
      setCollectionError(reason instanceof Error ? reason.message : tx('重试失败', 'Retry failed'));
    } finally { setRetryingCollectionID(null); }
  };
  const saveCredential = async () => {
    setCredentialSaving(true); setCredentialMessage(''); setCredentialError('');
    try {
      const response = await fetch('/api/v1/node-access/credentials', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(credentialDraft),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setCredentialDraft({ profile_id: '', priority: credentialDraft.priority + 10, username: '', password: '', enabled: true });
      setCredentialMessage(tx('凭据已加密保存；页面不会再次显示账号或密码', 'Credential encrypted and saved; the account and password will not be shown again'));
      reloadNodeAccess();
    } catch (reason) {
      setCredentialError(reason instanceof Error ? reason.message : tx('保存失败', 'Save failed'));
    } finally { setCredentialSaving(false); }
  };
  const deleteCredential = async (profileID: string) => {
    if (!window.confirm(tx(`确认从全局密码字典删除 ${profileID}？它可能仍用于其他节点，删除后无法恢复。`, `Delete ${profileID} from the global credential dictionary? Other nodes may still depend on it, and deletion cannot be undone.`))) return;
    setCredentialError(''); setCredentialMessage('');
    try {
      const response = await fetch(`/api/v1/node-access/credentials/${encodeURIComponent(profileID)}`, { method: 'DELETE' });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setCredentialMessage(tx('凭据配置已删除', 'Credential profile deleted'));
      reloadNodeAccess();
    } catch (reason) { setCredentialError(reason instanceof Error ? reason.message : tx('删除失败', 'Delete failed')); }
  };
  const definitions = [['dcgm_exporter', 'DCGM Exporter', 'GPU Telemetry'], ['gpu_exporter', 'GPU Exporter', 'NVML Extension'], ['node_exporter', 'Node Exporter', 'Host OS'], ['ipmi_exporter', 'IPMI Exporter', 'BMC / Hardware']] as const;
  const qualityLedger = <Card className="span-12 quality-ledger-card"><CardHead code="DATA QUALITY LEDGER" title={tx('数据质量问题统计', 'Data Quality Issue Statistics')} action={<div className="table-actions"><Badge value={`${issueSummary?.remaining_by_category?.data_quality || 0} REMAINING`} kind={(issueSummary?.remaining_by_category?.data_quality || 0) > 0 ? 'warning' : 'healthy'} /><button className="link quality-ledger-link" onClick={openIssues}>{tx('查看问题列表', 'View issues')}<ChevronRight size={13} /></button></div>} /><div className="issue-categories quality-ledger"><div><b>{tx('历史发现', 'Discovered')}</b><strong>{issueSummary?.by_category?.data_quality || 0}</strong><small>DATA QUALITY</small></div><div><b>{tx('已解决', 'Resolved')}</b><strong>{issueSummary?.resolved_by_category?.data_quality || 0}</strong><small>AUTO + MANUAL</small></div><div><b>{tx('遗留问题', 'Remaining')}</b><strong>{issueSummary?.remaining_by_category?.data_quality || 0}</strong><small>OPEN + IN PROGRESS</small></div><div><b>{tx('当前检测', 'Actively Detected')}</b><strong>{issueSummary?.active_by_category?.data_quality || 0}</strong><small>SOURCE ACTIVE</small></div></div></Card>;
  if (view === 'issues') return <div className="grid">
    <Card className="span-12 capability-note"><ClipboardList size={19} /><div><CardHead code="QUALITY WORKFLOW" title={tx('数据质量问题台账', 'Data Quality Issue Ledger')} /><p>{tx('集中展示数据质量问题的累计发现、已解决、遗留和当前检测数量。查看问题列表会进入数据统计，并自动应用数据质量分类筛选。', 'Review discovered, resolved, remaining and actively detected data-quality findings in one place. View issues opens Data Statistics with the data-quality category applied automatically.')}</p></div><Badge value="CLASSIFIED" kind="info" /></Card>
    {qualityLedger}
  </div>;
  if (view === 'continuity') {
    const problems = telemetry.filter(item => item.status !== 'fresh');
    return <div className="grid">
      <Card className="span-12 capability-note"><Activity size={19} /><div><CardHead code={telemetrySummary?.feature_catalog_version || 'FEATURE CATALOG'} title={tx('GPU 指标连续性', 'GPU Metric Continuity')} /><p>{tx('以现场 15 秒采样周期为基线，联合检查每卡 1 小时样本存在率、最大间隔、UUID 波动，以及 DCGM Target 的五分钟抓取成功率、样本量和耗时变化。该结果只判断遥测可信度，不作为硬件故障或性能异常。', 'Using the observed 15-second cadence, Atlas checks per-GPU one-hour presence, maximum gap and UUID flaps together with five-minute DCGM target success, sample-volume and duration ratios. This evaluates telemetry trustworthiness, not hardware faults or performance anomalies.')}</p></div><Badge value="READ-ONLY" kind="info" /></Card>
      {[[tx('GPU 总数', 'Total GPUs'), telemetrySummary?.total ?? '—', 'LATEST RUN'], [tx('连续正常', 'Fresh'), telemetrySummary?.by_status.fresh ?? 0, '≥95% · AGE≤60s'], [tx('连续性异常', 'Degraded / Stale'), (telemetrySummary?.by_status.degraded || 0) + (telemetrySummary?.by_status.stale || 0), '<95% OR AGE>60s'], [tx('平均存在率', 'Average Presence'), telemetrySummary ? `${telemetrySummary.average_presence_ratio_1h.toFixed(2)}%` : '—', `MAX GAP ${telemetrySummary?.max_metric_gap_seconds_1h?.toFixed(1) || '—'}s · FLAP ${telemetrySummary?.max_uuid_flap_count_1h?.toFixed(0) || '0'}`]].map(([label, value, note]) => <Card className="metric quality compact-quality-metric" key={String(label)}><Activity size={17} /><strong>{String(value)}</strong><b>{String(label)}</b><small>{String(note)}</small></Card>)}
      <Card className="span-12"><CardHead code="CONTINUITY ISSUES" title={tx('异常与未知 GPU', 'Degraded and Unknown GPUs')} action={<div className="table-actions"><Badge value={`SCRAPE ${telemetrySummary?.min_target_scrape_success_ratio_5m?.toFixed(1) || '—'}%`} kind={(telemetrySummary?.min_target_scrape_success_ratio_5m ?? 100) < 95 ? 'warning' : 'healthy'} /><Badge value={`${problems.length} ITEMS`} kind={problems.length ? 'warning' : 'healthy'} /></div>} /><div className="table-wrap"><table className="target-table"><thead><tr><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>UUID</th><th>{tx('状态', 'Status')}</th><th>{tx('1h 样本 / 存在率', '1h Samples / Presence')}</th><th>{tx('年龄 / 最大间隔', 'Age / Max Gap')}</th><th>{tx('UUID 波动', 'UUID Flaps')}</th><th>{tx('Target 抓取', 'Target Scrape')}</th><th>{tx('观测时间', 'Observed')}</th></tr></thead><tbody>{problems.map(item => <tr key={item.gpu_asset_id}><td><b>{item.node_ip} · GPU {item.gpu_index}</b><small>{item.model_name}</small></td><td><code>{item.gpu_uuid}</code></td><td><Badge value={item.status.toUpperCase()} kind={item.status === 'stale' ? 'danger' : 'warning'} /></td><td>{item.sample_count_1h?.toFixed(0) ?? '—'} / 240<small>{item.presence_ratio_1h != null ? `${item.presence_ratio_1h.toFixed(1)}%` : '—'}</small></td><td>{item.sample_age_seconds != null ? `${item.sample_age_seconds.toFixed(1)}s` : '—'}<small>{tx('最大', 'max')} {item.metric_gap_max_seconds_1h != null ? `${item.metric_gap_max_seconds_1h.toFixed(1)}s` : '—'}</small></td><td>{item.uuid_presence_flap_count_1h?.toFixed(0) ?? '—'}</td><td>{item.target_scrape_success_ratio_5m != null ? `${item.target_scrape_success_ratio_5m.toFixed(1)}%` : '—'}<small>{tx('样本', 'samples')} {item.target_scrape_samples_ratio_5m?.toFixed(1) ?? '—'}% · {tx('耗时比', 'duration')} {item.target_scrape_duration_ratio_5m?.toFixed(1) ?? '—'}%</small></td><td>{time(item.observed_at, lang)}</td></tr>)}</tbody></table>{problems.length === 0 && <Empty tx={tx} title={tx('GPU 指标连续性正常', 'GPU metric continuity is healthy')} />}</div></Card>
    </div>;
  }
  if (view === 'node-access') {
    const local = (value: LocalizedText) => lang.startsWith('zh') ? value.zh : value.en;
    const readOnly = nodeAccess?.commands.filter(command => command.approval_class === 'read_only') || [];
    const approvalRequired = nodeAccess?.commands.filter(command => command.approval_class === 'approval_required') || [];
    const statusKind = nodeAccess?.status === 'connectivity_ready' ? 'healthy' : nodeAccess?.status === 'credentials_missing' || nodeAccess?.status === 'known_hosts_missing' ? 'warning' : 'neutral';
    return <div className="grid node-access-page">
      <Card className="span-12 node-access-guard">
        <div><ShieldCheck size={21} /><div><CardHead code={`${nodeAccess?.skill_id || 'atlas-node-evidence'} / ${nodeAccess?.skill_version || 'v0.6.3'}`} title={tx('节点只读证据 Skill', 'Node Read-only Evidence Skill')} /><p>{tx('低负载只读信息按注册命令和固定资源预算自动采集；节点失联时等待恢复，恢复为 up 后使用全局密码字典补采，失败任务可保留历史并人工重试。', 'Low-impact read-only evidence is collected through registered commands and fixed budgets; offline nodes wait until recovery, then use the global credential dictionary for collection, while failed tasks preserve history and support manual retry.')}</p></div></div>
        <div className="node-access-badges"><Badge value={(nodeAccess?.status || 'unavailable').toUpperCase()} kind={statusKind} /><Badge value="HANDSHAKE ONLY" kind="info" /><Badge value="NO COMMAND EXECUTED" kind="healthy" /></div>
      </Card>
      {[
        [tx('连接超时', 'Connect timeout'), `${nodeAccess?.budget.connect_timeout_seconds ?? '—'}s`, 'PER ATTEMPT'],
        [tx('命令超时', 'Command timeout'), `${nodeAccess?.budget.command_timeout_seconds ?? '—'}s`, 'PER COMMAND'],
        [tx('输出上限', 'Output limit'), nodeAccess ? `${Math.round(nodeAccess.budget.max_output_bytes / 1024)} KiB` : '—', 'PER COMMAND'],
        [tx('并发节点', 'Concurrent nodes'), nodeAccess?.budget.max_concurrent_nodes ?? '—', `MAX ${nodeAccess?.budget.max_commands_per_node ?? '—'} COMMANDS / NODE`],
      ].map(([label, value, note]) => <Card className="metric quality node-access-stat" key={String(label)}><ShieldCheck size={17} /><strong>{String(value)}</strong><b>{String(label)}</b><small>{String(note)}</small></Card>)}
      <Card className="span-12">
        <CardHead code="FOUNDATIONAL SKILLS" title={tx('基础 Skill 目录', 'Foundational Skill Catalog')} action={<Badge value={`${(nodeAccess?.skills || []).length} SKILLS`} kind="info" />} />
        <p className="node-access-note">{tx('先建立证据采集、故障分析和案例学习三项基础契约。当前不提供维护、重启、节点变更或任务操作 Skill。', 'The first foundation covers evidence collection, fault analysis, and case learning. No maintenance, restart, node-change, or workload-operation Skill is provided.')}</p>
        <div className="node-skill-grid">{(nodeAccess?.skills || []).map(skill => <article key={skill.id}><header><code>{skill.id}</code><Badge value={skill.status.toUpperCase()} kind={skill.status.endsWith('_baseline') ? 'healthy' : 'info'} /></header><b>{local(skill.purpose)}</b><small>{skill.class.toUpperCase()} · {skill.version}</small></article>)}</div>
      </Card>
      <Card className="span-12">
        <CardHead code="EVENT / CONDITION POLICY" title={tx('告警与证据触发分层', 'Alert and Evidence Trigger Layers')} action={<div className="table-actions"><Badge value={`${nodeAccess?.collection_summary?.waiting_recovery || 0} WAITING RECOVERY`} kind={(nodeAccess?.collection_summary?.waiting_recovery || 0) > 0 ? 'warning' : 'healthy'} /><Badge value={`${(nodeAccess?.alert_evidence_policies || []).length} POLICIES`} kind="info" /></div>} />
        <p className="node-access-note">{tx('借鉴 Node Problem Detector：瞬时硬件事件立即留证；持续状态按恢复条件决定是否补采；数据质量和访问链路异常暂不递归登录节点。', 'Following Node Problem Detector semantics: transient hardware events collect immediately, persistent conditions may defer collection until recovery, and data-quality or access-path failures do not recursively log into nodes.')}</p>
        <div className="node-skill-grid">{(nodeAccess?.alert_evidence_policies || []).map(policy => <article key={`${policy.category}-${policy.collection_trigger}`}><header><code>{policy.category}</code><Badge value={policy.collection_trigger.toUpperCase()} kind={policy.collection_trigger === 'immediate' ? 'healthy' : policy.collection_trigger === 'after_recovery' ? 'warning' : 'neutral'} /></header><b>{local(policy.purpose)}</b><small>{policy.semantics.toUpperCase()} · {policy.issue_types.join(' · ')}</small></article>)}</div>
      </Card>
      <Card className="span-12">
        <CardHead code="CREDENTIAL ROTATION" title={tx('加密凭据字典与尝试顺序', 'Encrypted Credential Dictionary & Attempt Order')} action={<div className="table-actions"><Badge value={nodeAccess?.encryption_ready ? 'AES-256-GCM' : 'ENCRYPTION OFFLINE'} kind={nodeAccess?.encryption_ready ? 'healthy' : 'warning'} /><Badge value={`${nodeAccess?.credential_profiles.length || 0} PROFILES`} kind={nodeAccess?.credential_profiles.some(profile => profile.secret_available) ? 'healthy' : 'neutral'} /></div>} />
        <p className="node-access-note">{tx('这是面向全部受管节点的全局密码字典，不应根据单个节点的失败结果删除条目。账号和密码使用服务端主密钥加密保存；认证拒绝时按优先级继续下一项，只有全部可用条目耗尽后才产生节点访问问题。网络不可达或主机指纹异常会提前停止。', 'This is a global credential dictionary for all managed nodes; do not delete an entry based on one node rejection. Accounts and passwords are encrypted with the server master key. Rejection advances to the next priority, and an access issue is created only after every available entry is exhausted. Network or host-identity failures stop earlier.')}</p>
        <div className="credential-layout">
          <form className="credential-form" onSubmit={event => { event.preventDefault(); void saveCredential(); }}>
            <div className="credential-form-head"><div><b>{tx('新增密码字典项', 'Add credential dictionary entry')}</b><small>{tx('每项可对应不同节点或节点组；系统按优先级逐项尝试。账号和密码仍使用服务端主密钥加密保存。', 'Each entry may serve a different node or node group, and Atlas tries entries in priority order. Accounts and passwords remain encrypted with the server-side master key.')}</small></div><Badge value="GLOBAL DICTIONARY" kind="warning" /></div>
            <div className="credential-form-row"><label>Profile ID<input value={credentialDraft.profile_id} onChange={event => setCredentialDraft(current => ({ ...current, profile_id: event.target.value.toLowerCase() }))} placeholder="node-password-a" /></label><label>{tx('优先级', 'Priority')}<input type="number" min={1} max={10000} value={credentialDraft.priority} onChange={event => setCredentialDraft(current => ({ ...current, priority: Number(event.target.value) }))} /></label></div>
            <label>{tx('节点账号', 'Node username')}<input autoComplete="off" value={credentialDraft.username} onChange={event => { const username = event.target.value; const slug = username.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, ''); setCredentialDraft(current => ({ ...current, username, profile_id: current.profile_id || (slug ? `${slug}-primary` : '') })); }} /></label>
            <label>{tx('节点密码', 'Node password')}<input type="password" autoComplete="new-password" value={credentialDraft.password} onChange={event => setCredentialDraft(current => ({ ...current, password: event.target.value }))} /></label>
            <label className="training-check"><input type="checkbox" checked={credentialDraft.enabled} onChange={event => setCredentialDraft(current => ({ ...current, enabled: event.target.checked }))} /><span>{tx('启用并加入认证尝试顺序', 'Enable and include in authentication order')}</span></label>
            {credentialError && <p className="form-error">{credentialError}</p>}{credentialMessage && <p className="form-success">{credentialMessage}</p>}
            <button className="primary-action" type="submit" disabled={credentialSaving || !nodeAccess?.management_ready || !credentialDraft.profile_id || !credentialDraft.username || !credentialDraft.password}><Save size={15} />{credentialSaving ? tx('加密保存中', 'Encrypting') : tx('加密保存', 'Encrypt & Save')}</button>
          </form>
          <div className="table-wrap credential-table-wrap"><table className="node-access-table"><thead><tr><th>{tx('顺序', 'Order')}</th><th>PROFILE</th><th>{tx('账号', 'Username')}</th><th>{tx('认证方式', 'Auth type')}</th><th>{tx('密钥来源', 'Secret provider')}</th><th>{tx('状态', 'Status')}</th><th>{tx('操作', 'Action')}</th></tr></thead><tbody>{nodeAccess?.credential_profiles.map(profile => <tr key={profile.id}><td><code>{profile.priority}</code></td><td><b>{profile.id}</b></td><td><code>{profile.username_masked}</code></td><td>{profile.auth_type}</td><td><Badge value={profile.secret_provider.toUpperCase()} kind="info" /></td><td><Badge value={profile.status.toUpperCase()} kind={profile.status === 'ready' ? 'healthy' : profile.status === 'secret_unavailable' ? 'warning' : 'neutral'} /></td><td><button className="danger-icon" type="button" onClick={() => void deleteCredential(profile.id)} title={tx('删除凭据配置', 'Delete credential profile')}><Trash2 size={14} /></button></td></tr>)}</tbody></table>{!nodeAccess?.credential_profiles.length && <Empty tx={tx} title={tx('尚未配置加密凭据', 'No encrypted credentials configured')} />}</div>
        </div>
      </Card>
      <Card className="span-12 connectivity-card">
        <CardHead code="KNOWN-HOST SSH" title={tx('节点认证连通性检查', 'Node Authentication Connectivity Check')} action={<div className="table-actions"><Badge value={nodeAccess?.known_hosts_ready ? 'KNOWN HOSTS READY' : 'KNOWN HOSTS MISSING'} kind={nodeAccess?.known_hosts_ready ? 'healthy' : 'warning'} /><Badge value={`${connectivityChecks.length} RECENT`} kind="info" /></div>} />
        <p className="node-access-note">{tx('仅检查目标是否属于 Atlas 当前资产、SSH 主机指纹是否匹配以及凭据能否认证。不会执行 hostname、日志查询或任何其他命令；失败会进入 access 分类问题台账，后续成功会自动恢复。', 'Checks only whether the target is a current Atlas asset, its SSH host key matches, and a credential authenticates. It runs no hostname, log, or other command; failures enter the access issue ledger and a later success clears them automatically.')}</p>
        <div className="connectivity-runner">
          <label>{tx('节点 IP', 'Node IP')}<input value={checkNodeIP} onChange={event => setCheckNodeIP(event.target.value)} placeholder="10.114.4.25" /></label>
          <button className="primary-action" type="button" onClick={() => void runConnectivityCheck()} disabled={connectivityChecking || !nodeAccess?.connectivity_check_enabled || !nodeAccess?.known_hosts_ready || !nodeAccess?.credential_profiles.some(profile => profile.secret_available) || !checkNodeIP}><Network size={15} />{connectivityChecking ? tx('检查中', 'Checking') : tx('只检查认证', 'Check Authentication')}</button>
        </div>
        {connectivityError && <p className="form-error">{connectivityError}</p>}
        <div className="table-wrap connectivity-table-wrap"><table className="node-access-table"><thead><tr><th>{tx('节点', 'Node')}</th><th>{tx('结果', 'Result')}</th><th>PROFILE</th><th>{tx('尝试', 'Attempts')}</th><th>{tx('提醒', 'Alert')}</th><th>{tx('完成时间', 'Finished')}</th></tr></thead><tbody>{connectivityChecks.map(check => <tr key={check.id}><td><b>{check.node_ip}</b><small>#{check.id}</small></td><td><Badge value={check.status.toUpperCase()} kind={check.status === 'authenticated' ? 'healthy' : check.status === 'host_identity_failed' ? 'danger' : 'warning'} /></td><td><code>{check.credential_profile_id || '—'}</code></td><td><small>{(check.attempts || []).join(' · ') || '—'}</small></td><td><Badge value={check.alert_required ? 'REQUIRED' : 'NO'} kind={check.alert_required ? 'warning' : 'neutral'} /></td><td>{time(check.finished_at, lang)}</td></tr>)}</tbody></table>{connectivityChecks.length === 0 && <Empty tx={tx} title={tx('尚无节点认证检查记录', 'No node authentication checks yet')} />}</div>
        {connectivityHasMore && <button className="module-history-more connectivity-more" type="button" onClick={() => void loadMoreConnectivityChecks()} disabled={connectivityLoadingMore}>{connectivityLoadingMore ? tx('查询中…', 'Loading…') : tx('更多记录', 'Load more')}<ChevronRight size={13} /></button>}
      </Card>
      <Card className="span-12 evidence-collection-card">
        <CardHead code="EVIDENCE COLLECTION AUDIT" title={tx('节点证据采集任务', 'Node Evidence Collection Tasks')} action={<div className="table-actions"><Badge value={`${nodeAccess?.collection_summary?.completed || 0} CURRENT COMPLETED`} kind="healthy" /><Badge value={`${nodeAccess?.collection_summary?.failed || 0} CURRENT FAILED`} kind={(nodeAccess?.collection_summary?.failed || 0) > 0 ? 'warning' : 'neutral'} /><button className="collection-history-toggle" type="button" onClick={() => void toggleCollectionHistory()} disabled={collectionLoadingMore}>{collectionHistory ? tx('仅看当前结果', 'Current results only') : tx('查看历史尝试', 'View attempt history')}</button></div>} />
        <p className="node-access-note">{collectionHistory ? tx('正在展示完整审计历史，包括已被成功重试覆盖的旧失败和 partial 尝试；这些记录不会计入当前失败。', 'Showing the complete audit history, including old failed and partial attempts superseded by successful retries. These records do not count as current failures.') : tx('默认只展示每个事件或持续状态的最新采集结果。旧失败和 partial 尝试仍保留在审计历史，需要时可单独查看。', 'By default, only the latest collection result for each event or persistent condition is shown. Old failed and partial attempts remain in audit history and can be viewed separately when needed.')}</p>
        {collectionError && <p className="form-error">{collectionError}</p>}{collectionMessage && <p className="form-success">{collectionMessage}</p>}
        <div className="table-wrap evidence-collection-wrap"><table className="node-access-table evidence-collection-table"><thead><tr><th>ID</th><th>{tx('节点 / 来源', 'Node / Source')}</th><th>{tx('触发', 'Trigger')}</th><th>{tx('结果', 'Result')}</th><th>{tx('注册命令', 'Registered Commands')}</th><th>{tx('输出', 'Output')}</th><th>{tx('完成时间', 'Finished')}</th><th>{tx('操作', 'Action')}</th></tr></thead><tbody>{evidenceCollections.map(collection => { const retryable = collection.status === 'failed' || collection.status === 'partial'; const source = collection.fault_event_id ? `EVENT #${collection.fault_event_id}` : collection.platform_issue_id ? `ISSUE #${collection.platform_issue_id}` : 'UNKNOWN'; return <tr key={collection.id}><td><code>#{collection.id}</code>{collection.retry_of_collection_id ? <small>RETRY OF #{collection.retry_of_collection_id}</small> : null}</td><td><b>{collection.node_ip}</b><small>{source}</small></td><td><Badge value={(collection.trigger || 'legacy').toUpperCase()} kind={collection.trigger === 'manual_retry' ? 'info' : collection.trigger === 'after_recovery' ? 'warning' : 'neutral'} /></td><td><Badge value={collection.status.toUpperCase()} kind={collection.status === 'completed' ? 'healthy' : collection.status === 'failed' ? 'danger' : 'warning'} /><small>{collection.failure_code || collection.credential_profile_id || '—'}</small></td><td><div className="collection-command-meta">{(collection.records || []).map(record => <span key={record.id}><code>{record.command_id}</code><Badge value={record.status.toUpperCase()} kind={record.status === 'completed' ? 'healthy' : 'warning'} /></span>)}{!(collection.records || []).length && <small>{collection.command_count || 0} COMMANDS</small>}</div></td><td>{collection.output_bytes ? `${(collection.output_bytes / 1024).toFixed(1)} KiB` : '—'}<small>{collection.output_truncated ? 'TRUNCATED' : 'BOUNDED'}</small></td><td>{time(collection.finished_at, lang)}</td><td><button className="collection-retry" type="button" onClick={() => void retryEvidenceCollection(collection.id)} disabled={!retryable || retryingCollectionID !== null} title={retryable ? tx('使用当前密码字典重试只读采集', 'Retry read-only collection with the current credential dictionary') : tx('只有失败或部分成功任务可重试', 'Only failed or partial tasks can be retried')}><RefreshCw size={13} />{retryingCollectionID === collection.id ? tx('重试中', 'Retrying') : tx('重试', 'Retry')}</button></td></tr>; })}</tbody></table>{evidenceCollections.length === 0 && <Empty tx={tx} title={collectionHistory ? tx('尚无历史采集任务', 'No historical collection attempts') : tx('尚无当前采集结果', 'No current collection results')} />}</div>
        {collectionHasMore && <button className="module-history-more connectivity-more" type="button" onClick={() => void loadMoreEvidenceCollections()} disabled={collectionLoadingMore}>{collectionLoadingMore ? tx('查询中…', 'Loading…') : tx('更多记录', 'Load more')}<ChevronRight size={13} /></button>}
      </Card>
      <Card className="span-7 node-command-card">
        <CardHead code="DEFAULT / READ-ONLY" title={tx('默认自动采集', 'Default Automatic Collection')} action={<Badge value={`${readOnly.length} COMMANDS`} kind="healthy" />} />
        <p className="node-access-note">{tx('系统状态、系统参数、GPU、PCIe、注册服务状态、限定时间窗日志与 BMC 只读信息均无需逐次计划或人工授权；采集器必须遵守固定超时、输出和并发预算。', 'System state, parameters, GPU, PCIe, registered service state, bounded logs, and read-only BMC evidence require no per-run plan or human authorization. The runner must enforce fixed timeout, output, and concurrency budgets.')}</p>
        <div className="node-command-list">{readOnly.map(command => <article key={command.id}><header><code>{command.id}</code><Badge value="DEFAULT" kind="healthy" /></header><b>{local(command.purpose)}</b><small>{command.preview}</small></article>)}</div>
      </Card>
      <Card className="span-5 node-command-card approval">
        <CardHead code="HUMAN CONFIRMATION" title={tx('必须指定节点和操作并确认', 'Exact Node and Action Confirmation')} action={<Badge value={`${approvalRequired.length} COMMANDS`} kind="warning" />} />
        <div className="node-command-list">{approvalRequired.map(command => <article key={command.id}><header><code>{command.id}</code><Badge value="APPROVAL" kind="warning" /></header><b>{local(command.purpose)}</b><small>{command.preview}</small></article>)}</div>
      </Card>
      <Card className="span-12">
        <CardHead code="ENFORCED BOUNDARIES" title={tx('当前强制边界', 'Current Enforced Boundaries')} />
        <div className="rules">{(nodeAccess?.boundaries || []).map(item => <span key={item.en}><ShieldAlert size={15} />{local(item)}</span>)}</div>
      </Card>
    </div>;
  }
  if (view === 'identity') return <div className="grid"><Card className="span-12"><CardHead code="IDENTITY" title={tx('资产身份映射', 'Asset Identity')} /><div className="identity">{['HOST', 'BMC', 'CHASSIS', 'PCIE SLOT', 'BUS ID', 'GPU UUID'].map((x, i) => <span key={x}>{x}{i < 5 && <ChevronRight size={13} />}</span>)}</div></Card><Card className="span-12"><CardHead code="BMC / IPMI" title={tx('带外数据', 'Out-of-band Data')} /><div className="check-grid">{['SEL', 'PSU / VOLTAGE', 'FAN / THERMAL', 'POWER', 'PCIE SLOT', 'MEMORY CE / UE'].map(x => <span key={x}><CheckCircle2 size={14} />{x}</span>)}</div></Card></div>;
  if (view === 'audit') return <div className="grid">
    <Card className="span-12"><CardHead code="SYNC RUNS" title={tx('同步批次', 'Reconciliation Runs')} action={<Badge value={`${syncRuns.length} RUNS`} kind="info" />} /><div className="table-wrap"><table className="audit-table"><thead><tr><th>ID</th><th>{tx('任务', 'Task')}</th><th>{tx('状态', 'Status')}</th><th>{tx('节点 / GPU / UUID', 'Nodes / GPUs / UUIDs')}</th><th>TARGETS</th><th>{tx('变化', 'Changes')}</th><th>{tx('开始时间', 'Started')}</th><th>{tx('耗时', 'Duration')}</th></tr></thead><tbody>{syncRuns.map(run => <tr key={run.id}><td><code>#{run.id}</code></td><td><b>{run.task_type || 'legacy'}</b></td><td><Badge value={run.status.toUpperCase()} kind={run.status === 'success' ? 'healthy' : run.status === 'failed' ? 'danger' : 'warning'} /></td><td>{run.node_count} / {run.gpu_count} / {run.known_uuid_count}</td><td>{run.target_count}</td><td>{run.change_count}</td><td>{time(run.started_at, lang)}</td><td>{run.finished_at ? `${Math.max(0, (new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()) / 1000).toFixed(1)}s` : '—'}</td></tr>)}</tbody></table>{syncRuns.length === 0 && <Empty tx={tx} title={tx('暂无同步批次', 'No sync runs')} />}</div></Card>
    <Card className="span-12"><CardHead code="CHANGE LOG" title={tx('资产变更', 'Asset Changes')} action={<Badge value={`${assetChanges.length} EVENTS`} kind={assetChanges.length ? 'warning' : 'healthy'} />} /><div className="table-wrap"><table className="audit-table"><thead><tr><th>ID</th><th>{tx('事件', 'Event')}</th><th>{tx('节点', 'Node')}</th><th>{tx('对象', 'Entity')}</th><th>{tx('原值', 'Before')}</th><th>{tx('新值', 'After')}</th><th>RUN</th><th>{tx('时间', 'Time')}</th></tr></thead><tbody>{assetChanges.map(change => <tr key={change.id}><td><code>#{change.id}</code></td><td><Badge value={change.event_type.toUpperCase()} kind={change.event_type.includes('retired') ? 'danger' : 'warning'} /></td><td><b>{change.node_ip}</b></td><td><code>{change.asset_key}</code></td><td><code>{change.old_value || '—'}</code></td><td><code>{change.new_value || '—'}</code></td><td>#{change.sync_run_id || '—'}</td><td>{time(change.created_at, lang)}</td></tr>)}</tbody></table>{assetChanges.length === 0 && <Empty tx={tx} title={tx('暂无资产变化', 'No asset changes')} />}</div></Card>
  </div>;
  const problemTargets = targets.filter(target => target.health !== 'up');
  return <div className="grid"><Card className="span-12 capability-note"><Database size={19} /><div><CardHead code="MONITORING-ONLY" title={tx('监控数据质量发现', 'Monitoring Data Quality Detection')} /><p>{tx('仅针对 LXOP 实时在用 GPU 资产，基于 Prometheus 指标、Target、时序连续性和资产对账识别缺失、失效、漂移与覆盖异常；停机、下架及已移除 IP 自动退出统计。平台不自动修改 exporter、监控配置或节点。', 'For live in-use LXOP GPU assets only, Atlas detects missing, stale, drifting, or uncovered telemetry from Prometheus metrics, targets, continuity, and reconciliation. Stopped, retired, and removed IPs automatically leave statistics. Atlas does not modify exporters, monitoring configuration, or nodes.')}</p></div><Badge value="READ-ONLY" kind="info" /></Card>{definitions.map(([job, name, use]) => { const rows = targets.filter(x => x.job === job); const up = rows.filter(x => x.health === 'up').length; const down = rows.length - up; return <Card className="metric quality compact-quality-metric coverage-metric" key={job}><Server size={17} /><Badge value={inventoryError ? 'API ERROR' : down ? `${down} NOT UP` : rows.length ? 'ALL UP' : 'WAITING'} kind={inventoryError || down ? 'warning' : rows.length ? 'healthy' : 'neutral'} /><strong>{rows.length ? up : '—'}</strong><b>{name}</b><small>{use} · {rows.length || summary?.nodes.total || '—'} TARGETS</small></Card>; })}
    <Card className="span-12"><CardHead code="TARGET ISSUES" title={tx('非正常 Target', 'Non-UP Targets')} action={<Badge value={`${problemTargets.length} ISSUES`} kind={problemTargets.length ? 'warning' : 'healthy'} />} /><div className="table-wrap"><table className="target-table"><thead><tr><th>{tx('节点', 'Node')}</th><th>TARGET</th><th>{tx('状态', 'Health')}</th><th>{tx('原因', 'Reason')}</th><th>{tx('抑制', 'Suppression')}</th><th>{tx('原始错误', 'Last Error')}</th><th>{tx('同步时间', 'Synced')}</th></tr></thead><tbody>{problemTargets.map(target => <tr key={`${target.job}-${target.node_ip}`}><td><b>{target.node_ip}</b><small>{target.target_ip}</small></td><td><code>{target.job}</code></td><td><Badge value={target.health.toUpperCase()} kind={target.health === 'down' ? 'danger' : 'warning'} /></td><td><code>{target.reason_code || 'unclassified'}</code></td><td>{target.suppressed ? <Badge value={target.suppression_reason || 'SUPPRESSED'} kind="info" /> : <Badge value="ACTIONABLE" kind="warning" />}</td><td><small>{target.last_error || '—'}</small></td><td>{time(target.last_synced_at, lang)}</td></tr>)}</tbody></table>{problemTargets.length === 0 && <Empty tx={tx} title={tx('Target 全部正常', 'All targets healthy')} />}</div></Card>
  </div>;
}
function Models({ tx, view, lang }: { tx: Tx; view: string; lang: string }) {
  const pct = (value?: number) => value === undefined ? '—' : `${(value * 100).toFixed(1)}%`;
  const rankingRows = (rows?: RankingAtK[]) => (rows || []).filter(row => row.eligible > 0);
  const RankingTable = ({ label, rows: inputRows }: { label: string; rows?: RankingAtK[] }) => {
    const rows = rankingRows(inputRows);
    return <div className="table-wrap prediction-ranking-wrap"><table className="prediction-ranking-table"><thead><tr><th>{label}</th><th>HITS</th><th>PRECISION</th><th>RECALL</th><th>NDCG</th><th>LIFT</th></tr></thead><tbody>{rows.map(row => <tr key={`${label}-${row.k}`}><td><b>@{row.k}</b><small>{row.positives} POS / {row.eligible} ELIGIBLE</small></td><td>{row.hits}</td><td>{pct(row.precision)}</td><td>{pct(row.recall)}</td><td>{row.ndcg === undefined ? '—' : row.ndcg.toFixed(3)}</td><td>{row.lift === undefined ? '—' : `${row.lift.toFixed(2)}x`}</td></tr>)}</tbody></table>{rows.length === 0 && <Empty tx={tx} title={tx('尚无可排序的成熟预测样本', 'No mature scored predictions for ranking metrics')} />}</div>;
  };
  const readinessPageSize = 50;
  const [prediction, setPrediction] = useState<PredictionOverview | null>(null);
  const [predictionAccuracy, setPredictionAccuracy] = useState<PredictionAccuracy | null>(null);
  const [predictionOutcomeReport, setPredictionOutcomeReport] = useState<PredictionOutcomeReport | null>(null);
  const [predictionModelGovernance, setPredictionModelGovernance] = useState<PredictionModelGovernance | null>(null);
  const [heaRankChallenger, setHeaRankChallenger] = useState<HeaRankChallengerReport | null>(null);
  const [predictionOutcomes, setPredictionOutcomes] = useState<PredictionOutcome[]>([]);
  const [featureParityAudits, setFeatureParityAudits] = useState<PredictionFeatureParityAudit[]>([]);
  const [featureReplayRuns, setFeatureReplayRuns] = useState<PredictionFeatureReplayRun[]>([]);
  const [liveCoverageAudits, setLiveCoverageAudits] = useState<PredictionLiveCoverageAudit[]>([]);
  const [shadowScoringRuns, setShadowScoringRuns] = useState<PredictionShadowScoringRun[]>([]);
  const [riskPredictions, setRiskPredictions] = useState<HardwareRiskPrediction[]>([]);
  const [readiness, setReadiness] = useState<PredictionReadinessItem[]>([]);
  const [failureLabels, setFailureLabels] = useState<FailureLabel[]>([]);
  const [historySources, setHistorySources] = useState<MonitoringHistorySource[]>([]);
  const [historyBackfills, setHistoryBackfills] = useState<HistoryBackfillRun[]>([]);
  const [identityBackfills, setIdentityBackfills] = useState<HistoryBackfillRun[]>([]);
  const [identityIntervals, setIdentityIntervals] = useState<HistoricalGPUIdentityInterval[]>([]);
  const [identitySummary, setIdentitySummary] = useState<HistoricalIdentitySummary | null>(null);
  const [datasetBuilds, setDatasetBuilds] = useState<TrainingDatasetBuild[]>([]);
  const [featureBuilds, setFeatureBuilds] = useState<TrainingFeatureBuild[]>([]);
  const [preparationBuilds, setPreparationBuilds] = useState<TrainingPreparationBuild[]>([]);
  const [controlFeatureBuilds, setControlFeatureBuilds] = useState<TrainingControlFeatureBuild[]>([]);
  const [trainingMatrixBuilds, setTrainingMatrixBuilds] = useState<TrainingMatrixBuild[]>([]);
  const [matrixReadinessReport, setMatrixReadinessReport] = useState<MatrixReadinessReport | null>(null);
  const [baselineModelBuilds, setBaselineModelBuilds] = useState<BaselineModelBuild[]>([]);
  const [baselineEvaluationReport, setBaselineEvaluationReport] = useState<BaselineEvaluationReport | null>(null);
  const [historicalCandidates, setHistoricalCandidates] = useState<HistoricalFaultCandidate[]>([]);
  const [historicalCandidateSummary, setHistoricalCandidateSummary] = useState<HistoricalCandidateSummary | null>(null);
  const [trainingCohortPolicy, setTrainingCohortPolicy] = useState<TrainingCohortPolicy | null>(null);
  const [historyAuditRunning, setHistoryAuditRunning] = useState(false);
  const [historyBackfillRunning, setHistoryBackfillRunning] = useState(false);
  const [identityBackfillRunning, setIdentityBackfillRunning] = useState(false);
  const [datasetBuildRunning, setDatasetBuildRunning] = useState(false);
  const [featureBuildStarting, setFeatureBuildStarting] = useState(false);
  const [preparationBuilding, setPreparationBuilding] = useState(false);
  const [controlFeatureStarting, setControlFeatureStarting] = useState(false);
  const [trainingMatrixStarting, setTrainingMatrixStarting] = useState(false);
  const [baselineModelStarting, setBaselineModelStarting] = useState(false);
  const [featureReplayStarting, setFeatureReplayStarting] = useState(false);
  const [liveCoverageStarting, setLiveCoverageStarting] = useState(false);
  const [shadowScoringStarting, setShadowScoringStarting] = useState(false);
  const [reviewingCandidateID, setReviewingCandidateID] = useState<number | null>(null);
  const [readinessPage, setReadinessPage] = useState(0);
  const [predictionError, setPredictionError] = useState('');
  useEffect(() => {
    if (view !== 'prediction') return;
    let cancelled = false;
    const loadPrediction = async () => {
      try {
        const [overviewResponse, readinessResponse, labelsResponse, accuracyResponse, outcomeReportResponse, governanceResponse, challengerResponse, outcomesResponse, resultsResponse, parityResponse, replayResponse, coverageResponse, shadowResponse, historyResponse, backfillResponse, candidatesResponse] = await Promise.all([fetch('/api/v1/prediction/overview'), fetch('/api/v1/prediction/readiness'), fetch('/api/v1/prediction/labels'), fetch('/api/v1/prediction/accuracy'), fetch('/api/v1/prediction/outcome-report'), fetch('/api/v1/prediction/model-governance'), fetch('/api/v1/prediction/hearank-challenger'), fetch('/api/v1/prediction/outcomes'), fetch('/api/v1/prediction/results'), fetch('/api/v1/prediction/feature-parity'), fetch('/api/v1/prediction/history/feature-replays'), fetch('/api/v1/prediction/history/live-coverage'), fetch('/api/v1/prediction/history/shadow-scoring'), fetch('/api/v1/prediction/history/sources'), fetch('/api/v1/prediction/history/backfills'), fetch('/api/v1/prediction/history/candidates')]);
        if (!overviewResponse.ok || !readinessResponse.ok || !labelsResponse.ok || !accuracyResponse.ok || !outcomeReportResponse.ok || !governanceResponse.ok || !challengerResponse.ok || !outcomesResponse.ok || !resultsResponse.ok || !parityResponse.ok || !replayResponse.ok || !coverageResponse.ok || !shadowResponse.ok || !historyResponse.ok || !backfillResponse.ok || !candidatesResponse.ok) throw new Error(`HTTP ${overviewResponse.status}/${readinessResponse.status}/${labelsResponse.status}/${accuracyResponse.status}/${outcomeReportResponse.status}/${governanceResponse.status}/${challengerResponse.status}/${outcomesResponse.status}/${resultsResponse.status}/${parityResponse.status}/${replayResponse.status}/${coverageResponse.status}/${shadowResponse.status}/${historyResponse.status}/${backfillResponse.status}/${candidatesResponse.status}`);
        const [overviewPayload, readinessPayload, labelsPayload, accuracyPayload, outcomeReportPayload, governancePayload, challengerPayload, outcomesPayload, resultsPayload, parityPayload, replayPayload, coveragePayload, shadowPayload, historyPayload, backfillPayload, candidatesPayload] = await Promise.all([overviewResponse.json(), readinessResponse.json(), labelsResponse.json(), accuracyResponse.json(), outcomeReportResponse.json(), governanceResponse.json(), challengerResponse.json(), outcomesResponse.json(), resultsResponse.json(), parityResponse.json(), replayResponse.json(), coverageResponse.json(), shadowResponse.json(), historyResponse.json(), backfillResponse.json(), candidatesResponse.json()]);
        if (!cancelled) {
          setPrediction(overviewPayload.data || null);
          setReadiness(Array.isArray(readinessPayload.data) ? readinessPayload.data : []);
          setFailureLabels(Array.isArray(labelsPayload.data) ? labelsPayload.data : []);
          setPredictionAccuracy(accuracyPayload.data || null);
          setPredictionOutcomeReport(outcomeReportPayload.data || null);
          setPredictionModelGovernance(governancePayload.data || null);
          setHeaRankChallenger(challengerPayload.data || null);
          setPredictionOutcomes(Array.isArray(outcomesPayload.data) ? outcomesPayload.data : []);
          setRiskPredictions(Array.isArray(resultsPayload.data) ? resultsPayload.data : []);
          setFeatureParityAudits(Array.isArray(parityPayload.data) ? parityPayload.data : []);
          setFeatureReplayRuns(Array.isArray(replayPayload.data) ? replayPayload.data : []);
          setLiveCoverageAudits(Array.isArray(coveragePayload.data) ? coveragePayload.data : []);
          setShadowScoringRuns(Array.isArray(shadowPayload.data) ? shadowPayload.data : []);
          setHistorySources(Array.isArray(historyPayload.data) ? historyPayload.data : []);
          setHistoryBackfills(Array.isArray(backfillPayload.data) ? backfillPayload.data : []);
          setHistoricalCandidates(Array.isArray(candidatesPayload.data) ? candidatesPayload.data : []);
          setHistoricalCandidateSummary(candidatesPayload.summary || null);
          setTrainingCohortPolicy(candidatesPayload.training_policy || null);
          setPredictionError('');
        }
      } catch (reason) {
        if (!cancelled) setPredictionError(reason instanceof Error ? reason.message : String(reason));
      }
    };
    void loadPrediction();
    return () => { cancelled = true; };
  }, [view]);
  useEffect(() => {
    if (view !== 'prediction') return;
    let cancelled = false;
    const loadIdentityHistory = async () => {
      try {
        const [runsResponse, identitiesResponse, datasetsResponse, featuresResponse, preparationsResponse, controlsResponse, matricesResponse, baselinesResponse] = await Promise.all([
          fetch('/api/v1/prediction/history/identity-backfills'),
          fetch('/api/v1/prediction/history/identities'),
          fetch('/api/v1/prediction/history/datasets'),
          fetch('/api/v1/prediction/history/feature-datasets'),
          fetch('/api/v1/prediction/history/training-preparations'),
          fetch('/api/v1/prediction/history/control-feature-datasets'),
          fetch('/api/v1/prediction/history/training-matrices'),
          fetch('/api/v1/prediction/history/baseline-models'),
        ]);
        if (!runsResponse.ok || !identitiesResponse.ok || !datasetsResponse.ok || !featuresResponse.ok || !preparationsResponse.ok || !controlsResponse.ok || !matricesResponse.ok || !baselinesResponse.ok) throw new Error(`HTTP ${runsResponse.status}/${identitiesResponse.status}/${datasetsResponse.status}/${featuresResponse.status}/${preparationsResponse.status}/${controlsResponse.status}/${matricesResponse.status}/${baselinesResponse.status}`);
        const [runsPayload, identitiesPayload, datasetsPayload, featuresPayload, preparationsPayload, controlsPayload, matricesPayload, baselinesPayload] = await Promise.all([runsResponse.json(), identitiesResponse.json(), datasetsResponse.json(), featuresResponse.json(), preparationsResponse.json(), controlsResponse.json(), matricesResponse.json(), baselinesResponse.json()]);
        if (!cancelled) {
          setIdentityBackfills(Array.isArray(runsPayload.data) ? runsPayload.data : []);
          setIdentityIntervals(Array.isArray(identitiesPayload.data) ? identitiesPayload.data : []);
          setIdentitySummary(identitiesPayload.summary || null);
          setDatasetBuilds(Array.isArray(datasetsPayload.data) ? datasetsPayload.data : []);
          setFeatureBuilds(Array.isArray(featuresPayload.data) ? featuresPayload.data : []);
          setPreparationBuilds(Array.isArray(preparationsPayload.data) ? preparationsPayload.data : []);
          setControlFeatureBuilds(Array.isArray(controlsPayload.data) ? controlsPayload.data : []);
          setTrainingMatrixBuilds(Array.isArray(matricesPayload.data) ? matricesPayload.data : []);
          setBaselineModelBuilds(Array.isArray(baselinesPayload.data) ? baselinesPayload.data : []);
        }
      } catch (reason) {
        if (!cancelled) setPredictionError(reason instanceof Error ? reason.message : String(reason));
      }
    };
    void loadIdentityHistory();
    return () => { cancelled = true; };
  }, [view]);
  useEffect(() => {
    if (!featureBuilds.some(build => build.status === 'queued' || build.status === 'running')) return;
    const timer = window.setInterval(() => {
      void (async () => {
        try {
          const response = await fetch('/api/v1/prediction/history/feature-datasets');
          if (!response.ok) return;
          const payload = await response.json();
          setFeatureBuilds(Array.isArray(payload.data) ? payload.data : []);
        } catch {
          // Keep polling while the durable extraction task is active.
        }
      })();
    }, 3000);
    return () => window.clearInterval(timer);
  }, [featureBuilds]);
  useEffect(() => {
    if (!controlFeatureBuilds.some(build => build.status === 'queued' || build.status === 'running')) return;
    const timer = window.setInterval(() => {
      void (async () => {
        try {
          const response = await fetch('/api/v1/prediction/history/control-feature-datasets');
          if (!response.ok) return;
          const payload = await response.json();
          setControlFeatureBuilds(Array.isArray(payload.data) ? payload.data : []);
        } catch {
          // Keep the last durable progress while the deployment-node task runs.
        }
      })();
    }, 3000);
    return () => window.clearInterval(timer);
  }, [controlFeatureBuilds]);
  useEffect(() => {
    if (!trainingMatrixBuilds.some(build => build.status === 'queued' || build.status === 'running')) return;
    const timer = window.setInterval(() => {
      void (async () => {
        try {
          const response = await fetch('/api/v1/prediction/history/training-matrices');
          if (!response.ok) return;
          const payload = await response.json();
          setTrainingMatrixBuilds(Array.isArray(payload.data) ? payload.data : []);
        } catch {
          // Keep the durable matrix state while assembly is active.
        }
      })();
    }, 3000);
    return () => window.clearInterval(timer);
  }, [trainingMatrixBuilds]);
  useEffect(() => {
    if (!baselineModelBuilds.some(build => build.status === 'queued' || build.status === 'running')) return;
    const timer = window.setInterval(() => { void fetch('/api/v1/prediction/history/baseline-models').then(response => response.ok ? response.json() : null).then(payload => { if (payload) setBaselineModelBuilds(Array.isArray(payload.data) ? payload.data : []); }).catch(() => undefined); }, 3000);
    return () => window.clearInterval(timer);
  }, [baselineModelBuilds]);
  useEffect(() => {
    if (!featureReplayRuns.some(run => run.status === 'queued' || run.status === 'running')) return;
    const timer = window.setInterval(() => {
      void Promise.all([fetch('/api/v1/prediction/history/feature-replays'), fetch('/api/v1/prediction/feature-parity')]).then(async ([runsResponse, parityResponse]) => {
        if (!runsResponse.ok || !parityResponse.ok) return;
        const [runsPayload, parityPayload] = await Promise.all([runsResponse.json(), parityResponse.json()]);
        setFeatureReplayRuns(Array.isArray(runsPayload.data) ? runsPayload.data : []);
        setFeatureParityAudits(Array.isArray(parityPayload.data) ? parityPayload.data : []);
      }).catch(() => undefined);
    }, 3000);
    return () => window.clearInterval(timer);
  }, [featureReplayRuns]);
  useEffect(() => {
    const status = featureReplayRuns[0]?.status;
    if (status !== 'passed' && status !== 'failed') return;
    void fetch('/api/v1/prediction/feature-parity').then(response => response.ok ? response.json() : null).then(payload => {
      if (payload) setFeatureParityAudits(Array.isArray(payload.data) ? payload.data : []);
    }).catch(() => undefined);
  }, [featureReplayRuns]);
  useEffect(() => {
    if (!liveCoverageAudits.some(audit => audit.status === 'queued' || audit.status === 'running')) return;
    const timer = window.setInterval(() => {
      void Promise.all([fetch('/api/v1/prediction/history/live-coverage'), fetch('/api/v1/prediction/feature-parity')]).then(async ([auditsResponse, parityResponse]) => {
        if (!auditsResponse.ok || !parityResponse.ok) return;
        const [auditsPayload, parityPayload] = await Promise.all([auditsResponse.json(), parityResponse.json()]);
        setLiveCoverageAudits(Array.isArray(auditsPayload.data) ? auditsPayload.data : []);
        setFeatureParityAudits(Array.isArray(parityPayload.data) ? parityPayload.data : []);
      }).catch(() => undefined);
    }, 3000);
    return () => window.clearInterval(timer);
  }, [liveCoverageAudits]);
  useEffect(() => {
    if (!shadowScoringRuns.some(run => run.status === 'queued' || run.status === 'running')) return;
    const timer = window.setInterval(() => {
      void Promise.all([fetch('/api/v1/prediction/history/shadow-scoring'), fetch('/api/v1/prediction/results'), fetch('/api/v1/prediction/overview')]).then(async ([runsResponse, resultsResponse, overviewResponse]) => {
        if (!runsResponse.ok || !resultsResponse.ok || !overviewResponse.ok) return;
        const [runsPayload, resultsPayload, overviewPayload] = await Promise.all([runsResponse.json(), resultsResponse.json(), overviewResponse.json()]);
        setShadowScoringRuns(Array.isArray(runsPayload.data) ? runsPayload.data : []);
        setRiskPredictions(Array.isArray(resultsPayload.data) ? resultsPayload.data : []);
        setPrediction(overviewPayload.data || null);
      }).catch(() => undefined);
    }, 3000);
    return () => window.clearInterval(timer);
  }, [shadowScoringRuns]);
  useEffect(() => {
    const latest = trainingMatrixBuilds[0];
    if (!latest || latest.status !== 'completed') {
      setMatrixReadinessReport(null);
      return;
    }
    void fetch(`/api/v1/prediction/history/training-matrices/${latest.id}/readiness`)
      .then(response => response.ok ? response.json() : null)
      .then(payload => setMatrixReadinessReport(payload?.data || null))
      .catch(() => setMatrixReadinessReport(null));
  }, [trainingMatrixBuilds]);
  useEffect(() => {
    const latest = baselineModelBuilds[0];
    if (!latest || latest.status !== 'completed') {
      setBaselineEvaluationReport(null);
      return;
    }
    void fetch(`/api/v1/prediction/history/baseline-models/${latest.id}/report`)
      .then(response => response.ok ? response.json() : null)
      .then(payload => setBaselineEvaluationReport(payload?.data || null))
      .catch(() => setBaselineEvaluationReport(null));
  }, [baselineModelBuilds]);
  useEffect(() => {
    if (!identityBackfills.some(run => run.status === 'queued' || run.status === 'running')) return;
    const timer = window.setInterval(() => {
      void (async () => {
        try {
          const [runsResponse, identitiesResponse, candidatesResponse] = await Promise.all([
            fetch('/api/v1/prediction/history/identity-backfills'),
            fetch('/api/v1/prediction/history/identities'),
            fetch('/api/v1/prediction/history/candidates'),
          ]);
          if (!runsResponse.ok || !identitiesResponse.ok || !candidatesResponse.ok) return;
          const [runsPayload, identitiesPayload, candidatesPayload] = await Promise.all([
            runsResponse.json(), identitiesResponse.json(), candidatesResponse.json(),
          ]);
          setIdentityBackfills(Array.isArray(runsPayload.data) ? runsPayload.data : []);
          setIdentityIntervals(Array.isArray(identitiesPayload.data) ? identitiesPayload.data : []);
          setIdentitySummary(identitiesPayload.summary || null);
          setHistoricalCandidates(Array.isArray(candidatesPayload.data) ? candidatesPayload.data : []);
          setHistoricalCandidateSummary(candidatesPayload.summary || null);
        } catch {
          // The primary page error state remains authoritative; retry on the next tick.
        }
      })();
    }, 3000);
    return () => window.clearInterval(timer);
  }, [identityBackfills]);
  const runHistoryAudit = async () => {
    setHistoryAuditRunning(true);
    try {
      const response = await fetch('/api/v1/prediction/history/audits', { method: 'POST' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const sourcesResponse = await fetch('/api/v1/prediction/history/sources');
      if (!sourcesResponse.ok) throw new Error(`HTTP ${sourcesResponse.status}`);
      const payload = await sourcesResponse.json();
      setHistorySources(Array.isArray(payload.data) ? payload.data : []);
      setPredictionError('');
    } catch (reason) {
      setPredictionError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setHistoryAuditRunning(false);
    }
  };
  const readinessPageCount = Math.max(1, Math.ceil(readiness.length / readinessPageSize));
  const safeReadinessPage = Math.min(readinessPage, readinessPageCount - 1);
  const visibleReadiness = readiness.slice(safeReadinessPage * readinessPageSize, (safeReadinessPage + 1) * readinessPageSize);
  const runIdentityBackfill = async () => {
    setIdentityBackfillRunning(true);
    try {
      const response = await fetch('/api/v1/prediction/history/identity-backfills', { method: 'POST' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const [runsResponse, identitiesResponse] = await Promise.all([
        fetch('/api/v1/prediction/history/identity-backfills'),
        fetch('/api/v1/prediction/history/identities'),
      ]);
      if (!runsResponse.ok || !identitiesResponse.ok) throw new Error(`HTTP ${runsResponse.status}/${identitiesResponse.status}`);
      const [runsPayload, identitiesPayload] = await Promise.all([runsResponse.json(), identitiesResponse.json()]);
      setIdentityBackfills(Array.isArray(runsPayload.data) ? runsPayload.data : []);
      setIdentityIntervals(Array.isArray(identitiesPayload.data) ? identitiesPayload.data : []);
      setIdentitySummary(identitiesPayload.summary || null);
      setPredictionError('');
    } catch (reason) {
      setPredictionError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setIdentityBackfillRunning(false);
    }
  };
  const buildDatasetManifest = async () => {
    setDatasetBuildRunning(true);
    try {
      const response = await fetch('/api/v1/prediction/history/datasets', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ source_key: 'current-prometheus' }),
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const buildsResponse = await fetch('/api/v1/prediction/history/datasets');
      if (!buildsResponse.ok) throw new Error(`HTTP ${buildsResponse.status}`);
      const payload = await buildsResponse.json();
      setDatasetBuilds(Array.isArray(payload.data) ? payload.data : []);
      setPredictionError('');
    } catch (reason) {
      setPredictionError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setDatasetBuildRunning(false);
    }
  };
  const buildHistoricalFeatures = async () => {
    if (!datasetBuilds[0]) return;
    setFeatureBuildStarting(true);
    try {
      const response = await fetch('/api/v1/prediction/history/feature-datasets', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ source_dataset_build_id: datasetBuilds[0].id }),
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const buildsResponse = await fetch('/api/v1/prediction/history/feature-datasets');
      if (!buildsResponse.ok) throw new Error(`HTTP ${buildsResponse.status}`);
      const payload = await buildsResponse.json();
      setFeatureBuilds(Array.isArray(payload.data) ? payload.data : []);
      setPredictionError('');
    } catch (reason) {
      setPredictionError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setFeatureBuildStarting(false);
    }
  };
  const buildTrainingPreparation = async () => {
    if (!featureBuilds[0]) return;
    setPreparationBuilding(true);
    try {
      const response = await fetch('/api/v1/prediction/history/training-preparations', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ source_feature_build_id: featureBuilds[0].id, minimum_coverage: 0.7, controls_per_positive: 3 }),
      });
      if (!response.ok) {
        const payload = await response.json().catch(() => ({}));
        throw new Error(payload.error || `HTTP ${response.status}`);
      }
      const buildsResponse = await fetch('/api/v1/prediction/history/training-preparations');
      if (!buildsResponse.ok) throw new Error(`HTTP ${buildsResponse.status}`);
      const payload = await buildsResponse.json();
      setPreparationBuilds(Array.isArray(payload.data) ? payload.data : []);
      setPredictionError('');
    } catch (reason) {
      setPredictionError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setPreparationBuilding(false);
    }
  };
  const buildControlFeatures = async () => {
    if (!preparationBuilds[0]) return;
    setControlFeatureStarting(true);
    try {
      const response = await fetch('/api/v1/prediction/history/control-feature-datasets', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ source_preparation_build_id: preparationBuilds[0].id }),
      });
      if (!response.ok) {
        const payload = await response.json().catch(() => ({}));
        throw new Error(payload.error || `HTTP ${response.status}`);
      }
      const buildsResponse = await fetch('/api/v1/prediction/history/control-feature-datasets');
      if (!buildsResponse.ok) throw new Error(`HTTP ${buildsResponse.status}`);
      const payload = await buildsResponse.json();
      setControlFeatureBuilds(Array.isArray(payload.data) ? payload.data : []);
      setPredictionError('');
    } catch (reason) {
      setPredictionError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setControlFeatureStarting(false);
    }
  };
  const buildTrainingMatrix = async () => {
    if (!controlFeatureBuilds[0]) return;
    setTrainingMatrixStarting(true);
    try {
      const response = await fetch('/api/v1/prediction/history/training-matrices', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ source_control_build_id: controlFeatureBuilds[0].id }),
      });
      if (!response.ok) {
        const payload = await response.json().catch(() => ({}));
        throw new Error(payload.error || `HTTP ${response.status}`);
      }
      const buildsResponse = await fetch('/api/v1/prediction/history/training-matrices');
      if (!buildsResponse.ok) throw new Error(`HTTP ${buildsResponse.status}`);
      const payload = await buildsResponse.json();
      setTrainingMatrixBuilds(Array.isArray(payload.data) ? payload.data : []);
      setPredictionError('');
    } catch (reason) {
      setPredictionError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setTrainingMatrixStarting(false);
    }
  };
  const trainBaselineModels = async () => {
    if (!trainingMatrixBuilds[0]) return;
    const readyScope = matrixReadinessReport?.strata.find(item => item.status === 'exploratory_ready');
    if (!readyScope) {
      setPredictionError(tx('没有通过数据充分性门的故障类型与 GPU 型号', 'No fault-type and GPU-model scope passed the data-sufficiency gate'));
      return;
    }
    setBaselineModelStarting(true);
    try {
      const response = await fetch('/api/v1/prediction/history/baseline-models', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ source_matrix_build_id: trainingMatrixBuilds[0].id, event_type: readyScope.event_type, model_name: readyScope.model_name }) });
      if (!response.ok) { const payload = await response.json().catch(() => ({})); throw new Error(payload.error || `HTTP ${response.status}`); }
      const buildsResponse = await fetch('/api/v1/prediction/history/baseline-models'); const payload = await buildsResponse.json(); setBaselineModelBuilds(Array.isArray(payload.data) ? payload.data : []); setPredictionError('');
    } catch (reason) { setPredictionError(reason instanceof Error ? reason.message : String(reason)); } finally { setBaselineModelStarting(false); }
  };
  const runFeatureReplay = async () => {
    const candidate = prediction?.models.find(model => model.status === 'shadow_candidate');
    if (!candidate) return;
    setFeatureReplayStarting(true);
    try {
      const response = await fetch('/api/v1/prediction/history/feature-replays', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ model_spec_id: candidate.id, sample_count: 12 }) });
      if (!response.ok) { const payload = await response.json().catch(() => ({})); throw new Error(payload.error || `HTTP ${response.status}`); }
      const runsResponse = await fetch('/api/v1/prediction/history/feature-replays');
      const payload = await runsResponse.json();
      setFeatureReplayRuns(Array.isArray(payload.data) ? payload.data : []);
      setPredictionError('');
    } catch (reason) { setPredictionError(reason instanceof Error ? reason.message : String(reason)); } finally { setFeatureReplayStarting(false); }
  };
  const runLiveCoverageAudit = async () => {
    const candidate = prediction?.models.find(model => model.status === 'shadow_candidate');
    if (!candidate) return;
    setLiveCoverageStarting(true);
    try {
      const response = await fetch('/api/v1/prediction/history/live-coverage', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ model_spec_id: candidate.id }) });
      if (!response.ok) { const payload = await response.json().catch(() => ({})); throw new Error(payload.error || `HTTP ${response.status}`); }
      const auditsResponse = await fetch('/api/v1/prediction/history/live-coverage');
      const payload = await auditsResponse.json();
      setLiveCoverageAudits(Array.isArray(payload.data) ? payload.data : []);
      setPredictionError('');
    } catch (reason) { setPredictionError(reason instanceof Error ? reason.message : String(reason)); } finally { setLiveCoverageStarting(false); }
  };
  const runShadowScoring = async () => {
    const candidate = prediction?.models.find(model => model.status === 'shadow_candidate');
    if (!candidate) return;
    setShadowScoringStarting(true);
    try {
      const response = await fetch('/api/v1/prediction/history/shadow-scoring', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ model_spec_id: candidate.id }) });
      if (!response.ok) { const payload = await response.json().catch(() => ({})); throw new Error(payload.error || `HTTP ${response.status}`); }
      const runsResponse = await fetch('/api/v1/prediction/history/shadow-scoring');
      const payload = await runsResponse.json();
      setShadowScoringRuns(Array.isArray(payload.data) ? payload.data : []);
      setPredictionError('');
    } catch (reason) { setPredictionError(reason instanceof Error ? reason.message : String(reason)); } finally { setShadowScoringStarting(false); }
  };
  const refreshHistoricalCandidates = async () => {
    const [runsResponse, candidatesResponse] = await Promise.all([fetch('/api/v1/prediction/history/backfills'), fetch('/api/v1/prediction/history/candidates')]);
    if (!runsResponse.ok || !candidatesResponse.ok) throw new Error(`HTTP ${runsResponse.status}/${candidatesResponse.status}`);
    const [runsPayload, candidatesPayload] = await Promise.all([runsResponse.json(), candidatesResponse.json()]);
    const runs = Array.isArray(runsPayload.data) ? runsPayload.data : [];
    setHistoryBackfills(runs);
    setHistoricalCandidates(Array.isArray(candidatesPayload.data) ? candidatesPayload.data : []);
    setHistoricalCandidateSummary(candidatesPayload.summary || null);
    setTrainingCohortPolicy(candidatesPayload.training_policy || null);
    return runs as HistoryBackfillRun[];
  };
  const runAlertBackfill = async () => {
    setHistoryBackfillRunning(true);
    try {
      const response = await fetch('/api/v1/prediction/history/backfills', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      for (let attempt = 0; attempt < 60; attempt += 1) {
        await new Promise(resolve => window.setTimeout(resolve, 1000));
        const runs = await refreshHistoricalCandidates();
        if (!runs.some(run => run.status === 'queued' || run.status === 'running')) break;
      }
      setPredictionError('');
    } catch (reason) {
      setPredictionError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setHistoryBackfillRunning(false);
    }
  };
  const reviewHistoricalCandidate = async (candidate: HistoricalFaultCandidate, status: 'accepted_proxy' | 'context_only' | 'needs_evidence' | 'excluded') => {
    const prompts: Record<string, string> = {
      accepted_proxy: tx('填写接受该代理样本的证据说明', 'Describe the evidence for accepting this proxy'),
      context_only: tx('填写仅作上下文、不作为正样本的原因', 'Explain why this should remain context-only'),
      needs_evidence: tx('填写还需要补充的证据（可留空）', 'Describe the missing evidence (optional)'),
      excluded: tx('填写排除原因', 'Describe why this candidate is excluded'),
    };
    const note = window.prompt(prompts[status], candidate.review_note || '');
    if (note === null) return;
    if (status !== 'needs_evidence' && !note.trim()) {
      setPredictionError(tx('该审核结果必须填写备注', 'A review note is required for this outcome'));
      return;
    }
    setReviewingCandidateID(candidate.id);
    try {
      const response = await fetch(`/api/v1/prediction/history/candidates/${candidate.id}`, {
        method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status, note: note.trim(), reviewed_by: 'web-operator' }),
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      await refreshHistoricalCandidates();
      setPredictionError('');
    } catch (reason) {
      setPredictionError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setReviewingCandidateID(null);
    }
  };
  const overridePredictionOutcome = async (outcome: PredictionOutcome, actualValue: 0 | 1) => {
    const reason = window.prompt(actualValue === 1 ? tx('填写确认发生硬件故障的证据', 'Describe the evidence confirming a hardware failure') : tx('填写确认未发生硬件故障的证据', 'Describe the evidence confirming normal operation'), outcome.human_reason || '');
    if (reason === null) return;
    if (!reason.trim()) {
      setPredictionError(tx('人工改判必须填写证据说明', 'A human override requires an evidence note'));
      return;
    }
    try {
      const response = await fetch(`/api/v1/prediction/outcomes/${outcome.id}`, {
        method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ actual_value: actualValue, reason: reason.trim(), decided_by: 'web-operator' }),
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const [accuracyResponse, outcomesResponse] = await Promise.all([fetch('/api/v1/prediction/accuracy'), fetch('/api/v1/prediction/outcomes')]);
      if (!accuracyResponse.ok || !outcomesResponse.ok) throw new Error(`HTTP ${accuracyResponse.status}/${outcomesResponse.status}`);
      const [accuracyPayload, outcomesPayload] = await Promise.all([accuracyResponse.json(), outcomesResponse.json()]);
      setPredictionAccuracy(accuracyPayload.data || null);
      setPredictionOutcomes(Array.isArray(outcomesPayload.data) ? outcomesPayload.data : []);
      setPredictionError('');
    } catch (reasonValue) {
      setPredictionError(reasonValue instanceof Error ? reasonValue.message : String(reasonValue));
    }
  };
  const layers = [
    ['L1', tx('确定性规则', 'Deterministic rules'), 'health_score', tx('运行中', 'ACTIVE')],
    ['L2', tx('无监督异常', 'Unsupervised anomaly'), 'anomaly_score', tx('待开发', 'PLANNED')],
    ['L3', tx('监督预测', 'Supervised prediction'), 'failure_probability', tx('框架就绪', 'FRAMEWORK')],
    ['L4', 'LLM + Skill', 'RCA / SOP', tx('待开发', 'PLANNED')],
  ];
  if (view === 'prediction') {
    const summary = prediction?.readiness;
    const latestBackfill = historyBackfills[0];
    const latestIdentityBackfill = identityBackfills[0];
    const latestDatasetBuild = datasetBuilds[0];
    const latestFeatureBuild = featureBuilds[0];
    const latestPreparationBuild = preparationBuilds[0];
    const latestControlFeatureBuild = controlFeatureBuilds[0];
    const latestTrainingMatrixBuild = trainingMatrixBuilds[0];
    const latestBaselineModelBuild = baselineModelBuilds[0];
    const shadowModelCandidates = (prediction?.models || []).filter(model => model.status === 'shadow_candidate');
    const featureParityByModel = new Map(featureParityAudits.map(audit => [audit.model_spec_id, audit]));
    const latestFeatureReplay = featureReplayRuns[0];
    const latestLiveCoverage = liveCoverageAudits[0];
    const latestShadowScoring = shadowScoringRuns[0];
    const highestRiskPredictions = [...riskPredictions].filter(row => row.probability !== undefined).sort((left, right) => (right.probability || 0) - (left.probability || 0)).slice(0, 20);
    const coverageParity = featureParityByModel.get(shadowModelCandidates[0]?.id);
    return <div className="grid prediction-page">
      <Card className="span-12 capability-note"><BrainCircuit size={19} /><div><CardHead code={prediction?.framework_version || 'PREDICTION FRAMEWORK'} title={tx('GPU 故障预测框架', 'GPU Failure Prediction Framework')} /><p>{shadowModelCandidates.length > 0 ? tx('首个通过完整性、跨时间稳定性和概率校准门的模型已进入影子候选注册表。当前仍不执行在线评分、不发送告警、不触发隔离、重启或任务操作；下一阶段先补齐在线特征等价性并记录前瞻验证结果。', 'The first model passing integrity, cross-time stability, and calibration gates is now registered as a shadow candidate. Online scoring, alerts, isolation, restarts, and workload actions remain disabled; the next stage is online feature parity followed by prospective validation records.') : tx('当前已完成预测契约、标签质量、数据就绪门和模型注册骨架。尚未注册合格概率模型，不输出伪概率，不触发隔离、重启或任务操作。', 'Prediction contracts, label quality, readiness gates, and the model registry foundation are available. No qualified probability model is registered, no fake probability is emitted, and no isolation, restart, or workload action is triggered.')}</p></div><Badge value={predictionError ? 'API ERROR' : prediction?.phase?.toUpperCase() || 'LOADING'} kind={predictionError ? 'warning' : 'info'} /></Card>
      {[[tx('当前 GPU', 'Current GPUs'), summary?.total ?? '—', `${summary?.blocked || 0} ${tx('被质量门阻断', 'blocked')}`], [tx('数据集就绪', 'Dataset Ready'), summary?.ready_for_dataset ?? '—', tx('仅代表可构建样本', 'eligible for dataset only')], [tx('确认硬件标签', 'Confirmed Labels'), prediction?.labels.confirmed ?? '—', tx('需人工确认和完整处置', 'requires complete human confirmation')], [tx('代理标签', 'Proxy Labels'), prediction ? prediction.labels.strong_proxy + prediction.labels.weak_proxy : '—', tx('规则事件，不等于确认故障', 'rule events, not confirmed failures')], [tx('在线历史保留', 'Online Retention'), prediction ? `${prediction.retention.online_retention_days}d` : '—', prediction?.retention.training_history_safe ? tx('满足训练历史保护线', 'training history protected') : tx('保留期不足', 'retention insufficient')], [tx('预测概率', 'Probabilities'), prediction?.probability_emitted ? tx('影子输出', 'SHADOW') : tx('未启用', 'DISABLED'), prediction?.probability_emitted ? tx('仅前瞻验证，不发告警', 'prospective validation only') : tx('等待首次影子批次', 'waiting for first shadow run')]].map(([label, value, note]) => <Card className="metric prediction-metric" key={String(label)}><BrainCircuit size={17} /><span>{label}</span><strong>{value}</strong><small>{note}</small></Card>)}
      {shadowModelCandidates.length > 0 ? <Card className="span-12"><CardHead code="SHADOW MODEL REGISTRY" title={tx('影子候选模型', 'Shadow Model Candidates')} action={<div className="table-actions"><Badge value="SCORING DISABLED" kind="info" /><button className="link" type="button" disabled={featureReplayStarting || latestFeatureReplay?.status === 'queued' || latestFeatureReplay?.status === 'running'} onClick={() => void runFeatureReplay()}>{featureReplayStarting ? tx('启动中…', 'Starting…') : tx('运行历史逐值回放', 'Run value replay')}</button></div>} /><p className="node-access-note">{tx('候选注册只确认模型产物可追溯且离线门禁通过，不等同于线上发布。第一层逐列契约审计通过后仍必须完成历史数值回放和实时 24 小时覆盖审计，才允许生成只读影子预测记录。', 'Candidate registration confirms artifact provenance and offline gates only; it is not an online release. Even after column-level contract parity passes, historical value replay and live 24-hour coverage audits are required before read-only shadow predictions may be generated.')}</p>{latestFeatureReplay ? <div className="issue-categories quality-ledger"><div><b>{tx('回放状态', 'Replay status')}</b><strong>{latestFeatureReplay.status.toUpperCase()}</strong><small>{latestFeatureReplay.version}</small></div><div><b>{tx('样本完成 / 失败', 'Samples complete / failed')}</b><strong>{latestFeatureReplay.completed_sample_count} / {latestFeatureReplay.failed_sample_count}</strong><small>{latestFeatureReplay.selected_sample_count} SELECTED</small></div><div><b>{tx('验证列 / 训练列', 'Verified / trained columns')}</b><strong>{latestFeatureReplay.verified_column_count} / {latestFeatureReplay.training_feature_count}</strong><small>{latestFeatureReplay.compared_value_count} VALUE COMPARISONS</small></div><div><b>{tx('不匹配 / 回放缺失', 'Mismatch / replay missing')}</b><strong>{latestFeatureReplay.mismatch_count} / {latestFeatureReplay.missing_replay_value_count}</strong><small>MAX ABS {latestFeatureReplay.maximum_absolute_error.toExponential(2)} · REL {latestFeatureReplay.maximum_relative_error.toExponential(2)}</small></div></div> : null}<div className="table-wrap"><table><thead><tr><th>{tx('故障 / 型号', 'Fault / Model')}</th><th>{tx('窗口', 'Horizon')}</th><th>{tx('状态', 'Status')}</th><th>{tx('特征等价性', 'Feature Parity')}</th><th>{tx('阈值', 'Threshold')}</th><th>{tx('模型来源', 'Model Provenance')}</th><th>{tx('注册门', 'Registry Gate')}</th></tr></thead><tbody>{shadowModelCandidates.map(model => { const parity = featureParityByModel.get(model.id); return <tr key={`${model.model_key}-${model.version}`}><td><b>{model.scope_event_type || model.task}</b><small>{model.scope_model_name || 'ALL MODELS'}</small></td><td>{model.horizon_minutes}m</td><td><Badge value={model.status.toUpperCase()} kind="healthy" /><small>{model.mode.toUpperCase()} · NO ACTION</small></td><td><Badge value={(parity?.status || 'pending').toUpperCase()} kind={parity?.status === 'passed' ? 'healthy' : 'warning'} /><small>{parity ? `${parity.contract_matched_count}/${parity.training_feature_count} COLUMNS · ${parity.replay_verified_count} REPLAYED` : 'AUDIT PENDING'}</small><small>{(parity?.blocking_reasons || []).join(' · ')}</small></td><td>{model.decision_threshold?.toFixed(3) || '—'}</td><td><b>BUILD #{model.source_baseline_build_id || '—'}</b><small>{model.version}</small><small>{model.artifact_sha256?.slice(0, 16) || 'SHA256 MISSING'}</small></td><td><code>{model.registry_gate_version || '—'}</code><small>{parity?.transformation_contract_version || model.feature_contract_version}</small></td></tr>; })}</tbody></table></div></Card> : null}
      {shadowModelCandidates.length > 0 ? <Card className="span-12">
        <CardHead code="LIVE 24H FEATURE COVERAGE" title={tx('实时特征覆盖审计', 'Live Feature Coverage Audit')} action={<div className="table-actions"><Badge value="READ-ONLY / NO SCORING" kind="info" /><button className="link" type="button" disabled={liveCoverageStarting || liveCoverageAudits.some(audit => audit.status === 'queued' || audit.status === 'running') || !coverageParity || coverageParity.replay_verified_count !== coverageParity.training_feature_count} onClick={() => void runLiveCoverageAudit()}>{liveCoverageStarting ? tx('启动中…', 'Starting…') : tx('审计当前 24 小时', 'Audit current 24h')}</button></div>} />
        <p className="node-access-note">{tx('按当前 H100 GPU 逐卡检查模型所需的 10 个源指标：每项至少覆盖 24 小时期望样本的 70%，且最新样本不超过 15 分钟。全体通过后仍只解锁下一步影子运行时建设。', 'Checks the 10 model source metrics for every current H100 GPU. Each metric needs at least 70% of the expected trailing-24h samples and a latest sample no older than 15 minutes. A pass only unlocks construction of the next read-only shadow runtime stage.')}</p>
        {latestLiveCoverage ? <div className="issue-categories quality-ledger"><div><b>{tx('审计状态', 'Audit status')}</b><strong>{latestLiveCoverage.status.toUpperCase()}</strong><small>{latestLiveCoverage.version}</small></div><div><b>{tx('可用 / 目标 GPU', 'Eligible / target GPUs')}</b><strong>{latestLiveCoverage.eligible_gpu_count} / {latestLiveCoverage.target_gpu_count}</strong><small>{(latestLiveCoverage.eligible_ratio * 100).toFixed(1)}% ELIGIBLE</small></div><div><b>{tx('指标通过 / 总数', 'Passing / total metrics')}</b><strong>{latestLiveCoverage.passing_metric_pair_count} / {latestLiveCoverage.metric_pair_count}</strong><small>{latestLiveCoverage.source_metric_count} METRICS PER GPU</small></div><div><b>{tx('缺失 / 稀疏 / 过期', 'Missing / sparse / stale')}</b><strong>{latestLiveCoverage.missing_metric_pair_count} / {latestLiveCoverage.sparse_metric_pair_count} / {latestLiveCoverage.stale_metric_pair_count}</strong><small>{latestLiveCoverage.error_message || (latestLiveCoverage.blocking_reasons || []).join(' · ') || latestLiveCoverage.report_sha256?.slice(0, 16) || 'AUDIT IN PROGRESS'}</small></div></div> : <Empty tx={tx} title={tx('历史逐值回放通过后可运行覆盖审计', 'Run coverage audit after value replay passes')} />}
      </Card> : null}
      {shadowModelCandidates.length > 0 ? <Card className="span-12">
        <CardHead code="READ-ONLY SHADOW SCORING" title={tx('影子预测运行时', 'Shadow Prediction Runtime')} action={<div className="table-actions"><Badge value="NO ALERT / NO ACTION" kind="info" /><button className="link" type="button" disabled={shadowScoringStarting || shadowScoringRuns.some(run => run.status === 'queued' || run.status === 'running') || coverageParity?.status !== 'shadow_runtime_required'} onClick={() => void runShadowScoring()}>{shadowScoringStarting ? tx('启动中…', 'Starting…') : tx('运行一次影子评分', 'Run shadow scoring')}</button></div>} />
        <p className="node-access-note">{tx('人工触发的只读前瞻评分：每批重新验证模型 SHA256、当前 24 小时数据覆盖和 70 列特征契约。超阈值只是待验证影子候选，不是硬件高危判定；超阈值比例、中位数或节点聚集异常会阻断周期调度。', 'Manually triggered read-only prospective scoring. Every run rechecks model SHA256, current trailing-24h coverage, and all 70 columns. Above-threshold means an unvalidated shadow candidate, not confirmed hardware risk; abnormal hit ratio, median, or node clustering blocks periodic scheduling.')}</p>
        {latestShadowScoring ? <div className="issue-categories quality-ledger"><div><b>{tx('批次 / 分布状态', 'Run / distribution status')}</b><strong>{latestShadowScoring.status.toUpperCase()}</strong><small>{(latestShadowScoring.distribution_status || 'LEGACY_UNAUDITED').toUpperCase()} · {latestShadowScoring.version}</small></div><div><b>{tx('超阈值候选', 'Above-threshold candidates')}</b><strong>{latestShadowScoring.positive_gpu_count} / {latestShadowScoring.scored_gpu_count}</strong><small>{(latestShadowScoring.positive_ratio * 100).toFixed(1)}% · NOT A HARDWARE ALERT</small></div><div><b>{tx('中位数 / P90 / P99', 'Median / P90 / P99')}</b><strong>{latestShadowScoring.median_probability === undefined ? '—' : `${(latestShadowScoring.median_probability * 100).toFixed(1)}% / ${((latestShadowScoring.p90_probability || 0) * 100).toFixed(1)}% / ${((latestShadowScoring.p99_probability || 0) * 100).toFixed(1)}%`}</strong><small>THRESHOLD {shadowModelCandidates[0]?.decision_threshold?.toFixed(3) || '—'} · P95 {latestShadowScoring.p95_probability === undefined ? '—' : `${(latestShadowScoring.p95_probability * 100).toFixed(1)}%`}</small></div><div><b>{tx('整节点超阈值 / 阻断', 'Whole-node hits / blockers')}</b><strong>{latestShadowScoring.all_above_threshold_nodes || 0}</strong><small>{latestShadowScoring.error_message || (latestShadowScoring.blocking_reasons || []).join(' · ') || latestShadowScoring.report_sha256?.slice(0, 16) || 'RUNNING'}</small></div></div> : <Empty tx={tx} title={tx('覆盖门通过，等待首次人工触发影子评分', 'Coverage gate passed; waiting for the first manual shadow run')} />}
        {highestRiskPredictions.length > 0 ? <div className="table-wrap"><table><thead><tr><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('影子概率', 'Shadow probability')}</th><th>{tx('风险级别', 'Risk level')}</th><th>{tx('模型 / 窗口', 'Model / horizon')}</th><th>{tx('特征指纹', 'Feature fingerprint')}</th></tr></thead><tbody>{highestRiskPredictions.map(row => <tr key={row.id}><td><b>{row.node_ip}</b><small>{row.gpu_uuid}</small></td><td><strong>{((row.probability || 0) * 100).toFixed(3)}%</strong></td><td><Badge value="UNVALIDATED" kind="info" /><small>{row.status.toUpperCase()}</small></td><td><b>{row.model_version}</b><small>{row.horizon_minutes}m</small></td><td><code>{row.feature_vector_sha256?.slice(0, 16) || '—'}</code><small>{row.transformation_contract_version}</small></td></tr>)}</tbody></table></div> : null}
      </Card> : null}
      {predictionModelGovernance ? <Card className="span-12">
        <CardHead code={predictionModelGovernance.version} title={tx('模型与数据治理卡', 'Model & Dataset Governance Cards')} action={<div className="table-actions"><Badge value={predictionModelGovernance.mode.toUpperCase()} kind="info" /><Badge value={`${predictionModelGovernance.models.length} MODELS`} kind="healthy" /></div>} />
        <p className="node-access-note">{tx('汇总当前训练数据、模型版本、影子门禁和 outcome 结果。该报告只读取 Atlas 已持久化元数据，不复制原始遥测；概率仍保持只读 shadow，不触发告警、调度或维修动作。', 'Summarizes current training data, model versions, shadow gates, and outcome evidence. This report reads persisted Atlas metadata only and does not copy raw telemetry. Probabilities remain read-only shadow signals and do not trigger alerts, scheduling, or repair actions.')}</p>
        <div className="issue-categories quality-ledger">
          <div><b>{tx('Dataset Card', 'Dataset Card')}</b><strong>{predictionModelGovernance.dataset.matrix_sample_count || predictionModelGovernance.dataset.window_count || 0}</strong><small>{predictionModelGovernance.dataset.matrix_positive_count || predictionModelGovernance.dataset.eligible_positive_count || 0} POS · {predictionModelGovernance.dataset.matrix_control_count || 0} CONTROL</small></div>
          <div><b>{tx('数据排除 / 缺口', 'Exclusions / Gaps')}</b><strong>{(predictionModelGovernance.dataset.excluded_count || 0) + (predictionModelGovernance.dataset.identity_missing_count || 0) + (predictionModelGovernance.dataset.low_coverage_count || 0)}</strong><small>ID {predictionModelGovernance.dataset.identity_missing_count || 0} · LOW COVERAGE {predictionModelGovernance.dataset.low_coverage_count || 0} · EXCLUDED {predictionModelGovernance.dataset.excluded_count || 0}</small></div>
          <div><b>{tx('Model Card', 'Model Card')}</b><strong>{predictionModelGovernance.models[0]?.algorithm || '—'}</strong><small>{predictionModelGovernance.models[0]?.baseline_version || predictionModelGovernance.models[0]?.model_version || 'NO MODEL'} · AUC {predictionModelGovernance.models[0]?.test_macro_roc_auc === undefined ? '—' : predictionModelGovernance.models[0].test_macro_roc_auc.toFixed(3)}</small></div>
          <div><b>{tx('Shadow Gates', 'Shadow Gates')}</b><strong>{predictionModelGovernance.shadow_gates.shadow_distribution_status || predictionModelGovernance.shadow_gates.shadow_run_status || '—'}</strong><small>PARITY {predictionModelGovernance.shadow_gates.feature_parity_status || '—'} · REPLAY {predictionModelGovernance.shadow_gates.replay_status || '—'} · COVERAGE {predictionModelGovernance.shadow_gates.live_coverage_status || '—'}</small></div>
        </div>
        <div className="prediction-contract">
          <span><b>{tx('特征矩阵', 'Feature matrix')}</b>{predictionModelGovernance.dataset.matrix_key || predictionModelGovernance.dataset.dataset_key || '—'}<small>{predictionModelGovernance.dataset.feature_column_count || 0} COLUMNS · {predictionModelGovernance.dataset.feature_contract_version || prediction?.feature_contract_version || '—'}</small></span>
          <span><b>{tx('切分样本', 'Split samples')}</b>{predictionModelGovernance.dataset.train_count || 0} / {predictionModelGovernance.dataset.validation_count || 0} / {predictionModelGovernance.dataset.test_count || 0}<small>TRAIN / VALIDATION / TEST</small></span>
          <span><b>{tx('影子覆盖', 'Shadow coverage')}</b>{predictionModelGovernance.shadow_gates.scored_gpu_count || 0} SCORED · {predictionModelGovernance.shadow_gates.positive_gpu_count || 0} ABOVE THRESHOLD<small>{predictionModelGovernance.shadow_gates.positive_ratio === undefined ? '—' : pct(predictionModelGovernance.shadow_gates.positive_ratio)} POSITIVE RATIO</small></span>
          <span><b>{tx('下一步', 'Next run')}</b>{predictionModelGovernance.recommended_next_run[0] || '—'}<small>{time(predictionModelGovernance.generated_at, lang)}</small></span>
        </div>
      </Card> : null}
      {heaRankChallenger ? <Card className="span-12">
        <CardHead code={heaRankChallenger.version} title={tx('HeaRank 离线 Challenger', 'HeaRank Offline Challenger')} action={<div className="table-actions"><Badge value={heaRankChallenger.status.toUpperCase()} kind={heaRankChallenger.status === 'ready_for_offline_comparison' ? 'healthy' : 'warning'} /><Badge value={`${heaRankChallenger.target_horizon_minutes / 1440}D NODE RISK`} kind="info" /></div>} />
        <p className="node-access-note">{tx('这是 HeaRank 思路的离线验证入口，不是已发布模型。当前只用成熟 shadow outcome 对比三种节点级排序信号：Logistic 概率、节点历史故障次数和阈值二值信号；没有足够 7 天成熟样本时保持阻断。', 'This is the offline validation entry for the HeaRank idea, not a released model. It compares three node-level ranking signals on mature shadow outcomes only: Logistic probability, node prior failure count, and threshold-binary score. It remains blocked until enough mature 7-day samples exist.')}</p>
        <div className="issue-categories quality-ledger">
          <div><b>{tx('成熟样本', 'Matured samples')}</b><strong>{heaRankChallenger.sample_summary.matured} / {heaRankChallenger.sample_summary.total}</strong><small>{heaRankChallenger.sample_summary.node_eligible} NODE-ELIGIBLE · {pct(heaRankChallenger.sample_summary.matured_ratio)}</small></div>
          <div><b>{tx('7 天策略数', '7d policies')}</b><strong>{heaRankChallenger.seven_day.length}</strong><small>{heaRankChallenger.seven_day[0]?.rows || 0} ROWS · {heaRankChallenger.seven_day[0]?.nodes || 0} NODES</small></div>
          <div><b>{tx('7 天正例', '7d positives')}</b><strong>{heaRankChallenger.seven_day[0]?.positives || 0}</strong><small>{heaRankChallenger.blocking_reasons.join(' · ') || 'READY FOR OFFLINE COMPARISON'}</small></div>
          <div><b>{tx('下一步', 'Next run')}</b><strong>{heaRankChallenger.status === 'ready_for_offline_comparison' ? 'COMPARE' : 'WAIT'}</strong><small>{heaRankChallenger.recommended_next_run[0] || '—'}</small></div>
        </div>
        <div className="prediction-ranking-grid">{heaRankChallenger.seven_day.map(row => <RankingTable key={row.policy} label={row.policy.toUpperCase()} rows={row.ranking_at_k} />)}</div>
      </Card> : null}
      <Card className="span-12">
        <CardHead code={predictionAccuracy?.rule_decision_version || 'PREDICTION OUTCOME'} title={tx('预测准确率闭环', 'Prediction Accuracy Loop')} action={<div className="table-actions"><Badge value={`${predictionAccuracy?.pending || 0} PENDING`} kind="info" /><Badge value={`${predictionAccuracy?.human_overrides || 0} HUMAN OVERRIDES`} kind="warning" /></div>} />
        <p className="node-access-note">{tx('预测窗口结束后由故障标签规则生成 TP / FP / FN / TN；无阈值、身份缺失的数据不进入准确率，正常结果还需经过 24 小时删失期。规则结果永久保留，人工可依据维修或换卡证据改判最终结果。', 'After each prediction horizon closes, failure-label rules derive TP / FP / FN / TN. Missing thresholds or identities are excluded, and negative outcomes require a 24-hour censoring period. Rule outcomes remain immutable evidence while operators may override the final outcome using repair or replacement evidence.')}</p>
        {predictionOutcomeReport ? <div className="prediction-contract">
          <span><b>{tx('报告版本 / 模式', 'Report version / mode')}</b>{predictionOutcomeReport.version} · {predictionOutcomeReport.mode.toUpperCase()}<small>{time(predictionOutcomeReport.generated_at, lang)}</small></span>
          <span><b>{tx('成熟样本', 'Matured samples')}</b>{predictionOutcomeReport.sample_maturity.matured} / {predictionOutcomeReport.sample_maturity.total} · {pct(predictionOutcomeReport.sample_maturity.matured_ratio)}<small>{predictionOutcomeReport.sample_maturity.node_eligible} NODE-ELIGIBLE · {predictionOutcomeReport.sample_maturity.probability_scored} SCORED</small></span>
          <span><b>{tx('安全边界', 'Safety envelope')}</b>{predictionOutcomeReport.safety.read_only_shadow && predictionOutcomeReport.safety.no_alert_emitted && predictionOutcomeReport.safety.no_action_taken ? 'READ-ONLY · NO ALERT · NO ACTION' : 'REVIEW REQUIRED'}<small>{predictionOutcomeReport.safety.probability_use}</small></span>
          <span><b>{tx('下一步', 'Next run')}</b>{predictionOutcomeReport.recommended_next_run[0] || '—'}<small>{(predictionOutcomeReport.recommended_next_run || []).slice(1).join(' · ')}</small></span>
        </div> : null}
        <div className="issue-categories quality-ledger">
          <div><b>{tx('规则评估样本', 'Rule evaluated')}</b><strong>{predictionAccuracy?.rule.evaluated || 0}</strong><small>TP {predictionAccuracy?.rule.tp || 0} · FP {predictionAccuracy?.rule.fp || 0} · FN {predictionAccuracy?.rule.fn || 0} · TN {predictionAccuracy?.rule.tn || 0}</small></div>
          <div><b>{tx('规则准确率', 'Rule accuracy')}</b><strong>{predictionAccuracy?.rule.accuracy === undefined ? '—' : `${(predictionAccuracy.rule.accuracy * 100).toFixed(1)}%`}</strong><small>PRECISION {predictionAccuracy?.rule.precision === undefined ? '—' : `${(predictionAccuracy.rule.precision * 100).toFixed(1)}%`} · RECALL {predictionAccuracy?.rule.recall === undefined ? '—' : `${(predictionAccuracy.rule.recall * 100).toFixed(1)}%`}</small></div>
          <div><b>{tx('人工最终准确率', 'Human-final accuracy')}</b><strong>{predictionAccuracy?.final.accuracy === undefined ? '—' : `${(predictionAccuracy.final.accuracy * 100).toFixed(1)}%`}</strong><small>{predictionAccuracy?.human_overrides || 0} OVERRIDES · {predictionAccuracy?.final.evaluated || 0} EVALUATED</small></div>
          <div><b>{tx('等待 / 隔离', 'Pending / Censored')}</b><strong>{predictionAccuracy ? `${predictionAccuracy.pending} / ${predictionAccuracy.censored}` : '—'}</strong><small>{tx('不污染准确率分母', 'EXCLUDED FROM METRIC DENOMINATORS')}</small></div>
        </div>
        {(predictionOutcomeReport?.baseline_comparisons || []).length > 0 ? <div className="table-wrap"><table className="prediction-label-table"><thead><tr><th>{tx('Naive baseline', 'Naive baseline')}</th><th>{tx('策略', 'Policy')}</th><th>{tx('规则准确率', 'Rule accuracy')}</th><th>{tx('规则召回', 'Rule recall')}</th><th>{tx('最终准确率', 'Final accuracy')}</th><th>{tx('最终召回', 'Final recall')}</th></tr></thead><tbody>{(predictionOutcomeReport?.baseline_comparisons || []).map(row => <tr key={row.name}><td><b>{row.name.toUpperCase()}</b><small>{row.final.evaluated} MATURED SCORED</small></td><td>{row.prediction_policy}</td><td>{pct(row.rule.accuracy)}</td><td>{pct(row.rule.recall)}</td><td>{pct(row.final.accuracy)}</td><td>{pct(row.final.recall)}</td></tr>)}</tbody></table></div> : null}
        <div className="prediction-ranking-grid">
          <RankingTable label={tx('规则 GPU 排序', 'Rule GPU Ranking')} rows={predictionAccuracy?.rule.ranking_at_k} />
          <RankingTable label={tx('规则节点排序', 'Rule Node Ranking')} rows={predictionAccuracy?.rule.node_ranking_at_k} />
          <RankingTable label={tx('人工最终 GPU 排序', 'Human-final GPU Ranking')} rows={predictionAccuracy?.final.ranking_at_k} />
          <RankingTable label={tx('人工最终节点排序', 'Human-final Node Ranking')} rows={predictionAccuracy?.final.node_ranking_at_k} />
        </div>
        <div className="table-wrap"><table className="prediction-label-table"><thead><tr><th>{tx('模型 / 窗口', 'Model / Horizon')}</th><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('预测', 'Prediction')}</th><th>{tx('规则结果', 'Rule Outcome')}</th><th>{tx('最终结果', 'Final Outcome')}</th><th>{tx('人工改判', 'Human Override')}</th></tr></thead><tbody>{predictionOutcomes.map(outcome => <tr key={outcome.id}><td><b>{outcome.model_key}</b><small>v{outcome.model_version} · {outcome.horizon_minutes}m</small></td><td><b>{outcome.node_ip || '—'}</b><small>{outcome.gpu_uuid || 'IDENTITY MISSING'}</small></td><td><Badge value={outcome.probability === undefined ? 'NOT SCORED' : `${(outcome.probability * 100).toFixed(1)}%`} kind={outcome.predicted_positive ? 'warning' : 'info'} /><small>THRESHOLD {outcome.decision_threshold === undefined ? '—' : outcome.decision_threshold}</small></td><td><Badge value={outcome.rule_outcome.toUpperCase()} kind={outcome.rule_outcome === 'fp' || outcome.rule_outcome === 'fn' ? 'danger' : outcome.rule_outcome === 'tp' || outcome.rule_outcome === 'tn' ? 'healthy' : 'info'} /><small>{outcome.maturity_reason || outcome.maturity_status}</small></td><td><Badge value={outcome.final_outcome.toUpperCase()} kind={outcome.final_outcome === 'fp' || outcome.final_outcome === 'fn' ? 'danger' : outcome.final_outcome === 'tp' || outcome.final_outcome === 'tn' ? 'healthy' : 'info'} /><small>{outcome.final_source.toUpperCase()}</small></td><td><div className="table-actions"><button className="link" type="button" disabled={outcome.maturity_status !== 'matured'} onClick={() => void overridePredictionOutcome(outcome, 1)}>{tx('确认故障', 'Fault')}</button><button className="link" type="button" disabled={outcome.maturity_status !== 'matured'} onClick={() => void overridePredictionOutcome(outcome, 0)}>{tx('确认正常', 'Normal')}</button></div>{outcome.human_decided_by ? <small>{outcome.human_decided_by} · {outcome.human_reason}</small> : null}</td></tr>)}</tbody></table>{predictionOutcomes.length === 0 && <Empty tx={tx} title={tx('尚无已发布模型的预测结果；准确率不会被伪造', 'No predictions from a released model; accuracy is not fabricated')} />}</div>
      </Card>
      <Card className="span-12">
        <CardHead code="HEALTHY CONTROL FEATURES" title={tx('健康对照监控特征', 'Healthy-control Telemetry Features')} action={<div className="table-actions"><Badge value="DEDUPLICATED PROMETHEUS READ" kind="healthy" /><button className="link" type="button" disabled={controlFeatureStarting || latestPreparationBuild?.status !== 'completed' || latestPreparationBuild?.version !== 'gpu-training-preparation-v5' || latestControlFeatureBuild?.status === 'queued' || latestControlFeatureBuild?.status === 'running'} onClick={() => void buildControlFeatures()}>{controlFeatureStarting ? tx('启动中…', 'Starting…') : tx('提取健康对照', 'Extract controls')}</button></div>} />
        <p className="node-access-note">{tx('按 GPU UUID 与对照截止时间去重读取 Prometheus/VM，复用 24 小时特征契约；核心温度、功耗和利用率遥测连续性必须达到 70%，并要求故障前正样本与健康对照处于相同负载档位。所有不合格对照均保留排除原因。', 'Deduplicate Prometheus/VM reads by GPU UUID and control cutoff while reusing the trailing-24h feature contract. Core temperature, power, and utilization continuity must reach 70%, and each pre-fault positive must share the same load bucket as its healthy control. Every rejected control retains an exclusion reason.')}</p>
        <div className="issue-categories quality-ledger">
          <div><b>{tx('请求 / 去重窗口', 'Requests / Unique windows')}</b><strong>{latestControlFeatureBuild ? `${latestControlFeatureBuild.request_count} / ${latestControlFeatureBuild.unique_window_count}` : '—'}</strong><small>PROCESSED {latestControlFeatureBuild?.processed_unique_windows || 0}</small></div>
          <div><b>{tx('合格健康对照', 'Eligible controls')}</b><strong>{latestControlFeatureBuild?.eligible_request_count || 0}</strong><small>{latestControlFeatureBuild?.completed_request_count || 0} COMPLETED REQUESTS</small></div>
          <div><b>{tx('遥测删失 / 不连续', 'Censored / Discontinuous')}</b><strong>{latestControlFeatureBuild ? `${latestControlFeatureBuild.telemetry_censored_count} / ${latestControlFeatureBuild.discontinuous_count}` : '—'}</strong><small>LOW COVERAGE {latestControlFeatureBuild?.low_coverage_count || 0} · FAILED {latestControlFeatureBuild?.extraction_failed_count || 0}</small></div>
          <div><b>{tx('负载未知 / 不匹配', 'Load unknown / Mismatch')}</b><strong>{latestControlFeatureBuild ? `${latestControlFeatureBuild.load_unknown_count} / ${latestControlFeatureBuild.load_mismatch_count}` : '—'}</strong><small>IDLE &lt;10 · MODERATE 10–60 · HIGH ≥60</small></div>
        </div>
        {latestControlFeatureBuild ? <div className="prediction-contract"><span><b>STATUS / VERSION</b>{latestControlFeatureBuild.status.toUpperCase()} · {latestControlFeatureBuild.version}</span><span><b>FEATURE CONTRACT</b>{latestControlFeatureBuild.feature_contract_version}</span><span><b>OUTPUT</b>{latestControlFeatureBuild.output_dir}</span><span><b>SHA256</b>{latestControlFeatureBuild.feature_sha256 || latestControlFeatureBuild.error_message || 'PENDING'}</span></div> : <Empty tx={tx} title={tx('等待从 v2 训练准备清单提取健康对照', 'Waiting to extract healthy controls from a v2 training preparation')} />}
      </Card>
      <Card className="span-12">
        <CardHead code="SUPERVISED TRAINING MATRIX" title={tx('监督训练矩阵', 'Supervised Training Matrix')} action={<div className="table-actions"><Badge value="LEAKAGE AUDITED" kind="healthy" /><button className="link" type="button" disabled={trainingMatrixStarting || latestControlFeatureBuild?.status !== 'completed' || latestPreparationBuild?.version !== 'gpu-training-preparation-v5' || latestControlFeatureBuild?.source_preparation_build_id !== latestPreparationBuild?.id || latestTrainingMatrixBuild?.status === 'queued' || latestTrainingMatrixBuild?.status === 'running'} onClick={() => void buildTrainingMatrix()}>{trainingMatrixStarting ? tx('启动中…', 'Starting…') : tx('构建训练矩阵', 'Build matrix')}</button></div>} />
        <p className="node-access-note">{tx('合并合格正样本与同卡健康对照，保留稀疏缺失值，并强制审计 GPU UUID 分区隔离、正样本时间截点、控制配对、重复行和特征契约。训练权重按分区和预测窗平衡类别，验证与测试指标保持无权重统计。', 'Merge eligible positives with same-GPU healthy controls while preserving sparse missing values. Enforce GPU UUID split isolation, positive point-in-time ordering, control pairing, duplicate, and feature-contract audits. Training weights balance classes per split and horizon; validation and test metrics remain unweighted.')}</p>
        <div className="issue-categories quality-ledger">
          <div><b>{tx('正样本 / 健康对照', 'Positives / Controls')}</b><strong>{latestTrainingMatrixBuild ? `${latestTrainingMatrixBuild.positive_count} / ${latestTrainingMatrixBuild.control_count}` : '—'}</strong><small>{latestTrainingMatrixBuild?.sample_count || 0} TOTAL ROWS</small></div>
          <div><b>TRAIN</b><strong>{latestTrainingMatrixBuild ? `${latestTrainingMatrixBuild.train_positive_count} / ${latestTrainingMatrixBuild.train_control_count}` : '—'}</strong><small>POSITIVE / CONTROL</small></div>
          <div><b>VALIDATION / TEST</b><strong>{latestTrainingMatrixBuild ? `${latestTrainingMatrixBuild.validation_positive_count + latestTrainingMatrixBuild.validation_control_count} / ${latestTrainingMatrixBuild.test_positive_count + latestTrainingMatrixBuild.test_control_count}` : '—'}</strong><small>ENTITY + TIME ISOLATED</small></div>
          <div><b>{tx('审计违规', 'Audit violations')}</b><strong>{latestTrainingMatrixBuild ? latestTrainingMatrixBuild.duplicate_count + latestTrainingMatrixBuild.entity_split_conflict_count + latestTrainingMatrixBuild.point_in_time_violation_count + latestTrainingMatrixBuild.pairing_violation_count + latestTrainingMatrixBuild.contract_violation_count : 0}</strong><small>{latestTrainingMatrixBuild?.feature_column_count || 0} FEATURE COLUMNS</small></div>
        </div>
        {latestTrainingMatrixBuild ? <div className="prediction-contract"><span><b>STATUS / VERSION</b>{latestTrainingMatrixBuild.status.toUpperCase()} · {latestTrainingMatrixBuild.version}</span><span><b>SOURCES</b>PREP #{latestTrainingMatrixBuild.source_preparation_build_id} · CONTROL #{latestTrainingMatrixBuild.source_control_build_id}</span><span><b>OUTPUT</b>{latestTrainingMatrixBuild.output_dir}</span><span><b>SHA256</b>{latestTrainingMatrixBuild.matrix_sha256 || latestTrainingMatrixBuild.error_message || 'PENDING'}</span></div> : <Empty tx={tx} title={tx('等待合格健康对照以构建监督训练矩阵', 'Waiting for eligible healthy controls to build a supervised matrix')} />}
        {matrixReadinessReport ? <><div className="issue-categories quality-ledger"><div><b>{tx('探索性就绪分层', 'Exploratory-ready strata')}</b><strong>{matrixReadinessReport.ready_strata}</strong><small>{matrixReadinessReport.insufficient_strata} INSUFFICIENT</small></div><div><b>{tx('质量门粒度', 'Quality-gate grain')}</b><strong>{matrixReadinessReport.strata.length}</strong><small>FAULT TYPE × MODEL × HORIZON</small></div></div><div className="table-wrap"><table><thead><tr><th>{tx('故障类型 / 型号', 'Fault type / Model')}</th><th>{tx('预测窗口', 'Horizon')}</th><th>TRAIN</th><th>VALIDATION</th><th>TEST</th><th>{tx('数据状态', 'Data status')}</th></tr></thead><tbody>{matrixReadinessReport.strata.map(item => <tr key={`${item.event_type}-${item.model_name}-${item.horizon_minutes}`}><td><b>{item.event_type}</b><small>{item.model_name}</small></td><td>{item.horizon_minutes}m</td>{['train', 'validation', 'test'].map(split => { const value = item.splits[split]; return <td key={split}>{value ? `${value.positive_count} / ${value.control_count}` : '—'}<small>{value ? `${value.positive_gpus} POSITIVE GPUS` : ''}</small></td>; })}<td><Badge value={item.status.toUpperCase()} kind={item.status === 'exploratory_ready' ? 'healthy' : 'warning'} /><small>{(item.blocking_reasons || []).join(' · ')}</small></td></tr>)}</tbody></table></div></> : null}
      </Card>
      <Card className="span-12">
        <CardHead code="SCOPED LOGISTIC BASELINE" title={tx('质量门类型专用基线', 'Readiness-gated Scoped Baseline')} action={<div className="table-actions"><Badge value="NO ONLINE PROBABILITY" kind="info" /><button className="link" type="button" disabled={baselineModelStarting || latestTrainingMatrixBuild?.status !== 'completed' || latestTrainingMatrixBuild?.version !== 'gpu-supervised-training-matrix-v4' || !matrixReadinessReport?.strata.some(item => item.status === 'exploratory_ready') || latestBaselineModelBuild?.status === 'queued' || latestBaselineModelBuild?.status === 'running'} onClick={() => void trainBaselineModels()}>{baselineModelStarting ? tx('训练中…', 'Training…') : tx('训练就绪分层', 'Train ready scope')}</button></div>} />
        <p className="node-access-note">{tx('只对通过数据充分性门的预测窗分别训练零外部依赖 Logistic Regression；采样数仅用于质量门，XID、ECC、Row Remap 和 Reset 等故障已发生指标同样禁止入模。逐列审计结果写入模型产物，禁用特征非零即阻断训练；验证集选阈值，测试集只做最终离线评估，不发布概率。', 'Train a dependency-free Logistic Regression separately only for horizons that pass data-sufficiency gates. Sample counts remain quality-only, while XID, ECC, row-remap, reset and other occurred-fault indicators are also prohibited. Column-level audits are embedded in every artifact and any prohibited selection blocks training; validation selects thresholds, test remains final offline evaluation, and no probability is released.')}</p>
        <div className="issue-categories quality-ledger"><div><b>{tx('模型 / 特征', 'Models / Features')}</b><strong>{latestBaselineModelBuild ? `${latestBaselineModelBuild.trained_model_count} / ${latestBaselineModelBuild.feature_column_count}` : '—'}</strong><small>HORIZONS / SAFE FEATURES</small></div><div><b>{tx('特征审计', 'Feature audit')}</b><strong>{latestBaselineModelBuild?.feature_audit_status?.toUpperCase() || 'PENDING'}</strong><small>{latestBaselineModelBuild?.excluded_feature_count || 0} EXCLUDED · {latestBaselineModelBuild?.prohibited_feature_count || 0} PROHIBITED SELECTED</small></div><div><b>{tx('统计稳定 / 影子候选', 'Stable / Shadow candidates')}</b><strong>{latestBaselineModelBuild ? `${latestBaselineModelBuild.statistically_stable_count || 0} / ${latestBaselineModelBuild.shadow_candidate_count || 0}` : '—'}</strong><small>STABILITY + CALIBRATION GATES</small></div><div><b>TEST ROC-AUC / PR-AUC</b><strong>{latestBaselineModelBuild?.status === 'completed' ? `${latestBaselineModelBuild.test_macro_roc_auc.toFixed(3)} / ${latestBaselineModelBuild.test_macro_pr_auc.toFixed(3)}` : '—'}</strong><small>MACRO ACROSS HORIZONS</small></div><div><b>TEST PRECISION / RECALL</b><strong>{latestBaselineModelBuild?.status === 'completed' ? `${latestBaselineModelBuild.test_macro_precision.toFixed(3)} / ${latestBaselineModelBuild.test_macro_recall.toFixed(3)}` : '—'}</strong><small>VALIDATION-SELECTED THRESHOLDS</small></div><div><b>{tx('训练 / 验证 / 测试', 'Train / Validation / Test')}</b><strong>{latestBaselineModelBuild ? `${latestBaselineModelBuild.train_count} / ${latestBaselineModelBuild.validation_count} / ${latestBaselineModelBuild.test_count}` : '—'}</strong><small>OFFLINE ONLY</small></div></div>
        {latestBaselineModelBuild ? <div className="prediction-contract"><span><b>STATUS / VERSION</b>{latestBaselineModelBuild.status.toUpperCase()} · {latestBaselineModelBuild.version}</span><span><b>SCOPE</b>{latestBaselineModelBuild.scope_event_type || 'GLOBAL'} · {latestBaselineModelBuild.scope_model_name || 'ALL MODELS'}</span><span><b>READINESS GATE</b>{latestBaselineModelBuild.readiness_gate_version || '—'}</span><span><b>ALGORITHM</b>{latestBaselineModelBuild.algorithm}</span><span><b>OUTPUT</b>{latestBaselineModelBuild.output_dir}</span><span><b>SHA256</b>{latestBaselineModelBuild.artifact_sha256 || latestBaselineModelBuild.error_message || 'PENDING'}</span></div> : <Empty tx={tx} title={tx('等待监督训练矩阵以训练首个离线基线', 'Waiting for a supervised matrix to train the first offline baseline')} />}
        {baselineEvaluationReport && baselineEvaluationReport.horizons?.length > 0 ? <div className="table-wrap"><table><thead><tr><th>{tx('故障类型', 'Fault type')}</th><th>{tx('预测窗口', 'Horizon')}</th><th>{tx('测试样本', 'Test samples')}</th><th>ROC-AUC / 95% CI</th><th>PR-AUC / 95% CI</th><th>{tx('跨时间稳定性', 'Cross-time stability')}</th><th>{tx('校准 / 发布门', 'Calibration / Release')}</th><th>{tx('精确率', 'Precision')}</th><th>{tx('召回率', 'Recall')}</th></tr></thead><tbody>{baselineEvaluationReport.horizons.flatMap(horizon => Object.entries(horizon.test_by_event_type || {}).map(([eventType, metrics]) => ({ eventType, horizon: horizon.horizon_minutes, metrics, validationUncertainty: horizon.validation_uncertainty, testUncertainty: horizon.test_uncertainty, crossSplitStatus: horizon.cross_split_status, rawCalibration: horizon.raw_test_calibration, calibration: horizon.test_calibration, releaseReadiness: horizon.release_readiness }))).sort((left, right) => left.eventType.localeCompare(right.eventType) || left.horizon - right.horizon).map(({ eventType, horizon, metrics, validationUncertainty, testUncertainty, crossSplitStatus, rawCalibration, calibration, releaseReadiness }) => <tr key={`${eventType}-${horizon}`}><td><b>{eventType}</b></td><td>{horizon}m</td><td>{metrics.count}<small>{metrics.positive} POS / {metrics.control} CTRL</small></td><td>{metrics.roc_auc.toFixed(3)}<small>{testUncertainty ? `${testUncertainty.roc_auc_lower.toFixed(3)}–${testUncertainty.roc_auc_upper.toFixed(3)}` : 'CI PENDING'}</small></td><td>{metrics.pr_auc.toFixed(3)}<small>{testUncertainty ? `${testUncertainty.pr_auc_lower.toFixed(3)}–${testUncertainty.pr_auc_upper.toFixed(3)} · NULL ${testUncertainty.null_pr_auc.toFixed(3)}` : 'CI PENDING'}</small></td><td><Badge value={(crossSplitStatus || testUncertainty?.status || 'not_audited').toUpperCase()} kind={crossSplitStatus === 'robust_candidate' ? 'healthy' : crossSplitStatus === 'temporal_instability' || crossSplitStatus === 'consistent_inverse' ? 'danger' : 'warning'} /><small>{validationUncertainty ? `VAL ROC ${validationUncertainty.roc_auc_lower.toFixed(3)}–${validationUncertainty.roc_auc_upper.toFixed(3)}` : 'VALIDATION CI PENDING'}</small></td><td><Badge value={(releaseReadiness || 'not_audited').toUpperCase()} kind={releaseReadiness === 'shadow_candidate' ? 'healthy' : 'warning'} /><small>{calibration ? `ECE ${rawCalibration?.ece.toFixed(3) ?? '—'}→${calibration.ece.toFixed(3)} · BSS ${rawCalibration?.brier_skill_score.toFixed(3) ?? '—'}→${calibration.brier_skill_score.toFixed(3)}` : 'CALIBRATION PENDING'}</small></td><td>{metrics.precision.toFixed(3)}</td><td>{metrics.recall.toFixed(3)}</td></tr>)}</tbody></table></div> : null}
      </Card>
      <Card className="span-12"><CardHead code="HISTORICAL MONITORING" title={tx('历史监控数据源', 'Historical Monitoring Sources')} action={<div className="table-actions"><Badge value="REMOTE NODE / READ-ONLY" kind="healthy" /><button className="link" type="button" disabled={historyAuditRunning} onClick={() => void runHistoryAudit()}>{historyAuditRunning ? tx('审计中…', 'Auditing…') : tx('重新审计', 'Run audit')}</button></div>} /><p className="node-access-note">{tx('Atlas 在部署节点本地读取 Prometheus/VM，只保存审计元数据和版本化训练数据集，不把多年原始监控数据复制到开发机。', 'Atlas reads Prometheus/VM locally on the deployment node and stores only audit metadata and versioned training datasets; multi-year raw telemetry is not copied to the development machine.')}</p><div className="table-wrap"><table className="prediction-model-table"><thead><tr><th>{tx('数据源', 'Source')}</th><th>{tx('版本 / 保留期', 'Version / Retention')}</th><th>{tx('GPU 覆盖', 'GPU Coverage')}</th><th>{tx('历史范围', 'History Range')}</th><th>{tx('指标族', 'Metric Families')}</th><th>{tx('采样', 'Cadence')}</th><th>{tx('状态', 'Status')}</th></tr></thead><tbody>{historySources.map(source => { const audit = source.latest_audit; return <tr key={source.id}><td><b>{source.name}</b><small>{source.type} · {source.base_url}</small><small>{source.execution} · {source.dataset_dir}</small></td><td><code>{audit?.source_version || '—'}</code><small>{audit?.configured_retention || '—'}</small></td><td><b>{audit?.current_gpu_series || '—'} GPU</b><small>DCGM {audit?.dcgm_target_count ?? '—'} · GPU exporter {audit?.gpu_exporter_target_count ?? '—'}</small></td><td><small>{audit?.earliest_sample_at ? time(audit.earliest_sample_at, lang) : '—'}</small><small>→ {audit?.latest_sample_at ? time(audit.latest_sample_at, lang) : '—'}</small></td><td><b>{audit?.metric_families?.length ?? '—'}</b><small>{audit ? `${audit.missing_metric_families?.length || 0} ${tx('项研究指标待补', 'research metrics missing')}` : tx('等待首次审计', 'awaiting first audit')}</small></td><td>{audit?.scrape_interval_seconds ? `${audit.scrape_interval_seconds.toFixed(1)}s` : '—'}</td><td><Badge value={(audit?.status || (source.enabled ? 'waiting' : 'disabled')).toUpperCase()} kind={audit?.status === 'success' ? 'healthy' : audit?.status === 'failed' ? 'danger' : 'warning'} />{audit?.error_message ? <small title={audit.error_message}>{audit.error_message}</small> : null}</td></tr>; })}</tbody></table>{historySources.length === 0 && <Empty tx={tx} title={tx('尚未配置历史监控数据源', 'No historical monitoring source configured')} />}</div></Card>
      <Card className="span-12">
        <CardHead code="HISTORICAL GPU IDENTITY" title={tx('历史 GPU 身份区间', 'Historical GPU Identity Intervals')} action={<div className="table-actions"><Badge value={`${identitySummary?.by_transition_type?.gpu_uuid_changed || 0} GPU CHANGES`} kind={(identitySummary?.by_transition_type?.gpu_uuid_changed || 0) > 0 ? 'warning' : 'neutral'} /><button className="link" type="button" disabled={identityBackfillRunning || latestIdentityBackfill?.status === 'running' || latestIdentityBackfill?.status === 'queued'} onClick={() => void runIdentityBackfill()}>{identityBackfillRunning ? tx('重建中…', 'Backfilling…') : tx('重建身份历史', 'Backfill identity')}</button></div>} />
        <p className="node-access-note">{tx('按 7 天分块、6 小时间隔读取 GPU UUID、节点身份、主机 SN 与 PCI Bus ID，压缩成身份区间；同槽位换 UUID 仅作为换卡候选证据，节点身份同时变化时标记为整机边界。', 'Read GPU UUID, node identity, host serial and PCI bus ID in seven-day chunks at six-hour intervals and compress them into identity intervals. A UUID change in the same slot is supporting replacement evidence only; simultaneous host identity changes mark a node boundary.')}</p>
        <div className="issue-categories quality-ledger">
          <div><b>{tx('身份区间', 'Identity intervals')}</b><strong>{identitySummary?.total || 0}</strong><small>COMPRESSED OBSERVATIONS</small></div>
          <div><b>{tx('换卡候选', 'GPU changes')}</b><strong>{identitySummary?.by_transition_type?.gpu_uuid_changed || 0}</strong><small>SAME NODE / PCI SLOT</small></div>
          <div><b>{tx('整机身份边界', 'Node boundaries')}</b><strong>{identitySummary?.by_transition_type?.node_identity_changed || 0}</strong><small>HOST ID / SERIAL CHANGED</small></div>
          <div><b>{tx('候选已关联', 'Candidates annotated')}</b><strong>{latestIdentityBackfill?.records_annotated || 0}</strong><small>{latestIdentityBackfill?.status?.toUpperCase() || 'NOT STARTED'} · {latestIdentityBackfill ? `${latestIdentityBackfill.chunks_completed}/${latestIdentityBackfill.chunks_total}` : '0/0'}</small></div>
        </div>
        <div className="table-wrap"><table className="prediction-label-table"><thead><tr><th>{tx('首次 / 最后观测', 'First / Last Seen')}</th><th>{tx('节点 / 槽位', 'Node / Slot')}</th><th>GPU UUID</th><th>{tx('身份变化', 'Identity Change')}</th><th>{tx('主机身份', 'Host Identity')}</th></tr></thead><tbody>{identityIntervals.map(interval => <tr key={interval.id}><td><small>{time(interval.first_seen_at, lang)}</small><small>→ {time(interval.last_seen_at, lang)}</small></td><td><b>{interval.node_ip}</b><small>GPU {interval.gpu_index} · {interval.pci_bus_id || 'PCI UNKNOWN'}</small></td><td><code>{interval.gpu_uuid}</code><small>{interval.model_name || '—'}</small></td><td><Badge value={(interval.transition_type || 'initial_observation').toUpperCase()} kind={interval.transition_type === 'gpu_uuid_changed' || interval.transition_type === 'node_identity_changed' ? 'warning' : 'info'} /><small>{interval.predecessor_uuid || interval.evidence_strength}</small></td><td><small>{interval.host_id || 'HOST ID UNKNOWN'}</small><small>{interval.host_serial || interval.hostname || '—'}</small></td></tr>)}</tbody></table>{identityIntervals.length === 0 && <Empty tx={tx} title={tx('尚未重建 GPU 身份历史', 'GPU identity history has not been rebuilt')} />}</div>
      </Card>
      <Card className="span-12">
        <CardHead code="HISTORICAL FAULT CANDIDATES" title={tx('历史 GPU 故障候选', 'Historical GPU Fault Candidates')} action={<div className="table-actions"><Badge value={`${historicalCandidateSummary?.pending_review || 0} PENDING REVIEW`} kind={(historicalCandidateSummary?.pending_review || 0) > 0 ? 'warning' : 'neutral'} /><button className="link" type="button" disabled={historyBackfillRunning || latestBackfill?.status === 'running' || latestBackfill?.status === 'queued'} onClick={() => void runAlertBackfill()}>{historyBackfillRunning ? tx('重建中…', 'Backfilling…') : tx('重建历史候选', 'Backfill candidates')}</button></div>} />
        <p className="node-access-note">{tx('历史候选先由版本化规则自动裁决为正代理、仅上下文或需人工复核；规则结论不会升级为确认故障。人工只处理不确定项、重要故障和抽样质检，也可以接受、降级或排除规则结果，所有改判均保留审计记录。', 'Versioned rules first classify historical candidates as positive proxies, context-only evidence or requiring human review. Rule decisions never become confirmed failures. Humans handle uncertainty, important incidents and sampled quality checks, and may accept, downgrade or exclude rule results with a durable audit trail.')}</p>
        <div className="issue-categories quality-ledger">
          <div><b>{tx('候选总数', 'Candidates')}</b><strong>{historicalCandidateSummary?.total || 0}</strong><small>REVIEWABLE EPISODES</small></div>
          <div><b>{tx('规则正代理', 'Rule positives')}</b><strong>{historicalCandidateSummary?.by_rule_decision?.positive_proxy || 0}</strong><small>VERSIONED RULE · OVERRIDABLE</small></div>
          <div><b>{tx('仅作上下文', 'Context only')}</b><strong>{historicalCandidateSummary?.by_training_disposition?.context_only || 0}</strong><small>LOW-PRIORITY XID</small></div>
          <div><b>{tx('回填进度', 'Backfill Progress')}</b><strong>{latestBackfill ? `${latestBackfill.chunks_completed}/${latestBackfill.chunks_total}` : '—'}</strong><small>{latestBackfill?.status?.toUpperCase() || 'NOT STARTED'} · {latestBackfill?.query_version || 'gpu-fault-signal-onset-v2'}</small></div>
        </div>
        <div className="table-wrap"><table className="prediction-label-table"><thead><tr><th>{tx('发生时间', 'Onset')}</th><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('事件', 'Event')}</th><th>{tx('处置 / 硬件', 'Operations / Hardware')}</th><th>{tx('规则裁决 / 人工覆核', 'Rule / Human Override')}</th><th>{tx('历史来源', 'Historical Source')}</th></tr></thead><tbody>{historicalCandidates.map(candidate => <tr key={candidate.id}><td>{time(candidate.onset_at, lang)}</td><td><b>{candidate.node_ip || candidate.hostname || '—'}</b><small>{candidate.gpu_uuid || candidate.pci_bus_id || 'NODE-LEVEL'}</small></td><td><code>{candidate.event_type}</code><small>{candidate.event_message || candidate.event_code}</small></td><td><Badge value={(candidate.operational_priority || 'unknown').toUpperCase()} kind={candidate.operational_priority === 'critical' ? 'danger' : candidate.operational_priority === 'high' ? 'warning' : 'info'} /><small>{candidate.hardware_certainty || 'unclassified'}</small></td><td><Badge value={(candidate.rule_decision || 'not_decided').toUpperCase()} kind={candidate.rule_decision === 'positive_proxy' ? 'warning' : candidate.rule_decision === 'needs_human_review' ? 'danger' : 'info'} /><small>RULE · {candidate.rule_decision_version || 'NOT RUN'}{candidate.rule_confidence !== undefined ? ` · ${(candidate.rule_confidence * 100).toFixed(0)}%` : ''}</small><small title={candidate.rule_decision_reason || ''}>HUMAN · {candidate.review_status.toUpperCase()}{candidate.reviewed_by ? ` · ${candidate.reviewed_by}` : ''}</small><small>{candidate.identity_evidence_status ? `IDENTITY · ${candidate.identity_evidence_status.toUpperCase()}` : 'IDENTITY · NOT SCANNED'}</small>{candidate.review_note ? <small title={candidate.review_note}>{candidate.review_note}</small> : null}<div className="table-actions"><button className="link" type="button" disabled={reviewingCandidateID === candidate.id} onClick={() => void reviewHistoricalCandidate(candidate, 'accepted_proxy')}>{tx('人工接受', 'Accept')}</button><button className="link" type="button" disabled={reviewingCandidateID === candidate.id} onClick={() => void reviewHistoricalCandidate(candidate, 'context_only')}>{tx('改为上下文', 'Context')}</button><button className="link" type="button" disabled={reviewingCandidateID === candidate.id} onClick={() => void reviewHistoricalCandidate(candidate, 'needs_evidence')}>{tx('需补证据', 'Evidence')}</button><button className="link" type="button" disabled={reviewingCandidateID === candidate.id} onClick={() => void reviewHistoricalCandidate(candidate, 'excluded')}>{tx('人工排除', 'Exclude')}</button></div></td><td><small>{candidate.source_metric} · {candidate.source_alert_name || 'DIRECT METRIC'}</small><small>{candidate.signal_samples} ONSET SAMPLES{candidate.recovery_aware ? ' · RECOVERY-AWARE' : ''}</small></td></tr>)}</tbody></table>{historicalCandidates.length === 0 && <Empty tx={tx} title={tx('尚未执行历史候选重建', 'Historical candidate backfill has not run')} />}</div>
      </Card>
      <Card className="span-12">
        <CardHead code="POINT-IN-TIME COHORT MANIFEST" title={tx('训练样本时间窗清单', 'Training Sample Window Manifest')} action={<button className="link" type="button" disabled={datasetBuildRunning || latestIdentityBackfill?.status !== 'completed' || latestIdentityBackfill?.query_version !== 'gpu-identity-interval-v3'} onClick={() => void buildDatasetManifest()}>{datasetBuildRunning ? tx('构建中…', 'Building…') : tx('构建版本化清单', 'Build manifest')}</button>} />
        <p className="node-access-note">{tx('将规则正代理或人工接受的候选合并为故障 episode，并生成严格早于故障时间的多预测窗口截止点；规则置信度随样本写入，后续人工改判可重新构建新版本。当前文件不包含特征值、不训练模型，也不会把代理标签升级为确认故障。', 'Merge rule-positive or human-accepted candidates into fault episodes and generate multi-horizon cutoffs strictly before failure time. Rule confidence is carried into each sample, and later human overrides produce a new dataset version. The artifact contains no feature values, trains no model and never upgrades proxy labels to confirmed faults.')}</p>
        <div className="issue-categories quality-ledger">
          <div><b>{tx('可提取候选', 'Extractable candidates')}</b><strong>{latestDatasetBuild?.eligible_candidate_count || 0}</strong><small>IDENTITY-SUPPORTED / ACCEPTED</small></div>
          <div><b>{tx('故障 Episodes', 'Fault episodes')}</b><strong>{latestDatasetBuild?.episode_count || 0}</strong><small>DEDUPLICATED LABEL EVENTS</small></div>
          <div><b>{tx('样本窗口', 'Sample windows')}</b><strong>{latestDatasetBuild?.window_count || 0}</strong><small>{latestDatasetBuild?.horizons?.join(' · ') || '1m → 7d'}</small></div>
          <div><b>{tx('身份缺失隔离', 'Identity missing')}</b><strong>{latestDatasetBuild?.identity_missing_count || 0}</strong><small>EXCLUDED FROM POSITIVES</small></div>
        </div>
        {latestDatasetBuild ? <div className="prediction-contract"><span><b>STATUS</b>{latestDatasetBuild.status.toUpperCase()}</span><span><b>VERSION</b>{latestDatasetBuild.version}</span><span><b>OUTPUT</b>{latestDatasetBuild.output_dir}</span><span><b>SHA256</b>{latestDatasetBuild.window_manifest_sha256 || '—'}</span></div> : <Empty tx={tx} title={tx('尚未构建训练样本时间窗清单', 'No training sample-window manifest has been built')} />}
      </Card>
      <Card className="span-12">
        <CardHead code="HISTORICAL FEATURE DATASET" title={tx('故障前监控特征提取', 'Pre-fault Monitoring Feature Extraction')} action={<div className="table-actions"><Badge value="DEPLOYMENT NODE / READ-ONLY" kind="healthy" /><button className="link" type="button" disabled={featureBuildStarting || latestDatasetBuild?.version !== 'gpu-fault-cohort-manifest-v2' || latestFeatureBuild?.status === 'queued' || latestFeatureBuild?.status === 'running'} onClick={() => void buildHistoricalFeatures()}>{featureBuildStarting ? tx('启动中…', 'Starting…') : tx('提取历史特征', 'Extract features')}</button></div>} />
        <p className="node-access-note">{tx('按 GPU episode 批量读取 Prometheus/VM 核心指标，在 Atlas 部署节点内按每个预测截止点计算过去 24 小时的末值、均值、极值、标准差、增量和趋势。所有样本时间必须早于 feature_cutoff_at；原始多年时序不会复制到开发机。', 'Batch core Prometheus/VM metrics by GPU episode, then calculate trailing 24-hour last, mean, extrema, standard deviation, delta, and trend features for every prediction cutoff on the Atlas deployment node. Every sample timestamp must precede feature_cutoff_at; multi-year raw time series are never copied to the development machine.')}</p>
        <div className="issue-categories quality-ledger">
          <div><b>{tx('任务状态', 'Build status')}</b><strong>{latestFeatureBuild?.status?.toUpperCase() || 'NOT STARTED'}</strong><small>{latestFeatureBuild?.version || 'gpu-historical-features-v2'}</small></div>
          <div><b>{tx('Episode 进度', 'Episode progress')}</b><strong>{latestFeatureBuild ? `${latestFeatureBuild.processed_episodes}/${latestFeatureBuild.episode_count}` : '—'}</strong><small>MAX CONCURRENCY · BATCH QUERY</small></div>
          <div><b>{tx('成功 / 失败窗口', 'Complete / Failed')}</b><strong>{latestFeatureBuild ? `${latestFeatureBuild.completed_windows} / ${latestFeatureBuild.failed_windows}` : '—'}</strong><small>{latestFeatureBuild?.window_count || 0} TOTAL WINDOWS</small></div>
          <div><b>{tx('平均指标覆盖率', 'Average metric coverage')}</b><strong>{latestFeatureBuild?.finished_at ? `${(latestFeatureBuild.average_metric_coverage * 100).toFixed(1)}%` : '—'}</strong><small>MIN {latestFeatureBuild?.finished_at ? `${(latestFeatureBuild.minimum_metric_coverage * 100).toFixed(1)}%` : '—'} · {latestFeatureBuild?.feature_column_count || 0} COLUMNS</small></div>
        </div>
        {latestFeatureBuild ? <div className="prediction-contract"><span><b>FEATURE CONTRACT</b>{latestFeatureBuild.feature_contract_version}</span><span><b>LOOKBACK / STEP</b>{latestFeatureBuild.lookback_minutes}m / {latestFeatureBuild.query_step_seconds}s</span><span><b>OUTPUT</b>{latestFeatureBuild.output_dir}</span><span><b>SHA256</b>{latestFeatureBuild.feature_sha256 || latestFeatureBuild.error_message || 'PENDING'}</span></div> : <Empty tx={tx} title={tx('等待从最新训练窗口清单提取特征', 'Waiting to extract features from the latest cohort manifest')} />}
      </Card>
      <Card className="span-12">
        <CardHead code="TRAINING PREPARATION" title={tx('训练质量门与防泄漏切分', 'Training Quality Gate & Leakage-safe Split')} action={<div className="table-actions"><Badge value="MIN COVERAGE 70%" kind="info" /><button className="link" type="button" disabled={preparationBuilding || latestFeatureBuild?.status !== 'completed' || latestFeatureBuild?.version !== 'gpu-historical-features-v2' || latestFeatureBuild?.episode_count !== latestDatasetBuild?.episode_count} onClick={() => void buildTrainingPreparation()}>{preparationBuilding ? tx('构建中…', 'Building…') : tx('按当前规则重建', 'Rebuild with current rules')}</button></div>} />
        <p className="node-access-note">{tx('零遥测窗口标记为 telemetry_censored，低覆盖率和提取失败样本从训练中排除且不以 0 填充；训练、验证和测试按时间顺序切分，并以 GPU UUID 严格隔离。系统同时生成同一 GPU 历史健康对照请求，后续仍需校验遥测连续性与负载可比性。', 'Zero-telemetry windows are marked telemetry_censored; low-coverage and failed extractions are excluded without zero imputation. Train, validation, and test follow time order with strict GPU UUID isolation. Same-GPU historical healthy-control requests are also generated, pending telemetry-continuity and workload comparability checks.')}</p>
        <div className="issue-categories quality-ledger">
          <div><b>{tx('合格正样本', 'Eligible positives')}</b><strong>{latestPreparationBuild?.eligible_positive_count || 0}</strong><small>{latestPreparationBuild?.source_window_count || 0} SOURCE WINDOWS</small></div>
          <div><b>{tx('训练 / 验证 / 测试', 'Train / Validation / Test')}</b><strong>{latestPreparationBuild ? `${latestPreparationBuild.train_count} / ${latestPreparationBuild.validation_count} / ${latestPreparationBuild.test_count}` : '—'}</strong><small>TIME ORDER · GPU UUID ISOLATED</small></div>
          <div><b>{tx('标签不合格 / 相关事件', 'Label ineligible / Correlated events')}</b><strong>{latestPreparationBuild ? `${latestPreparationBuild.label_ineligible_count || 0} / ${latestPreparationBuild.correlated_event_count || 0}` : '—'}</strong><small>TELEMETRY CENSORED {latestPreparationBuild?.telemetry_censored_count || 0} · ENTITY CONFLICT {latestPreparationBuild?.entity_time_conflict_count || 0}</small></div>
          <div><b>{tx('健康对照请求 / 缺口', 'Control requests / Shortfall')}</b><strong>{latestPreparationBuild ? `${latestPreparationBuild.control_request_count} / ${latestPreparationBuild.control_shortfall_count}` : '—'}</strong><small>SAME GPU · FAULT-CENSORED</small></div>
        </div>
        {latestPreparationBuild ? <div className="prediction-contract"><span><b>STATUS / VERSION</b>{latestPreparationBuild.status.toUpperCase()} · {latestPreparationBuild.version}</span><span><b>TIME BOUNDARIES</b>{latestPreparationBuild.train_end_at ? time(latestPreparationBuild.train_end_at, lang) : '—'} · {latestPreparationBuild.validation_end_at ? time(latestPreparationBuild.validation_end_at, lang) : '—'}</span><span><b>OUTPUT</b>{latestPreparationBuild.output_dir}</span><span><b>POSITIVE SHA256</b>{latestPreparationBuild.prepared_samples_sha256 || latestPreparationBuild.error_message || 'PENDING'}</span><span><b>CONTROL SHA256</b>{latestPreparationBuild.control_requests_sha256 || 'PENDING'}</span></div> : <Empty tx={tx} title={tx('等待对完整历史特征执行训练准备', 'Waiting to prepare the full historical feature dataset')} />}
      </Card>
      <Card className="span-12"><CardHead code={trainingCohortPolicy?.version || 'GPU TRAINING COHORT'} title={tx('故障前窗口与正常对照规则', 'Pre-fault Windows & Healthy Controls')} /><div className="prediction-contract"><span><b>{tx('预测窗口', 'Positive horizons')}</b>{(trainingCohortPolicy?.positive_horizons_minutes || []).map(value => value >= 1440 ? `${value / 1440}d` : value >= 60 ? `${value / 60}h` : `${value}m`).join(' · ') || '—'}</span><span><b>{tx('匹配正常对照', 'Matched healthy controls')}</b>{(trainingCohortPolicy?.control_match_dimensions || []).join(' · ') || '—'}</span><span><b>{tx('正常区间', 'Normal ranges')}</b>{(trainingCohortPolicy?.normal_range_statistics || []).join(' · ') || '—'}</span><span><b>{tx('排除污染窗口', 'Censor contaminated windows')}</b>{trainingCohortPolicy ? `${trainingCohortPolicy.healthy_censor_before_hours}h BEFORE · ${trainingCohortPolicy.healthy_censor_after_hours}h AFTER · telemetry/restart/maintenance/identity change excluded` : '—'}</span><span><b>{tx('换卡证据边界', 'Replacement evidence boundary')}</b>{trainingCohortPolicy?.replacement_evidence_policy || '—'}</span></div></Card>
      <Card className="span-12"><CardHead code="MODEL REGISTRY" title={tx('预测时间窗与模型契约', 'Prediction Horizons & Model Contracts')} action={<Badge value={`${prediction?.models.length || 0} SPECS`} kind="info" />} /><div className="table-wrap"><table className="prediction-model-table"><thead><tr><th>MODEL KEY</th><th>{tx('对象', 'Entity')}</th><th>{tx('时间窗', 'Horizon')}</th><th>{tx('算法 / 运行时', 'Algorithm / Runtime')}</th><th>{tx('状态', 'Status')}</th><th>{tx('契约', 'Contracts')}</th></tr></thead><tbody>{(prediction?.models || []).map(model => <tr key={model.model_key}><td><b>{model.model_key}</b><small>v{model.version}</small></td><td>{model.hardware_class} / {model.entity_type}</td><td><b>{model.horizon_minutes >= 1440 ? `${model.horizon_minutes / 1440}d` : model.horizon_minutes >= 60 ? `${model.horizon_minutes / 60}h` : `${model.horizon_minutes}m`}</b></td><td><code>{model.algorithm} / {model.runtime}</code></td><td><Badge value={model.status.toUpperCase()} kind="info" /></td><td><small>{model.feature_contract_version}</small><small>{model.label_contract_version}</small></td></tr>)}</tbody></table></div></Card>
      <Card className="span-12"><CardHead code="POINT-IN-TIME READINESS" title={tx('GPU 数据就绪队列', 'GPU Data Readiness Queue')} action={<div className="table-actions"><Badge value={`${readiness.length} GPUS`} kind="info" /><button disabled={safeReadinessPage === 0} onClick={() => setReadinessPage(page => Math.max(0, page - 1))} aria-label={tx('上一页', 'Previous page')}><ChevronLeft size={15} /></button><span>{safeReadinessPage + 1} / {readinessPageCount}</span><button disabled={safeReadinessPage + 1 >= readinessPageCount} onClick={() => setReadinessPage(page => Math.min(readinessPageCount - 1, page + 1))} aria-label={tx('下一页', 'Next page')}><ChevronRight size={15} /></button></div>} /><div className="table-wrap"><table className="prediction-readiness-table"><thead><tr><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('型号', 'Model')}</th><th>{tx('状态', 'Status')}</th><th>{tx('数据置信度', 'Data Confidence')}</th><th>{tx('特征覆盖', 'Feature Coverage')}</th><th>{tx('阻断原因', 'Blocking Reasons')}</th><th>{tx('观测时间', 'Observed')}</th></tr></thead><tbody>{visibleReadiness.map(item => <tr key={`${item.gpu_asset_id}-${item.feature_snapshot_id}`}><td><b>{item.node_ip} · GPU {item.gpu_index}</b><small>{item.gpu_uuid || item.entity_key}</small></td><td>{item.model_name || '—'}</td><td><Badge value={item.status.toUpperCase()} kind={item.status === 'ready_for_dataset' ? 'healthy' : 'warning'} /></td><td><Badge value={item.data_confidence || 'UNKNOWN'} kind={item.data_confidence === 'A' ? 'healthy' : item.data_confidence === 'B' ? 'info' : 'warning'} /></td><td>{Math.round(item.feature_coverage * 100)}%</td><td><small>{item.blocking_reasons.length ? item.blocking_reasons.join(' · ') : tx('无；仅数据集就绪', 'none; dataset-ready only')}</small></td><td>{time(item.observed_at, lang)}</td></tr>)}</tbody></table>{readiness.length === 0 && <Empty tx={tx} title={predictionError ? tx('预测框架 API 不可用', 'Prediction API unavailable') : tx('等待 GPU 特征快照', 'Waiting for GPU feature snapshots')} />}</div></Card>
      <Card className="span-12"><CardHead code="LABEL LEDGER" title={tx('GPU 故障标签台账', 'GPU Failure Label Ledger')} action={<div className="table-actions"><Badge value={`${prediction?.labels.confirmed || 0} CONFIRMED`} kind={(prediction?.labels.confirmed || 0) > 0 ? 'healthy' : 'warning'} /><Badge value={`${(prediction?.labels.strong_proxy || 0) + (prediction?.labels.weak_proxy || 0)} PROXY`} kind="info" /></div>} /><div className="table-wrap"><table className="prediction-label-table"><thead><tr><th>LABEL</th><th>{tx('节点 / GPU', 'Node / GPU')}</th><th>{tx('型号', 'Model')}</th><th>{tx('事件类型', 'Event Type')}</th><th>{tx('质量层级', 'Quality Tier')}</th><th>{tx('来源', 'Provenance')}</th><th>{tx('发生时间', 'Occurred')}</th></tr></thead><tbody>{failureLabels.map(label => <tr key={label.id}><td><code>{label.label_key}</code></td><td><b>{label.node_ip || '—'}</b><small>{label.gpu_uuid || label.entity_key}</small></td><td>{label.model_name || '—'}</td><td><code>{label.event_type}</code><small>{label.rule_version || '—'}</small></td><td><Badge value={label.quality_tier.toUpperCase()} kind={label.quality_tier === 'confirmed' ? 'healthy' : label.quality_tier === 'strong_proxy' ? 'warning' : 'info'} /></td><td><small>{label.source_type} #{label.source_record_id}</small>{label.confirmation_resolution_id ? <small>resolution #{label.confirmation_resolution_id}</small> : null}</td><td>{time(label.occurred_at, lang)}</td></tr>)}</tbody></table>{failureLabels.length === 0 && <Empty tx={tx} title={tx('等待首次标签同步', 'Waiting for first label synchronization')} />}</div></Card>
      <Card className="span-6"><CardHead code="LABEL CONTRACT" title={tx('标签质量分层', 'Label Quality Tiers')} /><div className="prediction-contract"><span><b>{tx('确认正例', 'Confirmed positives')}</b>{(prediction?.label_policy.confirmed_positive || []).join(' · ') || '—'}</span><span><b>{tx('弱标签', 'Weak labels')}</b>{(prediction?.label_policy.weak_positive || []).join(' · ') || '—'}</span><span><b>{tx('禁止作为正例', 'Excluded positives')}</b>{(prediction?.label_policy.excluded_as_positive || []).join(' · ') || '—'}</span></div></Card>
      <Card className="span-6"><CardHead code="RELEASE GATES" title={tx('模型上线门槛', 'Model Release Gates')} /><div className="prediction-contract"><span><b>POINT-IN-TIME</b>{prediction?.label_policy.point_in_time_rule || '—'}</span><span><b>SPLIT</b>{prediction?.label_policy.entity_isolation || '—'}</span><span><b>SHADOW / NO AUTO ACTION</b>{prediction ? `precision ≥ ${prediction.release_gates.minimum_precision} · recall ≥ ${prediction.release_gates.minimum_recall} · calibration required` : '—'}</span></div></Card>
    </div>;
  }
  if (view === 'algorithms') return <div className="grid"><Card className="span-6"><CardHead code="CANDIDATES" title={tx('候选算法', 'Candidate Algorithms')} /><div className="chips">{['Logistic Regression', 'LightGBM', 'XGBoost', 'ECOD', 'Isolation Forest', 'Survival Analysis'].map(x => <span key={x}>{x}</span>)}</div></Card><Card className="span-6"><CardHead code="GUARDRAILS" title={tx('上线约束', 'Release Gates')} /><div className="rules">{[tx('特征必须早于标签截点，防止未来信息泄漏', 'Features must precede the label cutoff to prevent future leakage'), tx('按时间与 GPU UUID 隔离训练、校准和测试', 'Split train, calibration, and test by time and GPU UUID'), tx('概率必须按时间窗分别校准', 'Calibrate probability independently for each horizon'), tx('未达门槛仅 shadow mode', 'Shadow mode until release gates pass'), tx('LLM 不修改模型概率', 'LLM cannot alter model probability')].map(x => <span key={x}><ShieldCheck size={15} />{x}</span>)}</div></Card></div>;
  return <div className="grid"><Card className="span-12"><CardHead code="DECISION STACK" title={tx('决策分层', 'Decision Stack')} /><div className="model-grid">{layers.map(x => <div key={x[0]}><small>{x[0]}</small><b>{x[1]}</b><code>{x[2]}</code><Badge value={x[3]} kind={x[0] === 'L1' ? 'healthy' : x[0] === 'L3' ? 'info' : 'neutral'} /></div>)}</div></Card></div>;
}
function About({ tx, view, platformConfig, onPlatformConfig }: { tx: Tx; view: string; platformConfig: PlatformConfig; onPlatformConfig: (config: PlatformConfig) => void }) {
  const [moduleDetailID, setModuleDetailID] = useState<string | null>(null);
  if (view === 'settings') return <PlatformSettings tx={tx} value={platformConfig} onSaved={onPlatformConfig} />;
  if (view === 'architecture') return <div className="grid">
    <Card className="span-12 architecture-map-card"><CardHead code="SYSTEM MAP" title={tx('平台模块架构', 'Platform Module Architecture')} action={<Badge value="MODULE RELATIONSHIPS" kind="info" />} /><img src="/atlas-platform-architecture.svg?v=20260721.1" alt={tx('ATLAS 平台模块架构图', 'ATLAS platform module architecture')} /></Card>
    <Card className="span-7"><CardHead code="ARCHITECTURE" title={tx('平台架构', 'Platform Architecture')} /><div className="architecture">{[['01', tx('采集', 'INGEST'), 'DCGM · Prometheus · logs · BMC'], ['02', tx('治理', 'NORMALIZE'), 'identity · event · feature'], ['03', tx('决策', 'DECIDE'), 'rules · PyOD · supervised'], ['04', tx('闭环', 'WORKFLOW'), 'alert · repair · validation']].map(x => <div key={x[0]}><small>{x[0]}</small><b>{x[1]}</b><code>{x[2]}</code></div>)}</div></Card>
    <Card className="span-5"><CardHead code="BOUNDARIES" title={tx('工程边界', 'Engineering Boundaries')} /><div className="rules danger-list">{[tx('无节点登录权限时仍可使用监控数据模式', 'Monitoring-only mode does not require node access'), tx('节点信息收集只能通过版本化 Skill 和注册命令', 'Node evidence collection requires a versioned Skill and registered commands'), tx('平台发现数据问题，不自动修复监控配置', 'Detect data issues; do not auto-remediate monitoring'), tx('未确认维护窗口时禁止主动压测', 'No active stress before maintenance confirmation'), tx('LLM 不直接评分', 'LLM does not score hardware'), tx('异常分不等于故障概率', 'Anomaly score is not failure probability'), tx('缺失数据输出 unknown', 'Missing data returns unknown'), tx('重启、隔离、复测需审批', 'Restart, isolation and validation require approval')].map(x => <span key={x}><AlertTriangle size={15} />{x}</span>)}</div></Card>
    <Card className="span-12"><CardHead code="VERSIONING" title={tx('模块化版本治理', 'Modular Version Governance')} /><div className="version-policy"><span><b>MAJOR</b>{tx('模块契约或核心语义不兼容变更', 'Incompatible module contract or semantics')}</span><span><b>MINOR</b>{tx('新增向后兼容能力', 'Backward-compatible capability')}</span><span><b>PATCH</b>{tx('规则、阈值、缺陷与展示修正', 'Rules, thresholds, fixes and presentation')}</span></div></Card>
  </div>;

  const baseModules = [
    { id: 'asset', name: tx('资产对账', 'Asset Reconciliation'), version: 'v0.4.0', status: tx('节点身份代次', 'NODE IDENTITY GENERATIONS'), desc: tx('以 LXOP 实时在用资产为监控边界；同 IP 的 SN 变化识别为节点换代，旧 GPU、评分和问题历史保留，新节点建立独立身份代次。', 'Live LXOP assets define monitoring scope. A serial-number change at the same IP creates a new node identity generation while preserving the old GPUs, scores, and issue history.'), history: [tx('v0.4.0 · 同 IP 新 SN 建立新身份代次；下架仅退出当前检查，不再删除历史；GPU 双接口差值独立统计', 'v0.4.0 · a new serial at the same IP creates a new identity generation; retirement leaves current checks without deleting history; cross-source differences are GPU-only'), tx('v0.3.1 · 节点列表默认只返回当前资产，退休节点仅通过显式 retired 审计筛选查询', 'v0.3.1 · node lists default to current assets; retired nodes require the explicit retired audit filter'), tx('v0.3.0 · 在线状态白名单成为统一监控边界，10 分钟内退休非在用或已移除节点并失效当前评分', 'v0.3.0 · in-use state allowlist became the monitoring boundary; non-in-use or removed nodes retire and current scores invalidate within 10 minutes')] },
    { id: 'quality', name: tx('监控数据质量发现', 'Monitoring Data Quality Detection'), version: 'v0.8.2', status: tx('采集覆盖间距校准', 'COVERAGE SPACING TUNED'), desc: tx('联合识别每卡连续性和采集链路问题，并在独立问题统计子页面按历史、已解决、遗留和当前检测统一展示。', 'Detect per-GPU continuity and collection-path issues, with unified discovered, resolved, remaining and active counts on a dedicated Issue Statistics subpage.'), history: [tx('v0.8.2 · 采集覆盖状态徽标与数值间距增加约 1.4 倍，卡片高度不变', 'v0.8.2 · increased coverage badge-to-value spacing by about 1.4× without changing card height'), tx('v0.8.1 · 压缩采集覆盖和指标连续性数据卡高度与留白', 'v0.8.1 · reduced target-coverage and metric-continuity card height and whitespace'), tx('v0.8.0 · 数据质量台账迁入独立问题统计子页，问题列表跳转保持筛选与分页一致', 'v0.8.0 · moved the quality ledger to a dedicated subpage with consistent issue-list filtering and pagination'), tx('v0.7.0 · 连续性问题接入系统数据质量分类统计和侧边栏遗留数', 'v0.7.0 · continuity findings joined System Data Quality category statistics and sidebar remaining count'), tx('v0.6.1 · Target 相对耗时改为审计项，不再单独产生问题', 'v0.6.1 · relative target duration became audit-only and no longer creates findings alone'), tx('v0.6.0 · 新增最大间隔、UUID 波动和 Target 抓取质量判定', 'v0.6.0 · maximum gap, UUID flaps and target scrape-quality decisions'), tx('v0.5.0 · 新增每卡 1h 样本存在率、当前样本年龄、连续性页面与问题闭环', 'v0.5.0 · per-GPU 1h presence, current sample age, continuity UI and issue lifecycle')] },
    { id: 'health', name: tx('硬件健康评分', 'Hardware Health Scoring'), version: 'v1.5.0', status: tx('新增故障与历史累计分离', 'NEW FAULT / HISTORY SPLIT'), desc: tx('不可恢复重映射行累计值仅作为历史观察证据，不扣分、不生成故障；只有一小时或二十四小时内新增时才触发严重硬件事件。', 'Lifetime uncorrectable remapped-row totals are observation-only and do not affect health or create faults; only newly observed one-hour or twenty-four-hour growth triggers a critical hardware event.'), history: [tx('v1.5.0 · 修正累计不可恢复重映射行持续误报，旧规则标签自动排除出训练集', 'v1.5.0 · fixed persistent alerts from lifetime uncorrectable row totals and excludes legacy-rule labels from training'), tx('v1.4.1 · 相对抓取耗时退出异常判定，保留审计观测', 'v1.4.1 · relative scrape duration left anomaly decisions and remains audit evidence'), tx('v1.4.0 · 接入最大间隔、UUID 波动和 DCGM Target 抓取质量', 'v1.4.0 · maximum gap, UUID flaps and DCGM target scrape quality'), tx('v1.3.0 · 健康快照接入每卡结构性可观测性特征', 'v1.3.0 · health snapshots consume per-GPU structural observability features'), tx('v1.2.0 · Correctable Row Remap 改为 1h/24h 增长判定，稳定累计值只观察', 'v1.2.0 · correctable row remaps use 1h/24h growth; stable totals are observation-only')] },
    { id: 'detection', name: tx('故障检测', 'Fault Detection'), version: 'v0.1.1', status: tx('规则与报告联动', 'RULE + REPORT LINKAGE'), desc: tx('识别已经发生或正在发生的 ECC、XID、掉卡、不可用、温度和 PCIe 等硬件故障，并将稳定事件身份交给只读证据与故障报告模块。', 'Detect ECC, XID, dropout, unavailable, thermal and PCIe hardware faults that have occurred or are occurring, then pass stable event identity to the read-only evidence and fault-report module.'), history: [tx('v0.1.1 · 硬件事件接入结构化证据与报告入口', 'v0.1.1 · hardware events linked to structured evidence and reports'), tx('v0.1.0 · Row Remap、温度、PCIe 与 XID 变化确定性规则', 'v0.1.0 · deterministic row-remap, thermal, PCIe and XID-change rules')] },
    { id: 'analysis', name: tx('只读证据与故障报告', 'Read-only Evidence & Fault Reports'), version: 'v0.1.0', status: tx('确定性报告基线', 'DETERMINISTIC REPORT BASELINE'), desc: tx('按事件聚合健康快照、Feature Catalog、规则命中、问题台账和人工处置，生成双语结构化故障报告。v0.1 不执行节点命令、不调用 LLM、不修改任务、节点或配置。', 'Aggregate health snapshots, Feature Catalog data, rule hits, issue records and operator resolutions by event into bilingual structured fault reports. v0.1 runs no node commands or LLM and changes no workloads, nodes or configuration.'), history: [tx('v0.1.0 · Evidence Bundle、确定性报告 API、证据引用、缺口与安全边界', 'v0.1.0 · Evidence Bundle, deterministic report APIs, evidence references, gaps and safety boundaries'), tx('v0.1.0 · 告警中心增加报告入口与双语报告抽屉', 'v0.1.0 · report entry and bilingual report drawer added to Alert Center')] },
    { id: 'node-access', name: tx('节点证据 Skill', 'Node Evidence Skill'), version: 'v0.5.0', status: tx('恢复感知只读采集', 'RECOVERY-AWARE COLLECTION'), desc: tx('硬件事件立即采集；节点失联作为持续状态等待恢复，恢复后补采启动历史、GPU 快照和故障时间窗内核日志。', 'Hardware events collect immediately; node outages are persistent conditions that wait for recovery, then collect boot history, GPU snapshots, and incident-window kernel logs.'), history: [tx('v0.5.0 · 告警 Event/Condition 分层，节点 offline 等待恢复后自动补采并展示等待任务', 'v0.5.0 · alert Event/Condition layering with automatic post-recovery collection and waiting-task visibility for offline nodes'), tx('v0.4.3 · 认证检查默认查询最近 5 条并支持按需加载更多历史', 'v0.4.3 · authentication checks query the five most recent records by default and load more history on demand'), tx('v0.4.2 · SSH 握手按 known_hosts 已信任的主机密钥算法安全重试，避免多算法节点误报身份失败', 'v0.4.2 · safely retry SSH handshakes with host-key algorithms already trusted by known_hosts to prevent false identity failures on multi-algorithm nodes'), tx('v0.4.1 · 节点凭据与认证检查改为页面开放配置，Profile ID 随账号自动建议', 'v0.4.1 · open page configuration for node credentials and authentication checks with automatic Profile ID suggestions'), tx('v0.4.0 · 受控 SSH 只读执行器、默认事件采集、脱敏审计和 Evidence Bundle 接入', 'v0.4.0 · controlled SSH read-only runner, default event collection, redacted audit, and Evidence Bundle integration'), tx('v0.3.2 · 移除逐次计划入口，明确低负载只读默认采集与高影响操作人工确认边界', 'v0.3.2 · removed per-run planning and clarified default low-impact read-only collection versus confirmation-gated high-impact operations'), tx('v0.3.1 · 建立受管节点、注册命令与资源预算约束，后由默认采集策略替代页面计划', 'v0.3.1 · established managed-node, registered-command, and resource-budget constraints, later superseded by the default collection policy'), tx('v0.3.0 · 受管资产限制、known-host 校验、仅认证 API、审计记录和 access 问题自动开闭', 'v0.3.0 · managed-asset restriction, known-host verification, authentication-only API, audit records, and automatic access issue lifecycle'), tx('v0.2.1 · 增加默认关闭的 HTTP 兼容开关、API 状态与页面风险提示', 'v0.2.1 · added a default-off HTTP compatibility switch, API status, and UI risk warning'), tx('v0.2.0 · 页面加密录入、密文落库、管理口令写保护、掩码列表与认证前解密', 'v0.2.0 · encrypted page entry, ciphertext persistence, management-token write protection, masked lists, and pre-authentication decryption'), tx('v0.1.0 · Skill 契约、只读/审批命令分级、凭据引用轮换状态机与状态页面', 'v0.1.0 · Skill contract, read-only/approval command classes, credential-reference rotation state machine, and status UI')] },
    { id: 'skill-foundation', name: tx('基础 Skill 体系', 'Foundational Skill System'), version: 'v0.1.0', status: tx('契约就绪', 'CONTRACT READY'), desc: tx('建立节点只读证据、证据化故障分析和脱敏案例学习三项基础 Skill。当前只交付可审计契约，不提供维护、重启、节点变更或任务操作能力。', 'Establish three foundational Skills for read-only node evidence, evidence-linked fault analysis, and redacted case learning. This release delivers auditable contracts only and provides no maintenance, restart, node-change, or workload-operation capability.'), history: [tx('v0.1.0 · atlas-fault-analysis 定义证据引用、假设状态和结构化报告边界', 'v0.1.0 · atlas-fault-analysis defines evidence references, hypothesis states, and structured-report boundaries'), tx('v0.1.0 · atlas-case-learning 定义脱敏、质量门、episode 与实体隔离划分', 'v0.1.0 · atlas-case-learning defines redaction, quality gates, episodes, and entity-isolated splits'), tx('v0.1.0 · 与 atlas-node-evidence 组成采集→分析→学习基础链路', 'v0.1.0 · forms the evidence-to-analysis-to-learning foundation with atlas-node-evidence')] },
    { id: 'feature', name: tx('统一特征目录', 'Unified Feature Catalog'), version: 'v1.9.0', status: tx('累计值与新增量分离', 'LIFETIME / DELTA SEPARATION'), desc: tx('在 38 个在线健康特征、58 个源查询和全量 metric-family 规则之上，同时读取 Volatile ECC、Aggregate 增量和重映射行增量，区分硬件生命周期累计值与观测期新增故障。', 'Uses 38 online health features, 58 source queries and fleet metric-family rules to combine volatile ECC, aggregate growth and remapped-row growth while separating lifetime totals from newly observed faults.'), history: [tx('v1.9.0 · 接入 Volatile 不可纠正 ECC；不可恢复重映射行累计值仅作观察，只有新增量进入故障规则', 'v1.9.0 · added volatile uncorrected ECC; lifetime uncorrectable remapped-row totals are observation-only and only growth enters fault rules'), tx('基线 v1.1.0 · 以正式 FeatureDefinition 注册时间建立数据 epoch，排除复用版本号的早期实验快照', 'Baseline v1.1.0 · establishes a data epoch from the production FeatureDefinition registration time and excludes earlier experimental snapshots that reused the version'), tx('v1.8.0 · 正式版本隔离、当前版本默认读取与刷新耗时审计', 'v1.8.0 · production version isolation, current-version reads and refresh-duration audit'), tx('v1.7.0 · 7 天稳健历史基线、成熟度、刷新任务与读取 API', 'v1.7.0 · seven-day robust baselines, maturity, refresh jobs and read API'), tx('v1.6.0 · metric-family 规则提升并发布为全量正式特征，保持不参与健康评分', 'v1.6.0 · promoted and published metric-family rule as a production fleet feature while keeping it out of health scoring'), tx('v1.5.0 · metric-family 单节点 canary 配置、shadow 契约与范围保护', 'v1.5.0 · single-node metric-family canary config, shadow contract, and scope guard')] },
    { id: 'prediction', name: tx('硬件故障预警与预测', 'Hardware Early Warning & Failure Prediction'), version: 'v0.0.1', status: tx('特征底座就绪，模型未交付', 'FEATURE FOUNDATION READY'), desc: tx('承载故障发生前的风险预警与概率预测，覆盖 GPU，并按统一资产、特征和标签模型扩展至服务器、存储和网络硬件；当前仅完成统一特征底座，尚未交付 PyOD 或监督概率模型。', 'Provide pre-failure risk warnings and probability prediction for GPUs, extending through a common asset, feature and label model to server, storage and network hardware. The common feature foundation is ready; PyOD and supervised probability models are not yet delivered.'), history: [tx('v0.0.1 · Feature Catalog v1 可供异常检测、风险排序和监督训练消费', 'v0.0.1 · Feature Catalog v1 is consumable by anomaly detection, risk ranking and supervised training'), tx('v0.0.0 · 特征、标签、PyOD、监督模型与概率校准路线完成', 'v0.0.0 · feature, label, PyOD, supervised-model and probability-calibration roadmap')] },
    { id: 'degradation', name: tx('性能衰减识别', 'Performance Degradation Detection'), version: 'v0.2.0', status: tx('历史基线影子检测', 'HISTORICAL SHADOW BASELINE'), desc: tx('高负载 SM 时钟检测优先消费成熟的同型号 7 天历史基线，未成熟时以同节点或同型号集群实时中位数兜底；结果不影响健康分，不输出故障概率。', 'High-load SM-clock detection prefers mature same-model seven-day historical baselines and falls back to live same-node or same-model fleet medians. Results do not affect health scores or emit failure probabilities.'), history: [tx('v0.2.0 · 接入型号/负载/版本历史基线，保留实时同类兜底', 'v0.2.0 · model/load/version historical baselines with live peer fallback'), tx('v0.1.0 · 被动候选 API、同类中位数基线、证据/置信度与影子模式页面', 'v0.1.0 · passive candidate APIs, peer-median baseline, evidence/confidence and shadow-mode UI'), tx('v0.0.1 · 性能特征、SuperBench/DCGM 验证和基线契约', 'v0.0.1 · performance features, SuperBench/DCGM validation and baseline contract'), tx('v0.0.0 · 被动检测与主动验证安全门设计', 'v0.0.0 · passive detection and active-validation safety gates')] },
    { id: 'incident', name: tx('告警中心', 'Alert Center'), version: 'v0.3.1', status: tx('结构化接收增强', 'STRUCTURED INGESTION PLUS'), desc: tx('分层管理原始接收记录与 Atlas 硬件告警；兼容新旧飞书卡片，保留源事件时间、主机和定位标签，并对历史 RAW 记录只读回退解析。', 'Separate raw ingestion records and Atlas hardware alerts. Support legacy and current Feishu cards, preserve source event time, host and locator labels, and read-only reparse historical RAW records.'), history: [tx('v0.3.1 · 新版状态总览/定位标签/恢复卡片解析、历史 RAW 回退与完整通知', 'v0.3.1 · current status/locator/recovery card parsing, historical RAW fallback and complete notifications'), tx('v0.3.0 · 硬件告警关联处置台账并支持详情与人工处置', 'v0.3.0 · hardware alerts link to details and operator resolution workflows'), tx('v0.2.2 · 硬件事件稳定 ID 游标、服务端筛选与前端分页', 'v0.2.2 · stable hardware-event ID cursor, server-side filtering and UI pagination'), tx('v0.2.1 · 只读生产接收库、真实总数、游标分页与新鲜度', 'v0.2.1 · read-only production ingestion store, real totals, cursor pagination and freshness'), tx('v0.2.0 · 接收记录、硬件事件与故障案例分层', 'v0.2.0 · ingestion, hardware event and fault case layers'), tx('v0.1.0 · open / recovered 事件生命周期', 'v0.1.0 · open/recovered event lifecycle')] },
    { id: 'issue', name: tx('数据统计', 'Data Statistics'), version: 'v0.6.2', status: tx('当前范围与历史分离', 'CURRENT / HISTORY SPLIT'), desc: tx('实时统计只覆盖在用资产；节点下架、删除或换代后自动问题退出当前检测，但既有问题、评分、事件和人工处置历史继续保留。', 'Live analytics cover in-use assets only. After retirement, removal, or replacement, automated findings leave current detection while existing issues, scores, events, and operator history remain preserved.'), history: [tx('v0.6.2 · 完成一次性误报清理后，后续退休节点改为清除当前检测并保留历史', 'v0.6.2 · after the one-time false-positive cleanup, future retired nodes clear current detection while preserving history'), tx('v0.6.1 · 资产分类和双边差值仅统计在用状态', 'v0.6.1 · asset categories and cross-source differences count in-use states only'), tx('v0.6.0 · 新增资产统计子页、type 分类、GPU 型号分布、SN/IP 双接口对账与差值明细', 'v0.6.0 · asset statistics subpage, type classification, GPU model distribution, SN/IP cross-source reconciliation, and difference details')] },
    { id: 'platform', name: tx('平台实例与资产源配置', 'Platform & Asset Source Configuration'), version: 'v0.2.1', status: tx('LXOP 在线状态约束', 'LXOP LIVE-STATE POLICY'), desc: tx('支持加密维护 LXOP 双资产接口，并将 on / 已上架使用中作为唯一在用状态；停机、下架和接口移除均从实时监控域排除。', 'Supports encrypted LXOP dual-endpoint configuration and treats on / 已上架使用中 as the only in-use states; stopped, retired, and API-removed assets are excluded from the live monitoring domain.'), history: [tx('v0.2.1 · 在线状态白名单统一驱动资产、监控、健康与问题统计范围', 'v0.2.1 · the in-use allowlist now consistently scopes assets, monitoring, health, and issue analytics'), tx('v0.2.0 · LXOP 运维主机与资产设备双接口、API Key 加密、状态归一、通用资产快照与 GPU 域隔离', 'v0.2.0 · LXOP ops-host and asset-machine endpoints, encrypted API key, normalized lifecycle, general asset snapshots, and GPU-domain isolation'), tx('v0.1.0 · 数据库持久化、运行时读取、页面编辑与安全字段边界', 'v0.1.0 · database persistence, runtime reads, UI editing and public-field safety boundary')] },
    { id: 'validation', name: tx('维修验证闭环', 'Repair Validation Workflow'), version: 'v0.0.0', status: tx('方案阶段', 'DESIGNED'), desc: tx('记录人工维修反馈、根因、修复或更换结果，并通过识别、遥测、错误计数和性能复测验证重新上线。', 'Capture repair feedback, root cause and replacement results, then validate return to service through identity, telemetry, counters and performance checks.'), history: [tx('v0.0.0 · 状态机、维护窗口和验证门设计', 'v0.0.0 · state machine, maintenance window and validation gates')] },
  ];
  const modules = baseModules.map(module => module.id === 'prediction' ? {
    ...module,
    version: 'v0.7.0',
    status: tx('HeaRank 离线 Challenger 入口', 'HEARANK OFFLINE CHALLENGER ENTRY'),
    desc: tx(
      '当前预测能力保持 GPU-only、read-only shadow：已具备训练数据、模型治理卡、影子门禁、成熟 outcome、Ranking@K、naive baseline 和 HeaRank 7d node-risk challenger 入口，但不触发告警、调度、维修或自动隔离。',
      'Prediction remains GPU-only and read-only shadow: training data, model governance cards, shadow gates, mature outcomes, Ranking@K, naive baselines, and a HeaRank 7d node-risk challenger entry are available, but no alert, scheduling, repair or automatic isolation is triggered.',
    ),
    history: [
      tx('v0.7.0 · 增加 HeaRank 离线 challenger 报告入口，对比 Logistic 概率、节点历史故障次数和阈值二值信号的 7d 节点排序', 'v0.7.0 · adds a HeaRank offline challenger report comparing Logistic probability, node prior failure count, and threshold-binary signals for 7d node ranking'),
      tx('v0.6.1 · outcome report 增加 all-negative / all-positive naive baseline 对比，统一成熟样本口径评估真实增益', 'v0.6.1 · outcome report adds all-negative/all-positive naive baselines on the same matured-sample denominator to measure real lift'),
      tx('v0.6.0 · 增加 model card / dataset card / shadow gate 治理报告 API 与前端摘要，继续保持 read-only shadow', 'v0.6.0 · adds model card, dataset card and shadow-gate governance report API with UI summary while remaining read-only shadow'),
      tx('v0.5.0 · outcome report API 与前端报告展示，汇总成熟样本、安全边界和下一步建议', 'v0.5.0 · outcome report API and UI summarize maturity, safety envelope and next-run guidance'),
      tx('v0.4.0 · 增加 GPU / 节点级 Ranking@K outcome 指标，用于 HeaRank 风格验证对齐', 'v0.4.0 · adds GPU/node Ranking@K outcome metrics for HeaRank-style validation alignment'),
      tx('v0.3.0 · 历史训练数据、基线模型、特征契约与只读 shadow scoring 闭环', 'v0.3.0 · closes historical training data, baseline model, feature contract and read-only shadow scoring loop'),
      tx('v0.32.0 · 首批前瞻评分发现 56% 超阈值和节点聚集，增加分位数与分布偏移门，将 HIGH/LOW 降级为待验证观测并阻断周期调度', 'v0.32.0 · the first prospective run found 56% threshold hits and node clustering; adds quantile/distribution-shift gates, downgrades HIGH/LOW to unvalidated observations, and blocks periodic scheduling'),
      tx('v0.31.0 · 人工触发只读影子评分，每批重新验证模型哈希、数据覆盖和 70 列契约，保存概率与特征指纹但不发告警或执行动作', 'v0.31.0 · adds manually triggered read-only shadow scoring that rechecks artifact hash, live coverage, and all 70 columns, persisting probabilities and feature fingerprints without alerts or actions'),
      tx('v0.30.0 · 逐卡审计当前 24 小时模型源指标的覆盖率与新鲜度，通过后仍保持评分关闭', 'v0.30.0 · audits trailing-24h coverage and freshness of every model source metric per GPU while keeping scoring disabled after a pass'),
      tx('v0.29.0 · 在固定分层样本的原历史截止点重新查询 Prometheus，逐值比较 70 列并保存误差与失败列报告', 'v0.29.0 · re-queries Prometheus at fixed stratified historical cutoffs, compares all 70 values, and persists error and failed-column reports'),
      tx('v0.28.0 · 训练与未来在线提取器共享同一套 24 小时统计实现，并持久化逐列契约等价性和回放阻断项', 'v0.28.0 · shares one trailing-24h statistics implementation between training and future online extraction, persisting column parity and replay blockers'),
      tx('v0.27.0 · 将通过完整性、稳定性和校准门的窗口注册为只读影子候选，在线评分和任何操作仍保持关闭', 'v0.27.0 · registers horizons passing integrity, stability, and calibration gates as read-only shadow candidates while online scoring and every action remain disabled'),
      tx('v0.26.0 · 仅使用验证集拟合 Platt 概率校准参数，测试集保持只读审计并对比校准前后 ECE/BSS', 'v0.26.0 · fits Platt probability calibration on validation only, keeps test labels audit-only, and compares pre/post ECE and BSS'),
      tx('v0.25.0 · 以测试集 ECE、Brier Skill Score 和十档可靠性分箱阻断未校准概率进入影子候选', 'v0.25.0 · gates shadow candidacy on held-out ECE, Brier Skill Score, and ten-bin reliability diagnostics'),
      tx('v0.24.0 · 验证集与测试集分别执行 GPU cluster Bootstrap，只有两者均稳定正向才标记跨时间稳健候选', 'v0.24.0 · bootstraps validation and test by GPU cluster independently and marks a robust cross-time candidate only when both remain stably positive'),
      tx('v0.23.0 · 测试集按 GPU UUID 整体 Bootstrap 1000 次，输出 ROC/PR 95% 置信区间并标记候选、反向或不确定信号', 'v0.23.0 · runs 1,000 GPU-UUID cluster bootstraps on held-out tests, reports 95% ROC/PR confidence intervals, and classifies candidate, inverse, or inconclusive signals'),
      tx('v0.22.2 · 修正就绪分层空阻断原因导致的白屏，并增加模块级渲染错误隔离', 'v0.22.2 · fixes blank pages caused by null blocking reasons on ready strata and adds module-level render error isolation'),
      tx('v0.22.1 · 预测数据仅在进入故障预测子页时加载，GPU 就绪队列每页仅渲染 50 条', 'v0.22.1 · loads prediction datasets only when the failure-prediction subpage opens and renders the GPU readiness queue in pages of 50'),
      tx('v0.22.0 · 每次训练逐列审计禁用特征并记录排除原因，发现禁用列入模即阻断产物', 'v0.22.0 · audits prohibited features column by column with exclusion reasons and blocks artifacts whenever a prohibited column is selected'),
      tx('v0.21.0 · 采样数仅用于连续性质量门且禁止入模，正样本核心遥测连续性必须达到 70%', 'v0.21.0 · sample counts are quality-gate-only and model-excluded; positive core telemetry continuity must reach 70%'),
      tx('v0.20.0 · 只在通过数据充分性门的故障类型、GPU 型号和预测窗口上训练专用离线基线', 'v0.20.0 · trains scoped offline baselines only for fault type, GPU model and horizons that pass the data-sufficiency gate'),
      tx('v0.19.0 · 按故障类型、GPU 型号和预测窗口统计三分割样本与独立 GPU，阻断数据不足分层', 'v0.19.0 · counts split samples and independent GPUs by fault type, GPU model and horizon, blocking data-insufficient strata'),
      tx('v0.18.0 · 训练样本保留故障类型、驱动版本、规则版本与证据等级，基线按标签维度分层评估', 'v0.18.0 · preserves fault type, driver, rule version and evidence tier in training rows, with label-stratified baseline evaluation'),
      tx('v0.17.0 · XID 109 运营高优先与训练确定性拆分、规则 v2 重放及按当前规则快速重建训练准备', 'v0.17.0 · separates XID 109 operational priority from training certainty, adds rule-v2 replay and fast preparation rebuilds under current labels'),
      tx('v0.16.1 · 固定 PR-AUC 数据库列名并增加迁移回归测试', 'v0.16.1 · stabilized the PR-AUC database column name with a migration regression test'),
      tx('v0.16.0 · 11 个预测窗离线 Logistic Regression、已发生故障特征排除、验证阈值与测试集评估', 'v0.16.0 · offline Logistic Regression for 11 horizons, occurred-fault feature exclusion, validation thresholds and held-out test evaluation'),
      tx('v0.15.0 · 正负样本合并、稀疏缺失保留、类别权重及时间/实体/配对/契约泄漏审计', 'v0.15.0 · positive/control matrix assembly, sparse missingness, class weights, and time/entity/pairing/contract leakage audits'),
      tx('v0.14.0 · 同卡健康对照去重查询、24 小时特征、遥测连续性与负载可比性门禁', 'v0.14.0 · deduplicated same-GPU healthy-control queries, trailing-24h features, telemetry continuity and load comparability gates'),
      tx('v0.13.1 · 隔离跨节点同步事件批次，按唯一时间边界切分并强制验证/测试集最小占比', 'v0.13.1 · censor synchronized cross-node event batches, split on distinct time boundaries, and enforce minimum validation/test ratios'),
      tx('v0.13.0 · 全量特征质量门、零遥测删失、GPU UUID 与时间双重隔离、同卡健康对照请求及校验产物', 'v0.13.0 · full-feature quality gate, zero-telemetry censoring, GPU UUID and time isolation, same-GPU healthy-control requests and checksummed artifacts'),
      tx('v0.12.1 · 从历史身份区间补齐 GPU 型号，按型号支持范围计算应有指标覆盖率', 'v0.12.1 · enrich GPU model from historical identity intervals and calculate expected metric coverage by model support'),
      tx('v0.12.0 · 按 GPU episode 批量读取 Prometheus/VM、24 小时统计特征、覆盖率报告和版本化产物', 'v0.12.0 · batched Prometheus/VM reads by GPU episode, trailing 24-hour statistics, coverage report and versioned artifacts'),
      tx('v0.11.0 · 预测窗口成熟判定、规则/人工双结果、TP/FP/FN/TN 与准确率闭环', 'v0.11.0 · horizon maturity, rule/human dual outcomes, TP/FP/FN/TN and accuracy feedback loop'),
      tx('v0.10.0 · 规则正代理/仅上下文/需人工分层、规则置信度、人工覆核优先和可重建加权样本', 'v0.10.0 · rule-positive/context/human-review tiers, rule confidence, human override precedence and rebuildable weighted samples'),
      tx('v0.9.0 · 故障 episode 合并、point-in-time 多窗口清单、样本资格门和部署节点版本化产物', 'v0.9.0 · fault episode merging, point-in-time multi-horizon manifest, sample eligibility gates and deployment-node versioned artifacts'),
      tx('v0.8.0 · UUID 前缀/大小写标准化、跨 exporter 去重、旧表无损迁移和回填成功后旧区间替换', 'v0.8.0 · UUID prefix/case normalization, cross-exporter deduplication, lossless legacy-table migration and post-success stale interval replacement'),
      tx('v0.7.0 · 六小时稀疏身份区间、同槽位换卡候选、整机身份边界和历史故障候选关联', 'v0.7.0 · six-hour sparse identity intervals, same-slot replacement candidates, node boundaries and historical candidate linkage'),
      tx('v0.6.0 · 历史候选审核 API、可重审训练用途、操作人/时间/备注审计和页面操作', 'v0.6.0 · candidate review API, repeatable training disposition, operator/time/note audit and UI actions'),
      tx('v0.5.0 · 覆盖低/高优先 XID、恢复型 120/154、GPU 掉卡与不可恢复显存，并定义正常对照和污染窗口', 'v0.5.0 · low/high-priority XIDs, recovery-latched 120/154, GPU dropout, uncorrectable memory, matched controls and censor windows'),
      tx('v0.4.0 · 七天分块的 ALERTS 起点扫描、XID episode 合并、幂等候选台账与任务进度', 'v0.4.0 · seven-day ALERTS onset scans, XID episode merging, idempotent candidate ledger, and durable task progress'),
      tx('v0.3.0 · 历史监控源、远端本地执行、定时/人工审计、覆盖与缺失指标台账', 'v0.3.0 · historical monitoring sources, remote-local execution, scheduled/manual audits, and coverage/missing-metric ledger'),
      tx('v0.2.0 · 健康特征/评分/规则命中保留期由 35 天改为可配置，生产默认一年', 'v0.2.0 · configurable health feature/score/rule-hit retention replaces the fixed 35-day cap, with one-year production default'),
      tx('v0.2.0 · GPU 规则事件幂等物化为强弱代理标签，仅硬件人工确认可升级 confirmed', 'v0.2.0 · GPU rule episodes materialize idempotently as strong/weak proxy labels; only human-confirmed hardware resolutions upgrade to confirmed'),
      tx('v0.1.0 · 预测模型/标签/结果持久化契约、GPU 数据就绪 API 与页面', 'v0.1.0 · persistent model/label/result contracts plus GPU readiness APIs and UI'),
      tx('v0.1.0 · 明确弱标签、时间泄漏防护、实体隔离、概率校准和禁止自动动作', 'v0.1.0 · weak-label tiers, leakage prevention, entity isolation, calibration, and no-auto-action boundaries'),
      ...module.history,
    ],
  } : module.id === 'analysis' ? {
    ...module,
    version: 'v0.2.0',
    status: tx('恢复证据闭环', 'RECOVERY EVIDENCE LOOP'),
    desc: tx(
      '按事件生成确定性故障报告，并在可用性问题恢复后把启动历史、GPU 快照和时间窗内核日志直接关联到问题详情；仅展示证据，不自动宣称根因。',
      'Generate deterministic event reports and link boot history, GPU snapshots, and bounded kernel logs directly to availability issue details after recovery. Evidence is presented without automatically claiming root cause.',
    ),
    history: [
      tx('v0.2.0 · 节点失联恢复后的最新只读证据进入问题详情，保留来源、状态、截断与重试关系', 'v0.2.0 · latest post-outage read-only evidence is shown in issue details with provenance, status, truncation, and retry linkage'),
      ...module.history,
    ],
  } : module.id === 'node-access' ? {
    ...module,
    version: 'v0.6.3',
    desc: tx(
      '硬件事件立即采集，节点失联恢复后补采故障证据；任务保留可分页审计历史，失败后可使用当前全局密码字典人工重试且不覆盖旧记录。',
      'Hardware events collect immediately and outage evidence is collected after recovery. Tasks retain paginated audit history and failed collections can be retried manually with the current global credential dictionary without overwriting old records.',
    ),
    history: [
      tx('v0.6.3 · 任务列表默认只看最新结果，旧失败和 partial 通过历史尝试开关单独查看', 'v0.6.3 · task list defaults to latest results while old failed and partial attempts move behind an explicit history toggle'),
      tx('v0.6.2 · 任务总览按每个事件或状态的最新结果统计，旧失败仅保留在审计历史', 'v0.6.2 · task summary counts the latest result per event or condition while old failures remain audit history only'),
      tx('v0.6.1 · journalctl 时间窗改用 systemd epoch 语法，兼容 Ubuntu 22.04 / systemd 249', 'v0.6.1 · journalctl windows use systemd epoch syntax for Ubuntu 22.04 / systemd 249 compatibility'),
      tx('v0.6.0 · 最近 5 条证据任务、按需加载更多、失败原因与保留历史的人工只读重试', 'v0.6.0 · five recent evidence tasks, on-demand history, failure reasons, and manual read-only retry with preserved audit history'),
      tx('v0.5.1 · 全局密码字典按优先级轮询，任一凭据成功不告警，仅全部耗尽后产生访问问题', 'v0.5.1 · global credential dictionary rotation by priority; any success suppresses access findings and only full exhaustion creates one'),
      ...module.history,
    ],
  } : module);
  const milestones = [
    ['P0', tx('数据底座', 'Data Foundation'), tx('完成', 'COMPLETE')],
    ['P1', tx('GPU 健康', 'GPU Health'), tx('基线完成', 'BASELINE')],
    ['P2', tx('故障闭环', 'Incident Workflow'), tx('开发中', 'ACTIVE')],
    ['P2.5', tx('性能验证', 'Performance Validation'), tx('开发中', 'ACTIVE')],
    ['P3', tx('特征与异常检测', 'Features & Anomaly Detection'), tx('开发中', 'ACTIVE')],
    ['P3.5', tx('只读证据与自动分析', 'Read-only Evidence & Analysis'), tx('开发中', 'ACTIVE')],
    ['P4', tx('硬件预警与预测', 'Hardware Warning & Prediction'), tx('开发中', 'ACTIVE')],
  ];
  const selectedModule = modules.find(module => module.id === moduleDetailID) || null;
  return <>
  <div className="grid">
    <Card className="span-12 product-intro"><div><span>{platformConfig.product_name}</span><h2>Infrastructure Hardware Reliability Workbench</h2><p>{tx('ATLAS 是面向 GPU 集群并可扩展至服务器、存储和网络基础设施的硬件可靠性工作台，提供实时资产对账、监控数据质量发现、硬件健康评分、故障检测、只读证据与结构化故障报告、数据统计与处置、硬件故障预警与预测、性能衰减识别、告警中心以及维修验证闭环。', 'ATLAS is a hardware reliability workbench for GPU clusters, extensible to server, storage and network infrastructure. It provides live asset reconciliation, monitoring data quality detection, hardware health scoring, fault detection, read-only evidence and structured fault reports, data analytics and resolution, hardware early warning and failure prediction, performance degradation analysis, an alert center and repair validation workflows.')}</p></div><Badge value="PLATFORM / v0.62.0" kind="info" /></Card>
    <Card className="span-12"><CardHead code="MILESTONES" title={tx('平台开发里程碑', 'Platform Development Milestones')} /><div className="platform-milestones">{milestones.map(([phase, name, status]) => <div key={phase}><code>{phase}</code><b>{name}</b><Badge value={status} kind={status === tx('完成', 'COMPLETE') || status === tx('基线完成', 'BASELINE') ? 'healthy' : status === tx('开发中', 'ACTIVE') ? 'info' : 'neutral'} /></div>)}</div></Card>
    <Card className="span-12"><CardHead code="CAPABILITY MODULES" title={tx('平台能力模块', 'Platform Capability Modules')} action={<Badge value={`${modules.length} MODULES`} kind="info" />} /><div className="capability-modules">{modules.map(module => <article key={module.id}><header><code>{module.id.toUpperCase()}</code><Badge value={module.status} kind={module.status === tx('开发中', 'ACTIVE') ? 'info' : module.status.includes(tx('完成', 'BASELINE')) || module.status.includes('BASELINE') ? 'healthy' : 'neutral'} /></header><h3>{module.name}</h3><p>{module.desc}</p><div className="module-version"><span>{tx('当前版本', 'CURRENT VERSION')}</span><strong>{module.version}</strong></div><div className="module-history"><span>{tx('最近迭代', 'LATEST ITERATIONS')}</span>{module.history.slice(0, 3).map(item => <small key={item}>{item}</small>)}<button className="module-history-more" onClick={() => setModuleDetailID(module.id)}>{module.history.length > 3 ? tx(`查看全部 ${module.history.length} 次迭代`, `View all ${module.history.length} iterations`) : tx('版本详情', 'Version details')}<ChevronRight size={13} /></button></div></article>)}</div></Card>
  </div>
  <AnimatePresence>{selectedModule && <motion.div className="module-modal-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><button className="module-modal-bg" onClick={() => setModuleDetailID(null)} aria-label={tx('关闭版本详情', 'Close version details')} /><motion.section className="module-modal" role="dialog" aria-modal="true" aria-label={selectedModule.name} initial={{ y: 18, scale: .98 }} animate={{ y: 0, scale: 1 }} exit={{ y: 18, scale: .98 }}><header><div><code>{selectedModule.id.toUpperCase()}</code><h2>{selectedModule.name}</h2></div><button className="icon-btn" onClick={() => setModuleDetailID(null)} aria-label={tx('关闭', 'Close')}><X size={17} /></button></header><div className="module-modal-meta"><Badge value={selectedModule.status} kind={selectedModule.status.includes(tx('完成', 'BASELINE')) || selectedModule.status.includes('BASELINE') ? 'healthy' : 'neutral'} /><span>{tx('当前版本', 'CURRENT VERSION')} <strong>{selectedModule.version}</strong></span></div><p>{selectedModule.desc}</p><div className="module-modal-history"><span>{tx('完整迭代历史', 'FULL ITERATION HISTORY')}</span>{selectedModule.history.map((item, index) => <div key={item}><i /><small>{item}</small>{index === 0 && <Badge value={tx('最新', 'LATEST')} kind="info" />}</div>)}</div></motion.section></motion.div>}</AnimatePresence>
  </>;
}

function PlatformSettings({ tx, value, onSaved }: { tx: Tx; value: PlatformConfig; onSaved: (config: PlatformConfig) => void }) {
  const [draft, setDraft] = useState<PlatformConfig>(value);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const update = (field: keyof PlatformConfig, next: string) => setDraft(current => ({ ...current, [field]: next }));
  const save = async () => {
    setSaving(true); setMessage(''); setError('');
    try {
      const response = await fetch('/api/v1/platform-config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          instance_name: draft.instance_name, product_name: draft.product_name,
          product_tagline: draft.product_tagline, environment: draft.environment,
        }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setDraft(payload.data);
      onSaved(payload.data);
      setMessage(tx('配置已保存并即时生效', 'Configuration saved and applied'));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : tx('保存失败', 'Save failed'));
    } finally { setSaving(false); }
  };
  const valid = [draft.instance_name, draft.product_name, draft.product_tagline, draft.environment].every(item => item.trim());
  return <div className="grid platform-settings">
    <Card className="span-7">
      <CardHead code="INSTANCE BRANDING" title={tx('平台展示配置', 'Platform Display Settings')} action={<Badge value="PUBLIC FIELDS ONLY" kind="info" />} />
      <p className="settings-intro">{tx('这些字段只控制当前 Atlas 实例的展示身份，不会修改产品能力、采集地址、数据库或其他敏感运行参数。', 'These fields only control the display identity of this Atlas instance. They do not change product capabilities, collectors, databases or sensitive runtime settings.')}</p>
      <form className="settings-form" onSubmit={event => { event.preventDefault(); void save(); }}>
        <label><span>{tx('实例名称', 'Instance name')}<small>instance_name · 80</small></span><input maxLength={80} value={draft.instance_name} onChange={event => update('instance_name', event.target.value)} placeholder={tx('例如：智元集群', 'Example: Zhiyuan Cluster')} /></label>
        <label><span>{tx('产品名称', 'Product name')}<small>product_name · 40</small></span><input maxLength={40} value={draft.product_name} onChange={event => update('product_name', event.target.value)} placeholder="ATLAS" /></label>
        <label><span>{tx('产品副标题', 'Product tagline')}<small>product_tagline · 80</small></span><input maxLength={80} value={draft.product_tagline} onChange={event => update('product_tagline', event.target.value)} placeholder="GPU RELIABILITY" /></label>
        <label><span>{tx('环境标识', 'Environment label')}<small>environment · 40</small></span><input maxLength={40} value={draft.environment} onChange={event => update('environment', event.target.value)} placeholder="PRODUCTION / 7077" /></label>
        {error && <p className="form-error">{error}</p>}
        {message && <p className="form-success">{message}</p>}
        <button className="primary-action" type="submit" disabled={saving || !valid}><Save size={15} />{saving ? tx('保存中', 'Saving') : tx('保存并应用', 'Save & Apply')}</button>
      </form>
    </Card>
    <Card className="span-5 settings-preview">
      <CardHead code="LIVE PREVIEW" title={tx('展示预览', 'Display Preview')} />
      <div className="brand-preview"><span className="brand-icon"><Layers3 size={20} /></span><div><b>{draft.instance_name || '—'}</b><small>{draft.product_name || '—'}</small></div></div>
      <div className="preview-environment"><i /><span><b>{draft.environment || '—'}</b><small>{draft.product_tagline || '—'}</small></span></div>
      <dl><div><dt>{tx('浏览器标题', 'Browser title')}</dt><dd>{draft.instance_name || '—'} · {draft.product_name || '—'}</dd></div><div><dt>{tx('生效范围', 'Applied to')}</dt><dd>{tx('侧边栏、面包屑、环境徽标、浏览器标题', 'Sidebar, breadcrumbs, environment badge and browser title')}</dd></div></dl>
      {value.updated_at && <small className="settings-updated">{tx('最近更新', 'Last updated')} · {time(value.updated_at)}</small>}
    </Card>
    <LXOPAssetSettings tx={tx} />
  </div>;
}

function LXOPAssetSettings({ tx }: { tx: Tx }) {
  const empty: LXOPAssetConfig = {
    ops_host_url: '', asset_machine_url: '', data_center_id: '', insecure_skip_verify: true,
    enabled: true, api_key_configured: false, last_sync_status: 'not_configured',
    last_ops_host_count: 0, last_machine_count: 0,
  };
  const [config, setConfig] = useState<LXOPAssetConfig>(empty);
  const [apiKey, setAPIKey] = useState('');
  const [adminToken, setAdminToken] = useState('');
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    void fetch('/api/v1/platform-config/assets').then(async response => {
      const payload = await response.json();
      if (!cancelled && response.ok && payload.data) setConfig({ ...empty, ...payload.data });
    }).catch(() => undefined);
    return () => { cancelled = true; };
  }, []);
  const update = <K extends keyof LXOPAssetConfig>(field: K, next: LXOPAssetConfig[K]) => setConfig(current => ({ ...current, [field]: next }));
  const save = async () => {
    setSaving(true); setMessage(''); setError('');
    try {
      const response = await fetch('/api/v1/platform-config/assets', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-Atlas-Admin-Token': adminToken },
        body: JSON.stringify({
          ops_host_url: config.ops_host_url, asset_machine_url: config.asset_machine_url,
          data_center_id: config.data_center_id, api_key: apiKey,
          insecure_skip_verify: config.insecure_skip_verify, enabled: config.enabled,
        }),
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
      setConfig({ ...empty, ...payload.data }); setAPIKey(''); setAdminToken('');
      setMessage(tx('资产源配置已加密保存；下一轮同步开始使用', 'Asset source saved encrypted and will be used by the next sync'));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : tx('保存失败', 'Save failed'));
    } finally { setSaving(false); }
  };
  const valid = config.ops_host_url.trim() && config.asset_machine_url.trim() && config.data_center_id.trim() && (config.api_key_configured || apiKey.trim()) && adminToken.trim();
  return <>
    <Card className="span-8 lxop-settings">
      <CardHead code="LXOP ASSET SOURCE" title={tx('实时资产源配置', 'Real-time Asset Source')} action={<Badge value={config.enabled ? 'ENABLED' : 'DISABLED'} kind={config.enabled ? 'healthy' : 'neutral'} />} />
      <p className="settings-intro">{tx('每轮库存对账只读调用两个 LXOP OpenAPI。API Key 使用 AES-256-GCM 加密保存，保存后不回显；dataCenterId 必须明确配置。', 'Each inventory reconciliation reads both LXOP OpenAPIs. The API key is AES-256-GCM encrypted and never returned; dataCenterId must be explicit.')}</p>
      <form className="settings-form" onSubmit={event => { event.preventDefault(); void save(); }}>
        <label><span>{tx('运维主机接口', 'Ops host endpoint')}<small>GET · FULL URL</small></span><input value={config.ops_host_url} onChange={event => update('ops_host_url', event.target.value)} placeholder="https://host:7090/openapi/v1/ops/host/list" /></label>
        <label><span>{tx('资产设备接口', 'Asset machine endpoint')}<small>GET · FULL URL</small></span><input value={config.asset_machine_url} onChange={event => update('asset_machine_url', event.target.value)} placeholder="https://host:7090/openapi/v1/asset/machine/list" /></label>
        <label><span>{tx('数据中心 ID', 'Data center ID')}<small>dataCenterId · REQUIRED</small></span><input value={config.data_center_id} onChange={event => update('data_center_id', event.target.value)} /></label>
        <label><span>X-API-Key<small>{config.api_key_configured ? tx('已加密保存 · 留空保持不变', 'ENCRYPTED · BLANK KEEPS CURRENT') : tx('首次配置必填', 'REQUIRED INITIALLY')}</small></span><input type="password" autoComplete="new-password" value={apiKey} onChange={event => setAPIKey(event.target.value)} placeholder={config.api_key_configured ? '••••••••••••' : 'X-API-Key'} /></label>
        <label><span>{tx('管理口令', 'Management token')}<small>X-Atlas-Admin-Token</small></span><input type="password" autoComplete="off" value={adminToken} onChange={event => setAdminToken(event.target.value)} /></label>
        <label className="settings-check"><span>{tx('连接选项', 'Connection options')}<small>INTERNAL COMPATIBILITY</small></span><span className="check-options"><input type="checkbox" checked={config.enabled} onChange={event => update('enabled', event.target.checked)} />{tx('启用实时同步', 'Enable live sync')}<input type="checkbox" checked={config.insecure_skip_verify} onChange={event => update('insecure_skip_verify', event.target.checked)} />{tx('允许自签名 TLS', 'Allow self-signed TLS')}</span></label>
        {error && <p className="form-error">{error}</p>}
        {message && <p className="form-success">{message}</p>}
        <button className="primary-action" type="submit" disabled={saving || !valid}><Save size={15} />{saving ? tx('保存中', 'Saving') : tx('加密保存', 'Save Encrypted')}</button>
      </form>
    </Card>
    <Card className="span-4 settings-preview lxop-status">
      <CardHead code="SOURCE STATUS" title={tx('资产同步状态', 'Asset Sync Status')} />
      <div className="preview-environment"><i /><span><b>{config.last_sync_status.toUpperCase()}</b><small>{config.last_sync_at ? time(config.last_sync_at) : tx('等待首次同步', 'Awaiting first sync')}</small></span></div>
      <dl>
        <div><dt>{tx('运维主机', 'Ops hosts')}</dt><dd>{config.last_ops_host_count}</dd></div>
        <div><dt>{tx('资产设备', 'Machines')}</dt><dd>{config.last_machine_count}</dd></div>
        <div><dt>API KEY</dt><dd>{config.api_key_configured ? tx('已加密保存', 'Encrypted at rest') : tx('未配置', 'Not configured')}</dd></div>
        <div><dt>{tx('在用状态', 'In-use states')}</dt><dd><code>on</code> · <code>已上架使用中</code></dd></div>
      </dl>
      {config.last_sync_error && <p className="form-error">{config.last_sync_error}</p>}
    </Card>
  </>;
}

function FaultEventList({ tx, items }: { tx: Tx; items: FaultEvent[] }) { return <div className="event-list">{items.map(x => <div className="event-row" key={x.id}><Badge value={x.severity.toUpperCase()} kind={tone(x.severity)} /><span><b>{x.rule_code}</b><small>{x.node_ip} · GPU {x.gpu_index} · {x.evidence}</small></span><code>×{x.occurrence_count}</code></div>)}{items.length === 0 && <Empty tx={tx} title={tx('无未恢复硬件事件', 'No open hardware events')} />}</div>; }
function Empty({ tx, title }: { tx: Tx; title: string }) { return <div className="empty"><Command size={20} /><b>{title}</b><span>{tx('等待数据', 'Waiting for data')}</span></div>; }

function GlobalSearch({ tx, query, setQuery, pagesCopy, items, assets, close, navigate, select }: { tx: Tx; query: string; setQuery: (v: string) => void; pagesCopy: ReturnType<typeof pageCopy>; items: Ingestion[]; assets: GPUAsset[]; close: () => void; navigate: (p: PageId) => void; select: (id: number) => void }) {
  const input = useRef<HTMLInputElement>(null); useEffect(() => { input.current?.focus(); const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') close(); }; window.addEventListener('keydown', escape); return () => window.removeEventListener('keydown', escape); }, [close]);
  const q = query.trim().toLowerCase(); const pageRows = pages.filter(x => !q || `${pagesCopy[x].label} ${pagesCopy[x].title} ${x}`.toLowerCase().includes(q));
  const eventRows = q ? items.filter(x => [x.message, x.host, x.source, ...Object.entries(x.labels || {}).flat()].join(' ').toLowerCase().includes(q)).slice(0, 8) : items.slice(0, 4);
  const assetRows = q ? assets.filter(x => [x.node_ip, x.gpu_uuid, x.model_name, x.model, x.pci_bus_id, x.asset_key].join(' ').toLowerCase().includes(q)).slice(0, 8) : [];
  return <motion.div className="modal-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><button className="modal-bg" onClick={close} aria-label={tx('关闭搜索', 'Close search')} /><motion.div className="search-modal" initial={{ y: -18, scale: .98 }} animate={{ y: 0, scale: 1 }}><div className="search-input"><Search size={18} /><input ref={input} value={query} onChange={e => setQuery(e.target.value)} placeholder={tx('搜索页面、主机、GPU、UUID、XID、事件', 'Search pages, hosts, GPUs, UUIDs, XIDs, incidents')} /><kbd>ESC</kbd><button onClick={close} aria-label={tx('关闭搜索', 'Close search')}><X size={16} /></button></div><div className="search-results"><label>{tx('页面', 'PAGES')}</label>{pageRows.map(id => { const Icon = pageIcons[id]; return <button key={id} onClick={() => { navigate(id); close(); }}><Icon size={16} /><span><b>{pagesCopy[id].label}</b><small>{pagesCopy[id].desc}</small></span><ChevronRight size={14} /></button>; })}{assetRows.length > 0 && <label>{tx('GPU 资产', 'GPU ASSETS')}</label>}{assetRows.map(x => <button key={x.asset_key} onClick={() => { navigate('gpus'); close(); }}><Cpu size={16} /><span><b>{x.gpu_uuid || 'UUID UNKNOWN'}</b><small>{x.node_ip} · GPU {x.gpu_index} · {x.model_name || x.model || 'UNKNOWN'}</small></span><Badge value={x.state.toUpperCase()} kind={x.state === 'active' ? 'healthy' : 'warning'} /></button>)}<label>{tx('事件', 'INCIDENTS')}</label>{eventRows.map(x => <button key={x.id} onClick={() => select(x.id)}><ShieldAlert size={16} /><span><b>{x.message}</b><small>{x.host} · {x.labels?.UUID || x.source}</small></span><Badge value={x.level} kind={tone(x.level)} /></button>)}{pageRows.length + assetRows.length + eventRows.length === 0 && <Empty tx={tx} title={tx('无结果', 'No results')} />}</div></motion.div></motion.div>;
}

function IssueDrawer({ tx, detail, lang, close, saved }: { tx: Tx; detail: IssueDetail; lang: string; close: () => void; saved: () => void }) {
  const issue = detail.issue;
  const nodeEvidence = detail.node_evidence_collection;
  const [status, setStatus] = useState(issue.status === 'open' ? 'in_progress' : issue.status);
  const [rootCause, setRootCause] = useState('');
  const [solution, setSolution] = useState('');
  const [process, setProcess] = useState('');
  const [result, setResult] = useState('');
  const [operator, setOperator] = useState('');
  const [trainingEligible, setTrainingEligible] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const submit = async () => {
    setSaving(true); setError('');
    try {
      const response = await fetch(`/api/v1/issues/${issue.id}/resolution`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ status, root_cause: rootCause, solution, resolution_process: process, result, operator, training_eligible: trainingEligible, evidence: [] }) });
      if (!response.ok) { const payload = await response.json().catch(() => null); throw new Error(payload?.error?.message || `HTTP ${response.status}`); }
      setRootCause(''); setSolution(''); setProcess(''); setResult(''); setTrainingEligible(false); saved();
    } catch (reason) { setError(reason instanceof Error ? reason.message : String(reason)); }
    finally { setSaving(false); }
  };
  return <motion.div className="drawer-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><button className="drawer-bg" onClick={close} /><motion.aside className="drawer issue-drawer" initial={{ x: '100%' }} animate={{ x: 0 }} exit={{ x: '100%' }}><header><div><span>ISSUE / #{issue.id} · {issue.category.toUpperCase()}</span><h2>{issue.title}</h2></div><button className="icon-btn" onClick={close}><X size={17} /></button></header><div className="drawer-body"><div className="drawer-meta"><Badge value={issue.severity.toUpperCase()} kind={tone(issue.severity)} /><Badge value={issue.status.toUpperCase()} kind={issueTone(issue.status)} /><Badge value={issue.detection_state.toUpperCase()} kind={issue.detection_state === 'active' ? 'warning' : 'healthy'} /><time>{time(issue.last_detected_at, lang)}</time></div><div className="detail-grid">{[[tx('节点', 'Node'), issue.node_ip], ['GPU UUID', issue.gpu_uuid], [tx('对象', 'Entity'), `${issue.entity_type} / ${issue.entity_key}`], [tx('检测来源', 'Detection Source'), issue.detection_source]].map(item => <div key={item[0]}><small>{item[0]}</small><b>{item[1] || '—'}</b></div>)}</div><section><CardHead code="EVIDENCE" title={tx('问题描述', 'Problem Evidence')} /><p className="muted">{issue.description || '—'}</p></section>{nodeEvidence ? <section><CardHead code="RECOVERY EVIDENCE" title={tx('节点恢复只读证据', 'Post-recovery Read-only Evidence')} action={<Badge value={nodeEvidence.status.toUpperCase()} kind={nodeEvidence.status === 'completed' ? 'healthy' : nodeEvidence.status === 'waiting_recovery' ? 'info' : 'warning'} />} /><div className="recovery-evidence-banner"><ShieldCheck size={17} /><span><b>{tx('以下为采集证据，不是自动根因结论', 'Collected evidence, not an automatic root-cause conclusion')}</b><small>#{nodeEvidence.id} · {nodeEvidence.trigger} · {nodeEvidence.read_only ? 'READ-ONLY' : 'EXECUTION UNKNOWN'}{nodeEvidence.retry_of_collection_id ? ` · RETRY OF #${nodeEvidence.retry_of_collection_id}` : ''} · {time(nodeEvidence.finished_at || nodeEvidence.started_at, lang)}</small></span></div>{nodeEvidence.status === 'waiting_recovery' ? <p className="muted">{tx('节点仍不可连接；恢复为可访问状态后将自动补采。', 'The node is still unreachable; evidence will be collected automatically after it becomes accessible.')}</p> : <div className="recovery-evidence-records">{(nodeEvidence.records || []).map(record => <article key={record.id}><header><code>{record.command_id}</code><Badge value={record.status.toUpperCase()} kind={record.status === 'completed' ? 'healthy' : 'warning'} /><small>{record.kind.toUpperCase()} · {record.output_bytes} B{record.truncated ? ' · TRUNCATED' : ''}</small></header><pre>{record.output || tx('无输出', 'No output')}</pre></article>)}{(nodeEvidence.records || []).length === 0 && <p className="muted">{nodeEvidence.failure_code ? `${tx('采集失败', 'Collection failed')}: ${nodeEvidence.failure_code}` : tx('暂无证据记录', 'No evidence records')}</p>}</div>}</section> : issue.issue_type === 'node_state' ? <section><CardHead code="RECOVERY EVIDENCE" title={tx('节点恢复只读证据', 'Post-recovery Read-only Evidence')} /><p className="muted">{issue.detection_state === 'active' ? tx('节点仍处于失联状态；恢复后将自动采集启动历史、GPU 快照和时间窗内核日志。', 'The node is still unavailable. Boot history, GPU snapshots, and bounded kernel logs will be collected automatically after recovery.') : tx('该问题暂无关联的恢复证据任务。', 'No post-recovery evidence collection is linked to this issue yet.')}</p></section> : null}<section><CardHead code="HUMAN FEEDBACK" title={tx('补充原因与解决过程', 'Root Cause & Resolution')} /><div className="resolution-form"><label>{tx('处理状态', 'Status')}<select value={status} onChange={event => setStatus(event.target.value)}><option value="open">OPEN</option><option value="in_progress">IN PROGRESS</option><option value="resolved">RESOLVED</option><option value="ignored">IGNORED</option></select></label><label>{tx('根本原因', 'Root cause')}<textarea value={rootCause} onChange={event => setRootCause(event.target.value)} placeholder={tx('问题为什么发生？需写证据，不只写现象', 'Why did it happen? Record evidence, not only symptoms.')} /></label><label>{tx('解决方案', 'Solution')}<textarea value={solution} onChange={event => setSolution(event.target.value)} placeholder={tx('采用了什么修复方案？', 'What remediation was chosen?')} /></label><label>{tx('解决过程', 'Resolution process')}<textarea value={process} onChange={event => setProcess(event.target.value)} placeholder={tx('按顺序记录操作、验证与回滚点', 'Record actions, validation and rollback points in order.')} /></label><label>{tx('处理结果', 'Result')}<textarea value={result} onChange={event => setResult(event.target.value)} placeholder={tx('是否恢复、如何验证、是否遗留风险', 'Recovery state, validation evidence and remaining risk.')} /></label><label>{tx('处理人', 'Operator')}<input value={operator} onChange={event => setOperator(event.target.value)} placeholder={tx('姓名或工号', 'Name or operator ID')} /></label><label className="training-check"><input type="checkbox" checked={trainingEligible} onChange={event => { setTrainingEligible(event.target.checked); if (event.target.checked) setStatus('resolved'); }} /><span>{tx('内容完整且已脱敏，可进入 AI/Skill 训练数据集', 'Complete and sanitized; eligible for AI/Skill training dataset')}</span></label>{error && <p className="form-error">{error}</p>}<button className="primary-action" onClick={submit} disabled={saving}>{saving ? tx('保存中…', 'Saving…') : tx('保存处置记录', 'Save resolution')}</button></div></section><section><CardHead code="HISTORY" title={tx('处置历史', 'Resolution History')} action={<Badge value={`${detail.resolutions.length} RECORDS`} kind="info" />} /><div className="resolution-history">{detail.resolutions.map(item => <article key={item.id}><header><Badge value={item.status.toUpperCase()} kind={issueTone(item.status)} /><b>{item.operator}</b><time>{time(item.created_at, lang)}</time>{item.training_eligible && <Badge value="TRAINING" kind="info" />}</header><p><strong>{tx('原因', 'Cause')}:</strong> {item.root_cause || '—'}</p><p><strong>{tx('方案', 'Solution')}:</strong> {item.solution || '—'}</p><p><strong>{tx('过程', 'Process')}:</strong> {item.resolution_process || '—'}</p><p><strong>{tx('结果', 'Result')}:</strong> {item.result || '—'}</p></article>)}{detail.resolutions.length === 0 && <p className="muted">{tx('尚无人工处置记录', 'No human resolution records yet')}</p>}</div></section></div></motion.aside></motion.div>;
}

function FaultReportDrawer({ tx, report, loading, error, lang, close }: { tx: Tx; report: FaultAnalysisReport | null; loading: boolean; error: string; lang: string; close: () => void }) {
  const zh = lang.startsWith('zh');
  const local = (value?: LocalizedText) => value ? (zh ? value.zh : value.en) : '—';
  return <motion.div className="drawer-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}>
    <button className="drawer-bg" onClick={close} aria-label={tx('关闭报告', 'Close report')} />
    <motion.aside className="drawer fault-report-drawer" initial={{ x: '100%' }} animate={{ x: 0 }} exit={{ x: '100%' }}>
      <header><div><span>FAULT REPORT / {report?.report_version || 'v0.1'}</span><h2>{report ? local(report.title) : tx('结构化故障报告', 'Structured Fault Report')}</h2></div><button className="icon-btn" onClick={close} aria-label={tx('关闭', 'Close')}><X size={17} /></button></header>
      <div className="drawer-body">
        {loading && <Empty tx={tx} title={tx('正在聚合只读证据', 'Aggregating read-only evidence')} />}
        {error && <div className="report-error"><AlertTriangle size={18} /><b>{tx('报告生成失败', 'Report generation failed')}</b><span>{error}</span></div>}
        {report && <>
          <div className="report-banner"><div><ShieldCheck size={18} /><span><b>{tx('只读确定性分析', 'Read-only deterministic analysis')}</b><small>{tx('仅聚合 Atlas 已有证据；未执行节点、任务、配置或重启操作。', 'Aggregates existing Atlas evidence only; no node, workload, configuration, or restart action was executed.')}</small></span></div><div><Badge value="DETERMINISTIC" kind="info" /><Badge value="READ-ONLY" kind="healthy" /><Badge value={report.no_action_executed ? 'NO ACTION EXECUTED' : 'ACTION UNKNOWN'} kind={report.no_action_executed ? 'healthy' : 'warning'} /></div></div>
          <div className="drawer-meta"><Badge value={report.severity.toUpperCase()} kind={tone(report.severity)} /><Badge value={report.analysis_mode.toUpperCase()} kind="info" /><time>{time(report.generated_at, lang)}</time></div>
          <div className="detail-grid">{[[tx('节点', 'Node'), report.affected_entity.node_ip], ['GPU', String(report.affected_entity.gpu_index)], ['GPU UUID', report.affected_entity.gpu_uuid], [tx('型号', 'Model'), report.affected_entity.model_name]].map(item => <div key={item[0]}><small>{item[0]}</small><b>{item[1] || '—'}</b></div>)}</div>
          <section><CardHead code="SUMMARY" title={tx('报告摘要', 'Executive Summary')} /><p className="muted">{local(report.summary)}</p></section>
          <section><CardHead code="FINDINGS" title={tx('确定性发现', 'Deterministic Findings')} action={<Badge value={`${report.findings.length} ITEMS`} kind="info" />} /><div className="report-items">{report.findings.map(item => <article key={item.code}><header><code>{item.code}</code><Badge value={item.severity.toUpperCase()} kind={tone(item.severity)} /></header><p>{local(item.summary)}</p><small>{item.evidence_ids.join(' · ')}</small></article>)}</div></section>
          <section><CardHead code="HYPOTHESES" title={tx('根因假设', 'Root-cause Hypotheses')} /><div className="report-items">{report.hypotheses.map(item => <article key={item.code}><header><b>{local(item.title)}</b><Badge value={item.status.toUpperCase()} kind={item.status === 'supported' ? 'healthy' : 'warning'} /></header><p>{local(item.reason)}</p><small>{item.evidence_ids.join(' · ')}</small></article>)}</div></section>
          <section><CardHead code="TIMELINE" title={tx('证据时间线', 'Evidence Timeline')} /><div className="report-timeline">{report.timeline.map(item => <div key={`${item.at}-${item.evidence_id}`}><time>{time(item.at, lang)}</time><i /><span><b>{local(item.label)}</b><small>{item.evidence_id}</small></span></div>)}</div></section>
          <section><CardHead code="EVIDENCE GAPS" title={tx('缺失证据', 'Missing Evidence')} action={<Badge value={`${report.missing_evidence.length} GAPS`} kind={report.missing_evidence.length ? 'warning' : 'healthy'} />} /><div className="report-gaps">{report.missing_evidence.map(item => <span key={item.code}><code>{item.code}</code>{local(item.detail)}</span>)}</div></section>
          <section><CardHead code="READ-ONLY CHECKS" title={tx('建议只读检查', 'Recommended Read-only Checks')} /><ol className="report-list">{report.recommended_readonly_checks.map((item, index) => <li key={`${item.en}-${index}`}>{local(item)}</li>)}</ol></section>
          <section><CardHead code="OPERATOR" title={tx('人工后续动作', 'Operator Follow-up')} /><ol className="report-list">{report.operator_actions.map((item, index) => <li key={`${item.en}-${index}`}>{local(item)}</li>)}</ol></section>
          <section><CardHead code="LIMITATIONS" title={tx('报告边界', 'Report Limitations')} /><ul className="report-list">{report.limitations.map((item, index) => <li key={`${item.en}-${index}`}>{local(item)}</li>)}</ul></section>
        </>}
      </div>
    </motion.aside>
  </motion.div>;
}

function Drawer({ tx, item, report, lang, close }: { tx: Tx; item: Ingestion; report: Report | null; lang: string; close: () => void }) {
  return <motion.div className="drawer-layer" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}><button className="drawer-bg" onClick={close} /><motion.aside className="drawer" initial={{ x: '100%' }} animate={{ x: 0 }} exit={{ x: '100%' }}><header><div><span>INCIDENT / #{item.id}</span><h2>{item.message}</h2></div><button className="icon-btn" onClick={close}><X size={17} /></button></header><div className="drawer-body"><div className="drawer-meta"><Badge value={item.level} kind={tone(item.level)} /><span>{item.host || '—'}</span><time>{time(item.event_timestamp || item.created_at, lang)} · {item.event_timestamp ? tx('源事件时间', 'source event') : tx('接收时间', 'received')}</time></div><div className="detail-grid">{[['GPU UUID', item.labels?.UUID || item.labels?.uuid], ['PCI BUS ID', item.labels?.pci_bus_id], ['DEVICE', item.labels?.device || item.labels?.gpu], ['MODEL', item.labels?.modelName || item.labels?.model]].map(x => <div key={x[0]}><small>{x[0]}</small><b>{x[1] || '—'}</b></div>)}</div><section><CardHead code="ANALYSIS" title={tx('分析', 'Analysis')} action={report?.confidence != null ? <Badge value={`${Math.round(report.confidence * 100)}%`} kind="info" /> : undefined} />{report ? <div className="report"><p>{report.summary}</p>{report.probable_causes?.length ? <><b>{tx('可能原因', 'Probable causes')}</b><ul>{report.probable_causes.map(x => <li key={x}>{x}</li>)}</ul></> : null}{report.recommended_actions?.length ? <><b>{tx('处理建议', 'Actions')}</b><ol>{report.recommended_actions.map(x => <li key={x}>{x}</li>)}</ol></> : null}<small>{report.model} · DRAFT</small></div> : <p className="muted">{tx('无分析结果', 'No analysis result')}</p>}</section><section><CardHead code="DETAILS" title={tx('标签', 'Labels')} /><div className="labels">{Object.entries(item.labels || {}).map(([k, v]) => <div key={k}><small>{k}</small><b>{v}</b></div>)}</div></section><section><CardHead code="RAW" title="Payload" /><pre>{item.raw_payload || '—'}</pre></section></div></motion.aside></motion.div>;
}

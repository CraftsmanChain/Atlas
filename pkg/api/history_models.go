package api

import "time"

// MonitoringHistoryAudit records one bounded, read-only capability and
// coverage inspection against a historical metrics source. It stores metadata
// only; raw Prometheus samples are never copied into the Atlas database.
type MonitoringHistoryAudit struct {
	ID                     uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	SourceKey              string     `json:"source_key" gorm:"index;not null"`
	SourceName             string     `json:"source_name" gorm:"not null"`
	SourceType             string     `json:"source_type" gorm:"index;not null"`
	BaseURL                string     `json:"base_url" gorm:"type:text;not null"`
	Status                 string     `json:"status" gorm:"index;not null"`
	SourceVersion          string     `json:"source_version,omitempty"`
	ConfiguredRetention    string     `json:"configured_retention,omitempty"`
	DCGMTargetCount        int        `json:"dcgm_target_count"`
	GPUExporterTargetCount int        `json:"gpu_exporter_target_count"`
	CurrentGPUSeries       int        `json:"current_gpu_series"`
	ScrapeIntervalSeconds  float64    `json:"scrape_interval_seconds,omitempty"`
	MetricFamilies         StringList `json:"metric_families" gorm:"type:text"`
	RequiredMetricFamilies StringList `json:"required_metric_families" gorm:"type:text"`
	MissingMetricFamilies  StringList `json:"missing_metric_families" gorm:"type:text"`
	Capabilities           StringList `json:"capabilities" gorm:"type:text"`
	Details                StringMap  `json:"details" gorm:"type:text"`
	ErrorMessage           string     `json:"error_message,omitempty" gorm:"type:text"`
	EarliestSampleAt       *time.Time `json:"earliest_sample_at,omitempty" gorm:"index"`
	LatestSampleAt         *time.Time `json:"latest_sample_at,omitempty" gorm:"index"`
	StartedAt              time.Time  `json:"started_at" gorm:"index;not null"`
	FinishedAt             time.Time  `json:"finished_at" gorm:"index;not null"`
	CreatedAt              time.Time  `json:"created_at"`
}

// HistoryBackfillRun is the durable progress envelope for a bounded historical
// scan. The implementation reconstructs sparse GPU fault-signal onsets and
// does not export full-resolution telemetry.
type HistoryBackfillRun struct {
	ID                uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	SourceKey         string     `json:"source_key" gorm:"index;not null"`
	JobType           string     `json:"job_type" gorm:"index;not null"`
	Status            string     `json:"status" gorm:"index;not null"`
	QueryVersion      string     `json:"query_version" gorm:"index;not null"`
	RangeStart        time.Time  `json:"range_start" gorm:"index;not null"`
	RangeEnd          time.Time  `json:"range_end" gorm:"index;not null"`
	StepSeconds       int        `json:"step_seconds"`
	ChunkHours        int        `json:"chunk_hours"`
	ChunksTotal       int        `json:"chunks_total"`
	ChunksCompleted   int        `json:"chunks_completed"`
	SeriesScanned     int        `json:"series_scanned"`
	SignalPoints      int        `json:"signal_points"`
	CandidatesCreated int        `json:"candidates_created"`
	CandidatesUpdated int        `json:"candidates_updated"`
	RecordsCreated    int        `json:"records_created"`
	RecordsUpdated    int        `json:"records_updated"`
	RecordsAnnotated  int        `json:"records_annotated"`
	ErrorMessage      string     `json:"error_message,omitempty" gorm:"type:text"`
	StartedAt         time.Time  `json:"started_at" gorm:"index;not null"`
	FinishedAt        *time.Time `json:"finished_at,omitempty" gorm:"index"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// HistoricalFaultCandidate is a reviewable incident candidate reconstructed
// from historical monitoring state. A candidate is never a confirmed training
// label until an operator or repair record validates it.
type HistoricalFaultCandidate struct {
	ID                     uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	CandidateKey           string     `json:"candidate_key" gorm:"uniqueIndex;size:64;not null"`
	SourceKey              string     `json:"source_key" gorm:"index;not null"`
	BackfillRunID          uint       `json:"backfill_run_id" gorm:"index;not null"`
	EntityType             string     `json:"entity_type" gorm:"index;not null"`
	GPUUUID                string     `json:"gpu_uuid" gorm:"column:gpu_uuid;index"`
	NodeIP                 string     `json:"node_ip" gorm:"index"`
	Hostname               string     `json:"hostname" gorm:"index"`
	ModelName              string     `json:"model_name" gorm:"index"`
	PCIBusID               string     `json:"pci_bus_id" gorm:"index"`
	EventType              string     `json:"event_type" gorm:"index;not null"`
	EventCode              string     `json:"event_code" gorm:"index"`
	EventMessage           string     `json:"event_message" gorm:"type:text"`
	Severity               string     `json:"severity" gorm:"index"`
	QualityTier            string     `json:"quality_tier" gorm:"index;not null"`
	OperationalPriority    string     `json:"operational_priority" gorm:"index;not null;default:'unclassified'"`
	HardwareCertainty      string     `json:"hardware_certainty" gorm:"index;not null;default:'unclassified'"`
	TrainingDisposition    string     `json:"training_disposition" gorm:"index;not null;default:'pending_review'"`
	RecommendedAction      string     `json:"recommended_action" gorm:"type:text"`
	RecoveryAware          bool       `json:"recovery_aware" gorm:"index;not null;default:false"`
	ReviewStatus           string     `json:"review_status" gorm:"index;not null"`
	SourceMetric           string     `json:"source_metric" gorm:"index;not null"`
	SourceAlertName        string     `json:"source_alert_name" gorm:"index"`
	SignalSamples          int        `json:"signal_samples"`
	Labels                 StringMap  `json:"labels" gorm:"type:text"`
	OnsetAt                time.Time  `json:"onset_at" gorm:"index;not null"`
	DetectionWindowEndAt   time.Time  `json:"detection_window_end_at" gorm:"index;not null"`
	ReviewedAt             *time.Time `json:"reviewed_at,omitempty" gorm:"index"`
	ReviewedBy             string     `json:"reviewed_by,omitempty" gorm:"index"`
	ReviewNote             string     `json:"review_note,omitempty" gorm:"type:text"`
	IdentityEvidenceStatus string     `json:"identity_evidence_status,omitempty" gorm:"index"`
	IdentityIntervalID     uint       `json:"identity_interval_id,omitempty" gorm:"index"`
	IdentityEvidence       StringMap  `json:"identity_evidence,omitempty" gorm:"type:text"`
	RuleDecision           string     `json:"rule_decision,omitempty" gorm:"index"`
	RuleDecisionVersion    string     `json:"rule_decision_version,omitempty" gorm:"index"`
	RuleDecisionReason     string     `json:"rule_decision_reason,omitempty" gorm:"type:text"`
	RuleConfidence         float64    `json:"rule_confidence,omitempty"`
	RuleDecidedAt          *time.Time `json:"rule_decided_at,omitempty" gorm:"index"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// HistoricalGPUIdentityInterval is a compressed presence interval rebuilt
// from sparse Prometheus observations. It keeps identity evidence rather than
// copying full-resolution telemetry into Atlas.
type HistoricalGPUIdentityInterval struct {
	ID               uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	IntervalKey      string     `json:"interval_key" gorm:"uniqueIndex;size:64;not null"`
	SourceKey        string     `json:"source_key" gorm:"index;not null"`
	BackfillRunID    uint       `json:"backfill_run_id" gorm:"index;not null"`
	NodeIP           string     `json:"node_ip" gorm:"index;not null"`
	HostID           string     `json:"host_id" gorm:"index"`
	HostSerial       string     `json:"host_serial" gorm:"index"`
	Hostname         string     `json:"hostname" gorm:"index"`
	DataCenterID     string     `json:"data_center_id" gorm:"index"`
	GPUIndex         int        `json:"gpu_index" gorm:"column:gpu_index;index"`
	GPUUUID          string     `json:"gpu_uuid" gorm:"column:gpu_uuid;index;not null"`
	PCIBusID         string     `json:"pci_bus_id" gorm:"index"`
	ModelName        string     `json:"model_name" gorm:"index"`
	DriverVersion    string     `json:"driver_version" gorm:"index"`
	FirstSeenAt      time.Time  `json:"first_seen_at" gorm:"index;not null"`
	LastSeenAt       time.Time  `json:"last_seen_at" gorm:"index;not null"`
	ObservationCount int        `json:"observation_count"`
	TransitionType   string     `json:"transition_type" gorm:"index"`
	PredecessorUUID  string     `json:"predecessor_uuid,omitempty" gorm:"column:predecessor_uuid;index"`
	TransitionAt     *time.Time `json:"transition_at,omitempty" gorm:"index"`
	EvidenceStrength string     `json:"evidence_strength" gorm:"index"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func (HistoricalGPUIdentityInterval) TableName() string {
	return "historical_gpu_identity_intervals"
}

// TrainingDatasetBuild records a versioned point-in-time cohort manifest
// written on the Atlas deployment node. Raw Prometheus samples are not stored
// in this table.
type TrainingDatasetBuild struct {
	ID                     uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	DatasetKey             string     `json:"dataset_key" gorm:"uniqueIndex;not null"`
	Version                string     `json:"version" gorm:"index;not null"`
	Status                 string     `json:"status" gorm:"index;not null"`
	SourceKey              string     `json:"source_key" gorm:"index;not null"`
	Horizons               StringList `json:"horizons" gorm:"type:text"`
	CandidateCount         int        `json:"candidate_count"`
	EligibleCandidateCount int        `json:"eligible_candidate_count"`
	EpisodeCount           int        `json:"episode_count"`
	WindowCount            int        `json:"window_count"`
	PendingReviewCount     int        `json:"pending_review_count"`
	IdentityMissingCount   int        `json:"identity_missing_count"`
	ContextOnlyCount       int        `json:"context_only_count"`
	ExcludedCount          int        `json:"excluded_count"`
	OutputDir              string     `json:"output_dir" gorm:"type:text"`
	ManifestPath           string     `json:"manifest_path" gorm:"type:text"`
	WindowManifestPath     string     `json:"window_manifest_path" gorm:"type:text"`
	WindowManifestSHA256   string     `json:"window_manifest_sha256"`
	ErrorMessage           string     `json:"error_message,omitempty" gorm:"type:text"`
	StartedAt              time.Time  `json:"started_at" gorm:"index;not null"`
	FinishedAt             *time.Time `json:"finished_at,omitempty" gorm:"index"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// TrainingFeatureBuild records an asynchronous, point-in-time extraction from
// a cohort manifest. Feature artifacts remain on the Atlas deployment node.
type TrainingFeatureBuild struct {
	ID                     uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	FeatureDatasetKey      string     `json:"feature_dataset_key" gorm:"uniqueIndex;not null"`
	Version                string     `json:"version" gorm:"index;not null"`
	Status                 string     `json:"status" gorm:"index;not null"`
	SourceKey              string     `json:"source_key" gorm:"index;not null"`
	SourceDatasetBuildID   uint       `json:"source_dataset_build_id" gorm:"index;not null"`
	SourceDatasetKey       string     `json:"source_dataset_key" gorm:"index;not null"`
	FeatureContractVersion string     `json:"feature_contract_version" gorm:"index;not null"`
	LookbackMinutes        int        `json:"lookback_minutes"`
	QueryStepSeconds       int        `json:"query_step_seconds"`
	EpisodeCount           int        `json:"episode_count"`
	WindowCount            int        `json:"window_count"`
	ProcessedEpisodes      int        `json:"processed_episodes"`
	CompletedWindows       int        `json:"completed_windows"`
	FailedWindows          int        `json:"failed_windows"`
	MetricCount            int        `json:"metric_count"`
	FeatureColumnCount     int        `json:"feature_column_count"`
	AverageMetricCoverage  float64    `json:"average_metric_coverage"`
	MinimumMetricCoverage  float64    `json:"minimum_metric_coverage"`
	OutputDir              string     `json:"output_dir" gorm:"type:text"`
	FeaturePath            string     `json:"feature_path" gorm:"type:text"`
	FeatureSHA256          string     `json:"feature_sha256"`
	QualityReportPath      string     `json:"quality_report_path" gorm:"type:text"`
	ErrorMessage           string     `json:"error_message,omitempty" gorm:"type:text"`
	StartedAt              time.Time  `json:"started_at" gorm:"index;not null"`
	FinishedAt             *time.Time `json:"finished_at,omitempty" gorm:"index"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// TrainingPreparationBuild records quality gating, leakage-safe split
// assignment, and healthy-control extraction requests derived from one full
// historical feature dataset.
type TrainingPreparationBuild struct {
	ID                         uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	PreparedDatasetKey         string     `json:"prepared_dataset_key" gorm:"uniqueIndex;not null"`
	Version                    string     `json:"version" gorm:"index;not null"`
	Status                     string     `json:"status" gorm:"index;not null"`
	SourceFeatureBuildID       uint       `json:"source_feature_build_id" gorm:"index;not null"`
	SourceFeatureDatasetKey    string     `json:"source_feature_dataset_key" gorm:"index;not null"`
	MinimumMetricCoverage      float64    `json:"minimum_metric_coverage"`
	SourceWindowCount          int        `json:"source_window_count"`
	EligiblePositiveCount      int        `json:"eligible_positive_count"`
	TelemetryCensoredCount     int        `json:"telemetry_censored_count"`
	LowCoverageCount           int        `json:"low_coverage_count"`
	ExtractionFailedCount      int        `json:"extraction_failed_count"`
	PositiveDiscontinuousCount int        `json:"positive_discontinuous_count"`
	LabelIneligibleCount       int        `json:"label_ineligible_count"`
	CorrelatedEventCount       int        `json:"correlated_event_count"`
	EntityTimeConflictCount    int        `json:"entity_time_conflict_count"`
	TrainCount                 int        `json:"train_count"`
	ValidationCount            int        `json:"validation_count"`
	TestCount                  int        `json:"test_count"`
	ControlRequestCount        int        `json:"control_request_count"`
	ControlShortfallCount      int        `json:"control_shortfall_count"`
	TrainEndAt                 *time.Time `json:"train_end_at,omitempty" gorm:"index"`
	ValidationEndAt            *time.Time `json:"validation_end_at,omitempty" gorm:"index"`
	OutputDir                  string     `json:"output_dir" gorm:"type:text"`
	ManifestPath               string     `json:"manifest_path" gorm:"type:text"`
	PreparedSamplesPath        string     `json:"prepared_samples_path" gorm:"type:text"`
	PreparedSamplesSHA256      string     `json:"prepared_samples_sha256"`
	ControlRequestsPath        string     `json:"control_requests_path" gorm:"type:text"`
	ControlRequestsSHA256      string     `json:"control_requests_sha256"`
	ErrorMessage               string     `json:"error_message,omitempty" gorm:"type:text"`
	StartedAt                  time.Time  `json:"started_at" gorm:"index;not null"`
	FinishedAt                 *time.Time `json:"finished_at,omitempty" gorm:"index"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
}

// TrainingControlFeatureBuild records extraction and eligibility checks for
// same-GPU historical healthy-control requests.
type TrainingControlFeatureBuild struct {
	ID                       uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	ControlFeatureDatasetKey string     `json:"control_feature_dataset_key" gorm:"uniqueIndex;not null"`
	Version                  string     `json:"version" gorm:"index;not null"`
	Status                   string     `json:"status" gorm:"index;not null"`
	SourcePreparationBuildID uint       `json:"source_preparation_build_id" gorm:"index;not null"`
	SourcePreparedDatasetKey string     `json:"source_prepared_dataset_key" gorm:"index;not null"`
	SourceKey                string     `json:"source_key" gorm:"index;not null"`
	FeatureContractVersion   string     `json:"feature_contract_version" gorm:"index;not null"`
	RequestCount             int        `json:"request_count"`
	UniqueWindowCount        int        `json:"unique_window_count"`
	ProcessedUniqueWindows   int        `json:"processed_unique_windows"`
	CompletedRequestCount    int        `json:"completed_request_count"`
	EligibleRequestCount     int        `json:"eligible_request_count"`
	TelemetryCensoredCount   int        `json:"telemetry_censored_count"`
	LowCoverageCount         int        `json:"low_coverage_count"`
	DiscontinuousCount       int        `json:"discontinuous_count"`
	LoadUnknownCount         int        `json:"load_unknown_count"`
	LoadMismatchCount        int        `json:"load_mismatch_count"`
	ExtractionFailedCount    int        `json:"extraction_failed_count"`
	OutputDir                string     `json:"output_dir" gorm:"type:text"`
	FeaturePath              string     `json:"feature_path" gorm:"type:text"`
	FeatureSHA256            string     `json:"feature_sha256"`
	QualityReportPath        string     `json:"quality_report_path" gorm:"type:text"`
	ErrorMessage             string     `json:"error_message,omitempty" gorm:"type:text"`
	StartedAt                time.Time  `json:"started_at" gorm:"index;not null"`
	FinishedAt               *time.Time `json:"finished_at,omitempty" gorm:"index"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

// TrainingMatrixBuild records the immutable, leakage-audited supervised
// learning matrix assembled from eligible positives and healthy controls.
type TrainingMatrixBuild struct {
	ID                        uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	TrainingMatrixKey         string     `json:"training_matrix_key" gorm:"uniqueIndex;not null"`
	Version                   string     `json:"version" gorm:"index;not null"`
	Status                    string     `json:"status" gorm:"index;not null"`
	SourcePreparationBuildID  uint       `json:"source_preparation_build_id" gorm:"index;not null"`
	SourcePreparedDatasetKey  string     `json:"source_prepared_dataset_key" gorm:"index;not null"`
	SourceControlBuildID      uint       `json:"source_control_build_id" gorm:"index;not null"`
	SourceControlDatasetKey   string     `json:"source_control_dataset_key" gorm:"index;not null"`
	FeatureContractVersion    string     `json:"feature_contract_version" gorm:"index;not null"`
	FeatureColumnCount        int        `json:"feature_column_count"`
	PositiveCount             int        `json:"positive_count"`
	ControlCount              int        `json:"control_count"`
	SampleCount               int        `json:"sample_count"`
	TrainPositiveCount        int        `json:"train_positive_count"`
	TrainControlCount         int        `json:"train_control_count"`
	ValidationPositiveCount   int        `json:"validation_positive_count"`
	ValidationControlCount    int        `json:"validation_control_count"`
	TestPositiveCount         int        `json:"test_positive_count"`
	TestControlCount          int        `json:"test_control_count"`
	DuplicateCount            int        `json:"duplicate_count"`
	EntitySplitConflictCount  int        `json:"entity_split_conflict_count"`
	PointInTimeViolationCount int        `json:"point_in_time_violation_count"`
	PairingViolationCount     int        `json:"pairing_violation_count"`
	ContractViolationCount    int        `json:"contract_violation_count"`
	OutputDir                 string     `json:"output_dir" gorm:"type:text"`
	MatrixPath                string     `json:"matrix_path" gorm:"type:text"`
	MatrixSHA256              string     `json:"matrix_sha256"`
	ManifestPath              string     `json:"manifest_path" gorm:"type:text"`
	ErrorMessage              string     `json:"error_message,omitempty" gorm:"type:text"`
	StartedAt                 time.Time  `json:"started_at" gorm:"index;not null"`
	FinishedAt                *time.Time `json:"finished_at,omitempty" gorm:"index"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`
}

// BaselineModelBuild records offline-only supervised baseline training from an
// immutable training matrix. It never enables online probability emission.
type BaselineModelBuild struct {
	ID                       uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	BaselineModelKey         string     `json:"baseline_model_key" gorm:"uniqueIndex;not null"`
	Version                  string     `json:"version" gorm:"index;not null"`
	Status                   string     `json:"status" gorm:"index;not null"`
	Algorithm                string     `json:"algorithm" gorm:"index;not null"`
	SourceMatrixBuildID      uint       `json:"source_matrix_build_id" gorm:"index;not null"`
	SourceTrainingMatrixKey  string     `json:"source_training_matrix_key" gorm:"index;not null"`
	FeatureContractVersion   string     `json:"feature_contract_version" gorm:"index;not null"`
	ScopeEventType           string     `json:"scope_event_type,omitempty" gorm:"index"`
	ScopeModelName           string     `json:"scope_model_name,omitempty" gorm:"index"`
	ReadinessGateVersion     string     `json:"readiness_gate_version,omitempty" gorm:"index"`
	FeatureColumnCount       int        `json:"feature_column_count"`
	FeatureAuditStatus       string     `json:"feature_audit_status,omitempty" gorm:"index"`
	ExcludedFeatureCount     int        `json:"excluded_feature_count"`
	ProhibitedFeatureCount   int        `json:"prohibited_feature_count"`
	StatisticallyStableCount int        `json:"statistically_stable_count"`
	ShadowCandidateCount     int        `json:"shadow_candidate_count"`
	HorizonCount             int        `json:"horizon_count"`
	TrainedModelCount        int        `json:"trained_model_count"`
	TrainCount               int        `json:"train_count"`
	ValidationCount          int        `json:"validation_count"`
	TestCount                int        `json:"test_count"`
	TestMacroROCAUC          float64    `json:"test_macro_roc_auc" gorm:"column:test_macro_roc_auc"`
	TestMacroPRAUC           float64    `json:"test_macro_pr_auc" gorm:"column:test_macro_pr_auc"`
	TestMacroPrecision       float64    `json:"test_macro_precision"`
	TestMacroRecall          float64    `json:"test_macro_recall"`
	OutputDir                string     `json:"output_dir" gorm:"type:text"`
	ArtifactPath             string     `json:"artifact_path" gorm:"type:text"`
	ArtifactSHA256           string     `json:"artifact_sha256"`
	ReportPath               string     `json:"report_path" gorm:"type:text"`
	ErrorMessage             string     `json:"error_message,omitempty" gorm:"type:text"`
	StartedAt                time.Time  `json:"started_at" gorm:"index;not null"`
	FinishedAt               *time.Time `json:"finished_at,omitempty" gorm:"index"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

// PredictionFeatureReplayRun compares immutable matrix feature values with a
// fresh read-only Prometheus extraction at the same historical cutoff. Passing
// this audit verifies extractor parity only; live coverage remains a separate
// release gate.
type PredictionFeatureReplayRun struct {
	ID                            uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	ReplayKey                     string     `json:"replay_key" gorm:"uniqueIndex;not null"`
	Version                       string     `json:"version" gorm:"index;not null"`
	Status                        string     `json:"status" gorm:"index;not null"`
	ModelSpecID                   uint       `json:"model_spec_id" gorm:"index;not null"`
	ModelKey                      string     `json:"model_key" gorm:"index;not null"`
	ModelVersion                  string     `json:"model_version" gorm:"index;not null"`
	SourceBaselineBuildID         uint       `json:"source_baseline_build_id" gorm:"index;not null"`
	SourceMatrixBuildID           uint       `json:"source_matrix_build_id" gorm:"index;not null"`
	SourceKey                     string     `json:"source_key" gorm:"index;not null"`
	TransformationContractVersion string     `json:"transformation_contract_version" gorm:"index;not null"`
	RequestedSampleCount          int        `json:"requested_sample_count"`
	SelectedSampleCount           int        `json:"selected_sample_count"`
	CompletedSampleCount          int        `json:"completed_sample_count"`
	FailedSampleCount             int        `json:"failed_sample_count"`
	TrainingFeatureCount          int        `json:"training_feature_count"`
	ComparedValueCount            int        `json:"compared_value_count"`
	VerifiedColumnCount           int        `json:"verified_column_count"`
	MismatchCount                 int        `json:"mismatch_count"`
	MissingTrainingValueCount     int        `json:"missing_training_value_count"`
	MissingReplayValueCount       int        `json:"missing_replay_value_count"`
	MaximumAbsoluteError          float64    `json:"maximum_absolute_error"`
	MaximumRelativeError          float64    `json:"maximum_relative_error"`
	FailedColumns                 StringList `json:"failed_columns" gorm:"type:text"`
	BlockingReasons               StringList `json:"blocking_reasons" gorm:"type:text"`
	OutputDir                     string     `json:"output_dir" gorm:"type:text"`
	ReportPath                    string     `json:"report_path" gorm:"type:text"`
	ReportSHA256                  string     `json:"report_sha256"`
	ErrorMessage                  string     `json:"error_message,omitempty" gorm:"type:text"`
	StartedAt                     time.Time  `json:"started_at" gorm:"index;not null"`
	FinishedAt                    *time.Time `json:"finished_at,omitempty" gorm:"index"`
	CreatedAt                     time.Time  `json:"created_at"`
	UpdatedAt                     time.Time  `json:"updated_at"`
}

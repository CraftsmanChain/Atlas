package api

import "time"

// PredictionModelSpec is the immutable contract for one hardware failure
// prediction horizon. A registered spec does not imply that a trained model or
// calibrated probability is available.
type PredictionModelSpec struct {
	ID                     uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	ModelKey               string     `json:"model_key" gorm:"uniqueIndex:idx_prediction_model_version,priority:1;index;not null"`
	Version                string     `json:"version" gorm:"uniqueIndex:idx_prediction_model_version,priority:2;index;not null"`
	HardwareClass          string     `json:"hardware_class" gorm:"index;not null"`
	EntityType             string     `json:"entity_type" gorm:"index;not null"`
	Task                   string     `json:"task" gorm:"index;not null"`
	HorizonMinutes         int        `json:"horizon_minutes" gorm:"index;not null"`
	Algorithm              string     `json:"algorithm" gorm:"index;not null"`
	Runtime                string     `json:"runtime" gorm:"index;not null"`
	Mode                   string     `json:"mode" gorm:"index;not null"`
	Status                 string     `json:"status" gorm:"index;not null"`
	FeatureContractVersion string     `json:"feature_contract_version" gorm:"index;not null"`
	LabelContractVersion   string     `json:"label_contract_version" gorm:"index;not null"`
	DatasetVersion         string     `json:"dataset_version,omitempty" gorm:"index"`
	SourceBaselineBuildID  uint       `json:"source_baseline_build_id,omitempty" gorm:"index"`
	ScopeEventType         string     `json:"scope_event_type,omitempty" gorm:"index"`
	ScopeModelName         string     `json:"scope_model_name,omitempty" gorm:"index"`
	ArtifactURI            string     `json:"artifact_uri,omitempty" gorm:"type:text"`
	ArtifactSHA256         string     `json:"artifact_sha256,omitempty"`
	RegistryGateVersion    string     `json:"registry_gate_version,omitempty" gorm:"index"`
	DecisionThreshold      *float64   `json:"decision_threshold,omitempty"`
	Current                bool       `json:"current" gorm:"index"`
	TrainedAt              *time.Time `json:"trained_at,omitempty" gorm:"index"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// FailureLabel preserves the provenance and quality tier of a training label.
// Weak rule-derived labels must never be silently treated as confirmed repairs.
type FailureLabel struct {
	ID                       uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	LabelKey                 string     `json:"label_key" gorm:"uniqueIndex;not null"`
	HardwareClass            string     `json:"hardware_class" gorm:"index;not null"`
	EntityType               string     `json:"entity_type" gorm:"index;not null"`
	EntityKey                string     `json:"entity_key" gorm:"index;not null"`
	GPUAssetID               uint       `json:"gpu_asset_id,omitempty" gorm:"index"`
	GPUUUID                  string     `json:"gpu_uuid,omitempty" gorm:"column:gpu_uuid;index"`
	NodeIP                   string     `json:"node_ip,omitempty" gorm:"index"`
	ModelName                string     `json:"model_name,omitempty" gorm:"index"`
	EventType                string     `json:"event_type" gorm:"index;not null"`
	RuleVersion              string     `json:"rule_version,omitempty" gorm:"index"`
	LabelValue               int        `json:"label_value" gorm:"index;not null"`
	QualityTier              string     `json:"quality_tier" gorm:"index;not null"`
	SourceType               string     `json:"source_type" gorm:"index;not null"`
	SourceRecordID           uint       `json:"source_record_id" gorm:"index"`
	ConfirmationResolutionID uint       `json:"confirmation_resolution_id,omitempty" gorm:"index"`
	LabelContractVersion     string     `json:"label_contract_version" gorm:"index;not null"`
	OccurredAt               time.Time  `json:"occurred_at" gorm:"index;not null"`
	AvailableAt              time.Time  `json:"available_at" gorm:"index;not null"`
	ConfirmedAt              *time.Time `json:"confirmed_at,omitempty" gorm:"index"`
	Excluded                 bool       `json:"excluded" gorm:"index"`
	ExclusionReason          string     `json:"exclusion_reason,omitempty" gorm:"type:text"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

// HardwareRiskPrediction is an append-only, model-versioned inference result.
// A probability may be persisted only by the gated read-only shadow runtime or
// a later released runtime; status and provenance keep those modes distinct.
type HardwareRiskPrediction struct {
	ID                uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	ShadowRunID       uint       `json:"shadow_run_id,omitempty" gorm:"index"`
	ModelSpecID       uint       `json:"model_spec_id" gorm:"index;not null"`
	ModelVersion      string     `json:"model_version,omitempty" gorm:"index"`
	HardwareClass     string     `json:"hardware_class" gorm:"index;not null"`
	EntityType        string     `json:"entity_type" gorm:"index;not null"`
	EntityKey         string     `json:"entity_key" gorm:"index;not null"`
	GPUAssetID        uint       `json:"gpu_asset_id,omitempty" gorm:"index"`
	GPUUUID           string     `json:"gpu_uuid,omitempty" gorm:"column:gpu_uuid;index"`
	NodeIP            string     `json:"node_ip,omitempty" gorm:"index"`
	FeatureSnapshotID uint       `json:"feature_snapshot_id" gorm:"index"`
	FeatureVectorSHA  string     `json:"feature_vector_sha256,omitempty" gorm:"index"`
	TransformVersion  string     `json:"transformation_contract_version,omitempty" gorm:"index"`
	HorizonMinutes    int        `json:"horizon_minutes" gorm:"index;not null"`
	Probability       *float64   `json:"probability,omitempty"`
	RiskLevel         string     `json:"risk_level" gorm:"index;not null"`
	Status            string     `json:"status" gorm:"index;not null"`
	Explanations      StringList `json:"explanations" gorm:"type:text"`
	Current           bool       `json:"current" gorm:"index"`
	ObservedAt        time.Time  `json:"observed_at" gorm:"index"`
	EvaluatedAt       time.Time  `json:"evaluated_at" gorm:"index"`
	ExpiresAt         time.Time  `json:"expires_at" gorm:"index"`
	CreatedAt         time.Time  `json:"created_at"`
}

// PredictionFeatureParityAudit proves that every trained model column maps to
// an active online source and the exact shared transformation implementation.
// Contract parity alone never enables scoring; historical replay must verify
// actual values first.
type PredictionFeatureParityAudit struct {
	ID                            uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	ModelSpecID                   uint       `json:"model_spec_id" gorm:"uniqueIndex;not null"`
	ModelKey                      string     `json:"model_key" gorm:"index;not null"`
	ModelVersion                  string     `json:"model_version" gorm:"index;not null"`
	SourceBaselineBuildID         uint       `json:"source_baseline_build_id" gorm:"index;not null"`
	ArtifactSHA256                string     `json:"artifact_sha256" gorm:"index;not null"`
	FeatureContractVersion        string     `json:"feature_contract_version" gorm:"index;not null"`
	TransformationContractVersion string     `json:"transformation_contract_version" gorm:"index;not null"`
	Status                        string     `json:"status" gorm:"index;not null"`
	TrainingFeatureCount          int        `json:"training_feature_count"`
	ContractMatchedCount          int        `json:"contract_matched_count"`
	SourceMetricCount             int        `json:"source_metric_count"`
	MissingSourceCount            int        `json:"missing_source_count"`
	UnsupportedTransformCount     int        `json:"unsupported_transform_count"`
	ReplayVerifiedCount           int        `json:"replay_verified_count"`
	SourceMetrics                 StringList `json:"source_metrics" gorm:"type:text"`
	ContractMatchedColumns        StringList `json:"contract_matched_columns" gorm:"type:text"`
	MissingSourceColumns          StringList `json:"missing_source_columns" gorm:"type:text"`
	UnsupportedTransformColumns   StringList `json:"unsupported_transform_columns" gorm:"type:text"`
	BlockingReasons               StringList `json:"blocking_reasons" gorm:"type:text"`
	ScoringAllowed                bool       `json:"scoring_allowed" gorm:"index"`
	AuditedAt                     time.Time  `json:"audited_at" gorm:"index;not null"`
	CreatedAt                     time.Time  `json:"created_at"`
	UpdatedAt                     time.Time  `json:"updated_at"`
}

// PredictionOutcomeEvaluation keeps the rule-derived outcome and any later
// human correction side by side. Reconciliation may update the rule columns,
// but must never overwrite the human decision columns.
type PredictionOutcomeEvaluation struct {
	ID                    uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	PredictionID          uint       `json:"prediction_id" gorm:"uniqueIndex;not null"`
	ModelSpecID           uint       `json:"model_spec_id" gorm:"index;not null"`
	ModelKey              string     `json:"model_key" gorm:"index;not null"`
	ModelVersion          string     `json:"model_version" gorm:"index;not null"`
	EntityType            string     `json:"entity_type" gorm:"index;not null"`
	EntityKey             string     `json:"entity_key" gorm:"index;not null"`
	GPUAssetID            uint       `json:"gpu_asset_id,omitempty" gorm:"index"`
	GPUUUID               string     `json:"gpu_uuid,omitempty" gorm:"column:gpu_uuid;index"`
	NodeIP                string     `json:"node_ip,omitempty" gorm:"index"`
	HorizonMinutes        int        `json:"horizon_minutes" gorm:"index;not null"`
	Probability           *float64   `json:"probability,omitempty"`
	DecisionThreshold     *float64   `json:"decision_threshold,omitempty"`
	PredictedPositive     bool       `json:"predicted_positive"`
	PredictionEvaluatedAt time.Time  `json:"prediction_evaluated_at" gorm:"index;not null"`
	WindowStartAt         time.Time  `json:"window_start_at" gorm:"index;not null"`
	WindowEndAt           time.Time  `json:"window_end_at" gorm:"index;not null"`
	MaturityStatus        string     `json:"maturity_status" gorm:"index;not null"`
	MaturityReason        string     `json:"maturity_reason,omitempty" gorm:"type:text"`
	RuleActualValue       *int       `json:"rule_actual_value,omitempty"`
	RuleOutcome           string     `json:"rule_outcome" gorm:"index;not null"`
	RuleLabelID           uint       `json:"rule_label_id,omitempty" gorm:"index"`
	RuleLabelQuality      string     `json:"rule_label_quality,omitempty" gorm:"index"`
	RuleDecisionVersion   string     `json:"rule_decision_version" gorm:"index;not null"`
	RuleDecisionReason    string     `json:"rule_decision_reason,omitempty" gorm:"type:text"`
	HumanActualValue      *int       `json:"human_actual_value,omitempty"`
	HumanOutcome          string     `json:"human_outcome,omitempty" gorm:"index"`
	HumanDecision         string     `json:"human_decision,omitempty" gorm:"index"`
	HumanReason           string     `json:"human_reason,omitempty" gorm:"type:text"`
	HumanDecidedBy        string     `json:"human_decided_by,omitempty" gorm:"index"`
	HumanDecidedAt        *time.Time `json:"human_decided_at,omitempty" gorm:"index"`
	FinalActualValue      *int       `json:"final_actual_value,omitempty"`
	FinalOutcome          string     `json:"final_outcome" gorm:"index;not null"`
	FinalSource           string     `json:"final_source" gorm:"index;not null"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

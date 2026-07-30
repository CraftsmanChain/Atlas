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
	ArtifactURI            string     `json:"artifact_uri,omitempty" gorm:"type:text"`
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
// Probability remains nil until a trained and calibrated model is released.
type HardwareRiskPrediction struct {
	ID                uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	ModelSpecID       uint       `json:"model_spec_id" gorm:"index;not null"`
	HardwareClass     string     `json:"hardware_class" gorm:"index;not null"`
	EntityType        string     `json:"entity_type" gorm:"index;not null"`
	EntityKey         string     `json:"entity_key" gorm:"index;not null"`
	GPUAssetID        uint       `json:"gpu_asset_id,omitempty" gorm:"index"`
	GPUUUID           string     `json:"gpu_uuid,omitempty" gorm:"column:gpu_uuid;index"`
	NodeIP            string     `json:"node_ip,omitempty" gorm:"index"`
	FeatureSnapshotID uint       `json:"feature_snapshot_id" gorm:"index"`
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

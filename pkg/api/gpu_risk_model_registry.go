package api

import "time"

// Candidate records never imply promotion, probability calibration or actions.
type GPURiskModel struct {
	ID                 uint       `json:"id" gorm:"primaryKey"`
	ArtifactSHA        string     `json:"artifact_sha" gorm:"uniqueIndex;size:64;not null"`
	PreprocessorSHA    string     `json:"preprocessor_sha"`
	DatasetSHA         string     `json:"dataset_sha"`
	DatasetManifestSHA string     `json:"dataset_manifest_sha"`
	ParitySHA          string     `json:"parity_sha"`
	Version            string     `json:"version"`
	Target             string     `json:"target"`
	FeatureContract    string     `json:"feature_contract"`
	Status             string     `json:"status"`
	TrainingThrough    time.Time  `json:"training_through"`
	AvailableAt        time.Time  `json:"available_at"`
	Artifact           string     `json:"-" gorm:"type:text;not null"`
	TrainingIdentities StringList `json:"-" gorm:"type:text"`
}

func (GPURiskModel) TableName() string { return "gpu_risk_models" }

type GPURiskModelBatch struct {
	Scorer      string    `json:"scorer"`
	ID          uint      `json:"id" gorm:"primaryKey"`
	RequestSHA  string    `json:"request_sha" gorm:"uniqueIndex;size:64;not null"`
	QueryID     uint      `json:"query_id" gorm:"index"`
	ModelID     uint      `json:"model_id" gorm:"index"`
	ArtifactSHA string    `json:"artifact_sha"`
	ResultSHA   string    `json:"result_sha"`
	MemberCount int       `json:"member_count"`
	Status      string    `json:"status"`
	ErrorCode   string    `json:"error_code"`
	CreatedAt   time.Time `json:"created_at"`
	Input       string    `json:"-" gorm:"type:text"`
}

func (GPURiskModelBatch) TableName() string { return "gpu_risk_model_batches" }

type GPURiskModelScore struct {
	ID              uint       `json:"id" gorm:"primaryKey"`
	BatchID         uint       `json:"batch_id" gorm:"uniqueIndex:gpu_risk_model_score_key;not null"`
	NodeID          uint       `json:"node_id" gorm:"uniqueIndex:gpu_risk_model_score_key;not null"`
	Generation      int        `json:"generation"`
	RawScore        *float64   `json:"raw_score"`
	SignalStatus    string     `json:"signal_status"`
	Rank            int        `json:"rank"`
	TieCount        int        `json:"tie_count"`
	EligibleCount   int        `json:"eligible_count"`
	TopFivePercentK int        `json:"top_five_percent_k"`
	Warnings        StringList `json:"warnings" gorm:"type:text"`
	Blockers        StringList `json:"blockers" gorm:"type:text"`
}

func (GPURiskModelScore) TableName() string { return "gpu_risk_model_scores" }

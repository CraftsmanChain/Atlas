package api

import "time"

// GPUFeatureBaseline is a materialized robust distribution for one catalog
// feature, GPU model and load bucket. It is shared by shadow anomaly,
// risk-ranking, prediction and degradation consumers.
type GPUFeatureBaseline struct {
	ID              uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	ContractVersion string    `json:"contract_version" gorm:"index;uniqueIndex:idx_feature_baseline_scope"`
	FeatureName     string    `json:"feature_name" gorm:"index;uniqueIndex:idx_feature_baseline_scope"`
	FeatureVersion  string    `json:"feature_version" gorm:"index;uniqueIndex:idx_feature_baseline_scope"`
	ModelName       string    `json:"model_name" gorm:"index;uniqueIndex:idx_feature_baseline_scope"`
	LoadBucket      string    `json:"load_bucket" gorm:"index;uniqueIndex:idx_feature_baseline_scope"`
	WindowDays      int       `json:"window_days" gorm:"uniqueIndex:idx_feature_baseline_scope"`
	SampleCount     int       `json:"sample_count"`
	GPUCount        int       `json:"gpu_count"`
	P05             float64   `json:"p05"`
	P50             float64   `json:"p50"`
	P95             float64   `json:"p95"`
	MAD             float64   `json:"mad"`
	Maturity        string    `json:"maturity" gorm:"index"`
	WindowStartedAt time.Time `json:"window_started_at"`
	WindowEndedAt   time.Time `json:"window_ended_at"`
	ComputedAt      time.Time `json:"computed_at" gorm:"index"`
	Owner           string    `json:"owner"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type FeatureBaselineRefreshRun struct {
	ID              uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	Status          string     `json:"status" gorm:"index"`
	ContractVersion string     `json:"contract_version" gorm:"index"`
	WindowDays      int        `json:"window_days"`
	SnapshotCount   int        `json:"snapshot_count"`
	BaselineCount   int        `json:"baseline_count"`
	DurationMillis  int64      `json:"duration_millis"`
	StartedAt       time.Time  `json:"started_at" gorm:"index"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	ErrorMessage    string     `json:"error_message,omitempty" gorm:"type:text"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

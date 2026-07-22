package api

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

type FloatMap map[string]float64

func (m FloatMap) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

func (m *FloatMap) Scan(value any) error {
	if value == nil {
		*m = FloatMap{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}
	return json.Unmarshal(bytes, m)
}

type HealthEvaluationRun struct {
	ID           uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	Status       string     `json:"status" gorm:"index"`
	RuleVersion  string     `json:"rule_version" gorm:"index"`
	Source       string     `json:"source"`
	StartedAt    time.Time  `json:"started_at" gorm:"index"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	AssetCount   int        `json:"asset_count"`
	ScoredCount  int        `json:"scored_count"`
	UnknownCount int        `json:"unknown_count"`
	RuleHitCount int        `json:"rule_hit_count"`
	ErrorMessage string     `json:"error_message,omitempty" gorm:"type:text"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type GPUFeatureSnapshot struct {
	ID                    uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	EvaluationRunID       uint      `json:"evaluation_run_id" gorm:"index"`
	GPUAssetID            uint      `json:"gpu_asset_id" gorm:"index"`
	GPUUUID               string    `json:"gpu_uuid" gorm:"column:gpu_uuid;index"`
	NodeIP                string    `json:"node_ip" gorm:"index"`
	GPUIndex              int       `json:"gpu_index" gorm:"column:gpu_index;index"`
	ModelName             string    `json:"model_name" gorm:"index"`
	Metrics               FloatMap  `json:"metrics" gorm:"type:text"`
	FeatureCatalogVersion string    `json:"feature_catalog_version" gorm:"index"`
	FeatureVersions       StringMap `json:"feature_versions" gorm:"type:text"`
	AvailableMetricCount  int       `json:"available_metric_count"`
	ExpectedMetricCount   int       `json:"expected_metric_count"`
	DataConfidence        string    `json:"data_confidence" gorm:"index"`
	ObservedAt            time.Time `json:"observed_at" gorm:"index"`
	CreatedAt             time.Time `json:"created_at"`
}

type GPUHealthScore struct {
	ID                uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	EvaluationRunID   uint       `json:"evaluation_run_id" gorm:"index"`
	FeatureSnapshotID uint       `json:"feature_snapshot_id" gorm:"index"`
	GPUAssetID        uint       `json:"gpu_asset_id" gorm:"index"`
	GPUUUID           string     `json:"gpu_uuid" gorm:"column:gpu_uuid;index"`
	NodeIP            string     `json:"node_ip" gorm:"index"`
	GPUIndex          int        `json:"gpu_index" gorm:"column:gpu_index;index"`
	ModelName         string     `json:"model_name" gorm:"index"`
	Score             *int       `json:"score"`
	Level             string     `json:"level" gorm:"index"`
	DataConfidence    string     `json:"data_confidence" gorm:"index"`
	StabilityScore    int        `json:"stability_score"`
	MemoryScore       int        `json:"memory_score"`
	ThermalScore      int        `json:"thermal_score"`
	PowerScore        int        `json:"power_score"`
	InterconnectScore int        `json:"interconnect_score"`
	PerformanceScore  int        `json:"performance_score"`
	Evidence          StringList `json:"evidence" gorm:"type:text"`
	RuleVersion       string     `json:"rule_version" gorm:"index"`
	Current           bool       `json:"current" gorm:"index"`
	EvaluatedAt       time.Time  `json:"evaluated_at" gorm:"index"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type GPUHealthRuleHit struct {
	ID            uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	HealthScoreID uint      `json:"health_score_id" gorm:"index"`
	GPUAssetID    uint      `json:"gpu_asset_id" gorm:"index"`
	GPUUUID       string    `json:"gpu_uuid" gorm:"column:gpu_uuid;index"`
	RuleCode      string    `json:"rule_code" gorm:"index"`
	Domain        string    `json:"domain" gorm:"index"`
	Severity      string    `json:"severity" gorm:"index"`
	Deduction     int       `json:"deduction"`
	ObservedValue float64   `json:"observed_value"`
	Threshold     string    `json:"threshold"`
	Evidence      string    `json:"evidence" gorm:"type:text"`
	RuleVersion   string    `json:"rule_version" gorm:"index"`
	EvaluatedAt   time.Time `json:"evaluated_at" gorm:"index"`
	CreatedAt     time.Time `json:"created_at"`
}

// GPUFaultEvent is one lifecycle episode for a deterministic GPU health rule.
// A recovered episode is immutable; a later recurrence creates a new episode.
type GPUFaultEvent struct {
	ID              uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	EpisodeKey      string     `json:"episode_key" gorm:"index"`
	Source          string     `json:"source" gorm:"index"`
	State           string     `json:"state" gorm:"index"`
	GPUAssetID      uint       `json:"gpu_asset_id" gorm:"index"`
	GPUUUID         string     `json:"gpu_uuid" gorm:"column:gpu_uuid;index"`
	NodeIP          string     `json:"node_ip" gorm:"index"`
	GPUIndex        int        `json:"gpu_index" gorm:"column:gpu_index;index"`
	ModelName       string     `json:"model_name" gorm:"index"`
	RuleCode        string     `json:"rule_code" gorm:"index"`
	Domain          string     `json:"domain" gorm:"index"`
	Severity        string     `json:"severity" gorm:"index"`
	Evidence        string     `json:"evidence" gorm:"type:text"`
	ObservedValue   float64    `json:"observed_value"`
	Threshold       string     `json:"threshold"`
	OccurrenceCount int        `json:"occurrence_count"`
	LatestScoreID   uint       `json:"latest_score_id" gorm:"index"`
	RuleVersion     string     `json:"rule_version" gorm:"index"`
	FirstObservedAt time.Time  `json:"first_observed_at" gorm:"index"`
	LastObservedAt  time.Time  `json:"last_observed_at" gorm:"index"`
	RecoveredAt     *time.Time `json:"recovered_at,omitempty" gorm:"index"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

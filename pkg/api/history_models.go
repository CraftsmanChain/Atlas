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

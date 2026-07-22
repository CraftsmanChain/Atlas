package api

import "time"

// FeatureDefinition is the versioned contract shared by rules, anomaly
// detection, risk ranking, supervised prediction and degradation detection.
// A feature value is never meaningful without the definition version that
// describes its time and missing-data semantics.
type FeatureDefinition struct {
	ID                  uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	Name                string     `json:"name" gorm:"uniqueIndex:idx_feature_name_version,priority:1;index;not null"`
	Version             string     `json:"version" gorm:"uniqueIndex:idx_feature_name_version,priority:2;index;not null"`
	DisplayNameZH       string     `json:"display_name_zh"`
	DisplayNameEN       string     `json:"display_name_en"`
	Domain              string     `json:"domain" gorm:"index;not null"`
	EntityType          string     `json:"entity_type" gorm:"index;not null"`
	Granularity         string     `json:"granularity" gorm:"not null"`
	SourceType          string     `json:"source_type" gorm:"index;not null"`
	SourceReference     string     `json:"source_reference" gorm:"type:text;not null"`
	Computation         string     `json:"computation" gorm:"type:text;not null"`
	TimeSemantics       string     `json:"time_semantics" gorm:"not null"`
	Window              string     `json:"window"`
	FreshnessSLASeconds int64      `json:"freshness_sla_seconds"`
	MissingStrategy     string     `json:"missing_strategy" gorm:"not null"`
	QualityStatus       string     `json:"quality_status" gorm:"index;not null"`
	SupportedModels     StringList `json:"supported_models" gorm:"type:text"`
	Purposes            StringList `json:"purposes" gorm:"type:text"`
	Lineage             StringList `json:"lineage" gorm:"type:text"`
	Owner               string     `json:"owner" gorm:"index;not null"`
	Status              string     `json:"status" gorm:"index;not null"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

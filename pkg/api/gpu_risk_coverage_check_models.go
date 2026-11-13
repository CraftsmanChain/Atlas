package api

import "time"

// GPURiskSourceCoverageCheck is an append-only record that an operator pulled a
// history source completely through VerifiedThrough. ObservedAt is assigned by
// the server clock when the source is actually read and is never backfilled, so
// a check can only ever restate what was already visible when it ran. No input
// carries a timestamp.
type GPURiskSourceCoverageCheck struct {
	ID              uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	Source          string    `json:"source" gorm:"index;not null"`
	VerifiedThrough time.Time `json:"verified_through" gorm:"not null"`
	ObservedAt      time.Time `json:"observed_at" gorm:"index;not null"`
	RecordCount     int       `json:"record_count"`
	CheckSHA256     string    `json:"check_sha256" gorm:"size:64;not null"`
	Owner           string    `json:"owner" gorm:"index;not null"`
	Contract        string    `json:"contract" gorm:"not null"`
	CreatedAt       time.Time `json:"created_at"`
}

func (GPURiskSourceCoverageCheck) TableName() string { return "gpu_risk_source_coverage_checks" }

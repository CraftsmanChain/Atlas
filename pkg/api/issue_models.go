package api

import "time"

// PlatformIssue is the normalized problem record shared by inventory, data
// quality and hardware detection. DetectionState describes what automation
// currently observes; Status describes the human workflow.
type PlatformIssue struct {
	ID                 uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	IssueKey           string     `json:"issue_key" gorm:"uniqueIndex;not null"`
	Category           string     `json:"category" gorm:"index;not null"`
	IssueType          string     `json:"issue_type" gorm:"index;not null"`
	Title              string     `json:"title" gorm:"not null"`
	Description        string     `json:"description" gorm:"type:text"`
	EntityType         string     `json:"entity_type" gorm:"index"`
	EntityKey          string     `json:"entity_key" gorm:"index"`
	NodeIP             string     `json:"node_ip" gorm:"index"`
	GPUUUID            string     `json:"gpu_uuid" gorm:"column:gpu_uuid;index"`
	Severity           string     `json:"severity" gorm:"index;not null"`
	Status             string     `json:"status" gorm:"index;not null"`
	DetectionState     string     `json:"detection_state" gorm:"index;not null"`
	DetectionSource    string     `json:"detection_source" gorm:"index;not null"`
	SourceRecordID     uint       `json:"source_record_id" gorm:"index"`
	FirstDetectedAt    time.Time  `json:"first_detected_at" gorm:"index"`
	LastDetectedAt     time.Time  `json:"last_detected_at" gorm:"index"`
	SourceRecoveredAt  *time.Time `json:"source_recovered_at,omitempty" gorm:"index"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty" gorm:"index"`
	LatestResolutionID uint       `json:"latest_resolution_id" gorm:"index"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// IssueResolution is append-only operational feedback. Keeping each attempt
// makes the data suitable for audited AI datasets and future handling Skills.
type IssueResolution struct {
	ID                uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	IssueID           uint       `json:"issue_id" gorm:"index;not null"`
	Status            string     `json:"status" gorm:"index;not null"`
	RootCause         string     `json:"root_cause" gorm:"type:text"`
	Solution          string     `json:"solution" gorm:"type:text"`
	ResolutionProcess string     `json:"resolution_process" gorm:"type:text"`
	Result            string     `json:"result" gorm:"type:text"`
	Evidence          StringList `json:"evidence" gorm:"type:text"`
	Operator          string     `json:"operator" gorm:"index"`
	TrainingEligible  bool       `json:"training_eligible" gorm:"index"`
	CreatedAt         time.Time  `json:"created_at" gorm:"index"`
}

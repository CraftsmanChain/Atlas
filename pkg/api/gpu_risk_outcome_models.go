package api

import "time"

// Evidence revisions are append-only, compare-and-swap chains written only by
// the controlled CLI. Data holds a typed gpu-risk evidence contract, never a DSN.
type GPURiskEvidenceRevision struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	Kind        string    `json:"kind" gorm:"uniqueIndex:gpu_risk_evidence_revision;not null"`
	Key         string    `json:"key" gorm:"uniqueIndex:gpu_risk_evidence_revision;size:128;not null"`
	Revision    int       `json:"revision" gorm:"uniqueIndex:gpu_risk_evidence_revision;not null"`
	PreviousSHA string    `json:"previous_sha"`
	SHA         string    `json:"sha" gorm:"uniqueIndex;size:64;not null"`
	AvailableAt time.Time `json:"available_at" gorm:"index"`
	Data        string    `json:"-" gorm:"type:text;not null"`
}

func (GPURiskEvidenceRevision) TableName() string { return "gpu_risk_evidence_revisions" }

type GPURiskOutcomeBatch struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	QueryID     uint      `json:"query_id" gorm:"index;not null"`
	Version     string    `json:"version"`
	AsOf        time.Time `json:"as_of" gorm:"index"`
	InputSHA    string    `json:"input_sha"`
	SHA         string    `json:"sha" gorm:"uniqueIndex;size:64;not null"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

func (GPURiskOutcomeBatch) TableName() string { return "gpu_risk_outcome_batches" }

type GPURiskNodeOutcome struct {
	ID           uint       `json:"id" gorm:"primaryKey"`
	BatchID      uint       `json:"batch_id" gorm:"uniqueIndex:gpu_risk_outcome_member;not null"`
	NodeID       uint       `json:"node_id" gorm:"uniqueIndex:gpu_risk_outcome_member;not null"`
	Generation   int        `json:"generation" gorm:"uniqueIndex:gpu_risk_outcome_member;not null"`
	Status       string     `json:"status"`
	Positive     *bool      `json:"positive"`
	Intervention bool       `json:"intervention"`
	EpisodeKeys  StringList `json:"episode_keys" gorm:"type:text"`
	EvidenceSHAs StringList `json:"evidence_shas" gorm:"type:text"`
	Blockers     StringList `json:"blockers" gorm:"type:text"`
}

func (GPURiskNodeOutcome) TableName() string { return "gpu_risk_node_outcomes" }

package api

import "time"

// GPURiskShadowBatch is one immutable shadow-strategy ranking of a frozen query.
// Exactly one batch exists per (query, strategy); it is never recomputed, so a
// batch written shortly after the query is frozen stays a prospective record.
type GPURiskShadowBatch struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	QueryID     uint      `json:"query_id" gorm:"uniqueIndex:gpu_risk_shadow_batch_key;not null"`
	Strategy    string    `json:"strategy" gorm:"uniqueIndex:gpu_risk_shadow_batch_key;size:96;not null"`
	Cutoff      time.Time `json:"cutoff" gorm:"not null"`
	ComputedAt  time.Time `json:"computed_at" gorm:"not null"`
	Timing      string    `json:"timing" gorm:"size:16;not null"` // prospective | retrospective
	InputSHA    string    `json:"input_sha" gorm:"size:64;not null"`
	SHA         string    `json:"sha" gorm:"uniqueIndex;size:64;not null"`
	MemberCount int       `json:"member_count"`
	ScoredCount int       `json:"scored_count"`
	CreatedAt   time.Time `json:"created_at"`
}

func (GPURiskShadowBatch) TableName() string { return "gpu_risk_shadow_batches" }

// GPURiskShadowScore is one member row of a shadow batch. RawScore is null for
// members that are not included in the frozen scope (never imputed).
type GPURiskShadowScore struct {
	ID         uint     `json:"id" gorm:"primaryKey"`
	BatchID    uint     `json:"batch_id" gorm:"uniqueIndex:gpu_risk_shadow_score_key;not null"`
	NodeID     uint     `json:"node_id" gorm:"uniqueIndex:gpu_risk_shadow_score_key;not null"`
	Generation int      `json:"generation" gorm:"uniqueIndex:gpu_risk_shadow_score_key;not null"`
	RawScore   *float64 `json:"raw_score"`
	Rank       int      `json:"rank"`
	TieCount   int      `json:"tie_count"`
	History    int      `json:"history_episodes"`
	DaysSince  *float64 `json:"days_since_last"`
	Precursor  int      `json:"precursor_events_30d"`
}

func (GPURiskShadowScore) TableName() string { return "gpu_risk_shadow_scores" }

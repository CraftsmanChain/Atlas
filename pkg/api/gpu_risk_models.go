package api

import "time"

// GPU risk records are independent of probability predictions. Completed queries
// are immutable; all semantic input and output is SHA-bound.
type GPURiskScope struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	SHA       string    `json:"sha" gorm:"uniqueIndex;size:64;not null"`
	Input     string    `json:"-" gorm:"type:text;not null"`
	CreatedAt time.Time `json:"created_at"`
}

func (GPURiskScope) TableName() string { return "gpu_risk_scopes" }

type GPURiskQuery struct {
	ID             uint       `json:"id" gorm:"primaryKey"`
	Key            string     `json:"key" gorm:"uniqueIndex;size:64;not null"`
	ScopeSHA       string     `json:"scope_sha" gorm:"size:64;not null"`
	Contract       string     `json:"contract"`
	Target         string     `json:"target"`
	Cutoff         time.Time  `json:"cutoff"`
	HorizonMinutes int        `json:"horizon_minutes"`
	Status         string     `json:"status" gorm:"index"`
	MemberCount    int        `json:"member_count"`
	ResultSHA      string     `json:"result_sha" gorm:"size:64"`
	CompletedAt    *time.Time `json:"completed_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

func (GPURiskQuery) TableName() string { return "gpu_risk_queries" }

type GPURiskMember struct {
	ID           uint   `json:"id" gorm:"primaryKey"`
	QueryID      uint   `json:"query_id" gorm:"uniqueIndex:gpu_risk_member_key;not null"`
	NodeID       uint   `json:"node_id" gorm:"uniqueIndex:gpu_risk_member_key;not null"`
	Generation   int    `json:"generation" gorm:"uniqueIndex:gpu_risk_member_key;not null"`
	NodeIP       string `json:"node_ip"`
	EntityRef    string `json:"entity_ref"`
	Status       string `json:"status"`
	Reason       string `json:"reason"`
	ExpectedGPUs int    `json:"expected_gpus"`
	ObservedGPUs int    `json:"observed_gpus"`
}

func (GPURiskMember) TableName() string { return "gpu_risk_members" }

type GPURiskScore struct {
	ID              uint       `json:"id" gorm:"primaryKey"`
	QueryID         uint       `json:"query_id" gorm:"uniqueIndex:gpu_risk_score_key;not null"`
	NodeID          uint       `json:"node_id" gorm:"uniqueIndex:gpu_risk_score_key;not null"`
	Generation      int        `json:"generation" gorm:"uniqueIndex:gpu_risk_score_key;not null"`
	Policy          string     `json:"policy" gorm:"uniqueIndex:gpu_risk_score_key;not null"`
	RawScore        *float64   `json:"raw_score"`
	SignalStatus    string     `json:"signal_status"`
	Rank            int        `json:"rank"`
	TieCount        int        `json:"tie_count"`
	EligibleCount   int        `json:"eligible_count"`
	TopFivePercentK int        `json:"top_five_percent_k"`
	Coverage        float64    `json:"coverage"`
	Evidence        StringList `json:"evidence" gorm:"type:text"`
}

func (GPURiskScore) TableName() string { return "gpu_risk_scores" }

type GPURiskJob struct {
	Input      string    `json:"-" gorm:"type:text"`
	ID         uint      `json:"id" gorm:"primaryKey"`
	Key        string    `json:"key" gorm:"uniqueIndex;size:64;not null"`
	Owner      string    `json:"owner"`
	Token      int64     `json:"token"`
	LeaseUntil time.Time `json:"lease_until"`
	Attempt    int       `json:"attempt"`
	Status     string    `json:"status"`
	ErrorCode  string    `json:"error_code"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (GPURiskJob) TableName() string { return "gpu_risk_jobs" }

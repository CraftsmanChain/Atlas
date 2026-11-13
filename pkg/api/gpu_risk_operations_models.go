package api

import "time"

// GPURiskOperation records bounded operational observations, not labels/evidence.
type GPURiskOperation struct {
	ID             uint      `json:"id" gorm:"primaryKey"`
	Day            string    `json:"day" gorm:"uniqueIndex:gpu_operation_day_stage;not null"`
	Stage          string    `json:"stage" gorm:"uniqueIndex:gpu_operation_day_stage;not null"`
	Status         string    `json:"status"`
	ErrorCode      string    `json:"error_code"`
	JobCount       int       `json:"job_count"`
	Exhausted      int       `json:"exhausted"`
	QueryID        uint      `json:"query_id"`
	MemberCount    int       `json:"member_count"`
	ResultSHA      string    `json:"result_sha"`
	SHA            string    `json:"sha"`
	OutcomeBatchID uint      `json:"outcome_batch_id"`
	OutcomeSHA     string    `json:"outcome_sha"`
	ObservedAt     time.Time `json:"observed_at"`
}

func (GPURiskOperation) TableName() string { return "gpu_risk_operations" }

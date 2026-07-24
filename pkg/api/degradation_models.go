package api

import "time"

// GPUDegradationAssessment is a shadow-mode, passive comparison result.
// It is not a health-score deduction or a calibrated failure probability.
type GPUDegradationAssessment struct {
	GPUAssetID        uint       `json:"gpu_asset_id"`
	GPUUUID           string     `json:"gpu_uuid"`
	NodeIP            string     `json:"node_ip"`
	GPUIndex          int        `json:"gpu_index"`
	ModelName         string     `json:"model_name"`
	Status            string     `json:"status"`
	Metric            string     `json:"metric"`
	ObservedValue     float64    `json:"observed_value"`
	BaselineValue     float64    `json:"baseline_value"`
	PerformanceRatio  float64    `json:"performance_ratio"`
	GPUUtilization    float64    `json:"gpu_utilization"`
	PeerCount         int        `json:"peer_count"`
	BaselineID        uint       `json:"baseline_id,omitempty"`
	BaselineScope     string     `json:"baseline_scope"`
	BaselineMaturity  string     `json:"baseline_maturity"`
	DataConfidence    string     `json:"data_confidence"`
	Evidence          StringList `json:"evidence"`
	RecommendedAction string     `json:"recommended_action"`
	FeatureVersion    string     `json:"feature_version"`
	ObservedAt        time.Time  `json:"observed_at"`
	EvaluatedAt       time.Time  `json:"evaluated_at"`
}

type GPUDegradationSummary struct {
	Version                string         `json:"version"`
	Mode                   string         `json:"mode"`
	EvaluatedGPUs          int            `json:"evaluated_gpus"`
	EligibleGPUs           int            `json:"eligible_gpus"`
	BaselineReadyGPUs      int            `json:"baseline_ready_gpus"`
	HistoricalBaselineGPUs int            `json:"historical_baseline_gpus"`
	CandidateGPUs          int            `json:"candidate_gpus"`
	InsufficientGPUs       int            `json:"insufficient_gpus"`
	MinimumUtilization     float64        `json:"minimum_utilization"`
	RatioThreshold         float64        `json:"ratio_threshold"`
	FreshnessSLASeconds    int64          `json:"freshness_sla_seconds"`
	ByModel                map[string]int `json:"by_model"`
	LatestObservedAt       *time.Time     `json:"latest_observed_at,omitempty"`
	EvaluatedAt            time.Time      `json:"evaluated_at"`
}

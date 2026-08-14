package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"atlas/pkg/api"
)

const (
	DataDriftReportVersion        = "prediction-data-drift-report-v1"
	dataDriftMaximumPSIProxy      = 0.20
	dataDriftMaximumKSProxy       = 0.20
	dataDriftMaximumPositiveDelta = 0.05
)

type ShadowDistributionSnapshot struct {
	RunID                  uint       `json:"run_id"`
	RunKey                 string     `json:"run_key"`
	Version                string     `json:"version"`
	Status                 string     `json:"status"`
	ModelKey               string     `json:"model_key"`
	ModelVersion           string     `json:"model_version"`
	ReportSHA256           string     `json:"report_sha256,omitempty"`
	ScoredGPUCount         int        `json:"scored_gpu_count"`
	PositiveGPUCount       int        `json:"positive_gpu_count"`
	PositiveRatio          float64    `json:"positive_ratio"`
	MinimumProbability     *float64   `json:"minimum_probability,omitempty"`
	MaximumProbability     *float64   `json:"maximum_probability,omitempty"`
	MeanProbability        *float64   `json:"mean_probability,omitempty"`
	MedianProbability      *float64   `json:"median_probability,omitempty"`
	P90Probability         *float64   `json:"p90_probability,omitempty"`
	P95Probability         *float64   `json:"p95_probability,omitempty"`
	P99Probability         *float64   `json:"p99_probability,omitempty"`
	MaximumNodeMean        *float64   `json:"maximum_node_mean,omitempty"`
	AllAboveThresholdNodes int        `json:"all_above_threshold_nodes"`
	DistributionStatus     string     `json:"distribution_status"`
	DistributionBlocking   []string   `json:"distribution_blocking"`
	NoAlertEmitted         bool       `json:"no_alert_emitted"`
	NoActionExecuted       bool       `json:"no_action_executed"`
	FinishedAt             *time.Time `json:"finished_at,omitempty"`
}

type DataDriftReport struct {
	Version                string                      `json:"version"`
	FrameworkVersion       string                      `json:"framework_version"`
	Mode                   string                      `json:"mode"`
	Status                 string                      `json:"status"`
	ReportSHA256           string                      `json:"report_sha256"`
	Method                 string                      `json:"method"`
	MinimumShadowRuns      int                         `json:"minimum_shadow_runs"`
	PSIProxyThreshold      float64                     `json:"psi_proxy_threshold"`
	KSProxyThreshold       float64                     `json:"ks_proxy_threshold"`
	PositiveDeltaThreshold float64                     `json:"positive_ratio_delta_threshold"`
	Latest                 *ShadowDistributionSnapshot `json:"latest,omitempty"`
	Baseline               *ShadowDistributionSnapshot `json:"baseline,omitempty"`
	PSIProxy               float64                     `json:"psi_proxy"`
	KSProxy                float64                     `json:"ks_proxy"`
	PositiveRatioDelta     float64                     `json:"positive_ratio_delta"`
	MedianDelta            float64                     `json:"median_delta"`
	P95Delta               float64                     `json:"p95_delta"`
	P99Delta               float64                     `json:"p99_delta"`
	BlockingReasons        []string                    `json:"blocking_reasons"`
	RecommendedNextRun     []string                    `json:"recommended_next_run"`
	GeneratedAt            time.Time                   `json:"generated_at"`
}

func (s *Service) DataDriftReport() (DataDriftReport, error) {
	var runs []api.PredictionShadowScoringRun
	if err := s.db.Where("status IN ?", []string{"completed", "distribution_review_required"}).Order("finished_at DESC, id DESC").Limit(2).Find(&runs).Error; err != nil {
		return DataDriftReport{}, err
	}
	report := DataDriftReport{
		Version: DataDriftReportVersion, FrameworkVersion: FrameworkVersion, Mode: "read_only_shadow_distribution_drift",
		Method:                 "shadow score distribution PSI/KS proxy from persisted run quantiles; not a full feature-level PSI/KS replacement",
		MinimumShadowRuns:      2,
		PSIProxyThreshold:      dataDriftMaximumPSIProxy,
		KSProxyThreshold:       dataDriftMaximumKSProxy,
		PositiveDeltaThreshold: dataDriftMaximumPositiveDelta,
		GeneratedAt:            s.now(),
		RecommendedNextRun: []string{
			"collect at least two read-only shadow scoring runs before interpreting distribution drift",
			"treat this as a score-distribution drift proxy until feature-column PSI/KS is available",
			"keep drift review read-only; do not adjust thresholds or trigger actions from this report",
		},
	}
	if len(runs) > 0 {
		latest := shadowDistributionSnapshot(runs[0])
		report.Latest = &latest
	}
	if len(runs) < report.MinimumShadowRuns {
		report.Status = "exploratory_insufficient_shadow_runs"
		if len(runs) == 0 {
			report.Status = "blocked_no_shadow_runs"
			report.BlockingReasons = append(report.BlockingReasons, "no completed shadow scoring runs are available")
		} else {
			report.BlockingReasons = append(report.BlockingReasons, "only one completed shadow scoring run is available")
		}
		report.ReportSHA256 = dataDriftChecksum(report)
		return report, nil
	}
	baseline := shadowDistributionSnapshot(runs[1])
	report.Baseline = &baseline
	report.PositiveRatioDelta = math.Abs(report.Latest.PositiveRatio - baseline.PositiveRatio)
	report.MedianDelta = optionalAbsDelta(report.Latest.MedianProbability, baseline.MedianProbability)
	report.P95Delta = optionalAbsDelta(report.Latest.P95Probability, baseline.P95Probability)
	report.P99Delta = optionalAbsDelta(report.Latest.P99Probability, baseline.P99Probability)
	report.KSProxy = maxFloat(report.PositiveRatioDelta, report.MedianDelta, report.P95Delta, report.P99Delta)
	report.PSIProxy = approximatePSIProxy(*report.Latest, baseline)
	report.Status = "passed"
	if report.PSIProxy > report.PSIProxyThreshold {
		report.Status = "review_required"
		report.BlockingReasons = append(report.BlockingReasons, "psi_proxy_exceeds_threshold")
	}
	if report.KSProxy > report.KSProxyThreshold {
		report.Status = "review_required"
		report.BlockingReasons = append(report.BlockingReasons, "ks_proxy_exceeds_threshold")
	}
	if report.PositiveRatioDelta > report.PositiveDeltaThreshold {
		report.Status = "review_required"
		report.BlockingReasons = append(report.BlockingReasons, "positive_ratio_delta_exceeds_threshold")
	}
	if report.Latest.DistributionStatus == "review_required" {
		report.Status = "review_required"
		report.BlockingReasons = append(report.BlockingReasons, report.Latest.DistributionBlocking...)
	}
	report.BlockingReasons = uniqueSorted(report.BlockingReasons)
	report.ReportSHA256 = dataDriftChecksum(report)
	return report, nil
}

func shadowDistributionSnapshot(run api.PredictionShadowScoringRun) ShadowDistributionSnapshot {
	return ShadowDistributionSnapshot{
		RunID: run.ID, RunKey: run.RunKey, Version: run.Version, Status: run.Status,
		ModelKey: run.ModelKey, ModelVersion: run.ModelVersion, ReportSHA256: run.ReportSHA256,
		ScoredGPUCount: run.ScoredGPUCount, PositiveGPUCount: run.PositiveGPUCount, PositiveRatio: run.PositiveRatio,
		MinimumProbability: run.MinimumProbability, MaximumProbability: run.MaximumProbability,
		MeanProbability: run.MeanProbability, MedianProbability: run.MedianProbability,
		P90Probability: run.P90Probability, P95Probability: run.P95Probability, P99Probability: run.P99Probability,
		MaximumNodeMean: run.MaximumNodeMean, AllAboveThresholdNodes: run.AllAboveThresholdNodes,
		DistributionStatus: run.DistributionStatus, DistributionBlocking: append([]string(nil), run.BlockingReasons...),
		NoAlertEmitted: run.NoAlertEmitted, NoActionExecuted: run.NoActionExecuted, FinishedAt: run.FinishedAt,
	}
}

func optionalAbsDelta(left, right *float64) float64 {
	if left == nil || right == nil {
		return 0
	}
	return math.Abs(*left - *right)
}

func maxFloat(values ...float64) float64 {
	result := 0.0
	for _, value := range values {
		if value > result {
			result = value
		}
	}
	return result
}

func approximatePSIProxy(latest, baseline ShadowDistributionSnapshot) float64 {
	components := []float64{
		math.Abs(latest.PositiveRatio - baseline.PositiveRatio),
		optionalAbsDelta(latest.MedianProbability, baseline.MedianProbability),
		optionalAbsDelta(latest.P90Probability, baseline.P90Probability),
		optionalAbsDelta(latest.P95Probability, baseline.P95Probability),
		optionalAbsDelta(latest.P99Probability, baseline.P99Probability),
	}
	sum := 0.0
	for _, value := range components {
		sum += value * value
	}
	return sum
}

func dataDriftChecksum(report DataDriftReport) string {
	fingerprint := struct {
		Version                string                      `json:"version"`
		FrameworkVersion       string                      `json:"framework_version"`
		Mode                   string                      `json:"mode"`
		Status                 string                      `json:"status"`
		Method                 string                      `json:"method"`
		MinimumShadowRuns      int                         `json:"minimum_shadow_runs"`
		PSIProxyThreshold      float64                     `json:"psi_proxy_threshold"`
		KSProxyThreshold       float64                     `json:"ks_proxy_threshold"`
		PositiveDeltaThreshold float64                     `json:"positive_ratio_delta_threshold"`
		Latest                 *ShadowDistributionSnapshot `json:"latest,omitempty"`
		Baseline               *ShadowDistributionSnapshot `json:"baseline,omitempty"`
		PSIProxy               float64                     `json:"psi_proxy"`
		KSProxy                float64                     `json:"ks_proxy"`
		PositiveRatioDelta     float64                     `json:"positive_ratio_delta"`
		MedianDelta            float64                     `json:"median_delta"`
		P95Delta               float64                     `json:"p95_delta"`
		P99Delta               float64                     `json:"p99_delta"`
		BlockingReasons        []string                    `json:"blocking_reasons"`
		RecommendedNextRun     []string                    `json:"recommended_next_run"`
	}{
		Version: report.Version, FrameworkVersion: report.FrameworkVersion, Mode: report.Mode, Status: report.Status,
		Method: report.Method, MinimumShadowRuns: report.MinimumShadowRuns, PSIProxyThreshold: report.PSIProxyThreshold,
		KSProxyThreshold: report.KSProxyThreshold, PositiveDeltaThreshold: report.PositiveDeltaThreshold,
		Latest: report.Latest, Baseline: report.Baseline, PSIProxy: report.PSIProxy, KSProxy: report.KSProxy,
		PositiveRatioDelta: report.PositiveRatioDelta, MedianDelta: report.MedianDelta, P95Delta: report.P95Delta,
		P99Delta: report.P99Delta, BlockingReasons: report.BlockingReasons, RecommendedNextRun: report.RecommendedNextRun,
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

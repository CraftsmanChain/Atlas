package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
)

const RiskRankingSnapshotVersion = "prediction-risk-ranking-snapshot-v1"

type RiskRankingItem struct {
	Rank                   int     `json:"rank"`
	TopPercentile          float64 `json:"top_percentile"`
	NodeIP                 string  `json:"node_ip"`
	RiskScore              float64 `json:"risk_score"`
	MaximumProbability     float64 `json:"maximum_probability"`
	MeanProbability        float64 `json:"mean_probability"`
	GPUCount               int     `json:"gpu_count"`
	AboveThresholdGPUCount int     `json:"above_threshold_gpu_count"`
	TopGPUUUID             string  `json:"top_gpu_uuid"`
}

type RiskRankingSafety struct {
	ReadOnlyShadow   bool `json:"read_only_shadow"`
	NoAlertEmitted   bool `json:"no_alert_emitted"`
	NoActionExecuted bool `json:"no_action_executed"`
}

type RiskRankingSnapshotReport struct {
	Version               string            `json:"version"`
	FrameworkVersion      string            `json:"framework_version"`
	Mode                  string            `json:"mode"`
	Status                string            `json:"status"`
	ReportSHA256          string            `json:"report_sha256"`
	Policy                string            `json:"policy"`
	ScoreSemantics        string            `json:"score_semantics"`
	ShadowRunID           uint              `json:"shadow_run_id,omitempty"`
	ShadowRunKey          string            `json:"shadow_run_key,omitempty"`
	ModelSpecID           uint              `json:"model_spec_id,omitempty"`
	ModelKey              string            `json:"model_key,omitempty"`
	ModelVersion          string            `json:"model_version,omitempty"`
	ScopeModelName        string            `json:"scope_model_name,omitempty"`
	TransformationVersion string            `json:"transformation_contract_version,omitempty"`
	HorizonMinutes        int               `json:"horizon_minutes"`
	DecisionThreshold     *float64          `json:"decision_threshold,omitempty"`
	SnapshotCutoffAt      *time.Time        `json:"snapshot_cutoff_at,omitempty"`
	TargetGPUCount        int               `json:"target_gpu_count"`
	ScoredGPUCount        int               `json:"scored_gpu_count"`
	NodeCount             int               `json:"node_count"`
	Items                 []RiskRankingItem `json:"items"`
	Safety                RiskRankingSafety `json:"safety"`
	BlockingReasons       []string          `json:"blocking_reasons"`
	Interpretation        []string          `json:"interpretation"`
	RecommendedNextRun    []string          `json:"recommended_next_run"`
	GeneratedAt           time.Time         `json:"generated_at"`
}

func (s *Service) RiskRankingSnapshotReport() (RiskRankingSnapshotReport, error) {
	report := RiskRankingSnapshotReport{
		Version: RiskRankingSnapshotVersion, FrameworkVersion: FrameworkVersion,
		Mode: "read_only_shadow", Policy: "node_max_calibrated_probability",
		ScoreSemantics: "relative_priority_not_absolute_failure_probability",
		Items:          []RiskRankingItem{}, Safety: RiskRankingSafety{ReadOnlyShadow: true, NoAlertEmitted: true, NoActionExecuted: true},
		Interpretation: []string{
			"Rank answers which node should be reviewed first within the frozen shadow cohort.",
			"Risk score currently reuses each node's maximum calibrated GPU probability as a ranking signal; it is not an independently validated failure probability.",
			"The snapshot is bound to one immutable shadow run, model, horizon, cutoff, and deterministic node ordering.",
		},
		RecommendedNextRun: []string{
			"Evaluate this frozen ranking against the same mature outcomes used by the probability track.",
			"Keep ranking read-only until multiple prospective time cohorts show stable Top-K lift.",
		},
		GeneratedAt: s.now(),
	}
	var run api.PredictionShadowScoringRun
	result := s.db.Where("status IN ?", []string{"completed", "distribution_review_required"}).Order("finished_at DESC, id DESC").Limit(1).Find(&run)
	if result.Error != nil {
		return RiskRankingSnapshotReport{}, result.Error
	}
	if result.RowsAffected == 0 {
		report.Status = "blocked_no_shadow_run"
		report.BlockingReasons = []string{"no completed shadow scoring run is available"}
		report.ReportSHA256 = riskRankingSnapshotChecksum(report)
		return report, nil
	}
	report.ShadowRunID, report.ShadowRunKey = run.ID, run.RunKey
	report.ModelSpecID, report.ModelKey, report.ModelVersion = run.ModelSpecID, run.ModelKey, run.ModelVersion
	report.ScopeModelName, report.TransformationVersion = run.ScopeModelName, run.TransformationVersion
	report.TargetGPUCount, report.ScoredGPUCount = run.TargetGPUCount, run.ScoredGPUCount

	var spec api.PredictionModelSpec
	if err := s.db.First(&spec, run.ModelSpecID).Error; err != nil {
		return RiskRankingSnapshotReport{}, err
	}
	report.HorizonMinutes, report.DecisionThreshold = spec.HorizonMinutes, spec.DecisionThreshold
	var predictions []api.HardwareRiskPrediction
	if err := s.db.Where("shadow_run_id = ? AND probability IS NOT NULL", run.ID).Order("node_ip ASC, gpu_uuid ASC, id ASC").Find(&predictions).Error; err != nil {
		return RiskRankingSnapshotReport{}, err
	}
	if len(predictions) == 0 {
		report.Status = "blocked_no_scored_predictions"
		report.BlockingReasons = []string{"latest completed shadow run has no probability-scored predictions"}
		report.ReportSHA256 = riskRankingSnapshotChecksum(report)
		return report, nil
	}
	report.ScoredGPUCount = len(predictions)
	items, cutoff := riskRankingItems(predictions, spec.DecisionThreshold)
	if len(items) == 0 {
		report.Status = "blocked_no_node_eligible_predictions"
		report.BlockingReasons = []string{"latest completed shadow run has no node-eligible probability predictions"}
		report.ReportSHA256 = riskRankingSnapshotChecksum(report)
		return report, nil
	}
	report.Items, report.NodeCount, report.SnapshotCutoffAt = items, len(items), &cutoff
	report.Status = "shadow_snapshot_available"
	report.ReportSHA256 = riskRankingSnapshotChecksum(report)
	return report, nil
}

func riskRankingItems(predictions []api.HardwareRiskPrediction, threshold *float64) ([]RiskRankingItem, time.Time) {
	type nodeAccumulator struct {
		item RiskRankingItem
		sum  float64
	}
	byNode := map[string]*nodeAccumulator{}
	var cutoff time.Time
	for _, prediction := range predictions {
		if prediction.Probability == nil || strings.TrimSpace(prediction.NodeIP) == "" {
			continue
		}
		if prediction.ObservedAt.After(cutoff) {
			cutoff = prediction.ObservedAt
		}
		node := strings.TrimSpace(prediction.NodeIP)
		accumulator := byNode[node]
		if accumulator == nil {
			accumulator = &nodeAccumulator{item: RiskRankingItem{NodeIP: node, MaximumProbability: *prediction.Probability, RiskScore: *prediction.Probability, TopGPUUUID: prediction.GPUUUID}}
			byNode[node] = accumulator
		}
		accumulator.item.GPUCount++
		accumulator.sum += *prediction.Probability
		if *prediction.Probability > accumulator.item.MaximumProbability || (*prediction.Probability == accumulator.item.MaximumProbability && prediction.GPUUUID < accumulator.item.TopGPUUUID) {
			accumulator.item.MaximumProbability = *prediction.Probability
			accumulator.item.RiskScore = *prediction.Probability
			accumulator.item.TopGPUUUID = prediction.GPUUUID
		}
		if threshold != nil && *prediction.Probability >= *threshold {
			accumulator.item.AboveThresholdGPUCount++
		}
	}
	items := make([]RiskRankingItem, 0, len(byNode))
	for _, accumulator := range byNode {
		accumulator.item.MeanProbability = accumulator.sum / float64(accumulator.item.GPUCount)
		items = append(items, accumulator.item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].RiskScore != items[j].RiskScore {
			return items[i].RiskScore > items[j].RiskScore
		}
		return items[i].NodeIP < items[j].NodeIP
	})
	for index := range items {
		items[index].Rank = index + 1
		items[index].TopPercentile = float64(index+1) / float64(len(items))
	}
	return items, cutoff
}

func riskRankingSnapshotChecksum(report RiskRankingSnapshotReport) string {
	fingerprint := report
	fingerprint.ReportSHA256 = ""
	fingerprint.GeneratedAt = time.Time{}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

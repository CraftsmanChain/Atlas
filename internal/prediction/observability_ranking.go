package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

const (
	ObservabilityRankingValidationVersion = "prediction-observability-ranking-validation-v1"
	observabilityRankingHorizon           = 7 * 24 * time.Hour
	observabilityRankingNegativeCensor    = 24 * time.Hour
	observabilityRankingCohortLimit       = 12
	observabilityRankingMinimumCoverage   = 0.80
	observabilityMaxGapMetric             = "gpu_metric_gap_max_seconds_1h"
)

type ObservabilityRankingSafety struct {
	ReadOnlyShadow   bool `json:"read_only_shadow"`
	NoAlertEmitted   bool `json:"no_alert_emitted"`
	NoActionExecuted bool `json:"no_action_executed"`
	ModelIndependent bool `json:"model_registry_independent"`
}

type ObservabilityRankingCohort struct {
	HealthEvaluationRunID uint               `json:"health_evaluation_run_id"`
	CutoffAt              time.Time          `json:"cutoff_at"`
	WindowEndAt           time.Time          `json:"window_end_at"`
	Status                string             `json:"status"`
	GPUCount              int                `json:"gpu_count"`
	NodeCount             int                `json:"node_count"`
	ScoredGPUCount        int                `json:"scored_gpu_count"`
	ScoredNodeCount       int                `json:"scored_node_count"`
	ScoreCoverage         float64            `json:"score_coverage"`
	PositiveNodeCount     int                `json:"positive_node_count"`
	ScoredPositiveNodes   int                `json:"scored_positive_node_count"`
	UnscoredPositiveNodes int                `json:"unscored_positive_node_count"`
	MatchedLabelCount     int                `json:"matched_label_count"`
	RankingAtK            []RankingAtK       `json:"ranking_at_k"`
	RankingAtPercent      []RankingAtPercent `json:"ranking_at_percent"`
	BlockingReasons       []string           `json:"blocking_reasons"`
}

type ObservabilityRankingValidationReport struct {
	Version              string                       `json:"version"`
	FrameworkVersion     string                       `json:"framework_version"`
	Mode                 string                       `json:"mode"`
	Status               string                       `json:"status"`
	ReportSHA256         string                       `json:"report_sha256"`
	Signal               string                       `json:"signal"`
	ScoreSemantics       string                       `json:"score_semantics"`
	TargetHorizonMinutes int                          `json:"target_horizon_minutes"`
	NegativeCensorHours  int                          `json:"negative_censor_hours"`
	CohortLimit          int                          `json:"cohort_limit"`
	MinimumScoreCoverage float64                      `json:"minimum_score_coverage"`
	CohortCount          int                          `json:"cohort_count"`
	EvaluableCohortCount int                          `json:"evaluable_cohort_count"`
	PositiveCohortCount  int                          `json:"positive_cohort_count"`
	Cohorts              []ObservabilityRankingCohort `json:"cohorts"`
	TemporalConsistency  TemporalTrackConsistency     `json:"temporal_consistency"`
	Safety               ObservabilityRankingSafety   `json:"safety"`
	BlockingReasons      []string                     `json:"blocking_reasons"`
	Interpretation       []string                     `json:"interpretation"`
	RecommendedNextRun   []string                     `json:"recommended_next_run"`
	GeneratedAt          time.Time                    `json:"generated_at"`
}

func (s *Service) ObservabilityRankingValidationReport() (ObservabilityRankingValidationReport, error) {
	now := s.now()
	report := ObservabilityRankingValidationReport{
		Version: ObservabilityRankingValidationVersion, FrameworkVersion: FrameworkVersion,
		Mode: "read_only_prospective_observability_validation", Status: "blocked_no_mature_cohorts",
		Signal: observabilityMaxGapMetric, ScoreSemantics: "relative_node_priority_not_absolute_failure_probability",
		TargetHorizonMinutes: int(observabilityRankingHorizon / time.Minute),
		NegativeCensorHours:  int(observabilityRankingNegativeCensor / time.Hour),
		CohortLimit:          observabilityRankingCohortLimit, MinimumScoreCoverage: observabilityRankingMinimumCoverage,
		Cohorts: []ObservabilityRankingCohort{},
		Safety:  ObservabilityRankingSafety{ReadOnlyShadow: true, NoAlertEmitted: true, NoActionExecuted: true, ModelIndependent: true},
		Interpretation: []string{
			"each cohort is one persisted health evaluation run and uses only its point-in-time max-gap snapshots",
			"positive outcomes are confirmed or strong-proxy labels inside the following seven days; negatives require an additional 24-hour censoring window",
			"risk ranking is relative priority evidence and is not a calibrated hardware-failure probability",
		},
		RecommendedNextRun: []string{"continue collecting persisted health runs and reviewed failure labels without enabling alerts or actions"},
		GeneratedAt:        now,
	}

	runs, err := s.independentMatureHealthRuns(now)
	if err != nil {
		return report, err
	}
	for _, run := range runs {
		cohort, err := s.observabilityRankingCohort(run, now)
		if err != nil {
			return report, err
		}
		report.Cohorts = append(report.Cohorts, cohort)
		if cohort.Status == "comparable" {
			report.EvaluableCohortCount++
		}
		if cohort.PositiveNodeCount > 0 {
			report.PositiveCohortCount++
		}
	}
	report.CohortCount = len(report.Cohorts)
	report.TemporalConsistency = observabilityTemporalConsistency(report.Cohorts)
	report.Status, report.BlockingReasons = observabilityRankingReportStatus(report)
	report.ReportSHA256 = observabilityRankingChecksum(report)
	return report, nil
}

func (s *Service) independentMatureHealthRuns(now time.Time) ([]api.HealthEvaluationRun, error) {
	upperBound := now.Add(-observabilityRankingHorizon - observabilityRankingNegativeCensor)
	runs := make([]api.HealthEvaluationRun, 0, observabilityRankingCohortLimit)
	for len(runs) < observabilityRankingCohortLimit {
		var run api.HealthEvaluationRun
		err := s.db.Where("status = ? AND started_at <= ? AND finished_at IS NOT NULL AND finished_at <= ?", "success", upperBound, upperBound).
			Where("EXISTS (SELECT 1 FROM gpu_feature_snapshots WHERE gpu_feature_snapshots.evaluation_run_id = health_evaluation_runs.id)").
			Order("started_at DESC, id DESC").First(&run).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
		upperBound = run.FinishedAt.Add(-observabilityRankingHorizon)
	}
	return runs, nil
}

func (s *Service) observabilityRankingCohort(run api.HealthEvaluationRun, now time.Time) (ObservabilityRankingCohort, error) {
	cutoff := run.StartedAt
	if run.FinishedAt != nil {
		cutoff = *run.FinishedAt
	}
	cohort := ObservabilityRankingCohort{
		HealthEvaluationRunID: run.ID, CutoffAt: cutoff,
		WindowEndAt: cutoff.Add(observabilityRankingHorizon),
		Status:      "no_signal", RankingAtK: []RankingAtK{}, RankingAtPercent: []RankingAtPercent{}, BlockingReasons: []string{},
	}
	var snapshots []api.GPUFeatureSnapshot
	if err := s.db.Where("evaluation_run_id = ?", run.ID).Order("node_ip ASC, gpu_index ASC, id ASC").Find(&snapshots).Error; err != nil {
		return cohort, err
	}
	cohort.GPUCount = len(snapshots)
	nodes := map[string]struct{}{}
	maxGapByNode := map[string]float64{}
	nodeByGPU := map[string]string{}
	for _, snapshot := range snapshots {
		node := strings.TrimSpace(snapshot.NodeIP)
		if node == "" || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(cutoff) {
			continue
		}
		nodes[node] = struct{}{}
		if gpu := strings.ToLower(strings.TrimSpace(snapshot.GPUUUID)); gpu != "" {
			nodeByGPU[gpu] = node
		}
		value, exists := snapshot.Metrics[observabilityMaxGapMetric]
		if !exists || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			continue
		}
		cohort.ScoredGPUCount++
		if previous, exists := maxGapByNode[node]; !exists || value > previous {
			maxGapByNode[node] = value
		}
	}
	cohort.NodeCount = len(nodes)
	cohort.ScoredNodeCount = len(maxGapByNode)
	if cohort.NodeCount > 0 {
		cohort.ScoreCoverage = float64(cohort.ScoredNodeCount) / float64(cohort.NodeCount)
	}

	var labels []api.FailureLabel
	if err := s.db.Where("hardware_class = ? AND label_value = ? AND excluded = ? AND quality_tier IN ? AND occurred_at > ? AND occurred_at <= ? AND available_at <= ?",
		"gpu", 1, false, []string{"confirmed", "strong_proxy"}, cohort.CutoffAt, cohort.WindowEndAt, now).
		Order("occurred_at ASC, id ASC").Find(&labels).Error; err != nil {
		return cohort, err
	}
	positiveNodes := map[string]struct{}{}
	for _, label := range labels {
		node := ""
		if gpu := strings.ToLower(strings.TrimSpace(label.GPUUUID)); gpu != "" {
			node = nodeByGPU[gpu]
		}
		if node == "" {
			candidate := strings.TrimSpace(label.NodeIP)
			if _, exists := nodes[candidate]; exists {
				node = candidate
			}
		}
		if node == "" {
			continue
		}
		positiveNodes[node] = struct{}{}
		cohort.MatchedLabelCount++
	}
	cohort.PositiveNodeCount = len(positiveNodes)

	nodeKeys := make([]string, 0, len(maxGapByNode))
	for node := range maxGapByNode {
		nodeKeys = append(nodeKeys, node)
	}
	sort.Strings(nodeKeys)
	items := make([]rankedOutcome, 0, len(nodeKeys))
	for _, node := range nodeKeys {
		actual := 0
		if _, positive := positiveNodes[node]; positive {
			actual = 1
			cohort.ScoredPositiveNodes++
		}
		items = append(items, rankedOutcome{probability: maxGapByNode[node], actual: actual})
	}
	cohort.UnscoredPositiveNodes = cohort.PositiveNodeCount - cohort.ScoredPositiveNodes
	cohort.RankingAtK = nonNilRankingAtK(rankingFromItems(items))
	cohort.RankingAtPercent = nonNilRankingAtPercent(rankingPercentFromItems(items))
	cohort.Status, cohort.BlockingReasons = observabilityCohortStatus(cohort)
	return cohort, nil
}

func observabilityCohortStatus(cohort ObservabilityRankingCohort) (string, []string) {
	reasons := []string{}
	if cohort.ScoredNodeCount == 0 {
		return "no_signal", []string{"no node has a valid persisted max-gap score at the health-run cutoff"}
	}
	if cohort.ScoreCoverage < observabilityRankingMinimumCoverage {
		reasons = append(reasons, "node score coverage is below 80%")
	}
	if cohort.UnscoredPositiveNodes > 0 {
		reasons = append(reasons, "one or more positive nodes have no valid max-gap score")
	}
	if len(reasons) > 0 {
		return "blocked_incomplete_signal", reasons
	}
	if cohort.PositiveNodeCount == 0 {
		return "no_positives", []string{"no eligible positive node label occurred inside this matured seven-day window"}
	}
	if cohort.ScoredNodeCount < OutcomeMinimumMaturedSamples {
		return "exploratory", []string{"fewer than 30 scored nodes are available"}
	}
	return "comparable", reasons
}

func observabilityTemporalConsistency(cohorts []ObservabilityRankingCohort) TemporalTrackConsistency {
	consistency := TemporalTrackConsistency{
		Metric: "node_top_5_percent_lift", PositiveDirectionRule: "lift_greater_than_1",
		MinimumIndependentCohorts: DualTrackMinimumConsistentCohorts, MinimumDirectionRatio: DualTrackMinimumDirectionRatio,
	}
	for _, cohort := range cohorts {
		if cohort.Status != "comparable" {
			continue
		}
		for _, metric := range cohort.RankingAtPercent {
			if metric.Percent == 5 && metric.Lift != nil {
				consistency.EvaluableIndependentCohorts++
				if *metric.Lift > 1 {
					consistency.PositiveDirectionCohorts++
				}
				break
			}
		}
	}
	finalizeTemporalConsistency(&consistency, "observability ranking consistency requires at least three independent evaluable cohorts")
	return consistency
}

func observabilityRankingReportStatus(report ObservabilityRankingValidationReport) (string, []string) {
	if report.CohortCount == 0 {
		return "blocked_no_mature_cohorts", []string{"no health evaluation run has completed the seven-day horizon and 24-hour negative censoring window"}
	}
	if report.PositiveCohortCount == 0 {
		return "collecting_no_positive_cohorts", []string{"mature independent cohorts exist, but none contains an eligible positive node label"}
	}
	for _, cohort := range report.Cohorts {
		if cohort.Status == "no_signal" || cohort.Status == "blocked_incomplete_signal" {
			return "exploratory", []string{"one or more mature cohorts has incomplete observability signal coverage"}
		}
	}
	if report.TemporalConsistency.Status != "consistent" {
		return "exploratory", append([]string{}, report.TemporalConsistency.BlockingReasons...)
	}
	return "comparable", []string{}
}

func nonNilRankingAtK(metrics []RankingAtK) []RankingAtK {
	if metrics == nil {
		return []RankingAtK{}
	}
	return metrics
}

func nonNilRankingAtPercent(metrics []RankingAtPercent) []RankingAtPercent {
	if metrics == nil {
		return []RankingAtPercent{}
	}
	return metrics
}

func observabilityRankingChecksum(report ObservabilityRankingValidationReport) string {
	fingerprint := report
	fingerprint.ReportSHA256 = ""
	fingerprint.GeneratedAt = time.Time{}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

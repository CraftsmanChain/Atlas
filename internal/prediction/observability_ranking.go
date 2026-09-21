package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

const (
	ObservabilityRankingValidationVersion = "prediction-observability-ranking-validation-v4"
	observabilityRankingHorizon           = 7 * 24 * time.Hour
	observabilityRankingNegativeCensor    = 24 * time.Hour
	observabilityRankingCohortLimit       = 12
	observabilityRankingMinimumCoverage   = 0.80
	observabilityRankingMinimumEntities   = 3
	observabilityMaxGapMetric             = "gpu_metric_gap_max_seconds_1h"
)

var ErrInvalidObservabilityAsOf = errors.New("as_of must be a nonzero RFC3339 timestamp no later than the current time")

const (
	observabilityMaxGapPolicy                = "max_gap"
	observabilityPresenceDeficitPolicy       = "presence_deficit"
	observabilitySampleAgePolicy             = "sample_age"
	observabilityPresenceFlapPolicy          = "presence_flap"
	observabilityScrapeFailurePolicy         = "scrape_success_deficit"
	observabilityScrapeSampleDeficitPolicy   = "scrape_samples_deficit"
	observabilityContinuityWorstMarginPolicy = "continuity_worst_margin"
	observabilityOverallHealthPolicy         = "overall_health_deficit"
	observabilityStabilityHealthPolicy       = "stability_health_deficit"
	observabilityMemoryHealthPolicy          = "memory_health_deficit"
	observabilityThermalHealthPolicy         = "thermal_health_deficit"
	observabilityPowerHealthPolicy           = "power_health_deficit"
	observabilityInterconnectHealthPolicy    = "interconnect_health_deficit"
	observabilityPerformanceHealthPolicy     = "performance_health_deficit"
)

const (
	observabilityStructuralPlane = "structural_observability"
	observabilityHealthPlane     = "health_score_components"
)

type observabilityPolicyDefinition struct {
	Name          string
	Plane         string
	Description   string
	SnapshotScore func(api.FloatMap) (float64, bool)
	HealthScore   func(api.GPUHealthScore) (float64, bool)
}

var observabilityPolicyDefinitions = []observabilityPolicyDefinition{
	{Name: observabilityMaxGapPolicy, Plane: observabilityStructuralPlane, Description: "node maximum GPU metric gap over the preceding hour", SnapshotScore: metricRiskScore(observabilityMaxGapMetric)},
	{Name: observabilityPresenceDeficitPolicy, Plane: observabilityStructuralPlane, Description: "node maximum deficit from 100% GPU metric presence over the preceding hour", SnapshotScore: deficitRiskScore("gpu_metric_presence_ratio_1h", 100)},
	{Name: observabilitySampleAgePolicy, Plane: observabilityStructuralPlane, Description: "node maximum age of the latest GPU metric sample", SnapshotScore: metricRiskScore("gpu_metric_sample_age_seconds")},
	{Name: observabilityPresenceFlapPolicy, Plane: observabilityStructuralPlane, Description: "node maximum GPU UUID presence flap count over the preceding hour", SnapshotScore: metricRiskScore("gpu_uuid_presence_flap_count_1h")},
	{Name: observabilityScrapeFailurePolicy, Plane: observabilityStructuralPlane, Description: "node maximum deficit from 100% DCGM target scrape success over five minutes", SnapshotScore: deficitRiskScore("target_scrape_success_ratio_5m", 100)},
	{Name: observabilityScrapeSampleDeficitPolicy, Plane: observabilityStructuralPlane, Description: "node maximum deficit from 100% DCGM target scrape sample ratio over five minutes", SnapshotScore: deficitRiskScore("target_scrape_samples_ratio_5m", 100)},
	{Name: observabilityContinuityWorstMarginPolicy, Plane: observabilityStructuralPlane, Description: "node worst normalized telemetry-continuity margin using existing degraded thresholds", SnapshotScore: continuityWorstMarginScore},
	{Name: observabilityOverallHealthPolicy, Plane: observabilityHealthPlane, Description: "node maximum deficit from the persisted overall GPU health score", HealthScore: overallHealthDeficitScore},
	{Name: observabilityStabilityHealthPolicy, Plane: observabilityHealthPlane, Description: "node maximum deficit from the persisted GPU stability component score", HealthScore: componentHealthDeficitScore(func(score api.GPUHealthScore) int { return score.StabilityScore })},
	{Name: observabilityMemoryHealthPolicy, Plane: observabilityHealthPlane, Description: "node maximum deficit from the persisted GPU memory component score", HealthScore: componentHealthDeficitScore(func(score api.GPUHealthScore) int { return score.MemoryScore })},
	{Name: observabilityThermalHealthPolicy, Plane: observabilityHealthPlane, Description: "node maximum deficit from the persisted GPU thermal component score", HealthScore: componentHealthDeficitScore(func(score api.GPUHealthScore) int { return score.ThermalScore })},
	{Name: observabilityPowerHealthPolicy, Plane: observabilityHealthPlane, Description: "node maximum deficit from the persisted GPU power component score", HealthScore: componentHealthDeficitScore(func(score api.GPUHealthScore) int { return score.PowerScore })},
	{Name: observabilityInterconnectHealthPolicy, Plane: observabilityHealthPlane, Description: "node maximum deficit from the persisted GPU interconnect component score", HealthScore: componentHealthDeficitScore(func(score api.GPUHealthScore) int { return score.InterconnectScore })},
	{Name: observabilityPerformanceHealthPolicy, Plane: observabilityHealthPlane, Description: "node maximum deficit from the persisted GPU performance component score", HealthScore: componentHealthDeficitScore(func(score api.GPUHealthScore) int { return score.PerformanceScore })},
}

type ObservabilityRankingSafety struct {
	ReadOnlyShadow   bool `json:"read_only_shadow"`
	NoAlertEmitted   bool `json:"no_alert_emitted"`
	NoActionExecuted bool `json:"no_action_executed"`
	ModelIndependent bool `json:"model_registry_independent"`
}

type ObservabilityRankingCohort struct {
	HealthEvaluationRunID uint                               `json:"health_evaluation_run_id"`
	CutoffAt              time.Time                          `json:"cutoff_at"`
	WindowEndAt           time.Time                          `json:"window_end_at"`
	Status                string                             `json:"status"`
	GPUCount              int                                `json:"gpu_count"`
	NodeCount             int                                `json:"node_count"`
	ScoredGPUCount        int                                `json:"scored_gpu_count"`
	ScoredNodeCount       int                                `json:"scored_node_count"`
	ScoreCoverage         float64                            `json:"score_coverage"`
	PositiveNodeCount     int                                `json:"positive_node_count"`
	ScoredPositiveNodes   int                                `json:"scored_positive_node_count"`
	UnscoredPositiveNodes int                                `json:"unscored_positive_node_count"`
	MatchedLabelCount     int                                `json:"matched_label_count"`
	PolicyResults         []ObservabilityRankingPolicyResult `json:"policy_results"`
	RankingAtK            []RankingAtK                       `json:"ranking_at_k"`
	RankingAtPercent      []RankingAtPercent                 `json:"ranking_at_percent"`
	BlockingReasons       []string                           `json:"blocking_reasons"`
	positiveEntityKeys    []string
	positiveEpisodeKeys   []string
}

type ObservabilityRankingPolicyResult struct {
	Policy                string             `json:"policy"`
	Plane                 string             `json:"plane"`
	Description           string             `json:"description"`
	Status                string             `json:"status"`
	ScoredGPUCount        int                `json:"scored_gpu_count"`
	ScoredNodeCount       int                `json:"scored_node_count"`
	DistinctScoreCount    int                `json:"distinct_score_count"`
	ScoreCoverage         float64            `json:"score_coverage"`
	PositiveNodeCount     int                `json:"positive_node_count"`
	ScoredPositiveNodes   int                `json:"scored_positive_node_count"`
	UnscoredPositiveNodes int                `json:"unscored_positive_node_count"`
	RankingAtK            []RankingAtK       `json:"ranking_at_k"`
	RankingAtPercent      []RankingAtPercent `json:"ranking_at_percent"`
	BlockingReasons       []string           `json:"blocking_reasons"`
}

type ObservabilityRankingPolicySummary struct {
	Policy                 string                   `json:"policy"`
	Plane                  string                   `json:"plane"`
	Description            string                   `json:"description"`
	BlockedPositiveCohorts int                      `json:"blocked_positive_cohorts"`
	TemporalConsistency    TemporalTrackConsistency `json:"temporal_consistency"`
}

type ObservabilityRankingValidationReport struct {
	CohortSelectionMode           string                              `json:"cohort_selection_mode"`
	EvaluationAsOf                time.Time                           `json:"evaluation_as_of"`
	ReplayURL                     string                              `json:"replay_url"`
	Version                       string                              `json:"version"`
	FrameworkVersion              string                              `json:"framework_version"`
	Mode                          string                              `json:"mode"`
	Status                        string                              `json:"status"`
	ReportSHA256                  string                              `json:"report_sha256"`
	Signal                        string                              `json:"signal"`
	PrimaryPolicy                 string                              `json:"primary_policy"`
	CandidatePolicy               string                              `json:"candidate_policy,omitempty"`
	ScoreSemantics                string                              `json:"score_semantics"`
	TargetHorizonMinutes          int                                 `json:"target_horizon_minutes"`
	NegativeCensorHours           int                                 `json:"negative_censor_hours"`
	CohortLimit                   int                                 `json:"cohort_limit"`
	MinimumScoreCoverage          float64                             `json:"minimum_score_coverage"`
	CohortCount                   int                                 `json:"cohort_count"`
	EvaluableCohortCount          int                                 `json:"evaluable_cohort_count"`
	PositiveCohortCount           int                                 `json:"positive_cohort_count"`
	MinimumUniquePositiveEntities int                                 `json:"minimum_unique_positive_entities"`
	UniquePositiveEntityCount     int                                 `json:"unique_positive_entity_count"`
	RecurrentPositiveEntityCount  int                                 `json:"recurrent_positive_entity_count"`
	PositiveEpisodeCount          int                                 `json:"positive_episode_count"`
	EvidenceIndependenceStatus    string                              `json:"evidence_independence_status"`
	EvidenceIndependenceBlockers  []string                            `json:"evidence_independence_blocking_reasons"`
	Cohorts                       []ObservabilityRankingCohort        `json:"cohorts"`
	PolicySummaries               []ObservabilityRankingPolicySummary `json:"policy_summaries"`
	TemporalConsistency           TemporalTrackConsistency            `json:"temporal_consistency"`
	Safety                        ObservabilityRankingSafety          `json:"safety"`
	BlockingReasons               []string                            `json:"blocking_reasons"`
	Interpretation                []string                            `json:"interpretation"`
	RecommendedNextRun            []string                            `json:"recommended_next_run"`
	GeneratedAt                   time.Time                           `json:"generated_at"`
}

func (s *Service) ObservabilityRankingValidationReport() (ObservabilityRankingValidationReport, error) {
	now := s.now()
	return s.observabilityRankingValidationReport(now, now, false)
}

// AsOf pins run maturity and label availability, not a historical database snapshot.
// Reviewed labels or backfilled records can still change the resulting evidence SHA.
func (s *Service) ObservabilityRankingValidationReportAsOf(asOf time.Time) (ObservabilityRankingValidationReport, error) {
	now := s.now()
	if asOf.IsZero() || asOf.After(now) {
		return ObservabilityRankingValidationReport{}, ErrInvalidObservabilityAsOf
	}
	return s.observabilityRankingValidationReport(asOf.UTC(), now, true)
}

func (s *Service) observabilityRankingValidationReport(asOf, generatedAt time.Time, fixed bool) (ObservabilityRankingValidationReport, error) {
	selectionMode := "rolling_latest_mature"
	if fixed {
		selectionMode = "fixed_as_of"
	}
	report := ObservabilityRankingValidationReport{
		CohortSelectionMode: selectionMode, EvaluationAsOf: asOf.UTC(),
		ReplayURL: "/api/v1/prediction/observability-ranking-validation?as_of=" + url.QueryEscape(asOf.UTC().Format(time.RFC3339Nano)),
		Version:   ObservabilityRankingValidationVersion, FrameworkVersion: FrameworkVersion,
		Mode: "read_only_prospective_health_run_signal_validation", Status: "blocked_no_mature_cohorts",
		Signal: observabilityMaxGapMetric, PrimaryPolicy: observabilityMaxGapPolicy,
		ScoreSemantics:       "relative_node_priority_not_absolute_failure_probability",
		TargetHorizonMinutes: int(observabilityRankingHorizon / time.Minute),
		NegativeCensorHours:  int(observabilityRankingNegativeCensor / time.Hour),
		CohortLimit:          observabilityRankingCohortLimit, MinimumScoreCoverage: observabilityRankingMinimumCoverage,
		MinimumUniquePositiveEntities: observabilityRankingMinimumEntities,
		Cohorts:                       []ObservabilityRankingCohort{}, PolicySummaries: []ObservabilityRankingPolicySummary{},
		EvidenceIndependenceBlockers: []string{},
		Safety:                       ObservabilityRankingSafety{ReadOnlyShadow: true, NoAlertEmitted: true, NoActionExecuted: true, ModelIndependent: true},
		Interpretation: []string{
			"as_of pins run maturity and label availability; without it the selected cohorts roll with request time",
			"fixed-time replay reads current persisted records, not a historical database snapshot; retain downloaded reports and SHA fingerprints to detect evidence changes",
			"each cohort is one persisted health evaluation run and uses only its point-in-time structural snapshots and health component scores",
			"fourteen fixed policies across two signal planes race on identical cohorts and labels; legacy top-level ranking fields remain bound to the primary max-gap policy",
			"positive outcomes are confirmed or strong-proxy labels inside the following seven days; negatives require an additional 24-hour censoring window",
			"candidate selection requires at least three unique positive GPU identities, with node identity used only when GPU identity is unavailable",
			"risk ranking is relative priority evidence and is not a calibrated hardware-failure probability",
		},
		RecommendedNextRun: []string{"continue collecting persisted health runs and reviewed failure labels without enabling alerts or actions"},
		GeneratedAt:        generatedAt,
	}

	runs, err := s.independentMatureHealthRuns(asOf)
	if err != nil {
		return report, err
	}
	for _, run := range runs {
		cohort, err := s.observabilityRankingCohort(run, asOf)
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
	observabilityEvidenceIndependence(&report)
	for _, definition := range observabilityPolicyDefinitions {
		report.PolicySummaries = append(report.PolicySummaries, observabilityPolicySummary(report.Cohorts, definition))
	}
	if summary, ok := observabilityPolicySummaryByName(report.PolicySummaries, report.PrimaryPolicy); ok {
		report.TemporalConsistency = summary.TemporalConsistency
	}
	report.CandidatePolicy = observabilityCandidatePolicy(report.PolicySummaries, report.EvidenceIndependenceStatus == "passed")
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
		Status:      "no_signal", PolicyResults: []ObservabilityRankingPolicyResult{},
		RankingAtK: []RankingAtK{}, RankingAtPercent: []RankingAtPercent{}, BlockingReasons: []string{},
	}
	var snapshots []api.GPUFeatureSnapshot
	if err := s.db.Where("evaluation_run_id = ?", run.ID).Order("node_ip ASC, gpu_index ASC, id ASC").Find(&snapshots).Error; err != nil {
		return cohort, err
	}
	cohort.GPUCount = len(snapshots)
	nodes := map[string]struct{}{}
	scoresByPolicy := make(map[string]map[string]float64, len(observabilityPolicyDefinitions))
	scoredGPUsByPolicy := make(map[string]int, len(observabilityPolicyDefinitions))
	for _, definition := range observabilityPolicyDefinitions {
		scoresByPolicy[definition.Name] = map[string]float64{}
	}
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
		for _, definition := range observabilityPolicyDefinitions {
			if definition.SnapshotScore == nil {
				continue
			}
			value, valid := definition.SnapshotScore(snapshot.Metrics)
			if !valid {
				continue
			}
			scoredGPUsByPolicy[definition.Name]++
			nodeScores := scoresByPolicy[definition.Name]
			if previous, exists := nodeScores[node]; !exists || value > previous {
				nodeScores[node] = value
			}
		}
	}
	var healthScores []api.GPUHealthScore
	if err := s.db.Where("evaluation_run_id = ? AND evaluated_at <= ?", run.ID, cutoff).
		Order("node_ip ASC, gpu_index ASC, id ASC").Find(&healthScores).Error; err != nil {
		return cohort, err
	}
	for _, score := range healthScores {
		node := strings.TrimSpace(score.NodeIP)
		if node == "" {
			continue
		}
		if _, exists := nodes[node]; !exists {
			continue
		}
		for _, definition := range observabilityPolicyDefinitions {
			if definition.HealthScore == nil {
				continue
			}
			value, valid := definition.HealthScore(score)
			if !valid {
				continue
			}
			scoredGPUsByPolicy[definition.Name]++
			nodeScores := scoresByPolicy[definition.Name]
			if previous, exists := nodeScores[node]; !exists || value > previous {
				nodeScores[node] = value
			}
		}
	}
	cohort.NodeCount = len(nodes)

	var labels []api.FailureLabel
	if err := s.db.Where("hardware_class = ? AND label_value = ? AND excluded = ? AND quality_tier IN ? AND occurred_at > ? AND occurred_at <= ? AND available_at <= ?",
		"gpu", 1, false, []string{"confirmed", "strong_proxy"}, cohort.CutoffAt, cohort.WindowEndAt, now).
		Order("occurred_at ASC, id ASC").Find(&labels).Error; err != nil {
		return cohort, err
	}
	positiveNodes := map[string]struct{}{}
	positiveEntities := map[string]struct{}{}
	positiveEpisodes := map[string]struct{}{}
	for _, label := range labels {
		node := ""
		gpu := strings.ToLower(strings.TrimSpace(label.GPUUUID))
		if gpu != "" {
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
		entity := "gpu:" + gpu
		if gpu == "" {
			entity = "node:" + strings.ToLower(node)
		}
		positiveEntities[entity] = struct{}{}
		positiveEpisodes[entity+"|"+label.OccurredAt.UTC().Format(time.RFC3339Nano)] = struct{}{}
		cohort.MatchedLabelCount++
	}
	cohort.PositiveNodeCount = len(positiveNodes)
	cohort.positiveEntityKeys = sortedStringSet(positiveEntities)
	cohort.positiveEpisodeKeys = sortedStringSet(positiveEpisodes)

	for _, definition := range observabilityPolicyDefinitions {
		result := buildObservabilityPolicyResult(definition, scoresByPolicy[definition.Name], scoredGPUsByPolicy[definition.Name], cohort.NodeCount, positiveNodes)
		cohort.PolicyResults = append(cohort.PolicyResults, result)
		if definition.Name == observabilityMaxGapPolicy {
			cohort.Status = result.Status
			cohort.ScoredGPUCount = result.ScoredGPUCount
			cohort.ScoredNodeCount = result.ScoredNodeCount
			cohort.ScoreCoverage = result.ScoreCoverage
			cohort.ScoredPositiveNodes = result.ScoredPositiveNodes
			cohort.UnscoredPositiveNodes = result.UnscoredPositiveNodes
			cohort.RankingAtK = result.RankingAtK
			cohort.RankingAtPercent = result.RankingAtPercent
			cohort.BlockingReasons = result.BlockingReasons
		}
	}
	return cohort, nil
}

func buildObservabilityPolicyResult(definition observabilityPolicyDefinition, nodeScores map[string]float64, scoredGPUCount, nodeCount int, positiveNodes map[string]struct{}) ObservabilityRankingPolicyResult {
	result := ObservabilityRankingPolicyResult{
		Policy: definition.Name, Plane: definition.Plane, Description: definition.Description, ScoredGPUCount: scoredGPUCount,
		ScoredNodeCount: len(nodeScores), PositiveNodeCount: len(positiveNodes),
		RankingAtK: []RankingAtK{}, RankingAtPercent: []RankingAtPercent{}, BlockingReasons: []string{},
	}
	if nodeCount > 0 {
		result.ScoreCoverage = float64(result.ScoredNodeCount) / float64(nodeCount)
	}
	nodeKeys := make([]string, 0, len(nodeScores))
	distinctScores := map[uint64]struct{}{}
	for node, score := range nodeScores {
		nodeKeys = append(nodeKeys, node)
		distinctScores[math.Float64bits(score)] = struct{}{}
	}
	result.DistinctScoreCount = len(distinctScores)
	sort.Strings(nodeKeys)
	items := make([]rankedOutcome, 0, len(nodeKeys))
	for _, node := range nodeKeys {
		actual := 0
		if _, positive := positiveNodes[node]; positive {
			actual = 1
			result.ScoredPositiveNodes++
		}
		items = append(items, rankedOutcome{probability: nodeScores[node], actual: actual})
	}
	result.UnscoredPositiveNodes = result.PositiveNodeCount - result.ScoredPositiveNodes
	result.RankingAtK = nonNilRankingAtK(rankingFromItems(items))
	result.RankingAtPercent = nonNilRankingAtPercent(rankingPercentFromItems(items))
	result.Status, result.BlockingReasons = observabilityPolicyResultStatus(result)
	return result
}

func observabilityPolicyResultStatus(result ObservabilityRankingPolicyResult) (string, []string) {
	reasons := []string{}
	if result.ScoredNodeCount == 0 {
		return "no_signal", []string{"no node has a valid persisted " + result.Policy + " score at the health-run cutoff"}
	}
	if result.ScoreCoverage < observabilityRankingMinimumCoverage {
		reasons = append(reasons, "node score coverage is below 80%")
	}
	if result.UnscoredPositiveNodes > 0 {
		reasons = append(reasons, "one or more positive nodes have no valid "+result.Policy+" score")
	}
	if len(reasons) > 0 {
		return "blocked_incomplete_signal", reasons
	}
	if result.PositiveNodeCount == 0 {
		return "no_positives", []string{"no eligible positive node label occurred inside this matured seven-day window"}
	}
	if result.DistinctScoreCount < 2 {
		return "no_discrimination", []string{"all scored nodes have the same policy score"}
	}
	if result.ScoredNodeCount < OutcomeMinimumMaturedSamples {
		return "exploratory", []string{"fewer than 30 scored nodes are available"}
	}
	return "comparable", reasons
}

func observabilityPolicySummary(cohorts []ObservabilityRankingCohort, definition observabilityPolicyDefinition) ObservabilityRankingPolicySummary {
	consistency := TemporalTrackConsistency{
		Metric: "node_top_5_percent_lift", PositiveDirectionRule: "lift_greater_than_1",
		MinimumIndependentCohorts: DualTrackMinimumConsistentCohorts, MinimumDirectionRatio: DualTrackMinimumDirectionRatio,
	}
	summary := ObservabilityRankingPolicySummary{Policy: definition.Name, Plane: definition.Plane, Description: definition.Description}
	for _, cohort := range cohorts {
		result, ok := observabilityPolicyResultByName(cohort.PolicyResults, definition.Name)
		if !ok {
			if cohort.PositiveNodeCount > 0 {
				summary.BlockedPositiveCohorts++
			}
			continue
		}
		if cohort.PositiveNodeCount > 0 && (result.Status == "no_signal" || result.Status == "no_discrimination" || result.Status == "blocked_incomplete_signal") {
			summary.BlockedPositiveCohorts++
		}
		if result.Status != "comparable" {
			continue
		}
		for _, metric := range result.RankingAtPercent {
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
	if summary.BlockedPositiveCohorts > 0 {
		consistency.Status = "blocked_incomplete_positive_cohorts"
		consistency.BlockingReasons = []string{"one or more positive cohorts lack complete, discriminating policy scores"}
	}
	summary.TemporalConsistency = consistency
	return summary
}

func observabilityPolicyResultByName(results []ObservabilityRankingPolicyResult, name string) (ObservabilityRankingPolicyResult, bool) {
	for _, result := range results {
		if result.Policy == name {
			return result, true
		}
	}
	return ObservabilityRankingPolicyResult{}, false
}

func observabilityPolicySummaryByName(summaries []ObservabilityRankingPolicySummary, name string) (ObservabilityRankingPolicySummary, bool) {
	for _, summary := range summaries {
		if summary.Policy == name {
			return summary, true
		}
	}
	return ObservabilityRankingPolicySummary{}, false
}

func observabilityCandidatePolicy(summaries []ObservabilityRankingPolicySummary, evidenceIndependent bool) string {
	if !evidenceIndependent {
		return ""
	}
	for _, definition := range observabilityPolicyDefinitions {
		summary, ok := observabilityPolicySummaryByName(summaries, definition.Name)
		if ok && summary.BlockedPositiveCohorts == 0 && summary.TemporalConsistency.Status == "consistent" {
			return definition.Name
		}
	}
	return ""
}

func observabilityEvidenceIndependence(report *ObservabilityRankingValidationReport) {
	entityCohorts := map[string]int{}
	episodes := map[string]struct{}{}
	for _, cohort := range report.Cohorts {
		for _, entity := range cohort.positiveEntityKeys {
			entityCohorts[entity]++
		}
		for _, episode := range cohort.positiveEpisodeKeys {
			episodes[episode] = struct{}{}
		}
	}
	report.UniquePositiveEntityCount = len(entityCohorts)
	report.PositiveEpisodeCount = len(episodes)
	for _, cohortCount := range entityCohorts {
		if cohortCount > 1 {
			report.RecurrentPositiveEntityCount++
		}
	}
	switch {
	case report.PositiveCohortCount == 0:
		report.EvidenceIndependenceStatus = "collecting_no_positive_entities"
	case report.UniquePositiveEntityCount < report.MinimumUniquePositiveEntities:
		report.EvidenceIndependenceStatus = "blocked_insufficient_unique_positive_entities"
		report.EvidenceIndependenceBlockers = []string{"fewer than three unique positive GPU or fallback node entities are represented"}
	default:
		report.EvidenceIndependenceStatus = "passed"
	}
}

func observabilityRankingReportStatus(report ObservabilityRankingValidationReport) (string, []string) {
	if report.CohortCount == 0 {
		return "blocked_no_mature_cohorts", []string{"no health evaluation run has completed the seven-day horizon and 24-hour negative censoring window"}
	}
	if report.PositiveCohortCount == 0 {
		return "collecting_no_positive_cohorts", []string{"mature independent cohorts exist, but none contains an eligible positive node label"}
	}
	if report.EvidenceIndependenceStatus != "passed" {
		return "exploratory", append([]string{}, report.EvidenceIndependenceBlockers...)
	}
	if report.CandidatePolicy == "" {
		return "exploratory", []string{"no fixed structural or health-score policy shows complete and stable positive direction across independent cohorts"}
	}
	return "comparable", []string{}
}

func sortedStringSet(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}

func metricRiskScore(metric string) func(api.FloatMap) (float64, bool) {
	return func(metrics api.FloatMap) (float64, bool) {
		return validNonnegativeMetric(metrics, metric)
	}
}

func deficitRiskScore(metric string, ceiling float64) func(api.FloatMap) (float64, bool) {
	return func(metrics api.FloatMap) (float64, bool) {
		value, valid := validNonnegativeMetric(metrics, metric)
		if !valid || ceiling <= 0 {
			return 0, false
		}
		if value > ceiling {
			value = ceiling
		}
		return ceiling - value, true
	}
}

func overallHealthDeficitScore(score api.GPUHealthScore) (float64, bool) {
	if score.Score == nil {
		return 0, false
	}
	return boundedHealthDeficit(*score.Score)
}

func componentHealthDeficitScore(component func(api.GPUHealthScore) int) func(api.GPUHealthScore) (float64, bool) {
	return func(score api.GPUHealthScore) (float64, bool) {
		if score.Score == nil {
			return 0, false
		}
		return boundedHealthDeficit(component(score))
	}
}

func boundedHealthDeficit(value int) (float64, bool) {
	if value < 0 {
		return 0, false
	}
	if value > 100 {
		value = 100
	}
	return float64(100 - value), true
}

func validNonnegativeMetric(metrics api.FloatMap, metric string) (float64, bool) {
	value, exists := metrics[metric]
	if !exists || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, false
	}
	if value == 0 {
		return 0, true // Normalize negative zero so distinct-score audits stay deterministic.
	}
	return value, true
}

func continuityWorstMarginScore(metrics api.FloatMap) (float64, bool) {
	presence, hasPresence := validNonnegativeMetric(metrics, "gpu_metric_presence_ratio_1h")
	age, hasAge := validNonnegativeMetric(metrics, "gpu_metric_sample_age_seconds")
	if !hasPresence || !hasAge {
		return 0, false
	}
	if presence > 100 {
		presence = 100
	}
	scores := []float64{(100 - presence) / 5, age / 60}
	optional := []struct {
		metric    string
		threshold float64
		deficit   bool
	}{
		{metric: observabilityMaxGapMetric, threshold: 45},
		{metric: "gpu_uuid_presence_flap_count_1h", threshold: 1},
		{metric: "target_scrape_success_ratio_5m", threshold: 5, deficit: true},
		{metric: "target_scrape_samples_ratio_5m", threshold: 20, deficit: true},
	}
	for _, item := range optional {
		value, valid := validNonnegativeMetric(metrics, item.metric)
		if !valid {
			continue
		}
		if item.deficit {
			if value > 100 {
				value = 100
			}
			value = 100 - value
		}
		scores = append(scores, value/item.threshold)
	}
	return slicesMax(scores), true
}

func slicesMax(values []float64) float64 {
	maximum := values[0]
	for _, value := range values[1:] {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
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
	fingerprint.ReplayURL = ""
	// Preserve rolling ETags while evidence is unchanged; fixed replay binds its
	// requested evaluation time, even if another time happens to select the same runs.
	if fingerprint.CohortSelectionMode == "rolling_latest_mature" {
		fingerprint.EvaluationAsOf = time.Time{}
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

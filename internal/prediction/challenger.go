package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"

	"atlas/pkg/api"
)

const HeaRankChallengerReportVersion = "hearank-challenger-report-v13"

const (
	HeaRankMinimumSevenDayRows      = 30
	HeaRankMinimumSevenDayNodes     = 10
	HeaRankMinimumSevenDayPositives = 3
	HeaRankHistoryHalfLifeDays      = 90
	HeaRankMinimumSignalRows        = 3
	HeaRankMinimumSignalNodes       = 2
	HeaRankOperationalSignalMaxAge  = 24 * time.Hour
)

type ChallengerMetricSet struct {
	Policy               string       `json:"policy"`
	Description          string       `json:"description"`
	Rows                 int          `json:"rows"`
	Nodes                int          `json:"nodes"`
	Positives            int          `json:"positives"`
	NonZeroScoreRows     int          `json:"non_zero_score_rows"`
	NonZeroScoreNodes    int          `json:"non_zero_score_nodes"`
	SignalCoverageStatus string       `json:"signal_coverage_status"`
	RankingAtK           []RankingAtK `json:"ranking_at_k"`
}

type ChallengerPolicyComparison struct {
	ReferencePolicy  string `json:"reference_policy"`
	ChallengerPolicy string `json:"challenger_policy"`
	Status           string `json:"status"`
	Reason           string `json:"reason"`
}

type HeaRankChallengerReport struct {
	Version                  string                       `json:"version"`
	FrameworkVersion         string                       `json:"framework_version"`
	Mode                     string                       `json:"mode"`
	Status                   string                       `json:"status"`
	ReportSHA256             string                       `json:"report_sha256"`
	ConfidenceStatus         string                       `json:"confidence_status"`
	TargetHorizonMinutes     int                          `json:"target_horizon_minutes"`
	MinimumSevenDayRows      int                          `json:"minimum_seven_day_rows"`
	MinimumSevenDayNodes     int                          `json:"minimum_seven_day_nodes"`
	MinimumSevenDayPositives int                          `json:"minimum_seven_day_positives"`
	SampleSummary            OutcomeMaturity              `json:"sample_summary"`
	AllMatured               []ChallengerMetricSet        `json:"all_matured"`
	SevenDay                 []ChallengerMetricSet        `json:"seven_day"`
	PolicyComparisons        []ChallengerPolicyComparison `json:"policy_comparisons"`
	BlockingReasons          []string                     `json:"blocking_reasons"`
	Interpretation           []string                     `json:"interpretation"`
	RecommendedNextRun       []string                     `json:"recommended_next_run"`
	GeneratedAt              time.Time                    `json:"generated_at"`
}

func (s *Service) HeaRankChallengerReport() (HeaRankChallengerReport, error) {
	var rows []api.PredictionOutcomeEvaluation
	if err := s.db.Order("prediction_evaluated_at ASC, id ASC").Find(&rows).Error; err != nil {
		return HeaRankChallengerReport{}, err
	}
	evaluationRows := challengerEvaluationRows(rows, 0)
	labels, healthScores, ruleHits, err := s.loadChallengerEvidence(evaluationRows)
	if err != nil {
		return HeaRankChallengerReport{}, err
	}
	report := HeaRankChallengerReport{
		Version:                  HeaRankChallengerReportVersion,
		FrameworkVersion:         FrameworkVersion,
		Mode:                     "offline_challenger_read_only",
		TargetHorizonMinutes:     10080,
		MinimumSevenDayRows:      HeaRankMinimumSevenDayRows,
		MinimumSevenDayNodes:     HeaRankMinimumSevenDayNodes,
		MinimumSevenDayPositives: HeaRankMinimumSevenDayPositives,
		SampleSummary:            outcomeMaturity(rows),
		GeneratedAt:              s.now(),
		Interpretation: []string{
			"This is a node-risk challenger scaffold, not a released HeaRank model.",
			"All policies are evaluated on mature scored outcomes only; pending and censored rows are excluded.",
			"Seven-day rows are reported separately because HeaRank validation should target 7d node risk.",
			"Historical outcome windows and labels must close or become available strictly before the prediction cutoff; equal-timestamp evidence is excluded.",
			"A policy with no_signal or exploratory signal coverage must not be interpreted as a comparable historical-risk ranking result.",
		},
		RecommendedNextRun: []string{
			"Accumulate enough mature 7d node outcomes before training a HeaRank-style MLP.",
			"Use the same node-level Ranking@K denominator when comparing Logistic, historical-risk baselines, and future HeaRank scores.",
			"Keep the challenger offline until time-split, leakage, and calibration checks pass.",
			"Accumulate eligible historical evidence before interpreting policies whose signal coverage is no_signal or exploratory.",
		},
	}
	report.AllMatured = challengerMetricSets(rows, labels, healthScores, ruleHits, 0)
	report.SevenDay = challengerMetricSets(rows, labels, healthScores, ruleHits, report.TargetHorizonMinutes)
	rows7d, nodes7d, positives7d := validationReadinessSevenDaySummary(report.SevenDay)
	report.ConfidenceStatus, report.BlockingReasons = heaRankConfidence(rows7d, nodes7d, positives7d)
	if rows7d == 0 {
		report.Status = "blocked_no_7d_mature_outcomes"
	} else if report.ConfidenceStatus == "insufficient_sample" {
		report.Status = "blocked_insufficient_7d_sample"
	} else {
		report.Status = "ready_for_offline_comparison"
	}
	report.PolicyComparisons = challengerPolicyComparisons(report.SevenDay, report.ConfidenceStatus)
	report.ReportSHA256 = heaRankChallengerChecksum(report)
	return report, nil
}

func (s *Service) loadChallengerEvidence(rows []api.PredictionOutcomeEvaluation) ([]api.FailureLabel, []api.GPUHealthScore, []api.GPUHealthRuleHit, error) {
	windowStart, windowEnd, found := challengerEvidenceQueryWindow(rows)
	if !found {
		return nil, nil, nil, nil
	}
	var labels []api.FailureLabel
	if err := s.db.Where("occurred_at < ? AND available_at < ?", windowEnd, windowEnd).Order("available_at ASC, id ASC").Find(&labels).Error; err != nil {
		return nil, nil, nil, err
	}
	var healthScores []api.GPUHealthScore
	if err := s.db.Where("evaluated_at >= ? AND evaluated_at < ?", windowStart, windowEnd).Order("evaluated_at ASC, id ASC").Find(&healthScores).Error; err != nil {
		return nil, nil, nil, err
	}
	var ruleHits []api.GPUHealthRuleHit
	if err := s.db.Where("evaluated_at >= ? AND evaluated_at < ?", windowStart, windowEnd).Order("evaluated_at ASC, id ASC").Find(&ruleHits).Error; err != nil {
		return nil, nil, nil, err
	}
	return labels, healthScores, ruleHits, nil
}

func challengerEvidenceQueryWindow(rows []api.PredictionOutcomeEvaluation) (time.Time, time.Time, bool) {
	var earliest, latest time.Time
	for _, row := range rows {
		cutoff := row.PredictionEvaluatedAt
		if cutoff.IsZero() {
			continue
		}
		if earliest.IsZero() || cutoff.Before(earliest) {
			earliest = cutoff
		}
		if latest.IsZero() || cutoff.After(latest) {
			latest = cutoff
		}
	}
	if earliest.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	return earliest.Add(-HeaRankOperationalSignalMaxAge), latest, true
}

func heaRankChallengerChecksum(report HeaRankChallengerReport) string {
	fingerprint := struct {
		Version                  string                       `json:"version"`
		FrameworkVersion         string                       `json:"framework_version"`
		Mode                     string                       `json:"mode"`
		Status                   string                       `json:"status"`
		ConfidenceStatus         string                       `json:"confidence_status"`
		TargetHorizonMinutes     int                          `json:"target_horizon_minutes"`
		MinimumSevenDayRows      int                          `json:"minimum_seven_day_rows"`
		MinimumSevenDayNodes     int                          `json:"minimum_seven_day_nodes"`
		MinimumSevenDayPositives int                          `json:"minimum_seven_day_positives"`
		SampleSummary            OutcomeMaturity              `json:"sample_summary"`
		AllMatured               []ChallengerMetricSet        `json:"all_matured"`
		SevenDay                 []ChallengerMetricSet        `json:"seven_day"`
		PolicyComparisons        []ChallengerPolicyComparison `json:"policy_comparisons"`
		BlockingReasons          []string                     `json:"blocking_reasons"`
		Interpretation           []string                     `json:"interpretation"`
		RecommendedNextRun       []string                     `json:"recommended_next_run"`
	}{
		Version: report.Version, FrameworkVersion: report.FrameworkVersion, Mode: report.Mode, Status: report.Status,
		ConfidenceStatus: report.ConfidenceStatus, TargetHorizonMinutes: report.TargetHorizonMinutes,
		MinimumSevenDayRows: report.MinimumSevenDayRows, MinimumSevenDayNodes: report.MinimumSevenDayNodes,
		MinimumSevenDayPositives: report.MinimumSevenDayPositives, SampleSummary: report.SampleSummary,
		AllMatured: report.AllMatured, SevenDay: report.SevenDay, PolicyComparisons: report.PolicyComparisons,
		BlockingReasons: report.BlockingReasons, Interpretation: report.Interpretation, RecommendedNextRun: report.RecommendedNextRun,
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func challengerPolicyComparisons(rows []ChallengerMetricSet, confidence string) []ChallengerPolicyComparison {
	byPolicy := map[string]ChallengerMetricSet{}
	for _, row := range rows {
		byPolicy[row.Policy] = row
	}
	reference, found := byPolicy["logistic_probability"]
	comparisons := make([]ChallengerPolicyComparison, 0, len(rows))
	for _, row := range rows {
		if row.Policy == "logistic_probability" {
			continue
		}
		comparison := ChallengerPolicyComparison{ReferencePolicy: "logistic_probability", ChallengerPolicy: row.Policy}
		switch {
		case confidence == "insufficient_sample":
			comparison.Status, comparison.Reason = "blocked_insufficient_sample", "7d mature outcome sample does not meet comparison gates"
		case !found || reference.SignalCoverageStatus != "covered":
			comparison.Status, comparison.Reason = "blocked_reference_signal", "Logistic reference has insufficient non-zero signal coverage"
		case row.SignalCoverageStatus != "covered":
			comparison.Status, comparison.Reason = "blocked_challenger_signal", "challenger has insufficient non-zero signal coverage"
		case confidence == "exploratory":
			comparison.Status, comparison.Reason = "exploratory", "sample passes minimum gates but remains below comparable-volume guidance"
		default:
			comparison.Status, comparison.Reason = "comparable", "same 7d denominator and covered signals"
		}
		comparisons = append(comparisons, comparison)
	}
	return comparisons
}

func heaRankConfidence(rows, nodes, positives int) (string, []string) {
	reasons := []string{}
	if rows == 0 {
		reasons = append(reasons, "no mature scored 7d node-risk outcomes are available")
	}
	if rows < HeaRankMinimumSevenDayRows {
		reasons = append(reasons, "7d mature scored rows below comparison gate")
	}
	if nodes < HeaRankMinimumSevenDayNodes {
		reasons = append(reasons, "7d unique node count below comparison gate")
	}
	if positives < HeaRankMinimumSevenDayPositives {
		reasons = append(reasons, "7d positive outcome count below comparison gate")
	}
	if len(reasons) > 0 {
		return "insufficient_sample", uniqueSorted(reasons)
	}
	if rows < HeaRankMinimumSevenDayRows*3 || positives < HeaRankMinimumSevenDayPositives*3 {
		return "exploratory", nil
	}
	return "comparable", nil
}

func challengerMetricSets(rows []api.PredictionOutcomeEvaluation, labels []api.FailureLabel, healthScores []api.GPUHealthScore, ruleHits []api.GPUHealthRuleHit, horizonMinutes int) []ChallengerMetricSet {
	histories := challengerHistoriesForCutoffs(rows, challengerEvaluationRows(rows, horizonMinutes), labels, healthScores, ruleHits)
	return []ChallengerMetricSet{
		challengerMetricSet(rows, histories, horizonMinutes, "logistic_probability", "current shadow probability aggregated by node", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			if row.Probability == nil {
				return 0
			}
			return *row.Probability
		}),
		challengerMetricSet(rows, histories, horizonMinutes, "health_score_risk_prior", "node maximum risk derived from each GPU's latest health score strictly before the prediction cutoff", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			return prior.HealthRiskByNode[normalNode(row.NodeIP)]
		}),
		challengerMetricSet(rows, histories, horizonMinutes, "rule_hit_risk_prior", "node maximum severity-weighted risk from each GPU's latest health-rule-hit batch strictly before the prediction cutoff", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			return prior.RuleHitRiskByNode[normalNode(row.NodeIP)]
		}),
		challengerMetricSet(rows, histories, horizonMinutes, "model_label_density_prior", "node maximum eligible historical label-event density for its GPU model cohort strictly before the prediction cutoff", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			return prior.ModelLabelDensityByNode[normalNode(row.NodeIP)]
		}),
		challengerMetricSet(rows, histories, horizonMinutes, "failure_count_prior", "node positive outcomes whose full evaluation window closed before the prediction cutoff", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			return float64(prior.PositiveCounts[normalNode(row.NodeIP)])
		}),
		challengerMetricSet(rows, histories, horizonMinutes, "recency_weighted_failure_prior", "node prior positive outcomes exponentially decayed by a 90-day half-life; only closed evaluation windows are used", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			return prior.RecencyWeighted[normalNode(row.NodeIP)]
		}),
		challengerMetricSet(rows, histories, horizonMinutes, "severity_weighted_label_history", "node confirmed or strong-proxy labels available before the prediction cutoff, weighted critical=3, xid/thermal-critical=2, other eligible=1", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			return prior.SeverityWeightedLabels[normalNode(row.NodeIP)]
		}),
		challengerMetricSet(rows, histories, horizonMinutes, "threshold_binary", "released decision threshold converted to a node-level binary score", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			if row.PredictedPositive {
				return 1
			}
			return 0
		}),
	}
}

func challengerEvaluationRows(rows []api.PredictionOutcomeEvaluation, horizonMinutes int) []api.PredictionOutcomeEvaluation {
	evaluationRows := make([]api.PredictionOutcomeEvaluation, 0, len(rows))
	for _, row := range rows {
		if row.MaturityStatus != "matured" || row.Probability == nil || row.FinalActualValue == nil || strings.TrimSpace(row.NodeIP) == "" {
			continue
		}
		if horizonMinutes > 0 && row.HorizonMinutes != horizonMinutes {
			continue
		}
		evaluationRows = append(evaluationRows, row)
	}
	return evaluationRows
}

type challengerHistory struct {
	PositiveCounts          map[string]int
	RecencyWeighted         map[string]float64
	SeverityWeightedLabels  map[string]float64
	HealthRiskByNode        map[string]float64
	RuleHitRiskByNode       map[string]float64
	ModelLabelDensityByNode map[string]float64
}

func challengerMetricSet(rows []api.PredictionOutcomeEvaluation, histories map[time.Time]challengerHistory, horizonMinutes int, policy, description string, score func(api.PredictionOutcomeEvaluation, challengerHistory) float64) ChallengerMetricSet {
	items := make([]rankedOutcome, 0, len(rows))
	nodes := map[string]struct{}{}
	nonZeroNodes := map[string]struct{}{}
	nonZeroRows := 0
	for _, row := range rows {
		if row.MaturityStatus != "matured" || row.Probability == nil || row.FinalActualValue == nil || strings.TrimSpace(row.NodeIP) == "" {
			continue
		}
		if horizonMinutes > 0 && row.HorizonMinutes != horizonMinutes {
			continue
		}
		node := normalNode(row.NodeIP)
		nodes[node] = struct{}{}
		scoreValue := score(row, histories[row.PredictionEvaluatedAt])
		if math.Abs(scoreValue) > 1e-12 {
			nonZeroRows++
			nonZeroNodes[node] = struct{}{}
		}
		items = append(items, rankedOutcome{probability: scoreValue, actual: *row.FinalActualValue})
	}
	positives := 0
	for _, item := range items {
		if item.actual == 1 {
			positives++
		}
	}
	return ChallengerMetricSet{
		Policy: policy, Description: description, Rows: len(items), Nodes: len(nodes), Positives: positives,
		NonZeroScoreRows: nonZeroRows, NonZeroScoreNodes: len(nonZeroNodes), SignalCoverageStatus: challengerSignalCoverageStatus(nonZeroRows, len(nonZeroNodes)),
		RankingAtK: rankingFromItems(items),
	}
}

func challengerHistoriesByCutoff(rows []api.PredictionOutcomeEvaluation, labels []api.FailureLabel, healthScores []api.GPUHealthScore, ruleHits []api.GPUHealthRuleHit) map[time.Time]challengerHistory {
	return challengerHistoriesForCutoffs(rows, rows, labels, healthScores, ruleHits)
}

func challengerHistoriesForCutoffs(historyRows, cutoffRows []api.PredictionOutcomeEvaluation, labels []api.FailureLabel, healthScores []api.GPUHealthScore, ruleHits []api.GPUHealthRuleHit) map[time.Time]challengerHistory {
	histories := make(map[time.Time]challengerHistory, len(cutoffRows))
	for _, row := range cutoffRows {
		cutoff := row.PredictionEvaluatedAt
		if _, found := histories[cutoff]; found {
			continue
		}
		histories[cutoff] = challengerHistoryWithEvidenceBefore(historyRows, labels, healthScores, ruleHits, cutoff)
	}
	return histories
}

func challengerSignalCoverageStatus(rows, nodes int) string {
	if rows == 0 {
		return "no_signal"
	}
	if rows < HeaRankMinimumSignalRows || nodes < HeaRankMinimumSignalNodes {
		return "exploratory"
	}
	return "covered"
}

func challengerHistoryBefore(rows []api.PredictionOutcomeEvaluation, cutoff time.Time) challengerHistory {
	return challengerHistoryWithEvidenceBefore(rows, nil, nil, nil, cutoff)
}

func challengerHistoryWithLabelsBefore(rows []api.PredictionOutcomeEvaluation, labels []api.FailureLabel, cutoff time.Time) challengerHistory {
	return challengerHistoryWithEvidenceBefore(rows, labels, nil, nil, cutoff)
}

func challengerHistoryWithEvidenceBefore(rows []api.PredictionOutcomeEvaluation, labels []api.FailureLabel, healthScores []api.GPUHealthScore, ruleHits []api.GPUHealthRuleHit, cutoff time.Time) challengerHistory {
	history := challengerHistory{PositiveCounts: map[string]int{}, RecencyWeighted: map[string]float64{}, SeverityWeightedLabels: map[string]float64{}, HealthRiskByNode: healthRiskByNodeBefore(healthScores, cutoff), RuleHitRiskByNode: ruleHitRiskByNodeBefore(ruleHits, healthScores, cutoff), ModelLabelDensityByNode: modelLabelDensityByNodeBefore(labels, healthScores, cutoff)}
	for _, candidate := range rows {
		if candidate.MaturityStatus != "matured" || candidate.FinalActualValue == nil || *candidate.FinalActualValue != 1 || strings.TrimSpace(candidate.NodeIP) == "" || !candidate.WindowEndAt.Before(cutoff) {
			continue
		}
		node := normalNode(candidate.NodeIP)
		history.PositiveCounts[node]++
		elapsedDays := cutoff.Sub(candidate.WindowEndAt).Hours() / 24
		if elapsedDays < 0 {
			continue
		}
		history.RecencyWeighted[node] += math.Exp(-math.Ln2 * elapsedDays / HeaRankHistoryHalfLifeDays)
	}
	for _, label := range labels {
		if !eligibleSeverityHistoryLabel(label, cutoff) {
			continue
		}
		history.SeverityWeightedLabels[normalNode(label.NodeIP)] += severityHistoryWeight(label.EventType)
	}
	return history
}

func healthRiskByNodeBefore(scores []api.GPUHealthScore, cutoff time.Time) map[string]float64 {
	latestByGPU := map[string]api.GPUHealthScore{}
	for _, score := range scores {
		if score.Score == nil || strings.TrimSpace(score.NodeIP) == "" || strings.TrimSpace(score.GPUUUID) == "" || !operationalSignalFresh(score.EvaluatedAt, cutoff) {
			continue
		}
		key := normalNode(score.NodeIP) + "|" + strings.ToLower(strings.TrimSpace(score.GPUUUID))
		previous, found := latestByGPU[key]
		if !found || score.EvaluatedAt.After(previous.EvaluatedAt) || (score.EvaluatedAt.Equal(previous.EvaluatedAt) && score.ID > previous.ID) {
			latestByGPU[key] = score
		}
	}
	riskByNode := map[string]float64{}
	for _, score := range latestByGPU {
		risk := math.Max(0, math.Min(100, 100-float64(*score.Score)))
		node := normalNode(score.NodeIP)
		if risk > riskByNode[node] {
			riskByNode[node] = risk
		}
	}
	return riskByNode
}

func ruleHitRiskByNodeBefore(hits []api.GPUHealthRuleHit, scores []api.GPUHealthScore, cutoff time.Time) map[string]float64 {
	type latestRisk struct {
		node        string
		evaluatedAt time.Time
		risk        float64
	}
	scoreByID := map[uint]api.GPUHealthScore{}
	for _, score := range scores {
		scoreByID[score.ID] = score
	}
	latestByGPU := map[string]latestRisk{}
	for _, hit := range hits {
		score, found := scoreByID[hit.HealthScoreID]
		if !found || strings.TrimSpace(score.NodeIP) == "" || strings.TrimSpace(hit.GPUUUID) == "" || !operationalSignalFresh(score.EvaluatedAt, cutoff) || !operationalSignalFresh(hit.EvaluatedAt, cutoff) {
			continue
		}
		key := normalNode(score.NodeIP) + "|" + strings.ToLower(strings.TrimSpace(hit.GPUUUID))
		previous, found := latestByGPU[key]
		weight := ruleHitSeverityWeight(hit.Severity)
		if !found || hit.EvaluatedAt.After(previous.evaluatedAt) {
			latestByGPU[key] = latestRisk{node: normalNode(score.NodeIP), evaluatedAt: hit.EvaluatedAt, risk: weight}
		} else if hit.EvaluatedAt.Equal(previous.evaluatedAt) {
			previous.risk += weight
			latestByGPU[key] = previous
		}
	}
	riskByNode := map[string]float64{}
	for _, risk := range latestByGPU {
		if risk.risk > riskByNode[risk.node] {
			riskByNode[risk.node] = risk.risk
		}
	}
	return riskByNode
}

func ruleHitSeverityWeight(severity string) float64 {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return 3
	case "warning":
		return 2
	default:
		return 1
	}
}

func modelLabelDensityByNodeBefore(labels []api.FailureLabel, scores []api.GPUHealthScore, cutoff time.Time) map[string]float64 {
	latestByGPU := map[string]api.GPUHealthScore{}
	for _, score := range scores {
		if strings.TrimSpace(score.NodeIP) == "" || strings.TrimSpace(score.GPUUUID) == "" || strings.TrimSpace(score.ModelName) == "" || !operationalSignalFresh(score.EvaluatedAt, cutoff) {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(score.GPUUUID))
		previous, found := latestByGPU[key]
		if !found || score.EvaluatedAt.After(previous.EvaluatedAt) || (score.EvaluatedAt.Equal(previous.EvaluatedAt) && score.ID > previous.ID) {
			latestByGPU[key] = score
		}
	}
	observedGPUsByModel := map[string]int{}
	for _, score := range latestByGPU {
		observedGPUsByModel[normalModel(score.ModelName)]++
	}
	labelsByModel := map[string]int{}
	for _, label := range labels {
		if !eligibleSeverityHistoryLabel(label, cutoff) || strings.TrimSpace(label.ModelName) == "" {
			continue
		}
		labelsByModel[normalModel(label.ModelName)]++
	}
	densityByModel := map[string]float64{}
	for model, labels := range labelsByModel {
		if observed := observedGPUsByModel[model]; observed > 0 {
			densityByModel[model] = float64(labels) / float64(observed)
		}
	}
	densityByNode := map[string]float64{}
	for _, score := range latestByGPU {
		node := normalNode(score.NodeIP)
		if density := densityByModel[normalModel(score.ModelName)]; density > densityByNode[node] {
			densityByNode[node] = density
		}
	}
	return densityByNode
}

func normalModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func operationalSignalFresh(observedAt, cutoff time.Time) bool {
	return observedAt.Before(cutoff) && cutoff.Sub(observedAt) <= HeaRankOperationalSignalMaxAge
}

func eligibleSeverityHistoryLabel(label api.FailureLabel, cutoff time.Time) bool {
	if label.LabelValue != 1 || label.Excluded || strings.TrimSpace(label.NodeIP) == "" || !label.AvailableAt.Before(cutoff) || !label.OccurredAt.Before(cutoff) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(label.QualityTier)) {
	case "confirmed", "strong_proxy":
		return true
	default:
		return false
	}
}

func severityHistoryWeight(eventType string) float64 {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "uncorrectable_remapped_rows_growth", "row_remap_failure", "recent_uncorrected_ecc", "ecc_dbe", "xid_critical", "xid_repeated":
		return 3
	case "gpu_temp_sustained_5m_critical", "recent_xid_change":
		return 2
	default:
		return 1
	}
}

func matureCount(rows []api.PredictionOutcomeEvaluation, horizonMinutes int) int {
	count := 0
	for _, row := range rows {
		if row.MaturityStatus == "matured" && row.Probability != nil && row.FinalActualValue != nil && strings.TrimSpace(row.NodeIP) != "" && (horizonMinutes == 0 || row.HorizonMinutes == horizonMinutes) {
			count++
		}
	}
	return count
}

func normalNode(node string) string {
	return strings.ToLower(strings.TrimSpace(node))
}

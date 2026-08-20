package prediction

import (
	"math"
	"strings"
	"time"

	"atlas/pkg/api"
)

const HeaRankChallengerReportVersion = "hearank-challenger-report-v4"

const (
	HeaRankMinimumSevenDayRows      = 30
	HeaRankMinimumSevenDayNodes     = 10
	HeaRankMinimumSevenDayPositives = 3
	HeaRankHistoryHalfLifeDays      = 90
	HeaRankMinimumSignalRows        = 3
	HeaRankMinimumSignalNodes       = 2
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

type HeaRankChallengerReport struct {
	Version                  string                `json:"version"`
	FrameworkVersion         string                `json:"framework_version"`
	Mode                     string                `json:"mode"`
	Status                   string                `json:"status"`
	ConfidenceStatus         string                `json:"confidence_status"`
	TargetHorizonMinutes     int                   `json:"target_horizon_minutes"`
	MinimumSevenDayRows      int                   `json:"minimum_seven_day_rows"`
	MinimumSevenDayNodes     int                   `json:"minimum_seven_day_nodes"`
	MinimumSevenDayPositives int                   `json:"minimum_seven_day_positives"`
	SampleSummary            OutcomeMaturity       `json:"sample_summary"`
	AllMatured               []ChallengerMetricSet `json:"all_matured"`
	SevenDay                 []ChallengerMetricSet `json:"seven_day"`
	BlockingReasons          []string              `json:"blocking_reasons"`
	Interpretation           []string              `json:"interpretation"`
	RecommendedNextRun       []string              `json:"recommended_next_run"`
	GeneratedAt              time.Time             `json:"generated_at"`
}

func (s *Service) HeaRankChallengerReport() (HeaRankChallengerReport, error) {
	var rows []api.PredictionOutcomeEvaluation
	if err := s.db.Order("prediction_evaluated_at ASC, id ASC").Find(&rows).Error; err != nil {
		return HeaRankChallengerReport{}, err
	}
	var labels []api.FailureLabel
	if err := s.db.Order("available_at ASC, id ASC").Find(&labels).Error; err != nil {
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
			"A policy with no_signal or exploratory signal coverage must not be interpreted as a comparable historical-risk ranking result.",
		},
		RecommendedNextRun: []string{
			"Accumulate enough mature 7d node outcomes before training a HeaRank-style MLP.",
			"Use the same node-level Ranking@K denominator when comparing Logistic, historical-risk baselines, and future HeaRank scores.",
			"Keep the challenger offline until time-split, leakage, and calibration checks pass.",
			"Accumulate eligible historical evidence before interpreting policies whose signal coverage is no_signal or exploratory.",
		},
	}
	report.AllMatured = challengerMetricSets(rows, labels, 0)
	report.SevenDay = challengerMetricSets(rows, labels, report.TargetHorizonMinutes)
	rows7d, nodes7d, positives7d := validationReadinessSevenDaySummary(report.SevenDay)
	report.ConfidenceStatus, report.BlockingReasons = heaRankConfidence(rows7d, nodes7d, positives7d)
	if rows7d == 0 {
		report.Status = "blocked_no_7d_mature_outcomes"
	} else if report.ConfidenceStatus == "insufficient_sample" {
		report.Status = "blocked_insufficient_7d_sample"
	} else {
		report.Status = "ready_for_offline_comparison"
	}
	return report, nil
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

func challengerMetricSets(rows []api.PredictionOutcomeEvaluation, labels []api.FailureLabel, horizonMinutes int) []ChallengerMetricSet {
	return []ChallengerMetricSet{
		challengerMetricSet(rows, labels, horizonMinutes, "logistic_probability", "current shadow probability aggregated by node", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			if row.Probability == nil {
				return 0
			}
			return *row.Probability
		}),
		challengerMetricSet(rows, labels, horizonMinutes, "failure_count_prior", "node positive outcomes whose full evaluation window closed before the prediction cutoff", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			return float64(prior.PositiveCounts[normalNode(row.NodeIP)])
		}),
		challengerMetricSet(rows, labels, horizonMinutes, "recency_weighted_failure_prior", "node prior positive outcomes exponentially decayed by a 90-day half-life; only closed evaluation windows are used", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			return prior.RecencyWeighted[normalNode(row.NodeIP)]
		}),
		challengerMetricSet(rows, labels, horizonMinutes, "severity_weighted_label_history", "node confirmed or strong-proxy labels available before the prediction cutoff, weighted critical=3, xid/thermal-critical=2, other eligible=1", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			return prior.SeverityWeightedLabels[normalNode(row.NodeIP)]
		}),
		challengerMetricSet(rows, labels, horizonMinutes, "threshold_binary", "released decision threshold converted to a node-level binary score", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			if row.PredictedPositive {
				return 1
			}
			return 0
		}),
	}
}

type challengerHistory struct {
	PositiveCounts         map[string]int
	RecencyWeighted        map[string]float64
	SeverityWeightedLabels map[string]float64
}

func challengerMetricSet(rows []api.PredictionOutcomeEvaluation, labels []api.FailureLabel, horizonMinutes int, policy, description string, score func(api.PredictionOutcomeEvaluation, challengerHistory) float64) ChallengerMetricSet {
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
		scoreValue := score(row, challengerHistoryWithLabelsBefore(rows, labels, row.PredictionEvaluatedAt))
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
	return challengerHistoryWithLabelsBefore(rows, nil, cutoff)
}

func challengerHistoryWithLabelsBefore(rows []api.PredictionOutcomeEvaluation, labels []api.FailureLabel, cutoff time.Time) challengerHistory {
	history := challengerHistory{PositiveCounts: map[string]int{}, RecencyWeighted: map[string]float64{}, SeverityWeightedLabels: map[string]float64{}}
	for _, candidate := range rows {
		if candidate.MaturityStatus != "matured" || candidate.FinalActualValue == nil || *candidate.FinalActualValue != 1 || strings.TrimSpace(candidate.NodeIP) == "" || candidate.WindowEndAt.After(cutoff) {
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

func eligibleSeverityHistoryLabel(label api.FailureLabel, cutoff time.Time) bool {
	if label.LabelValue != 1 || label.Excluded || strings.TrimSpace(label.NodeIP) == "" || label.AvailableAt.After(cutoff) || label.OccurredAt.After(cutoff) {
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

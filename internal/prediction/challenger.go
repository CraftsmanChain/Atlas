package prediction

import (
	"math"
	"strings"
	"time"

	"atlas/pkg/api"
)

const HeaRankChallengerReportVersion = "hearank-challenger-report-v1"

const (
	HeaRankMinimumSevenDayRows      = 30
	HeaRankMinimumSevenDayNodes     = 10
	HeaRankMinimumSevenDayPositives = 3
	HeaRankHistoryHalfLifeDays      = 90
)

type ChallengerMetricSet struct {
	Policy      string       `json:"policy"`
	Description string       `json:"description"`
	Rows        int          `json:"rows"`
	Nodes       int          `json:"nodes"`
	Positives   int          `json:"positives"`
	RankingAtK  []RankingAtK `json:"ranking_at_k"`
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
		},
		RecommendedNextRun: []string{
			"Accumulate enough mature 7d node outcomes before training a HeaRank-style MLP.",
			"Use the same node-level Ranking@K denominator when comparing Logistic, historical-risk baselines, and future HeaRank scores.",
			"Keep the challenger offline until time-split, leakage, and calibration checks pass.",
		},
	}
	report.AllMatured = challengerMetricSets(rows, 0)
	report.SevenDay = challengerMetricSets(rows, report.TargetHorizonMinutes)
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

func challengerMetricSets(rows []api.PredictionOutcomeEvaluation, horizonMinutes int) []ChallengerMetricSet {
	return []ChallengerMetricSet{
		challengerMetricSet(rows, horizonMinutes, "logistic_probability", "current shadow probability aggregated by node", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			if row.Probability == nil {
				return 0
			}
			return *row.Probability
		}),
		challengerMetricSet(rows, horizonMinutes, "failure_count_prior", "node positive outcomes whose full evaluation window closed before the prediction cutoff", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			return float64(prior.PositiveCounts[normalNode(row.NodeIP)])
		}),
		challengerMetricSet(rows, horizonMinutes, "recency_weighted_failure_prior", "node prior positive outcomes exponentially decayed by a 90-day half-life; only closed evaluation windows are used", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			return prior.RecencyWeighted[normalNode(row.NodeIP)]
		}),
		challengerMetricSet(rows, horizonMinutes, "threshold_binary", "released decision threshold converted to a node-level binary score", func(row api.PredictionOutcomeEvaluation, prior challengerHistory) float64 {
			if row.PredictedPositive {
				return 1
			}
			return 0
		}),
	}
}

type challengerHistory struct {
	PositiveCounts  map[string]int
	RecencyWeighted map[string]float64
}

func challengerMetricSet(rows []api.PredictionOutcomeEvaluation, horizonMinutes int, policy, description string, score func(api.PredictionOutcomeEvaluation, challengerHistory) float64) ChallengerMetricSet {
	items := make([]rankedOutcome, 0, len(rows))
	nodes := map[string]struct{}{}
	for _, row := range rows {
		if row.MaturityStatus != "matured" || row.Probability == nil || row.FinalActualValue == nil || strings.TrimSpace(row.NodeIP) == "" {
			continue
		}
		if horizonMinutes > 0 && row.HorizonMinutes != horizonMinutes {
			continue
		}
		node := normalNode(row.NodeIP)
		nodes[node] = struct{}{}
		items = append(items, rankedOutcome{probability: score(row, challengerHistoryBefore(rows, row.PredictionEvaluatedAt)), actual: *row.FinalActualValue})
	}
	positives := 0
	for _, item := range items {
		if item.actual == 1 {
			positives++
		}
	}
	return ChallengerMetricSet{
		Policy: policy, Description: description, Rows: len(items), Nodes: len(nodes), Positives: positives,
		RankingAtK: rankingFromItems(items),
	}
}

func challengerHistoryBefore(rows []api.PredictionOutcomeEvaluation, cutoff time.Time) challengerHistory {
	history := challengerHistory{PositiveCounts: map[string]int{}, RecencyWeighted: map[string]float64{}}
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
	return history
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

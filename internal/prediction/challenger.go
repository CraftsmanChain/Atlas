package prediction

import (
	"strings"
	"time"

	"atlas/pkg/api"
)

const HeaRankChallengerReportVersion = "hearank-challenger-report-v1"

type ChallengerMetricSet struct {
	Policy      string       `json:"policy"`
	Description string       `json:"description"`
	Rows        int          `json:"rows"`
	Nodes       int          `json:"nodes"`
	Positives   int          `json:"positives"`
	RankingAtK  []RankingAtK `json:"ranking_at_k"`
}

type HeaRankChallengerReport struct {
	Version              string                `json:"version"`
	FrameworkVersion     string                `json:"framework_version"`
	Mode                 string                `json:"mode"`
	Status               string                `json:"status"`
	TargetHorizonMinutes int                   `json:"target_horizon_minutes"`
	SampleSummary        OutcomeMaturity       `json:"sample_summary"`
	AllMatured           []ChallengerMetricSet `json:"all_matured"`
	SevenDay             []ChallengerMetricSet `json:"seven_day"`
	BlockingReasons      []string              `json:"blocking_reasons"`
	Interpretation       []string              `json:"interpretation"`
	RecommendedNextRun   []string              `json:"recommended_next_run"`
	GeneratedAt          time.Time             `json:"generated_at"`
}

func (s *Service) HeaRankChallengerReport() (HeaRankChallengerReport, error) {
	var rows []api.PredictionOutcomeEvaluation
	if err := s.db.Order("prediction_evaluated_at ASC, id ASC").Find(&rows).Error; err != nil {
		return HeaRankChallengerReport{}, err
	}
	report := HeaRankChallengerReport{
		Version:              HeaRankChallengerReportVersion,
		FrameworkVersion:     FrameworkVersion,
		Mode:                 "offline_challenger_read_only",
		TargetHorizonMinutes: 10080,
		SampleSummary:        outcomeMaturity(rows),
		GeneratedAt:          s.now(),
		Interpretation: []string{
			"This is a node-risk challenger scaffold, not a released HeaRank model.",
			"All policies are evaluated on mature scored outcomes only; pending and censored rows are excluded.",
			"Seven-day rows are reported separately because HeaRank validation should target 7d node risk.",
		},
		RecommendedNextRun: []string{
			"Accumulate enough mature 7d node outcomes before training a HeaRank-style MLP.",
			"Use the same node-level Ranking@K denominator when comparing Logistic, failure-count, and future HeaRank scores.",
			"Keep the challenger offline until time-split, leakage, and calibration checks pass.",
		},
	}
	report.AllMatured = challengerMetricSets(rows, 0)
	report.SevenDay = challengerMetricSets(rows, report.TargetHorizonMinutes)
	if matureCount(rows, report.TargetHorizonMinutes) == 0 {
		report.Status = "blocked_no_7d_mature_outcomes"
		report.BlockingReasons = append(report.BlockingReasons, "no mature scored 7d node-risk outcomes are available")
	} else {
		report.Status = "ready_for_offline_comparison"
	}
	return report, nil
}

func challengerMetricSets(rows []api.PredictionOutcomeEvaluation, horizonMinutes int) []ChallengerMetricSet {
	return []ChallengerMetricSet{
		challengerMetricSet(rows, horizonMinutes, "logistic_probability", "current shadow probability aggregated by node", func(row api.PredictionOutcomeEvaluation, prior map[string]int) float64 {
			if row.Probability == nil {
				return 0
			}
			return *row.Probability
		}),
		challengerMetricSet(rows, horizonMinutes, "failure_count_prior", "node prior positive outcomes before the prediction cutoff", func(row api.PredictionOutcomeEvaluation, prior map[string]int) float64 {
			return float64(prior[normalNode(row.NodeIP)])
		}),
		challengerMetricSet(rows, horizonMinutes, "threshold_binary", "released decision threshold converted to a node-level binary score", func(row api.PredictionOutcomeEvaluation, prior map[string]int) float64 {
			if row.PredictedPositive {
				return 1
			}
			return 0
		}),
	}
}

func challengerMetricSet(rows []api.PredictionOutcomeEvaluation, horizonMinutes int, policy, description string, score func(api.PredictionOutcomeEvaluation, map[string]int) float64) ChallengerMetricSet {
	prior := map[string]int{}
	items := make([]rankedOutcome, 0, len(rows))
	nodes := map[string]struct{}{}
	for _, row := range rows {
		if row.MaturityStatus != "matured" || row.Probability == nil || row.FinalActualValue == nil || strings.TrimSpace(row.NodeIP) == "" {
			continue
		}
		if horizonMinutes > 0 && row.HorizonMinutes != horizonMinutes {
			if *row.FinalActualValue == 1 {
				prior[normalNode(row.NodeIP)]++
			}
			continue
		}
		node := normalNode(row.NodeIP)
		nodes[node] = struct{}{}
		items = append(items, rankedOutcome{probability: score(row, prior), actual: *row.FinalActualValue})
		if *row.FinalActualValue == 1 {
			prior[node]++
		}
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

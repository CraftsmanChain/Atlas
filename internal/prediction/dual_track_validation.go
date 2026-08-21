package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
)

const DualTrackValidationReportVersion = "prediction-dual-track-validation-v5"
const DualTrackTemporalCohortLimit = 12
const DualTrackMinimumConsistentCohorts = 3
const DualTrackMinimumDirectionRatio = 2.0 / 3.0

type DualTrackAlignment struct {
	Status             string     `json:"status"`
	ModelSpecID        uint       `json:"model_spec_id,omitempty"`
	ModelKey           string     `json:"model_key,omitempty"`
	ModelVersion       string     `json:"model_version,omitempty"`
	HorizonMinutes     int        `json:"horizon_minutes"`
	SnapshotCutoffAt   *time.Time `json:"snapshot_cutoff_at,omitempty"`
	AlignedOutcomeRows int        `json:"aligned_outcome_rows"`
}

type RankingValidationTrack struct {
	Status          string       `json:"status"`
	Policy          string       `json:"policy"`
	ScoreSemantics  string       `json:"score_semantics"`
	SnapshotVersion string       `json:"snapshot_version"`
	SnapshotSHA256  string       `json:"snapshot_sha256"`
	Nodes           int          `json:"nodes"`
	MaturedRows     int          `json:"matured_rows"`
	PositiveRows    int          `json:"positive_rows"`
	Metrics         []RankingAtK `json:"ranking_at_k"`
	BlockingReasons []string     `json:"blocking_reasons"`
}

type ProbabilityValidationTrack struct {
	Status               string                     `json:"status"`
	OutcomeReportVersion string                     `json:"outcome_report_version"`
	HorizonMinutes       int                        `json:"horizon_minutes"`
	Maturity             OutcomeMaturity            `json:"maturity"`
	PositiveRows         int                        `json:"positive_rows"`
	ProbabilityCoverage  float64                    `json:"probability_coverage"`
	Rule                 AccuracyMetrics            `json:"rule"`
	Final                AccuracyMetrics            `json:"final"`
	Quality              TemporalProbabilityMetrics `json:"quality"`
	CalibrationStatus    string                     `json:"calibration_status"`
	BlockingReasons      []string                   `json:"blocking_reasons"`
}

type DualTrackValidationEvidence struct {
	ReadinessVersion       string `json:"readiness_version"`
	ReadinessSHA256        string `json:"readiness_sha256"`
	ReadinessStatus        string `json:"readiness_status"`
	ChallengerVersion      string `json:"challenger_version"`
	ChallengerStatus       string `json:"challenger_status"`
	DataDriftStatus        string `json:"data_drift_status"`
	CalibrationDriftStatus string `json:"calibration_drift_status"`
	FeatureDriftStatus     string `json:"feature_drift_status"`
}

type TemporalProbabilityMetrics struct {
	Status                   string   `json:"status"`
	TP                       int      `json:"tp"`
	FP                       int      `json:"fp"`
	FN                       int      `json:"fn"`
	TN                       int      `json:"tn"`
	Evaluated                int      `json:"evaluated"`
	Precision                *float64 `json:"precision,omitempty"`
	Recall                   *float64 `json:"recall,omitempty"`
	Accuracy                 *float64 `json:"accuracy,omitempty"`
	F1Score                  *float64 `json:"f1_score,omitempty"`
	BrierScore               *float64 `json:"brier_score,omitempty"`
	NullBrierScore           *float64 `json:"null_brier_score,omitempty"`
	BrierSkillScore          *float64 `json:"brier_skill_score,omitempty"`
	ROCAUC                   *float64 `json:"roc_auc,omitempty"`
	PRAUCAveragePrecision    *float64 `json:"pr_auc_average_precision,omitempty"`
	ExpectedCalibrationError *float64 `json:"expected_calibration_error,omitempty"`
	ScoredRows               int      `json:"scored_rows"`
	PositiveRows             int      `json:"positive_rows"`
	NegativeRows             int      `json:"negative_rows"`
	InvalidProbabilityRows   int      `json:"invalid_probability_rows"`
	InvalidLabelRows         int      `json:"invalid_label_rows"`
	CalibrationBins          int      `json:"calibration_bins"`
	CalibrationBinsUsed      int      `json:"calibration_bins_used"`
	BlockingReasons          []string `json:"blocking_reasons"`
}

type temporalScoredActual struct {
	probability float64
	actual      float64
}

type DualTrackTemporalCohort struct {
	PredictionCutoffAt   time.Time                  `json:"prediction_cutoff_at"`
	IndependentTimeBatch bool                       `json:"independent_time_batch"`
	TotalRows            int                        `json:"total_rows"`
	MaturedRows          int                        `json:"matured_rows"`
	PendingRows          int                        `json:"pending_rows"`
	CensoredRows         int                        `json:"censored_rows"`
	NodeCount            int                        `json:"node_count"`
	PositiveRows         int                        `json:"positive_rows"`
	RankingStatus        string                     `json:"ranking_status"`
	ProbabilityStatus    string                     `json:"probability_status"`
	NodeRankingAtK       []RankingAtK               `json:"node_ranking_at_k"`
	ProbabilityMetrics   TemporalProbabilityMetrics `json:"probability_metrics"`
}

type DualTrackTemporalSummary struct {
	CohortLimit                      int `json:"cohort_limit"`
	CohortCount                      int `json:"cohort_count"`
	MaturedCohortCount               int `json:"matured_cohort_count"`
	IndependentCohortCount           int `json:"independent_cohort_count"`
	ComparableRankingCohortCount     int `json:"comparable_ranking_cohort_count"`
	ComparableProbabilityCohortCount int `json:"comparable_probability_cohort_count"`
}

type TemporalTrackConsistency struct {
	Status                      string   `json:"status"`
	Metric                      string   `json:"metric"`
	PositiveDirectionRule       string   `json:"positive_direction_rule"`
	MinimumIndependentCohorts   int      `json:"minimum_independent_cohorts"`
	MinimumDirectionRatio       float64  `json:"minimum_direction_ratio"`
	EvaluableIndependentCohorts int      `json:"evaluable_independent_cohorts"`
	PositiveDirectionCohorts    int      `json:"positive_direction_cohorts"`
	DirectionConsistencyRatio   *float64 `json:"direction_consistency_ratio,omitempty"`
	BlockingReasons             []string `json:"blocking_reasons"`
}

type DualTrackTemporalConsistency struct {
	Ranking     TemporalTrackConsistency `json:"ranking_track"`
	Probability TemporalTrackConsistency `json:"probability_track"`
}

type DualTrackValidationReport struct {
	Version             string                       `json:"version"`
	FrameworkVersion    string                       `json:"framework_version"`
	Mode                string                       `json:"mode"`
	Status              string                       `json:"status"`
	ReportSHA256        string                       `json:"report_sha256"`
	Alignment           DualTrackAlignment           `json:"alignment"`
	Ranking             RankingValidationTrack       `json:"ranking_track"`
	Probability         ProbabilityValidationTrack   `json:"probability_track"`
	Evidence            DualTrackValidationEvidence  `json:"evidence"`
	Readiness           ValidationReadinessReport    `json:"readiness"`
	TemporalSummary     DualTrackTemporalSummary     `json:"temporal_summary"`
	TemporalCohorts     []DualTrackTemporalCohort    `json:"temporal_cohorts"`
	TemporalConsistency DualTrackTemporalConsistency `json:"temporal_consistency"`
	Safety              RiskRankingSafety            `json:"safety"`
	Interpretation      []string                     `json:"interpretation"`
	RecommendedNextRun  []string                     `json:"recommended_next_run"`
	GeneratedAt         time.Time                    `json:"generated_at"`
}

func (s *Service) DualTrackValidationReport() (DualTrackValidationReport, error) {
	rankingSnapshot, err := s.RiskRankingSnapshotReport()
	if err != nil {
		return DualTrackValidationReport{}, err
	}
	temporalSummary := DualTrackTemporalSummary{CohortLimit: DualTrackTemporalCohortLimit}
	temporalCohorts := []DualTrackTemporalCohort{}
	if rankingSnapshot.Status == "shadow_snapshot_available" && rankingSnapshot.SnapshotCutoffAt != nil {
		temporalSummary, temporalCohorts, err = s.dualTrackTemporalCohorts(rankingSnapshot)
		if err != nil {
			return DualTrackValidationReport{}, err
		}
	}
	temporalConsistency := dualTrackTemporalConsistency(temporalCohorts)
	readiness, err := s.validationReadinessReport(&rankingSnapshot, &temporalSummary, &temporalConsistency)
	if err != nil {
		return DualTrackValidationReport{}, err
	}
	report := DualTrackValidationReport{
		Version: DualTrackValidationReportVersion, FrameworkVersion: FrameworkVersion,
		Mode: "read_only_prospective_shadow", Safety: rankingSnapshot.Safety,
		Alignment: DualTrackAlignment{
			Status: "blocked_no_ranking_snapshot", ModelSpecID: rankingSnapshot.ModelSpecID,
			ModelKey: rankingSnapshot.ModelKey, ModelVersion: rankingSnapshot.ModelVersion,
			HorizonMinutes: rankingSnapshot.HorizonMinutes, SnapshotCutoffAt: rankingSnapshot.SnapshotCutoffAt,
		},
		Ranking: RankingValidationTrack{
			Status: "blocked", Policy: rankingSnapshot.Policy, ScoreSemantics: rankingSnapshot.ScoreSemantics,
			SnapshotVersion: rankingSnapshot.Version, SnapshotSHA256: rankingSnapshot.ReportSHA256,
			Nodes: rankingSnapshot.NodeCount, Metrics: []RankingAtK{}, BlockingReasons: append([]string(nil), rankingSnapshot.BlockingReasons...),
		},
		Probability: ProbabilityValidationTrack{
			Status: "blocked", OutcomeReportVersion: "prediction-outcome-report-v1",
			HorizonMinutes: rankingSnapshot.HorizonMinutes, CalibrationStatus: readiness.CalibrationDriftStatus,
			BlockingReasons: []string{"no ranking-aligned outcome cohort is available"},
		},
		Evidence: DualTrackValidationEvidence{
			ReadinessVersion: readiness.Version, ReadinessSHA256: readiness.ReadinessSHA256, ReadinessStatus: readiness.Status,
			ChallengerVersion: readiness.ChallengerVersion, ChallengerStatus: readiness.ChallengerStatus,
			DataDriftStatus: readiness.DataDriftStatus, CalibrationDriftStatus: readiness.CalibrationDriftStatus,
			FeatureDriftStatus: readiness.FeatureDriftStatus,
		},
		Readiness:           readiness,
		TemporalSummary:     temporalSummary,
		TemporalCohorts:     temporalCohorts,
		TemporalConsistency: temporalConsistency,
		Interpretation: []string{
			"ranking track answers who should be reviewed first and is evaluated with node Ranking@K metrics",
			"probability track answers event risk within a fixed horizon and is evaluated with discrimination and calibration metrics",
			"track statuses remain independent; no combined success rate is produced",
		},
		RecommendedNextRun: []string{
			"wait for the aligned outcome cohort to mature before interpreting either track",
			"compare both tracks on the same frozen cutoff without tuning from test outcomes",
		},
		GeneratedAt: s.now(),
	}
	if rankingSnapshot.Status != "shadow_snapshot_available" || rankingSnapshot.SnapshotCutoffAt == nil {
		if len(report.Ranking.BlockingReasons) == 0 {
			report.Ranking.BlockingReasons = []string{"risk ranking snapshot is unavailable"}
		}
		report.Status = "blocked"
		report.ReportSHA256 = dualTrackValidationChecksum(report)
		return report, nil
	}
	var rows []api.PredictionOutcomeEvaluation
	if err := s.db.Where("model_spec_id = ? AND horizon_minutes = ? AND prediction_evaluated_at = ?", rankingSnapshot.ModelSpecID, rankingSnapshot.HorizonMinutes, *rankingSnapshot.SnapshotCutoffAt).
		Order("node_ip ASC, gpu_uuid ASC, id ASC").Find(&rows).Error; err != nil {
		return DualTrackValidationReport{}, err
	}
	report.Alignment.AlignedOutcomeRows = len(rows)
	if len(rows) == 0 {
		report.Alignment.Status = "blocked_no_aligned_outcomes"
		report.Ranking.BlockingReasons = uniqueSorted(append(report.Ranking.BlockingReasons, "no outcomes align with the frozen ranking cutoff"))
		report.Status = "blocked"
		report.ReportSHA256 = dualTrackValidationChecksum(report)
		return report, nil
	}
	report.Alignment.Status = "aligned"
	accuracy := accuracyFromRows(rows, report.GeneratedAt)
	maturity := outcomeMaturity(rows)
	stability := outcomeStability(maturity, accuracy)
	quality := temporalProbabilityMetrics(rows)
	positives := accuracy.Final.TP + accuracy.Final.FN
	report.Ranking.MaturedRows = maturity.Matured
	report.Ranking.PositiveRows = positives
	report.Ranking.Metrics = nonNilRankingMetrics(accuracy.Final.NodeRankingAtK)
	report.Ranking.Status, report.Ranking.BlockingReasons = dualTrackRankingStatus(rankingSnapshot, maturity, positives)
	report.Probability.Status = dualTrackProbabilityStatus(stability.Status, quality.Status)
	report.Probability.Maturity = maturity
	report.Probability.PositiveRows = positives
	report.Probability.ProbabilityCoverage = stability.ProbabilityCoverage
	report.Probability.Rule = accuracy.Rule
	report.Probability.Final = accuracy.Final
	report.Probability.Quality = quality
	report.Probability.BlockingReasons = uniqueSorted(append(append([]string(nil), stability.BlockingReasons...), quality.BlockingReasons...))
	report.Status = dualTrackOverallStatus(report.Ranking.Status, report.Probability.Status)
	report.ReportSHA256 = dualTrackValidationChecksum(report)
	return report, nil
}

func (s *Service) dualTrackTemporalCohorts(snapshot RiskRankingSnapshotReport) (DualTrackTemporalSummary, []DualTrackTemporalCohort, error) {
	summary := DualTrackTemporalSummary{CohortLimit: DualTrackTemporalCohortLimit}
	var cutoffs []time.Time
	if err := s.db.Model(&api.PredictionOutcomeEvaluation{}).
		Where("model_spec_id = ? AND horizon_minutes = ?", snapshot.ModelSpecID, snapshot.HorizonMinutes).
		Distinct("prediction_evaluated_at").Order("prediction_evaluated_at DESC").Limit(DualTrackTemporalCohortLimit).
		Pluck("prediction_evaluated_at", &cutoffs).Error; err != nil {
		return summary, nil, err
	}
	if len(cutoffs) == 0 {
		return summary, []DualTrackTemporalCohort{}, nil
	}
	var rows []api.PredictionOutcomeEvaluation
	if err := s.db.Where("model_spec_id = ? AND horizon_minutes = ? AND prediction_evaluated_at IN ?", snapshot.ModelSpecID, snapshot.HorizonMinutes, cutoffs).
		Order("prediction_evaluated_at DESC, node_ip ASC, gpu_uuid ASC, id ASC").Find(&rows).Error; err != nil {
		return summary, nil, err
	}
	rowsByCutoff := make(map[int64][]api.PredictionOutcomeEvaluation, len(cutoffs))
	for _, row := range rows {
		key := row.PredictionEvaluatedAt.UnixNano()
		rowsByCutoff[key] = append(rowsByCutoff[key], row)
	}
	independent := make(map[int64]bool, len(cutoffs))
	horizon := time.Duration(snapshot.HorizonMinutes) * time.Minute
	var lastIndependent time.Time
	for _, cutoff := range cutoffs {
		if lastIndependent.IsZero() || !cutoff.After(lastIndependent.Add(-horizon)) {
			independent[cutoff.UnixNano()] = true
			lastIndependent = cutoff
		}
	}
	cohorts := make([]DualTrackTemporalCohort, 0, len(cutoffs))
	for _, cutoff := range cutoffs {
		cohortRows := rowsByCutoff[cutoff.UnixNano()]
		accuracy := accuracyFromRows(cohortRows, cutoff)
		maturity := outcomeMaturity(cohortRows)
		stability := outcomeStability(maturity, accuracy)
		quality := temporalProbabilityMetrics(cohortRows)
		positives := accuracy.Final.TP + accuracy.Final.FN
		rankingStatus, _ := dualTrackRankingStatus(snapshot, maturity, positives)
		cohort := DualTrackTemporalCohort{
			PredictionCutoffAt: cutoff, IndependentTimeBatch: independent[cutoff.UnixNano()],
			TotalRows: maturity.Total, MaturedRows: maturity.Matured, PendingRows: maturity.Pending,
			CensoredRows: maturity.Censored, NodeCount: maturity.NodeEligible, PositiveRows: positives,
			RankingStatus: rankingStatus, ProbabilityStatus: dualTrackProbabilityStatus(stability.Status, quality.Status),
			NodeRankingAtK:     nonNilRankingMetrics(accuracy.Final.NodeRankingAtK),
			ProbabilityMetrics: quality,
		}
		cohorts = append(cohorts, cohort)
		if cohort.MaturedRows > 0 {
			summary.MaturedCohortCount++
		}
		if cohort.IndependentTimeBatch {
			summary.IndependentCohortCount++
		}
		if cohort.RankingStatus == "comparable" {
			summary.ComparableRankingCohortCount++
		}
		if cohort.ProbabilityStatus == "comparable" {
			summary.ComparableProbabilityCohortCount++
		}
	}
	summary.CohortCount = len(cohorts)
	return summary, cohorts, nil
}

func temporalProbabilityMetrics(rows []api.PredictionOutcomeEvaluation) TemporalProbabilityMetrics {
	metrics := TemporalProbabilityMetrics{CalibrationBins: 10, BlockingReasons: []string{}}
	values := make([]temporalScoredActual, 0, len(rows))
	actualSum := 0.0
	for _, row := range rows {
		if row.MaturityStatus != "matured" || row.Probability == nil || row.FinalActualValue == nil {
			continue
		}
		if *row.Probability < 0 || *row.Probability > 1 || math.IsNaN(*row.Probability) || math.IsInf(*row.Probability, 0) {
			metrics.InvalidProbabilityRows++
			continue
		}
		if *row.FinalActualValue != 0 && *row.FinalActualValue != 1 {
			metrics.InvalidLabelRows++
			continue
		}
		actual := float64(*row.FinalActualValue)
		values = append(values, temporalScoredActual{probability: *row.Probability, actual: actual})
		actualSum += actual
		if *row.FinalActualValue == 1 {
			metrics.PositiveRows++
			if row.PredictedPositive {
				metrics.TP++
			} else {
				metrics.FN++
			}
		} else {
			metrics.NegativeRows++
			if row.PredictedPositive {
				metrics.FP++
			} else {
				metrics.TN++
			}
		}
	}
	metrics.ScoredRows = len(values)
	metrics.Evaluated = metrics.ScoredRows
	if len(values) == 0 {
		metrics.Status = "blocked_no_scored_rows"
		metrics.BlockingReasons = []string{"no mature rows have a valid probability and binary final label"}
		return metrics
	}
	metrics.Precision = temporalRatio(metrics.TP, metrics.TP+metrics.FP)
	metrics.Recall = temporalRatio(metrics.TP, metrics.TP+metrics.FN)
	metrics.Accuracy = temporalRatio(metrics.TP+metrics.TN, metrics.Evaluated)
	if denominator := 2*metrics.TP + metrics.FP + metrics.FN; denominator > 0 {
		f1 := float64(2*metrics.TP) / float64(denominator)
		metrics.F1Score = &f1
	}
	baseRate := actualSum / float64(len(values))
	modelLoss, nullLoss := 0.0, 0.0
	binCounts := make([]int, metrics.CalibrationBins)
	binProbabilitySums := make([]float64, metrics.CalibrationBins)
	binActualSums := make([]float64, metrics.CalibrationBins)
	for _, value := range values {
		modelLoss += math.Pow(value.probability-value.actual, 2)
		nullLoss += math.Pow(baseRate-value.actual, 2)
		bin := int(value.probability * float64(metrics.CalibrationBins))
		if bin == metrics.CalibrationBins {
			bin--
		}
		binCounts[bin]++
		binProbabilitySums[bin] += value.probability
		binActualSums[bin] += value.actual
	}
	brier := modelLoss / float64(len(values))
	nullBrier := nullLoss / float64(len(values))
	metrics.BrierScore, metrics.NullBrierScore = &brier, &nullBrier
	if nullBrier > 0 {
		skill := 1 - brier/nullBrier
		metrics.BrierSkillScore = &skill
	}
	ece := 0.0
	for bin, count := range binCounts {
		if count == 0 {
			continue
		}
		metrics.CalibrationBinsUsed++
		meanProbability := binProbabilitySums[bin] / float64(count)
		observedRate := binActualSums[bin] / float64(count)
		ece += math.Abs(meanProbability-observedRate) * float64(count) / float64(len(values))
	}
	metrics.ExpectedCalibrationError = &ece
	metrics.ROCAUC = temporalROCAUC(values)
	metrics.PRAUCAveragePrecision = temporalAveragePrecision(values)
	switch {
	case metrics.PositiveRows == 0 || metrics.NegativeRows == 0:
		metrics.Status = "blocked_single_class"
		metrics.BlockingReasons = []string{"probability discrimination requires both positive and negative mature rows"}
	case metrics.ScoredRows < OutcomeMinimumMaturedSamples || metrics.PositiveRows < OutcomeMinimumPositiveSamples:
		metrics.Status = "exploratory"
		if metrics.ScoredRows < OutcomeMinimumMaturedSamples {
			metrics.BlockingReasons = append(metrics.BlockingReasons, "probability quality sample count is below the stability gate")
		}
		if metrics.PositiveRows < OutcomeMinimumPositiveSamples {
			metrics.BlockingReasons = append(metrics.BlockingReasons, "positive probability sample count is below the stability gate")
		}
	default:
		metrics.Status = "comparable"
	}
	return metrics
}

func temporalRatio(numerator, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}

func dualTrackProbabilityStatus(stabilityStatus, qualityStatus string) string {
	if stabilityStatus == "blocked" || strings.HasPrefix(qualityStatus, "blocked_") || qualityStatus == "" {
		return "blocked"
	}
	if stabilityStatus == "exploratory" || qualityStatus == "exploratory" {
		return "exploratory"
	}
	if stabilityStatus == "comparable" && qualityStatus == "comparable" {
		return "comparable"
	}
	return "blocked"
}

func temporalROCAUC(values []temporalScoredActual) *float64 {
	ordered := append([]temporalScoredActual(nil), values...)
	positives := 0
	for _, value := range ordered {
		if value.actual == 1 {
			positives++
		}
	}
	negatives := len(ordered) - positives
	if positives == 0 || negatives == 0 {
		return nil
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].probability < ordered[j].probability })
	positiveRankSum := 0.0
	for start := 0; start < len(ordered); {
		end := start + 1
		for end < len(ordered) && ordered[end].probability == ordered[start].probability {
			end++
		}
		averageRank := float64(start+1+end) / 2
		for index := start; index < end; index++ {
			if ordered[index].actual == 1 {
				positiveRankSum += averageRank
			}
		}
		start = end
	}
	auc := (positiveRankSum - float64(positives*(positives+1))/2) / float64(positives*negatives)
	return &auc
}

func temporalAveragePrecision(values []temporalScoredActual) *float64 {
	ordered := append([]temporalScoredActual(nil), values...)
	positives := 0
	for _, value := range ordered {
		if value.actual == 1 {
			positives++
		}
	}
	if positives == 0 || positives == len(ordered) {
		return nil
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].probability > ordered[j].probability })
	tp, seen, previousTP, averagePrecision := 0, 0, 0, 0.0
	for start := 0; start < len(ordered); {
		end := start + 1
		for end < len(ordered) && ordered[end].probability == ordered[start].probability {
			end++
		}
		for index := start; index < end; index++ {
			seen++
			if ordered[index].actual == 1 {
				tp++
			}
		}
		averagePrecision += float64(tp-previousTP) / float64(positives) * float64(tp) / float64(seen)
		previousTP = tp
		start = end
	}
	return &averagePrecision
}

func dualTrackTemporalConsistency(cohorts []DualTrackTemporalCohort) DualTrackTemporalConsistency {
	ranking := TemporalTrackConsistency{
		Metric: "node_rank_at_3_lift", PositiveDirectionRule: "lift_greater_than_1",
		MinimumIndependentCohorts: DualTrackMinimumConsistentCohorts, MinimumDirectionRatio: DualTrackMinimumDirectionRatio,
	}
	probability := TemporalTrackConsistency{
		Metric: "brier_skill_score", PositiveDirectionRule: "brier_skill_score_greater_than_0",
		MinimumIndependentCohorts: DualTrackMinimumConsistentCohorts, MinimumDirectionRatio: DualTrackMinimumDirectionRatio,
	}
	for _, cohort := range cohorts {
		if !cohort.IndependentTimeBatch || cohort.MaturedRows == 0 || cohort.PositiveRows == 0 {
			continue
		}
		for _, metric := range cohort.NodeRankingAtK {
			if metric.K == 3 && metric.Lift != nil {
				ranking.EvaluableIndependentCohorts++
				if *metric.Lift > 1 {
					ranking.PositiveDirectionCohorts++
				}
				break
			}
		}
		if cohort.ProbabilityMetrics.BrierSkillScore != nil {
			probability.EvaluableIndependentCohorts++
			if *cohort.ProbabilityMetrics.BrierSkillScore > 0 {
				probability.PositiveDirectionCohorts++
			}
		}
	}
	finalizeTemporalConsistency(&ranking, "ranking consistency requires at least three independent evaluable cohorts")
	finalizeTemporalConsistency(&probability, "probability consistency requires at least three independent evaluable cohorts")
	return DualTrackTemporalConsistency{Ranking: ranking, Probability: probability}
}

func finalizeTemporalConsistency(consistency *TemporalTrackConsistency, insufficientReason string) {
	if consistency.EvaluableIndependentCohorts > 0 {
		ratio := float64(consistency.PositiveDirectionCohorts) / float64(consistency.EvaluableIndependentCohorts)
		consistency.DirectionConsistencyRatio = &ratio
	}
	if consistency.EvaluableIndependentCohorts < consistency.MinimumIndependentCohorts {
		consistency.Status = "insufficient_independent_cohorts"
		consistency.BlockingReasons = []string{insufficientReason}
		return
	}
	if consistency.PositiveDirectionCohorts >= consistency.MinimumIndependentCohorts && consistency.DirectionConsistencyRatio != nil && *consistency.DirectionConsistencyRatio >= consistency.MinimumDirectionRatio {
		consistency.Status = "consistent"
		consistency.BlockingReasons = []string{}
		return
	}
	consistency.Status = "review_mixed_direction"
	consistency.BlockingReasons = []string{"independent cohorts do not show a stable positive direction"}
}

func nonNilRankingMetrics(rows []RankingAtK) []RankingAtK {
	if len(rows) == 0 {
		return []RankingAtK{}
	}
	return append([]RankingAtK(nil), rows...)
}

func dualTrackRankingStatus(snapshot RiskRankingSnapshotReport, maturity OutcomeMaturity, positives int) (string, []string) {
	blockers := append([]string(nil), snapshot.BlockingReasons...)
	if snapshot.Status != "shadow_snapshot_available" {
		return "blocked", uniqueSorted(blockers)
	}
	if maturity.Matured == 0 {
		blockers = append(blockers, "no mature ranking-aligned outcomes are available")
	}
	if positives == 0 {
		blockers = append(blockers, "no positive mature ranking-aligned outcomes are available")
	}
	if maturity.NodeEligible == 0 {
		blockers = append(blockers, "no node-eligible mature outcomes are available")
	}
	if len(blockers) > 0 {
		return "blocked", uniqueSorted(blockers)
	}
	if maturity.Matured < HeaRankMinimumSevenDayRows || maturity.NodeEligible < HeaRankMinimumSevenDayNodes || positives < HeaRankMinimumSevenDayPositives {
		return "exploratory", []string{"aligned ranking cohort is below the offline comparison sample gate"}
	}
	return "comparable", []string{}
}

func dualTrackOverallStatus(rankingStatus, probabilityStatus string) string {
	if rankingStatus == "blocked" || probabilityStatus == "blocked" {
		return "blocked"
	}
	if rankingStatus == "exploratory" || probabilityStatus == "exploratory" {
		return "exploratory"
	}
	return "comparable"
}

func dualTrackValidationChecksum(report DualTrackValidationReport) string {
	fingerprint := report
	fingerprint.ReportSHA256 = ""
	fingerprint.GeneratedAt = time.Time{}
	fingerprint.Readiness.GeneratedAt = time.Time{}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

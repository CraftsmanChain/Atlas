package prediction

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const OutcomeRuleVersion = "prediction-outcome-v1"

const (
	OutcomeMinimumMaturedSamples      = 30
	OutcomeMinimumMaturedRatio        = 0.50
	OutcomeMinimumProbabilityCoverage = 0.80
	OutcomeMinimumPositiveSamples     = 3
)

type ConfusionMatrix struct {
	TP        int `json:"tp"`
	FP        int `json:"fp"`
	FN        int `json:"fn"`
	TN        int `json:"tn"`
	Evaluated int `json:"evaluated"`
}

type AccuracyMetrics struct {
	ConfusionMatrix
	Precision         *float64     `json:"precision,omitempty"`
	Recall            *float64     `json:"recall,omitempty"`
	Specificity       *float64     `json:"specificity,omitempty"`
	FalsePositiveRate *float64     `json:"false_positive_rate,omitempty"`
	FalseNegativeRate *float64     `json:"false_negative_rate,omitempty"`
	Accuracy          *float64     `json:"accuracy,omitempty"`
	RankingAtK        []RankingAtK `json:"ranking_at_k,omitempty"`
	NodeRankingAtK    []RankingAtK `json:"node_ranking_at_k,omitempty"`
}

type RankingAtK struct {
	K         int      `json:"k"`
	Status    string   `json:"status"`
	Eligible  int      `json:"eligible"`
	Positives int      `json:"positives"`
	Hits      int      `json:"hits"`
	Precision *float64 `json:"precision,omitempty"`
	Recall    *float64 `json:"recall,omitempty"`
	NDCG      *float64 `json:"ndcg,omitempty"`
	Lift      *float64 `json:"lift,omitempty"`
}

type AccuracySlice struct {
	ModelKey       string          `json:"model_key"`
	ModelVersion   string          `json:"model_version"`
	HorizonMinutes int             `json:"horizon_minutes"`
	Rule           AccuracyMetrics `json:"rule"`
	Final          AccuracyMetrics `json:"final"`
}

type AccuracySummary struct {
	Rule                AccuracyMetrics `json:"rule"`
	Final               AccuracyMetrics `json:"final"`
	Pending             int             `json:"pending"`
	Censored            int             `json:"censored"`
	HumanOverrides      int             `json:"human_overrides"`
	RuleDecisionVersion string          `json:"rule_decision_version"`
	ByModel             []AccuracySlice `json:"by_model"`
	EvaluatedAt         time.Time       `json:"evaluated_at"`
}

type OutcomeReport struct {
	Version             string               `json:"version"`
	FrameworkVersion    string               `json:"framework_version"`
	Mode                string               `json:"mode"`
	Safety              OutcomeSafety        `json:"safety"`
	SampleMaturity      OutcomeMaturity      `json:"sample_maturity"`
	Stability           OutcomeStability     `json:"stability"`
	Accuracy            AccuracySummary      `json:"accuracy"`
	BaselineComparisons []BaselineComparison `json:"baseline_comparisons"`
	Interpretation      []string             `json:"interpretation"`
	RecommendedNextRun  []string             `json:"recommended_next_run"`
	GeneratedAt         time.Time            `json:"generated_at"`
}

type OutcomeStability struct {
	Status                      string   `json:"status"`
	MinimumMaturedSamples       int      `json:"minimum_matured_samples"`
	MinimumMaturedRatio         float64  `json:"minimum_matured_ratio"`
	MinimumProbabilityCoverage  float64  `json:"minimum_probability_coverage"`
	MinimumPositiveSamples      int      `json:"minimum_positive_samples"`
	ProbabilityCoverage         float64  `json:"probability_coverage"`
	PositiveSamples             int      `json:"positive_samples"`
	RankingInterpretationStatus string   `json:"ranking_interpretation_status"`
	FalsePositiveReviewHint     string   `json:"false_positive_review_hint"`
	FalseNegativeReviewHint     string   `json:"false_negative_review_hint"`
	BlockingReasons             []string `json:"blocking_reasons"`
	RecommendedReview           []string `json:"recommended_review"`
}

type BaselineComparison struct {
	Name             string          `json:"name"`
	PredictionPolicy string          `json:"prediction_policy"`
	Rule             AccuracyMetrics `json:"rule"`
	Final            AccuracyMetrics `json:"final"`
}

type OutcomeSafety struct {
	ReadOnlyShadow bool   `json:"read_only_shadow"`
	NoAlertEmitted bool   `json:"no_alert_emitted"`
	NoActionTaken  bool   `json:"no_action_taken"`
	ProbabilityUse string `json:"probability_use"`
}

type OutcomeMaturity struct {
	Total             int     `json:"total"`
	Matured           int     `json:"matured"`
	Pending           int     `json:"pending"`
	Censored          int     `json:"censored"`
	MaturedRatio      float64 `json:"matured_ratio"`
	ProbabilityScored int     `json:"probability_scored"`
	NodeEligible      int     `json:"node_eligible"`
}

type OutcomeOverride struct {
	ActualValue int    `json:"actual_value"`
	Reason      string `json:"reason"`
	DecidedBy   string `json:"decided_by"`
}

func (s *Service) RunOutcomeSync(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		if err := s.SyncOutcomes(); err != nil && ctx.Err() == nil {
			log.Printf("prediction outcome reconciliation failed: %v", err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (s *Service) SyncOutcomes() error {
	s.outcomeMu.Lock()
	defer s.outcomeMu.Unlock()

	now := s.now()
	var predictions []api.HardwareRiskPrediction
	if err := s.db.Order("id").Find(&predictions).Error; err != nil {
		return err
	}
	var specs []api.PredictionModelSpec
	if err := s.db.Find(&specs).Error; err != nil {
		return err
	}
	specByID := make(map[uint]api.PredictionModelSpec, len(specs))
	for _, spec := range specs {
		specByID[spec.ID] = spec
	}
	for _, prediction := range predictions {
		spec, exists := specByID[prediction.ModelSpecID]
		if !exists {
			spec = api.PredictionModelSpec{ID: prediction.ModelSpecID, ModelKey: "unknown", Version: "unknown", HorizonMinutes: prediction.HorizonMinutes}
		}
		row, err := s.evaluatePrediction(now, prediction, spec)
		if err != nil {
			return err
		}
		if err := s.db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "prediction_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"model_spec_id", "model_key", "model_version", "entity_type", "entity_key",
				"gpu_asset_id", "gpu_uuid", "node_ip", "horizon_minutes", "probability",
				"decision_threshold", "predicted_positive", "prediction_evaluated_at",
				"window_start_at", "window_end_at", "maturity_status", "maturity_reason",
				"rule_actual_value", "rule_outcome", "rule_label_id", "rule_label_quality",
				"rule_decision_version", "rule_decision_reason", "final_actual_value",
				"final_outcome", "final_source", "updated_at",
			}),
		}).Create(&row).Error; err != nil {
			return err
		}
		// Human decisions are authoritative and survive every rule replay.
		var stored api.PredictionOutcomeEvaluation
		if err := s.db.Where("prediction_id = ?", prediction.ID).First(&stored).Error; err != nil {
			return err
		}
		if stored.HumanActualValue != nil {
			outcome := classify(stored.PredictedPositive, *stored.HumanActualValue)
			if err := s.db.Model(&stored).Updates(map[string]any{
				"human_outcome": outcome, "final_actual_value": stored.HumanActualValue,
				"final_outcome": outcome, "final_source": "human_override",
			}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) evaluatePrediction(now time.Time, prediction api.HardwareRiskPrediction, spec api.PredictionModelSpec) (api.PredictionOutcomeEvaluation, error) {
	horizon := prediction.HorizonMinutes
	if horizon <= 0 {
		horizon = spec.HorizonMinutes
	}
	windowStart := prediction.EvaluatedAt
	if windowStart.IsZero() {
		windowStart = prediction.ObservedAt
	}
	windowEnd := prediction.ExpiresAt
	if windowEnd.IsZero() && !windowStart.IsZero() && horizon > 0 {
		windowEnd = windowStart.Add(time.Duration(horizon) * time.Minute)
	}
	row := api.PredictionOutcomeEvaluation{
		PredictionID: prediction.ID, ModelSpecID: prediction.ModelSpecID,
		ModelKey: spec.ModelKey, ModelVersion: spec.Version, EntityType: prediction.EntityType,
		EntityKey: prediction.EntityKey, GPUAssetID: prediction.GPUAssetID, GPUUUID: prediction.GPUUUID,
		NodeIP: prediction.NodeIP, HorizonMinutes: horizon, Probability: prediction.Probability,
		DecisionThreshold: spec.DecisionThreshold, PredictionEvaluatedAt: windowStart,
		WindowStartAt: windowStart, WindowEndAt: windowEnd, MaturityStatus: "pending",
		RuleOutcome: "pending", RuleDecisionVersion: OutcomeRuleVersion,
		FinalOutcome: "pending", FinalSource: "rule",
	}
	if prediction.Probability == nil || spec.DecisionThreshold == nil {
		row.MaturityStatus, row.MaturityReason = "censored", "prediction probability or released decision threshold is unavailable"
		row.RuleOutcome, row.FinalOutcome = "censored", "censored"
		return row, nil
	}
	row.PredictedPositive = *prediction.Probability >= *spec.DecisionThreshold
	if strings.TrimSpace(prediction.GPUUUID) == "" {
		row.MaturityStatus, row.MaturityReason = "censored", "stable GPU UUID is unavailable"
		row.RuleOutcome, row.FinalOutcome = "censored", "censored"
		return row, nil
	}
	if windowStart.IsZero() || windowEnd.IsZero() || !windowEnd.After(windowStart) {
		row.MaturityStatus, row.MaturityReason = "censored", "prediction window is invalid"
		row.RuleOutcome, row.FinalOutcome = "censored", "censored"
		return row, nil
	}
	if now.Before(windowEnd) {
		row.MaturityReason = "prediction horizon has not elapsed"
		return row, nil
	}

	var label api.FailureLabel
	err := s.db.
		Where("LOWER(gpu_uuid) = ? AND label_value = ? AND excluded = ? AND occurred_at > ? AND occurred_at <= ? AND available_at <= ?",
			strings.ToLower(strings.TrimSpace(prediction.GPUUUID)), 1, false, windowStart, windowEnd, now).
		Order("CASE quality_tier WHEN 'confirmed' THEN 3 WHEN 'strong_proxy' THEN 2 WHEN 'weak_proxy' THEN 1 ELSE 0 END DESC, occurred_at, id").
		First(&label).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return row, err
	}
	if err == nil {
		actual := 1
		row.MaturityStatus, row.MaturityReason = "matured", "eligible failure label occurred inside the prediction horizon"
		row.RuleActualValue, row.RuleLabelID, row.RuleLabelQuality = &actual, label.ID, label.QualityTier
		row.RuleDecisionReason = fmt.Sprintf("%s label %d matched by GPU UUID", label.QualityTier, label.ID)
		row.RuleOutcome = classify(row.PredictedPositive, actual)
		row.FinalActualValue, row.FinalOutcome = &actual, row.RuleOutcome
		return row, nil
	}
	if now.Before(windowEnd.Add(24 * time.Hour)) {
		row.MaturityReason = "negative censoring window has not elapsed"
		return row, nil
	}
	actual := 0
	row.MaturityStatus, row.MaturityReason = "matured", "full horizon and 24-hour negative censoring window elapsed without an eligible failure label"
	row.RuleActualValue, row.RuleDecisionReason = &actual, row.MaturityReason
	row.RuleOutcome = classify(row.PredictedPositive, actual)
	row.FinalActualValue, row.FinalOutcome = &actual, row.RuleOutcome
	return row, nil
}

func classify(predictedPositive bool, actual int) string {
	if predictedPositive {
		if actual == 1 {
			return "tp"
		}
		return "fp"
	}
	if actual == 1 {
		return "fn"
	}
	return "tn"
}

func (s *Service) Outcomes(limit int) ([]api.PredictionOutcomeEvaluation, error) {
	rows, _, err := s.OutcomesPage(limit, 0)
	return rows, err
}

func (s *Service) OutcomesPage(limit, offset int) ([]api.PredictionOutcomeEvaluation, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var total int64
	if err := s.db.Model(&api.PredictionOutcomeEvaluation{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []api.PredictionOutcomeEvaluation
	err := s.db.Order("prediction_evaluated_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

func (s *Service) Accuracy() (AccuracySummary, error) {
	var rows []api.PredictionOutcomeEvaluation
	if err := s.db.Order("model_key, model_version, horizon_minutes").Find(&rows).Error; err != nil {
		return AccuracySummary{}, err
	}
	return accuracyFromRows(rows, s.now()), nil
}

func accuracyFromRows(rows []api.PredictionOutcomeEvaluation, evaluatedAt time.Time) AccuracySummary {
	summary := AccuracySummary{RuleDecisionVersion: OutcomeRuleVersion, EvaluatedAt: evaluatedAt}
	type sliceKey struct {
		key, version string
		horizon      int
	}
	byKey := map[sliceKey]*AccuracySlice{}
	rowsByKey := map[sliceKey][]api.PredictionOutcomeEvaluation{}
	for _, row := range rows {
		if row.MaturityStatus == "pending" {
			summary.Pending++
		} else if row.MaturityStatus == "censored" {
			summary.Censored++
		}
		if row.HumanActualValue != nil {
			summary.HumanOverrides++
		}
		addOutcome(&summary.Rule, row.RuleOutcome)
		addOutcome(&summary.Final, row.FinalOutcome)
		key := sliceKey{row.ModelKey, row.ModelVersion, row.HorizonMinutes}
		item := byKey[key]
		if item == nil {
			item = &AccuracySlice{ModelKey: key.key, ModelVersion: key.version, HorizonMinutes: key.horizon}
			byKey[key] = item
		}
		rowsByKey[key] = append(rowsByKey[key], row)
		addOutcome(&item.Rule, row.RuleOutcome)
		addOutcome(&item.Final, row.FinalOutcome)
	}
	finalizeMetrics(&summary.Rule)
	finalizeMetrics(&summary.Final)
	summary.Rule.RankingAtK = rankingAtK(rows, func(row api.PredictionOutcomeEvaluation) *int { return row.RuleActualValue })
	summary.Final.RankingAtK = rankingAtK(rows, func(row api.PredictionOutcomeEvaluation) *int { return row.FinalActualValue })
	summary.Rule.NodeRankingAtK = nodeRankingAtK(rows, func(row api.PredictionOutcomeEvaluation) *int { return row.RuleActualValue })
	summary.Final.NodeRankingAtK = nodeRankingAtK(rows, func(row api.PredictionOutcomeEvaluation) *int { return row.FinalActualValue })
	for _, item := range byKey {
		finalizeMetrics(&item.Rule)
		finalizeMetrics(&item.Final)
		sliceRows := rowsByKey[sliceKey{key: item.ModelKey, version: item.ModelVersion, horizon: item.HorizonMinutes}]
		item.Rule.RankingAtK = rankingAtK(sliceRows, func(row api.PredictionOutcomeEvaluation) *int { return row.RuleActualValue })
		item.Final.RankingAtK = rankingAtK(sliceRows, func(row api.PredictionOutcomeEvaluation) *int { return row.FinalActualValue })
		item.Rule.NodeRankingAtK = nodeRankingAtK(sliceRows, func(row api.PredictionOutcomeEvaluation) *int { return row.RuleActualValue })
		item.Final.NodeRankingAtK = nodeRankingAtK(sliceRows, func(row api.PredictionOutcomeEvaluation) *int { return row.FinalActualValue })
		summary.ByModel = append(summary.ByModel, *item)
	}
	sort.Slice(summary.ByModel, func(i, j int) bool {
		if summary.ByModel[i].ModelKey != summary.ByModel[j].ModelKey {
			return summary.ByModel[i].ModelKey < summary.ByModel[j].ModelKey
		}
		if summary.ByModel[i].ModelVersion != summary.ByModel[j].ModelVersion {
			return summary.ByModel[i].ModelVersion < summary.ByModel[j].ModelVersion
		}
		return summary.ByModel[i].HorizonMinutes < summary.ByModel[j].HorizonMinutes
	})
	return summary
}

func (s *Service) OutcomeReport() (OutcomeReport, error) {
	var rows []api.PredictionOutcomeEvaluation
	if err := s.db.Order("model_key, model_version, horizon_minutes").Find(&rows).Error; err != nil {
		return OutcomeReport{}, err
	}
	generatedAt := s.now()
	accuracy := accuracyFromRows(rows, generatedAt)
	maturity := outcomeMaturity(rows)
	report := OutcomeReport{
		Version:          "prediction-outcome-report-v1",
		FrameworkVersion: FrameworkVersion,
		Mode:             "read_only_shadow",
		Safety: OutcomeSafety{
			ReadOnlyShadow: true, NoAlertEmitted: true, NoActionTaken: true,
			ProbabilityUse: "ranking and retrospective/prospective validation only; not an operational alert or automated action signal",
		},
		SampleMaturity:      maturity,
		Stability:           outcomeStability(maturity, accuracy),
		Accuracy:            accuracy,
		BaselineComparisons: baselineComparisons(rows),
		Interpretation: []string{
			"rule metrics preserve deterministic label-reconciliation provenance",
			"final metrics include human overrides backed by repair or replacement evidence",
			"gpu ranking evaluates per-device probabilities; node ranking aggregates by node for HeaRank-style node-risk validation",
			"baseline comparisons use the same matured outcome set as naive all-negative and all-positive references",
			"pending and censored predictions are excluded from metric denominators",
		},
		RecommendedNextRun: []string{
			"wait for additional matured shadow windows before changing thresholds",
			"compare node_ranking_at_k against HeaRank 7d challenger with the same time split",
			"review false positives and false negatives before promoting any candidate",
		},
		GeneratedAt: generatedAt,
	}
	return report, nil
}

func outcomeStability(maturity OutcomeMaturity, accuracy AccuracySummary) OutcomeStability {
	stability := OutcomeStability{
		MinimumMaturedSamples:      OutcomeMinimumMaturedSamples,
		MinimumMaturedRatio:        OutcomeMinimumMaturedRatio,
		MinimumProbabilityCoverage: OutcomeMinimumProbabilityCoverage,
		MinimumPositiveSamples:     OutcomeMinimumPositiveSamples,
		PositiveSamples:            accuracy.Final.TP + accuracy.Final.FN,
		FalsePositiveReviewHint:    "review false positives for operational-only events, weak labels, and distribution-shifted feature windows",
		FalseNegativeReviewHint:    "review false negatives for missing source metrics, delayed labels, and hardware events outside the modeled horizon",
		RecommendedReview: []string{
			"keep outcome metrics read-only until stability status is comparable",
			"use the same matured scored denominator when comparing rules, Logistic, and challenger policies",
		},
	}
	if maturity.Total > 0 {
		stability.ProbabilityCoverage = float64(maturity.ProbabilityScored) / float64(maturity.Total)
	}
	switch {
	case maturity.Matured == 0:
		stability.BlockingReasons = append(stability.BlockingReasons, "no mature shadow outcomes are available")
		stability.RankingInterpretationStatus = "no_scored_rows"
	case maturity.ProbabilityScored == 0:
		stability.BlockingReasons = append(stability.BlockingReasons, "no probability-scored outcomes are available")
		stability.RankingInterpretationStatus = "no_scored_rows"
	case stability.PositiveSamples == 0:
		stability.BlockingReasons = append(stability.BlockingReasons, "no positive mature outcomes are available")
		stability.RankingInterpretationStatus = "no_positives"
	default:
		stability.RankingInterpretationStatus = "comparable"
	}
	if maturity.Matured > 0 && maturity.Matured < OutcomeMinimumMaturedSamples {
		stability.BlockingReasons = append(stability.BlockingReasons, "mature outcome count below stability gate")
		stability.RankingInterpretationStatus = "insufficient_matured_samples"
	}
	if maturity.Total > 0 && maturity.MaturedRatio < OutcomeMinimumMaturedRatio {
		stability.BlockingReasons = append(stability.BlockingReasons, "mature outcome ratio below stability gate")
	}
	if maturity.Total > 0 && stability.ProbabilityCoverage < OutcomeMinimumProbabilityCoverage {
		stability.BlockingReasons = append(stability.BlockingReasons, "probability-scored coverage below stability gate")
	}
	if stability.PositiveSamples > 0 && stability.PositiveSamples < OutcomeMinimumPositiveSamples {
		stability.BlockingReasons = append(stability.BlockingReasons, "positive mature outcome count below stability gate")
	}
	stability.BlockingReasons = uniqueSorted(stability.BlockingReasons)
	if len(stability.BlockingReasons) == 0 {
		stability.Status = "comparable"
	} else if maturity.Matured == 0 || maturity.ProbabilityScored == 0 || stability.PositiveSamples == 0 {
		stability.Status = "blocked"
	} else {
		stability.Status = "exploratory"
	}
	stability.RecommendedReview = uniqueSorted(append(stability.RecommendedReview, stability.BlockingReasons...))
	return stability
}

func baselineComparisons(rows []api.PredictionOutcomeEvaluation) []BaselineComparison {
	policies := []struct {
		name     string
		policy   string
		positive bool
	}{
		{name: "all_negative", policy: "predict every matured scored sample as non-failure", positive: false},
		{name: "all_positive", policy: "predict every matured scored sample as failure", positive: true},
	}
	comparisons := make([]BaselineComparison, 0, len(policies))
	for _, policy := range policies {
		item := BaselineComparison{Name: policy.name, PredictionPolicy: policy.policy}
		for _, row := range rows {
			if row.MaturityStatus != "matured" || row.Probability == nil {
				continue
			}
			if row.RuleActualValue != nil {
				addOutcome(&item.Rule, classify(policy.positive, *row.RuleActualValue))
			}
			if row.FinalActualValue != nil {
				addOutcome(&item.Final, classify(policy.positive, *row.FinalActualValue))
			}
		}
		finalizeMetrics(&item.Rule)
		finalizeMetrics(&item.Final)
		comparisons = append(comparisons, item)
	}
	return comparisons
}

func outcomeMaturity(rows []api.PredictionOutcomeEvaluation) OutcomeMaturity {
	maturity := OutcomeMaturity{Total: len(rows)}
	nodes := map[string]struct{}{}
	for _, row := range rows {
		switch row.MaturityStatus {
		case "matured":
			maturity.Matured++
		case "pending":
			maturity.Pending++
		case "censored":
			maturity.Censored++
		}
		if row.Probability != nil {
			maturity.ProbabilityScored++
		}
		if row.MaturityStatus == "matured" && row.Probability != nil && strings.TrimSpace(row.NodeIP) != "" {
			nodes[strings.TrimSpace(row.NodeIP)] = struct{}{}
		}
	}
	if maturity.Total > 0 {
		maturity.MaturedRatio = float64(maturity.Matured) / float64(maturity.Total)
	}
	maturity.NodeEligible = len(nodes)
	return maturity
}

func addOutcome(metrics *AccuracyMetrics, outcome string) {
	switch outcome {
	case "tp":
		metrics.TP++
	case "fp":
		metrics.FP++
	case "fn":
		metrics.FN++
	case "tn":
		metrics.TN++
	default:
		return
	}
	metrics.Evaluated++
}

func finalizeMetrics(metrics *AccuracyMetrics) {
	ratio := func(numerator, denominator int) *float64 {
		if denominator == 0 {
			return nil
		}
		value := float64(numerator) / float64(denominator)
		return &value
	}
	metrics.Precision = ratio(metrics.TP, metrics.TP+metrics.FP)
	metrics.Recall = ratio(metrics.TP, metrics.TP+metrics.FN)
	metrics.Specificity = ratio(metrics.TN, metrics.TN+metrics.FP)
	metrics.FalsePositiveRate = ratio(metrics.FP, metrics.FP+metrics.TN)
	metrics.FalseNegativeRate = ratio(metrics.FN, metrics.FN+metrics.TP)
	metrics.Accuracy = ratio(metrics.TP+metrics.TN, metrics.Evaluated)
}

type rankedOutcome struct {
	probability float64
	actual      int
}

func rankingAtK(rows []api.PredictionOutcomeEvaluation, actualValue func(api.PredictionOutcomeEvaluation) *int) []RankingAtK {
	items := make([]rankedOutcome, 0, len(rows))
	for _, row := range rows {
		if row.MaturityStatus != "matured" || row.Probability == nil {
			continue
		}
		actual := actualValue(row)
		if actual == nil || (*actual != 0 && *actual != 1) {
			continue
		}
		items = append(items, rankedOutcome{probability: *row.Probability, actual: *actual})
	}
	return rankingFromItems(items)
}

func nodeRankingAtK(rows []api.PredictionOutcomeEvaluation, actualValue func(api.PredictionOutcomeEvaluation) *int) []RankingAtK {
	byNode := map[string]rankedOutcome{}
	for _, row := range rows {
		if row.MaturityStatus != "matured" || row.Probability == nil || strings.TrimSpace(row.NodeIP) == "" {
			continue
		}
		actual := actualValue(row)
		if actual == nil || (*actual != 0 && *actual != 1) {
			continue
		}
		key := strings.TrimSpace(row.NodeIP)
		item, exists := byNode[key]
		if !exists || *row.Probability > item.probability {
			item.probability = *row.Probability
		}
		if *actual == 1 {
			item.actual = 1
		}
		byNode[key] = item
	}
	items := make([]rankedOutcome, 0, len(byNode))
	for _, item := range byNode {
		items = append(items, item)
	}
	return rankingFromItems(items)
}

func rankingFromItems(items []rankedOutcome) []RankingAtK {
	if len(items) == 0 {
		return nil
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].probability > items[j].probability
	})
	positiveTotal := 0
	for _, item := range items {
		if item.actual == 1 {
			positiveTotal++
		}
	}
	baseRate := float64(positiveTotal) / float64(len(items))
	ks := []int{1, 3, 5, 10}
	out := make([]RankingAtK, 0, len(ks))
	for _, k := range ks {
		limit := k
		if limit > len(items) {
			limit = len(items)
		}
		hits := 0
		dcg := 0.0
		for index := 0; index < limit; index++ {
			if items[index].actual == 1 {
				hits++
				dcg += 1 / math.Log2(float64(index+2))
			}
		}
		metric := RankingAtK{K: k, Eligible: len(items), Positives: positiveTotal, Hits: hits, Status: rankingInterpretationStatus(len(items), positiveTotal)}
		precision := float64(hits) / float64(limit)
		metric.Precision = &precision
		if positiveTotal > 0 {
			recall := float64(hits) / float64(positiveTotal)
			metric.Recall = &recall
			idealLimit := positiveTotal
			if idealLimit > limit {
				idealLimit = limit
			}
			idealDCG := 0.0
			for index := 0; index < idealLimit; index++ {
				idealDCG += 1 / math.Log2(float64(index+2))
			}
			if idealDCG > 0 {
				ndcg := dcg / idealDCG
				metric.NDCG = &ndcg
			}
		}
		if baseRate > 0 {
			lift := precision / baseRate
			metric.Lift = &lift
		}
		out = append(out, metric)
	}
	return out
}

func rankingInterpretationStatus(eligible, positives int) string {
	if eligible == 0 {
		return "no_scored_rows"
	}
	if positives == 0 {
		return "no_positives"
	}
	if eligible < OutcomeMinimumMaturedSamples {
		return "insufficient_matured_samples"
	}
	return "comparable"
}

func (s *Service) OverrideOutcome(id uint, input OutcomeOverride) (api.PredictionOutcomeEvaluation, error) {
	if input.ActualValue != 0 && input.ActualValue != 1 {
		return api.PredictionOutcomeEvaluation{}, fmt.Errorf("actual_value must be 0 or 1")
	}
	if strings.TrimSpace(input.Reason) == "" {
		return api.PredictionOutcomeEvaluation{}, fmt.Errorf("reason is required")
	}
	decidedBy := strings.TrimSpace(input.DecidedBy)
	if decidedBy == "" {
		decidedBy = "operator"
	}
	var row api.PredictionOutcomeEvaluation
	if err := s.db.First(&row, id).Error; err != nil {
		return row, err
	}
	if row.MaturityStatus != "matured" {
		return row, fmt.Errorf("only matured outcomes can be overridden")
	}
	now := s.now()
	outcome := classify(row.PredictedPositive, input.ActualValue)
	if err := s.db.Model(&row).Updates(map[string]any{
		"human_actual_value": input.ActualValue, "human_outcome": outcome,
		"human_decision": "override", "human_reason": strings.TrimSpace(input.Reason),
		"human_decided_by": decidedBy, "human_decided_at": &now,
		"final_actual_value": input.ActualValue, "final_outcome": outcome,
		"final_source": "human_override",
	}).Error; err != nil {
		return row, err
	}
	return row, s.db.First(&row, id).Error
}

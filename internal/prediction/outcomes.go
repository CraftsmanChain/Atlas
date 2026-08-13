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
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []api.PredictionOutcomeEvaluation
	err := s.db.Order("prediction_evaluated_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) Accuracy() (AccuracySummary, error) {
	var rows []api.PredictionOutcomeEvaluation
	if err := s.db.Order("model_key, model_version, horizon_minutes").Find(&rows).Error; err != nil {
		return AccuracySummary{}, err
	}
	summary := AccuracySummary{RuleDecisionVersion: OutcomeRuleVersion, EvaluatedAt: s.now()}
	type sliceKey struct {
		key, version string
		horizon      int
	}
	byKey := map[sliceKey]*AccuracySlice{}
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
		var sliceRows []api.PredictionOutcomeEvaluation
		for _, row := range rows {
			if row.ModelKey == item.ModelKey && row.ModelVersion == item.ModelVersion && row.HorizonMinutes == item.HorizonMinutes {
				sliceRows = append(sliceRows, row)
			}
		}
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
	return summary, nil
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
		metric := RankingAtK{K: k, Eligible: len(items), Positives: positiveTotal, Hits: hits}
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

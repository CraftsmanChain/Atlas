package history

import "testing"

func TestLogisticBaselineSeparatesSignalAndPreservesMissingAsMean(t *testing.T) {
	rows := make([]trainingMatrixRow, 0, 80)
	for i := 0; i < 80; i++ {
		label := 0
		value := -2.0 + float64(i%5)/10
		if i >= 40 {
			label, value = 1, 2+float64(i%5)/10
		}
		features := map[string]float64{"gpu_temp_slope_per_hour_24h": value}
		if i == 0 {
			features = map[string]float64{}
		}
		rows = append(rows, trainingMatrixRow{LabelValue: label, TrainingWeight: 1, Features: features})
	}
	model := fitLogistic(rows, []string{"gpu_temp_slope_per_hour_24h"}, 60)
	scores := scoreRows(model, rows)
	metrics := evaluateScores(scores, 0.5)
	if metrics.ROCAUC < 0.95 || metrics.Precision < 0.9 || metrics.Recall < 0.9 {
		t.Fatalf("unexpected separable metrics: %+v", metrics)
	}
	missing := scoreRows(model, []trainingMatrixRow{{Features: map[string]float64{}}})[0].score
	if missing <= 0 || missing >= 1 {
		t.Fatalf("missing-value mean imputation produced invalid probability %v", missing)
	}
}

func TestSafeBaselineColumnsExcludeOccurredFaultIndicators(t *testing.T) {
	rows := []trainingMatrixRow{{Features: map[string]float64{"gpu_temp_mean_24h": 1, "xid_current_delta_24h": 1, "uncorrectable_remapped_rows_delta_24h": 1, "row_remap_failure_last_24h": 1}}}
	columns := safeBaselineColumns(rows)
	if len(columns) != 1 || columns[0] != "gpu_temp_mean_24h" {
		t.Fatalf("unsafe feature policy: %v", columns)
	}
}

func TestRankAUCTreatsTiesAsHalfCredit(t *testing.T) {
	scores := []scoredLabel{{score: 0.5, label: 0}, {score: 0.5, label: 1}}
	if auc := rankAUC(scores); auc != 0.5 {
		t.Fatalf("tie AUC=%v", auc)
	}
}

func TestStratifiedMetricsUsePairedLabelDimensions(t *testing.T) {
	rows := []trainingMatrixRow{
		{LabelMetadata: trainingLabelMetadata{EventTypes: []string{"xid_94_contained_ecc"}}},
		{LabelMetadata: trainingLabelMetadata{EventTypes: []string{"xid_94_contained_ecc"}}},
		{LabelMetadata: trainingLabelMetadata{EventTypes: []string{"gpu_dropout"}}},
	}
	scores := []scoredLabel{{score: 0.9, label: 1, threshold: 0.5}, {score: 0.1, label: 0, threshold: 0.5}, {score: 0.8, label: 1, threshold: 0.5}}
	metrics := stratifiedTestMetrics(rows, scores, func(row trainingMatrixRow) []string { return row.LabelMetadata.EventTypes })
	if metrics["xid_94_contained_ecc"].Count != 2 || metrics["xid_94_contained_ecc"].ROCAUC != 1 || metrics["gpu_dropout"].Count != 1 {
		t.Fatalf("unexpected label-stratified metrics: %+v", metrics)
	}
}

func TestMacroStratifiedMetricsAverageHorizonsWithoutPoolingScores(t *testing.T) {
	horizons := []baselineHorizonReport{
		{TestByEventType: map[string]baselineMetrics{"gpu_dropout": {Count: 20, Positive: 5, Control: 15, ROCAUC: 0.75, PRAUC: 0.625}}},
		{TestByEventType: map[string]baselineMetrics{"gpu_dropout": {Count: 40, Positive: 10, Control: 30, ROCAUC: 0.25, PRAUC: 0.375}}},
	}
	metrics := macroStratifiedMetrics(horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByEventType })["gpu_dropout"]
	if metrics.Count != 60 || metrics.Positive != 15 || metrics.Control != 45 || metrics.ROCAUC != 0.5 || metrics.PRAUC != 0.5 {
		t.Fatalf("unexpected horizon-macro metrics: %+v", metrics)
	}
}

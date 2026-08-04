package history

import (
	"fmt"
	"testing"
)

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
	rows := []trainingMatrixRow{{Features: map[string]float64{"gpu_temp_mean_24h": 1, "gpu_temp_sample_count_24h": 289, "xid_current_delta_24h": 1, "uncorrectable_remapped_rows_delta_24h": 1, "row_remap_failure_last_24h": 1}}}
	columns := safeBaselineColumns(rows)
	if len(columns) != 1 || columns[0] != "gpu_temp_mean_24h" {
		t.Fatalf("unsafe feature policy: %v", columns)
	}
}

func TestBaselineFeatureAuditRecordsEveryExcludedColumn(t *testing.T) {
	rows := []trainingMatrixRow{{Features: map[string]float64{
		"gpu_temp_mean_24h": 1, "gpu_temp_sample_count_24h": 289,
		"xid_current_delta_24h": 1, "uncorrected_ecc_delta_24h": 1,
	}}}
	audit := auditBaselineFeatures(rows)
	if audit.Status != "passed" || audit.SourceFeatureCount != 4 || audit.SelectedFeatureCount != 1 || audit.ExcludedFeatureCount != 3 || audit.ProhibitedSelectedCount != 0 {
		t.Fatalf("unexpected feature audit: %+v", audit)
	}
	if audit.SelectedColumns[0] != "gpu_temp_mean_24h" {
		t.Fatalf("unexpected selected columns: %v", audit.SelectedColumns)
	}
	reasons := map[string]string{}
	for _, exclusion := range audit.Exclusions {
		reasons[exclusion.Feature] = exclusion.Reason
	}
	if reasons["gpu_temp_sample_count_24h"] != "quality_only_sampling_count" || reasons["xid_current_delta_24h"] != "occurred_fault_xid" || reasons["uncorrected_ecc_delta_24h"] != "occurred_fault_ecc" {
		t.Fatalf("unexpected exclusion reasons: %v", reasons)
	}
}

func TestRankAUCTreatsTiesAsHalfCredit(t *testing.T) {
	scores := []scoredLabel{{score: 0.5, label: 0}, {score: 0.5, label: 1}}
	if auc := rankAUC(scores); auc != 0.5 {
		t.Fatalf("tie AUC=%v", auc)
	}
}

func TestBootstrapBaselineUncertaintySeparatesStableAndInverseSignals(t *testing.T) {
	stable, inverse := make([]scoredLabel, 0, 80), make([]scoredLabel, 0, 80)
	rows := make([]trainingMatrixRow, 0, 80)
	for index := 0; index < 40; index++ {
		stable = append(stable, scoredLabel{score: 0.1 + float64(index%5)/100, label: 0})
		stable = append(stable, scoredLabel{score: 0.8 + float64(index%5)/100, label: 1})
		inverse = append(inverse, scoredLabel{score: 0.8 + float64(index%5)/100, label: 0})
		inverse = append(inverse, scoredLabel{score: 0.1 + float64(index%5)/100, label: 1})
		rows = append(rows, trainingMatrixRow{GPUUUID: fmt.Sprintf("GPU-%02d", index), RowKey: fmt.Sprintf("C-%02d", index)}, trainingMatrixRow{GPUUUID: fmt.Sprintf("GPU-%02d", index), RowKey: fmt.Sprintf("P-%02d", index)})
	}
	stableResult := bootstrapBaselineUncertainty(rows, stable, 60)
	if stableResult.Status != "candidate_signal" || stableResult.ROCAUCLower != 1 || stableResult.PRAUCLower <= stableResult.NullPRAUC {
		t.Fatalf("unexpected stable uncertainty: %+v", stableResult)
	}
	inverseResult := bootstrapBaselineUncertainty(rows, inverse, 60)
	if inverseResult.Status != "inverse_signal" || inverseResult.ROCAUCUpper != 0 {
		t.Fatalf("unexpected inverse uncertainty: %+v", inverseResult)
	}
}

func TestBootstrapBaselineUncertaintyIsDeterministic(t *testing.T) {
	scores := []scoredLabel{{score: 0.1, label: 0}, {score: 0.2, label: 0}, {score: 0.3, label: 0}, {score: 0.4, label: 1}, {score: 0.5, label: 1}, {score: 0.6, label: 1}}
	rows := []trainingMatrixRow{{GPUUUID: "GPU-1"}, {GPUUUID: "GPU-2"}, {GPUUUID: "GPU-3"}, {GPUUUID: "GPU-1"}, {GPUUUID: "GPU-2"}, {GPUUUID: "GPU-3"}}
	first, second := bootstrapBaselineUncertainty(rows, scores, 4320), bootstrapBaselineUncertainty(rows, scores, 4320)
	if first != second {
		t.Fatalf("bootstrap must be reproducible: first=%+v second=%+v", first, second)
	}
}

func TestCrossSplitStabilityRequiresValidationAndTestAgreement(t *testing.T) {
	candidate := baselineUncertainty{Status: "candidate_signal"}
	inverse := baselineUncertainty{Status: "inverse_signal"}
	inconclusive := baselineUncertainty{Status: "inconclusive"}
	if status := crossSplitStability(candidate, candidate); status != "robust_candidate" {
		t.Fatalf("candidate agreement status=%s", status)
	}
	if status := crossSplitStability(candidate, inverse); status != "temporal_instability" {
		t.Fatalf("opposite split status=%s", status)
	}
	if status := crossSplitStability(inverse, inverse); status != "consistent_inverse" {
		t.Fatalf("inverse agreement status=%s", status)
	}
	if status := crossSplitStability(candidate, inconclusive); status != "inconclusive" {
		t.Fatalf("uncertain split status=%s", status)
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

func TestScopedReadyMatrixRowsRejectsInsufficientFaultTypes(t *testing.T) {
	rows := make([]trainingMatrixRow, 0, 150)
	add := func(eventType, split string, positives, controls, positiveGPUs int) {
		metadata := trainingLabelMetadata{EventTypes: []string{eventType}}
		for index := 0; index < positives; index++ {
			rows = append(rows, trainingMatrixRow{LabelValue: 1, Split: split, HorizonMinutes: 60, GPUUUID: fmt.Sprintf("GPU-%s-P-%02d", split, index%positiveGPUs), ModelName: "H100", LabelMetadata: metadata})
		}
		for index := 0; index < controls; index++ {
			rows = append(rows, trainingMatrixRow{LabelValue: 0, Split: split, HorizonMinutes: 60, GPUUUID: fmt.Sprintf("GPU-%s-C-%02d", split, index), ModelName: "H100", LabelMetadata: metadata})
		}
	}
	add("xid_94_contained_ecc", "train", 30, 60, 10)
	add("xid_94_contained_ecc", "validation", 10, 20, 5)
	add("xid_94_contained_ecc", "test", 10, 20, 5)
	add("gpu_dropout", "train", 1, 1, 1)
	scoped, err := scopedReadyMatrixRows("matrix", rows, "xid_94_contained_ecc", "H100")
	if err != nil || len(scoped) != 150 {
		t.Fatalf("unexpected ready scope rows=%d err=%v", len(scoped), err)
	}
	if _, err := scopedReadyMatrixRows("matrix", rows, "gpu_dropout", "H100"); err == nil {
		t.Fatal("insufficient fault type must not train a scoped model")
	}
}

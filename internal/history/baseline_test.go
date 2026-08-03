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

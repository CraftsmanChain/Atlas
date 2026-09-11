package history

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
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

func TestShallowGBDTLearnsNonlinearThresholdsDeterministically(t *testing.T) {
	rows := make([]trainingMatrixRow, 0, 120)
	for index := 0; index < 120; index++ {
		value := -3.0 + 6*float64(index)/119
		label := 0
		if value < -1.2 || value > 1.2 {
			label = 1
		}
		rows = append(rows, trainingMatrixRow{LabelValue: label, TrainingWeight: 1, Features: map[string]float64{"gpu_temp_mean_24h": value}})
	}
	first := fitShallowGBDT(rows, []string{"gpu_temp_mean_24h"}, 60)
	second := fitShallowGBDT(rows, []string{"gpu_temp_mean_24h"}, 60)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("shallow GBDT training must be deterministic")
	}
	metrics := evaluateScores(scoreShallowGBDTRowsWithoutCalibration(first, rows), 0.5)
	if len(first.Trees) == 0 || metrics.ROCAUC < 0.95 || metrics.Precision < 0.9 || metrics.Recall < 0.9 {
		t.Fatalf("nonlinear threshold signal was not learned: trees=%d metrics=%+v", len(first.Trees), metrics)
	}
}

func TestShallowGBDTSelectionPreservesMetricFamilyDiversity(t *testing.T) {
	columns := []string{"correctable_remapped_rows_delta_15m", "correctable_remapped_rows_delta_1h", "gpu_temp_mean_15m", "gpu_temp_mean_1h"}
	rows := make([]trainingMatrixRow, 0, 40)
	for index := 0; index < 40; index++ {
		features := map[string]float64{}
		for columnIndex, column := range columns {
			features[column] = float64((index + columnIndex) % 7)
		}
		rows = append(rows, trainingMatrixRow{LabelValue: index % 2, Features: features})
	}
	selection := selectShallowGBDTFeatures(rows, columns)
	selected := selectedFeatureNames(selection)
	if selection.Status != "passed" || len(selected) != len(columns) {
		t.Fatalf("expected all diverse safe features, got %+v", selection)
	}
	if selected[0] == selected[1] || !reflect.DeepEqual(selection, selectShallowGBDTFeatures(rows, columns)) {
		t.Fatalf("selection must be diverse and deterministic: %+v", selection)
	}
}

func TestResolveBaselineAlgorithmDefaultsAndRejectsUnknownRuntime(t *testing.T) {
	algorithm, version, err := resolveBaselineAlgorithm("")
	if err != nil || algorithm != logisticRegressionAlgorithm || version != baselineModelVersion {
		t.Fatalf("legacy request default changed: algorithm=%q version=%q err=%v", algorithm, version, err)
	}
	algorithm, version, err = resolveBaselineAlgorithm(shallowGBDTAlgorithm)
	if err != nil || algorithm != shallowGBDTAlgorithm || version != shallowGBDTModelVersion {
		t.Fatalf("challenger resolution failed: algorithm=%q version=%q err=%v", algorithm, version, err)
	}
	algorithm, version, err = resolveBaselineAlgorithm(anomalyLogisticAlgorithm)
	if err != nil || algorithm != anomalyLogisticAlgorithm || version != anomalyLogisticModelVersion {
		t.Fatalf("cascade resolution failed: algorithm=%q version=%q err=%v", algorithm, version, err)
	}
	algorithm, version, err = resolveBaselineAlgorithm(anomalyAugmentedAlgorithm)
	if err != nil || algorithm != anomalyAugmentedAlgorithm || version != anomalyAugmentedModelVersion {
		t.Fatalf("augmented resolution failed: algorithm=%q version=%q err=%v", algorithm, version, err)
	}
	if _, _, err := resolveBaselineAlgorithm("xgboost_external"); err == nil {
		t.Fatal("unknown algorithm must be rejected before a build is queued")
	}
}

func TestFeatureWindowPolicyDefaultsFiltersAndRejectsUnknown(t *testing.T) {
	policy, err := resolveFeatureWindowPolicy("")
	if err != nil || policy != allAvailableWindowsPolicy {
		t.Fatalf("legacy requests must retain all windows: policy=%q err=%v", policy, err)
	}
	if _, err := resolveFeatureWindowPolicy("max_30d"); err == nil {
		t.Fatal("unsupported temporal ablation policy must be rejected before queueing")
	}
	rows := []trainingMatrixRow{{Features: map[string]float64{
		"gpu_temp_mean_24h": 1, "gpu_temp_mean_3d": 2, "gpu_temp_mean_7d": 3, "gpu_temp_mean_30d": 4,
	}}}
	tests := []struct {
		policy string
		want   []string
	}{
		{maximum24HourWindowPolicy, []string{"gpu_temp_mean_24h"}},
		{maximum3DayWindowPolicy, []string{"gpu_temp_mean_24h", "gpu_temp_mean_3d"}},
		{maximum7DayWindowPolicy, []string{"gpu_temp_mean_24h", "gpu_temp_mean_3d", "gpu_temp_mean_7d"}},
		{allAvailableWindowsPolicy, []string{"gpu_temp_mean_24h", "gpu_temp_mean_30d", "gpu_temp_mean_3d", "gpu_temp_mean_7d"}},
	}
	for _, test := range tests {
		audit := auditBaselineFeaturesForTargetAndWindowPolicy(rows, hardwareFailureTarget, test.policy)
		if !reflect.DeepEqual(audit.SelectedColumns, test.want) {
			t.Fatalf("policy %s selected %v, want %v", test.policy, audit.SelectedColumns, test.want)
		}
		if audit.SourceFeatureCount != audit.SelectedFeatureCount+audit.ExcludedFeatureCount {
			t.Fatalf("policy %s lost audit accounting: %+v", test.policy, audit)
		}
	}
}

func TestAnomalyAugmentedLogisticPreservesRowsAndLearnsSymmetricAnomalies(t *testing.T) {
	rows := make([]trainingMatrixRow, 0, 160)
	for index := 0; index < 160; index++ {
		label := 0
		value := float64(index%20)/100 - 0.1
		if index >= 80 {
			label = 1
			value = 3
			if index%2 == 0 {
				value = -3
			}
		}
		rows = append(rows, trainingMatrixRow{LabelValue: label, TrainingWeight: 1, Features: map[string]float64{"gpu_temp_mean_24h": value}})
	}
	model, err := fitAnomalyAugmentedLogistic(rows, []string{"gpu_temp_mean_24h"}, nil, 1440)
	if err != nil {
		t.Fatal(err)
	}
	metrics := evaluateScores(scoreAnomalyAugmentedRowsWithoutCalibration(model, rows), 0.5)
	if metrics.ROCAUC < 0.95 || metrics.Precision < 0.9 || metrics.Recall < 0.9 {
		t.Fatalf("continuous anomaly feature did not learn symmetric deviations: %+v", metrics)
	}
	if _, mutated := rows[0].Features[anomalySyntheticFeature]; mutated {
		t.Fatal("anomaly augmentation must not mutate immutable matrix rows")
	}
	second, err := fitAnomalyAugmentedLogistic(rows, []string{"gpu_temp_mean_24h"}, nil, 1440)
	if err != nil || !reflect.DeepEqual(model, second) {
		t.Fatalf("augmented training must be deterministic: err=%v", err)
	}
}

func TestAugmentedArtifactDistributionColumnsExcludeSyntheticFeature(t *testing.T) {
	artifact := baselineArtifact{AugmentedModels: []anomalyAugmentedLogisticModel{{
		Anomaly:    anomalyFilterModel{FeatureColumns: []string{"gpu_temp_mean_24h"}},
		Classifier: logisticModel{FeatureColumns: []string{"gpu_power_mean_24h", anomalySyntheticFeature}},
	}}}
	columns := uniqueBaselineFeatureColumns(artifact)
	want := []string{"gpu_power_mean_24h", "gpu_temp_mean_24h"}
	if !reflect.DeepEqual(columns, want) {
		t.Fatalf("synthetic feature cannot be materialized from the immutable matrix: got=%v want=%v", columns, want)
	}
}

func TestAnomalyLogisticCascadeFitsGateOnTrainingControlsDeterministically(t *testing.T) {
	rows := make([]trainingMatrixRow, 0, 120)
	for index := 0; index < 120; index++ {
		label := 0
		value := float64(index%20)/100 - 0.1
		if index >= 80 {
			label = 1
			value = 3 + float64(index%10)/10
		}
		rows = append(rows, trainingMatrixRow{LabelValue: label, TrainingWeight: 1, Features: map[string]float64{"gpu_temp_mean_24h": value, "gpu_power_mean_24h": value * 2}})
	}
	first, filtered, err := fitAnomalyLogisticCascade(rows, []string{"gpu_temp_mean_24h", "gpu_power_mean_24h"}, 60)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := fitAnomalyLogisticCascade(rows, []string{"gpu_temp_mean_24h", "gpu_power_mean_24h"}, 60)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("cascade must be deterministic: err=%v first=%+v second=%+v", err, first, second)
	}
	filterReport := describeAnomalyFilterSplit(rows, first.Filter)
	metrics := evaluateScores(scoreAnomalyLogisticRowsWithoutCalibration(first, rows), 0.5)
	if len(filtered) >= len(rows) || filterReport.PositiveRetention < 0.99 || filterReport.ControlRetention > 0.30 || metrics.ROCAUC < 0.95 {
		t.Fatalf("unexpected cascade behavior: filtered=%d report=%+v metrics=%+v", len(filtered), filterReport, metrics)
	}
}

func TestBuildShallowGBDTProducesOfflineOnlyImmutableArtifact(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:shallow-gbdt-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	rows := make([]trainingMatrixRow, 0, 120)
	for _, split := range []string{"train", "validation", "test"} {
		for index := 0; index < 40; index++ {
			value := -3.0 + 6*float64(index)/39
			label := 0
			if value < -1.2 || value > 1.2 {
				label = 1
			}
			rows = append(rows, trainingMatrixRow{RowKey: fmt.Sprintf("%s-%d", split, index), GPUUUID: fmt.Sprintf("GPU-%s-%d", split, index), ModelName: "H100", HorizonMinutes: 60, PredictionTarget: highPriorityXIDEventTarget, Split: split, LabelValue: label, TrainingWeight: 1, Features: map[string]float64{"gpu_temp_mean_24h": value}, LabelMetadata: trainingLabelMetadata{EventTypes: []string{"xid_94_contained_ecc"}}})
		}
	}
	matrixPath := filepath.Join(root, "matrix.jsonl")
	matrixSHA, err := writeJSONLines(matrixPath, rows)
	if err != nil {
		t.Fatal(err)
	}
	matrix := api.TrainingMatrixBuild{TrainingMatrixKey: "matrix-v7-test", Version: trainingMatrixVersion, Status: "completed", FeatureContractVersion: "1.10.0", MatrixPath: matrixPath, MatrixSHA256: matrixSHA, StartedAt: time.Now()}
	if err := db.Create(&matrix).Error; err != nil {
		t.Fatal(err)
	}
	build := api.BaselineModelBuild{BaselineModelKey: "gbdt-test", Version: shallowGBDTModelVersion, Status: "running", Algorithm: shallowGBDTAlgorithm, SourceMatrixBuildID: matrix.ID, SourceTrainingMatrixKey: matrix.TrainingMatrixKey, FeatureContractVersion: matrix.FeatureContractVersion, OutputDir: filepath.Join(root, "model"), StartedAt: time.Now()}
	if err := db.Create(&build).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{DatasetDir: root}, time.Second)
	if err := service.buildBaselineModels(&build); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&build, build.ID).Error; err != nil {
		t.Fatal(err)
	}
	if build.Status != "completed" || build.TrainedModelCount != 1 || build.ShadowCandidateCount != 0 || build.ArtifactSHA256 == "" {
		t.Fatalf("unexpected challenger build: %+v", build)
	}
	var artifact baselineArtifact
	if err := readJSONFile(build.ArtifactPath, &artifact); err != nil {
		t.Fatal(err)
	}
	report, err := service.BaselineModelReport(build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Algorithm != shallowGBDTAlgorithm || len(artifact.Models) != 0 || len(artifact.BoostedModels) != 1 || report.Mode != "offline_challenger_evaluation_only" {
		t.Fatalf("challenger runtime boundary was not preserved: artifact=%+v report_mode=%s", artifact, report.Mode)
	}
}

func TestBuildAnomalyLogisticProducesOfflineOnlyImmutableArtifact(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:anomaly-logistic-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	rows := make([]trainingMatrixRow, 0, 120)
	for _, split := range []string{"train", "validation", "test"} {
		for index := 0; index < 40; index++ {
			label := 0
			value := float64(index%10)/100 - 0.05
			if index >= 30 {
				label, value = 1, 3+float64(index%5)/10
			}
			rows = append(rows, trainingMatrixRow{RowKey: fmt.Sprintf("%s-%d", split, index), GPUUUID: fmt.Sprintf("GPU-%s-%d", split, index), ModelName: "H100", HorizonMinutes: 60, PredictionTarget: highPriorityXIDEventTarget, Split: split, LabelValue: label, TrainingWeight: 1, Features: map[string]float64{"gpu_temp_mean_24h": value, "gpu_power_mean_24h": value * 2}})
		}
	}
	matrixPath := filepath.Join(root, "matrix.jsonl")
	matrixSHA, err := writeJSONLines(matrixPath, rows)
	if err != nil {
		t.Fatal(err)
	}
	matrix := api.TrainingMatrixBuild{TrainingMatrixKey: "matrix-v7-cascade", Version: trainingMatrixVersion, Status: "completed", FeatureContractVersion: "1.10.0", MatrixPath: matrixPath, MatrixSHA256: matrixSHA, StartedAt: time.Now()}
	if err := db.Create(&matrix).Error; err != nil {
		t.Fatal(err)
	}
	build := api.BaselineModelBuild{BaselineModelKey: "cascade-test", Version: anomalyLogisticModelVersion, Status: "running", Algorithm: anomalyLogisticAlgorithm, SourceMatrixBuildID: matrix.ID, SourceTrainingMatrixKey: matrix.TrainingMatrixKey, FeatureContractVersion: matrix.FeatureContractVersion, OutputDir: filepath.Join(root, "model"), StartedAt: time.Now()}
	if err := db.Create(&build).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{DatasetDir: root}, time.Second)
	if err := service.buildBaselineModels(&build); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&build, build.ID).Error; err != nil {
		t.Fatal(err)
	}
	var artifact baselineArtifact
	if err := readJSONFile(build.ArtifactPath, &artifact); err != nil {
		t.Fatal(err)
	}
	report, err := service.BaselineModelReport(build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "completed" || build.TrainedModelCount != 1 || build.ShadowCandidateCount != 0 || len(artifact.Models) != 0 || len(artifact.CascadeModels) != 1 || report.Mode != "offline_challenger_evaluation_only" || report.Horizons[0].AnomalyFilter == nil {
		t.Fatalf("cascade runtime boundary or audit report missing: build=%+v artifact=%+v report=%+v", build, artifact, report)
	}
}

func TestBuildAnomalyAugmentedProducesOfflineOnlyImmutableArtifact(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:anomaly-augmented-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	rows := make([]trainingMatrixRow, 0, 120)
	for _, split := range []string{"train", "validation", "test"} {
		for index := 0; index < 40; index++ {
			label := 0
			value := float64(index%10)/100 - 0.05
			if index >= 30 {
				label, value = 1, 3+float64(index%5)/10
			}
			rows = append(rows, trainingMatrixRow{RowKey: fmt.Sprintf("%s-%d", split, index), GPUUUID: fmt.Sprintf("GPU-%s-%d", split, index), ModelName: "H100", HorizonMinutes: 1440, PredictionTarget: highPriorityXIDEventTarget, Split: split, LabelValue: label, TrainingWeight: 1, Features: map[string]float64{"gpu_temp_mean_24h": value, "gpu_power_mean_24h": value * 2}})
		}
	}
	matrixPath := filepath.Join(root, "matrix.jsonl")
	matrixSHA, err := writeJSONLines(matrixPath, rows)
	if err != nil {
		t.Fatal(err)
	}
	matrix := api.TrainingMatrixBuild{TrainingMatrixKey: "matrix-v7-augmented", Version: trainingMatrixVersion, Status: "completed", FeatureContractVersion: "1.10.0", MatrixPath: matrixPath, MatrixSHA256: matrixSHA, StartedAt: time.Now()}
	if err := db.Create(&matrix).Error; err != nil {
		t.Fatal(err)
	}
	build := api.BaselineModelBuild{BaselineModelKey: "augmented-test", Version: anomalyAugmentedModelVersion, Status: "running", Algorithm: anomalyAugmentedAlgorithm, SourceMatrixBuildID: matrix.ID, SourceTrainingMatrixKey: matrix.TrainingMatrixKey, FeatureContractVersion: matrix.FeatureContractVersion, OutputDir: filepath.Join(root, "model"), StartedAt: time.Now()}
	if err := db.Create(&build).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{DatasetDir: root}, time.Second)
	if err := service.buildBaselineModels(&build); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&build, build.ID).Error; err != nil {
		t.Fatal(err)
	}
	var artifact baselineArtifact
	if err := readJSONFile(build.ArtifactPath, &artifact); err != nil {
		t.Fatal(err)
	}
	report, err := service.BaselineModelReport(build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "completed" || build.TrainedModelCount != 1 || build.ShadowCandidateCount != 0 || len(artifact.Models) != 0 || len(artifact.AugmentedModels) != 1 || report.Mode != "offline_challenger_evaluation_only" || report.Horizons[0].AnomalyAugmentation == nil {
		t.Fatalf("augmented runtime boundary or audit report missing: build=%+v artifact=%+v report=%+v", build, artifact, report)
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

func TestBaselineFeatureAuditAllowsOnlyCorrectablePrecursorsForXIDTarget(t *testing.T) {
	rows := []trainingMatrixRow{{Features: map[string]float64{
		"correctable_remapped_rows_delta_24h":   1,
		"uncorrectable_remapped_rows_delta_24h": 1,
		"uncorrected_ecc_delta_24h":             1,
		"xid_current_last_24h":                  94,
		"gpu_reset_required_last_24h":           1,
	}}}
	xidAudit := auditBaselineFeaturesForTarget(rows, highPriorityXIDEventTarget)
	if len(xidAudit.SelectedColumns) != 1 || xidAudit.SelectedColumns[0] != "correctable_remapped_rows_delta_24h" || xidAudit.PredictionTarget != highPriorityXIDEventTarget {
		t.Fatalf("XID precursor policy mismatch: %+v", xidAudit)
	}
	hardwareAudit := auditBaselineFeaturesForTarget(rows, hardwareFailureTarget)
	if len(hardwareAudit.SelectedColumns) != 0 {
		t.Fatalf("hardware target admitted occurred-fault indicators: %+v", hardwareAudit)
	}
}

func TestBaselineFeatureSelectionUsesTrainingCoverageAndCapsDimensionality(t *testing.T) {
	columns := []string{
		"gpu_temp_mean_15m", "gpu_temp_max_15m", "gpu_temp_delta_15m",
		"gpu_temp_slope_per_hour_15m", "gpu_temp_mean_1h", "gpu_temp_max_1h",
		"gpu_power_mean_15m", "gpu_util_mean_15m", "gpu_clock_mean_15m",
		"gpu_memory_temp_mean_15m", "gpu_temp_mean_6h_sparse",
	}
	rows := make([]trainingMatrixRow, 0, 90)
	for index := 0; index < 90; index++ {
		label := 0
		shift := 0.0
		if index >= 60 {
			label, shift = 1, 10
		}
		features := map[string]float64{}
		for columnIndex, column := range columns[:10] {
			features[column] = shift + float64((index+columnIndex)%5)
		}
		if index < 10 {
			features[columns[10]] = float64(index)
		}
		rows = append(rows, trainingMatrixRow{LabelValue: label, Features: features})
	}
	selection := selectBaselineFeatures(rows, columns)
	if selection.Status != "passed" || selection.SelectionLimit != 6 || selection.SelectedFeatureCount != 6 {
		t.Fatalf("unexpected train-only selection: %+v", selection)
	}
	bySource := map[string]int{}
	selected := map[string]bool{}
	for _, feature := range selection.Selected {
		bySource[feature.SourceMetric]++
		selected[feature.Feature] = true
	}
	if bySource["gpu_temp"] > baselineMaximumFeaturesPerSource || selected[columns[10]] {
		t.Fatalf("selection ignored source or coverage caps: %+v", selection)
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

func TestBaselineCalibrationRequiresReliabilityAndPositiveSkill(t *testing.T) {
	perfect := []scoredLabel{{score: 0, label: 0}, {score: 0, label: 0}, {score: 1, label: 1}, {score: 1, label: 1}}
	passed := evaluateBaselineCalibration(perfect)
	if passed.Status != "passed" || passed.ECE != 0 || passed.BrierSkillScore != 1 {
		t.Fatalf("unexpected perfect calibration: %+v", passed)
	}
	wrong := []scoredLabel{{score: 0.9, label: 0}, {score: 0.8, label: 0}, {score: 0.2, label: 1}, {score: 0.1, label: 1}}
	failed := evaluateBaselineCalibration(wrong)
	if failed.Status != "calibration_required" || failed.BrierSkillScore >= 0 {
		t.Fatalf("unexpected failed calibration: %+v", failed)
	}
}

func TestBaselineReleaseReadinessRequiresStabilityAndCalibration(t *testing.T) {
	passing := baselineMetrics{Precision: 0.70, Recall: 0.50}
	if status := baselineReleaseReadiness("robust_candidate", "passed", passing); status != "shadow_candidate" {
		t.Fatalf("ready status=%s", status)
	}
	if status := baselineReleaseReadiness("robust_candidate", "calibration_required", passing); status != "blocked_calibration" {
		t.Fatalf("calibration block status=%s", status)
	}
	if status := baselineReleaseReadiness("inconclusive", "passed", passing); status != "blocked_stability" {
		t.Fatalf("stability block status=%s", status)
	}
	if status := baselineReleaseReadiness("robust_candidate", "passed", baselineMetrics{Precision: 0.69, Recall: 1}); status != "blocked_operating_point" {
		t.Fatalf("precision/recall block status=%s", status)
	}
}

func TestOperationalThresholdPrefersReleaseGateBeforeF1Fallback(t *testing.T) {
	scores := []scoredLabel{{score: 0.10, label: 0}, {score: 0.20, label: 0}, {score: 0.60, label: 1}, {score: 0.90, label: 1}}
	threshold := operationalThreshold(scores)
	metrics := evaluateScores(scores, threshold)
	if metrics.Precision < baselineMinimumPrecision || metrics.Recall < baselineMinimumRecall {
		t.Fatalf("threshold %.2f missed operational gate: %+v", threshold, metrics)
	}
}

func TestPlattCalibrationUsesValidationScoresAndPreservesRanking(t *testing.T) {
	scores := []scoredLabel{
		{score: 0.05, label: 0}, {score: 0.15, label: 0}, {score: 0.25, label: 0},
		{score: 0.75, label: 1}, {score: 0.85, label: 1}, {score: 0.95, label: 1},
	}
	calibration := fitPlattCalibration(scores)
	if calibration.Status != "fitted" || calibration.FittedCount != len(scores) || calibration.Slope <= 0 {
		t.Fatalf("unexpected Platt calibration: %+v", calibration)
	}
	calibrated := applyCalibration(scores, calibration)
	for index := 1; index < len(calibrated); index++ {
		if calibrated[index].score <= calibrated[index-1].score {
			t.Fatalf("calibration changed score ordering: %+v", calibrated)
		}
	}
}

func TestPlattCalibrationRejectsSingleClassValidation(t *testing.T) {
	calibration := fitPlattCalibration([]scoredLabel{{score: 0.1, label: 0}, {score: 0.2, label: 0}})
	if calibration.Status != "rejected" {
		t.Fatalf("single-class validation must be rejected: %+v", calibration)
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

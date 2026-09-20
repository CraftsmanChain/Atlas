package history

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestCompareBaselineModelsRequiresSameVerifiedMatrixAndProducesStableDigest(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:model-race-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	matrixPath := filepath.Join(root, "matrix.jsonl")
	matrixSHA, err := writeJSONLines(matrixPath, []trainingMatrixRow{{RowKey: "row-1", Split: "train", LabelValue: 0}})
	if err != nil {
		t.Fatal(err)
	}
	matrix := api.TrainingMatrixBuild{TrainingMatrixKey: "matrix-v7-race", Version: trainingMatrixVersion, Status: "completed", FeatureContractVersion: "1.10.0", MatrixPath: matrixPath, MatrixSHA256: matrixSHA, StartedAt: time.Now()}
	if err := db.Create(&matrix).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{DatasetDir: root}, time.Second)
	finished := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	create := func(key, version, algorithm, windowPolicy, planePolicy string, roc, pr float64) api.BaselineModelBuild {
		dir := filepath.Join(root, key)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		artifactPath := filepath.Join(dir, "models.json")
		reportPath := filepath.Join(dir, "evaluation_report.json")
		artifact := baselineArtifact{Version: version, Algorithm: algorithm, FeatureWindowPolicy: windowPolicy, FeaturePlanePolicy: planePolicy, MatrixKey: matrix.TrainingMatrixKey}
		if err := writeJSONAtomic(artifactPath, artifact); err != nil {
			t.Fatal(err)
		}
		artifactSHA, err := fileSHA256(artifactPath)
		if err != nil {
			t.Fatal(err)
		}
		report := baselineReport{Version: version, Algorithm: algorithm, FeatureWindowPolicy: windowPolicy, FeaturePlanePolicy: planePolicy, MatrixKey: matrix.TrainingMatrixKey, PredictionTarget: highPriorityXIDEventTarget, MacroTest: baselineMetrics{ROCAUC: roc, PRAUC: pr}, Horizons: []baselineHorizonReport{{HorizonMinutes: 60, FeatureSelection: baselineFeatureSelection{SelectedFeatureCount: 2, Selected: []baselineSelectedFeature{{Feature: "gpu_temp_mean_1h", SourceMetric: "gpu_temp"}, {Feature: "pcie_replay_counter_delta_3d", SourceMetric: "pcie_replay_counter"}}}, Test: baselineMetrics{ROCAUC: roc, PRAUC: pr}, ReleaseReadiness: "blocked_stability"}}}
		if err := writeJSONAtomic(reportPath, report); err != nil {
			t.Fatal(err)
		}
		build := api.BaselineModelBuild{BaselineModelKey: key, Version: version, Status: "completed", Algorithm: algorithm, FeatureWindowPolicy: windowPolicy, FeaturePlanePolicy: planePolicy, SourceMatrixBuildID: matrix.ID, SourceTrainingMatrixKey: matrix.TrainingMatrixKey, FeatureContractVersion: matrix.FeatureContractVersion, ArtifactPath: artifactPath, ArtifactSHA256: artifactSHA, ReportPath: reportPath, StartedAt: finished, FinishedAt: &finished}
		if err := db.Create(&build).Error; err != nil {
			t.Fatal(err)
		}
		return build
	}
	reference := create("reference", baselineModelVersion, logisticRegressionAlgorithm, maximum24HourWindowPolicy, allFeaturePlanesPolicy, 0.60, 0.40)
	challenger := create("challenger", baselineModelVersion, logisticRegressionAlgorithm, maximum3DayWindowPolicy, excludeStructuralPlanePolicy, 0.65, 0.45)
	first, err := service.CompareBaselineModels(reference.ID, []uint{challenger.ID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CompareBaselineModels(reference.ID, []uint{challenger.ID})
	if err != nil {
		t.Fatal(err)
	}
	if first.ComparisonSHA256 == "" || first.ComparisonSHA256 != second.ComparisonSHA256 || first.MatrixSHA256 != matrixSHA || len(first.Deltas) != 1 || first.Deltas[0].ROCAUC < 0.049 {
		t.Fatalf("unexpected comparison: %+v", first)
	}
	if first.Version != modelRaceComparisonVersion || len(first.Builds) != 2 || first.Builds[0].FeatureWindowPolicy != maximum24HourWindowPolicy || first.Builds[1].FeaturePlanePolicy != excludeStructuralPlanePolicy || first.Builds[0].SelectedSourceMetricCount != 2 || first.Builds[0].SelectedWindowCounts["3d"] != 1 || len(first.Horizons) != 1 || first.Horizons[0].SelectedFeatureCount[fmt.Sprint(reference.ID)] != 2 || len(first.Horizons[0].SelectedSourceMetrics[fmt.Sprint(reference.ID)]) != 2 || first.Horizons[0].SelectedWindowCounts[fmt.Sprint(reference.ID)]["1h"] != 1 {
		t.Fatalf("selected source metrics must be preserved in comparison: %+v", first)
	}
	if _, err := service.CompareBaselineModels(reference.ID, []uint{reference.ID}); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("duplicate ids must be rejected, got %v", err)
	}
}

func TestFormatFeatureWindowKeepsTwentyFourHoursExplicit(t *testing.T) {
	if got := formatFeatureWindow(24 * time.Hour); got != "24h" {
		t.Fatalf("24h window must not be mislabeled as %q", got)
	}
	if got := formatFeatureWindow(7 * 24 * time.Hour); got != "7d" {
		t.Fatalf("multi-day window label=%q", got)
	}
}

func TestSelectedWindowCountsSeparatesStructuralFeatures(t *testing.T) {
	counts := selectedWindowCounts([]baselineHorizonReport{{FeatureSelection: baselineFeatureSelection{Selected: []baselineSelectedFeature{
		{Feature: "gpu_metric_gap_max_seconds_1h"},
		{Feature: "gpu_temp_mean_24h"},
		{Feature: "legacy_exact_feature"},
	}}}})
	if counts["structural"] != 1 || counts["24h"] != 1 || counts["non_window"] != 1 {
		t.Fatalf("unexpected selected-window classification: %+v", counts)
	}
}

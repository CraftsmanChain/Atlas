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
	matrix := api.TrainingMatrixBuild{TrainingMatrixKey: "matrix-v7-race", Version: trainingMatrixVersion, Status: "completed", FeatureContractVersion: "1.9.0", MatrixPath: matrixPath, MatrixSHA256: matrixSHA, StartedAt: time.Now()}
	if err := db.Create(&matrix).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{DatasetDir: root}, time.Second)
	finished := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	create := func(key, version, algorithm string, roc, pr float64) api.BaselineModelBuild {
		dir := filepath.Join(root, key)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		artifactPath := filepath.Join(dir, "models.json")
		reportPath := filepath.Join(dir, "evaluation_report.json")
		artifact := baselineArtifact{Version: version, Algorithm: algorithm, MatrixKey: matrix.TrainingMatrixKey}
		if err := writeJSONAtomic(artifactPath, artifact); err != nil {
			t.Fatal(err)
		}
		artifactSHA, err := fileSHA256(artifactPath)
		if err != nil {
			t.Fatal(err)
		}
		report := baselineReport{Version: version, Algorithm: algorithm, MatrixKey: matrix.TrainingMatrixKey, PredictionTarget: highPriorityXIDEventTarget, MacroTest: baselineMetrics{ROCAUC: roc, PRAUC: pr}, Horizons: []baselineHorizonReport{{HorizonMinutes: 60, Test: baselineMetrics{ROCAUC: roc, PRAUC: pr}, ReleaseReadiness: "blocked_stability"}}}
		if err := writeJSONAtomic(reportPath, report); err != nil {
			t.Fatal(err)
		}
		build := api.BaselineModelBuild{BaselineModelKey: key, Version: version, Status: "completed", Algorithm: algorithm, SourceMatrixBuildID: matrix.ID, SourceTrainingMatrixKey: matrix.TrainingMatrixKey, FeatureContractVersion: matrix.FeatureContractVersion, ArtifactPath: artifactPath, ArtifactSHA256: artifactSHA, ReportPath: reportPath, StartedAt: finished, FinishedAt: &finished}
		if err := db.Create(&build).Error; err != nil {
			t.Fatal(err)
		}
		return build
	}
	reference := create("reference", baselineModelVersion, logisticRegressionAlgorithm, 0.60, 0.40)
	challenger := create("challenger", anomalyLogisticModelVersion, anomalyLogisticAlgorithm, 0.65, 0.45)
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
	if _, err := service.CompareBaselineModels(reference.ID, []uint{reference.ID}); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("duplicate ids must be rejected, got %v", err)
	}
}

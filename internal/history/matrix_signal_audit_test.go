package history

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestBuildTrainingMatrixSignalAuditExposesIndependentSignalBreadth(t *testing.T) {
	rows := []trainingMatrixRow{
		{
			PredictionTarget: highPriorityXIDEventTarget, Split: "train", HorizonMinutes: 60,
			MetricCoverage: 0.8, Features: map[string]float64{
				"gpu_temp_mean_15m": 55, "gpu_temp_max_24h": 70,
				"gpu_temp_sample_count_24h": 289, "correctable_remapped_rows_delta_24h": 1,
			},
		},
		{
			PredictionTarget: highPriorityXIDEventTarget, Split: "test", HorizonMinutes: 10080,
			MetricCoverage: 0.6, Features: map[string]float64{
				"gpu_temp_mean_15m": 45, "gpu_temp_max_24h": 60,
				"gpu_temp_sample_count_24h": 250, "correctable_remapped_rows_delta_24h": 0,
			},
		},
	}
	result, err := buildTrainingMatrixSignalAudit(api.TrainingMatrixBuild{
		ID: 7, TrainingMatrixKey: "matrix-7", MatrixSHA256: "sha",
	}, rows)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExpandedFeatureColumnCount != 4 || result.ObservedSourceMetricCount != 2 || result.SafeSourceMetricCount != 2 {
		t.Fatalf("derived columns must not be reported as independent signals: %+v", result)
	}
	if len(result.AuditSHA256) != 64 || result.TargetSafeFeatureColumnCount != 3 || result.ExcludedFeatureColumnCount != 1 {
		t.Fatalf("unexpected immutable target-safety summary: %+v", result)
	}
	if result.MaximumLookbackMinutes != 1440 || result.CoverageBySplit["train"].Average != 0.8 || result.CoverageBySplit["test"].Minimum != 0.6 {
		t.Fatalf("unexpected temporal or coverage audit: %+v", result)
	}
	codes := map[string]bool{}
	for _, finding := range result.Findings {
		codes[finding.Code] = true
	}
	if result.ConfiguredCoreMetricCount != 10 || result.ConfiguredOptionalMetricCount != 18 || len(result.MissingCoreMetrics) != 9 || len(result.MissingOptionalMetrics) != 17 {
		t.Fatalf("core and optional coverage must be audited separately: %+v", result)
	}
	for _, expected := range []string{"core_metrics_absent_from_matrix", "optional_metrics_absent_from_matrix", "structural_observability_plane_missing", "lookback_shorter_than_prediction_horizon"} {
		if !codes[expected] {
			t.Fatalf("missing finding %s: %+v", expected, result.Findings)
		}
	}
}

func TestTrainingMatrixSignalAuditVerifiesArtifactAndServesReadOnlyReport(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "training_matrix.jsonl")
	rows := []trainingMatrixRow{{
		PredictionTarget: highPriorityXIDEventTarget, Split: "train", HorizonMinutes: 60,
		MetricCoverage: 1, Features: map[string]float64{"gpu_temp_mean_1h": 50},
	}}
	checksum, err := writeJSONLines(path, rows)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.InitDB(fmt.Sprintf("file:matrix-signal-audit-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	build := api.TrainingMatrixBuild{
		TrainingMatrixKey: "matrix-signal-audit", Version: trainingMatrixVersion, Status: "completed",
		MatrixPath: path, MatrixSHA256: checksum,
	}
	if err := db.Create(&build).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(NewService(db, config.HistoryConfig{DatasetDir: root}, time.Second))
	response := httptest.NewRecorder()
	handler.HandleTrainingMatrix(response, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/prediction/history/training-matrices/%d/signal-audit", build.ID), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data trainingMatrixSignalAudit `json:"data"`
		Meta map[string]bool           `json:"meta"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.MatrixSHA256 != checksum || len(payload.Data.AuditSHA256) != 64 || !payload.Meta["read_only"] || payload.Meta["alerts_emitted"] {
		t.Fatalf("unexpected API payload: %+v", payload)
	}
	if response.Header().Get("ETag") != `"`+payload.Data.AuditSHA256+`"` || response.Header().Get("X-Atlas-Training-Signal-Audit-Version") != trainingMatrixSignalAuditVersion {
		t.Fatalf("missing immutable audit headers: %+v", response.Header())
	}
}

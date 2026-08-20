package prediction

import (
	"bytes"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestRiskRankingSnapshotFreezesLatestShadowRun(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC)
	service := NewService(db)
	service.now = func() time.Time { return now }
	empty, err := service.RiskRankingSnapshotReport()
	if err != nil {
		t.Fatal(err)
	}
	if empty.Status != "blocked_no_shadow_run" || empty.ReportSHA256 == "" || len(empty.BlockingReasons) == 0 {
		t.Fatalf("empty risk ranking must be explicitly blocked: %+v", empty)
	}

	threshold := 0.6
	spec := api.PredictionModelSpec{
		ModelKey: "gpu.failure.7d", Version: "model-v1", HardwareClass: "gpu", EntityType: "gpu",
		Task: "failure_probability", HorizonMinutes: 10080, Algorithm: "logistic_regression", Runtime: "go_native",
		Mode: "shadow", Status: "shadow_candidate", FeatureContractVersion: FeatureContractVersion,
		LabelContractVersion: LabelContractVersion, ScopeModelName: "H100", DecisionThreshold: &threshold,
	}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	finished := now.Add(-time.Hour)
	run := api.PredictionShadowScoringRun{
		RunKey: "shadow-run-1", Version: "gpu-shadow-scoring-v3", Status: "distribution_review_required",
		ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version, ScopeModelName: spec.ScopeModelName,
		TransformationVersion: "transform-v1", TargetGPUCount: 4, ScoredGPUCount: 4,
		NoAlertEmitted: true, NoActionExecuted: true, StartedAt: finished.Add(-time.Hour), FinishedAt: &finished,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	probabilities := []float64{0.2, 0.9, 0.7, 0.7}
	nodes := []string{"10.0.0.1", "10.0.0.1", "10.0.0.2", "10.0.0.3"}
	uuids := []string{"GPU-A", "GPU-B", "GPU-C", "GPU-D"}
	cutoff := now.Add(-2 * time.Hour)
	for index := range probabilities {
		prediction := api.HardwareRiskPrediction{
			ShadowRunID: run.ID, ModelSpecID: spec.ID, ModelVersion: spec.Version, HardwareClass: "gpu", EntityType: "gpu",
			EntityKey: uuids[index], GPUUUID: uuids[index], NodeIP: nodes[index], HorizonMinutes: spec.HorizonMinutes,
			Probability: &probabilities[index], RiskLevel: "unvalidated", Status: "shadow_scored", ObservedAt: cutoff, EvaluatedAt: cutoff,
		}
		if err := db.Create(&prediction).Error; err != nil {
			t.Fatal(err)
		}
	}

	report, err := service.RiskRankingSnapshotReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != RiskRankingSnapshotVersion || report.Status != "shadow_snapshot_available" || report.ReportSHA256 == "" || report.ShadowRunID != run.ID || report.NodeCount != 3 || report.ScoredGPUCount != 4 || report.HorizonMinutes != 10080 {
		t.Fatalf("unexpected risk ranking snapshot: %+v", report)
	}
	if report.ScoreSemantics != "relative_priority_not_absolute_failure_probability" || !report.Safety.ReadOnlyShadow || !report.Safety.NoAlertEmitted || !report.Safety.NoActionExecuted {
		t.Fatalf("risk ranking safety semantics are incomplete: %+v", report)
	}
	if report.SnapshotCutoffAt == nil || !report.SnapshotCutoffAt.Equal(cutoff) || len(report.Items) != 3 {
		t.Fatalf("risk ranking cutoff/items are incomplete: %+v", report)
	}
	first := report.Items[0]
	if first.Rank != 1 || first.NodeIP != "10.0.0.1" || first.TopGPUUUID != "GPU-B" || first.RiskScore != 0.9 || first.AboveThresholdGPUCount != 1 || math.Abs(first.MeanProbability-0.55) > 1e-12 || math.Abs(first.TopPercentile-1.0/3.0) > 1e-12 {
		t.Fatalf("unexpected top-ranked node: %+v", first)
	}
	if report.Items[1].NodeIP != "10.0.0.2" || report.Items[2].NodeIP != "10.0.0.3" {
		t.Fatalf("equal scores must use deterministic node ordering: %+v", report.Items)
	}

	service.now = func() time.Time { return now.Add(time.Hour) }
	later, err := service.RiskRankingSnapshotReport()
	if err != nil || later.ReportSHA256 != report.ReportSHA256 || later.GeneratedAt.Equal(report.GeneratedAt) {
		t.Fatalf("risk ranking checksum must ignore generated_at: before=%+v after=%+v err=%v", report, later, err)
	}
	response := httptest.NewRecorder()
	NewHandlerWithService(service).HandleRiskRankingSnapshot(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/risk-ranking-snapshot?download=1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(RiskRankingSnapshotVersion)) || response.Header().Get("Content-Disposition") == "" || response.Header().Get("ETag") == "" {
		t.Fatalf("risk ranking download failed: %d %s", response.Code, response.Body.String())
	}
}

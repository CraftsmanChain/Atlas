package prediction

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestDualTrackValidationAlignsRankingAndProbabilityCohort(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 20, 0, 0, 0, time.UTC)
	service := NewService(db)
	service.now = func() time.Time { return now }
	empty, err := service.DualTrackValidationReport()
	if err != nil {
		t.Fatal(err)
	}
	if empty.Status != "blocked" || empty.Alignment.Status != "blocked_no_ranking_snapshot" || empty.ReportSHA256 == "" {
		t.Fatalf("empty dual-track report must be blocked and archived: %+v", empty)
	}

	threshold := 0.5
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
		RunKey: "shadow-run-dual", Version: "gpu-shadow-scoring-v3", Status: "completed",
		ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version, ScopeModelName: spec.ScopeModelName,
		TargetGPUCount: 4, ScoredGPUCount: 4, NoAlertEmitted: true, NoActionExecuted: true,
		StartedAt: finished.Add(-time.Hour), FinishedAt: &finished,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	cutoff := now.Add(-8 * 24 * time.Hour)
	probabilities := []float64{0.9, 0.7, 0.4, 0.2}
	actuals := []int{1, 0, 0, 0}
	for index := range probabilities {
		node := "10.0.0." + string(rune('1'+index))
		uuid := "GPU-" + string(rune('A'+index))
		prediction := api.HardwareRiskPrediction{
			ShadowRunID: run.ID, ModelSpecID: spec.ID, ModelVersion: spec.Version, HardwareClass: "gpu", EntityType: "gpu",
			EntityKey: uuid, GPUUUID: uuid, NodeIP: node, HorizonMinutes: spec.HorizonMinutes,
			Probability: &probabilities[index], Status: "shadow_scored", ObservedAt: cutoff, EvaluatedAt: cutoff,
		}
		if err := db.Create(&prediction).Error; err != nil {
			t.Fatal(err)
		}
		outcome := "tn"
		if actuals[index] == 1 {
			outcome = "tp"
		}
		actual := actuals[index]
		evaluation := api.PredictionOutcomeEvaluation{
			PredictionID: prediction.ID, ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version,
			EntityType: "gpu", EntityKey: uuid, GPUUUID: uuid, NodeIP: node, HorizonMinutes: spec.HorizonMinutes,
			Probability: &probabilities[index], DecisionThreshold: &threshold, PredictedPositive: probabilities[index] >= threshold,
			PredictionEvaluatedAt: cutoff, WindowStartAt: cutoff, WindowEndAt: cutoff.Add(7 * 24 * time.Hour),
			MaturityStatus: "matured", RuleActualValue: &actual, RuleOutcome: outcome, RuleDecisionVersion: OutcomeRuleVersion,
			FinalActualValue: &actual, FinalOutcome: outcome, FinalSource: "rule",
		}
		if err := db.Create(&evaluation).Error; err != nil {
			t.Fatal(err)
		}
	}

	report, err := service.DualTrackValidationReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != DualTrackValidationReportVersion || report.Alignment.Status != "aligned" || report.Alignment.AlignedOutcomeRows != 4 || report.Alignment.SnapshotCutoffAt == nil || !report.Alignment.SnapshotCutoffAt.Equal(cutoff) {
		t.Fatalf("unexpected dual-track alignment: %+v", report)
	}
	if report.Status != "exploratory" || report.Ranking.Status != "exploratory" || report.Probability.Status != "exploratory" || report.Ranking.PositiveRows != 1 || report.Probability.Maturity.Matured != 4 || len(report.Ranking.Metrics) == 0 {
		t.Fatalf("tracks must remain independently exploratory on the aligned small cohort: %+v", report)
	}
	if report.Ranking.ScoreSemantics != "relative_priority_not_absolute_failure_probability" || report.ReportSHA256 == "" || report.Evidence.ReadinessSHA256 == "" {
		t.Fatalf("dual-track evidence binding is incomplete: %+v", report)
	}
	service.now = func() time.Time { return now.Add(time.Hour) }
	later, err := service.DualTrackValidationReport()
	if err != nil || later.ReportSHA256 != report.ReportSHA256 || later.GeneratedAt.Equal(report.GeneratedAt) {
		t.Fatalf("dual-track checksum must ignore generated_at: before=%+v after=%+v err=%v", report, later, err)
	}
	response := httptest.NewRecorder()
	NewHandlerWithService(service).HandleDualTrackValidation(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/dual-track-validation?download=1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(DualTrackValidationReportVersion)) || response.Header().Get("Content-Disposition") == "" || response.Header().Get("ETag") == "" {
		t.Fatalf("dual-track validation download failed: %d %s", response.Code, response.Body.String())
	}
}

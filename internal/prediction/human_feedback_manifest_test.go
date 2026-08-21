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

func TestHumanFeedbackManifestBindsOperatorFeedbackToPredictionWindows(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	threshold := 0.5
	spec := api.PredictionModelSpec{
		ModelKey: "gpu.failure.within_24h", Version: "0.2.0", HardwareClass: "gpu", EntityType: "gpu",
		Task: "failure_probability", HorizonMinutes: 1440, Algorithm: "logistic_regression", Runtime: "atlas",
		Mode: "shadow", Status: "shadow_candidate", FeatureContractVersion: FeatureContractVersion,
		LabelContractVersion: LabelContractVersion, DecisionThreshold: &threshold, Current: true,
	}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	probability := 0.7
	prediction := api.HardwareRiskPrediction{
		ModelSpecID: spec.ID, ModelVersion: spec.Version, HardwareClass: "gpu", EntityType: "gpu",
		EntityKey: "GPU-A", GPUAssetID: 7, GPUUUID: "GPU-A", NodeIP: "10.1.1.7", ModelName: "H100",
		HorizonMinutes: 1440, Probability: &probability, RiskLevel: "unvalidated", Status: "shadow_observation",
		ObservedAt: now.Add(-2 * time.Hour), EvaluatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(22 * time.Hour),
	}
	if err := db.Create(&prediction).Error; err != nil {
		t.Fatal(err)
	}
	actual := 1
	decidedAt := now.Add(time.Hour)
	outcome := api.PredictionOutcomeEvaluation{
		PredictionID: prediction.ID, ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version,
		EntityType: "gpu", EntityKey: "GPU-A", GPUAssetID: 7, GPUUUID: "GPU-A", NodeIP: "10.1.1.7",
		ModelName: "H100", HorizonMinutes: 1440, Probability: &probability, DecisionThreshold: &threshold,
		PredictedPositive: true, PredictionEvaluatedAt: prediction.EvaluatedAt, WindowStartAt: prediction.EvaluatedAt,
		WindowEndAt: prediction.ExpiresAt, MaturityStatus: "matured", RuleOutcome: "tp", RuleDecisionVersion: OutcomeRuleVersion,
		HumanActualValue: &actual, HumanOutcome: "tp", HumanDecision: "override", HumanReason: "operator confirmed board replacement",
		HumanDecidedBy: "ops", HumanDecidedAt: &decidedAt, FinalActualValue: &actual, FinalOutcome: "tp", FinalSource: "human_override",
	}
	if err := db.Create(&outcome).Error; err != nil {
		t.Fatal(err)
	}
	confirmedAt := now.Add(2 * time.Hour)
	label := api.FailureLabel{
		LabelKey: "human-confirmed-gpu-a", HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-A",
		GPUAssetID: 7, GPUUUID: "GPU-A", NodeIP: "10.1.1.7", ModelName: "H100", EventType: "xid_94",
		LabelValue: 1, QualityTier: "confirmed", SourceType: "human_resolution", SourceRecordID: 42,
		ConfirmationResolutionID: 42, LabelContractVersion: LabelContractVersion, OccurredAt: now,
		AvailableAt: now.Add(time.Hour), ConfirmedAt: &confirmedAt,
	}
	if err := db.Create(&label).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return now.Add(3 * time.Hour) }
	report, err := service.HumanFeedbackManifest()
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != HumanFeedbackManifestVersion || report.Status != "feedback_ready" || report.ManifestSHA256 == "" {
		t.Fatalf("unexpected feedback report identity: %+v", report)
	}
	if report.HumanConfirmedLabels != 1 || report.HumanOverrideOutcomes != 1 || report.MatchedPredictionWindows != 1 || report.MatchedPositivePredictionWindows != 1 || len(report.BlockingReasons) != 0 {
		t.Fatalf("feedback report did not bind label and outcome evidence: %+v", report)
	}
	later, err := service.HumanFeedbackManifest()
	if err != nil {
		t.Fatal(err)
	}
	if later.ManifestSHA256 != report.ManifestSHA256 {
		t.Fatalf("feedback manifest SHA must ignore generated_at: %s != %s", later.ManifestSHA256, report.ManifestSHA256)
	}
}

func TestHumanFeedbackManifestBlocksMissingFeedbackAndExposesAPI(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC) }
	report, err := service.HumanFeedbackManifest()
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "blocked" || len(report.BlockingReasons) == 0 || report.ManifestSHA256 == "" {
		t.Fatalf("empty feedback manifest should block: %+v", report)
	}
	handler := NewHandlerWithService(service)
	response := httptest.NewRecorder()
	handler.HandleHumanFeedbackManifest(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/human-feedback-manifest?download=1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(HumanFeedbackManifestVersion)) || response.Header().Get("Content-Disposition") == "" || response.Header().Get("ETag") == "" {
		t.Fatalf("feedback manifest API failed: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

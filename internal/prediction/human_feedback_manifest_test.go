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

func TestHumanFeedbackManifestIncludesHardwareFaultFeedback(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&api.MonitoringHistoryAudit{
		SourceKey: "current-prometheus", SourceName: "Current Prometheus", SourceType: "prometheus", BaseURL: "http://prometheus",
		Status: "success", EarliestSampleAt: &start, LatestSampleAt: &end, StartedAt: start, FinishedAt: end,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC) }
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.30", GPUUUID: "GPU-MANUAL-FAULT", GPUIndex: 4,
		FaultType: "gpu_hardware_failure", FaultOccurredAt: "2026-08-21T08:00:00Z",
		PreWindowHours: 4, PostWindowHours: 4, Operator: "ops-g", RepairAction: "reseat",
		TrainingEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.PrepareHardwareFaultFeedbackPack(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.HistoryPackStatus != "manifest_ready_pending_metric_extraction" {
		t.Fatalf("test feedback pack was not prepared: %+v", prepared)
	}
	reviewed, err := service.ReviewHardwareFaultFeedbackWarning(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.WarningReviewStatus != "manual_feedback_no_prior_shadow_warning" {
		t.Fatalf("test feedback should become warning-miss evidence: %+v", reviewed)
	}
	report, err := service.HumanFeedbackManifest()
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "exploratory_ready" || report.HardwareFaultFeedbackRequests != 1 || report.HardwareFeedbackTrainingEligible != 1 || report.HardwareFeedbackPackReady != 1 || report.HardwareFeedbackWarningReviewed != 1 || report.HardwareFeedbackWarningMisses != 1 || report.HardwareFeedbackBlocked != 0 {
		t.Fatalf("hardware feedback was not reflected in manifest: %+v", report)
	}
	if report.HumanConfirmedLabels != 1 || report.ConfirmedPositiveLabels != 1 || report.ConfirmedLabelsWithoutMatch != 1 {
		t.Fatalf("hardware feedback did not contribute confirmed positive label governance: %+v", report)
	}
	if len(report.SampleRecords) != 1 || report.SampleRecords[0].Source != "hardware_fault_feedback" || report.SampleRecords[0].Status != "eligible_warning_miss_feedback" {
		t.Fatalf("hardware feedback sample record missing: %+v", report.SampleRecords)
	}
}

func TestHumanFeedbackManifestBlocksUnpreparedHardwareFaultFeedback(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC) }
	if _, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.31", GPUUUID: "GPU-BLOCKED-FEEDBACK", GPUIndex: 5,
		FaultType: "pcie_link_failure", FaultOccurredAt: "2026-08-21T08:00:00Z",
		Operator: "ops-h", TrainingEligible: true,
	}); err != nil {
		t.Fatal(err)
	}
	report, err := service.HumanFeedbackManifest()
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "blocked" || report.HardwareFeedbackBlocked != 1 || report.HardwareFeedbackPackReady != 0 || report.HardwareFeedbackWarningReviewed != 0 {
		t.Fatalf("unprepared hardware feedback should block manifest: %+v", report)
	}
	if len(report.BlockingReasons) == 0 || len(report.SampleRecords) != 1 || report.SampleRecords[0].Status != "blocked_hardware_feedback" {
		t.Fatalf("hardware feedback blockers not exposed: %+v", report)
	}
}

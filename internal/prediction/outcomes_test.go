package prediction

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestOutcomeReconciliationAndHumanOverride(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	threshold := 0.5
	spec := api.PredictionModelSpec{
		ModelKey: "gpu.failure.test", Version: "1.0.0", HardwareClass: "gpu",
		EntityType: "gpu", Task: "failure_probability", HorizonMinutes: 60,
		Algorithm: "test", Runtime: "test", Mode: "shadow", Status: "released",
		FeatureContractVersion: FeatureContractVersion, LabelContractVersion: LabelContractVersion,
		DecisionThreshold: &threshold, Current: true,
	}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	evaluatedAt := now.Add(-26 * time.Hour)
	expiresAt := evaluatedAt.Add(time.Hour)
	probabilities := []float64{0.9, 0.8, 0.2, 0.1}
	uuids := []string{"GPU-TP", "GPU-FP", "GPU-FN", "GPU-TN"}
	for index := range probabilities {
		prediction := api.HardwareRiskPrediction{
			ModelSpecID: spec.ID, HardwareClass: "gpu", EntityType: "gpu", EntityKey: uuids[index],
			GPUUUID: uuids[index], HorizonMinutes: 60, Probability: &probabilities[index],
			RiskLevel: "test", Status: "scored", EvaluatedAt: evaluatedAt, ExpiresAt: expiresAt,
		}
		if err := db.Create(&prediction).Error; err != nil {
			t.Fatal(err)
		}
	}
	for index, uuid := range []string{"GPU-TP", "GPU-FN"} {
		label := api.FailureLabel{
			LabelKey: "test-label-" + uuid, HardwareClass: "gpu", EntityType: "gpu",
			EntityKey: uuid, GPUUUID: uuid, EventType: "xid_critical", LabelValue: 1,
			QualityTier: "strong_proxy", SourceType: "test", SourceRecordID: uint(index + 1),
			LabelContractVersion: LabelContractVersion, OccurredAt: evaluatedAt.Add(30 * time.Minute),
			AvailableAt: evaluatedAt.Add(31 * time.Minute),
		}
		if err := db.Create(&label).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	if err := service.SyncOutcomes(); err != nil {
		t.Fatal(err)
	}
	summary, err := service.Accuracy()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Rule.TP != 1 || summary.Rule.FP != 1 || summary.Rule.FN != 1 || summary.Rule.TN != 1 || summary.Rule.Evaluated != 4 {
		t.Fatalf("unexpected rule confusion matrix: %+v", summary.Rule)
	}
	if summary.Rule.Precision == nil || *summary.Rule.Precision != 0.5 || summary.Rule.Recall == nil || *summary.Rule.Recall != 0.5 {
		t.Fatalf("unexpected rule metrics: %+v", summary.Rule)
	}
	var falsePositive api.PredictionOutcomeEvaluation
	if err := db.Where("gpu_uuid = ?", "GPU-FP").First(&falsePositive).Error; err != nil {
		t.Fatal(err)
	}
	overridden, err := service.OverrideOutcome(falsePositive.ID, OutcomeOverride{
		ActualValue: 1, Reason: "operator confirmed a board replacement", DecidedBy: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.RuleOutcome != "fp" || overridden.FinalOutcome != "tp" || overridden.FinalSource != "human_override" {
		t.Fatalf("human override lost rule provenance: %+v", overridden)
	}
	if err := service.SyncOutcomes(); err != nil {
		t.Fatal(err)
	}
	summary, err = service.Accuracy()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Rule.FP != 1 || summary.Final.TP != 2 || summary.Final.FP != 0 || summary.HumanOverrides != 1 {
		t.Fatalf("unexpected post-override metrics: %+v", summary)
	}
}

func TestOutcomeCensoringAndHandlerValidation(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	spec := api.PredictionModelSpec{
		ModelKey: "gpu.failure.unreleased", Version: "1.0.0", HardwareClass: "gpu",
		EntityType: "gpu", Task: "failure_probability", HorizonMinutes: 60,
		Algorithm: "none", Runtime: "none", Mode: "shadow", Status: "data_readiness",
		FeatureContractVersion: FeatureContractVersion, LabelContractVersion: LabelContractVersion, Current: true,
	}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	prediction := api.HardwareRiskPrediction{
		ModelSpecID: spec.ID, HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-NOT-SCORED",
		GPUUUID: "GPU-NOT-SCORED", HorizonMinutes: 60, RiskLevel: "unavailable",
		Status: "not_scored", EvaluatedAt: time.Now().Add(-48 * time.Hour), ExpiresAt: time.Now().Add(-47 * time.Hour),
	}
	if err := db.Create(&prediction).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	if err := service.SyncOutcomes(); err != nil {
		t.Fatal(err)
	}
	summary, err := service.Accuracy()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Censored != 1 || summary.Rule.Evaluated != 0 || summary.Rule.Accuracy != nil {
		t.Fatalf("unscored prediction polluted accuracy: %+v", summary)
	}

	handler := NewHandlerWithService(service)
	body, _ := json.Marshal(OutcomeOverride{ActualValue: 1})
	response := httptest.NewRecorder()
	handler.HandleOutcome(response, httptest.NewRequest(http.MethodPatch, "/api/v1/prediction/outcomes/1", bytes.NewReader(body)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("override without reason must fail: %d %s", response.Code, response.Body.String())
	}
}

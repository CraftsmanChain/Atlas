package history

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"atlas/internal/prediction"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestBuildManualFeedbackFeatureRequestManifestFreezesPackReadyReviewedFeedback(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:manual-feedback-feature-request-%d?mode=memory&cache=shared", time.Now().UnixNano()))
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
	predictionService := prediction.NewService(db)
	row, err := predictionService.CreateHardwareFaultFeedback(prediction.HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.30", GPUUUID: "GPU-MANUAL-FAULT", GPUIndex: 3,
		FaultType: "gpu_hardware_failure", FaultOccurredAt: "2026-08-21T08:00:00Z",
		PreWindowHours: 4, PostWindowHours: 4, Operator: "ops-g", RepairAction: "reseat",
		TrainingEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := predictionService.PrepareHardwareFaultFeedbackPack(row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := predictionService.ReviewHardwareFaultFeedbackWarning(row.ID); err != nil {
		t.Fatal(err)
	}
	outputRoot := t.TempDir()
	service := NewService(db, config.HistoryConfig{DatasetDir: outputRoot}, time.Second)
	service.now = func() time.Time { return time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC) }
	build, err := service.BuildManualFeedbackFeatureRequestManifest(ManualFeedbackFeatureRequestBuildRequest{SourceKey: "current-prometheus"})
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "manifest_ready_pending_offline_worker" || build.WindowCount != 1 || build.BlockedRequests != 0 || build.ManifestSHA256 == "" || build.SourceManifestSHA256 == "" {
		t.Fatalf("unexpected feature request build: %+v", build)
	}
	if build.WarningMissRequests != 1 || !build.NoRawTelemetryStored || !build.NoAlertEmitted || !build.NoActionExecuted {
		t.Fatalf("feedback governance counters/safety flags missing: %+v", build)
	}
	body, err := os.ReadFile(build.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("GPU-MANUAL-FAULT")) || bytes.Contains(body, []byte("values")) {
		t.Fatalf("manifest should contain feedback metadata but no raw telemetry values: %s", string(body))
	}
	handler := NewHandler(service)
	response := httptest.NewRecorder()
	handler.HandleManualFeedbackFeatureRequests(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/history/feedback-feature-requests?limit=1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("manual-feedback-feature-request-v1")) {
		t.Fatalf("feedback feature request API failed: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBuildManualFeedbackFeatureRequestManifestBlocksUnpreparedFeedback(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:manual-feedback-feature-request-blocked-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	predictionService := prediction.NewService(db)
	if _, err := predictionService.CreateHardwareFaultFeedback(prediction.HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.31", GPUUUID: "GPU-BLOCKED-FEEDBACK", GPUIndex: 5,
		FaultType: "pcie_link_failure", FaultOccurredAt: "2026-08-21T08:00:00Z",
		Operator: "ops-h", TrainingEligible: true,
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{DatasetDir: t.TempDir()}, time.Second)
	service.now = func() time.Time { return time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC) }
	build, err := service.BuildManualFeedbackFeatureRequestManifest(ManualFeedbackFeatureRequestBuildRequest{SourceKey: "current-prometheus"})
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "blocked" || build.WindowCount != 0 || build.BlockedRequests != 1 || build.ManifestSHA256 == "" {
		t.Fatalf("unprepared feedback should create blocked manifest metadata: %+v", build)
	}
	var payload struct {
		WindowCount int `json:"window_count"`
		Records     []struct {
			GPUUUID string `json:"gpu_uuid"`
		} `json:"records"`
	}
	body, err := os.ReadFile(build.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WindowCount != 0 || len(payload.Records) != 0 || len(build.BlockingReasons) == 0 {
		t.Fatalf("blocked manifest should not expose training windows: payload=%+v build=%+v", payload, build)
	}
}

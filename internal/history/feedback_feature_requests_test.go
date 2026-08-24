package history

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

func TestManualFeedbackFeatureRequestWorkerAggregatesFeatures(t *testing.T) {
	cutoff := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		if query := r.URL.Query().Get("query"); !strings.Contains(query, "GPU-MANUAL-WORKER") || !strings.Contains(query, "DCGM_FI_DEV_GPU_UTIL") {
			t.Errorf("manual feedback worker query lost UUID or metric family: %s", query)
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"__name__":"DCGM_FI_DEV_GPU_UTIL","UUID":"GPU-MANUAL-WORKER"},"values":[[%d,"10"],[%d,"30"]]},
			{"metric":{"__name__":"DCGM_FI_DEV_XID_ERRORS","UUID":"GPU-MANUAL-WORKER"},"values":[[%d,"0"],[%d,"79"]]}
		]}}`, cutoff.Add(-time.Hour).Unix(), cutoff.Unix(), cutoff.Add(-time.Hour).Unix(), cutoff.Unix())
	}))
	defer server.Close()
	db, err := storage.InitDB(fmt.Sprintf("file:manual-feedback-feature-worker-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	auditStart := cutoff.Add(-72 * time.Hour)
	auditEnd := cutoff.Add(24 * time.Hour)
	if err := db.Create(&api.MonitoringHistoryAudit{
		SourceKey: "current-prometheus", SourceName: "Current Prometheus", SourceType: "prometheus", BaseURL: server.URL,
		Status: "success", EarliestSampleAt: &auditStart, LatestSampleAt: &auditEnd, StartedAt: auditStart, FinishedAt: auditEnd,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.HistoricalGPUIdentityInterval{
		IntervalKey: "identity-worker-gpu-3", SourceKey: "current-prometheus",
		NodeIP: "10.114.4.36", GPUIndex: 3, GPUUUID: "GPU-MANUAL-WORKER", ModelName: "NVIDIA H100",
		FirstSeenAt: cutoff.Add(-7 * 24 * time.Hour), LastSeenAt: cutoff.Add(24 * time.Hour),
		ObservationCount: 1000, TransitionType: "stable", EvidenceStrength: "strong",
	}).Error; err != nil {
		t.Fatal(err)
	}
	predictionService := prediction.NewService(db)
	row, err := predictionService.CreateHardwareFaultFeedback(prediction.HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.36", GPUUUID: "GPU-MANUAL-WORKER", GPUIndex: 3,
		FaultType: "gpu_hardware_failure", FaultOccurredAt: cutoff.Format(time.RFC3339),
		PreWindowHours: 72, PostWindowHours: 24, Operator: "admin", RepairAction: "replace_gpu_baseboard",
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
	service := NewService(db, config.HistoryConfig{
		DatasetDir: t.TempDir(), MaxConcurrency: 2,
		Sources: []config.HistorySourceConfig{{
			ID: "current-prometheus", Type: "prometheus", BaseURL: server.URL, Enabled: true,
		}},
	}, time.Second)
	build, err := service.BuildManualFeedbackFeatureRequestManifest(ManualFeedbackFeatureRequestBuildRequest{SourceKey: "current-prometheus"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(service)
	response := httptest.NewRecorder()
	body := strings.NewReader(fmt.Sprintf(`{"build_id":%d}`, build.ID))
	handler.HandleManualFeedbackFeatureRequestWorker(response, httptest.NewRequest(http.MethodPost, "/api/v1/prediction/history/feedback-feature-requests/run", body))
	if response.Code != http.StatusAccepted {
		t.Fatalf("worker API failed: status=%d body=%s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := db.First(&build, build.ID).Error; err != nil {
			t.Fatal(err)
		}
		if build.Status != "queued" && build.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if build.Status != "features_ready_pending_training_preparation" || build.CompletedWindows != 1 || build.FailedWindows != 0 || build.FeatureSHA256 == "" || build.QualityReportPath == "" {
		t.Fatalf("manual feedback worker did not produce feature artifacts: %+v", build)
	}
	featureFile, err := os.Open(build.FeaturePath)
	if err != nil {
		t.Fatal(err)
	}
	defer featureFile.Close()
	var feature extractedFeatureRow
	if err := json.NewDecoder(featureFile).Decode(&feature); err != nil {
		t.Fatal(err)
	}
	if feature.SampleKey != fmt.Sprintf("manual-feedback-%d", row.ID) || feature.Features["gpu_util_max_24h"] != 30 || feature.Features["xid_current_last_24h"] != 79 {
		t.Fatalf("unexpected manual feedback features: %+v", feature)
	}
	if !feature.FeatureCutoffAt.Equal(cutoff) || !feature.LabelOnsetAt.Equal(cutoff) {
		t.Fatalf("manual feedback provenance or cutoff missing: %+v", feature)
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

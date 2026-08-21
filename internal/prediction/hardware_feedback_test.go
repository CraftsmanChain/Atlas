package prediction

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestHardwareFaultFeedbackCreatesOfflineHistoryPackRequest(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	asset := api.GPUAsset{
		AssetKey:    "node-10.114.4.21-gpu-2",
		NodeIP:      "10.114.4.21",
		GPUIndex:    2,
		CurrentUUID: "GPU-HW-FAULT",
		ModelName:   "H100",
		State:       "active",
	}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	handler.service.now = func() time.Time { return time.Date(2026, 8, 21, 9, 30, 0, 0, time.UTC) }
	body := bytes.NewBufferString(`{
		"gpu_asset_id": 1,
		"node_ip": "10.114.4.21",
		"reported_gpu_uuid": "GPU-HW-FAULT",
		"gpu_index": 2,
		"fault_type": "gpu_hardware_failure",
		"fault_occurred_at": "2026-08-21T08:00:00Z",
		"pre_window_hours": 72,
		"post_window_hours": 24,
		"operator": "ops-a",
		"description": "GPU dropped from nvidia-smi and recovered after board replacement",
		"repair_action": "replace_gpu",
		"hardware_replaced": true,
		"evidence_note": "ticket HW-100",
		"training_eligible": true
	}`)
	response := httptest.NewRecorder()
	handler.HandleHardwareFaultFeedback(response, httptest.NewRequest(http.MethodPost, "/api/v1/prediction/hardware-fault-feedback", body))
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created struct {
		Data api.HardwareFaultFeedbackRequest `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Status != "history_pack_requested" || created.Data.HistoryPackStatus != "queued_offline_collection" {
		t.Fatalf("feedback did not become offline pack request: %+v", created.Data)
	}
	if created.Data.NodeIP != "10.114.4.21" || created.Data.GPUIndex != 2 || created.Data.GPUUUID != "" || created.Data.ReportedGPUUUID != "GPU-HW-FAULT" || created.Data.ModelName != "H100" {
		t.Fatalf("feedback lost failed GPU identity: %+v", created.Data)
	}
	if created.Data.IdentityResolutionStatus != "requires_historical_identity_at_fault_time" || !strings.Contains(created.Data.IdentityResolutionNote, "historical identity intervals") {
		t.Fatalf("replacement feedback must require historical identity resolution: %+v", created.Data)
	}
	if !strings.Contains(created.Data.HistoryPackScope, "start=2026-08-18T08:00:00Z") || !strings.Contains(created.Data.HistoryPackScope, "end=2026-08-22T08:00:00Z") {
		t.Fatalf("feedback scope did not bind pre/post windows: %s", created.Data.HistoryPackScope)
	}
	if !strings.Contains(created.Data.HistoryPackScope, "identity_resolution=requires_historical_identity_at_fault_time") || !strings.Contains(created.Data.HistoryPackScope, "reported_gpu_uuid=GPU-HW-FAULT") {
		t.Fatalf("replacement feedback scope must keep reported UUID separate from resolved fault identity: %s", created.Data.HistoryPackScope)
	}
	if len(created.Data.BlockingReasons) < 2 || !created.Data.TrainingEligible || !created.Data.HardwareReplaced {
		t.Fatalf("feedback lost governance fields: %+v", created.Data)
	}

	response = httptest.NewRecorder()
	handler.HandleHardwareFaultFeedback(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/hardware-fault-feedback", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	var listed struct {
		Data []api.HardwareFaultFeedbackRequest `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data) != 1 || listed.Data[0].RequestKey != created.Data.RequestKey {
		t.Fatalf("feedback request was not listed: %+v", listed.Data)
	}
}

func TestHardwareFaultFeedbackRejectsMissingOperator(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	response := httptest.NewRecorder()
	handler.HandleHardwareFaultFeedback(response, httptest.NewRequest(http.MethodPost, "/api/v1/prediction/hardware-fault-feedback", bytes.NewBufferString(`{
		"node_ip": "10.114.4.21",
		"gpu_index": 0,
		"fault_type": "gpu_hardware_failure",
		"fault_occurred_at": "2026-08-21T08:00:00Z"
	}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "operator is required") {
		t.Fatalf("missing operator should be rejected: %d %s", response.Code, response.Body.String())
	}
}

func TestHardwareFaultFeedbackUsesCurrentIdentityWhenNoReplacementReported(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	asset := api.GPUAsset{
		AssetKey:    "node-10.114.4.22-gpu-3",
		NodeIP:      "10.114.4.22",
		GPUIndex:    3,
		CurrentUUID: "GPU-STILL-IN-SLOT",
		ModelName:   "H100",
		State:       "active",
	}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		GPUAssetID:       asset.ID,
		NodeIP:           asset.NodeIP,
		GPUUUID:          asset.CurrentUUID,
		GPUIndex:         asset.GPUIndex,
		FaultType:        "pcie_link_failure",
		FaultOccurredAt:  "2026-08-21T08:00:00Z",
		Operator:         "ops-b",
		TrainingEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.IdentityResolutionStatus != "current_identity_selected" || row.GPUUUID != "GPU-STILL-IN-SLOT" || row.ReportedGPUUUID != "GPU-STILL-IN-SLOT" {
		t.Fatalf("non-replacement feedback should keep current identity selected: %+v", row)
	}
	if len(row.BlockingReasons) != 1 || strings.Contains(row.HistoryPackScope, "requires_historical_identity_at_fault_time") {
		t.Fatalf("non-replacement feedback should not require replacement identity blocker: %+v", row)
	}
}

func TestPrepareHardwareFaultFeedbackPackResolvesReplacementIdentityAndSourceCoverage(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	last := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&api.HistoricalGPUIdentityInterval{
		IntervalKey: "identity-10.114.4.23-gpu3-old",
		SourceKey:   "current-prometheus", NodeIP: "10.114.4.23", GPUIndex: 3, GPUUUID: "GPU-OLD-FAILED", ModelName: "H100",
		FirstSeenAt: first, LastSeenAt: last, ObservationCount: 120, EvidenceStrength: "strong",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.MonitoringHistoryAudit{
		SourceKey: "current-prometheus", SourceName: "Current Prometheus", SourceType: "prometheus", BaseURL: "http://prometheus",
		Status: "success", EarliestSampleAt: &first, LatestSampleAt: &last, StartedAt: first, FinishedAt: last,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.23", ReportedGPUUUID: "GPU-NEW-AFTER-REPLACE", GPUIndex: 3,
		FaultType: "pcie_link_failure", FaultOccurredAt: "2026-08-21T08:00:00Z",
		PreWindowHours: 8, PostWindowHours: 4, Operator: "ops-c", RepairAction: "replace_gpu", TrainingEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.PrepareHardwareFaultFeedbackPack(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "history_pack_manifest_ready" || prepared.HistoryPackStatus != "manifest_ready_pending_metric_extraction" || prepared.HistoryPackSHA256 == "" {
		t.Fatalf("pack manifest was not prepared: %+v", prepared)
	}
	if prepared.GPUUUID != "GPU-OLD-FAILED" || prepared.ReportedGPUUUID != "GPU-NEW-AFTER-REPLACE" || prepared.IdentityResolutionStatus != "historical_identity_resolved" {
		t.Fatalf("replacement identity was not resolved safely: %+v", prepared)
	}
	if len(prepared.BlockingReasons) != 0 || !strings.Contains(prepared.HistoryPackScope, "source_key=current-prometheus") {
		t.Fatalf("prepared pack kept blockers or lost source scope: %+v", prepared)
	}
	again, err := service.PrepareHardwareFaultFeedbackPack(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.HistoryPackSHA256 != prepared.HistoryPackSHA256 {
		t.Fatalf("pack checksum must be stable: %s != %s", again.HistoryPackSHA256, prepared.HistoryPackSHA256)
	}
}

func TestPrepareHardwareFaultFeedbackPackBlocksWhenFaultTimeIdentityMissing(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.24", ReportedGPUUUID: "GPU-NEW", GPUIndex: 3,
		FaultType: "pcie_link_failure", FaultOccurredAt: "2026-08-21T08:00:00Z",
		Operator: "ops-d", RepairAction: "replace_gpu", TrainingEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.PrepareHardwareFaultFeedbackPack(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "blocked" || prepared.HistoryPackStatus != "blocked_identity_unresolved" || prepared.HistoryPackSHA256 != "" {
		t.Fatalf("missing identity should block pack preparation: %+v", prepared)
	}
	if prepared.IdentityResolutionStatus != "blocked_no_fault_time_identity" || len(prepared.BlockingReasons) == 0 {
		t.Fatalf("missing identity blocker not recorded: %+v", prepared)
	}
}

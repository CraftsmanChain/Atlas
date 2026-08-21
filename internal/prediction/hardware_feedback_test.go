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
		"gpu_uuid": "GPU-HW-FAULT",
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
	if created.Data.NodeIP != "10.114.4.21" || created.Data.GPUIndex != 2 || created.Data.GPUUUID != "GPU-HW-FAULT" || created.Data.ModelName != "H100" {
		t.Fatalf("feedback lost failed GPU identity: %+v", created.Data)
	}
	if !strings.Contains(created.Data.HistoryPackScope, "start=2026-08-18T08:00:00Z") || !strings.Contains(created.Data.HistoryPackScope, "end=2026-08-22T08:00:00Z") {
		t.Fatalf("feedback scope did not bind pre/post windows: %s", created.Data.HistoryPackScope)
	}
	if len(created.Data.BlockingReasons) == 0 || !created.Data.TrainingEligible || !created.Data.HardwareReplaced {
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

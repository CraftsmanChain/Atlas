package inventory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestReconciliationMatchesBySNThenIPAndClassifiesTypes(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := []api.InfrastructureAsset{
		{AssetKey: "ops-gpu", Source: "ops_host", IPAddress: "10.0.0.1", Name: "gpu-01", Type: "server", Model: "H100", SerialNumber: "GPU-1", InUse: true, Present: true, EntityKind: "gpu_node", FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now},
		{AssetKey: "machine-gpu", Source: "asset_machine", IPAddress: "", Name: "H100gpu-01", Type: "H100", Model: "server-model", SerialNumber: "GPU-1", InUse: true, Present: true, EntityKind: "gpu_node", FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now},
		{AssetKey: "ops-only", Source: "ops_host", IPAddress: "10.0.0.2", Name: "H200gpu-02", Type: "server", Model: "H100", SerialNumber: "GPU-2", InUse: true, Present: true, EntityKind: "gpu_node", FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now},
		{AssetKey: "machine-network", Source: "asset_machine", Name: "GPU_Fabric_leaf", Type: "交换机", Model: "MQM9790", SerialNumber: "SW-1", InUse: true, Present: true, EntityKind: "network", FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now},
		{AssetKey: "machine-storage", Source: "asset_machine", Name: "storage-01", Type: "存储", Model: "storage", SerialNumber: "ST-1", InUse: true, Present: true, EntityKind: "unknown", FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	response := httptest.NewRecorder()
	handler.HandleReconciliation(response, httptest.NewRequest(http.MethodGet, "/api/v1/inventory/reconciliation?limit=100", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data    []reconciliationRow   `json:"data"`
		Summary reconciliationSummary `json:"summary"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Summary.Total != 4 || payload.Summary.ByScope[reconcileBoth] != 1 || payload.Summary.ByScope[reconcileOpsOnly] != 1 || payload.Summary.ByScope[reconcileMachineOnly] != 2 {
		t.Fatalf("unexpected summary: %+v", payload.Summary)
	}
	if len(payload.Summary.GPUModels) != 2 || payload.Summary.GPUModels[0].Count != 1 || payload.Summary.GPUModels[1].Count != 1 {
		t.Fatalf("unexpected GPU model counts: %+v", payload.Summary.GPUModels)
	}
	var matched, network, h200 bool
	for _, row := range payload.Data {
		if row.Scope == reconcileBoth && row.IPAddress == "10.0.0.1" && row.Category == "gpu" && row.GPUModel == "H100" {
			matched = true
		}
		if row.Type == "交换机" && row.Category == "network" {
			network = true
		}
		if row.Scope == reconcileOpsOnly && row.GPUModel == "H200" && row.Type == "H200" {
			h200 = true
		}
	}
	if !matched || !network || !h200 {
		t.Fatalf("expected SN match and network classification, rows=%+v", payload.Data)
	}
}

func TestReconciliationFiltersDifferenceScope(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := []api.InfrastructureAsset{
		{AssetKey: "ops-only", Source: "ops_host", IPAddress: "10.0.0.2", Type: "server", Model: "H200", Present: true, EntityKind: "gpu_node", FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now},
		{AssetKey: "machine-only", Source: "asset_machine", Type: "UFM", Name: "ufm-1", Present: true, FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewHandler(db).HandleReconciliation(response, httptest.NewRequest(http.MethodGet, "/api/v1/inventory/reconciliation?scope=ops_only&limit=10", nil))
	var payload struct {
		Data []reconciliationRow `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Meta.Total != 1 || len(payload.Data) != 1 || payload.Data[0].Scope != reconcileOpsOnly {
		t.Fatalf("unexpected filtered response: %+v", payload)
	}
}

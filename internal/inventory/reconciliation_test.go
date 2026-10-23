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
		{AssetKey: "stopped-gpu", Source: "asset_machine", IPAddress: "10.0.0.9", Name: "H100gpu-09", Type: "H100", State: "停机", SerialNumber: "GPU-9", InUse: false, Present: true, EntityKind: "gpu_node", FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now},
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
	if payload.Summary.Total != 4 || payload.Summary.ByScope[reconcileBoth] != 1 || payload.Summary.ByScope[reconcileOpsOnly] != 1 || payload.Summary.ByScope[reconcileMachineOnly] != 0 {
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
		{AssetKey: "ops-only", Source: "ops_host", IPAddress: "10.0.0.2", Type: "server", Name: "H200gpu-02", Model: "H200", InUse: true, Present: true, EntityKind: "gpu_node", FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now},
		{AssetKey: "machine-only", Source: "asset_machine", Type: "H100", Name: "H100gpu-3", InUse: true, Present: true, EntityKind: "gpu_node", FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now},
		{AssetKey: "ufm-only", Source: "asset_machine", Type: "UFM", Name: "ufm-1", InUse: true, Present: true, EntityKind: "unknown", FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now},
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

func TestGPUHostnameModelsIgnoreRackPositions(t *testing.T) {
	for _, tc := range []struct{ name, kind, model string }{
		{"204-A16-8U-H100-node016", "gpu", "H100"},
		{"204-A10-8U-H100-node010", "gpu", "H100"},
		{"301-A10-2U-CPU001", "cpu", ""},
		{"201-B07-4U-Gpfs01", "storage", ""},
		{"201-B07-4U-Obs01", "storage", ""},
		{"204-B16-8U-H100-node032", "gpu", "H100"},
		{"201-B07-12U-4090-node116", "gpu", "4090"},
		{"H200gpu-01", "gpu", "H200"},
		{"rack-RTX_4090-node", "gpu", "4090"},
		{"rack-L40S-node", "gpu", "L40S"},
		{"301-B13-19U-GC-CPU126(CPU-Master)", "cpu", ""},
		{"201-A16-Storage01", "storage", ""},
		{"201-B16-server01", "cpu", ""},
		{"server-H1000-node", "cpu", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			category, _, model := assetCategory(api.InfrastructureAsset{Name: tc.name, Type: "服务器", EntityKind: "gpu_node", Model: "H100"})
			if category != tc.kind || model != tc.model {
				t.Fatalf("got %s/%s, want %s/%s", category, model, tc.kind, tc.model)
			}
		})
	}
}

func TestReconciliationSourcePresenceBeforeLifecycle(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	rows := []api.InfrastructureAsset{
		{AssetKey: "ops-unknown", Source: "ops_host", DataCenterID: "dc1", IPAddress: "10.0.0.1", SerialNumber: "old-sn", Name: "h100-001", Present: true},
		{AssetKey: "machine-live", Source: "asset_machine", DataCenterID: "dc1", IPAddress: "10.0.0.1", SerialNumber: "new-sn", Name: "204-B07-8U-H100-node001", Present: true, InUse: true},
		{AssetKey: "ops-live", Source: "ops_host", DataCenterID: "dc1", IPAddress: "10.0.0.2", Name: "h100-002", Present: true, InUse: true},
		{AssetKey: "machine-stopped", Source: "asset_machine", DataCenterID: "dc1", IPAddress: "10.0.0.2", Name: "H100-002", State: "停机", Present: true},
		{AssetKey: "ops-removed", Source: "ops_host", DataCenterID: "dc1", IPAddress: "10.0.0.3", Present: false, InUse: true},
		{AssetKey: "machine-only", Source: "asset_machine", DataCenterID: "dc1", IPAddress: "10.0.0.3", Name: "H100-003", Present: true, InUse: true},
		{AssetKey: "ops-other-dc", Source: "ops_host", DataCenterID: "dc2", IPAddress: "10.0.0.3", Present: true},
		{AssetKey: "unknown-only", Source: "ops_host", Name: "H100-004", Present: true},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewHandler(db).HandleReconciliation(response, httptest.NewRequest(http.MethodGet, "/api/v1/inventory/reconciliation?limit=100", nil))
	var payload struct {
		Data    []reconciliationRow
		Summary reconciliationSummary
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(payload.Data) != 2 || payload.Summary.ByScope[reconcileBoth] != 1 || payload.Summary.ByScope[reconcileMachineOnly] != 1 {
		t.Fatalf("unexpected reconciliation: %+v", payload)
	}
	for _, row := range payload.Data {
		if row.IPAddress == "10.0.0.1" && (row.OpsHost == nil || row.OpsHost.InUse || row.OpsHost.State != "" || row.GPUModel != "H100") {
			t.Fatalf("lost source state: %+v", row)
		}
	}
}

func TestDuplicateEndpointRecordsDoNotBecomeMissingNodes(t *testing.T) {
	records := []api.InfrastructureAsset{
		{AssetKey: "ops", Source: "ops_host", DataCenterID: "dc", IPAddress: "10.0.0.1", SerialNumber: "NEW", Present: true, InUse: true},
		{AssetKey: "machine-old", Source: "asset_machine", DataCenterID: "dc", IPAddress: "10.0.0.1", SerialNumber: "OLD", Name: "4090-node1", Present: true, InUse: true},
		{AssetKey: "machine-new", Source: "asset_machine", DataCenterID: "dc", IPAddress: "10.0.0.1", SerialNumber: "NEW", Name: "4090-node1", Present: true, InUse: true},
		{AssetKey: "ops-duplicate", Source: "ops_host", DataCenterID: "dc", IPAddress: "10.0.0.1", SerialNumber: "NEW", Present: true, InUse: true},
	}
	var key string
	for i := 0; i < len(records); i++ {
		records = append(records[1:], records[0])
		rows := reconcileAssets(records)
		if len(rows) != 1 || rows[0].Scope != reconcileBoth || !rows[0].SourceConflict || len(rows[0].MachineRecords) != 2 || len(rows[0].OpsRecords) != 2 || rows[0].SerialNumber != "NEW" {
			t.Fatalf("duplicate endpoint misclassified: %+v", rows)
		}
		if i > 0 && key != rows[0].Key {
			t.Fatal("input order changes identity")
		}
		key = rows[0].Key
	}
	// Removing a duplicate clears the conflict; removing the ops source still
	// produces a genuine asset-only node. No sticky presence or debounce cache.
	rows := reconcileAssets(records[:0])
	if len(rows) != 0 {
		t.Fatal(rows)
	}
	machine := api.InfrastructureAsset{AssetKey: "solo", Source: "asset_machine", IPAddress: "10.0.0.1", Name: "4090-node1", Present: true, InUse: true}
	rows = reconcileAssets([]api.InfrastructureAsset{machine})
	if len(rows) != 1 || rows[0].Scope != reconcileMachineOnly || rows[0].SourceConflict {
		t.Fatal(rows)
	}
}

func TestEndpointGroupingPreservesDataCentersAndMissingIPIdentities(t *testing.T) {
	var records []api.InfrastructureAsset
	for _, key := range []string{"a", "b"} {
		records = append(records, api.InfrastructureAsset{AssetKey: key, Source: "asset_machine", DataCenterID: key, IPAddress: "10.0.0.1", Name: "H100-node", Present: true, InUse: true}, api.InfrastructureAsset{AssetKey: "noip" + key, Source: "asset_machine", Name: "H100-node", Present: true, InUse: true})
	}
	if rows := reconcileAssets(records); len(rows) != 4 {
		t.Fatalf("unrelated endpoints collapsed: %+v", rows)
	}
}

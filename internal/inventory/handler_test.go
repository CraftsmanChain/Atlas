package inventory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestNodesDefaultToCurrentInventoryAndAllowExplicitRetiredAudit(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	nodes := []api.GPUNode{
		{NodeIP: "10.114.4.21", State: "up", Lifecycle: "active"},
		{NodeIP: "10.111.4.23", State: "offline", Lifecycle: "retired"},
	}
	if err := db.Create(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	fetch := func(path string) struct {
		Data []api.GPUNode `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	} {
		t.Helper()
		response := httptest.NewRecorder()
		handler.HandleNodes(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d: %s", path, response.Code, response.Body.String())
		}
		var payload struct {
			Data []api.GPUNode `json:"data"`
			Meta struct {
				Total int64 `json:"total"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	current := fetch("/api/v1/nodes?limit=20")
	if current.Meta.Total != 1 || len(current.Data) != 1 || current.Data[0].NodeIP != "10.114.4.21" {
		t.Fatalf("default node list exposed retired inventory: %+v", current)
	}
	retired := fetch("/api/v1/nodes?lifecycle=retired&limit=20")
	if retired.Meta.Total != 1 || len(retired.Data) != 1 || retired.Data[0].NodeIP != "10.111.4.23" {
		t.Fatalf("explicit retired audit is unavailable: %+v", retired)
	}
}

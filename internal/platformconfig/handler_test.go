package platformconfig

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func newTestHandler(t *testing.T) (*Handler, *storage.DB) {
	t.Helper()
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(db, config.BrandingConfig{
		InstanceName: "智元集群", ProductName: "ATLAS",
		ProductTagline: "GPU RELIABILITY", Environment: "TEST / 8077",
	}), db
}

func TestGetSeedsConfiguredDefaults(t *testing.T) {
	handler, db := newTestHandler(t)
	response := httptest.NewRecorder()
	handler.Handle(response, httptest.NewRequest(http.MethodGet, "/api/v1/platform-config", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data api.PlatformDisplayConfig `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.InstanceName != "智元集群" || payload.Data.ProductName != "ATLAS" || payload.Data.Environment != "TEST / 8077" {
		t.Fatalf("unexpected defaults: %+v", payload.Data)
	}
	var count int64
	if err := db.Model(&api.PlatformDisplayConfig{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected one persisted singleton, count=%d err=%v", count, err)
	}
}

func TestPutUpdatesAndPersistsPlatformConfig(t *testing.T) {
	handler, _ := newTestHandler(t)
	body := bytes.NewBufferString(`{"instance_name":" 算力一集群 ","product_name":"ATLAS","product_tagline":"GPU RELIABILITY","environment":"PRODUCTION"}`)
	response := httptest.NewRecorder()
	handler.Handle(response, httptest.NewRequest(http.MethodPut, "/api/v1/platform-config", body))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"instance_name":"算力一集群"`)) {
		t.Fatalf("unexpected update: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.Handle(response, httptest.NewRequest(http.MethodGet, "/api/v1/platform-config", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"environment":"PRODUCTION"`)) {
		t.Fatalf("updated config was not persisted: %d %s", response.Code, response.Body.String())
	}
}

func TestPutRejectsInvalidPlatformConfig(t *testing.T) {
	handler, _ := newTestHandler(t)
	tests := []string{
		`{"instance_name":"","product_name":"ATLAS","product_tagline":"GPU RELIABILITY","environment":"TEST"}`,
		`{"instance_name":"cluster","product_name":"ATLAS","product_tagline":"GPU RELIABILITY","environment":"TEST","secret":"no"}`,
	}
	for _, body := range tests {
		response := httptest.NewRecorder()
		handler.Handle(response, httptest.NewRequest(http.MethodPut, "/api/v1/platform-config", bytes.NewBufferString(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d: %s", body, response.Code, response.Body.String())
		}
	}
}

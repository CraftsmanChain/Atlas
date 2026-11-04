package platformconfig

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

func newTestMonitoringSource(t *testing.T, fallbackURL string) (*MonitoringSource, *storage.DB) {
	t.Helper()
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	return NewMonitoringSource(db, fallbackURL, "15s"), db
}

func TestMonitoringSeedsFromFallbackOnce(t *testing.T) {
	source, db := newTestMonitoringSource(t, "http://10.80.192.233:28481/select/0/prometheus")
	row, err := source.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if row.BaseURL != "http://10.80.192.233:28481/select/0/prometheus" || row.RequestTimeout != "15s" {
		t.Fatalf("unexpected seed: %+v", row)
	}
	var count int64
	if err := db.Model(&api.MonitoringConfig{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected one persisted singleton, count=%d err=%v", count, err)
	}
}

func TestMonitoringSavePersistsAndApplies(t *testing.T) {
	source, db := newTestMonitoringSource(t, "http://10.80.192.233:28481/select/0/prometheus")
	var appliedBase string
	var appliedTimeout time.Duration
	source.SetApplier(func(baseURL string, timeout time.Duration) error {
		appliedBase, appliedTimeout = baseURL, timeout
		return nil
	})
	handler := NewMonitoringConfigHandler(source, "secret", true)

	body := bytes.NewBufferString(`{"base_url":" http://10.80.192.240:28481/select/0/prometheus ","request_timeout":"20s"}`)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/platform-config/monitoring", body)
	request.Header.Set("X-Atlas-Admin-Token", "secret")
	response := httptest.NewRecorder()
	handler.Handle(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if appliedBase != "http://10.80.192.240:28481/select/0/prometheus" || appliedTimeout != 20*time.Second {
		t.Fatalf("save did not apply to the live client: %q %s", appliedBase, appliedTimeout)
	}

	var stored api.MonitoringConfig
	if err := db.First(&stored, singletonID).Error; err != nil || stored.BaseURL != appliedBase {
		t.Fatalf("address was not persisted: %+v err=%v", stored, err)
	}
}

func TestMonitoringRejectsInvalidInput(t *testing.T) {
	source, _ := newTestMonitoringSource(t, "http://10.80.192.233:28481/select/0/prometheus")
	handler := NewMonitoringConfigHandler(source, "secret", true)
	tests := []struct {
		name string
		body string
	}{
		{"relative url", `{"base_url":"10.80.192.233:28481","request_timeout":"15s"}`},
		{"blank url", `{"base_url":"   ","request_timeout":"15s"}`},
		{"bad timeout", `{"base_url":"http://10.80.192.233:28481","request_timeout":"soon"}`},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodPut, "/api/v1/platform-config/monitoring", bytes.NewBufferString(test.body))
		request.Header.Set("X-Atlas-Admin-Token", "secret")
		response := httptest.NewRecorder()
		handler.Handle(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", test.name, response.Code, response.Body.String())
		}
	}
}

func TestMonitoringWritesRequireManagementToken(t *testing.T) {
	source, _ := newTestMonitoringSource(t, "http://10.80.192.233:28481/select/0/prometheus")
	handler := NewMonitoringConfigHandler(source, "secret", true)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/platform-config/monitoring", bytes.NewBufferString(`{"base_url":"http://10.80.192.233:28481","request_timeout":"15s"}`))
	response := httptest.NewRecorder()
	handler.Handle(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d: %s", response.Code, response.Body.String())
	}

	secure := NewMonitoringConfigHandler(source, "secret", false)
	insecureRequest := httptest.NewRequest(http.MethodPut, "/api/v1/platform-config/monitoring", bytes.NewBufferString(`{}`))
	insecureRequest.Header.Set("X-Atlas-Admin-Token", "secret")
	insecureResponse := httptest.NewRecorder()
	secure.Handle(insecureResponse, insecureRequest)
	if insecureResponse.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected 412 without TLS, got %d: %s", insecureResponse.Code, insecureResponse.Body.String())
	}
}

// TestMonitoringProbeFallsBackToUp covers the VictoriaMetrics cluster-select
// contract: /api/v1/targets is unsupported, so the probe must succeed through
// the up{} instant-query fallback.
func TestMonitoringProbeFallsBackToUp(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/targets":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("unsupported path requested"))
		case "/api/v1/query":
			if r.URL.Query().Get("query") != "up" {
				t.Fatalf("unexpected query %q", r.URL.Query().Get("query"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"node","instance":"10.0.0.1:9100"},"value":[1712000000,"1"]},{"metric":{"job":"node","instance":"10.0.0.2:9100"},"value":[1712000000,"0"]}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	source, _ := newTestMonitoringSource(t, upstream.URL)
	handler := NewMonitoringConfigHandler(source, "secret", true)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/platform-config/monitoring/test", bytes.NewBufferString(`{"base_url":"`+upstream.URL+`"}`))
	request.Header.Set("X-Atlas-Admin-Token", "secret")
	response := httptest.NewRecorder()
	handler.HandleTest(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected probe status %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			OK      bool `json:"ok"`
			Targets int  `json:"targets"`
			Up      int  `json:"up"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Data.OK || payload.Data.Targets != 2 || payload.Data.Up != 1 {
		t.Fatalf("unexpected probe result: %+v", payload.Data)
	}
	if !strings.Contains(response.Body.String(), upstream.URL) {
		t.Fatalf("probe did not echo the probed address: %s", response.Body.String())
	}
}

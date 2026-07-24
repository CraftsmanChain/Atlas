package evidence

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestEvidenceHandlerRoutesAreReadOnlyAndStable(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	event := api.GPUFaultEvent{
		EpisodeKey: "event", Source: "health_rule", State: "open", RuleCode: "gpu_unavailable",
		Domain: "availability", Severity: "critical", Evidence: "GPU is unavailable",
		FirstObservedAt: time.Now(), LastObservedAt: time.Now(),
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(NewService(db))
	tests := []struct {
		method, path string
		status       int
		contains     string
	}{
		{http.MethodGet, "/api/v1/fault-events/1/evidence", http.StatusOK, EvidenceSchemaVersion},
		{http.MethodGet, "/api/v1/fault-events/1/report", http.StatusOK, `"no_action_executed":true`},
		{http.MethodGet, "/api/v1/fault-events/999/report", http.StatusNotFound, "fault event not found"},
		{http.MethodPost, "/api/v1/fault-events/1/report", http.StatusMethodNotAllowed, "method not allowed"},
		{http.MethodGet, "/api/v1/fault-events/1/execute", http.StatusNotFound, "resource not found"},
		{http.MethodGet, "/api/v1/fault-events/not-a-number/report", http.StatusBadRequest, "invalid fault event id"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.HandleEventSubresource(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != test.status || !bytes.Contains(response.Body.Bytes(), []byte(test.contains)) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("evidence response must not be publicly cached")
			}
		})
	}
}

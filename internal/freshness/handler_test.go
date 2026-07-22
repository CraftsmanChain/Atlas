package freshness

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestClassifyFreshnessBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	boundary := now.Add(-15 * time.Minute)
	stale := boundary.Add(-time.Nanosecond)
	future := now.Add(time.Minute)
	tests := []struct {
		name string
		at   *time.Time
		want string
		age  int64
	}{
		{"empty", nil, "empty", 0},
		{"exact boundary is fresh", &boundary, "fresh", 900},
		{"past boundary is stale", &stale, "stale", 900},
		{"future clock skew is clamped", &future, "fresh", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(now, tc.at, 15*time.Minute)
			if got.Status != tc.want {
				t.Fatalf("status=%s want=%s", got.Status, tc.want)
			}
			if tc.at != nil && (got.AgeSeconds == nil || *got.AgeSeconds != tc.age) {
				t.Fatalf("age=%v want=%d", got.AgeSeconds, tc.age)
			}
		})
	}
}

func TestHandlerReturnsStableSourceMetadata(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&api.AlertIngestionRecord{EventID: "e1", CreatedAt: now.Add(-5 * time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	finished := now.Add(-21 * time.Minute)
	if err := db.Create(&api.InventorySyncRun{Status: "success", StartedAt: finished.Add(-time.Second), FinishedAt: &finished}).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db, db, "upstream-readonly", 15*time.Minute, 20*time.Minute, time.Hour)
	handler.now = func() time.Time { return now }
	response := httptest.NewRecorder()
	handler.Handle(response, httptest.NewRequest("GET", "/api/v1/data-freshness", nil))
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload Response
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.OverallStatus != "stale" || payload.Sources["ingestion"].Status != "fresh" || payload.Sources["ingestion"].SourceMode != "upstream-readonly" || payload.Sources["inventory"].Status != "stale" || payload.Sources["health"].Status != "empty" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

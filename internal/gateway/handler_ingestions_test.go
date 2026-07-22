package gateway

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestHandleRecentIngestionsReturnsSnapshotMetadata(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 14, 0, 0, 0, time.UTC)
	record := api.AlertIngestionRecord{EventID: "event-1", Source: "alertmanager", Host: "gpu-node-1", Level: "warning", Message: "XID 43", ProcessStatus: "success", CreatedAt: now.Add(-24 * time.Hour)}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db, db, nil, "", "", "snapshot", 15*time.Minute)
	handler.now = func() time.Time { return now }

	request := httptest.NewRequest("GET", "/api/v1/alerts/ingestions?limit=1000", nil)
	response := httptest.NewRecorder()
	handler.HandleRecentIngestions(response, request)
	if response.Code != 200 {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Total          int64  `json:"total"`
		AllTotal       int64  `json:"all_total"`
		Limit          int    `json:"limit"`
		SourceMode     string `json:"source_mode"`
		StreamStatus   string `json:"stream_status"`
		Received5m     int64  `json:"received_5m"`
		LatestReceived string `json:"latest_received_at"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 1 || payload.AllTotal != 1 || payload.Limit != 200 || payload.SourceMode != "snapshot" || payload.StreamStatus != "snapshot" || payload.Received5m != 0 || payload.LatestReceived == "" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestHandleRecentIngestionsRejectsInvalidPagination(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db, db, nil, "", "", "local-live", time.Minute)
	for _, path := range []string{
		"/api/v1/alerts/ingestions?limit=zero",
		"/api/v1/alerts/ingestions?limit=0",
		"/api/v1/alerts/ingestions?before_id=-1",
		"/api/v1/alerts/ingestions?before_id=abc",
	} {
		response := httptest.NewRecorder()
		handler.HandleRecentIngestions(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 400 {
			t.Fatalf("%s: expected 400, got %d", path, response.Code)
		}
	}
}

func TestHandleRecentIngestionsCursorIsStableWhenNewRowsArrive(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	// Deliberately use the same timestamp: the primary-key tiebreaker must make
	// traversal deterministic even when a batch is inserted at once.
	for i := 1; i <= 7; i++ {
		record := api.AlertIngestionRecord{EventID: fmt.Sprintf("event-%d", i), Source: "alertmanager", Level: "warning", Message: fmt.Sprintf("row-%d", i), CreatedAt: now}
		if err := db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	handler := NewHandler(db, db, nil, "", "", "local-live", time.Minute)
	handler.now = func() time.Time { return now }

	type pagePayload struct {
		Items        []api.AlertIngestionRecord `json:"items"`
		Total        int64                      `json:"total"`
		HasMore      bool                       `json:"has_more"`
		NextBeforeID uint                       `json:"next_before_id"`
	}
	fetch := func(path string) pagePayload {
		t.Helper()
		response := httptest.NewRecorder()
		handler.HandleRecentIngestions(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 {
			t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
		}
		var payload pagePayload
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	first := fetch("/api/v1/alerts/ingestions?limit=3")
	if len(first.Items) != 3 || first.Items[0].ID != 7 || first.Items[2].ID != 5 || !first.HasMore || first.NextBeforeID != 5 {
		t.Fatalf("unexpected first page: %+v", first)
	}
	newRecord := api.AlertIngestionRecord{EventID: "event-new", Source: "alertmanager", Level: "critical", Message: "arrived during traversal", CreatedAt: now.Add(time.Second)}
	if err := db.Create(&newRecord).Error; err != nil {
		t.Fatal(err)
	}

	second := fetch(fmt.Sprintf("/api/v1/alerts/ingestions?limit=3&before_id=%d", first.NextBeforeID))
	third := fetch(fmt.Sprintf("/api/v1/alerts/ingestions?limit=3&before_id=%d", second.NextBeforeID))
	got := []uint{first.Items[0].ID, first.Items[1].ID, first.Items[2].ID}
	for _, item := range append(second.Items, third.Items...) {
		got = append(got, item.ID)
	}
	want := []uint{7, 6, 5, 4, 3, 2, 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("cursor traversal duplicated or omitted rows: got %v want %v", got, want)
	}
	if second.Total != 8 {
		t.Fatalf("total should reflect the current filtered dataset, got %d", second.Total)
	}
}

func TestHandleRecentIngestionsMarksUpstreamReadOnlyAsLive(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	if err := db.Create(&api.AlertIngestionRecord{EventID: "event-1", Source: "alertmanager", Message: "quiet stream", ProcessStatus: "success", CreatedAt: now.Add(-4 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db, db, nil, "", "", "upstream-readonly", 15*time.Minute)
	handler.now = func() time.Time { return now }
	request := httptest.NewRequest("GET", "/api/v1/alerts/ingestions", nil)
	response := httptest.NewRecorder()
	handler.HandleRecentIngestions(response, request)
	var payload struct {
		SourceMode   string `json:"source_mode"`
		StreamStatus string `json:"stream_status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.SourceMode != "upstream-readonly" || payload.StreamStatus != "live" {
		t.Fatalf("unexpected upstream metadata: %+v", payload)
	}
}

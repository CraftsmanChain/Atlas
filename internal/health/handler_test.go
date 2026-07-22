package health

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestFaultEventCursorPaginationIsStableAcrossUpdatesAndInserts(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 7; i++ {
		event := api.GPUFaultEvent{EpisodeKey: fmt.Sprintf("episode-%d", i), State: "open", Severity: "warning", RuleCode: "TEST", FirstObservedAt: now, LastObservedAt: now}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}
	handler := NewHandler(db)
	type responsePayload struct {
		Data []api.GPUFaultEvent `json:"data"`
		Meta struct {
			Total        int64 `json:"total"`
			HasMore      bool  `json:"has_more"`
			NextBeforeID uint  `json:"next_before_id"`
		} `json:"meta"`
	}
	fetch := func(path string) responsePayload {
		t.Helper()
		response := httptest.NewRecorder()
		handler.HandleEvents(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var payload responsePayload
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	first := fetch("/api/v1/fault-events?limit=3")
	if len(first.Data) != 3 || first.Data[0].ID != 7 || first.Meta.NextBeforeID != 5 || !first.Meta.HasMore {
		t.Fatalf("unexpected first page: %+v", first)
	}
	// Mutable fields must not affect ordering, and a new row must stay ahead of
	// the traversal cursor rather than reappearing on a later page.
	if err := db.Model(&api.GPUFaultEvent{}).Where("id = ?", 4).Updates(map[string]any{"severity": "critical", "last_observed_at": now.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.GPUFaultEvent{EpisodeKey: "new", State: "open", Severity: "critical", RuleCode: "NEW", FirstObservedAt: now, LastObservedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	second := fetch(fmt.Sprintf("/api/v1/fault-events?limit=3&before_id=%d", first.Meta.NextBeforeID))
	third := fetch(fmt.Sprintf("/api/v1/fault-events?limit=3&before_id=%d", second.Meta.NextBeforeID))
	got := []uint{}
	for _, item := range append(append(first.Data, second.Data...), third.Data...) {
		got = append(got, item.ID)
	}
	if fmt.Sprint(got) != fmt.Sprint([]uint{7, 6, 5, 4, 3, 2, 1}) {
		t.Fatalf("got IDs %v", got)
	}
}

func TestFaultEventPaginationRejectsInvalidCursor(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	for _, path := range []string{"/api/v1/fault-events?limit=0", "/api/v1/fault-events?limit=x", "/api/v1/fault-events?before_id=-1"} {
		response := httptest.NewRecorder()
		handler.HandleEvents(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 400 {
			t.Fatalf("%s status=%d", path, response.Code)
		}
	}
}

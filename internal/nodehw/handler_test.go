package nodehw

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, day, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, day), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, day, "fleet-hw.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHandleFleetServesLatestDay(t *testing.T) {
	root := t.TempDir()
	write(t, root, "2026-11-20", `{"schema":"node-hw-fleet-v1","day":"2026-11-20"}`)
	write(t, root, "2026-11-21", `{"schema":"node-hw-fleet-v1","day":"2026-11-21"}`)
	_ = os.MkdirAll(filepath.Join(root, "2026-11-22"), 0o755) // no snapshot yet
	h := NewHandler(root)
	rec := httptest.NewRecorder()
	h.HandleFleet(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hardware/fleet", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Days []string `json:"available_days"`
		Data struct {
			Day string `json:"day"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Data.Day != "2026-11-21" || len(got.Days) != 2 || got.Days[0] != "2026-11-21" {
		t.Fatalf("unexpected %+v", got)
	}
	rec = httptest.NewRecorder()
	h.HandleFleet(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hardware/fleet?day=2026-11-20", nil))
	if rec.Code != 200 || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("day select failed %d", rec.Code)
	}
}

func TestHandleFleetRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	write(t, root, "2026-11-21", `{"schema":"other"}`)
	h := NewHandler(root)
	for _, c := range []struct {
		url  string
		code int
	}{{"/x?day=../../etc", 400}, {"/x?day=2026-11-21", 404}, {"/x?day=2026-11-19", 404}} {
		rec := httptest.NewRecorder()
		h.HandleFleet(rec, httptest.NewRequest(http.MethodGet, c.url, nil))
		if rec.Code != c.code {
			t.Fatalf("%s: got %d want %d", c.url, rec.Code, c.code)
		}
	}
	rec := httptest.NewRecorder()
	h.HandleFleet(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != 405 {
		t.Fatalf("post: %d", rec.Code)
	}
}

func TestHandleFleetSummaryDropsRows(t *testing.T) {
	root := t.TempDir()
	write(t, root, "2026-11-21", `{"schema":"node-hw-fleet-v1","day":"2026-11-21","coverage":{"nodes_total":2},"summary":{"disk":{"total":1}},"disks":[{"ip":"a"}],"memory":[{"ip":"a"}],"nodes":[{"ip":"a"}]}`)
	h := NewHandler(root)
	for i := 0; i < 2; i++ { // second call hits the cache
		rec := httptest.NewRecorder()
		h.HandleFleet(rec, httptest.NewRequest(http.MethodGet, "/x?summary=1", nil))
		var got struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("status %d %s", rec.Code, rec.Body.String())
		}
		if _, ok := got.Data["disks"]; ok {
			t.Fatal("summary must not include disks")
		}
		if string(got.Data["day"]) != `"2026-11-21"` || got.Data["coverage"] == nil || got.Data["summary"] == nil {
			t.Fatalf("missing header fields: %s", rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	h.HandleFleet(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !json.Valid(rec.Body.Bytes()) || !strings.Contains(rec.Body.String(), `"disks"`) {
		t.Fatal("full snapshot must still include rows")
	}
}

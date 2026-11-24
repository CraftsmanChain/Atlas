// Package nodehw serves the fleet disk/memory health snapshot produced offline by
// scripts/node_hw_fleet_report.py (schema node-hw-fleet-v1) from the daily read-only
// collector output. It only reads files; it never contacts nodes or the database.
package nodehw

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const Schema = "node-hw-fleet-v1"

var dayPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

type Handler struct {
	root string

	mu      sync.Mutex
	path    string
	modTime time.Time
	body    []byte
	summary map[string][]byte // path -> light summary
}

// NewHandler reads <root>/<YYYY-MM-DD>/fleet-hw.json (latest day that has one).
func NewHandler(root string) *Handler { return &Handler{root: strings.TrimSpace(root)} }

// Days lists available snapshot days, newest first.
func (h *Handler) Days() []string {
	entries, err := os.ReadDir(h.root)
	if err != nil {
		return nil
	}
	var days []string
	for _, e := range entries {
		if e.IsDir() && dayPattern.MatchString(e.Name()) {
			if _, err := os.Stat(filepath.Join(h.root, e.Name(), "fleet-hw.json")); err == nil {
				days = append(days, e.Name())
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	return days
}

func (h *Handler) load(day string) ([]byte, error) {
	path := filepath.Join(h.root, day, "fleet-hw.json")
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.path == path && h.modTime.Equal(info.ModTime()) && h.body != nil {
		return h.body, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var probe struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || probe.Schema != Schema {
		return nil, os.ErrInvalid
	}
	if h.path == path {
		delete(h.summary, path)
	}
	h.path, h.modTime, h.body = path, info.ModTime(), data
	return data, nil
}

// summaryOf keeps only the light header fields of a snapshot (no per-disk,
// per-node rows) so overview pages do not download the full fleet table.
func (h *Handler) summaryOf(day string, body []byte) ([]byte, error) {
	key := filepath.Join(h.root, day, "fleet-hw.json")
	h.mu.Lock()
	defer h.mu.Unlock()
	if v, ok := h.summary[key]; ok && h.path == key {
		return v, nil
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(body, &full); err != nil {
		return nil, err
	}
	light := map[string]json.RawMessage{}
	for _, key := range []string{"schema", "rule_version", "day", "previous_day", "collected_at", "generated_at", "coverage", "summary"} {
		if v, ok := full[key]; ok {
			light[key] = v
		}
	}
	out, err := json.Marshal(light)
	if err != nil {
		return nil, err
	}
	if h.summary == nil {
		h.summary = map[string][]byte{}
	}
	h.summary[key] = out
	return out, nil
}

// HandleFleet: GET /api/v1/hardware/fleet[?day=YYYY-MM-DD][&summary=1]
func (h *Handler) HandleFleet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	days := h.Days()
	day := r.URL.Query().Get("day")
	if day == "" {
		if len(days) == 0 {
			writeError(w, http.StatusNotFound, "no fleet hardware snapshot available")
			return
		}
		day = days[0]
	} else if !dayPattern.MatchString(day) {
		writeError(w, http.StatusBadRequest, "day must be YYYY-MM-DD")
		return
	}
	body, err := h.load(day)
	if err != nil {
		writeError(w, http.StatusNotFound, "fleet hardware snapshot unavailable for "+day)
		return
	}
	if r.URL.Query().Get("summary") == "1" {
		if body, err = h.summaryOf(day, body); err != nil {
			writeError(w, http.StatusInternalServerError, "snapshot summary unavailable")
			return
		}
	}
	if len(days) > 14 {
		days = days[:14]
	}
	daysJSON, _ := json.Marshal(days)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"available_days":`))
	_, _ = w.Write(daysJSON)
	_, _ = w.Write([]byte(`,"data":`))
	_, _ = w.Write(body)
	_, _ = w.Write([]byte(`}`))
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message}})
}

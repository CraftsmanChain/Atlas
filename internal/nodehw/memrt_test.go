package nodehw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	promclient "atlas/internal/prometheus"
)

type fakeQ struct {
	rows map[string][]promclient.Sample
}

func (f fakeQ) QueryAt(_ context.Context, q string, _ time.Time) ([]promclient.Sample, error) {
	best, out := -1, []promclient.Sample(nil) // longest matching key wins (queries nest each other)
	for k, v := range f.rows {
		if strings.Contains(q, k) && len(k) > best {
			best, out = len(k), v
		}
	}
	return out, nil
}

func s(ip string, v float64, extra ...string) promclient.Sample {
	m := map[string]string{"host_ip": ip}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return promclient.Sample{Metric: m, Value: v}
}

func memRTFixture(t *testing.T) (*MemRT, string) {
	dir := t.TempDir()
	resets := filepath.Join(dir, "sel_reset_events.tsv")
	if err := os.WriteFile(resets, []byte("ip\ttime\tby\n10.0.0.9\t2026-11-26 16:10:00\tatlas\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := fakeQ{rows: map[string][]promclient.Sample{
		// UE: now 3 vs min 2 over previous 30 min on .1; unchanged on .2
		`max by (host_ip) (max_over_time(ipmi_sel_events_count_by_name{name="uncorrectable_ecc"}[5m]))`:                 {s("10.0.0.1", 3), s("10.0.0.2", 1)},
		`min_over_time((max by (host_ip) (max_over_time(ipmi_sel_events_count_by_name{name="uncorrectable_ecc"}[5m])))`: {s("10.0.0.1", 2), s("10.0.0.2", 1)},
		// CE burst: 174.214-like refill after a clear (min over the hour = 3, now 400)
		`max_over_time((max by (host_ip) (ipmi_sel_events_count_by_name{name="correctable_ecc"})`:               {s("10.0.0.3", 400), s("10.0.0.4", 120)},
		`min_over_time(max_over_time((max by (host_ip) (ipmi_sel_events_count_by_name{name="correctable_ecc"})`: {s("10.0.0.3", 3), s("10.0.0.4", 100)},
		// SEL logs: .5 cleared externally, .9 cleared by Atlas rotation, .6 a small dip
		`max by (host_ip) (max_over_time(ipmi_sel_logs_count[5m]))`:                           {s("10.0.0.5", 3), s("10.0.0.9", 0), s("10.0.0.6", 1300)},
		`max by (host_ip) (max_over_time(ipmi_sel_logs_count[30m] offset 5m))`:                {s("10.0.0.5", 1364), s("10.0.0.9", 1364), s("10.0.0.6", 1364)},
		`max by (host_ip) (max_over_time(node_memory_HardwareCorrupted_bytes[5m]))`:           {s("10.0.0.7", 8192)},
		`max by (host_ip) (max_over_time(node_memory_HardwareCorrupted_bytes[1h] offset 5m))`: {s("10.0.0.7", 4096)},
		// DIMM sensors: .8 DIMM000 stable 0 -> 2 (alarm); .10 unstable mapping (filtered)
		`[5m])) > 0`:       {s("10.0.0.8", 2, "id", "127", "name", "DIMM000"), s("10.0.0.10", 2, "id", "130", "name", "DIMM080")},
		`[1h] offset 5m))`: {s("10.0.0.8", 0, "id", "127", "name", "DIMM000"), s("10.0.0.10", 0, "id", "130", "name", "DIMM080")},
		`[1d]))`: {s("10.0.0.8", 2, "id", "127", "name", "DIMM000"), s("10.0.0.10", 2, "id", "130", "name", "DIMM080"),
			s("10.0.0.10", 0, "id", "130", "name", "DIMM060")},
	}}
	return NewMemRT(q, filepath.Join(dir, "mem_rt_events.tsv"), resets, ""), dir
}

func TestMemRTSignals(t *testing.T) {
	m, dir := memRTFixture(t)
	at := time.Date(2026, 11, 26, 16, 25, 0, 0, shanghai)
	evs, err := m.Poll(context.Background(), at)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]RTEvent{}
	for _, e := range evs {
		got[e.Signal+"@"+e.IP] = e
	}
	for _, want := range []string{"ue_increase@10.0.0.1", "ce_burst_1h@10.0.0.3", "sel_cleared@10.0.0.5", "sel_cleared@10.0.0.9",
		"hw_corrupted_up@10.0.0.7", "dimm_sensor_alarm@10.0.0.8"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s in %+v", want, evs)
		}
	}
	for _, not := range []string{"ue_increase@10.0.0.2", "ce_burst_1h@10.0.0.4", "sel_cleared@10.0.0.6", "dimm_sensor_alarm@10.0.0.10"} {
		if _, ok := got[not]; ok {
			t.Errorf("unexpected %s", not)
		}
	}
	if got["sel_cleared@10.0.0.9"].By != "atlas" || got["sel_cleared@10.0.0.5"].By != "external" {
		t.Errorf("clear attribution wrong: %+v / %+v", got["sel_cleared@10.0.0.9"], got["sel_cleared@10.0.0.5"])
	}
	// second poll 5 minutes later: same values -> nothing new (dedupe)
	again, _ := m.Poll(context.Background(), at.Add(5*time.Minute))
	if len(again) != 0 {
		t.Errorf("expected dedupe, got %+v", again)
	}
	data, err := os.ReadFile(filepath.Join(dir, "mem_rt_events.tsv"))
	if err != nil || strings.Count(string(data), "\n") != len(evs)+1 {
		t.Fatalf("events file: %v %q", err, data)
	}
	// restart: events reloaded, dedupe survives
	m2 := NewMemRT(m.q, filepath.Join(dir, "mem_rt_events.tsv"), filepath.Join(dir, "sel_reset_events.tsv"), "")
	if again, _ := m2.Poll(context.Background(), at.Add(10*time.Minute)); len(again) != 0 {
		t.Errorf("expected dedupe after restart, got %+v", again)
	}
	m2.Now = func() time.Time { return at.Add(time.Hour) }
	rec := httptest.NewRecorder()
	m2.HandleEvents(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hardware/memory/rt-events?hours=24", nil))
	var body struct {
		Version string    `json:"version"`
		Events  []RTEvent `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Version != MemRTVersion || len(body.Events) != len(evs) {
		t.Fatalf("handler: %v %s", err, rec.Body.String())
	}
}

func TestSwapList(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026-11-27")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	snap := `{"schema":"node-hw-fleet-v1","rule_version":"node-hw-rules-v2.0","memory":[{"ip":"10.0.0.1","host":"h","board":"GOOXI G3DCTR-B PCBA","level":"warning","hits":["mem_h4_ce_storm"],"dimm_issues":[{"dmi_slot":"DIMM 0 / P0 CHANNEL B","sn":"G0B0","ce_30d":60,"first_ce":"2026-11-26","mapped":true}]}]}`
	if err := os.WriteFile(filepath.Join(day, "fleet-hw.json"), []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(root)
	rec := httptest.NewRecorder()
	h.HandleSwapList(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hardware/memory/swaplist?ip=10.0.0.1", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"G0B0"`) || !strings.Contains(rec.Body.String(), "node-hw-rules-v2.0") {
		t.Fatalf("swaplist: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.HandleSwapList(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hardware/memory/swaplist?ip=10.0.0.2", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown ip: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.HandleSwapList(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hardware/memory/swaplist?ip=x;rm", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad ip: %d", rec.Code)
	}
}

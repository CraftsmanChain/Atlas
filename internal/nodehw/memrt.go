package nodehw

// MEM-RT (memory plan v1.2 section 3.3): near-real-time, read-only detection of
// serious memory signals from VictoriaMetrics every 5 minutes. It only queries
// the monitoring source and appends to a TSV file; it never contacts nodes or
// BMCs, sends no notification and executes no action (U4).
//
// Signals (evaluated at time t; "now" = max over the last 5 minutes so a
// single missing/partial sample does not count):
//   ue_increase        ipmi_sel_events_count_by_name{name="uncorrectable_ecc"}
//                      now - min over the previous 30 min >= 1
//   ce_burst_1h        CE = correctable_ecc - uncorrectable_ecc (ipmi_exporter's
//                      correctable_ecc also matches "Uncorrectable ECC");
//                      now - min over the previous hour >= 50. The minimum makes
//                      a SEL clear inside the hour count only post-clear records.
//   sel_cleared        ipmi_sel_logs_count: all samples of the last 5 min
//                      <= 0.7 x the max of the 30 min before, and >= 20 lower;
//                      by=atlas when node-hw-ledger/sel_reset_events.tsv has an
//                      Atlas rotation for that node within 2 h.
//   hw_corrupted_up    node_memory_HardwareCorrupted_bytes above its max of the
//                      previous hour.
//   dimm_sensor_alarm  ipmi_sensor_state{type="Memory"} of a DIMM-named sensor
//                      > 0 now and 0 for the previous hour; only for (host, id)
//                      whose sensor name is stable over a day (11-27 A6: XFUSION
//                      alternates two SDR mappings, state=2 is an artefact) and
//                      not on XFUSION boards (Redfish Health is used there).
// The daily fleet report lists these events; it does not add them to the
// ledger windows, so nothing is counted twice.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	promclient "atlas/internal/prometheus"
)

const MemRTVersion = "mem-rt-v1"

var shanghai = time.FixedZone("Asia/Shanghai", 8*3600)

// InstantQuerier is the subset of the Prometheus client MEM-RT needs.
type InstantQuerier interface {
	QueryAt(ctx context.Context, query string, at time.Time) ([]promclient.Sample, error)
}

type RTEvent struct {
	Time   string `json:"time"`
	IP     string `json:"ip"`
	Signal string `json:"signal"`
	Before string `json:"before"`
	After  string `json:"after"`
	Detail string `json:"detail,omitempty"`
	By     string `json:"by,omitempty"`
}

var rtColumns = []string{"time", "ip", "signal", "before", "after", "detail", "by", "rule"}

type MemRT struct {
	q          InstantQuerier
	path       string // mem_rt_events.tsv
	resetsPath string // sel_reset_events.tsv (Atlas rotations)
	fleetRoot  string // node-hw-daily, for board names
	Now        func() time.Time

	mu        sync.Mutex
	events    []RTEvent
	seen      map[string]time.Time
	stableAt  time.Time
	stable    map[string]bool // host|id -> name stable over 1d
	boards    map[string]string
	boardsAt  time.Time
	lastError string
	lastRun   time.Time
}

func NewMemRT(q InstantQuerier, eventsPath, resetsPath, fleetRoot string) *MemRT {
	m := &MemRT{q: q, path: eventsPath, resetsPath: resetsPath, fleetRoot: fleetRoot, Now: time.Now, seen: map[string]time.Time{}}
	m.loadExisting()
	return m
}

func (m *MemRT) loadExisting() {
	f, err := os.Open(m.path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		c := strings.Split(sc.Text(), "\t")
		for len(c) < len(rtColumns) {
			c = append(c, "")
		}
		e := RTEvent{Time: c[0], IP: c[1], Signal: c[2], Before: c[3], After: c[4], Detail: c[5], By: c[6]}
		m.events = append(m.events, e)
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", e.Time, shanghai); err == nil {
			m.seen[dedupeKey(e)] = t
		}
	}
	if len(m.events) > 5000 {
		m.events = m.events[len(m.events)-5000:]
	}
}

// dedupeKey: one event per new UE / bad-page value; bursts and clears at most
// once per window (see dedupeWindow).
func dedupeKey(e RTEvent) string {
	switch e.Signal {
	case "ue_increase", "hw_corrupted_up":
		return e.IP + "|" + e.Signal + "|" + e.After
	case "dimm_sensor_alarm":
		return e.IP + "|" + e.Signal + "|" + e.Detail
	default:
		return e.IP + "|" + e.Signal
	}
}

func dedupeWindow(signal string) time.Duration {
	switch signal {
	case "ce_burst_1h":
		return 6 * time.Hour
	case "sel_cleared":
		return time.Hour
	default:
		return 24 * time.Hour
	}
}

// Run evaluates every interval until ctx ends.
func (m *MemRT) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if _, err := m.Poll(ctx, m.Now()); err != nil {
			log.Printf("MEM-RT poll failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Poll evaluates at t, appends new (deduplicated) events and returns them.
func (m *MemRT) Poll(ctx context.Context, t time.Time) ([]RTEvent, error) {
	evs, err := m.Evaluate(ctx, t)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRun = t
	if err != nil {
		m.lastError = err.Error()
	} else {
		m.lastError = ""
	}
	var fresh []RTEvent
	for _, e := range evs {
		k := dedupeKey(e)
		if prev, ok := m.seen[k]; ok && t.Sub(prev) < dedupeWindow(e.Signal) && t.Sub(prev) > -dedupeWindow(e.Signal) {
			continue
		}
		m.seen[k] = t
		fresh = append(fresh, e)
	}
	if len(fresh) > 0 {
		m.events = append(m.events, fresh...)
		if len(m.events) > 5000 {
			m.events = m.events[len(m.events)-5000:]
		}
		if werr := appendEvents(m.path, fresh); werr != nil {
			log.Printf("MEM-RT: write %s: %v (events kept in memory)", m.path, werr)
		}
	}
	return fresh, err
}

func appendEvents(path string, evs []RTEvent) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, statErr := os.Stat(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	if os.IsNotExist(statErr) {
		fmt.Fprintln(w, strings.Join(rtColumns, "\t"))
	}
	for _, e := range evs {
		fmt.Fprintln(w, strings.Join([]string{e.Time, e.IP, e.Signal, e.Before, e.After, clean(e.Detail), e.By, MemRTVersion}, "\t"))
	}
	return w.Flush()
}

func clean(s string) string { return strings.NewReplacer("\t", " ", "\n", " ").Replace(s) }

const (
	selCount = `ipmi_sel_events_count_by_name{name="%s"}`
	ceExpr   = `(max by (host_ip) (ipmi_sel_events_count_by_name{name="correctable_ecc"}) - on (host_ip) max by (host_ip) (ipmi_sel_events_count_by_name{name="uncorrectable_ecc"}))`
)

type pair struct{ now, before float64 }

func (m *MemRT) pairs(ctx context.Context, at time.Time, nowQ, beforeQ string) (map[string]pair, error) {
	now, err := m.q.QueryAt(ctx, nowQ, at)
	if err != nil {
		return nil, err
	}
	before, err := m.q.QueryAt(ctx, beforeQ, at)
	if err != nil {
		return nil, err
	}
	out := map[string]pair{}
	for _, s := range now {
		if ip := s.Metric["host_ip"]; ip != "" {
			out[ip] = pair{now: s.Value, before: math.NaN()}
		}
	}
	for _, s := range before {
		if p, ok := out[s.Metric["host_ip"]]; ok {
			p.before = s.Value
			out[s.Metric["host_ip"]] = p
		}
	}
	return out, nil
}

func f0(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// Evaluate runs all signals at t (no dedupe, no write). Partial results are
// returned with the first error.
func (m *MemRT) Evaluate(ctx context.Context, t time.Time) ([]RTEvent, error) {
	ts := t.In(shanghai).Format("2006-01-02 15:04:05")
	var out []RTEvent
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	ue := fmt.Sprintf(selCount, "uncorrectable_ecc")
	if p, err := m.pairs(ctx, t,
		`max by (host_ip) (max_over_time(`+ue+`[5m]))`,
		`min_over_time((max by (host_ip) (max_over_time(`+ue+`[5m])))[30m:5m] offset 5m)`); err == nil {
		for ip, v := range p {
			if !math.IsNaN(v.before) && v.now-v.before >= 1 {
				out = append(out, RTEvent{Time: ts, IP: ip, Signal: "ue_increase", Before: f0(v.before), After: f0(v.now)})
			}
		}
	} else {
		keep(err)
	}
	if p, err := m.pairs(ctx, t,
		`max_over_time(`+ceExpr+`[5m:1m])`,
		`min_over_time(max_over_time(`+ceExpr+`[5m:1m])[1h:5m] offset 5m)`); err == nil {
		for ip, v := range p {
			if !math.IsNaN(v.before) && v.now-v.before >= 50 {
				out = append(out, RTEvent{Time: ts, IP: ip, Signal: "ce_burst_1h", Before: f0(v.before), After: f0(v.now),
					Detail: fmt.Sprintf("+%s CE within 1h", f0(v.now-v.before))})
			}
		}
	} else {
		keep(err)
	}
	if p, err := m.pairs(ctx, t,
		`max by (host_ip) (max_over_time(ipmi_sel_logs_count[5m]))`,
		`max by (host_ip) (max_over_time(ipmi_sel_logs_count[30m] offset 5m))`); err == nil {
		resets := m.atlasResets()
		for ip, v := range p {
			if !math.IsNaN(v.before) && v.before-v.now >= 20 && v.now <= 0.7*v.before {
				by := "external"
				for _, r := range resets[ip] {
					if d := t.Sub(r); d > -time.Hour && d < 2*time.Hour {
						by = "atlas"
					}
				}
				out = append(out, RTEvent{Time: ts, IP: ip, Signal: "sel_cleared", Before: f0(v.before), After: f0(v.now), By: by})
			}
		}
	} else {
		keep(err)
	}
	if p, err := m.pairs(ctx, t,
		`max by (host_ip) (max_over_time(node_memory_HardwareCorrupted_bytes[5m]))`,
		`max by (host_ip) (max_over_time(node_memory_HardwareCorrupted_bytes[1h] offset 5m))`); err == nil {
		for ip, v := range p {
			if !math.IsNaN(v.before) && v.now > v.before {
				out = append(out, RTEvent{Time: ts, IP: ip, Signal: "hw_corrupted_up", Before: f0(v.before), After: f0(v.now)})
			}
		}
	} else {
		keep(err)
	}
	evs, err := m.dimmSensors(ctx, t, ts)
	keep(err)
	out = append(out, evs...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Signal != out[j].Signal {
			return out[i].Signal < out[j].Signal
		}
		return out[i].IP < out[j].IP
	})
	return out, firstErr
}

var dimmName = regexp.MustCompile(`(?i)DIMM`)

func (m *MemRT) dimmSensors(ctx context.Context, t time.Time, ts string) ([]RTEvent, error) {
	sel := `ipmi_sensor_state{type="Memory"}`
	now, err := m.q.QueryAt(ctx, `max by (host_ip, id, name) (max_over_time(`+sel+`[5m])) > 0`, t)
	if err != nil || len(now) == 0 {
		return nil, err
	}
	before, err := m.q.QueryAt(ctx, `max by (host_ip, id, name) (max_over_time(`+sel+`[1h] offset 5m))`, t)
	if err != nil {
		return nil, err
	}
	if err := m.refreshStable(ctx, t); err != nil {
		return nil, err
	}
	m.refreshBoards(t)
	prev := map[string]float64{}
	for _, s := range before {
		prev[s.Metric["host_ip"]+"|"+s.Metric["id"]+"|"+s.Metric["name"]] = s.Value
	}
	var out []RTEvent
	for _, s := range now {
		ip, id, name := s.Metric["host_ip"], s.Metric["id"], s.Metric["name"]
		if !dimmName.MatchString(name) || !m.stableFor(ip+"|"+id) || strings.Contains(strings.ToUpper(m.boardOf(ip)), "BC16MBSBA") {
			continue
		}
		if p, ok := prev[ip+"|"+id+"|"+name]; ok && p == 0 {
			out = append(out, RTEvent{Time: ts, IP: ip, Signal: "dimm_sensor_alarm", Before: "0", After: f0(s.Value), Detail: name})
		}
	}
	return out, nil
}

func (m *MemRT) stableFor(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stable[key]
}

func (m *MemRT) refreshStable(ctx context.Context, t time.Time) error {
	m.mu.Lock()
	fresh := m.stable != nil && t.Sub(m.stableAt) < time.Hour && t.Sub(m.stableAt) > -time.Hour
	m.mu.Unlock()
	if fresh {
		return nil
	}
	res, err := m.q.QueryAt(ctx, `max by (host_ip, id, name) (max_over_time(ipmi_sensor_state{type="Memory"}[1d]))`, t)
	if err != nil {
		return err
	}
	names := map[string]map[string]bool{}
	ids := map[string]map[string]bool{}
	for _, s := range res {
		k := s.Metric["host_ip"] + "|" + s.Metric["id"]
		n := s.Metric["host_ip"] + "|" + s.Metric["name"]
		if names[k] == nil {
			names[k] = map[string]bool{}
		}
		if ids[n] == nil {
			ids[n] = map[string]bool{}
		}
		names[k][s.Metric["name"]] = true
		ids[n][s.Metric["id"]] = true
	}
	stable := map[string]bool{}
	for k, ns := range names {
		ok := len(ns) == 1
		for name := range ns {
			ip := strings.SplitN(k, "|", 2)[0]
			if len(ids[ip+"|"+name]) > 1 {
				ok = false
			}
		}
		stable[k] = ok
	}
	m.mu.Lock()
	m.stable, m.stableAt = stable, t
	m.mu.Unlock()
	return nil
}

// boards: ip -> board name from the latest fleet-hw.json (memory rows).
func (m *MemRT) refreshBoards(t time.Time) {
	m.mu.Lock()
	fresh := m.boards != nil && t.Sub(m.boardsAt) < 6*time.Hour && t.Sub(m.boardsAt) > -6*time.Hour
	m.mu.Unlock()
	if fresh || m.fleetRoot == "" {
		return
	}
	days := NewHandler(m.fleetRoot).Days()
	boards := map[string]string{}
	if len(days) > 0 {
		if data, err := os.ReadFile(filepath.Join(m.fleetRoot, days[0], "fleet-hw.json")); err == nil {
			var snap struct {
				Memory []struct {
					IP    string `json:"ip"`
					Board string `json:"board"`
				} `json:"memory"`
			}
			if json.Unmarshal(data, &snap) == nil {
				for _, r := range snap.Memory {
					boards[r.IP] = r.Board
				}
			}
		}
	}
	m.mu.Lock()
	m.boards, m.boardsAt = boards, t
	m.mu.Unlock()
}

func (m *MemRT) boardOf(ip string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.boards[ip]
}

func (m *MemRT) atlasResets() map[string][]time.Time {
	out := map[string][]time.Time{}
	f, err := os.Open(m.resetsPath)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		c := strings.Split(sc.Text(), "\t")
		if len(c) < 2 || c[0] == "ip" {
			continue
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", c[1], shanghai); err == nil {
			out[c[0]] = append(out[c[0]], t)
		}
	}
	return out
}

// Recent returns events newer than since (newest last) plus run status.
func (m *MemRT) Recent(since time.Time) ([]RTEvent, time.Time, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lo := since.In(shanghai).Format("2006-01-02 15:04:05")
	var out []RTEvent
	for _, e := range m.events {
		if e.Time >= lo {
			out = append(out, e)
		}
	}
	return out, m.lastRun, m.lastError
}

// HandleEvents: GET /api/v1/hardware/memory/rt-events[?hours=48]
func (m *MemRT) HandleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	hours := 48
	if v, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && v > 0 && v <= 24*30 {
		hours = v
	}
	evs, last, lastErr := m.Recent(m.Now().Add(-time.Duration(hours) * time.Hour))
	if evs == nil {
		evs = []RTEvent{}
	}
	body := map[string]any{"version": MemRTVersion, "hours": hours, "events": evs, "read_only": true}
	if !last.IsZero() {
		body["last_run"] = last.In(shanghai).Format(time.RFC3339)
	}
	if lastErr != "" {
		body["last_error"] = lastErr
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(body)
}

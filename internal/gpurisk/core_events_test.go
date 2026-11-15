package gpurisk

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"atlas/internal/prometheus"
	"atlas/pkg/api"
)

// fakeVM serves daily buckets: present[series][day] = value (xid: tfirst unix).
type fakeVM struct {
	xid, urr, rrf, gpus map[string]map[string]float64
	labels              map[string]map[string]string
	calls               int
	fail                bool
}

func (f *fakeVM) QueryRange(_ context.Context, q string, start, end time.Time, step time.Duration) ([]prometheus.RangeSeries, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("vm down")
	}
	src := f.xid
	if strings.Contains(q, "GPU_TEMP") {
		src = f.gpus
	} else if strings.Contains(q, "UNCORRECTABLE_REMAPPED_ROWS") {
		src = f.urr
	} else if strings.Contains(q, "ROW_REMAP_FAILURE") {
		src = f.rrf
	}
	out := []prometheus.RangeSeries{}
	for k, byDay := range src {
		r := prometheus.RangeSeries{Metric: f.labels[k]}
		for t := start; !t.After(end); t = t.Add(step) {
			// full-day point at t covers the day ending at t; a partial point
			// (start==end, not at midnight) covers its own day.
			d := t.Add(-time.Second).In(shanghai).Format("2006-01-02")
			if start.Equal(end) && !t.Equal(dayStart(t)) {
				d = t.In(shanghai).Format("2006-01-02")
			}
			if v, ok := byDay[d]; ok {
				r.Values = append(r.Values, prometheus.RangePoint{Timestamp: t, Value: v})
			}
		}
		if len(r.Values) > 0 {
			out = append(out, r)
		}
	}
	return out, nil
}

func sday(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", s, shanghai)
	return t
}

func newFakeVM() *fakeVM {
	return &fakeVM{xid: map[string]map[string]float64{}, urr: map[string]map[string]float64{}, rrf: map[string]map[string]float64{}, gpus: map[string]map[string]float64{}, labels: map[string]map[string]string{}}
}

func (f *fakeVM) setXID(ip, uuid, code string, days ...string) {
	k := uuid + "|" + code
	f.labels[k] = map[string]string{"UUID": uuid, "err_code": code, "host_ip": ip, "modelName": "NVIDIA GeForce RTX 4090"}
	if f.xid[k] == nil {
		f.xid[k] = map[string]float64{}
	}
	for _, d := range days {
		f.xid[k][d] = float64(sday(d + " 10:30").Unix())
	}
}

func TestDetectCoreEventsOnsetRules(t *testing.T) {
	vm := newFakeVM()
	// onset 07-10, persists 07-11..07-12 (one onset); gap of 1 day (07-14) does
	// not restart; absent 07-16,07-17 then 07-18 restarts.
	vm.setXID("10.0.0.1", "GPU-a", "79", "2026-07-10", "2026-07-11", "2026-07-12", "2026-07-14", "2026-07-15", "2026-07-18")
	// present on the very first monitored days: never an onset (no lookback)
	vm.setXID("10.0.0.2", "GPU-b", "119", "2026-06-30", "2026-07-01", "2026-07-03")
	// urr growth on 07-12 (5 -> 6); flat otherwise
	vm.labels["GPU-c"] = map[string]string{"UUID": "GPU-c", "host_ip": "10.0.0.3"}
	vm.urr["GPU-c"] = map[string]float64{"2026-07-10": 5, "2026-07-11": 5, "2026-07-12": 6, "2026-07-13": 6}
	vm.rrf["GPU-c"] = map[string]float64{"2026-07-10": 0, "2026-07-11": 0, "2026-07-12": 1, "2026-07-13": 1}
	events, _, err := DetectCoreEvents(context.Background(), vm, CoreEventLedgerStart, sday("2026-07-20 02:00"))
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, e := range events {
		got = append(got, e.NodeIP+"/"+e.Code+"/"+e.Day)
		if e.Code == "xid79" && e.Day == "2026-07-10" && !e.ObservedAt.Equal(sday("2026-07-10 10:30").UTC()) {
			t.Fatal("tfirst observed_at", e.ObservedAt)
		}
	}
	want := "10.0.0.1/xid79/2026-07-10,10.0.0.3/remap_fail/2026-07-12,10.0.0.3/urr_growth/2026-07-12,10.0.0.1/xid79/2026-07-18"
	if strings.Join(sortedStrings(got), ",") != strings.Join(sortedStrings(strings.Split(want, ",")), ",") {
		t.Fatal(got)
	}
}

func sortedStrings(s []string) []string {
	out := append([]string{}, s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestDetectCoreEventsPartialDay(t *testing.T) {
	vm := newFakeVM()
	vm.setXID("10.0.0.1", "GPU-a", "79", "2026-11-16")
	events, _, err := DetectCoreEvents(context.Background(), vm, sday("2026-11-10 00:00"), sday("2026-11-16 02:00"))
	if err != nil || len(events) != 1 || events[0].Day != "2026-11-16" {
		t.Fatal(events, err)
	}
}

func TestCoreRecurrenceShadow(t *testing.T) {
	db, q := shadowDB(t)
	if e := db.AutoMigrate(&api.GPUCoreEvent{}, &api.GPUCoreEventRun{}); e != nil {
		t.Fatal(e)
	}
	c := q.Cutoff
	// Stale ledger: R1 refuses, v1 still freezes.
	if e := ShadowDailyStrategies(context.Background(), db, q.CompletedAt.Add(time.Minute), ShadowStrategies); !errors.Is(e, ErrCoreLedgerStale) {
		t.Fatal(e)
	}
	var n int64
	db.Model(&api.GPURiskShadowBatch{}).Where("strategy = ?", ShadowStrategy).Count(&n)
	if n != 1 {
		t.Fatal("v1 not frozen", n)
	}
	day := func(t time.Time) string { return t.In(shanghai).Format("2006-01-02") }
	mk := func(ip, code string, at time.Time, imported time.Time) {
		e := api.GPUCoreEvent{Key: ip + code + at.String(), Source: CoreEventSourceVM, NodeIP: ip, GPUUUID: "g-" + ip, Code: code, Day: day(at), ObservedAt: at, ImportedAt: imported}
		if err := db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	imp := c.Add(-time.Hour)
	// node-1: two episodes within 90d (20d and 10d before), latest 10d ago.
	mk("node-1", "xid79", c.Add(-20*24*time.Hour), imp)
	mk("node-1", "xid79", c.Add(-19*24*time.Hour), imp) // same episode (1 day gap)
	mk("node-1", "xid119", c.Add(-10*24*time.Hour), imp)
	// node-2: one old episode (200d) => 0 in window, recency only.
	mk("node-2", "xid79", c.Add(-200*24*time.Hour), imp)
	// leakage guards: after cutoff, and imported after compute time
	mk("node-2", "xid79", c.Add(time.Hour), imp)
	if err := db.Create(&api.GPUCoreEventRun{Source: CoreEventSourceVM, WindowStart: c.Add(-300 * 24 * time.Hour), WindowEnd: c.Add(time.Minute), StartedAt: c, Status: "completed"}).Error; err != nil {
		t.Fatal(err)
	}
	// An event imported after the caller's tick time (same operations tick,
	// ledger stage ran first) must still be visible.
	mk("node-1", "xid79", c.Add(-40*24*time.Hour), time.Now().Add(time.Second))
	time.Sleep(1100 * time.Millisecond)
	b, rows, err := ComputeCoreRecurrence(context.Background(), db, q.ID, *q.CompletedAt)
	if err != nil {
		t.Fatal(err)
	}
	if b.Timing != "prospective" || b.ScoredCount != 2 || len(rows) != 2 {
		t.Fatal(b)
	}
	by := map[int]api.GPURiskShadowScore{}
	for _, r := range rows {
		by[r.Rank] = r
	}
	if by[1].History != 3 || by[2].History != 0 || by[2].DaysSince == nil || *by[2].DaysSince < 199 {
		t.Fatal(rows[0], rows[1])
	}
	// R4 cohort: node-2 first seen 3 days before cutoff (young) and 4090;
	// node-1 old H100. node-2 gets +0.8 but stays below node-1's history.
	if e := db.AutoMigrate(&api.GPUNodeObservation{}); e != nil {
		t.Fatal(e)
	}
	db.Create(&api.GPUNodeObservation{NodeIP: "node-1", Model: "NVIDIA H100 80GB HBM3", FirstSeenDay: c.AddDate(0, 0, -100).In(shanghai).Format("2006-01-02"), LastSeenDay: "x"})
	db.Create(&api.GPUNodeObservation{NodeIP: "node-2", Model: "NVIDIA GeForce RTX 4090", FirstSeenDay: c.AddDate(0, 0, -3).In(shanghai).Format("2006-01-02"), LastSeenDay: "x"})
	_, crow, err := ComputeShadowStrategy(context.Background(), db, q.ID, *q.CompletedAt, CoreCohortStrategy)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range crow {
		if r.History == 0 && (r.RawScore == nil || *r.RawScore < 0.8 || *r.RawScore > 0.81) {
			t.Fatal("cohort score", *r.RawScore)
		}
		if r.History == 3 && (r.RawScore == nil || *r.RawScore > 3.1) {
			t.Fatal("old H100 got cohort bonus", *r.RawScore)
		}
	}
	if e := ShadowDailyStrategies(context.Background(), db, q.CompletedAt.Add(time.Minute), ShadowStrategies); e != nil {
		t.Fatal(e)
	}
	db.Model(&api.GPURiskShadowBatch{}).Count(&n)
	if n != 3 {
		t.Fatal("batches", n)
	}
}

func TestCoreEventsDailyImportIdempotent(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPUCoreEvent{}, &api.GPUCoreEventRun{}, &api.GPUNodeObservation{}); e != nil {
		t.Fatal(e)
	}
	vm := newFakeVM()
	vm.setXID("10.0.0.1", "GPU-a", "79", "2026-07-10")
	vm.labels["n1"] = map[string]string{"host_ip": "10.0.0.1", "modelName": "NVIDIA GeForce RTX 4090"}
	vm.gpus["n1"] = map[string]float64{"2026-07-05": 8, "2026-07-06": 7, "2026-07-19": 8}
	now := sday("2026-07-20 03:00")
	if e := CoreEventsDaily(context.Background(), db, vm, now); e != nil {
		t.Fatal(e)
	}
	var ob api.GPUNodeObservation
	if e := db.First(&ob, "node_ip = ?", "10.0.0.1").Error; e != nil || ob.FirstSeenDay != "2026-07-05" || ob.LastSeenDay != "2026-07-19" || ob.MaxGPUs != 8 {
		t.Fatal(ob, e)
	}
	calls := vm.calls
	// within refresh window and past newest cutoff: no-op
	if e := CoreEventsDaily(context.Background(), db, vm, now.Add(time.Hour)); e != nil || vm.calls != calls {
		t.Fatal(e, vm.calls)
	}
	// later run overlaps and stays idempotent; failures are recorded and throttled
	vm.setXID("10.0.0.2", "GPU-b", "79", "2026-07-20")
	if e := CoreEventsDaily(context.Background(), db, vm, now.Add(7*time.Hour)); e != nil {
		t.Fatal(e)
	}
	var ev []api.GPUCoreEvent
	db.Order("id").Find(&ev)
	if len(ev) != 2 {
		t.Fatal(len(ev))
	}
	vm.fail = true
	if e := CoreEventsDaily(context.Background(), db, vm, now.Add(14*time.Hour)); e == nil {
		t.Fatal("want failure")
	}
	calls = vm.calls
	if e := CoreEventsDaily(context.Background(), db, vm, now.Add(14*time.Hour+time.Minute)); e != nil || vm.calls != calls {
		t.Fatal("retry not throttled", e)
	}
}

func TestCoreEpisodesMerge(t *testing.T) {
	ev := []api.GPUCoreEvent{
		{ID: 1, NodeIP: "n", GPUUUID: "a", Code: "xid79", Day: "2026-10-01"},
		{ID: 2, NodeIP: "n", GPUUUID: "b", Code: "xid79", Day: "2026-10-04"},  // gap 3 -> same
		{ID: 3, NodeIP: "n", GPUUUID: "a", Code: "xid119", Day: "2026-10-08"}, // gap 4 -> new
	}
	eps := CoreEpisodes(ev)["n"]
	if len(eps) != 2 || eps[0].GPUs != 2 || len(eps[1].Codes) != 1 {
		t.Fatal(eps)
	}
}

func TestKernelCoreEvents(t *testing.T) {
	in := "10.0.0.1\t2026-11-10T09:22:26+08:00\t0000:24:00\t119\t3\n" +
		"10.0.0.1\tNov 11 01:00:00\t0000:24:00\t119\t1\n" + // continues, no new onset
		"10.0.0.1\t2026-11-14T08:00:00+08:00\t0000:24:00\t119\t1\n" + // 2 quiet days -> onset
		"10.0.0.2\t2026-11-12T05:00:00+08:00\t0000:e1:00\t31\t9\n" + // non-critical
		"10.0.0.3\t2026-11-08T05:00:00+08:00\t0000:e1:00\t79\t1\n" + // inside lookback only
		"garbage\n"
	lines, bad, err := ParseKernelXIDAt(strings.NewReader(in), sday("2026-11-15 02:00"))
	if err != nil || bad != 1 || len(lines) != 5 {
		t.Fatal(lines, bad, err)
	}
	ev := DetectKernelCoreEvents(lines, sday("2026-11-08 00:00"), sday("2026-11-15 02:00"))
	got := []string{}
	for _, e := range ev {
		got = append(got, e.NodeIP+"/"+e.PCIBusID+"/"+e.Code+"/"+e.Day)
	}
	if strings.Join(got, ",") != "10.0.0.1/24:00/xid119/2026-11-10,10.0.0.1/24:00/xid119/2026-11-14" {
		t.Fatal(got)
	}
	if y, _ := parseKernelTime("Dec 31 23:00:00", sday("2027-01-01 01:00")); y.Year() != 2026 {
		t.Fatal("year", y)
	}
	if NormPCI("00000000:E1:00.0") != "e1:00" || NormPCI("0000:e1:00") != "e1:00" {
		t.Fatal("pci")
	}
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPUCoreEvent{}, &api.GPUCoreEventRun{}); e != nil {
		t.Fatal(e)
	}
	db.Create(&api.GPUCoreEvent{Key: "vm1", Source: CoreEventSourceVM, NodeIP: "10.0.0.1", GPUUUID: "GPU-x", PCIBusID: "00000000:24:00.0", Code: "xid119", Day: "2026-11-10", ObservedAt: sday("2026-11-10 09:22"), ImportedAt: time.Now()})
	for i := 0; i < 2; i++ {
		run, err := ImportKernelCoreEvents(context.Background(), db, lines, sday("2026-11-08 00:00"), sday("2026-11-15 02:00"))
		if err != nil || (i == 0 && run.NewEvents != 2) || (i == 1 && run.NewEvents != 0) {
			t.Fatal(i, run, err)
		}
	}
	var all []api.GPUCoreEvent
	db.Find(&all)
	eps := CoreEpisodes(all)["10.0.0.1"]
	if len(eps) != 2 || eps[0].GPUs != 1 {
		t.Fatal(eps)
	}
	for _, e := range all {
		if e.Source == CoreEventSourceKernel && e.GPUUUID != "GPU-x" {
			t.Fatal("identity not attached", e)
		}
	}
}

package gpurisk

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"atlas/internal/prometheus"
	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Core-event ledger (plan v5 M1, gpu-core-event-ledger-v1).
//
// Automatic critical GPU events, the HeaRank-style target and history source:
//
//	xidNN       - a DCGM_FI_DEV_XID_ERRORS{err_code=NN} series (per GPU UUID) is
//	              present on a Shanghai day and absent on the two days before
//	              (onset after >=3 days of absence; the series is the GPU's
//	              latest XID, so a persisting XID is one onset);
//	urr_growth  - daily max of DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS grows
//	              over the previous observed day;
//	remap_fail  - DCGM_FI_DEV_ROW_REMAP_FAILURE becomes > 0.
//
// It is read-only towards monitoring and writes only gpu_core_events/runs.
const CoreEventLedgerVersion = "gpu-core-event-ledger-v1"

const (
	CoreEventSourceVM     = "vm_dcgm"
	CoreEventSourceKernel = "kernel_log"
)

// CoreXIDCodes are the critical XIDs (route v3, plan v5). XID94 is excluded.
var CoreXIDCodes = []string{"79", "48", "95", "63", "64", "74", "119", "120", "154", "62", "92"}

var shanghai = time.FixedZone("Asia/Shanghai", 8*3600)

// CoreEventLedgerStart is the first monitored day; onsets are emitted only from
// two days later, when a full absence lookback exists.
var CoreEventLedgerStart = time.Date(2026, 6, 30, 0, 0, 0, 0, shanghai)

const (
	coreEventChunk    = 14 * 24 * time.Hour
	coreEventOverlap  = 3 * 24 * time.Hour
	coreEventLookback = 3 // days fetched before a window for onset detection
	coreEventRefresh  = 6 * time.Hour
	coreEventRetry    = 10 * time.Minute
	coreEventEpisode  = 3 // node days; a gap > 3 days starts a new episode
)

// RangeQuerier is the read-only monitoring interface the ledger needs.
type RangeQuerier interface {
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]prometheus.RangeSeries, error)
}

func dayStart(t time.Time) time.Time {
	l := t.In(shanghai)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, shanghai)
}

type coreObs struct {
	value  float64
	first  time.Time
	labels map[string]string
}

// coreSeries maps series key -> day -> observation.
type coreSeries map[string]map[string]coreObs

func (s coreSeries) add(key, day string, o coreObs) {
	if s[key] == nil {
		s[key] = map[string]coreObs{}
	}
	if old, ok := s[key][day]; ok {
		if o.value > old.value {
			old.value = o.value
		}
		if !o.first.IsZero() && (old.first.IsZero() || o.first.Before(old.first)) {
			old.first = o.first
		}
		s[key][day] = old
		return
	}
	s[key][day] = o
}

// coreFetch reads daily-bucketed series over full days [from, to) plus, when
// partialEnd is after to, the partial day [to, partialEnd).
func coreFetch(ctx context.Context, src RangeQuerier, expr func(window string) string, from, to, partialEnd time.Time, keyOf func(map[string]string) string, firstIsValue bool, out coreSeries) (int, error) {
	n := 0
	put := func(rs []prometheus.RangeSeries, dayOf func(time.Time) string) {
		for _, r := range rs {
			k := keyOf(r.Metric)
			if k == "" {
				continue
			}
			n++
			for _, p := range r.Values {
				o := coreObs{value: p.Value, labels: r.Metric}
				if firstIsValue {
					o.first = time.Unix(int64(p.Value), int64((p.Value-float64(int64(p.Value)))*1e9))
				}
				out.add(k, dayOf(p.Timestamp), o)
			}
		}
	}
	full := func(ts time.Time) string { return ts.Add(-time.Second).In(shanghai).Format("2006-01-02") }
	for s := from; s.Before(to); s = s.Add(coreEventChunk) {
		e := s.Add(coreEventChunk)
		if e.After(to) {
			e = to
		}
		// Point at t covers (t-1d, t]: evaluate at each day end.
		rs, err := src.QueryRange(ctx, expr("1d"), s.Add(24*time.Hour), e, 24*time.Hour)
		if err != nil {
			return n, err
		}
		put(rs, full)
	}
	if w := partialEnd.Sub(to); w >= time.Minute {
		rs, err := src.QueryRange(ctx, expr(fmt.Sprintf("%ds", int(w.Seconds()))), partialEnd, partialEnd, 24*time.Hour)
		if err != nil {
			return n, err
		}
		day := to.In(shanghai).Format("2006-01-02")
		put(rs, func(time.Time) string { return day })
	}
	return n, nil
}

func days(from, to time.Time) []string {
	out := []string{}
	for d := from; d.Before(to); d = d.Add(24 * time.Hour) {
		out = append(out, d.In(shanghai).Format("2006-01-02"))
	}
	return out
}

func prevDay(day string, n int) string {
	t, _ := time.ParseInLocation("2006-01-02", day, shanghai)
	return t.AddDate(0, 0, -n).Format("2006-01-02")
}

// DetectCoreEvents fetches [from-lookback, end) and returns the onsets whose day
// lies in [from, end). It is pure towards the database.
func DetectCoreEvents(ctx context.Context, src RangeQuerier, from, end time.Time) ([]api.GPUCoreEvent, int, error) {
	from = dayStart(from)
	if emit := CoreEventLedgerStart.AddDate(0, 0, 2); from.Before(emit) {
		from = emit
	}
	fetchFrom := from.AddDate(0, 0, -coreEventLookback)
	today := dayStart(end)
	codes := strings.Join(CoreXIDCodes, "|")
	gpuKey := func(m map[string]string) string { return strings.TrimSpace(m["UUID"]) }
	xidKey := func(m map[string]string) string {
		if m["UUID"] == "" || m["err_code"] == "" {
			return ""
		}
		return m["UUID"] + "|" + m["err_code"]
	}
	xid, urr, rrf := coreSeries{}, coreSeries{}, coreSeries{}
	total := 0
	for _, f := range []struct {
		expr  func(string) string
		key   func(map[string]string) string
		first bool
		out   coreSeries
	}{
		{func(w string) string {
			return `min by (UUID,err_code,host_ip,Hostname,modelName,pci_bus_id)(tfirst_over_time(DCGM_FI_DEV_XID_ERRORS{err_code=~"` + codes + `"}[` + w + `]))`
		}, xidKey, true, xid},
		{func(w string) string {
			return `max by (UUID,host_ip,Hostname,modelName,pci_bus_id)(max_over_time(DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS[` + w + `]))`
		}, gpuKey, false, urr},
		{func(w string) string {
			return `max by (UUID,host_ip,Hostname,modelName,pci_bus_id)(max_over_time(DCGM_FI_DEV_ROW_REMAP_FAILURE[` + w + `]))`
		}, gpuKey, false, rrf},
	} {
		n, err := coreFetch(ctx, src, f.expr, fetchFrom, today, end, f.key, f.first, f.out)
		total += n
		if err != nil {
			return nil, total, err
		}
	}
	window := days(from, today)
	if end.Sub(today) >= time.Minute {
		window = append(window, today.Format("2006-01-02"))
	}
	dayEnd := func(day string) time.Time {
		t, _ := time.ParseInLocation("2006-01-02", day, shanghai)
		t = t.AddDate(0, 0, 1)
		if t.After(end) {
			t = end
		}
		return t.UTC()
	}
	out := []api.GPUCoreEvent{}
	mk := func(o coreObs, code, day string, observed time.Time) api.GPUCoreEvent {
		m := o.labels
		ev := api.GPUCoreEvent{Source: CoreEventSourceVM, NodeIP: strings.TrimSpace(m["host_ip"]), Hostname: m["Hostname"], GPUUUID: m["UUID"], PCIBusID: m["pci_bus_id"], Model: m["modelName"], Code: code, Day: day, ObservedAt: observed.UTC().Truncate(time.Microsecond), Value: o.value}
		ev.Key, _ = digest([]any{CoreEventLedgerVersion, ev.Source, ev.GPUUUID, ev.Code, ev.Day})
		return ev
	}
	for k, byDay := range xid {
		code := "xid" + k[strings.LastIndex(k, "|")+1:]
		for _, d := range window {
			o, ok := byDay[d]
			if !ok {
				continue
			}
			if _, a := byDay[prevDay(d, 1)]; a {
				continue
			}
			if _, b := byDay[prevDay(d, 2)]; b {
				continue
			}
			at := o.first
			if at.IsZero() || at.After(dayEnd(d)) {
				at = dayEnd(d)
			}
			out = append(out, mk(o, code, d, at))
		}
	}
	prevObs := func(byDay map[string]coreObs, d string) (coreObs, bool) {
		for i := 1; i <= coreEventLookback; i++ {
			if o, ok := byDay[prevDay(d, i)]; ok {
				return o, true
			}
		}
		return coreObs{}, false
	}
	for _, byDay := range urr {
		for _, d := range window {
			o, ok := byDay[d]
			if p, has := prevObs(byDay, d); ok && has && o.value > p.value {
				out = append(out, mk(o, "urr_growth", d, dayEnd(d)))
			}
		}
	}
	for _, byDay := range rrf {
		for _, d := range window {
			o, ok := byDay[d]
			if !ok || o.value <= 0 {
				continue
			}
			if p, has := prevObs(byDay, d); !has || p.value <= 0 {
				out = append(out, mk(o, "remap_fail", d, dayEnd(d)))
			}
		}
	}
	keep := out[:0]
	for _, e := range out {
		if e.NodeIP != "" && e.GPUUUID != "" {
			keep = append(keep, e)
		}
	}
	sort.Slice(keep, func(i, j int) bool {
		if keep[i].Day != keep[j].Day {
			return keep[i].Day < keep[j].Day
		}
		return keep[i].Key < keep[j].Key
	})
	return keep, total, nil
}

// CoreEventsDaily imports new onsets from the VM source. It runs when the last
// completed import started before the newest frozen query cutoff, or more
// than coreEventRefresh ago; failed attempts are retried after coreEventRetry.
func CoreEventsDaily(ctx context.Context, db *gorm.DB, src RangeQuerier, now time.Time) error {
	if src == nil {
		return nil
	}
	db = db.WithContext(ctx)
	now = now.UTC().Truncate(time.Second)
	var last api.GPUCoreEventRun
	if e := db.Where("source = ?", CoreEventSourceVM).Order("started_at DESC,id DESC").Limit(1).Find(&last).Error; e != nil {
		return e
	}
	var done api.GPUCoreEventRun
	if e := db.Where("source = ? AND status = ?", CoreEventSourceVM, "completed").Order("window_end DESC,id DESC").Limit(1).Find(&done).Error; e != nil {
		return e
	}
	var q api.GPURiskQuery
	if e := db.Where("status = ?", "completed").Order("cutoff DESC,id DESC").Limit(1).Find(&q).Error; e != nil {
		return e
	}
	if last.ID != 0 && last.Status != "completed" && now.Sub(last.StartedAt) < coreEventRetry {
		return nil
	}
	if done.ID != 0 && now.Sub(done.WindowEnd) < coreEventRefresh && (q.ID == 0 || !done.WindowEnd.Before(q.Cutoff)) {
		return nil
	}
	from := CoreEventLedgerStart
	if done.ID != 0 {
		from = done.WindowEnd.Add(-coreEventOverlap)
	}
	_, err := ImportCoreEvents(ctx, db, src, from, now)
	return err
}

// ImportCoreEvents detects and stores onsets in [from, end); idempotent.
func ImportCoreEvents(ctx context.Context, db *gorm.DB, src RangeQuerier, from, end time.Time) (api.GPUCoreEventRun, error) {
	db = db.WithContext(ctx)
	run := api.GPUCoreEventRun{Source: CoreEventSourceVM, WindowStart: dayStart(from).UTC(), WindowEnd: end.UTC(), StartedAt: time.Now().UTC(), Status: "running"}
	if err := db.Create(&run).Error; err != nil {
		return run, err
	}
	events, series, err := DetectCoreEvents(ctx, src, from, end)
	run.Series = series
	if err == nil {
		imported := time.Now().UTC().Truncate(time.Microsecond)
		keys := []string{}
		for i := range events {
			events[i].ImportedAt, events[i].RunID = imported, run.ID
			keys = append(keys, events[i].Key)
		}
		run.SHA, _ = digest([]any{CoreEventLedgerVersion, run.WindowStart, run.WindowEnd, keys})
		if len(events) > 0 {
			res := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).CreateInBatches(events, 200)
			err = res.Error
			run.NewEvents = int(res.RowsAffected)
		}
	}
	if err == nil {
		err = importNodeObservations(ctx, db, src, from, end)
	}
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	run.FinishedAt = time.Now().UTC()
	run.Status = "completed"
	if err != nil {
		run.Status, run.ErrorCode = "failed", "core_event_source_failed"
		msg := err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		run.Error = msg
	}
	if e := db.WithContext(save).Save(&run).Error; e != nil && err == nil {
		err = e
	}
	return run, err
}

// CoreEpisode is a node-level critical failure: core events on one node merged
// while consecutive event days are at most coreEventEpisode days apart.
type CoreEpisode struct {
	NodeIP string
	Start  time.Time // first event ObservedAt
	Last   time.Time // last event ObservedAt
	Codes  []string
	GPUs   int
	Events []uint
}

// CoreEpisodes builds episodes from events (any order).
func CoreEpisodes(events []api.GPUCoreEvent) map[string][]CoreEpisode {
	by := map[string][]api.GPUCoreEvent{}
	for _, e := range events {
		by[strings.TrimSpace(e.NodeIP)] = append(by[strings.TrimSpace(e.NodeIP)], e)
	}
	out := map[string][]CoreEpisode{}
	for ip, es := range by {
		sort.Slice(es, func(i, j int) bool {
			if es[i].Day != es[j].Day {
				return es[i].Day < es[j].Day
			}
			return es[i].ObservedAt.Before(es[j].ObservedAt)
		})
		var cur *CoreEpisode
		lastDay := ""
		codes, gpus := map[string]bool{}, map[string]bool{}
		flush := func() {
			if cur == nil {
				return
			}
			for c := range codes {
				cur.Codes = append(cur.Codes, c)
			}
			sort.Strings(cur.Codes)
			cur.GPUs = len(gpus)
			out[ip] = append(out[ip], *cur)
		}
		for _, e := range es {
			if cur == nil || dayGap(lastDay, e.Day) > coreEventEpisode {
				flush()
				cur = &CoreEpisode{NodeIP: ip, Start: e.ObservedAt}
				codes, gpus = map[string]bool{}, map[string]bool{}
			}
			if e.ObservedAt.Before(cur.Start) {
				cur.Start = e.ObservedAt
			}
			if e.ObservedAt.After(cur.Last) {
				cur.Last = e.ObservedAt
			}
			codes[e.Code] = true
			if e.PCIBusID != "" {
				gpus[NormPCI(e.PCIBusID)] = true
			} else if e.GPUUUID != "" {
				gpus[e.GPUUUID] = true
			}
			cur.Events = append(cur.Events, e.ID)
			lastDay = e.Day
		}
		flush()
	}
	return out
}

func dayGap(a, b string) int {
	ta, e1 := time.ParseInLocation("2006-01-02", a, shanghai)
	tb, e2 := time.ParseInLocation("2006-01-02", b, shanghai)
	if e1 != nil || e2 != nil {
		return 1 << 30
	}
	return int(tb.Sub(ta).Hours()/24 + 0.5)
}

// ErrCoreLedgerStale: no completed import reaches the query cutoff yet.
var ErrCoreLedgerStale = errors.New("gpu core-event ledger has not reached the query cutoff")

// coreLedgerReady reports the newest completed run covering cutoff.
func coreLedgerReady(tx *gorm.DB, cutoff time.Time) (api.GPUCoreEventRun, error) {
	var run api.GPUCoreEventRun
	if e := tx.Where("source = ? AND status = ?", CoreEventSourceVM, "completed").Order("window_end DESC,id DESC").Limit(1).Find(&run).Error; e != nil {
		return run, e
	}
	if run.ID == 0 || run.WindowEnd.Before(cutoff) {
		return run, ErrCoreLedgerStale
	}
	return run, nil
}

// importNodeObservations widens each node's first/last DCGM day over the
// window (the whole ledger history when the table is still empty).
func importNodeObservations(ctx context.Context, db *gorm.DB, src RangeQuerier, from, end time.Time) error {
	var n int64
	if e := db.Model(&api.GPUNodeObservation{}).Count(&n).Error; e != nil {
		return e
	}
	from = dayStart(from)
	if n == 0 || from.Before(CoreEventLedgerStart) {
		from = CoreEventLedgerStart
	}
	seen := coreSeries{}
	_, err := coreFetch(ctx, src, func(w string) string {
		return `count by (host_ip,modelName)(max by (host_ip,modelName,UUID)(max_over_time(DCGM_FI_DEV_GPU_TEMP[` + w + `])))`
	}, from, dayStart(end), end, func(m map[string]string) string { return strings.TrimSpace(m["host_ip"]) }, false, seen)
	if err != nil {
		return err
	}
	var old []api.GPUNodeObservation
	if e := db.Find(&old).Error; e != nil {
		return e
	}
	have := map[string]api.GPUNodeObservation{}
	for _, o := range old {
		have[o.NodeIP] = o
	}
	ips := make([]string, 0, len(seen))
	for ip := range seen {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	now := time.Now().UTC()
	for _, ip := range ips {
		o, ok := have[ip]
		changed := !ok
		if !ok {
			o = api.GPUNodeObservation{NodeIP: ip}
		}
		for day, obs := range seen[ip] {
			if o.FirstSeenDay == "" || day < o.FirstSeenDay {
				o.FirstSeenDay, changed = day, true
			}
			if day > o.LastSeenDay {
				o.LastSeenDay, changed = day, true
				if m := obs.labels["modelName"]; m != "" {
					o.Model = m
				}
			}
			if int(obs.value) > o.MaxGPUs {
				o.MaxGPUs, changed = int(obs.value), true
			}
		}
		if changed {
			o.UpdatedAt = now
			if e := db.Save(&o).Error; e != nil {
				return e
			}
		}
	}
	return nil
}

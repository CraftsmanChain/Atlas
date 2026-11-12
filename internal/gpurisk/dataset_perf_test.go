package gpurisk

import (
	"atlas/pkg/api"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func historyAtBefore(ctx context.Context, m api.GPURiskMember, q api.GPURiskQuery, evs []api.GPURiskEvidenceRevision, reviews []api.HardwareFaultFeedbackReview) (HistoryFeatures, error) {
	f := HistoryFeatures{Numeric: map[string]*float64{}, Categorical: map[string]*string{}, EvidenceSHAs: []string{}, Blockers: []string{}}
	for _, field := range CoreSchema() {
		if field.Dtype == "nullable_float64" {
			f.Numeric[field.Name] = nil
		}
		if field.Dtype == "nullable_category" {
			f.Categorical[field.Name] = nil
		}
	}
	host := m.EntityRef
	if host != "" {
		f.Categorical["host_id"] = &host
	}
	local := q.Cutoff.In(time.FixedZone("Asia/Shanghai", 8*3600))
	weekday := (int(local.Weekday()) + 6) % 7
	f.Numeric["weekday"] = numberPtr(float64(weekday))
	weekend := 0.0
	if weekday >= 5 {
		weekend = 1
	}
	f.Numeric["weekend"] = numberPtr(weekend)
	latest := map[string]api.GPURiskEvidenceRevision{}
	for _, e := range evs {
		if !e.AvailableAt.Before(q.Cutoff) {
			continue
		}
		key := e.Kind + ":" + e.Key
		if p, ok := latest[key]; !ok || e.Revision > p.Revision {
			latest[key] = e
		}
	}
	assertions := []api.GPURiskEvidenceRevision{}
	for _, e := range latest {
		assertions = append(assertions, e)
	}
	sort.Slice(assertions, func(i, j int) bool { return assertions[i].SHA < assertions[j].SHA })
	selected := map[uint]api.HardwareFaultFeedbackReview{}
	for _, r := range reviews {
		if !r.ReviewedAt.Before(q.Cutoff) || !r.CreatedAt.Before(q.Cutoff) {
			continue
		}
		old, ok := selected[r.FeedbackRequestID]
		if !ok || r.Revision > old.Revision {
			selected[r.FeedbackRequestID] = r
		}
	}
	excluded := map[string]bool{}
	for _, r := range selected {
		if r.Decision == "excluded_no_hardware" && r.EpisodeKey != "" {
			excluded[r.EpisodeKey] = true
		}
	}
	events := map[string]historyEvent{}
	conflict := false
	var intervals [][2]time.Time
	var incomplete [][2]time.Time
	for index, e := range assertions {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return f, err
			}
		}
		var v EvidenceInput
		if json.Unmarshal([]byte(e.Data), &v) != nil {
			return f, ErrInput
		}
		if v.EntityRef != m.EntityRef {
			continue
		}
		if v.Kind == "coverage" && v.Source == "hardware_fault_ledger" && !v.Complete {
			incomplete = append(incomplete, [2]time.Time{v.From, v.Through})
		}
		if v.Kind == "coverage" && v.Source == "hardware_fault_ledger" && v.Complete {
			end := v.Through
			if end.After(q.Cutoff) {
				end = q.Cutoff
			}
			if end.After(v.From) {
				intervals = append(intervals, [2]time.Time{v.From, end})
				f.EvidenceSHAs = append(f.EvidenceSHAs, e.SHA)
			}
		}
		if v.Kind != "identity" {
			continue
		}
		r, ok := selected[v.FeedbackID]
		if !ok || r.ReviewSHA256 != v.ReviewSHA || r.Decision != "confirmed_hardware" || r.EpisodeKey == "" || len(r.EvidenceKeys) == 0 {
			continue
		}
		a, b := reviewWindow(r)
		if a.IsZero() || !b.Before(q.Cutoff) || a.After(b) || v.From.After(a) || v.Through.Before(b) {
			continue
		}
		if excluded[r.EpisodeKey] {
			conflict = true
			continue
		}
		if prior, ok := events[r.EpisodeKey]; ok && (!prior.at.Equal(a) || !prior.through.Equal(b) || prior.kind != r.ConfirmedFaultType) {
			conflict = true
			continue
		}
		shas := append(events[r.EpisodeKey].sha, r.ReviewSHA256, e.SHA)
		events[r.EpisodeKey] = historyEvent{r.EpisodeKey, r.ConfirmedFaultType, a, b, shas}
	}
	sorted := []historyEvent{}
	for _, e := range events {
		sorted = append(sorted, e)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].at.Equal(sorted[j].at) {
			return sorted[i].key < sorted[j].key
		}
		return sorted[i].at.Before(sorted[j].at)
	})
	// Merge complete as-known source intervals; exposure ends at complete_through.
	// R3-C permits a bounded unknown tail [complete_through, cutoff] of at most
	// MaxSourceLag. It does not extend observed exposure or relax AvailableAt < cutoff.
	sort.Slice(intervals, func(i, j int) bool { return intervals[i][0].Before(intervals[j][0]) })
	var start, end time.Time
	for _, x := range intervals {
		if end.IsZero() || x[0].After(end) {
			start, end = x[0], x[1]
		} else if x[1].After(end) {
			end = x[1]
		}
	}
	for _, bad := range incomplete {
		if !end.IsZero() && bad[0].Before(end) && bad[1].After(start) {
			start = time.Time{}
			end = time.Time{}
			f.Blockers = append(f.Blockers, "history_coverage_conflict")
			break
		}
	}
	covered := !end.IsZero() && !end.Before(q.Cutoff.Add(-MaxSourceLag))
	f.HistoryUnknown = !covered
	if !end.IsZero() {
		days := end.Sub(start).Hours() / 24
		f.HistoryCoverageDays = numberPtr(days)
	}
	if !covered {
		f.Blockers = append(f.Blockers, "history_coverage_unknown")
	}
	// Counts represent observed, independently confirmed episodes; coverage is
	// separately exposed. Zero is known only under an explicit observation window.
	if len(sorted) > 0 || covered {
		f.Numeric["episode_count"] = numberPtr(float64(len(sorted)))
		recent := 0
		recentAmbiguous, intervalAmbiguous := false, false
		f.ByFaultType = map[string]int{}
		for _, e := range sorted {
			if !e.at.Equal(e.through) {
				intervalAmbiguous = true
			}
			if e.at.Before(q.Cutoff.Add(-30*24*time.Hour)) && !e.through.Before(q.Cutoff.Add(-30*24*time.Hour)) {
				recentAmbiguous = true
			}
			if !e.at.Before(q.Cutoff.Add(-30 * 24 * time.Hour)) {
				recent++
			}
			f.ByFaultType[e.kind]++
			f.EvidenceSHAs = append(f.EvidenceSHAs, e.sha...)
		}
		noHistory := len(sorted) == 0
		f.NoHistory = &noHistory
		if !recentAmbiguous && (len(sorted) > 0 || covered && !start.After(q.Cutoff.Add(-30*24*time.Hour))) {
			f.Numeric["episode_count_30d"] = numberPtr(float64(recent))
		}
		if len(sorted) > 0 && !recentAmbiguous {
			f.Numeric["recent_ratio"] = numberPtr(float64(recent) / float64(len(sorted)))
		} else if covered && !recentAmbiguous {
			f.Numeric["recent_ratio"] = numberPtr(0)
		}
		if len(sorted) >= 2 && !intervalAmbiguous {
			f.Numeric["mean_interval_days"] = numberPtr(sorted[len(sorted)-1].at.Sub(sorted[0].at).Hours() / 24 / float64(len(sorted)-1))
		}
		if f.HistoryCoverageDays != nil && *f.HistoryCoverageDays > 0 {
			observed := 0
			ambiguousExposure := false
			for _, e := range sorted {
				if e.at.Before(start) && !e.through.Before(start) || !e.at.After(end) && e.through.After(end) {
					ambiguousExposure = true
				}
				if !e.at.Before(start) && !e.through.After(end) {
					observed++
				}
			}
			if !ambiguousExposure {
				f.Numeric["frequency_per_day"] = numberPtr(float64(observed) / *f.HistoryCoverageDays)
			}
		}
	}
	if conflict {
		f.Blockers = append(f.Blockers, "historical_episode_conflict")
	}
	applyHistorySources(&f, m, q, assertions, sorted)
	sort.Strings(f.EvidenceSHAs)
	return f, nil
}

func historyPerfFixture() (api.GPURiskQuery, []api.GPURiskEvidenceRevision, []api.GPURiskMember) {
	q := api.GPURiskQuery{Cutoff: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}
	es := []api.GPURiskEvidenceRevision{}
	ms := []api.GPURiskMember{}
	for i := 0; i < 398; i++ {
		ms = append(ms, api.GPURiskMember{NodeID: uint(i + 1), EntityRef: fmt.Sprint("entity", i)})
	}
	for i := 0; i < 3000; i++ {
		v := EvidenceInput{Kind: "coverage", Source: "hardware_fault_ledger", EntityRef: ms[i%398].EntityRef, From: q.Cutoff.Add(-48 * time.Hour), Through: q.Cutoff.Add(-time.Hour), Complete: true}
		raw, _ := json.Marshal(v)
		es = append(es, api.GPURiskEvidenceRevision{Kind: "coverage", Key: fmt.Sprint(i), Revision: 1, SHA: fmt.Sprintf("%064d", i), AvailableAt: q.Cutoff.Add(-time.Hour), Data: string(raw)})
	}
	return q, es, ms
}
func BenchmarkHistoryBefore398x3000(b *testing.B) {
	q, es, ms := historyPerfFixture()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, m := range ms {
			if _, err := historyAtBefore(context.Background(), m, q, es, nil); err != nil {
				b.Fatal(err)
			}
		}
	}
}
func TestHistoryOracleBefore(t *testing.T) {
	q, es, ms := historyPerfFixture()
	for _, m := range ms[:3] {
		a, e := historyAtBefore(context.Background(), m, q, es, nil)
		z, f := historyAt(context.Background(), m, q, es, nil)
		if !reflect.DeepEqual(a, z) || e != f {
			t.Fatal("feature or error changed")
		}
	}
}

func BenchmarkHistoryPrepared398x3000(b *testing.B) {
	q, es, ms := historyPerfFixture()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		view := prepareHistoryView(q, es, nil)
		for _, m := range ms {
			if _, err := historyAtView(context.Background(), m, q, nil, nil, view); err != nil {
				b.Fatal(err)
			}
		}
	}
}
func TestHistoryPreparedOracleBoundaries(t *testing.T) {
	q, es, ms := historyPerfFixture()
	for _, scenario := range []string{"normal", "equal_cutoff", "revision", "malformed_other_entity", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			evs := append([]api.GPURiskEvidenceRevision(nil), es...)
			switch scenario {
			case "equal_cutoff":
				evs[0].AvailableAt = q.Cutoff
			case "revision":
				e := evs[0]
				e.Revision = 2
				e.Data = `{"kind":"coverage","entity_ref":"entity0","source":"hardware_fault_ledger","complete":false}`
				evs = append(evs, e)
			case "malformed_other_entity":
				evs[len(evs)-1].Data = "{"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			view := prepareHistoryView(q, evs, nil)
			for _, m := range ms[:8] {
				old, e := historyAtBefore(ctx, m, q, evs, nil)
				got, f := historyAtView(ctx, m, q, nil, nil, view)
				if e != f || !reflect.DeepEqual(old, got) {
					t.Fatalf("oracle mismatch %s: %v %v", scenario, e, f)
				}
			}
		})
	}
}

// This fixture contains real review/identity/state shapes but no production data.
func historyReviewedFixture() (api.GPURiskQuery, api.GPURiskMember, []api.GPURiskEvidenceRevision, []api.HardwareFaultFeedbackReview) {
	q := api.GPURiskQuery{Cutoff: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	m := api.GPURiskMember{NodeID: 1, EntityRef: "serial:sn-1"}
	onset := q.Cutoff.Add(-48 * time.Hour)
	old := q.Cutoff.Add(-time.Hour)
	r := api.HardwareFaultFeedbackReview{FeedbackRequestID: 1, Revision: 1, Decision: "confirmed_hardware", EpisodeKey: "episode-1", ConfirmedFaultType: "gpu_hardware_failure", ConfirmedOnsetAt: &onset, EvidenceKeys: api.StringList{"proof"}, ReviewSHA256: strings.Repeat("a", 64), ReviewedAt: old, CreatedAt: old}
	inputs := []EvidenceInput{
		{Kind: "coverage", Key: "coverage", Source: "hardware_fault_ledger", EntityRef: m.EntityRef, NodeID: 1, From: q.Cutoff.Add(-60 * 24 * time.Hour), Through: old, Complete: true},
		{Kind: "identity", Key: "identity", EntityRef: m.EntityRef, NodeID: 1, FeedbackID: 1, ReviewSHA: r.ReviewSHA256, From: onset.Add(-time.Hour), Through: onset.Add(time.Hour)},
		{Kind: "episode_state", Key: "state", EntityRef: m.EntityRef, NodeID: 1, FeedbackID: 1, ReviewSHA: r.ReviewSHA256, SourceVersion: HistorySourceVersion, State: "unresolved", Severity: "critical", From: onset, Through: old},
		{Kind: "host_context", Key: "context", EntityRef: m.EntityRef, NodeID: 1, SourceVersion: HistorySourceVersion, State: "active", From: onset, Through: old, Context: &HostContext{GPUModel: "fixture-gpu"}},
	}
	es := make([]api.GPURiskEvidenceRevision, 0, len(inputs))
	for i, v := range inputs {
		raw, _ := json.Marshal(v)
		es = append(es, api.GPURiskEvidenceRevision{Kind: v.Kind, Key: v.Key, Revision: 1, SHA: fmt.Sprintf("%064d", i+1), AvailableAt: old, Data: string(raw)})
	}
	return q, m, es, []api.HardwareFaultFeedbackReview{r}
}

func TestHistoryPreparedReviewedBoundaryOracle(t *testing.T) {
	for _, scenario := range []string{"base", "reviewed_equal", "created_equal", "identity_available_equal", "onset_equal", "state_available_equal", "state_from_equal", "state_through_equal", "state_through_after", "state_withdrawn", "withdrawal_at_cutoff", "review_withdrawn", "episode_conflict", "context_conflict"} {
		t.Run(scenario, func(t *testing.T) {
			q, m, es, rs := historyReviewedFixture()
			change := func(index int, fn func(*EvidenceInput)) {
				var v EvidenceInput
				if err := json.Unmarshal([]byte(es[index].Data), &v); err != nil {
					t.Fatal(err)
				}
				fn(&v)
				raw, _ := json.Marshal(v)
				es[index].Data = string(raw)
			}
			switch scenario {
			case "reviewed_equal":
				rs[0].ReviewedAt = q.Cutoff
			case "created_equal":
				rs[0].CreatedAt = q.Cutoff
			case "identity_available_equal":
				es[1].AvailableAt = q.Cutoff
			case "onset_equal":
				rs[0].ConfirmedOnsetAt = &q.Cutoff
				change(1, func(v *EvidenceInput) { v.Through = q.Cutoff })
			case "state_available_equal":
				es[2].AvailableAt = q.Cutoff
			case "state_from_equal":
				change(2, func(v *EvidenceInput) { v.From = q.Cutoff; v.Through = q.Cutoff })
			case "state_through_equal":
				change(2, func(v *EvidenceInput) { v.Through = q.Cutoff })
			case "state_through_after":
				change(2, func(v *EvidenceInput) { v.Through = q.Cutoff.Add(time.Nanosecond) })
			case "state_withdrawn", "withdrawal_at_cutoff":
				e := es[2]
				e.Revision = 2
				e.SHA = strings.Repeat("b", 64)
				es = append(es, e)
				change(4, func(v *EvidenceInput) { v.State = "withdrawn"; v.Severity = "" })
				if scenario == "withdrawal_at_cutoff" {
					es[4].AvailableAt = q.Cutoff
				}
			case "review_withdrawn":
				r := rs[0]
				r.Revision = 2
				r.Decision = "excluded_no_hardware"
				r.ReviewSHA256 = strings.Repeat("b", 64)
				rs = append(rs, r)
			case "episode_conflict":
				r := rs[0]
				r.FeedbackRequestID = 2
				r.ReviewSHA256 = strings.Repeat("b", 64)
				a := r.ConfirmedOnsetAt.Add(-time.Hour)
				r.ConfirmedOnsetAt = &a
				rs = append(rs, r)
				e := es[1]
				e.Key = "other-identity"
				e.SHA = strings.Repeat("c", 64)
				es = append(es, e)
				change(4, func(v *EvidenceInput) { v.FeedbackID = 2; v.ReviewSHA = r.ReviewSHA256; v.From = a.Add(-time.Hour) })
			case "context_conflict":
				e := es[3]
				e.Key = "other-context"
				e.SHA = strings.Repeat("c", 64)
				es = append(es, e)
				change(4, func(v *EvidenceInput) { v.Context.GPUModel = "other-gpu" })
			}
			old, e := historyAtBefore(context.Background(), m, q, es, rs)
			view := prepareHistoryView(q, es, rs)
			got, f := historyAtView(context.Background(), m, q, nil, nil, view)
			if e != f || !reflect.DeepEqual(old, got) {
				t.Fatalf("old/new mismatch: %v / %v\n%+v\n%+v", e, f, old, got)
			}
			count := got.Numeric["episode_count"]
			absent := scenario == "reviewed_equal" || scenario == "created_equal" || scenario == "identity_available_equal" || scenario == "onset_equal" || scenario == "review_withdrawn"
			want := 1.0
			if absent {
				want = 0
			}
			if count == nil || *count != want {
				t.Fatalf("episode count %v, want %v", count, want)
			}
			unknown := absent || scenario == "state_available_equal" || scenario == "state_through_after" || scenario == "state_withdrawn"
			// Zero observed episodes under coverage has a known zero unresolved count.
			if absent {
				unknown = false
			}
			if (got.Numeric["unresolved_count"] == nil) != unknown {
				t.Fatalf("unexpected state availability: %+v", got)
			}
			if scenario == "episode_conflict" && !contains(got.Blockers, "historical_episode_conflict") {
				t.Fatal("missing conflict")
			}
			if scenario == "context_conflict" && !contains(got.Blockers, "host_context_conflict:gpu_model") {
				t.Fatal("missing context conflict")
			}
			// A prepared view is reused only sequentially; a second member cannot inherit
			// a prior member's events/context or mutate its returned feature maps.
			other := m
			other.NodeID = 2
			other.EntityRef = "serial:sn-2"
			a, x := historyAtBefore(context.Background(), other, q, es, rs)
			b, y := historyAtView(context.Background(), other, q, nil, nil, view)
			if x != y || !reflect.DeepEqual(a, b) {
				t.Fatal("member cross-contamination")
			}
		})
	}
}

type historyCancelAfter struct {
	context.Context
	calls int
}

func (c *historyCancelAfter) Err() error {
	c.calls++
	if c.calls >= 2 {
		return context.Canceled
	}
	return nil
}
func TestHistoryPreparedCancellationErrorOrder(t *testing.T) {
	q, es, ms := historyPerfFixture()
	for _, bad := range []int{255, 256, 257} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			evs := append([]api.GPURiskEvidenceRevision(nil), es...)
			evs[bad].Data = "{"
			view := prepareHistoryView(q, evs, nil)
			old, e := historyAtBefore(&historyCancelAfter{Context: context.Background()}, ms[0], q, evs, nil)
			got, f := historyAtView(&historyCancelAfter{Context: context.Background()}, ms[0], q, nil, nil, view)
			if !errors.Is(e, f) || !reflect.DeepEqual(old, got) {
				t.Fatalf("error/partial features order changed: %v / %v", e, f)
			}
			// A later retry reuses only parsed entries and still reports the old malformed error.
			a, x := historyAtBefore(context.Background(), ms[0], q, evs, nil)
			b, y := historyAtView(context.Background(), ms[0], q, nil, nil, view)
			if x != y || !reflect.DeepEqual(a, b) {
				t.Fatal("cancelled view retry changed")
			}
		})
	}
	view := prepareHistoryView(q, es, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := historyAtView(ctx, ms[0], q, nil, nil, view); err != context.Canceled || len(view.decoded) != 0 {
		t.Fatal("cancelled call decoded evidence", err, len(view.decoded))
	}
}

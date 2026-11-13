package gpurisk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
)

// P1 taskcard §4 item 5: v4 ledger lag boundary.
func TestHistoryV4LedgerLagBoundary(t *testing.T) {
	cutoff := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	q := api.GPURiskQuery{ID: 9, Cutoff: cutoff}
	m := api.GPURiskMember{QueryID: 9, NodeID: 1, EntityRef: "serial:sn-1", Status: "included"}
	ledger := func(through, available time.Time) []api.GPURiskEvidenceRevision {
		v := EvidenceInput{Kind: "coverage", Key: "ledger", Source: "hardware_fault_ledger", Complete: true, EntityRef: m.EntityRef, NodeID: 1, QueryID: 1, From: cutoff.Add(-40 * 24 * time.Hour), Through: through}
		raw, _ := json.Marshal(v)
		return []api.GPURiskEvidenceRevision{{ID: 1, Kind: "coverage", Key: "ledger", Revision: 1, AvailableAt: available, Data: string(raw), SHA: strings.Repeat("a", 64)}}
	}
	run := func(evs []api.GPURiskEvidenceRevision, lag time.Duration) (HistoryFeatures, time.Time) {
		f, end, e := historyAtViewLag(context.Background(), m, q, evs, nil, nil, lag)
		if e != nil {
			t.Fatal(e)
		}
		return f, end
	}
	exact := cutoff.Add(-LedgerMaxLag)
	f, end := run(ledger(exact, cutoff.Add(-time.Hour)), LedgerMaxLag)
	if f.HistoryUnknown || contains(f.Blockers, "history_coverage_unknown") || !end.Equal(exact) || f.Numeric["episode_count"] == nil {
		t.Fatal("cutoff-8d must be covered under v4", f, end)
	}
	f, _ = run(ledger(exact.Add(-time.Microsecond), cutoff.Add(-time.Hour)), LedgerMaxLag)
	if !f.HistoryUnknown || !contains(f.Blockers, "history_coverage_unknown") {
		t.Fatal("cutoff-8d-1us must be unknown under v4", f)
	}
	// AvailableAt == cutoff is excluded (AvailableAt < cutoff unchanged).
	f, end = run(ledger(exact, cutoff), LedgerMaxLag)
	if !f.HistoryUnknown || !end.IsZero() || len(f.EvidenceSHAs) != 0 {
		t.Fatal("assertion available at cutoff leaked", f, end)
	}
	// The same 8d-old watermark stays unknown under the v2 24h tail.
	f, _ = run(ledger(exact, cutoff.Add(-time.Hour)), MaxSourceLag)
	if !f.HistoryUnknown {
		t.Fatal("v2 lag changed", f)
	}
	v2, e := historyAt(context.Background(), m, q, ledger(cutoff.Add(-MaxSourceLag), cutoff.Add(-time.Hour)), nil)
	if e != nil || v2.HistoryUnknown {
		t.Fatal("v2 boundary changed", v2, e)
	}
}

// §4.5/§4.6: BuildDataset v4 is explicit; v2 default is byte-identical to the
// frozen pre-change implementation; row metadata is not a core feature.
func TestDatasetHistoryContractV4ExplicitAndV2Unchanged(t *testing.T) {
	db, q, _ := outcomeDB(t)
	// Fixture historical ledger coverage known before the cutoff and ending
	// 3 days before it (inside 8d, outside 24h).
	var members []api.GPURiskMember
	db.Where("query_id = ?", q.ID).Order("node_id").Find(&members)
	if len(members) != 2 {
		t.Fatal(members)
	}
	through := q.Cutoff.Add(-72 * time.Hour)
	for _, m := range members {
		r := appendInput(t, db, EvidenceInput{Kind: "coverage", Key: fmt.Sprint("history-ledger-", m.NodeID), Owner: "fixture", EvidenceSHA: strings.Repeat("a", 64), NodeID: m.NodeID, QueryID: q.ID, EntityRef: m.EntityRef, From: q.Cutoff.Add(-40 * 24 * time.Hour), Through: through, Source: "hardware_fault_ledger", Complete: true})
		db.Model(&api.GPURiskEvidenceRevision{}).Where("id = ?", r.ID).Update("available_at", q.Cutoff.Add(-48*time.Hour))
	}
	b, e := RecordOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	spec := datasetSpec(q, b)
	v2, e := BuildDataset(context.Background(), db, spec)
	if e != nil {
		t.Fatal(e)
	}
	before, e := buildDatasetBefore(context.Background(), db, spec)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(v2)
	z, _ := json.Marshal(before)
	if !bytes.Equal(a, z) || v2.SHA != before.SHA || v2.FeatureSHA != before.FeatureSHA || v2.SplitSHA != before.SplitSHA || v2.InputSHA != before.InputSHA {
		t.Fatal("v2 default output changed")
	}
	for _, key := range []string{"history_contract", "metadata", "history_ledger_lag_hours"} {
		if bytes.Contains(a, []byte(`"`+key)) {
			t.Fatal("v2 output gained key", key)
		}
	}
	if v2.Version != DatasetVersion || v2.FeatureContract != HistoryFeatureVersion || v2.Counts["history_unknown"] != 2 {
		t.Fatal("v2 semantics", v2.Version, v2.Counts)
	}
	spec.HistoryContract = HistoryContractV4
	v4, e := BuildDataset(context.Background(), db, spec)
	if e != nil {
		t.Fatal(e)
	}
	again, e := BuildDataset(context.Background(), db, spec)
	if e != nil || again.SHA != v4.SHA {
		t.Fatal("v4 not deterministic", e)
	}
	if v4.Version != DatasetVersionV4 || v4.FeatureContract != HistoryFeatureVersionV4 || v4.Version == AvailableDatasetVersion || v4.HistoryContract == nil || v4.HistoryContract.LedgerMaxLagHours != 192 || v4.HistoryContract.SourceMaxLagHours != 24 || v4.HistoryContract.CoreFeatureChanged {
		t.Fatal("v4 manifest", v4.Version, v4.HistoryContract)
	}
	if v4.Counts["history_unknown"] != 0 || v4.SHA == v2.SHA {
		t.Fatal("v4 lag not applied", v4.Counts)
	}
	for _, r := range v4.Rows {
		if r.Metadata == nil || r.Metadata.HistoryLedgerLagHours == nil || *r.Metadata.HistoryLedgerLagHours != 72 {
			t.Fatal("row metadata", r.Metadata)
		}
		if _, ok := r.Features.Numeric["history_ledger_lag_hours"]; ok {
			t.Fatal("metadata leaked into core features")
		}
	}
	if len(v4.Schema) != len(CoreSchema()) {
		t.Fatal("core schema changed")
	}
	if _, e := coreFeatureRows(v4); e != nil {
		t.Fatal("v4 rows violate core feature contract", e)
	}
	for _, bad := range []string{"v2", "v3", "V4"} {
		spec.HistoryContract = bad
		if _, e := BuildDataset(context.Background(), db, spec); !errors.Is(e, ErrInput) {
			t.Fatal("unknown history contract accepted", bad, e)
		}
	}
	spec.HistoryContract = HistoryContractV4
	spec.Profile = AvailableContextProfile
	if _, e := BuildDataset(context.Background(), db, spec); !errors.Is(e, ErrInput) {
		t.Fatal("v4 with available-context profile accepted", e)
	}
}

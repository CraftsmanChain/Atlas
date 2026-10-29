package gpurisk

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// Use the existing server clock seam: all persisted evidence is validated and
// timestamped by AppendEvidence. No immutable revision is inserted or updated.
func appendBoundary(t *testing.T, db *gorm.DB, at time.Time, v EvidenceInput) api.GPURiskEvidenceRevision {
	t.Helper()
	previous := evidenceNow
	evidenceNow = func() time.Time { return at }
	defer func() { evidenceNow = previous }()
	r := appendInput(t, db, v)
	if !r.AvailableAt.Equal(at) {
		t.Fatalf("server timestamp = %v, want %v", r.AvailableAt, at)
	}
	return r
}

func assertBoundaryHistory(t *testing.T, f HistoryFeatures, sha string, unknown, included bool) {
	t.Helper()
	if f.HistoryUnknown != unknown || contains(f.Blockers, "history_coverage_unknown") != unknown || contains(f.EvidenceSHAs, sha) != included {
		t.Fatalf("want unknown/blocker=%v SHA=%v; got unknown=%v blockers=%v SHAs=%v", unknown, included, f.HistoryUnknown, f.Blockers, f.EvidenceSHAs)
	}
	t.Logf("actual HistoryUnknown=%v coverage_unknown=%v SHA_in_history=%v", f.HistoryUnknown, contains(f.Blockers, "history_coverage_unknown"), contains(f.EvidenceSHAs, sha))
}

func TestC3C4CoverageBoundaryMatrix(t *testing.T) {
	db, q, _ := outcomeDB(t)
	c := q.Cutoff
	us := time.Microsecond
	for _, tc := range []struct {
		name                     string
		from, through, available time.Time
		unknown, sha             bool
	}{
		{"available_before", c.Add(-40 * 24 * time.Hour), c.Add(-time.Hour), c.Add(-us), false, true},
		{"available_equal", c.Add(-40 * 24 * time.Hour), c.Add(-time.Hour), c, true, false},
		{"available_after", c.Add(-40 * 24 * time.Hour), c.Add(-time.Hour), c.Add(us), true, false},
		{"lag_minus_1us", c.Add(-40 * 24 * time.Hour), c.Add(-MaxSourceLag - us), c.Add(-us), true, true},
		{"lag_equal", c.Add(-40 * 24 * time.Hour), c.Add(-MaxSourceLag), c.Add(-us), false, true},
		{"lag_plus_1us", c.Add(-40 * 24 * time.Hour), c.Add(-MaxSourceLag + us), c.Add(-us), false, true},
		{"through_before", c.Add(-time.Hour), c.Add(-us), c.Add(-us), false, true},
		// Through>=cutoff implies AvailableAt>=cutoff under the ingestion contract.
		{"through_equal", c.Add(-time.Hour), c, c, true, false},
		{"through_crosses", c.Add(-time.Hour), c.Add(us), c.Add(us), true, false},
		{"from_equal", c, c.Add(time.Hour), c.Add(time.Hour), true, false},
		{"from_after", c.Add(us), c.Add(time.Hour), c.Add(time.Hour), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := EvidenceInput{Kind: "coverage", Key: tc.name, Owner: "fixture", EvidenceSHA: stringSHA(), NodeID: 1, QueryID: q.ID, EntityRef: "serial:sn-1", Source: "hardware_fault_ledger", Complete: true, From: tc.from, Through: tc.through}
			e := appendBoundary(t, db, tc.available, v)
			f, err := historyAt(context.Background(), api.GPURiskMember{NodeID: 1, EntityRef: v.EntityRef}, q, []api.GPURiskEvidenceRevision{e}, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertBoundaryHistory(t, f, e.SHA, tc.unknown, tc.sha)
			// The same partial historical interval never covers the future 7d window.
			_, rows := evaluate(t, db, q)
			if rows[0].Status != "pending_watermark" || rows[0].Positive != nil || contains(rows[0].EvidenceSHAs, e.SHA) {
				t.Fatal("partial interval became future coverage", rows[0])
			}
			t.Log("actual outcome=pending_watermark SHA_in_outcome=false")
		})
	}
}

func TestC3EqualAvailabilityRejectsOtherwiseUsableEvidence(t *testing.T) {
	db, q, _ := outcomeDB(t)
	v := EvidenceInput{Kind: "coverage", Key: "counterexample", Owner: "fixture", EvidenceSHA: stringSHA(), NodeID: 1, QueryID: q.ID, EntityRef: "serial:sn-1", Source: "hardware_fault_ledger", Complete: true, From: q.Cutoff.Add(-40 * 24 * time.Hour), Through: q.Cutoff.Add(-time.Hour)}
	e := appendBoundary(t, db, q.Cutoff, v)
	m := api.GPURiskMember{NodeID: 1, EntityRef: v.EntityRef}
	f, err := historyAt(context.Background(), m, q, []api.GPURiskEvidenceRevision{e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertBoundaryHistory(t, f, e.SHA, true, false)
	if f.Numeric["episode_count"] != nil || f.HistoryCoverageDays != nil || f.NoHistory != nil {
		t.Fatal("equal-cutoff evidence supplied features", f)
	}
	// Positive control moves only the evaluation cutoff; the stored revision stays immutable.
	q.Cutoff = q.Cutoff.Add(time.Microsecond)
	f, err = historyAt(context.Background(), m, q, []api.GPURiskEvidenceRevision{e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertBoundaryHistory(t, f, e.SHA, false, true)
}

func TestC3C4IdentityWindowMatrix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		a, b       time.Duration
		history    bool
		outcome    string
		outcomeSHA bool
	}{
		{"b_before", -2 * time.Hour, -time.Hour, true, "pending_watermark", false},
		{"b_equal", -time.Hour, 0, false, "pending_watermark", false},
		{"crosses", -time.Hour, time.Hour, false, "ambiguous", true},
		{"a_equal", 0, time.Hour, false, "ambiguous", true},
		{"a_after", time.Microsecond, time.Hour, false, "matured_positive", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, q, _ := outcomeDB(t)
			a, b := q.Cutoff.Add(tc.a), q.Cutoff.Add(tc.b)
			old := q.Cutoff.Add(-3 * time.Hour)
			review := api.HardwareFaultFeedbackReview{FeedbackRequestID: 1, Revision: 1, Decision: "confirmed_hardware", EpisodeKey: "boundary", EvidenceKeys: api.StringList{"fixture"}, ReviewSHA256: stringSHA(), CreatedAt: old, ReviewedAt: old, ConfirmedWindowStartAt: &a, ConfirmedWindowEndAt: &b}
			if err := db.Create(&review).Error; err != nil {
				t.Fatal(err)
			}
			v := EvidenceInput{Kind: "identity", Key: tc.name, Owner: "fixture", EvidenceSHA: stringSHA(), NodeID: 1, EntityRef: "serial:sn-1", FeedbackID: 1, ReviewSHA: review.ReviewSHA256, From: a, Through: b}
			persisted := appendBoundary(t, db, q.Cutoff.Add(2*time.Hour), v)
			// Pure projection fixture isolates the window predicate from availability.
			// This copy is never written to the DB; the outcome uses the appended revision.
			projection := persisted
			projection.AvailableAt = q.Cutoff.Add(-time.Microsecond)
			for _, covered := range []bool{false, true} {
				t.Run(fmt.Sprint("covered=", covered), func(t *testing.T) {
					evs := []api.GPURiskEvidenceRevision{projection}
					if covered {
						cv := EvidenceInput{Kind: "coverage", EntityRef: v.EntityRef, Source: "hardware_fault_ledger", Complete: true, From: q.Cutoff.Add(-40 * 24 * time.Hour), Through: q.Cutoff.Add(-time.Hour)}
						data, err := json.Marshal(cv)
						if err != nil {
							t.Fatal(err)
						}
						evs = append(evs, api.GPURiskEvidenceRevision{Kind: "coverage", Key: "background", Revision: 1, SHA: fmt.Sprintf("%064x", 2), AvailableAt: old, Data: string(data)})
					}
					f, err := historyAt(context.Background(), api.GPURiskMember{NodeID: 1, EntityRef: v.EntityRef}, q, evs, []api.HardwareFaultFeedbackReview{review})
					if err != nil {
						t.Fatal(err)
					}
					assertBoundaryHistory(t, f, projection.SHA, !covered, tc.history)
					if contains(f.EvidenceSHAs, review.ReviewSHA256) != tc.history {
						t.Fatal("review SHA mismatch", f)
					}
					count := f.Numeric["episode_count"]
					if tc.history && (count == nil || *count != 1) || !tc.history && !covered && count != nil || !tc.history && covered && (count == nil || *count != 0) {
						t.Fatal("incorrect episode count", f)
					}
				})
			}
			_, rows := evaluate(t, db, q)
			if rows[0].Status != tc.outcome || contains(rows[0].EvidenceSHAs, persisted.SHA) != tc.outcomeSHA {
				t.Fatal(rows[0])
			}
			if (rows[0].Positive != nil) != (tc.outcome == "matured_positive") {
				t.Fatal(rows[0])
			}
			t.Logf("actual outcome=%s SHA_in_outcome=%v", rows[0].Status, contains(rows[0].EvidenceSHAs, persisted.SHA))
		})
	}
}

func TestC4FutureCoverageIntervalMatrix(t *testing.T) {
	for _, tc := range []struct {
		name          string
		from, through time.Duration
		accepted      bool
	}{
		{"full", 0, 0, true}, {"starts_before", -time.Microsecond, 0, true}, {"starts_after", time.Microsecond, 0, false}, {"ends_before", 0, -time.Microsecond, false}, {"ends_after", 0, time.Microsecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, q, _ := outcomeDB(t)
			var sha string
			for _, source := range []string{"hardware_fault_ledger", "monitoring", "identity"} {
				from, through := q.Cutoff, q.Cutoff.Add(7*24*time.Hour)
				if source == "hardware_fault_ledger" {
					from = from.Add(tc.from)
					through = through.Add(tc.through)
				}
				e := appendBoundary(t, db, q.Cutoff.Add(8*24*time.Hour), EvidenceInput{Kind: "coverage", Key: source, Owner: "fixture", EvidenceSHA: stringSHA(), NodeID: 1, QueryID: q.ID, EntityRef: "serial:sn-1", Source: source, Complete: true, From: from, Through: through})
				if source == "hardware_fault_ledger" {
					sha = e.SHA
				}
			}
			var evs []api.GPURiskEvidenceRevision
			if err := db.Find(&evs).Error; err != nil {
				t.Fatal(err)
			}
			f, err := historyAt(context.Background(), api.GPURiskMember{NodeID: 1, EntityRef: "serial:sn-1"}, q, evs, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertBoundaryHistory(t, f, sha, true, false)
			_, rows := evaluate(t, db, q)
			want := "pending_watermark"
			if tc.accepted {
				want = "matured_negative"
			}
			if rows[0].Status != want || contains(rows[0].EvidenceSHAs, sha) != tc.accepted {
				t.Fatal(rows[0])
			}
			t.Logf("actual outcome=%s SHA_in_outcome=%v", rows[0].Status, contains(rows[0].EvidenceSHAs, sha))
		})
	}
}

func TestC4DatasetMemberDenominatorReconciliation(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&api.GPUNode{}, &api.HardwareFaultFeedbackRequest{}, &api.HardwareFaultFeedbackReview{}); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-20 * 24 * time.Hour).Truncate(time.Microsecond)
	if err := db.Callback().Create().Before("gorm:create").Register("c4_query_clock", func(tx *gorm.DB) {
		if q, ok := tx.Statement.Dest.(*api.GPURiskQuery); ok {
			at := cutoff
			q.CompletedAt = &at
		}
	}); err != nil {
		t.Fatal(err)
	}
	p := fixture(t, 3)
	p.Cutoff = cutoff
	for i := range p.Nodes {
		n := &p.Nodes[i]
		n.AvailableAt = cutoff.Add(-time.Hour)
		for probe := 0; ; probe++ {
			uuid := fmt.Sprintf("C4-%d-%d", n.ID, probe)
			if componentSplit("gpu:"+uuid) == "train" {
				n.Cards = []Card{{AssetID: n.ID, UUID: uuid, State: "active"}}
				break
			}
		}
	}
	score := 80
	p.Nodes[0].Cards[0].Score = &score
	p.Nodes[0].Cards[0].HealthID = 1
	p.Nodes[0].Cards[0].RunID = 1
	p.Nodes[0].Cards[0].Rule = "fixture"
	p.Nodes[0].Cards[0].AvailableAt = cutoff.Add(-time.Hour)
	p.Nodes[0].Cards[0].EvaluatedAt = cutoff.Add(-time.Hour)
	q, err := Record(context.Background(), db, reseal(t, p))
	if err != nil {
		t.Fatal(err)
	}
	appendBoundary(t, db, cutoff.Add(-time.Hour), EvidenceInput{Kind: "coverage", Key: "history", Owner: "fixture", EvidenceSHA: stringSHA(), NodeID: 1, QueryID: q.ID, EntityRef: "serial:sn-1", Source: "hardware_fault_ledger", Complete: true, From: cutoff.Add(-40 * 24 * time.Hour), Through: cutoff.Add(-time.Hour)})
	for _, node := range []uint{1, 3} {
		for _, source := range []string{"hardware_fault_ledger", "monitoring", "identity"} {
			appendBoundary(t, db, cutoff.Add(8*24*time.Hour), EvidenceInput{Kind: "coverage", Key: fmt.Sprintf("%d-%s", node, source), Owner: "fixture", EvidenceSHA: stringSHA(), NodeID: node, QueryID: q.ID, EntityRef: fmt.Sprintf("serial:sn-%d", node), Source: source, Complete: true, From: cutoff, Through: cutoff.Add(7 * 24 * time.Hour)})
		}
	}
	batch, err := RecordOutcomes(context.Background(), db, q.ID, cutoff.Add(8*24*time.Hour+time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	spec := DatasetSpec{BatchIDs: []uint{batch.ID}, Anchor: cutoff, TrainEnd: cutoff.Add(10 * 24 * time.Hour), ValidationEnd: cutoff.Add(12 * 24 * time.Hour), TestEnd: cutoff.Add(14 * 24 * time.Hour), Profile: "paper_core"}
	d, err := BuildDataset(context.Background(), db, spec)
	if err != nil {
		t.Fatal(err)
	}
	// Pin the original member identities as well as all totals. Recounting only
	// surviving rows would miss a dropped unknown/no-signal member.
	if len(d.Rows) != 3 || d.Counts["rows"] != q.MemberCount {
		t.Fatalf("lost denominator: members=%d rows=%v counts=%v", q.MemberCount, d.Rows, d.Counts)
	}
	for i, r := range d.Rows {
		if r.QueryID != q.ID || r.NodeID != uint(i+1) || r.Generation != p.Nodes[i].Generation {
			t.Fatal("missing or duplicated member", d.Rows)
		}
		wantUnknown := r.NodeID != 1
		if r.Features.HistoryUnknown != wantUnknown || contains(r.Features.Blockers, "history_coverage_unknown") != wantUnknown {
			t.Fatal(r)
		}
		if r.Eligible != (r.NodeID != 2) || (r.Label == nil) != (r.NodeID == 2) {
			t.Fatal(r)
		}
	}
	for key, want := range map[string]int{"rows": 3, "queries": 1, "eligible": 2, "excluded": 1, "train_eligible": 2, "unknown": 1, "history_unknown": 2, "no_signal": 2} {
		got, present := d.Counts[key]
		if !present || got != want {
			t.Fatalf("%s=%d present=%v, want %d", key, got, present, want)
		}
	}
	eligible := d.Counts["train_eligible"] + d.Counts["validation_eligible"] + d.Counts["test_eligible"]
	if eligible != d.Counts["eligible"] || eligible+d.Counts["excluded"] != d.Counts["rows"] {
		t.Fatal("unreconciled denominator", d.Counts)
	}
	t.Logf("actual members=%d rows=%d counts=%v", q.MemberCount, len(d.Rows), d.Counts)
}

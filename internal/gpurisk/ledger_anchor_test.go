package gpurisk

import (
	"context"
	"errors"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// P1 round 2: a ledger declaration whose through lies before the newest query
// cutoff must still produce non-degenerate member coverage, on the ANCHOR
// query (newest completed query with cutoff < through).

func anchorClock(t *testing.T, at time.Time) {
	t.Helper()
	previous := evidenceNow
	evidenceNow = func() time.Time { return at }
	t.Cleanup(func() { evidenceNow = previous })
}

// anchorHistoryUnknown builds a fresh query at cutoff over the same hosts and
// returns its dataset history_unknown counts under v2 and v4.
func anchorHistoryUnknown(t *testing.T, db *gorm.DB, cutoff time.Time) (v2, v4 int) {
	t.Helper()
	q := recordStageQuery(t, db, cutoff)
	b, e := RecordOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	spec := datasetSpec(q, b)
	d2, e := BuildDataset(context.Background(), db, spec)
	if e != nil {
		t.Fatal(e)
	}
	spec.HistoryContract = HistoryContractV4
	d4, e := BuildDataset(context.Background(), db, spec)
	if e != nil {
		t.Fatal(e)
	}
	if len(d2.Rows) != 2 || len(d4.Rows) != 2 {
		t.Fatal("rows", len(d2.Rows), len(d4.Rows))
	}
	return d2.Counts["history_unknown"], d4.Counts["history_unknown"]
}

func TestLedgerAnchorCoverageNonDegenerateAndV4History(t *testing.T) {
	for _, path := range []string{"stage", "declare-after-stage"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			db := coverageStageDB(t)
			base := time.Now().UTC().Add(-40 * 24 * time.Hour).Truncate(time.Hour)
			q1 := recordStageQuery(t, db, base)
			q2 := recordStageQuery(t, db, base.Add(48*time.Hour))
			through := base.Add(24 * time.Hour) // q1.cutoff < T < q2.cutoff
			mint := q2.Cutoff.Add(time.Hour)
			restore := evidenceNow
			anchorClock(t, mint)
			if path == "declare-after-stage" {
				// The daily stage already ran before the operator declared.
				if _, e := runCoverageSources(ctx, db, mint, CoverageOptions{}); e != nil {
					t.Fatal(e)
				}
				if len(coverageRows(t, db, q1.ID)["hardware_fault_ledger"]) != 0 {
					t.Fatal("ledger asserted without declaration")
				}
			}
			if _, e := DeclareLedgerCoverage(ctx, db, through, "operator"); e != nil {
				t.Fatal(e)
			}
			if path == "stage" {
				codes, e := runCoverageSources(ctx, db, mint, CoverageOptions{})
				if e != nil {
					t.Fatal(e)
				}
				for _, c := range codes {
					if c == "coverage_hardware_fault_ledger_incomplete" {
						t.Fatal(codes)
					}
				}
				// The newest query is still asserted with its (degenerate) tail.
				for _, v := range coverageRows(t, db, q2.ID)["hardware_fault_ledger"] {
					if !v.From.Equal(through) || !v.Through.Equal(through) {
						t.Fatal("newest query ledger interval changed", v.From, v.Through)
					}
				}
			} else {
				anchor, rows, minted, e := MintLedgerAnchorCoverage(ctx, db)
				if e != nil || anchor.ID != q1.ID || len(rows) != 2 || minted != 2 {
					t.Fatal("anchor mint", anchor.ID, q1.ID, len(rows), minted, e)
				}
				// The next stage run reuses the CLI-minted anchor heads (it only
				// adds the newest query's own degenerate tail assertions).
				before := len(coverageRows(t, db, q1.ID)["hardware_fault_ledger"])
				if _, e := runCoverageSources(ctx, db, mint, CoverageOptions{}); e != nil {
					t.Fatal(e)
				}
				if after := len(coverageRows(t, db, q1.ID)["hardware_fault_ledger"]); after != before {
					t.Fatal("stage re-minted CLI anchor coverage", before, after)
				}
			}
			led := coverageRows(t, db, q1.ID)["hardware_fault_ledger"]
			if len(led) != 2 {
				t.Fatal("anchor query ledger assertions", len(led))
			}
			for _, v := range led {
				if !v.Complete || !v.From.Equal(q1.Cutoff) || !v.Through.Equal(through) || !v.Through.After(v.From) {
					t.Fatal("anchor ledger interval degenerate", v.From, v.Through)
				}
			}
			// (c) unchanged re-run of stage and CLI mint creates no revisions.
			n := countRevisions(t, db)
			if _, e := runCoverageSources(ctx, db, mint, CoverageOptions{}); e != nil {
				t.Fatal(e)
			}
			if anchor, _, minted, e := MintLedgerAnchorCoverage(ctx, db); e != nil || anchor.ID != q1.ID || minted != 0 {
				t.Fatal("anchor re-mint", anchor.ID, minted, e)
			}
			if got := countRevisions(t, db); got != n {
				t.Fatal("unchanged re-run minted revisions", n, got)
			}
			evidenceNow = restore
			// v4 lag boundary on a later query q3 using q1's assertions.
			v2, v4 := anchorHistoryUnknown(t, db, through.Add(LedgerMaxLag))
			if v4 != 0 || v2 != 2 {
				t.Fatal("q3.cutoff = T+8d", v2, v4)
			}
			_, v4 = anchorHistoryUnknown(t, db, through.Add(LedgerMaxLag+time.Microsecond))
			if v4 != 2 {
				t.Fatal("q3.cutoff > T+8d", v4)
			}
		})
	}
}

// (b) C3: an assertion with AvailableAt == q3.cutoff is not used.
func TestLedgerAnchorCoverageAvailableAtCutoffExcluded(t *testing.T) {
	ctx := context.Background()
	db := coverageStageDB(t)
	base := time.Now().UTC().Add(-40 * 24 * time.Hour).Truncate(time.Hour)
	q1 := recordStageQuery(t, db, base)
	through := base.Add(24 * time.Hour)
	mint := base.Add(50 * time.Hour)
	restore := evidenceNow
	anchorClock(t, mint)
	if _, _, _, e := MintLedgerAnchorCoverage(ctx, db); !errors.Is(e, ErrNoLedgerAnchor) {
		t.Fatal("anchor without declaration", e)
	}
	if _, e := DeclareLedgerCoverage(ctx, db, through, "operator"); e != nil {
		t.Fatal(e)
	}
	anchor, rows, minted, e := MintLedgerAnchorCoverage(ctx, db)
	if e != nil || anchor.ID != q1.ID || len(rows) != 2 || minted != 2 {
		t.Fatal(anchor.ID, len(rows), minted, e)
	}
	for _, r := range rows {
		if !r.AvailableAt.Equal(mint) {
			t.Fatal("available_at", r.AvailableAt)
		}
	}
	evidenceNow = restore
	if _, v4 := anchorHistoryUnknown(t, db, mint); v4 != 2 {
		t.Fatal("assertion available at cutoff used", v4)
	}
	if _, v4 := anchorHistoryUnknown(t, db, mint.Add(time.Microsecond)); v4 != 0 {
		t.Fatal("assertion available before cutoff not used", v4)
	}
	var n int64
	db.Model(&api.GPURiskEvidenceRevision{}).Where("kind = ?", "coverage").Count(&n)
	if n != 2 {
		t.Fatal("dataset build wrote evidence", n)
	}
}

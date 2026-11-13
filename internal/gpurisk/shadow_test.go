package gpurisk

import (
	"context"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

func shadowDB(t *testing.T) (*gorm.DB, api.GPURiskQuery) {
	t.Helper()
	db, q, _ := outcomeDB(t) // members node-1/sn-1 and node-2/sn-2, cutoff 9 days ago
	if e := db.AutoMigrate(&api.GPUFaultEvent{}, &api.GPURiskShadowBatch{}, &api.GPURiskShadowScore{}, &api.GPURiskOperation{}); e != nil {
		t.Fatal(e)
	}
	return db, q
}

func shadowFeedback(t *testing.T, db *gorm.DB, key, ip string, onset, created time.Time) api.HardwareFaultFeedbackRequest {
	t.Helper()
	f := api.HardwareFaultFeedbackRequest{RequestKey: key, Status: "reported", NodeIP: ip, TargetScope: "node", FaultType: "node_hardware_fault", FaultOccurredAt: onset, Operator: "ops", CreatedAt: created}
	if e := db.Create(&f).Error; e != nil {
		t.Fatal(e)
	}
	return f
}

func shadowXID(t *testing.T, db *gorm.DB, ip string, code float64, first, created time.Time) {
	t.Helper()
	if e := db.Create(&api.GPUFaultEvent{NodeIP: ip, RuleCode: "recent_xid_change", ObservedValue: code, FirstObservedAt: first, LastObservedAt: first, CreatedAt: created}).Error; e != nil {
		t.Fatal(e)
	}
}

func shadowRows(t *testing.T, db *gorm.DB, q api.GPURiskQuery) (api.GPURiskShadowBatch, []api.GPURiskShadowScore) {
	t.Helper()
	b, rows, e := ComputeShadow(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 2 || b.Strategy != ShadowStrategy || b.ScoredCount != 2 {
		t.Fatal(b, rows)
	}
	return b, rows
}

func TestShadowPercentileTies(t *testing.T) {
	got := percentile([]float64{0, 0, 5, 1})
	want := []float64{1.0 / 6, 1.0 / 6, 1, 2.0 / 3}
	for i := range want {
		if d := got[i] - want[i]; d > 1e-12 || d < -1e-12 {
			t.Fatal(got)
		}
	}
	if p := percentile([]float64{3}); p[0] != 0 {
		t.Fatal(p)
	}
}

// With no signals every member ties; nothing invented.
func TestShadowAllTieWithoutSignals(t *testing.T) {
	db, q := shadowDB(t)
	_, rows := shadowRows(t, db, q)
	for _, r := range rows {
		if r.RawScore == nil || r.Rank != 1 || r.TieCount != 2 || r.History != 0 || r.DaysSince != nil || r.Precursor != 0 {
			t.Fatal(r)
		}
	}
}

// Only rows available strictly before cutoff contribute (C3), and reviews
// decided before cutoff are honoured; later reviews are not.
func TestShadowHistoryRespectsAvailability(t *testing.T) {
	db, q := shadowDB(t)
	c := q.Cutoff
	// Two faults 1 day apart on node-1 -> one 72h-merged episode, available before cutoff.
	shadowFeedback(t, db, "a", "node-1", c.Add(-48*time.Hour), c.Add(-47*time.Hour))
	shadowFeedback(t, db, "b", "node-1", c.Add(-24*time.Hour), c.Add(-23*time.Hour))
	// Known only at cutoff (available_at == cutoff) -> excluded.
	shadowFeedback(t, db, "c", "node-2", c.Add(-time.Hour), c)
	// Excluded by a review made before cutoff -> ignored.
	f := shadowFeedback(t, db, "d", "node-2", c.Add(-10*24*time.Hour), c.Add(-9*24*time.Hour))
	if e := db.Create(&api.HardwareFaultFeedbackReview{FeedbackRequestID: f.ID, Revision: 1, Decision: "excluded_no_hardware", Reviewer: "r", ReviewNote: "x", ReviewSHA256: strings.Repeat("1", 64), ReviewedAt: c.Add(-time.Hour), CreatedAt: c.Add(-time.Hour)}).Error; e != nil {
		t.Fatal(e)
	}
	// An exclusion decided after cutoff must not be used.
	g := shadowFeedback(t, db, "e", "node-1", c.Add(-20*24*time.Hour), c.Add(-19*24*time.Hour))
	if e := db.Create(&api.HardwareFaultFeedbackReview{FeedbackRequestID: g.ID, Revision: 1, Decision: "excluded_no_hardware", Reviewer: "r", ReviewNote: "x", ReviewSHA256: strings.Repeat("2", 64), ReviewedAt: time.Now().UTC(), CreatedAt: time.Now().UTC()}).Error; e != nil {
		t.Fatal(e)
	}
	_, rows := shadowRows(t, db, q)
	if rows[0].History != 2 || rows[0].DaysSince == nil || *rows[0].DaysSince < 0.99 || *rows[0].DaysSince > 1.01 {
		t.Fatal("node-1", rows[0])
	}
	if rows[1].History != 0 || rows[1].DaysSince != nil {
		t.Fatal("node-2", rows[1])
	}
	if rows[0].Rank != 1 || rows[1].Rank != 2 || *rows[0].RawScore <= *rows[1].RawScore {
		t.Fatal(rows)
	}
}

// Precursors: critical XID only, first observed in [cutoff-30d, cutoff), created before cutoff.
func TestShadowPrecursorWindow(t *testing.T) {
	db, q := shadowDB(t)
	c := q.Cutoff
	shadowXID(t, db, "node-2", 79, c.Add(-2*24*time.Hour), c.Add(-2*24*time.Hour))   // counts
	shadowXID(t, db, "node-2", 31, c.Add(-2*24*time.Hour), c.Add(-2*24*time.Hour))   // non-critical
	shadowXID(t, db, "node-2", 94, c.Add(-31*24*time.Hour), c.Add(-31*24*time.Hour)) // too old
	shadowXID(t, db, "node-2", 48, c.Add(-time.Hour), c)                             // available at cutoff
	shadowXID(t, db, "node-1", 154, c, c.Add(-time.Minute))                          // first observed at cutoff
	_, rows := shadowRows(t, db, q)
	if rows[1].Precursor != 1 || rows[0].Precursor != 0 || rows[1].Rank != 1 {
		t.Fatal(rows)
	}
}

// A batch is frozen once per query; later data never rewrites it. Timing marks
// whether it was written within the prospective window after the query froze.
func TestShadowFrozenOnceAndTiming(t *testing.T) {
	db, q := shadowDB(t)
	first, e := RecordShadow(context.Background(), db, q.ID, time.Now())
	if e != nil || first.ID == 0 || first.Timing != "prospective" {
		t.Fatal(first, e)
	}
	shadowFeedback(t, db, "late", "node-2", q.Cutoff.Add(-time.Hour), q.Cutoff.Add(-30*time.Minute))
	again, e := RecordShadow(context.Background(), db, q.ID, time.Now())
	if e != nil || again.ID != first.ID || again.SHA != first.SHA {
		t.Fatal("frozen batch changed", again, e)
	}
	var n int64
	db.Model(&api.GPURiskShadowScore{}).Where("batch_id = ?", first.ID).Count(&n)
	if n != 2 {
		t.Fatal(n)
	}
	late, _, e := ComputeShadow(context.Background(), db, q.ID, q.CompletedAt.Add(ShadowProspectiveWindow+time.Second))
	if e != nil || late.Timing != "retrospective" {
		t.Fatal(late.Timing, e)
	}
}

// The operations stage freezes the newest query and is observed as completed.
func TestShadowOperationsStage(t *testing.T) {
	db, q := shadowDB(t)
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		if e := OperationsOnceWith(context.Background(), db, now, OperationsConfig{Shadow: true}); e != nil {
			t.Fatal(e)
		}
	}
	var batches []api.GPURiskShadowBatch
	db.Find(&batches)
	if len(batches) != 1 || batches[0].QueryID != q.ID {
		t.Fatal(batches)
	}
	var op api.GPURiskOperation
	if e := db.Where("stage = ?", "shadow").First(&op).Error; e != nil || op.Status != "completed" {
		t.Fatal(op, e)
	}
}

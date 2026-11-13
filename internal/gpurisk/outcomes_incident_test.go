package gpurisk

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// incidentDB: the v1 outcome fixture (members node-1/sn-1 and node-2/sn-2,
// cutoff 9 days ago so the horizon and 24h buffer have elapsed) plus the
// identity table the T-A evaluator reads for contrary evidence.
func incidentDB(t *testing.T) (*gorm.DB, api.GPURiskQuery) {
	t.Helper()
	db, q, _ := outcomeDB(t)
	if e := db.AutoMigrate(&api.HistoricalGPUIdentityInterval{}); e != nil {
		t.Fatal(e)
	}
	return db, q
}

func coverAll(t *testing.T, db *gorm.DB, q api.GPURiskQuery, nodes ...uint) {
	t.Helper()
	for _, n := range nodes {
		for _, s := range []string{"hardware_fault_ledger", "monitoring", "identity"} {
			cover(t, db, q, n, s, true)
		}
	}
}

var incidentSeq = 0

// feedbackOn creates feedback on nodeIP with onset cutoff+delta; decision ""
// leaves it unreviewed.
func feedbackOn(t *testing.T, db *gorm.DB, q api.GPURiskQuery, nodeIP, scope, decision string, delta time.Duration) api.HardwareFaultFeedbackRequest {
	t.Helper()
	incidentSeq++
	onset := q.Cutoff.Add(delta)
	f := api.HardwareFaultFeedbackRequest{RequestKey: fmt.Sprintf("inc-%d", incidentSeq), Status: "reported", NodeIP: nodeIP, TargetScope: scope, FaultType: "gpu_lost", FaultOccurredAt: onset, Operator: "ops", CreatedAt: time.Now().UTC()}
	if e := db.Create(&f).Error; e != nil {
		t.Fatal(e)
	}
	if decision != "" {
		r := api.HardwareFaultFeedbackReview{FeedbackRequestID: f.ID, Revision: 1, Decision: decision, Reviewer: "r", ReviewNote: "fixture", ConfirmedOnsetAt: &onset, ConfirmedNodeIP: nodeIP, TargetScope: scope, EpisodeKey: fmt.Sprintf("hwfb-%d", f.ID), EvidenceKeys: api.StringList{"proof"}, ReviewSHA256: strings.Repeat(fmt.Sprintf("%x", incidentSeq%16), 64), ReviewedAt: time.Now().UTC()}
		if e := db.Create(&r).Error; e != nil {
			t.Fatal(e)
		}
	}
	return f
}

func incident(t *testing.T, db *gorm.DB, q api.GPURiskQuery) (api.GPURiskOutcomeBatch, []api.GPURiskNodeOutcome) {
	t.Helper()
	b, rows, e := EvaluateIncidentOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if b.Version != IncidentOutcomeVersion || len(rows) != 2 {
		t.Fatal(b, rows)
	}
	return b, rows
}

// Node-scope confirmed incident is a T-A positive without any per-query identity
// ingest, while the frozen v1 target still treats it as unresolved for everyone.
func TestIncidentNodeScopePositiveWithoutIdentityIngest(t *testing.T) {
	db, q := incidentDB(t)
	coverAll(t, db, q, 1, 2)
	feedbackOn(t, db, q, "node-1", "node", "confirmed_hardware", 24*time.Hour)
	_, rows := incident(t, db, q)
	if rows[0].Status != "matured_positive" || rows[0].Positive == nil || !*rows[0].Positive || len(rows[0].EpisodeKeys) != 1 {
		t.Fatal("node-1", rows[0])
	}
	if rows[1].Status != "matured_negative" {
		t.Fatal("node-2 must mature negative", rows[1])
	}
	_, v1 := evaluate(t, db, q)
	if v1[1].Status == "matured_negative" {
		t.Fatal("v1 semantics must be unchanged (fleet-wide unresolved without identity ingest)", v1[1])
	}
}

// Unreviewed feedback blocks only its own node in T-A; v1 still blocks all.
func TestIncidentUnreviewedBlocksOnlyItsNode(t *testing.T) {
	db, q := incidentDB(t)
	coverAll(t, db, q, 1, 2)
	feedbackOn(t, db, q, "node-1", "gpu", "", 24*time.Hour)
	_, rows := incident(t, db, q)
	if rows[0].Status != "pending_watermark" || strings.Join(rows[0].Blockers, ",") != "unresolved_feedback" {
		t.Fatal(rows[0])
	}
	if rows[1].Status != "matured_negative" {
		t.Fatal(rows[1])
	}
	_, v1 := evaluate(t, db, q)
	if v1[1].Status != "pending_watermark" {
		t.Fatal("v1 must still block fleet-wide", v1[1])
	}
	// needs_evidence keeps blocking the node; context_only/excluded release it.
	f2 := feedbackOn(t, db, q, "node-2", "gpu", "needs_evidence", 48*time.Hour)
	_, rows = incident(t, db, q)
	if rows[1].Status != "pending_watermark" {
		t.Fatal("needs_evidence must block node-2", rows[1])
	}
	r := api.HardwareFaultFeedbackReview{FeedbackRequestID: f2.ID, Revision: 2, Decision: "context_only", Reviewer: "r", ReviewNote: "x", ReviewSHA256: strings.Repeat("c", 64), ReviewedAt: time.Now().UTC().Add(-time.Second)}
	if e := db.Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	_, rows = incident(t, db, q)
	if rows[1].Status != "matured_negative" {
		t.Fatal("context_only must release node-2", rows[1])
	}
}

// Feedback without a node IP fails closed for the whole query.
func TestIncidentUnknownNodeFailsClosed(t *testing.T) {
	db, q := incidentDB(t)
	coverAll(t, db, q, 1, 2)
	feedbackOn(t, db, q, "", "gpu", "", 24*time.Hour)
	_, rows := incident(t, db, q)
	for _, r := range rows {
		if r.Status != "pending_watermark" {
			t.Fatal(r)
		}
	}
}

// Boundaries: onset exactly at cutoff is not in (cutoff, end]; onset exactly at
// end is in; a window crossing the cutoff is ambiguous; outside is ignored.
func TestIncidentHorizonBoundaries(t *testing.T) {
	db, q := incidentDB(t)
	coverAll(t, db, q, 1, 2)
	feedbackOn(t, db, q, "node-1", "node", "confirmed_hardware", 0)
	feedbackOn(t, db, q, "node-2", "node", "confirmed_hardware", 7*24*time.Hour)
	_, rows := incident(t, db, q)
	if rows[0].Status != "matured_negative" {
		t.Fatal("onset == cutoff must not be a positive or blocker", rows[0])
	}
	if rows[1].Status != "matured_positive" {
		t.Fatal("onset == cutoff+7d must be a positive", rows[1])
	}
	db2, q2 := incidentDB(t)
	coverAll(t, db2, q2, 1, 2)
	f := feedbackOn(t, db2, q2, "node-1", "node", "", time.Minute)
	a, b := q2.Cutoff.Add(-time.Minute), q2.Cutoff.Add(time.Minute)
	onset := q2.Cutoff.Add(time.Minute)
	r := api.HardwareFaultFeedbackReview{FeedbackRequestID: f.ID, Revision: 1, Decision: "confirmed_hardware", Reviewer: "r", ReviewNote: "x", ConfirmedOnsetAt: &onset, ConfirmedWindowStartAt: &a, ConfirmedWindowEndAt: &b, ConfirmedNodeIP: "node-1", TargetScope: "node", EpisodeKey: "cross", EvidenceKeys: api.StringList{"p"}, ReviewSHA256: strings.Repeat("d", 64), ReviewedAt: time.Now().UTC()}
	if e := db2.Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	feedbackOn(t, db2, q2, "node-2", "node", "confirmed_hardware", 8*24*time.Hour)
	_, rows = incident(t, db2, q2)
	if rows[0].Status != "ambiguous" || rows[0].Positive != nil {
		t.Fatal(rows[0])
	}
	if rows[1].Status != "matured_negative" {
		t.Fatal("onset after end must be ignored", rows[1])
	}
}

// Contrary identity evidence (different serial on the same IP at onset) is a
// conflict; missing identity is not.
func TestIncidentSerialMismatchIsConflict(t *testing.T) {
	db, q := incidentDB(t)
	coverAll(t, db, q, 1, 2)
	feedbackOn(t, db, q, "node-1", "node", "confirmed_hardware", 24*time.Hour)
	feedbackOn(t, db, q, "node-2", "node", "confirmed_hardware", 24*time.Hour)
	at := q.Cutoff.Add(24 * time.Hour)
	if e := db.Create(&api.HistoricalGPUIdentityInterval{IntervalKey: "i1", SourceKey: "s", NodeIP: "node-1", HostSerial: "other-serial", GPUUUID: "u1", FirstSeenAt: at.Add(-time.Hour), LastSeenAt: at.Add(time.Hour)}).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&api.HistoricalGPUIdentityInterval{IntervalKey: "i2", SourceKey: "s", NodeIP: "node-2", HostSerial: "sn-2", GPUUUID: "u2", FirstSeenAt: at.Add(-time.Hour), LastSeenAt: at.Add(time.Hour)}).Error; e != nil {
		t.Fatal(e)
	}
	_, rows := incident(t, db, q)
	if rows[0].Status != "identity_conflict" || !strings.Contains(strings.Join(rows[0].Blockers, ","), "incident_serial_mismatch") {
		t.Fatal(rows[0])
	}
	if rows[1].Status != "matured_positive" {
		t.Fatal(rows[1])
	}
}

// Negatives keep coverage and late-buffer safeguards; batches are as-of
// replayable, immutable, idempotent and never confused with v1 batches.
func TestIncidentSafeguardsReplayAndVersionIsolation(t *testing.T) {
	db, q := incidentDB(t)
	_, rows := incident(t, db, q)
	if rows[0].Status != "pending_watermark" || !strings.Contains(strings.Join(rows[0].Blockers, ","), "missing_coverage:monitoring") {
		t.Fatal(rows[0])
	}
	coverAll(t, db, q, 1, 2)
	b, rows := incident(t, db, q)
	if rows[0].Status != "matured_negative" {
		t.Fatal(rows[0])
	}
	first, e := RecordIncidentOutcomes(context.Background(), db, q.ID, b.AsOf)
	if e != nil {
		t.Fatal(e)
	}
	again, e := RecordIncidentOutcomes(context.Background(), db, q.ID, b.AsOf)
	if e != nil || again.ID != first.ID {
		t.Fatal(again, e)
	}
	feedbackOn(t, db, q, "node-1", "node", "confirmed_hardware", 24*time.Hour)
	_, rows = incident(t, db, q)
	if rows[0].Status != "matured_positive" {
		t.Fatal(rows[0])
	}
	replay, _, e := EvaluateIncidentOutcomes(context.Background(), db, q.ID, b.AsOf)
	if e != nil || replay.SHA != b.SHA {
		t.Fatal("as-of replay changed after later review", e)
	}
	v1, e := RecordOutcomes(context.Background(), db, q.ID, b.AsOf)
	if e != nil || v1.Version != OutcomeVersion || v1.SHA == first.SHA {
		t.Fatal("v1 and incident batches must be distinct", v1, e)
	}
	var stored api.GPURiskOutcomeBatch
	db.First(&stored, first.ID)
	if stored.Version != IncidentOutcomeVersion {
		t.Fatal(stored)
	}
}

// The daily job records both targets under distinct keys.
func TestOutcomeDailyRecordsBothTargets(t *testing.T) {
	db, q := incidentDB(t)
	coverAll(t, db, q, 1, 2)
	day := time.Now().UTC()
	local := day.In(time.FixedZone("Asia/Shanghai", 8*3600))
	if local.Hour() < 2 || (local.Hour() == 2 && local.Minute() < 10) {
		t.Skip("daily window not open at this wall clock; covered by explicit Record tests")
	}
	if e := OutcomeDaily(context.Background(), db, day); e != nil {
		t.Fatal(e)
	}
	var versions []string
	db.Model(&api.GPURiskOutcomeBatch{}).Where("query_id = ?", q.ID).Order("version").Pluck("version", &versions)
	if len(versions) != 2 || versions[0] != OutcomeVersion || versions[1] != IncidentOutcomeVersion {
		t.Fatal(versions)
	}
}

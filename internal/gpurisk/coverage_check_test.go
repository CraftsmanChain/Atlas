package gpurisk

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

func coverageDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testDB(t)
	if e := db.AutoMigrate(&api.HardwareFaultFeedbackRequest{}, &api.HistoricalGPUIdentityInterval{}, &api.MonitoringHistoryAudit{}, &api.GPURiskSourceCoverageCheck{}); e != nil {
		t.Fatal(e)
	}
	return db
}

// RecordCount and CheckSHA256 come from the rows actually read, in a stable key
// order. The two live tables are read at the server clock, so ObservedAt ==
// VerifiedThrough (rule A); no input carries a timestamp, so a check can only
// restate what was visible.
func TestRecordSourceCoverageReadsRealRowsOnServerClock(t *testing.T) {
	db := coverageDB(t)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	previous := evidenceNow
	evidenceNow = func() time.Time { return at }
	t.Cleanup(func() { evidenceNow = previous })

	for _, key := range []string{"ledger-b", "ledger-a"} {
		if e := db.Create(&api.HardwareFaultFeedbackRequest{RequestKey: key, Status: "reported", NodeIP: "n", Operator: "ops"}).Error; e != nil {
			t.Fatal(e)
		}
	}
	row, e := RecordSourceCoverage(t.Context(), db, CoverageCheckInput{Source: "hardware_fault_ledger", Owner: "platform-admin"})
	if e != nil {
		t.Fatal(e)
	}
	want, _ := digest([]string{"ledger-a", "ledger-b"})
	if row.RecordCount != 2 || row.CheckSHA256 != want || row.Contract != CoverageWatermarkContract {
		t.Fatal(row)
	}
	if !row.VerifiedThrough.Equal(at) || !row.ObservedAt.Equal(at) || !row.CreatedAt.Equal(at) {
		t.Fatal(row)
	}
	for _, key := range []string{"interval-2", "interval-1"} {
		if e := db.Create(&api.HistoricalGPUIdentityInterval{IntervalKey: key, SourceKey: "history", NodeIP: "n", GPUUUID: "u"}).Error; e != nil {
			t.Fatal(e)
		}
	}
	idRow, e := RecordSourceCoverage(t.Context(), db, CoverageCheckInput{Source: "identity", Owner: "platform-admin"})
	if e != nil || idRow.RecordCount != 2 {
		t.Fatal(idRow, e)
	}
	idWant, _ := digest([]string{"interval-1", "interval-2"})
	if idRow.CheckSHA256 != idWant {
		t.Fatal(idRow.CheckSHA256, idWant)
	}
	// Append-only: a second run adds a row instead of rewriting history.
	if _, e := RecordSourceCoverage(t.Context(), db, CoverageCheckInput{Source: "hardware_fault_ledger", Owner: "platform-admin"}); e != nil {
		t.Fatal(e)
	}
	var count int64
	if e := db.Model(&api.GPURiskSourceCoverageCheck{}).Where("source = ?", "hardware_fault_ledger").Count(&count).Error; e != nil || count != 2 {
		t.Fatal(count, e)
	}
}

// monitoring has no Atlas table of raw samples, so its canonical durable record
// is the audit table and its watermark is the completed pull of the newest audit
// per source, not the server clock.
func TestRecordSourceCoverageMonitoringUsesCanonicalAudit(t *testing.T) {
	db := coverageDB(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	previous := evidenceNow
	evidenceNow = func() time.Time { return at }
	t.Cleanup(func() { evidenceNow = previous })
	pulled := at.Add(-90 * time.Minute)
	// A healthy audit still reports the research metric families this exporter
	// version never emits; that chronic capability gap must not block the pull.
	if e := db.Create(&api.MonitoringHistoryAudit{SourceKey: "vm", SourceName: "VictoriaMetrics", SourceType: "victoriametrics-cluster", BaseURL: "http://vm", Status: "success", CurrentGPUSeries: 4104, MissingMetricFamilies: api.StringList{"DCGM_EXP_XID_ERRORS_TOTAL", "DCGM_FI_DEV_THERMAL_VIOLATION"}, StartedAt: pulled.Add(-time.Minute), FinishedAt: pulled}).Error; e != nil {
		t.Fatal(e)
	}
	row, e := RecordSourceCoverage(t.Context(), db, CoverageCheckInput{Source: "monitoring", Owner: "monitoring-owner"})
	if e != nil {
		t.Fatal(e)
	}
	want, _ := digest([]string{"vm|1"})
	if row.RecordCount != 1 || row.CheckSHA256 != want || row.Contract != CoverageWatermarkContract {
		t.Fatal(row)
	}
	if !row.VerifiedThrough.Equal(pulled) || !row.ObservedAt.Equal(at) || row.VerifiedThrough.After(row.ObservedAt) {
		t.Fatal(row)
	}
}

func TestRecordSourceCoverageMonitoringRefusesDegradedSource(t *testing.T) {
	db := coverageDB(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	previous := evidenceNow
	evidenceNow = func() time.Time { return at }
	t.Cleanup(func() { evidenceNow = previous })

	// No canonical record at all: refuse rather than assert anything.
	if _, e := RecordSourceCoverage(t.Context(), db, CoverageCheckInput{Source: "monitoring", Owner: "ops"}); !errors.Is(e, ErrCoverageSourceIncomplete) {
		t.Fatal(e)
	}
	// A newest partial audit means the pull did not complete, even though an
	// older audit succeeded: refuse instead of certifying from the healthy row.
	for _, audit := range []api.MonitoringHistoryAudit{
		{SourceKey: "vm", SourceName: "VictoriaMetrics", SourceType: "victoriametrics-cluster", BaseURL: "http://vm", Status: "success", CurrentGPUSeries: 4104},
		{SourceKey: "vm", SourceName: "VictoriaMetrics", SourceType: "victoriametrics-cluster", BaseURL: "http://vm", Status: "partial", CurrentGPUSeries: 0},
	} {
		audit.StartedAt = at.Add(-2 * time.Hour)
		audit.FinishedAt = at.Add(-time.Hour)
		if e := db.Create(&audit).Error; e != nil {
			t.Fatal(e)
		}
	}
	if _, e := RecordSourceCoverage(t.Context(), db, CoverageCheckInput{Source: "monitoring", Owner: "ops"}); !errors.Is(e, ErrCoverageSourceIncomplete) {
		t.Fatal(e)
	}
	// A newest success that read no GPU series is not complete either.
	var last api.MonitoringHistoryAudit
	if e := db.Order("id DESC").First(&last).Error; e != nil {
		t.Fatal(e)
	}
	last.ID, last.Status, last.CurrentGPUSeries = 0, "success", 0
	if e := db.Create(&last).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := RecordSourceCoverage(t.Context(), db, CoverageCheckInput{Source: "monitoring", Owner: "ops"}); !errors.Is(e, ErrCoverageSourceIncomplete) {
		t.Fatal(e)
	}
	var count int64
	if e := db.Model(&api.GPURiskSourceCoverageCheck{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatal("refused source wrote a check row", count)
	}
}

func TestRecordSourceCoverageRejectsInvalidInput(t *testing.T) {
	db := coverageDB(t)
	for _, in := range []CoverageCheckInput{
		{Source: "hardware_fault_ledger", Owner: "   "},
		{Source: "", Owner: "ops"},
		{Source: "bogus", Owner: "ops"},
	} {
		if _, e := RecordSourceCoverage(t.Context(), db, in); !errors.Is(e, ErrInput) {
			t.Fatalf("accepted %+v: %v", in, e)
		}
	}
	var count int64
	if e := db.Model(&api.GPURiskSourceCoverageCheck{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatal("invalid input wrote a row", count)
	}
}

// A per-member coverage batch restates the newest signed source check for every
// included member: one shared watermark, idempotent on re-run, and never a row
// for a member the query did not include.
func TestRecordMemberCoverageDerivesFromSourceCheck(t *testing.T) {
	db := testDB(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	previous := evidenceNow
	evidenceNow = func() time.Time { return at }
	t.Cleanup(func() { evidenceNow = previous })

	cutoff := at.Add(-48 * time.Hour)
	q := api.GPURiskQuery{Key: strings.Repeat("c", 64), ScopeSHA: strings.Repeat("a", 64), Contract: Contract, Target: Target, Cutoff: cutoff, HorizonMinutes: 10080, Status: "completed"}
	if e := db.Create(&q).Error; e != nil {
		t.Fatal(e)
	}
	for _, m := range []api.GPURiskMember{
		{QueryID: q.ID, NodeID: 1, Generation: 0, NodeIP: "n1", EntityRef: "serial:SN1", Status: "included"},
		{QueryID: q.ID, NodeID: 2, Generation: 0, NodeIP: "n2", EntityRef: "serial:SN2", Status: "included"},
		{QueryID: q.ID, NodeID: 3, Generation: 0, NodeIP: "n3", EntityRef: "serial:SN3", Status: "excluded"},
	} {
		if e := db.Create(&m).Error; e != nil {
			t.Fatal(e)
		}
	}
	through := at.Add(-time.Hour)
	if e := db.Create(&api.GPURiskSourceCoverageCheck{Source: "monitoring", VerifiedThrough: through, ObservedAt: at, RecordCount: 1, CheckSHA256: strings.Repeat("b", 64), Owner: "ops", Contract: CoverageWatermarkContract}).Error; e != nil {
		t.Fatal(e)
	}
	rows, e := RecordMemberCoverage(t.Context(), db, q.ID, "monitoring", "platform-admin")
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 2 {
		t.Fatalf("expected one row per included member, got %d", len(rows))
	}
	for _, r := range rows {
		var v EvidenceInput
		if json.Unmarshal([]byte(r.Data), &v) != nil {
			t.Fatal(r.Data)
		}
		if v.Kind != "coverage" || v.Source != "monitoring" || !v.Complete || v.Owner != "platform-admin" {
			t.Fatal(v)
		}
		if !v.From.Equal(cutoff) || !v.Through.Equal(through) {
			t.Fatal(v)
		}
		if v.Key != memberCoverageKey(q.ID, v.NodeID, v.Generation, "monitoring") {
			t.Fatal(v.Key)
		}
	}
	// Re-running an unchanged state must not mint revisions.
	again, e := RecordMemberCoverage(t.Context(), db, q.ID, "monitoring", "platform-admin")
	if e != nil {
		t.Fatal(e)
	}
	if len(again) != 2 || again[0].Revision != 1 {
		t.Fatal("re-run minted a revision")
	}
	var count int64
	if e := db.Model(&api.GPURiskEvidenceRevision{}).Count(&count).Error; e != nil || count != 2 {
		t.Fatal("unexpected revision count", count)
	}
}

// A member is never more complete than the source that observed it: without a
// current check the batch is refused rather than defaulted complete.
func TestRecordMemberCoverageRefusesWithoutCurrentCheck(t *testing.T) {
	db := testDB(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	previous := evidenceNow
	evidenceNow = func() time.Time { return at }
	t.Cleanup(func() { evidenceNow = previous })

	q := api.GPURiskQuery{Key: strings.Repeat("d", 64), ScopeSHA: strings.Repeat("a", 64), Contract: Contract, Target: Target, Cutoff: at.Add(-48 * time.Hour), HorizonMinutes: 10080, Status: "completed"}
	if e := db.Create(&q).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&api.GPURiskMember{QueryID: q.ID, NodeID: 1, Generation: 0, EntityRef: "serial:SN1", Status: "included"}).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := RecordMemberCoverage(t.Context(), db, q.ID, "monitoring", "ops"); !errors.Is(e, ErrCoverageSourceIncomplete) {
		t.Fatal(e)
	}
	var count int64
	if e := db.Model(&api.GPURiskEvidenceRevision{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatal("refused batch wrote evidence", count)
	}
	if _, e := RecordMemberCoverage(t.Context(), db, q.ID, "bogus", "ops"); !errors.Is(e, ErrInput) {
		t.Fatal(e)
	}
}

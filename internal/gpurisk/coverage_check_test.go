package gpurisk

import (
	"errors"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

func coverageDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testDB(t)
	if e := db.AutoMigrate(&api.HardwareFaultFeedbackRequest{}, &api.HistoricalGPUIdentityInterval{}, &api.GPURiskSourceCoverageCheck{}); e != nil {
		t.Fatal(e)
	}
	return db
}

// RecordCount and CheckSHA256 come from the rows actually read, in a stable key
// order, and ObservedAt == VerifiedThrough is the server clock (rule A). No
// input carries a timestamp, so a check can only restate what was visible.
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

func TestRecordSourceCoverageRejectsInvalidAndMonitoring(t *testing.T) {
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
	// monitoring has no single canonical durable table: refuse rather than
	// synthesize a watermark.
	if _, e := RecordSourceCoverage(t.Context(), db, CoverageCheckInput{Source: "monitoring", Owner: "ops"}); !errors.Is(e, ErrCoverageSourceUnsupported) {
		t.Fatal(e)
	}
	var count int64
	if e := db.Model(&api.GPURiskSourceCoverageCheck{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatal("invalid input wrote a row", count)
	}
}

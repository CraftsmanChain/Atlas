package gpurisk

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
)

// P1 taskcard §4 item 2 (library side): ledger delivery declarations.
func TestLedgerDeclareRejectsFutureRegressAndEmptyOwner(t *testing.T) {
	db := coverageDB(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	previous := evidenceNow
	evidenceNow = func() time.Time { return at }
	t.Cleanup(func() { evidenceNow = previous })
	for _, key := range []string{"ledger-b", "ledger-a"} {
		if e := db.Create(&api.HardwareFaultFeedbackRequest{RequestKey: key, Status: "reported", NodeIP: "n", Operator: "ops"}).Error; e != nil {
			t.Fatal(e)
		}
	}
	count := func() int64 {
		var n int64
		db.Model(&api.GPURiskSourceCoverageCheck{}).Count(&n)
		return n
	}
	through := at.Add(-time.Hour)
	for _, tc := range []struct {
		name    string
		through time.Time
		owner   string
	}{
		{"empty_owner", through, "   "},
		{"future", at.Add(time.Microsecond), "operator"},
		{"zero", time.Time{}, "operator"},
	} {
		if _, e := DeclareLedgerCoverage(context.Background(), db, tc.through, tc.owner); !errors.Is(e, ErrLedgerDeclarationRejected) {
			t.Fatal(tc.name, e)
		}
	}
	if count() != 0 {
		t.Fatal("rejected declaration wrote a row")
	}
	row, e := DeclareLedgerCoverage(context.Background(), db, through, "operator")
	if e != nil {
		t.Fatal(e)
	}
	want, _ := digest([]string{"ledger-a", "ledger-b"})
	if row.Source != "hardware_fault_ledger" || row.Contract != LedgerDeclaredContract || !row.VerifiedThrough.Equal(through) || !row.ObservedAt.Equal(at) || row.RecordCount != 2 || row.CheckSHA256 != want || row.Owner != "operator" {
		t.Fatal(row)
	}
	// Exactly now is not in the future.
	if _, e := DeclareLedgerCoverage(context.Background(), db, at, "operator"); e != nil {
		t.Fatal(e)
	}
	if _, e := DeclareLedgerCoverage(context.Background(), db, at.Add(-time.Microsecond), "operator"); !errors.Is(e, ErrLedgerDeclarationRejected) || !strings.Contains(e.Error(), "regresses") {
		t.Fatal("regressing declaration accepted", e)
	}
	// Re-declaring the same watermark ("no new faults") is not a regression.
	if _, e := DeclareLedgerCoverage(context.Background(), db, at, "operator"); e != nil {
		t.Fatal(e)
	}
	if count() != 3 {
		t.Fatal("unexpected rows", count())
	}
}

// v3: the ledger is never certified by reading the table at now.
func TestLedgerSourceCheckV3RefusedAndLegacyCheckIgnoredForMembers(t *testing.T) {
	db := coverageDB(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	previous := evidenceNow
	evidenceNow = func() time.Time { return at }
	t.Cleanup(func() { evidenceNow = previous })
	if _, e := RecordSourceCoverageV3(context.Background(), db, CoverageCheckInput{Source: "hardware_fault_ledger", Owner: "ops"}, DefaultIdentitySource); !errors.Is(e, ErrLedgerDeclarationRequired) {
		t.Fatal(e)
	}
	var n int64
	db.Model(&api.GPURiskSourceCoverageCheck{}).Count(&n)
	if n != 0 {
		t.Fatal("refused ledger check wrote a row")
	}
	cutoff := at.Add(-48 * time.Hour)
	completed := cutoff.Add(time.Second)
	q := api.GPURiskQuery{Key: strings.Repeat("e", 64), ScopeSHA: strings.Repeat("a", 64), ResultSHA: strings.Repeat("c", 64), Contract: Contract, Target: Target, Cutoff: cutoff, HorizonMinutes: 10080, Status: "completed", MemberCount: 1, CompletedAt: &completed}
	if e := db.Create(&q).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&api.GPURiskMember{QueryID: q.ID, NodeID: 1, EntityRef: "serial:SN1", Status: "included"}).Error; e != nil {
		t.Fatal(e)
	}
	// A legacy (v2) read-at-now ledger check exists and is the newest check.
	if _, e := RecordSourceCoverage(context.Background(), db, CoverageCheckInput{Source: "hardware_fault_ledger", Owner: "legacy"}); e != nil {
		t.Fatal(e)
	}
	if _, e := RecordMemberCoverage(context.Background(), db, q.ID, "hardware_fault_ledger", CoverageOwner); !errors.Is(e, ErrCoverageSourceIncomplete) {
		t.Fatal("legacy ledger check certified members", e)
	}
	plan, e := BuildCoveragePlan(context.Background(), db, q.ID, at)
	if e != nil {
		t.Fatal(e)
	}
	ledger := plan.Sources[0]
	if ledger.Source != "hardware_fault_ledger" || ledger.CheckStatus != "invalid" || !contains(ledger.ActionReasons, "ledger_requires_operator_delivery_declaration") || len(ledger.Commands) == 0 || !strings.HasPrefix(ledger.Commands[0], "gpu-risk ledger-declare ") {
		t.Fatal("legacy ledger check in plan", ledger)
	}
	through := at.Add(-time.Hour)
	if _, e := DeclareLedgerCoverage(context.Background(), db, through, "operator"); e != nil {
		t.Fatal(e)
	}
	rows, e := RecordMemberCoverage(context.Background(), db, q.ID, "hardware_fault_ledger", CoverageOwner)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
	plan, e = BuildCoveragePlan(context.Background(), db, q.ID, at)
	if e != nil {
		t.Fatal(e)
	}
	if plan.Sources[0].CheckStatus != "within_query_tail" || plan.Sources[0].Check.Contract != LedgerDeclaredContract || plan.Sources[0].MemberAssertions != 1 {
		t.Fatal("declared ledger check in plan", plan.Sources[0])
	}
	// A declaration older than cutoff-24h is stale for this query and is refused.
	old := cutoff.Add(-MaxSourceLag - time.Microsecond)
	db2 := coverageDB(t)
	q.ID = 0
	if e := db2.Create(&q).Error; e != nil {
		t.Fatal(e)
	}
	if e := db2.Create(&api.GPURiskMember{QueryID: q.ID, NodeID: 1, EntityRef: "serial:SN1", Status: "included"}).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := DeclareLedgerCoverage(context.Background(), db2, old, "operator"); e != nil {
		t.Fatal(e)
	}
	if _, e := RecordMemberCoverage(context.Background(), db2, q.ID, "hardware_fault_ledger", CoverageOwner); !errors.Is(e, ErrCoverageSourceIncomplete) {
		t.Fatal("stale declaration accepted", e)
	}
}

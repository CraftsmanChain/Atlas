package gpurisk

import (
	"context"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

func dailyFinding(c DailyCheck, prefix, text string) bool {
	for _, f := range c.Findings {
		if strings.HasPrefix(f, prefix+" ") && strings.Contains(f, text) {
			return true
		}
	}
	return false
}

func dailyLedger(t *testing.T, db *gorm.DB, through time.Time) {
	t.Helper()
	if e := db.Create(&api.GPURiskSourceCoverageCheck{Source: "hardware_fault_ledger", VerifiedThrough: through, ObservedAt: through.Add(time.Minute), CheckSHA256: strings.Repeat("d", 64), Owner: "ops", Contract: LedgerDeclaredContract}).Error; e != nil {
		t.Fatal(e)
	}
}

func buildDaily(t *testing.T, db *gorm.DB, now time.Time) DailyCheck {
	t.Helper()
	c, e := BuildDailyCheck(context.Background(), db, now)
	if e != nil {
		t.Fatal(e)
	}
	if md := RenderDailyMarkdown(c); !strings.Contains(md, "# GPU 风险日检") || !strings.Contains(md, "## E2′ 摘要") {
		t.Fatal(md)
	}
	return c
}

// Without a shadow batch the check warns; it never fails on missing inputs,
// and the R1 probe records why it could not run.
func TestDailyCheckShadowMissingWarns(t *testing.T) {
	db, q := shadowDB(t)
	c := buildDaily(t, db, time.Now())
	if c.LatestQuery == nil || c.LatestQuery.ID != q.ID || c.LatestQuery.MemberCount != 2 {
		t.Fatalf("%+v", c.LatestQuery)
	}
	if c.Shadow.Exists || !dailyFinding(c, "WARN", "缺少影子批次") {
		t.Fatal(c.Shadow, c.Findings)
	}
	if c.R1.Error == "" || c.R1.OutcomeBatchID != 0 {
		t.Fatalf("%+v", c.R1)
	}
	if !dailyFinding(c, "WARN", "无 ledger 声明") || c.E2.Error != "" || c.E2.Verdict != "accumulating" {
		t.Fatal(c.Findings, c.E2)
	}
}

// A prospective batch is summarised (scored, ties, signal) and not warned on.
func TestDailyCheckProspectiveShadowNoWarn(t *testing.T) {
	db, q := shadowDB(t)
	shadowXID(t, db, "node-2", 79, q.Cutoff.Add(-48*time.Hour), q.Cutoff.Add(-48*time.Hour))
	b, e := RecordShadow(context.Background(), db, q.ID, time.Now())
	if e != nil || b.Timing != "prospective" {
		t.Fatal(b, e)
	}
	dailyLedger(t, db, time.Now().UTC().Add(-24*time.Hour))
	c := buildDaily(t, db, time.Now())
	if !c.Shadow.Exists || c.Shadow.BatchID != b.ID || c.Shadow.SHA != b.SHA || c.Shadow.Timing != "prospective" || c.Shadow.ScoredCount != 2 || c.Shadow.NonZeroSignal != 1 || c.Shadow.LargestTie != 1 {
		t.Fatalf("%+v", c.Shadow)
	}
	for _, f := range c.Findings {
		if strings.Contains(f, "影子批次") || strings.Contains(f, "ledger 声明") {
			t.Fatal(c.Findings)
		}
	}
	if c.Coverage.Ledger == nil || c.Coverage.Ledger.AgeDays < 0.99 || c.Coverage.Ledger.AgeDays > 1.01 {
		t.Fatalf("%+v", c.Coverage.Ledger)
	}
}

// A retrospective batch is warned as not prospective.
func TestDailyCheckRetrospectiveShadowWarns(t *testing.T) {
	db, q := shadowDB(t)
	if _, e := RecordShadow(context.Background(), db, q.ID, q.CompletedAt.Add(ShadowProspectiveWindow+time.Minute)); e != nil {
		t.Fatal(e)
	}
	c := buildDaily(t, db, time.Now())
	if !dailyFinding(c, "WARN", "非前瞻") {
		t.Fatal(c.Findings)
	}
}

// Ledger declaration older than 7 days warns; a coverage stage blocked only on
// the ledger with a declaration before cutoff-24h is INFO; failures are WARN.
func TestDailyCheckStaleLedgerAndOperations(t *testing.T) {
	db, q := shadowDB(t)
	now := time.Now().UTC()
	dailyLedger(t, db, q.Cutoff.Add(-2*24*time.Hour)) // ~11 days old
	day := now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
	for _, op := range []api.GPURiskOperation{
		{Day: day, Stage: "coverage", Status: "blocked", ErrorCode: "coverage_hardware_fault_ledger_incomplete", ObservedAt: now},
		{Day: day, Stage: "outcomes", Status: "manual_required", ErrorCode: "attempt_budget_exhausted", ObservedAt: now},
		{Day: day, Stage: "ranking", Status: "completed", ObservedAt: now},
		{Day: "2000-01-01", Stage: "ranking", Status: "failed", ObservedAt: now},
	} {
		if e := db.Create(&op).Error; e != nil {
			t.Fatal(e)
		}
	}
	c := buildDaily(t, db, now)
	if len(c.Operations) != 3 {
		t.Fatalf("%+v", c.Operations)
	}
	if !dailyFinding(c, "WARN", "ledger 声明 verified_through") || c.Coverage.Ledger == nil || c.Coverage.Ledger.AgeDays < 10.9 {
		t.Fatal(c.Findings, c.Coverage.Ledger)
	}
	if !dailyFinding(c, "INFO", "需要新的 ledger 声明") || !dailyFinding(c, "WARN", "outcomes 状态 manual_required") || dailyFinding(c, "WARN", "ranking") {
		t.Fatal(c.Findings)
	}
	if len(c.Coverage.Sources) != 1 || c.Coverage.Sources[0].Contract != LedgerDeclaredContract {
		t.Fatalf("%+v", c.Coverage.Sources)
	}

	// Blocked on another source is a WARN even with a stale ledger.
	db.Model(&api.GPURiskOperation{}).Where("day = ? AND stage = ?", day, "coverage").Update("error_code", "coverage_monitoring_incomplete")
	c = buildDaily(t, db, now)
	if !dailyFinding(c, "WARN", "coverage 阶段 blocked") || dailyFinding(c, "INFO", "需要新的 ledger 声明") {
		t.Fatal(c.Findings)
	}
}

// Unreviewed and needs_evidence feedback with onset in the last 30 days is
// listed for the Commander; reviewed and older feedback is not.
func TestDailyCheckUnreviewedFeedback(t *testing.T) {
	db, q := shadowDB(t)
	open := feedbackOn(t, db, q, "node-2", "gpu", "", 24*time.Hour)
	needs := feedbackOn(t, db, q, "node-1", "node", "needs_evidence", 48*time.Hour)
	feedbackOn(t, db, q, "node-1", "node", "confirmed_hardware", 24*time.Hour)
	feedbackOn(t, db, q, "node-1", "node", "", -30*24*time.Hour) // onset ~39 days ago
	c := buildDaily(t, db, time.Now())
	if len(c.UnreviewedFeedback) != 2 || c.UnreviewedFeedback[0].ID != open.ID || c.UnreviewedFeedback[1].ID != needs.ID {
		t.Fatalf("%+v", c.UnreviewedFeedback)
	}
	if c.UnreviewedFeedback[0].NodeIP != "node-2" || c.UnreviewedFeedback[0].FaultType != "gpu_lost" || c.UnreviewedFeedback[1].Decision != "needs_evidence" {
		t.Fatalf("%+v", c.UnreviewedFeedback)
	}
	if !dailyFinding(c, "INFO", "待 Commander 核对") {
		t.Fatal(c.Findings)
	}
}

// R1 runs on the newest v1 outcome batch of the latest query: both contracts
// report numbers or a recorded error, never a failed check. T-A counts are
// reported per recent query.
func TestDailyCheckR1AndIncidentCounts(t *testing.T) {
	db, q := incidentDB(t)
	if e := db.AutoMigrate(&api.GPUFaultEvent{}, &api.GPURiskShadowBatch{}, &api.GPURiskShadowScore{}, &api.GPURiskOperation{}); e != nil {
		t.Fatal(e)
	}
	coverAll(t, db, q, 1, 2)
	b, e := RecordOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	ib, e := RecordIncidentOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	c := buildDaily(t, db, time.Now())
	if c.R1.Error != "" || c.R1.OutcomeBatchID != b.ID || c.R1.Spec == nil {
		t.Fatalf("%+v", c.R1)
	}
	t.Logf("R1 v4=%+v v2=%+v", c.R1.V4, c.R1.V2)
	cut := q.Cutoff.UTC()
	anchor := time.Date(cut.Year(), cut.Month(), cut.Day(), 0, 0, 0, 0, time.UTC)
	if !c.R1.Spec.Anchor.Equal(anchor) || !c.R1.Spec.TrainEnd.Equal(anchor.Add(8*time.Hour)) || !c.R1.Spec.ValidationEnd.Equal(anchor.Add(16*time.Hour)) || c.R1.Spec.HistoryContract != HistoryContractV4 || c.R1.Spec.Profile != "paper_core" {
		t.Fatalf("%+v", c.R1.Spec)
	}
	// Fixture: two members with no history coverage evidence -> both rows
	// history_unknown under v4 and v2, so the >10% WARN fires.
	for _, d := range []DailyDatasetCounts{c.R1.V4, c.R1.V2} {
		if d.Error != "" || d.Rows != 2 || d.SHA == "" || d.HistoryUnknown != 2 || d.Eligible != 0 {
			t.Fatalf("%+v", d)
		}
	}
	if c.R1.V4.Version != DatasetVersionV4 || c.R1.V2.Version == DatasetVersionV4 || c.R1.V4.SHA == c.R1.V2.SHA {
		t.Fatalf("%+v %+v", c.R1.V4, c.R1.V2)
	}
	if !dailyFinding(c, "WARN", "history_unknown 2/2") {
		t.Fatal(c.Findings)
	}
	if len(c.IncidentOutcomes) != 1 || c.IncidentOutcomes[0].BatchID != ib.ID {
		t.Fatalf("%+v", c.IncidentOutcomes)
	}
	total := 0
	for _, n := range c.IncidentOutcomes[0].Counts {
		total += n
	}
	if total != 2 {
		t.Fatalf("%+v", c.IncidentOutcomes[0].Counts)
	}
}

// The fallback test end is never in the future; when unreachable it is an
// error, which BuildDailyCheck records instead of failing.
func TestDailyR1SpecTestEnd(t *testing.T) {
	cutoff := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	s, e := dailyR1Spec(7, cutoff, cutoff.Add(48*time.Hour))
	if e != nil || !s.TestEnd.Equal(cutoff.Add(48*time.Hour-time.Second)) || !s.Anchor.Equal(time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)) || s.BatchIDs[0] != 7 {
		t.Fatal(s, e)
	}
	early := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	if _, e := dailyR1Spec(7, time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC), early); e == nil {
		t.Fatal("test end after now accepted")
	}
}

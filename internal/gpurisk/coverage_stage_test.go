package gpurisk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// P1 taskcard §4 items 1, 3 and 7: daily coverage stage, identity watermark
// and outcome maturation from a ledger declaration.

func coverageStageDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPUNode{}, &api.HardwareFaultFeedbackRequest{}, &api.HardwareFaultFeedbackReview{}, &api.HistoricalGPUIdentityInterval{}, &api.MonitoringHistoryAudit{}, &api.HistoryBackfillRun{}, &api.GPURiskOperation{}); e != nil {
		t.Fatal(e)
	}
	return db
}

// coverageStageNow is the latest instant <= the real clock at which the
// Shanghai-day stage gate (>= 02:00) is open.
func coverageStageNow() time.Time {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	now := time.Now().UTC().Truncate(time.Microsecond)
	local := now.In(zone)
	if local.Hour() < 2 {
		now = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone).Add(-time.Second).UTC()
	}
	return now
}

// recordStageQuery freezes a two-member query at cutoff.
func recordStageQuery(t *testing.T, db *gorm.DB, cutoff time.Time) api.GPURiskQuery {
	t.Helper()
	p := fixture(t, 2)
	p.Cutoff = cutoff.UTC().Truncate(time.Microsecond)
	for i := range p.Nodes {
		p.Nodes[i].AvailableAt = p.Cutoff.Add(-time.Hour)
		node := api.GPUNode{ID: p.Nodes[i].ID, NodeIP: p.Nodes[i].IP, AssetSerial: p.Nodes[i].Serial, Lifecycle: "active"}
		if e := db.Where("id = ?", node.ID).FirstOrCreate(&node).Error; e != nil {
			t.Fatal(e)
		}
	}
	p = reseal(t, p)
	q, e := Record(context.Background(), db, p)
	if e != nil {
		t.Fatal(e)
	}
	// Synthetic historical fixture: pin completion shortly after the cutoff so
	// as-of evaluations at the stage clock are valid.
	completed := q.Cutoff.Add(time.Minute)
	if e := db.Model(&api.GPURiskQuery{}).Where("id = ?", q.ID).Update("completed_at", completed).Error; e != nil {
		t.Fatal(e)
	}
	q.CompletedAt = &completed
	return q
}

type stageSources struct {
	ledgerThrough     time.Time // zero: no declaration
	monitoringPartial bool
	identityEnd       time.Time // zero: no completed identity backfill
}

func seedStageSources(t *testing.T, db *gorm.DB, now time.Time, s stageSources) {
	t.Helper()
	if !s.ledgerThrough.IsZero() {
		if _, e := DeclareLedgerCoverage(context.Background(), db, s.ledgerThrough, "operator"); e != nil {
			t.Fatal(e)
		}
	}
	status, series := "success", 4104
	if s.monitoringPartial {
		status, series = "partial", 0
	}
	if e := db.Create(&api.MonitoringHistoryAudit{SourceKey: "vm", SourceName: "VictoriaMetrics", SourceType: "victoriametrics-cluster", BaseURL: "http://vm", Status: status, CurrentGPUSeries: series, StartedAt: now.Add(-31 * time.Minute), FinishedAt: now.Add(-30 * time.Minute)}).Error; e != nil {
		t.Fatal(e)
	}
	if !s.identityEnd.IsZero() {
		finished := s.identityEnd.Add(10 * time.Minute)
		if e := db.Create(&api.HistoryBackfillRun{SourceKey: DefaultIdentitySource, JobType: identityBackfillJobType, Status: "completed", QueryVersion: "gpu-identity-interval-v3", RangeStart: s.identityEnd.Add(-24 * time.Hour), RangeEnd: s.identityEnd, StartedAt: s.identityEnd, FinishedAt: &finished}).Error; e != nil {
			t.Fatal(e)
		}
	}
}

func coverageRows(t *testing.T, db *gorm.DB, queryID uint) map[string][]EvidenceInput {
	t.Helper()
	var rows []api.GPURiskEvidenceRevision
	if e := db.Where("kind = ?", "coverage").Order("id").Find(&rows).Error; e != nil {
		t.Fatal(e)
	}
	out := map[string][]EvidenceInput{}
	for _, r := range rows {
		var v EvidenceInput
		if e := json.Unmarshal([]byte(r.Data), &v); e != nil {
			t.Fatal(e)
		}
		if v.QueryID == queryID {
			out[v.Source] = append(out[v.Source], v)
		}
	}
	return out
}

func countRevisions(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if e := db.Model(&api.GPURiskEvidenceRevision{}).Count(&n).Error; e != nil {
		t.Fatal(e)
	}
	return n
}

// §4.1: three healthy sources -> 3 x N assertions for the newest query and for
// the matured pending query; a re-run in the same day mints nothing.
func TestCoverageStageAssertsThreeSourcesPerQueryAndIsIdempotent(t *testing.T) {
	db := coverageStageDB(t)
	now := coverageStageNow()
	matured := recordStageQuery(t, db, now.Add(-9*24*time.Hour))
	if _, e := RecordOutcomes(context.Background(), db, matured.ID, now); e != nil {
		t.Fatal(e)
	}
	latest := recordStageQuery(t, db, now.Add(-3*time.Hour))
	seedStageSources(t, db, now, stageSources{ledgerThrough: now.Add(-time.Hour), identityEnd: now.Add(-time.Hour)})
	targets, e := coverageTargetQueries(context.Background(), db, now)
	if e != nil || len(targets) != 2 || targets[0].ID != matured.ID || targets[1].ID != latest.ID {
		t.Fatal("target queries", targets, e)
	}
	if e := OperationsOnceWith(context.Background(), db, now, OperationsConfig{Coverage: true}); e != nil {
		t.Fatal(e)
	}
	for _, q := range []api.GPURiskQuery{matured, latest} {
		got := coverageRows(t, db, q.ID)
		total := 0
		for _, source := range coverageSources {
			if len(got[source]) != q.MemberCount {
				t.Fatalf("query %d source %s: %d assertions, want %d", q.ID, source, len(got[source]), q.MemberCount)
			}
			for _, v := range got[source] {
				if !v.Complete || v.Owner != CoverageOwner || v.From.After(q.Cutoff) || v.Through.Before(v.From) {
					t.Fatal("unexpected assertion", v)
				}
			}
			total += len(got[source])
		}
		if total != 3*q.MemberCount {
			t.Fatal("not 3 x N", total)
		}
	}
	var job api.GPURiskJob
	if e := db.Where("key = ?", coverageJobKey(now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02"))).First(&job).Error; e != nil || job.Status != "completed" || job.ErrorCode != "" {
		t.Fatal("job", job, e)
	}
	var op api.GPURiskOperation
	if e := db.Where("stage = ?", "coverage").First(&op).Error; e != nil || op.Status != "completed" || op.ErrorCode != "" {
		t.Fatal("operation", op, e)
	}
	var identityCheck api.GPURiskSourceCoverageCheck
	if e := db.Where("source = ?", "identity").Order("id DESC").First(&identityCheck).Error; e != nil || identityCheck.Contract != IdentityBackfillContract || identityCheck.Owner != CoverageOwner || !identityCheck.VerifiedThrough.Equal(now.Add(-time.Hour)) {
		t.Fatal("identity check", identityCheck, e)
	}
	before := countRevisions(t, db)
	var checksBefore int64
	db.Model(&api.GPURiskSourceCoverageCheck{}).Count(&checksBefore)
	// Same day: the job is completed, nothing runs.
	if e := CoverageDaily(context.Background(), db, now, CoverageOptions{}); e != nil {
		t.Fatal(e)
	}
	// Same state forced through the stage body again: heads are reused.
	if codes, e := runCoverageSources(context.Background(), db, now, CoverageOptions{}); e != nil || len(codes) != 0 {
		t.Fatal(codes, e)
	}
	var checksAfter int64
	db.Model(&api.GPURiskSourceCoverageCheck{}).Count(&checksAfter)
	if after := countRevisions(t, db); after != before || checksAfter != checksBefore {
		t.Fatal("re-run minted revisions or checks", before, after, checksBefore, checksAfter)
	}
}

// §4.1: one failing source writes no assertion, records its own code, and the
// other two sources are still asserted.
func TestCoverageStageFailsClosedPerSource(t *testing.T) {
	db := coverageStageDB(t)
	now := coverageStageNow()
	q := recordStageQuery(t, db, now.Add(-3*time.Hour))
	seedStageSources(t, db, now, stageSources{ledgerThrough: now.Add(-time.Hour), identityEnd: now.Add(-time.Hour), monitoringPartial: true})
	err := CoverageDaily(context.Background(), db, now, CoverageOptions{})
	var stage *CoverageStageError
	if !errors.As(err, &stage) || strings.Join(stage.Codes, ",") != "coverage_monitoring_incomplete" {
		t.Fatal("expected monitoring code only", err)
	}
	got := coverageRows(t, db, q.ID)
	if len(got["monitoring"]) != 0 || len(got["hardware_fault_ledger"]) != q.MemberCount || len(got["identity"]) != q.MemberCount {
		t.Fatal("per-source isolation", len(got["monitoring"]), len(got["hardware_fault_ledger"]), len(got["identity"]))
	}
	for _, rows := range got {
		for _, v := range rows {
			if !v.Complete || v.Source == "monitoring" {
				t.Fatal("synthesized assertion", v)
			}
		}
	}
	// The observation keeps the code from the job, also on later ticks.
	for i := 0; i < 2; i++ {
		if e := OperationsOnceWith(context.Background(), db, now, OperationsConfig{Coverage: true}); e != nil {
			t.Fatal(e)
		}
		var op api.GPURiskOperation
		if e := db.Where("stage = ?", "coverage").First(&op).Error; e != nil || op.Status != "blocked" || op.ErrorCode != "coverage_monitoring_incomplete" {
			t.Fatal("operation", op, e)
		}
	}
}

// §4.1: every source missing fails closed with three codes; nothing written.
func TestCoverageStageAllSourcesMissingWritesNothing(t *testing.T) {
	db := coverageStageDB(t)
	now := coverageStageNow()
	q := recordStageQuery(t, db, now.Add(-3*time.Hour))
	if e := db.Create(&api.MonitoringHistoryAudit{SourceKey: "vm", SourceName: "vm", SourceType: "vm", BaseURL: "http://vm", Status: "failed", StartedAt: now.Add(-time.Hour), FinishedAt: now.Add(-time.Hour)}).Error; e != nil {
		t.Fatal(e)
	}
	// A legacy read-at-now ledger check must not be used for members.
	if _, e := RecordSourceCoverage(context.Background(), db, CoverageCheckInput{Source: "hardware_fault_ledger", Owner: "legacy"}); e != nil {
		t.Fatal(e)
	}
	err := CoverageDaily(context.Background(), db, now, CoverageOptions{})
	var stage *CoverageStageError
	if !errors.As(err, &stage) || strings.Join(stage.Codes, ",") != "coverage_hardware_fault_ledger_incomplete,coverage_monitoring_incomplete,coverage_identity_incomplete" {
		t.Fatal(err)
	}
	if n := len(coverageRows(t, db, q.ID)); n != 0 {
		t.Fatal("assertions written", n)
	}
}

// The stage waits for today's ranking job and runs only after 02:00 Shanghai.
func TestCoverageStageWaitsForRankingAndSchedule(t *testing.T) {
	db := coverageStageDB(t)
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	early := time.Date(2026, 11, 12, 1, 59, 0, 0, zone)
	if e := CoverageDaily(context.Background(), db, early, CoverageOptions{}); e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 11, 12, 2, 1, 0, 0, zone)
	rankKey, _ := digest([]any{"daily", "2026-11-12", Contract, Target})
	if e := db.Create(&api.GPURiskJob{Key: rankKey, Status: "running", Attempt: 1}).Error; e != nil {
		t.Fatal(e)
	}
	if e := CoverageDaily(context.Background(), db, at, CoverageOptions{}); e != nil {
		t.Fatal(e)
	}
	var n int64
	db.Model(&api.GPURiskJob{}).Where("key = ?", coverageJobKey("2026-11-12")).Count(&n)
	if n != 0 {
		t.Fatal("coverage ran before ranking was terminal or before schedule")
	}
}

// §4.3: identity watermark is the RangeEnd of the newest completed backfill of
// the configured source, never the clock; none -> ErrCoverageSourceIncomplete.
func TestIdentityWatermarkIsNewestCompletedBackfillRangeEnd(t *testing.T) {
	db := coverageStageDB(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	previous := evidenceNow
	evidenceNow = func() time.Time { return at }
	t.Cleanup(func() { evidenceNow = previous })

	in := CoverageCheckInput{Source: "identity", Owner: "ops"}
	if _, e := RecordSourceCoverageV3(context.Background(), db, in, DefaultIdentitySource); !errors.Is(e, ErrCoverageSourceIncomplete) {
		t.Fatal("no backfill accepted", e)
	}
	// Non-completed runs and other sources do not count.
	finished := at.Add(-time.Minute)
	for _, r := range []api.HistoryBackfillRun{
		{SourceKey: DefaultIdentitySource, JobType: identityBackfillJobType, Status: "failed", RangeStart: at.Add(-48 * time.Hour), RangeEnd: at.Add(-2 * time.Hour), StartedAt: at.Add(-3 * time.Hour), FinishedAt: &finished},
		{SourceKey: DefaultIdentitySource, JobType: identityBackfillJobType, Status: "running", RangeStart: at.Add(-48 * time.Hour), RangeEnd: at.Add(-2 * time.Hour), StartedAt: at.Add(-3 * time.Hour)},
		{SourceKey: "other-source", JobType: identityBackfillJobType, Status: "completed", RangeStart: at.Add(-48 * time.Hour), RangeEnd: at.Add(-time.Minute), StartedAt: at.Add(-3 * time.Hour), FinishedAt: &finished},
		{SourceKey: DefaultIdentitySource, JobType: "gpu_fault_signal_onset", Status: "completed", RangeStart: at.Add(-48 * time.Hour), RangeEnd: at.Add(-time.Minute), StartedAt: at.Add(-3 * time.Hour), FinishedAt: &finished},
	} {
		if e := db.Create(&r).Error; e != nil {
			t.Fatal(e)
		}
	}
	if _, e := RecordSourceCoverageV3(context.Background(), db, in, DefaultIdentitySource); !errors.Is(e, ErrCoverageSourceIncomplete) {
		t.Fatal("non-completed or foreign run accepted", e)
	}
	var count int64
	db.Model(&api.GPURiskSourceCoverageCheck{}).Count(&count)
	if count != 0 {
		t.Fatal("refused identity check wrote a row")
	}
	olderEnd, newerEnd := at.Add(-30*time.Hour), at.Add(-5*time.Hour)
	f1, f2 := at.Add(-20*time.Hour), at.Add(-4*time.Hour)
	for _, r := range []api.HistoryBackfillRun{
		{SourceKey: DefaultIdentitySource, JobType: identityBackfillJobType, Status: "completed", RangeStart: olderEnd.Add(-24 * time.Hour), RangeEnd: olderEnd, StartedAt: f1, FinishedAt: &f1},
		{SourceKey: DefaultIdentitySource, JobType: identityBackfillJobType, Status: "completed", RangeStart: olderEnd, RangeEnd: newerEnd, StartedAt: f2, FinishedAt: &f2},
	} {
		if e := db.Create(&r).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e := db.Create(&api.HistoricalGPUIdentityInterval{IntervalKey: "interval-1", SourceKey: DefaultIdentitySource, NodeIP: "n", GPUUUID: "u"}).Error; e != nil {
		t.Fatal(e)
	}
	row, e := RecordSourceCoverageV3(context.Background(), db, in, DefaultIdentitySource)
	if e != nil {
		t.Fatal(e)
	}
	want, _ := digest([]string{"interval-1"})
	if !row.VerifiedThrough.Equal(newerEnd) || row.VerifiedThrough.Equal(at) || !row.ObservedAt.Equal(at) || row.Contract != IdentityBackfillContract || row.RecordCount != 1 || row.CheckSHA256 != want {
		t.Fatal("identity watermark", row)
	}
	// A completed run whose RangeEnd is in the future is not a watermark.
	future := at.Add(time.Minute)
	f3 := at
	if e := db.Create(&api.HistoryBackfillRun{SourceKey: DefaultIdentitySource, JobType: identityBackfillJobType, Status: "completed", RangeStart: newerEnd, RangeEnd: future, StartedAt: f3, FinishedAt: &f3}).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := RecordSourceCoverageV3(context.Background(), db, in, DefaultIdentitySource); !errors.Is(e, ErrCoverageSourceIncomplete) {
		t.Fatal("future watermark accepted", e)
	}
}

// Daily identity backfill: one start per day over [last RangeEnd, now-1h),
// capped at 48h; the stage waits for that run before judging identity.
func TestCoverageStageDailyIdentityBackfillWindowAndWait(t *testing.T) {
	db := coverageStageDB(t)
	now := coverageStageNow()
	q := recordStageQuery(t, db, now.Add(-3*time.Hour))
	lastEnd := now.Add(-100 * time.Hour).Truncate(time.Minute)
	seedStageSources(t, db, now, stageSources{ledgerThrough: now.Add(-time.Hour), identityEnd: lastEnd})
	type call struct {
		source     string
		start, end time.Time
	}
	calls := []call{}
	var started api.HistoryBackfillRun
	opts := CoverageOptions{IdentityBackfill: true, StartIdentityBackfill: func(_ context.Context, source string, start, end time.Time) (uint, error) {
		calls = append(calls, call{source, start, end})
		started = api.HistoryBackfillRun{SourceKey: source, JobType: identityBackfillJobType, Status: "running", RangeStart: start, RangeEnd: end, StartedAt: now}
		if e := db.Create(&started).Error; e != nil {
			return 0, e
		}
		return started.ID, nil
	}}
	// First tick starts the run and waits.
	if e := CoverageDaily(context.Background(), db, now, opts); e != nil {
		t.Fatal(e)
	}
	if len(calls) != 1 || calls[0].source != DefaultIdentitySource || !calls[0].start.Equal(lastEnd) || !calls[0].end.Equal(lastEnd.Add(IdentityBackfillMaxWindow)) {
		t.Fatal("window", calls)
	}
	if n := len(coverageRows(t, db, q.ID)); n != 0 {
		t.Fatal("stage did not wait for identity backfill", n)
	}
	// Second tick: still running, no second start.
	if e := CoverageDaily(context.Background(), db, now, opts); e != nil || len(calls) != 1 {
		t.Fatal("second start or error", calls, e)
	}
	finished := now
	if e := db.Model(&api.HistoryBackfillRun{}).Where("id = ?", started.ID).Updates(map[string]any{"status": "completed", "finished_at": &finished}).Error; e != nil {
		t.Fatal(e)
	}
	// Completed: identity is judged on the new watermark (lastEnd+48h), which
	// is still older than cutoff-24h, so identity fails closed alone.
	err := CoverageDaily(context.Background(), db, now, opts)
	var stage *CoverageStageError
	if !errors.As(err, &stage) || strings.Join(stage.Codes, ",") != "coverage_identity_incomplete" || len(calls) != 1 {
		t.Fatal(err, calls)
	}
	var check api.GPURiskSourceCoverageCheck
	if e := db.Where("source = ?", "identity").Order("id DESC").First(&check).Error; e != nil || !check.VerifiedThrough.Equal(lastEnd.Add(IdentityBackfillMaxWindow)) {
		t.Fatal("identity watermark not taken from the completed daily run", check, e)
	}
	got := coverageRows(t, db, q.ID)
	if len(got["identity"]) != 0 || len(got["hardware_fault_ledger"]) != q.MemberCount || len(got["monitoring"]) != q.MemberCount {
		t.Fatal("isolation", got)
	}
	// Uncapped window: end = now-1h.
	window := func(end time.Time) (time.Time, time.Time) {
		f := end
		if e := db.Create(&api.HistoryBackfillRun{SourceKey: DefaultIdentitySource, JobType: identityBackfillJobType, Status: "completed", RangeStart: end.Add(-time.Hour), RangeEnd: end, StartedAt: f, FinishedAt: &f}).Error; e != nil {
			t.Fatal(e)
		}
		s, en, e := identityBackfillWindow(context.Background(), db, DefaultIdentitySource, now)
		if e != nil {
			t.Fatal(e)
		}
		return s, en
	}
	recent := now.Add(-20 * time.Hour).Truncate(time.Minute)
	if s, en := window(recent); !s.Equal(recent) || !en.Equal(now.Add(-time.Hour).Truncate(time.Minute)) {
		t.Fatal("uncapped window", s, en)
	}
}

// §4.7: ledger declaration through >= end plus the two other sources matures a
// negative; a declaration through < end leaves the member pending_watermark.
func TestCoverageStageLedgerDeclarationDrivesOutcomeMaturity(t *testing.T) {
	db := coverageStageDB(t)
	now := coverageStageNow()
	q := recordStageQuery(t, db, now.Add(-9*24*time.Hour))
	end := q.Cutoff.Add(7 * 24 * time.Hour)
	if _, e := RecordOutcomes(context.Background(), db, q.ID, now); e != nil {
		t.Fatal(e)
	}
	seedStageSources(t, db, now, stageSources{ledgerThrough: end.Add(-time.Microsecond), identityEnd: now.Add(-time.Hour)})
	if codes, e := runCoverageSources(context.Background(), db, now, CoverageOptions{}); e != nil || len(codes) != 0 {
		t.Fatal(codes, e)
	}
	_, rows, e := EvaluateOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range rows {
		if r.Status != "pending_watermark" || r.Positive != nil || strings.Join(r.Blockers, ",") != "missing_coverage:hardware_fault_ledger" {
			t.Fatal("declaration before end matured", r)
		}
	}
	// The pending query is re-asserted once a later declaration covers end.
	if _, e := DeclareLedgerCoverage(context.Background(), db, end, "operator"); e != nil {
		t.Fatal(e)
	}
	targets, e := coverageTargetQueries(context.Background(), db, now)
	if e != nil || len(targets) != 1 || targets[0].ID != q.ID {
		t.Fatal("pending query not targeted", targets, e)
	}
	if codes, e := runCoverageSources(context.Background(), db, now, CoverageOptions{}); e != nil || len(codes) != 0 {
		t.Fatal(codes, e)
	}
	b, e := RecordOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	var stored []api.GPURiskNodeOutcome
	db.Where("batch_id = ?", b.ID).Order("node_id").Find(&stored)
	if len(stored) != 2 {
		t.Fatal(stored)
	}
	for _, r := range stored {
		if r.Status != "matured_negative" || r.Positive == nil || *r.Positive {
			t.Fatal("declared ledger did not mature negative", r.Status, r.Blockers)
		}
	}
	// The matured query is no longer a pending target.
	targets, e = coverageTargetQueries(context.Background(), db, now)
	if e != nil || len(targets) != 1 {
		t.Fatal(targets, e)
	}
	if fmt.Sprint(targets[0].ID) != fmt.Sprint(q.ID) {
		t.Fatal("latest query must still be targeted")
	}
}

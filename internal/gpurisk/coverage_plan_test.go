package gpurisk

import (
	"atlas/pkg/api"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCoveragePlanBoundariesHeadsAndReadOnly(t *testing.T) {
	db := coverageDB(t)
	cutoff := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Microsecond)
	completed := cutoff.Add(time.Second)
	q := api.GPURiskQuery{Key: "coverage-plan-query", ResultSHA: strings.Repeat("c", 64), ScopeSHA: strings.Repeat("a", 64), Contract: Contract, Target: Target, Cutoff: cutoff, HorizonMinutes: 10080, Status: "completed", MemberCount: 2, CompletedAt: &completed}
	if e := db.Create(&q).Error; e != nil {
		t.Fatal(e)
	}
	for i := uint(1); i <= 2; i++ {
		if e := db.Create(&api.GPURiskMember{QueryID: q.ID, NodeID: i, EntityRef: "node", Status: "included"}).Error; e != nil {
			t.Fatal(e)
		}
	}
	asOf := cutoff.Add(9 * 24 * time.Hour)
	appendRevision := func(key string, rev int, available time.Time, complete bool, from, through time.Time) {
		v := EvidenceInput{Kind: "coverage", Key: key, QueryID: q.ID, NodeID: 1, EntityRef: "node", Source: "hardware_fault_ledger", Complete: complete, From: from, Through: through}
		raw, _ := json.Marshal(v)
		sha, _ := digest([]any{key, rev, available, complete})
		if e := db.Create(&api.GPURiskEvidenceRevision{Kind: "coverage", Key: key, Revision: rev, AvailableAt: available, Data: string(raw), SHA: sha}).Error; e != nil {
			t.Fatal(e)
		}
	}
	for _, offset := range []time.Duration{-time.Microsecond, 0, time.Microsecond} {
		key := offset.String()
		appendRevision(key, 1, cutoff.Add(offset), true, cutoff.Add(-2*time.Hour), cutoff.Add(-time.Hour))
	}
	appendRevision("outcome-window", 1, cutoff.Add(8*24*time.Hour), true, cutoff, cutoff.Add(7*24*time.Hour))
	report, e := BuildCoveragePlan(t.Context(), db, q.ID, asOf)
	if e != nil {
		t.Fatal(e)
	}
	s := report.Sources[0]
	if report.Members != 2 || s.MemberAssertions != 1 || s.OutcomeWindowAssertions != 1 || s.CutoffSafeHistoryCandidates != 1 || s.CutoffSafeHistoryMissing != 1 || s.CheckStatus != "missing" {
		t.Fatal(report)
	}
	again, e := BuildCoveragePlan(t.Context(), db, q.ID, asOf)
	if e != nil || again.SHA != report.SHA {
		t.Fatal(e, "unstable SHA")
	}
	// Latest incomplete revision supersedes the pre-cutoff positive chain in
	// the as-of view, but cannot retroactively overwrite the cutoff view.
	for _, offset := range []time.Duration{-time.Microsecond, 0, time.Microsecond} {
		appendRevision(offset.String(), 2, cutoff.Add(time.Hour), false, cutoff.Add(-time.Hour), cutoff.Add(7*24*time.Hour))
	}
	appendRevision("outcome-window", 2, cutoff.Add(8*24*time.Hour+time.Hour), false, cutoff, cutoff.Add(7*24*time.Hour))
	revised, e := BuildCoveragePlan(t.Context(), db, q.ID, asOf)
	if e != nil {
		t.Fatal(e)
	}
	if revised.SHA == report.SHA || revised.Sources[0].MemberAssertions != 0 || revised.Sources[0].OutcomeWindowAssertions != 0 || revised.Sources[0].CutoffSafeHistoryCandidates != 1 {
		t.Fatal(revised)
	}
	check := api.GPURiskSourceCoverageCheck{Source: "identity", VerifiedThrough: cutoff.Add(-MaxSourceLag), ObservedAt: cutoff, CreatedAt: cutoff, Contract: CoverageWatermarkContract, CheckSHA256: strings.Repeat("b", 64)}
	if e := db.Create(&check).Error; e != nil {
		t.Fatal(e)
	}
	future := check
	future.ID = 0
	future.ObservedAt = asOf.Add(time.Microsecond)
	future.VerifiedThrough = asOf
	if e := db.Create(&future).Error; e != nil {
		t.Fatal(e)
	}
	checked, e := BuildCoveragePlan(t.Context(), db, q.ID, asOf)
	if e != nil {
		t.Fatal(e)
	}
	if checked.Sources[1].Check == nil {
		t.Fatal("missing valid check", checked)
	}
	if checked.Sources[1].Check.ID != check.ID || checked.Sources[1].CheckStatus != "within_query_tail" || !checked.Sources[1].CheckStaleAtAsOf {
		t.Fatal(checked.Sources[1])
	}
	check.ID = 0
	check.VerifiedThrough = cutoff.Add(-MaxSourceLag - time.Microsecond)
	check.ObservedAt = cutoff.Add(time.Second)
	if e := db.Create(&check).Error; e != nil {
		t.Fatal(e)
	}
	stale, e := BuildCoveragePlan(t.Context(), db, q.ID, asOf)
	if e != nil || stale.Sources[1].CheckStatus != "stale_for_query" {
		t.Fatal(e, stale)
	}
	var n int64
	db.Model(&api.GPURiskEvidenceRevision{}).Count(&n)
	if n != 8 {
		t.Fatal("report wrote evidence", n)
	}
	if revised.Sources[0].ConflictingMembers != 1 {
		t.Fatal("incomplete assertions not reported", revised)
	}
	// Newest invalid source check must be exposed instead of falling back.
	check.ID = 0
	check.ObservedAt = cutoff.Add(2 * time.Second)
	check.CheckSHA256 = "invalid"
	if e := db.Create(&check).Error; e != nil {
		t.Fatal(e)
	}
	invalid, e := BuildCoveragePlan(t.Context(), db, q.ID, asOf)
	if e != nil || invalid.Sources[1].CheckStatus != "invalid" {
		t.Fatal(e, invalid)
	}
	if path := os.Getenv("ATLAS_COVERAGE_PLAN_FIXTURE"); path != "" {
		if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			t.Fatal(e)
		}
		if e := db.Exec("VACUUM INTO ?", path).Error; e != nil {
			t.Fatal(e)
		}
		t.Logf("fixture=%s query=%d as_of=%s", path, q.ID, asOf.Format(time.RFC3339Nano))
	}
	for _, at := range []time.Time{time.Time{}, cutoff, time.Now().Add(time.Hour)} {
		if _, e := BuildCoveragePlan(t.Context(), db, q.ID, at); e == nil {
			t.Fatal("accepted invalid asof", at)
		}
	}
}

func TestCoveragePlanExactAvailabilityBoundary(t *testing.T) {
	for _, offset := range []time.Duration{-time.Microsecond, 0, time.Microsecond} {
		t.Run(offset.String(), func(t *testing.T) {
			db := coverageDB(t)
			cutoff := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
			completed := cutoff.Add(time.Second)
			asOf := cutoff.Add(time.Hour)
			q := api.GPURiskQuery{Key: strings.Repeat("d", 64), ScopeSHA: strings.Repeat("a", 64), ResultSHA: strings.Repeat("b", 64), Contract: Contract, Target: Target, Cutoff: cutoff, HorizonMinutes: 10080, Status: "completed", MemberCount: 1, CompletedAt: &completed}
			if e := db.Create(&q).Error; e != nil {
				t.Fatal(e)
			}
			if e := db.Create(&api.GPURiskMember{QueryID: q.ID, NodeID: 1, EntityRef: "node", Status: "included"}).Error; e != nil {
				t.Fatal(e)
			}
			v := EvidenceInput{Kind: "coverage", Key: "boundary", QueryID: q.ID, NodeID: 1, EntityRef: "node", Source: "hardware_fault_ledger", Complete: true, From: cutoff.Add(-time.Hour), Through: cutoff.Add(-time.Minute)}
			data, _ := json.Marshal(v)
			r := api.GPURiskEvidenceRevision{Kind: v.Kind, Key: v.Key, Revision: 1, AvailableAt: cutoff.Add(offset), Data: string(data), SHA: strings.Repeat("c", 64)}
			if e := db.Create(&r).Error; e != nil {
				t.Fatal(e)
			}
			report, e := BuildCoveragePlan(t.Context(), db, q.ID, asOf)
			if e != nil {
				t.Fatal(e)
			}
			expected := 0
			if offset < 0 {
				expected = 1
			}
			if report.Sources[0].CutoffSafeHistoryCandidates != expected {
				t.Fatal("cutoff boundary", offset, report)
			}
			// A row just beyond as_of can pass SQLite's coarse julianday SQL
			// prefilter; the exact Go filter must exclude it.
			r.ID = 0
			r.Revision = 2
			r.SHA = strings.Repeat("e", 64)
			r.AvailableAt = asOf.Add(time.Microsecond)
			v.Complete = false
			r.Data = ""
			raw, _ := json.Marshal(v)
			r.Data = string(raw)
			if e := db.Create(&r).Error; e != nil {
				t.Fatal(e)
			}
			again, e := BuildCoveragePlan(t.Context(), db, q.ID, asOf)
			if e != nil || again.SHA != report.SHA {
				t.Fatal("future evidence changed asof view", e, again)
			}
		})
	}
}

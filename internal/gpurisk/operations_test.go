package gpurisk

import (
	"atlas/pkg/api"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOperationsBoundedStateAndIndependentSwitches(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPURiskOperation{}); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.FixedZone("Shanghai", 8*3600))
	if e := OperationsOnce(context.Background(), db, now, false, false); e != nil {
		t.Fatal(e)
	}
	var n int64
	db.Model(&api.GPURiskOperation{}).Count(&n)
	if n != 0 {
		t.Fatal("disabled wrote")
	}
	for i := 0; i < 3; i++ {
		if e := OperationsOnce(context.Background(), db, now, true, false); e != nil {
			t.Fatal(e)
		}
	}
	var row api.GPURiskOperation
	if e := db.First(&row).Error; e != nil {
		t.Fatal(e)
	}
	if row.Stage != "ranking" || row.Status != "waiting_schedule" {
		t.Fatal(row)
	}
	observed := row.ObservedAt
	if e := OperationsOnce(context.Background(), db, now, true, false); e != nil {
		t.Fatal(e)
	}
	db.First(&row)
	if !row.ObservedAt.Equal(observed) {
		t.Fatal("heartbeat mutated durable record")
	}
	db.Model(&api.GPURiskJob{}).Count(&n)
	if n != 0 {
		t.Fatal("before-schedule job write")
	}
	if e := OperationsOnce(context.Background(), db, now, false, true); e != nil {
		t.Fatal(e)
	}
	db.Model(&api.GPURiskOperation{}).Count(&n)
	if n != 2 {
		t.Fatal(n)
	}
	db.Model(&api.GPURiskJob{}).Count(&n)
	if n != 0 {
		t.Fatal("outcome-only wrote ranking")
	}
	if e := OperationsOnce(context.Background(), db, now.Add(24*time.Hour), true, false); e != nil {
		t.Fatal(e)
	}
	db.Model(&api.GPURiskOperation{}).Count(&n)
	if n != 3 {
		t.Fatal("new day did not advance")
	}
}
func TestOperationsExhaustionAndReadOnlyStatus(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPURiskOperation{}); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.FixedZone("Shanghai", 8*3600))
	key, _ := digest([]any{"daily", "2026-10-28", Contract, Target})
	j := api.GPURiskJob{Key: key, Status: "failed", Attempt: 2, Input: "frozen-input"}
	if e := db.Create(&j).Error; e != nil {
		t.Fatal(e)
	}
	if e := OperationsOnce(context.Background(), db, now, true, false); e != nil {
		t.Fatal(e)
	}
	var row api.GPURiskOperation
	db.First(&row)
	if row.Status != "manual_required" || row.Exhausted != 1 {
		t.Fatal(row)
	}
	db.First(&j)
	if j.Input != "frozen-input" || j.Attempt != 2 {
		t.Fatal("budget/input modified")
	}
	var before int64
	db.Model(&api.GPURiskOperation{}).Count(&before)
	h := Handler{DB: db, AutomaticJobsEnabled: true, OperationsEnabled: true}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/gpu-risk/operations", nil))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var after int64
	db.Model(&api.GPURiskOperation{}).Count(&after)
	if before != after {
		t.Fatal("GET wrote state")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/gpu-risk/operations", nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
	var checks int64
	db.Model(&api.GPURiskSourceCoverageCheck{}).Count(&checks)
	if checks != 0 {
		t.Fatal("source signing occurred")
	}
}

func TestOperationsOutcomeRestartPreservesPinnedHistory(t *testing.T) {
	db, q, _ := outcomeDB(t)
	if e := db.AutoMigrate(&api.GPURiskOperation{}); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	local := now.In(time.FixedZone("Shanghai", 8*3600))
	if local.Hour() < 3 {
		now = now.Add(3 * time.Hour)
	}
	before, _, e := EvaluateOutcomes(context.Background(), db, q.ID, now)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		if e := OperationsOnce(context.Background(), db, now, false, true); e != nil {
			t.Fatal(e)
		}
	}
	after, _, e := EvaluateOutcomes(context.Background(), db, q.ID, now)
	if e != nil {
		t.Fatal(e)
	}
	if before.SHA != after.SHA {
		t.Fatal("old outcome changed")
	}
	var n int64
	db.Model(&api.GPURiskOutcomeBatch{}).Count(&n)
	if n != 1 {
		t.Fatal("duplicate outcome", n)
	}
	db.Model(&api.GPURiskOperation{}).Count(&n)
	if n != 1 {
		t.Fatal("duplicate operation", n)
	}
	var row api.GPURiskOperation
	db.First(&row)
	if row.Status != "completed" {
		t.Fatal(row)
	}
}

func TestOperationsConcurrentObserversAndRollback(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPURiskOperation{}); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.FixedZone("Shanghai", 8*3600))
	key, _ := digest([]any{"daily", "2026-10-28", Contract, Target})
	job := api.GPURiskJob{Key: key, Status: "completed", Attempt: 1, Input: "frozen"}
	if e := db.Create(&job).Error; e != nil {
		t.Fatal(e)
	}
	// An observer cannot read a stale source while another transaction owns the
	// writer reservation. It must see the source committed before lock release.
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	seed := api.GPURiskOperation{Day: "2026-10-28", Stage: "ranking", Status: "running", SHA: "old", ObservedAt: time.Now().Add(-time.Hour)}
	if e := tx.Create(&seed).Error; e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- observeOperation(context.Background(), db, now, "ranking", nil) }()
	select {
	case e := <-done:
		t.Fatal("observer bypassed reservation", e)
	case <-time.After(30 * time.Millisecond):
	}
	if e := tx.Commit().Error; e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- observeOperation(context.Background(), db, now, "ranking", nil) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var rows []api.GPURiskOperation
	db.Find(&rows)
	if len(rows) != 1 || rows[0].Status != "completed" {
		t.Fatal(rows)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := observeOperation(ctx, db, now, "outcomes", nil); e == nil {
		t.Fatal("cancel ignored")
	}
	var n int64
	db.Model(&api.GPURiskOperation{}).Where("stage = ?", "outcomes").Count(&n)
	if n != 0 {
		t.Fatal("cancel published partial observation")
	}
}

func TestOperationsStatusInspectionFailureAndMissingTableAreReadOnly(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPURiskOperation{}); e != nil {
		t.Fatal(e)
	}
	h := Handler{DB: db}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/gpu-risk/operations", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "no_completed_query") {
		t.Fatal(w.Code, w.Body.String())
	}
	at := time.Now().Add(-time.Hour)
	q := api.GPURiskQuery{Key: "invalid-query-fixture", Status: "completed", CompletedAt: &at, Cutoff: at, HorizonMinutes: 10080}
	if e := db.Create(&q).Error; e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/gpu-risk/operations", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "inspection_failed") {
		t.Fatal(w.Code, w.Body.String())
	}
	var n int64
	db.Model(&api.GPURiskJob{}).Count(&n)
	if n != 0 {
		t.Fatal("GET ran daily job")
	}
	db.Model(&api.GPURiskEvidenceRevision{}).Count(&n)
	if n != 0 {
		t.Fatal("GET signed evidence")
	}
	if e := db.Migrator().DropTable(&api.GPURiskOperation{}); e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/gpu-risk/operations", nil))
	if w.Code != 503 || db.Migrator().HasTable(&api.GPURiskOperation{}) {
		t.Fatal("GET migrated missing table")
	}
}

func TestOperationsReadFailureDoesNotMasqueradeAsCurrentHealth(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPURiskOperation{}); e != nil {
		t.Fatal(e)
	}
	old := api.GPURiskOperation{Day: "2026-10-28", Stage: "ranking", Status: "completed", SHA: "prior"}
	if e := db.Create(&old).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Migrator().DropTable(&api.GPURiskJob{}); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	if e := observeOperation(context.Background(), db, now, "ranking", nil); e == nil {
		t.Fatal("source failure hidden")
	}
	var n int64
	db.Model(&api.GPURiskOperation{}).Count(&n)
	if n != 1 {
		t.Fatal("partial record published")
	}
	w := httptest.NewRecorder()
	(Handler{DB: db}).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/gpu-risk/operations", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "inspection_failed") || !strings.Contains(w.Body.String(), "last_content_change_not_loop_heartbeat_or_current_health") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestOperationsPreserveFixedCutoffHistoryFeatureSHA(t *testing.T) {
	db, q, _ := outcomeDB(t)
	if e := db.AutoMigrate(&api.GPURiskOperation{}); e != nil {
		t.Fatal(e)
	}
	// Seed explicit pre-cutoff context so the proof covers nonempty historical
	// source content, not merely a calendar-only or zero-evidence feature vector.
	source := sourceRow(sourceInput(q), q.Cutoff.Add(-time.Minute), 1)
	if e := db.Create(&source).Error; e != nil {
		t.Fatal(e)
	}
	var member api.GPURiskMember
	if e := db.Where("query_id = ? AND node_id = ?", q.ID, 1).First(&member).Error; e != nil {
		t.Fatal(e)
	}
	historySHA := func() string {
		var evs []api.GPURiskEvidenceRevision
		var reviews []api.HardwareFaultFeedbackReview
		if e := db.Order("id").Find(&evs).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Order("id").Find(&reviews).Error; e != nil {
			t.Fatal(e)
		}
		f, e := historyAt(context.Background(), member, q, evs, reviews)
		if e != nil {
			t.Fatal(e)
		}
		if f.Categorical["gpu_model"] == nil || *f.Categorical["gpu_model"] != "fixture-model" {
			t.Fatal("fixture did not exercise real pre-cutoff context", f)
		}
		v, e := digest([]any{member.NodeID, member.Generation, q.Cutoff, f})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	beforeFeature := historySHA()
	at := time.Now().UTC()
	local := at.In(time.FixedZone("Shanghai", 8*3600))
	if local.Hour() < 3 {
		at = at.Add(3 * time.Hour)
	}
	beforeOutcome, _, e := EvaluateOutcomes(context.Background(), db, q.ID, at)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		if e := OperationsOnce(context.Background(), db, at, false, true); e != nil {
			t.Fatal(e)
		}
	}
	afterFeature := historySHA()
	afterOutcome, _, e := EvaluateOutcomes(context.Background(), db, q.ID, at)
	if e != nil {
		t.Fatal(e)
	}
	if beforeFeature != afterFeature || beforeOutcome.SHA != afterOutcome.SHA {
		t.Fatal("fixed historical feature or outcome changed")
	}
	var frozen api.GPURiskQuery
	if e = db.First(&frozen, q.ID).Error; e != nil {
		t.Fatal(e)
	}
	if frozen.ScopeSHA != q.ScopeSHA || frozen.ResultSHA != q.ResultSHA {
		t.Fatal("frozen ranking modified")
	}
	t.Logf("fixed_query=%d node=%d generation=%d cutoff=%s as_of=%s scope_sha=%s result_sha=%s feature_before=%s feature_after=%s outcome_before=%s outcome_after=%s", q.ID, member.NodeID, member.Generation, q.Cutoff.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), q.ScopeSHA, q.ResultSHA, beforeFeature, afterFeature, beforeOutcome.SHA, afterOutcome.SHA)
}

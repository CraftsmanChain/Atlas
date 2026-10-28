package gpurisk

import (
	"context"
	"encoding/json"
	"fmt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm/logger"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"atlas/internal/prediction"
	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

func outcomeDB(t *testing.T) (*gorm.DB, api.GPURiskQuery, Snapshot) {
	t.Helper()
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPUNode{}, &api.HardwareFaultFeedbackRequest{}, &api.HardwareFaultFeedbackReview{}); e != nil {
		t.Fatal(e)
	}
	p := fixture(t, 2)
	p.Cutoff = time.Now().UTC().Add(-9 * 24 * time.Hour).Truncate(time.Microsecond)
	for i := range p.Nodes {
		p.Nodes[i].AvailableAt = p.Cutoff.Add(-time.Hour)
		if e := db.Create(&api.GPUNode{ID: p.Nodes[i].ID, NodeIP: p.Nodes[i].IP, AssetSerial: p.Nodes[i].Serial, Lifecycle: "active"}).Error; e != nil {
			t.Fatal(e)
		}
	}
	p = reseal(t, p)
	q, e := Record(context.Background(), db, p)
	if e != nil {
		t.Fatal(e)
	}
	return db, q, p
}
func appendInput(t *testing.T, db *gorm.DB, v EvidenceInput) api.GPURiskEvidenceRevision {
	t.Helper()
	r, e := AppendEvidence(context.Background(), db, v)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func cover(t *testing.T, db *gorm.DB, q api.GPURiskQuery, node uint, source string, complete bool) api.GPURiskEvidenceRevision {
	return appendInput(t, db, EvidenceInput{Kind: "coverage", Key: fmt.Sprintf("%d-%s", node, source), Owner: "reviewer", EvidenceSHA: strings.Repeat("a", 64), NodeID: node, QueryID: q.ID, EntityRef: fmt.Sprintf("serial:sn-%d", node), From: q.Cutoff, Through: q.Cutoff.Add(7 * 24 * time.Hour), Source: source, Complete: complete})
}
func evaluate(t *testing.T, db *gorm.DB, q api.GPURiskQuery) (api.GPURiskOutcomeBatch, []api.GPURiskNodeOutcome) {
	t.Helper()
	b, r, e := EvaluateOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	return b, r
}
func seedReview(t *testing.T, db *gorm.DB, q api.GPURiskQuery, key string, delta time.Duration) (api.HardwareFaultFeedbackRequest, api.HardwareFaultFeedbackReview) {
	t.Helper()
	onset := q.Cutoff.Add(delta)
	f := api.HardwareFaultFeedbackRequest{RequestKey: key, Status: "reported", NodeIP: "node-1", TargetScope: "node", FaultType: "gpu_hardware_failure", FaultOccurredAt: onset, Operator: "ops", CreatedAt: time.Now().UTC()}
	if e := db.Create(&f).Error; e != nil {
		t.Fatal(e)
	}
	r := api.HardwareFaultFeedbackReview{FeedbackRequestID: f.ID, Revision: 1, Decision: "confirmed_hardware", Reviewer: "reviewer", ReviewNote: "fixture", ConfirmedOnsetAt: &onset, TargetScope: "node", EpisodeKey: "same-episode", EvidenceKeys: api.StringList{"fixture-proof"}, ReviewSHA256: strings.Repeat(fmt.Sprint(f.ID%10), 64), ReviewedAt: time.Now().UTC()}
	if e := db.Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	return f, r
}
func identity(t *testing.T, db *gorm.DB, q api.GPURiskQuery, f api.HardwareFaultFeedbackRequest, r api.HardwareFaultFeedbackReview) api.GPURiskEvidenceRevision {
	return appendInput(t, db, EvidenceInput{Kind: "identity", Key: fmt.Sprint("identity-", f.ID), Owner: "reviewer", EvidenceSHA: strings.Repeat("b", 64), NodeID: 1, EntityRef: "serial:sn-1", FeedbackID: f.ID, ReviewSHA: r.ReviewSHA256, From: q.Cutoff.Add(-time.Hour), Through: q.Cutoff.Add(7 * 24 * time.Hour)})
}
func TestOutcomeNegativeRequiresAllSourcesAndNoProbability(t *testing.T) {
	db, q, _ := outcomeDB(t)
	_, rows := evaluate(t, db, q)
	if rows[0].Status != "pending_watermark" || rows[0].Positive != nil {
		t.Fatal(rows)
	}
	for _, s := range []string{"hardware_fault_ledger", "monitoring", "identity"} {
		cover(t, db, q, 1, s, true)
	}
	b, rows := evaluate(t, db, q)
	if rows[0].Status != "matured_negative" || rows[0].Positive == nil || *rows[0].Positive || rows[1].Status != "pending_watermark" {
		t.Fatal(rows)
	}
	first, e := RecordOutcomes(context.Background(), db, q.ID, b.AsOf)
	if e != nil {
		t.Fatal(e)
	}
	again, e := RecordOutcomes(context.Background(), db, q.ID, b.AsOf)
	if e != nil || again.ID != first.ID || again.SHA != first.SHA {
		t.Fatal(again, e)
	}
	oldSHA := b.SHA
	f, r := seedReview(t, db, q, "late", 24*time.Hour)
	identity(t, db, q, f, r)
	later, rows := evaluate(t, db, q)
	if rows[0].Status != "matured_positive" || rows[0].Positive == nil || !*rows[0].Positive || later.SHA == oldSHA {
		t.Fatal(rows)
	}
	replay, _, e := EvaluateOutcomes(context.Background(), db, q.ID, b.AsOf)
	if e != nil || replay.SHA != oldSHA {
		t.Fatal("old as-of changed", replay.SHA, e)
	}
	var stored api.GPURiskOutcomeBatch
	db.First(&stored, first.ID)
	if stored.SHA != oldSHA {
		t.Fatal("overwrote immutable batch")
	}
	// Appending a withdrawal review removes the old positive, without changing old batches.
	r.ID = 0
	r.Revision = 2
	r.PreviousReviewSHA256 = r.ReviewSHA256
	r.ReviewSHA256 = strings.Repeat("e", 64)
	r.Decision = "excluded_no_hardware"
	r.CreatedAt = time.Time{}
	r.ReviewedAt = time.Now().UTC()
	if e := db.Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	_, rows = evaluate(t, db, q)
	if rows[0].Status != "matured_negative" {
		t.Fatal(rows)
	}
	replay, _, e = EvaluateOutcomes(context.Background(), db, q.ID, b.AsOf)
	if e != nil || replay.SHA != oldSHA {
		t.Fatal("withdrawal changed old as-of", e)
	}
}
func TestEpisodeDedupMappingAndBoundary(t *testing.T) {
	db, q, _ := outcomeDB(t)
	f, r := seedReview(t, db, q, "multi-a", time.Hour)
	identity(t, db, q, f, r)
	f2, r2 := seedReview(t, db, q, "multi-b", time.Hour)
	identity(t, db, q, f2, r2)
	_, rows := evaluate(t, db, q)
	if rows[0].Status != "matured_positive" || len(rows[0].EpisodeKeys) != 1 {
		t.Fatal("multi-source episode multiplied", rows)
	}
	// A revision must cover the whole ambiguous onset interval; crossing q is not a hit.
	r.ID = 0
	r.Revision = 2
	r.ReviewSHA256 = strings.Repeat("d", 64)
	a, b := q.Cutoff.Add(-time.Minute), q.Cutoff.Add(time.Minute)
	r.ConfirmedWindowStartAt = &a
	r.ConfirmedWindowEndAt = &b
	r.ReviewedAt = time.Now().UTC()
	r.CreatedAt = time.Time{}
	db.Create(&r)
	v := EvidenceInput{Kind: "identity", Key: "boundary", Owner: "reviewer", EvidenceSHA: strings.Repeat("c", 64), NodeID: 1, EntityRef: "serial:sn-1", FeedbackID: f.ID, ReviewSHA: r.ReviewSHA256, From: q.Cutoff.Add(-time.Hour), Through: q.Cutoff.Add(time.Hour)}
	appendInput(t, db, v)
	_, rows = evaluate(t, db, q)
	if rows[0].Status != "identity_conflict" {
		t.Fatal("conflicting same-episode windows were accepted", rows)
	}
}
func TestEvidenceCASInterventionsAndIncompleteCoverage(t *testing.T) {
	db, q, _ := outcomeDB(t)
	v := EvidenceInput{Kind: "coverage", Key: "revision", Owner: "ops", EvidenceSHA: strings.Repeat("a", 64), NodeID: 1, QueryID: q.ID, EntityRef: "serial:sn-1", From: q.Cutoff, Through: q.Cutoff.Add(7 * 24 * time.Hour), Source: "monitoring", Complete: false}
	one := appendInput(t, db, v)
	again := appendInput(t, db, v)
	if one.ID != again.ID {
		t.Fatal("evidence retry duplicated")
	}
	v.Complete = true
	if _, e := AppendEvidence(context.Background(), db, v); e != ErrFence {
		t.Fatal("missing CAS accepted", e)
	}
	v.PreviousSHA = one.SHA
	two := appendInput(t, db, v)
	if two.Revision != 2 || two.PreviousSHA != one.SHA {
		t.Fatal(two)
	}
	cover(t, db, q, 1, "hardware_fault_ledger", false)
	cover(t, db, q, 1, "identity", true)
	_, rows := evaluate(t, db, q)
	if rows[0].Status != "censored" {
		t.Fatal(rows)
	}
	f, r := seedReview(t, db, q, "context", time.Hour)
	r.ID = 0
	r.Revision = 2
	r.Decision = "context_only"
	r.ReviewSHA256 = strings.Repeat("f", 64)
	r.CreatedAt = time.Time{}
	r.ReviewedAt = time.Now().UTC()
	db.Create(&r)
	appendInput(t, db, EvidenceInput{Kind: "repair", Key: "repair", Owner: "ops", EvidenceSHA: strings.Repeat("a", 64), NodeID: 1, EntityRef: "serial:sn-1", FeedbackID: f.ID, ReviewSHA: r.ReviewSHA256, From: q.Cutoff.Add(time.Hour), Through: q.Cutoff.Add(2 * time.Hour), State: "recovered"})
	_, rows = evaluate(t, db, q)
	if rows[0].Status != "intervention_affected" || rows[0].Positive != nil {
		t.Fatal("repair converted to false positive", rows)
	}
}
func TestOutcomeHTTPReadOnlyAndBatchReplay(t *testing.T) {
	db, q, _ := outcomeDB(t)
	h := Handler{DB: db}
	path := fmt.Sprintf("/api/v1/gpu-risk/queries/%d/outcomes", q.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "collecting") {
		t.Fatal(w.Code, w.Body.String())
	}
	b, e := RecordOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("%s?batch_id=%d&limit=1", path, b.ID), nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct{ Data []api.GPURiskNodeOutcome }
	json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Data) != 1 {
		t.Fatal(body)
	}
	req := httptest.NewRequest("GET", fmt.Sprintf("%s?batch_id=%d&limit=1", path, b.ID), nil)
	req.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 304 {
		t.Fatal(w.Code)
	}
	var count int64
	db.Model(&api.GPURiskOutcomeBatch{}).Count(&count)
	if count != 1 {
		t.Fatal("GET wrote outcome")
	}
}

// Exercise the existing public feedback/review service, then bind its immutable
// review to the new node-outcome path, rather than only seeding fake review rows.
func TestExistingFeedbackToNodeOutcomeEndToEnd(t *testing.T) {
	store, e := storage.InitDB(t.TempDir() + "/integration.db")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := store.DB.DB()
	defer raw.Close()
	p := fixture(t, 1)
	p.Cutoff = time.Now().UTC().Add(-9 * 24 * time.Hour)
	p.Nodes[0].AvailableAt = p.Cutoff.Add(-time.Hour)
	p = reseal(t, p)
	store.Create(&api.GPUNode{ID: 1, NodeIP: "node-1", AssetSerial: "sn-1"})
	q, e := Record(context.Background(), store.DB, p)
	if e != nil {
		t.Fatal(e)
	}
	service := prediction.NewService(store)
	onset := p.Cutoff.Add(time.Hour)
	f, e := service.CreateHardwareFaultFeedback(prediction.HardwareFaultFeedbackInput{NodeIP: "node-1", TargetScope: "node", FaultType: "gpu_hardware_failure", FaultOccurredAt: onset.Format(time.RFC3339), Operator: "ops"})
	if e != nil {
		t.Fatal(e)
	}
	review, e := service.ReviewHardwareFaultFeedback(f.ID, prediction.HardwareFaultFeedbackReviewInput{Decision: "confirmed_hardware", Reviewer: "ops", ReviewNote: "fixed node evidence", TargetScope: "node", EpisodeKey: "integration-episode", EvidenceKeys: []string{"fixture:node-repair"}})
	if e != nil {
		t.Fatal(e)
	}
	identity(t, store.DB, q, f, review)
	_, rows := evaluate(t, store.DB, q)
	if rows[0].Status != "matured_positive" {
		t.Fatal(rows)
	}
}

func TestEvidenceRejectsFutureWrongReviewAndRetargetedCoverage(t *testing.T) {
	db, q, _ := outcomeDB(t)
	v := EvidenceInput{Kind: "coverage", Key: "future", Owner: "ops", EvidenceSHA: strings.Repeat("a", 64), NodeID: 1, QueryID: q.ID, EntityRef: "serial:sn-1", From: q.Cutoff, Through: time.Now().Add(time.Hour), Source: "monitoring", Complete: true}
	if _, e := AppendEvidence(context.Background(), db, v); e == nil {
		t.Fatal("future coverage accepted")
	}
	v.Through = q.Cutoff.Add(7 * 24 * time.Hour)
	one := appendInput(t, db, v)
	v.PreviousSHA = one.SHA
	v.NodeID = 2
	v.EntityRef = "serial:sn-2"
	if _, e := AppendEvidence(context.Background(), db, v); e == nil {
		t.Fatal("coverage revision moved between nodes")
	}
	_, r := seedReview(t, db, q, "unknown-mapping", time.Hour)
	v = EvidenceInput{Kind: "identity", Key: "wrong", Owner: "ops", EvidenceSHA: strings.Repeat("a", 64), NodeID: 1, EntityRef: "serial:sn-1", FeedbackID: r.FeedbackRequestID, ReviewSHA: strings.Repeat("0", 64), From: q.Cutoff, Through: q.Cutoff.Add(7 * 24 * time.Hour)}
	if _, e := AppendEvidence(context.Background(), db, v); e == nil {
		t.Fatal("unknown review accepted")
	}
	_, rows := evaluate(t, db, q)
	for _, row := range rows {
		if row.Status == "matured_negative" || row.Status == "matured_positive" {
			t.Fatal("unmapped fault became decisive", rows)
		}
	}
}
func TestOutcomeHorizonBufferAndUnknownSlots(t *testing.T) {
	db, q, _ := outcomeDB(t)
	// Produce a query whose seven-day window has ended but whose 24h buffer has not.
	p := fixture(t, 1)
	p.Cutoff = time.Now().UTC().Add(-7*24*time.Hour - time.Hour)
	p.Nodes[0].AvailableAt = p.Cutoff.Add(-time.Hour)
	p = reseal(t, p)
	newer, e := Record(context.Background(), db, p)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"hardware_fault_ledger", "monitoring", "identity"} {
		cover(t, db, newer, 1, s, true)
	}
	_, rows := evaluate(t, db, newer)
	if rows[0].Status != "pending_watermark" || !strings.Contains(strings.Join(rows[0].Blockers, ","), "late_report_buffer") {
		t.Fatal(rows)
	}
	// An incident whose uncertain onset crosses q remains ambiguous, never a hit.
	f, r := seedReview(t, db, q, "cross", time.Minute)
	a, b := q.Cutoff.Add(-time.Minute), q.Cutoff.Add(time.Minute)
	r.ID = 0
	r.Revision = 2
	r.ReviewSHA256 = strings.Repeat("9", 64)
	r.ConfirmedWindowStartAt = &a
	r.ConfirmedWindowEndAt = &b
	r.ReviewedAt = time.Now().UTC()
	r.CreatedAt = time.Time{}
	if e := db.Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	identity(t, db, q, f, r)
	_, rows = evaluate(t, db, q)
	if rows[0].Status != "ambiguous" || rows[0].Positive != nil {
		t.Fatal(rows)
	}
}

func TestProactiveInterventionWithoutFakeFault(t *testing.T) {
	db, q, _ := outcomeDB(t)
	appendInput(t, db, EvidenceInput{Kind: "intervention", Key: "inspection", Owner: "ops", EvidenceSHA: strings.Repeat("a", 64), NodeID: 1, EntityRef: "serial:sn-1", From: q.Cutoff.Add(time.Hour), Through: q.Cutoff.Add(2 * time.Hour), State: "completed"})
	_, rows := evaluate(t, db, q)
	if rows[0].Status != "intervention_affected" || !rows[0].Intervention || rows[0].Positive != nil {
		t.Fatal(rows)
	}
	var count int64
	db.Model(&api.HardwareFaultFeedbackRequest{}).Count(&count)
	if count != 0 {
		t.Fatal("proactive inspection fabricated fault")
	}
}

func TestEvidencePostgresCAS(t *testing.T) {
	dsn := os.Getenv("ATLAS_GPU_RISK_R2_TEST_PG")
	if dsn == "" {
		t.Skip("isolated PostgreSQL only")
	}
	a, q, _ := outcomeDB(t)
	var schema string
	if e := a.Raw("SELECT current_schema()").Scan(&schema).Error; e != nil {
		t.Fatal(e)
	}
	b, e := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := b.DB()
	raw.SetMaxOpenConns(1)
	defer raw.Close()
	if e := b.Exec("SET search_path TO " + schema).Error; e != nil {
		t.Fatal(e)
	}
	in := EvidenceInput{Kind: "coverage", Key: "pg-cas", Owner: "ops", EvidenceSHA: strings.Repeat("a", 64), NodeID: 1, QueryID: q.ID, EntityRef: "serial:sn-1", From: q.Cutoff, Through: q.Cutoff.Add(7 * 24 * time.Hour), Source: "monitoring"}
	initial := appendInput(t, a, in)
	in.PreviousSHA = initial.SHA
	in.Complete = true
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, db := range []*gorm.DB{a, b} {
		wg.Add(1)
		go func(i int, db *gorm.DB) {
			defer wg.Done()
			v := in
			v.Owner = fmt.Sprint("writer-", i)
			_, e := AppendEvidence(context.Background(), db, v)
			results <- e
		}(i, db)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for e := range results {
		if e == nil {
			wins++
		} else if e == ErrFence {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
	var count int64
	a.Model(&api.GPURiskEvidenceRevision{}).Where("key = ?", in.Key).Count(&count)
	if count != 2 {
		t.Fatal("concurrent writers forked revision", count)
	}
	// Two writers recording the same immutable as-of produce one complete batch.
	asOf := time.Now().UTC()
	results = make(chan error, 2)
	for _, db := range []*gorm.DB{a, b} {
		wg.Add(1)
		go func(db *gorm.DB) {
			defer wg.Done()
			_, e := RecordOutcomes(context.Background(), db, q.ID, asOf)
			results <- e
		}(db)
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	a.Model(&api.GPURiskOutcomeBatch{}).Where("query_id = ?", q.ID).Count(&count)
	if count != 1 {
		t.Fatal("concurrent outcome duplicated", count)
	}
	a.Model(&api.GPURiskNodeOutcome{}).Count(&count)
	if count != 2 {
		t.Fatal("half batch", count)
	}
}

func TestCaptureMixedTimezoneHealth(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.InfrastructureAsset{}, &api.GPUNode{}, &api.GPUAsset{}, &api.GPUHealthScore{}, &api.HealthEvaluationRun{}); e != nil {
		t.Fatal(e)
	}
	old := time.Now().UTC().Add(-time.Hour)
	n := api.GPUNode{NodeIP: "timezone", AssetSerial: "tz", Lifecycle: "active", CreatedAt: old, UpdatedAt: old}
	db.Create(&n)
	a := api.GPUAsset{AssetKey: "tz-slot", NodeID: n.ID, CurrentNodeIdentity: true, CurrentUUID: "TZ-GPU", NodeIP: n.NodeIP, State: "active", CreatedAt: old, UpdatedAt: old}
	db.Create(&a)
	run := api.HealthEvaluationRun{Status: "success", FinishedAt: &old}
	db.Create(&run)
	local := time.FixedZone("Shanghai", 8*3600)
	earlier, later := old.Add(time.Minute).In(local), old.Add(2*time.Minute)
	v1, v2 := 80, 40
	db.Create(&api.GPUHealthScore{GPUAssetID: a.ID, GPUUUID: a.CurrentUUID, NodeIP: n.NodeIP, Score: &v1, EvaluationRunID: run.ID, RuleVersion: "mixed", EvaluatedAt: earlier, CreatedAt: earlier})
	db.Create(&api.GPUHealthScore{GPUAssetID: a.ID, GPUUUID: a.CurrentUUID, NodeIP: n.NodeIP, Score: &v2, EvaluationRunID: run.ID, RuleVersion: "mixed", EvaluatedAt: later, CreatedAt: later})
	p, e := Capture(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Nodes[0].Cards) != 1 || p.Nodes[0].Cards[0].Score == nil || *p.Nodes[0].Cards[0].Score != 40 {
		t.Fatal("timezone text ordering selected stale score", p)
	}
}

func TestOutcomeDailyIdempotentAndFenced(t *testing.T) {
	db, q, _ := outcomeDB(t)
	now := time.Now().UTC()
	if e := OutcomeDaily(context.Background(), db, now); e != nil {
		t.Fatal(e)
	}
	if e := OutcomeDaily(context.Background(), db, now); e != nil {
		t.Fatal(e)
	}
	var count int64
	db.Model(&api.GPURiskOutcomeBatch{}).Count(&count)
	local := now.In(time.FixedZone("Shanghai", 8*3600))
	expected := int64(1)
	if local.Hour() < 2 || (local.Hour() == 2 && local.Minute() < 10) {
		expected = 0
	}
	if count != expected {
		t.Fatal("daily schedule/idempotence", count, expected)
	}
	key := fmt.Sprint("outcome-fence-", time.Now().UnixNano())
	old, e := Claim(db, key, "old", now)
	if e != nil {
		t.Fatal(e)
	}
	db.Model(&api.GPURiskJob{}).Where("key = ?", key).Update("lease_until", now.Add(-time.Second))
	next, e := Claim(db, key, "next", now)
	if e != nil {
		t.Fatal(e)
	}
	asOf := time.Now().UTC()
	if _, e := recordOutcomes(context.Background(), db, q.ID, asOf, &old); e != ErrFence {
		t.Fatal("stale outcome published", e)
	}
	if _, e := recordOutcomes(context.Background(), db, q.ID, asOf, &next); e != nil {
		t.Fatal(e)
	}
	db.Model(&api.GPURiskOutcomeBatch{}).Count(&count)
	if count != expected+1 {
		t.Fatal(count)
	}
}

func TestAdditiveOutcomeMigrationPreservesRanking(t *testing.T) {
	db, q, _ := outcomeDB(t)
	// Recreate the delivered R1 shape, then migrate R2 twice on existing data.
	for _, model := range []any{&api.GPURiskNodeOutcome{}, &api.GPURiskOutcomeBatch{}, &api.GPURiskEvidenceRevision{}} {
		if e := db.Migrator().DropTable(model); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 2; i++ {
		if e := Migrate(db); e != nil {
			t.Fatal(e)
		}
	}
	var stored api.GPURiskQuery
	if e := db.First(&stored, q.ID).Error; e != nil {
		t.Fatal(e)
	}
	if stored.Key != q.Key || stored.ResultSHA != q.ResultSHA || stored.ScopeSHA != q.ScopeSHA {
		t.Fatal("migration changed frozen ranking")
	}
	var count int64
	db.Model(&api.GPURiskScore{}).Where("query_id = ?", q.ID).Count(&count)
	if count != 4 {
		t.Fatal("lost scores", count)
	}
}

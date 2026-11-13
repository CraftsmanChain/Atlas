package gpurisk

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func fixture(t *testing.T, n int) Snapshot {
	t.Helper()
	q := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	p := Snapshot{Contract: Contract, Target: Target, Cutoff: q, Nodes: []Node{}}
	for i := 1; i <= n; i++ {
		p.Nodes = append(p.Nodes, Node{ID: uint(i), IP: fmt.Sprintf("node-%d", i), Serial: fmt.Sprintf("sn-%d", i), Lifecycle: "active", ScopeStatus: "in_use", Expected: 1, Observed: 1, AvailableAt: q.Add(-time.Hour), Cards: []Card{}})
	}
	p, e := seal(p)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func reseal(t *testing.T, p Snapshot) Snapshot {
	t.Helper()
	p, e := seal(p)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	var db *gorm.DB
	var e error
	pgDSN := os.Getenv("ATLAS_GPU_RISK_R2_TEST_PG")
	schema := ""
	if pgDSN != "" {
		db, e = gorm.Open(postgres.Open(pgDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if e == nil {
			raw, _ := db.DB()
			raw.SetMaxOpenConns(1)
			schema = fmt.Sprintf("gpu_r2_test_%d", time.Now().UnixNano())
			if e = db.Exec("CREATE SCHEMA " + schema).Error; e == nil {
				e = db.Exec("SET search_path TO " + schema).Error
			}
		}
	} else {
		db, e = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "risk.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	}
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := db.DB()
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if schema != "" {
			db.Exec("DROP SCHEMA " + schema + " CASCADE")
		}
		raw.Close()
	})
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	return db
}
func TestColdStartAndNodeDenominator(t *testing.T) {
	for _, n := range []int{20, 21} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			p := fixture(t, n)
			m, s, e := Build(p)
			if e != nil {
				t.Fatal(e)
			}
			if len(m) != n || len(s) != n*2 {
				t.Fatal("incomplete denominator")
			}
			for _, r := range s {
				if r.Policy == Uniform {
					if r.RawScore == nil || r.Rank != 1 || r.TieCount != n || r.TopFivePercentK != (n+19)/20 {
						t.Fatalf("bad ties/Top5: %+v", r)
					}
				} else if r.RawScore != nil || r.SignalStatus != "no_signal" || r.Rank != 0 {
					t.Fatal("missing became healthy")
				}
			}
		})
	}
}
func TestHealthCoverageAndIdentity(t *testing.T) {
	p := fixture(t, 4)
	score := 60
	for i := 0; i < 8; i++ {
		p.Nodes[0].Cards = append(p.Nodes[0].Cards, Card{AssetID: uint(i + 1), UUID: fmt.Sprint(i), State: "active", HealthID: uint(i + 1), RunID: 1, Rule: "health-v1", Score: &score, EvaluatedAt: p.Cutoff.Add(-time.Hour), AvailableAt: p.Cutoff.Add(-time.Hour + time.Second)})
	}
	p.Nodes[0].Expected = 8
	p.Nodes[0].Observed = 8
	p.Nodes[1].Serial = ""
	p.Nodes[2].Serial = p.Nodes[3].Serial
	p = reseal(t, p)
	m, s, e := Build(p)
	if e != nil {
		t.Fatal(e)
	}
	if len(m) != 4 || s[1].RawScore == nil || *s[1].RawScore != 40 || s[1].Coverage != 1 || s[1].EligibleCount != 1 {
		t.Fatalf("8 cards must rank as one node: %+v", s[1])
	}
	if m[1].Reason != "identity_unknown" || m[2].Reason != "serial_conflict" {
		t.Fatal(m)
	}
	p.Nodes[0].Expected = 9
	p = reseal(t, p)
	_, s, e = Build(p)
	if e != nil {
		t.Fatal(e)
	}
	if s[1].SignalStatus != "partial" || s[1].Rank != 0 {
		t.Fatal("partial entered fair ranking")
	}
}
func TestInputRejection(t *testing.T) {
	cases := map[string]func(*Snapshot){
		"future_cutoff":       func(p *Snapshot) { p.Cutoff = time.Now().Add(time.Hour) },
		"duplicate_node":      func(p *Snapshot) { p.Nodes = append(p.Nodes, p.Nodes[0]) },
		"target":              func(p *Snapshot) { p.Target = "xid" },
		"available_at_cutoff": func(p *Snapshot) { p.Nodes[0].AvailableAt = p.Cutoff },
		"negative_generation": func(p *Snapshot) { p.Nodes[0].Generation = -1 },
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			p := fixture(t, 1)
			f(&p)
			p = reseal(t, p)
			if _, _, e := Build(p); e == nil {
				t.Fatal("accepted invalid plan")
			}
		})
	}
	for _, age := range []time.Duration{0, time.Hour, -25 * time.Hour} {
		t.Run(fmt.Sprint(age), func(t *testing.T) {
			p := fixture(t, 1)
			v := 90
			p.Nodes[0].Cards = []Card{{AssetID: 1, UUID: "uuid", State: "active", Score: &v, HealthID: 1, RunID: 1, Rule: "r1", EvaluatedAt: p.Cutoff.Add(age), AvailableAt: p.Cutoff.Add(-time.Second)}}
			p = reseal(t, p)
			if _, _, e := Build(p); e == nil {
				t.Fatal("cutoff/future/expired accepted")
			}
		})
	}
	p := fixture(t, 1)
	p.Nodes[0].Serial = "tampered"
	if _, _, e := Build(p); e == nil {
		t.Fatal("SHA tampering accepted")
	}
}
func TestRecordIdempotenceRollbackAndFence(t *testing.T) {
	db := testDB(t)
	p := fixture(t, 21)
	q, e := Record(context.Background(), db, p)
	if e != nil {
		t.Fatal(e)
	}
	q2, e := Record(context.Background(), db, p)
	if e != nil || q.ID != q2.ID || q.ResultSHA != q2.ResultSHA {
		t.Fatal(q2, e)
	}
	for model, want := range map[any]int64{&api.GPURiskQuery{}: 1, &api.GPURiskMember{}: 21, &api.GPURiskScore{}: 42} {
		var count int64
		db.Model(model).Count(&count)
		if count != want {
			t.Fatalf("%T rows=%d", model, count)
		}
	}
	now := time.Now().UTC()
	l, e := Claim(db, "fenced", "one", now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Claim(db, "fenced", "two", now); e != ErrBusy {
		t.Fatal(e)
	}
	db.Model(&api.GPURiskJob{}).Where("key = ?", l.Key).Update("lease_until", now.Add(-time.Second))
	l2, e := Claim(db, "fenced", "two", now)
	if e != nil {
		t.Fatal(e)
	}
	m, s, _ := Build(p)
	if _, e := commit(db, p, l, m, s); e != ErrFence {
		t.Fatal("stale worker committed", e)
	}
	s[0].RawScore = new(float64)
	*s[0].RawScore = math.NaN()
	if _, e := commit(db, p, l2, m, s); e == nil {
		t.Fatal("NaN accepted")
	}
	var job api.GPURiskJob
	db.Where("key = ?", l.Key).First(&job)
	if job.Status != "running" {
		t.Fatal("transaction did not roll back fence")
	}
	m, s, _ = Build(p)
	if _, e := commit(db, p, l2, m[:1], s); e == nil {
		t.Fatal("partial batch accepted")
	}
	var count int64
	db.Model(&api.GPURiskQuery{}).Count(&count)
	if count != 1 {
		t.Fatal("half batch visible")
	}
}
func TestCaptureCurrentScopeAndGeneration(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.InfrastructureAsset{}, &api.GPUNode{}, &api.GPUAsset{}, &api.GPUHealthScore{}, &api.HealthEvaluationRun{}); e != nil {
		t.Fatal(e)
	}
	old := time.Now().UTC().Add(-time.Hour)
	score := 30
	n := api.GPUNode{NodeIP: "test", AssetSerial: "sn", Lifecycle: "active", IdentityGeneration: 2, ExpectedGPUCount: 1, CreatedAt: old, UpdatedAt: old}
	db.Create(&n)
	a := api.GPUAsset{AssetKey: "slot", NodeID: n.ID, NodeIP: n.NodeIP, NodeIdentityGeneration: 1, CurrentNodeIdentity: true, CurrentUUID: "uuid", State: "active", CreatedAt: old, UpdatedAt: old}
	db.Create(&a)
	run := api.HealthEvaluationRun{Status: "success", FinishedAt: &old}
	db.Create(&run)
	h := api.GPUHealthScore{GPUAssetID: a.ID, GPUUUID: "uuid", NodeIP: "test", Score: &score, EvaluationRunID: run.ID, RuleVersion: "v1", EvaluatedAt: old, CreatedAt: old}
	db.Create(&h)
	p, e := Capture(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Nodes) != 1 || len(p.Nodes[0].Cards) != 0 {
		t.Fatal("cross generation mixed")
	}
	if p.Nodes[0].ScopeStatus != "scope_unconfirmed" {
		t.Fatal("monitoring became asset proof")
	}
	source := api.InfrastructureAsset{AssetKey: "source", Source: "asset_machine", EntityKind: "gpu_node", SerialNumber: n.AssetSerial, IPAddress: n.NodeIP, InUse: true, Present: true, CreatedAt: old, UpdatedAt: old}
	if e := db.Create(&source).Error; e != nil {
		t.Fatal(e)
	}
	db.Model(&a).Update("node_identity_generation", 2)
	p, e = Capture(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Nodes[0].Cards) != 1 || p.Nodes[0].Cards[0].Score == nil {
		t.Fatal("persisted health unavailable", p)
	}
	if p.Nodes[0].ScopeStatus != "in_use" || len(p.Nodes[0].SourceAssetIDs) != 1 {
		t.Fatal("trusted source not frozen")
	}
	q, e := Record(context.Background(), db, p)
	if e != nil {
		t.Fatal(e)
	}
	db.Model(&n).Update("asset_serial", "new")
	q2, e := Record(context.Background(), db, p)
	if e != nil || q.ID != q2.ID {
		t.Fatal("replay re-read live assets", e)
	}
}
func TestReadOnlyAPI(t *testing.T) {
	db := testDB(t)
	h := Handler{DB: db}
	p := fixture(t, 21)
	q, e := Record(context.Background(), db, p)
	if e != nil {
		t.Fatal(e)
	}
	requests := map[string]int{"overview": 200, "queries?limit=1": 200, fmt.Sprintf("queries/%d/nodes?limit=20", q.ID): 200, "queries/999/nodes": 404, "queries/0/nodes": 400, "queries/1/nodes?policy=invalid": 400, "queries?limit=1&limit=2": 400, "overview?refresh=true": 400, "queries?limit=201": 400, "queries?before_id=0": 400, "queries/1/nodes?status=bad": 400}
	for path, status := range requests {
		r := httptest.NewRequest("GET", "/api/v1/gpu-risk/"+path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	path := fmt.Sprintf("/api/v1/gpu-risk/queries/%d/nodes?limit=20", q.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	tag := w.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag")
	}
	var payload struct {
		Data []api.GPURiskScore
		Meta struct {
			HasMore bool `json:"has_more"`
			Next    uint `json:"next_after_node_id"`
		}
	}
	if json.Unmarshal(w.Body.Bytes(), &payload) != nil || len(payload.Data) != 20 || !payload.Meta.HasMore || payload.Meta.Next != 20 {
		t.Fatal(w.Body.String())
	}
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("If-None-Match", tag)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 304 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
	var count int64
	db.Model(&api.GPURiskQuery{}).Count(&count)
	if count != 1 {
		t.Fatal("GET wrote rows")
	}
}

// ATLAS_GPU_RISK_TEST_PG must point to a dedicated empty local test database.
// This test uses two independent pools and exercises actual PostgreSQL locks.
func TestPostgresConcurrentClaim(t *testing.T) {
	dsn := os.Getenv("ATLAS_GPU_RISK_TEST_PG")
	if dsn == "" {
		t.Skip("isolated PostgreSQL DSN not supplied")
	}
	open := func() *gorm.DB {
		db, e := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := db.DB()
		t.Cleanup(func() { raw.Close() })
		return db
	}
	a, b := open(), open()
	if e := Migrate(a); e != nil {
		t.Fatal(e)
	}
	if e := Migrate(a); e != nil {
		t.Fatal(e)
	}
	if e := a.AutoMigrate(&api.InfrastructureAsset{}, &api.GPUNode{}, &api.GPUAsset{}, &api.GPUHealthScore{}, &api.HealthEvaluationRun{}); e != nil {
		t.Fatal(e)
	}
	old := time.Now().UTC().Add(-time.Hour)
	node := api.GPUNode{NodeIP: fmt.Sprintf("pg-%d", time.Now().UnixNano()), AssetSerial: "pg-fixture", Lifecycle: "active", CreatedAt: old, UpdatedAt: old}
	if e := a.Create(&node).Error; e != nil {
		t.Fatal(e)
	}
	captured, e := Capture(context.Background(), b)
	if e != nil || len(captured.Nodes) == 0 {
		t.Fatal("PG readonly capture", e)
	}

	key := fmt.Sprintf("concurrent-%d", time.Now().UnixNano())
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, db := range []*gorm.DB{a, b} {
		wg.Add(1)
		go func(i int, db *gorm.DB) {
			defer wg.Done()
			_, e := Claim(db, key, fmt.Sprint(i), time.Now().UTC())
			results <- e
		}(i, db)
	}
	wg.Wait()
	close(results)
	wins, busy := 0, 0
	for e := range results {
		if e == nil {
			wins++
		} else if e == ErrBusy {
			busy++
		} else {
			t.Fatal(e)
		}
	}
	if wins != 1 || busy != 1 {
		t.Fatal(wins, busy)
	}
	p := fixture(t, 21)
	q, e := Record(context.Background(), a, p)
	if e != nil {
		t.Fatal(e)
	}
	q2, e := Record(context.Background(), b, p)
	if e != nil || q.ID != q2.ID {
		t.Fatal(q2, e)
	}
	// Real PostgreSQL must also reject an expired owner and roll back a half batch.
	fenceKey := fmt.Sprintf("pg-fence-%d", time.Now().UnixNano())
	oldLease, e := Claim(a, fenceKey, "old", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	a.Model(&api.GPURiskJob{}).Where("key = ?", fenceKey).Update("lease_until", time.Now().Add(-time.Second))
	nextLease, e := Claim(b, fenceKey, "new", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	m, s, _ := Build(p)
	if _, e := commit(a, p, oldLease, m, s); e != ErrFence {
		t.Fatal("PG accepted expired lease", e)
	}
	if _, e := commit(b, p, nextLease, m[:1], s); e == nil {
		t.Fatal("PG accepted half batch")
	}
	var count int64
	a.Model(&api.GPURiskQuery{}).Where("key = ?", fenceKey).Count(&count)
	if count != 0 {
		t.Fatal("PG published partial batch")
	}
	if _, e := commit(b, p, nextLease, m, s); e != nil {
		t.Fatal("PG rollback did not preserve retry", e)
	}

}

func TestDailyFreezesScopeAndBeforeTwoAMDoesNothing(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.InfrastructureAsset{}, &api.GPUNode{}, &api.GPUAsset{}, &api.GPUHealthScore{}, &api.HealthEvaluationRun{}); e != nil {
		t.Fatal(e)
	}
	old := time.Now().UTC().Add(-time.Hour)
	node := api.GPUNode{NodeIP: "daily", AssetSerial: "daily-sn", Lifecycle: "active", CreatedAt: old, UpdatedAt: old}
	if e := db.Create(&node).Error; e != nil {
		t.Fatal(e)
	}
	zone := time.FixedZone("Shanghai", 8*3600)
	day := time.Date(2026, 10, 28, 1, 0, 0, 0, zone)
	if e := Daily(context.Background(), db, day); e != nil {
		t.Fatal(e)
	}
	var count int64
	db.Model(&api.GPURiskJob{}).Count(&count)
	if count != 0 {
		t.Fatal("wrote before schedule")
	}
	if e := Daily(context.Background(), db, day.Add(2*time.Hour)); e != nil {
		t.Fatal(e)
	}
	var first api.GPURiskQuery
	db.First(&first)
	db.Model(&node).Update("asset_serial", "changed")
	if e := Daily(context.Background(), db, day.Add(3*time.Hour)); e != nil {
		t.Fatal(e)
	}
	db.Model(&api.GPURiskQuery{}).Count(&count)
	if count != 1 {
		t.Fatal("daily duplicated scope")
	}
	var member api.GPURiskMember
	db.First(&member)
	if member.EntityRef != "serial:daily-sn" {
		t.Fatal("rewrote frozen identity")
	}
}

func TestOverviewCollectingFailedAndStale(t *testing.T) {
	db := testDB(t)
	h := Handler{DB: db}
	fetch := func() map[string]any {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/gpu-risk/overview", nil))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		var p map[string]any
		if json.Unmarshal(w.Body.Bytes(), &p) != nil {
			t.Fatal(w.Body.String())
		}
		return p["data"].(map[string]any)
	}
	data := fetch()
	if data["status"] != "collecting" || data["latest"] != nil || data["automatic_jobs_enabled"] != false {
		t.Fatal(data)
	}
	p := fixture(t, 1)
	p.Cutoff = p.Cutoff.Add(-26 * time.Hour)
	p.Nodes[0].AvailableAt = p.Cutoff.Add(-time.Hour)
	p = reseal(t, p)
	if _, e := Record(context.Background(), db, p); e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&api.GPURiskJob{Key: "failed-job", Status: "failed", ErrorCode: "record_failed"}).Error; e != nil {
		t.Fatal(e)
	}
	data = fetch()
	if data["status"] != "stale" || data["probability_emitted"] != false || data["outcome_status"] != "available_on_explicit_record" {
		t.Fatal(data)
	}
}

func TestDailyCaptureFailureBudget(t *testing.T) {
	db := testDB(t) // Deliberately omit source tables; only the job ledger exists.
	now := time.Date(2026, 10, 28, 5, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if e := Daily(context.Background(), db, now); e == nil {
			t.Fatal("missing source schema accepted")
		}
	}
	if e := Daily(context.Background(), db, now); e != nil {
		t.Fatal("budget did not stop retries", e)
	}
	var jobs []api.GPURiskJob
	db.Find(&jobs)
	if len(jobs) != 1 || jobs[0].Attempt != 2 || jobs[0].Status != "failed" {
		t.Fatal(jobs)
	}
}

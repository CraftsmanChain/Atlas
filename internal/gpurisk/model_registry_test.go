package gpurisk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This helper inserts synthetic provenance only into an isolated test DB. There
// is no runtime CLI/API option to bypass RegisterRanker's genuine-data gate.
func candidateDB(t *testing.T) (*gorm.DB, api.GPURiskQuery, api.GPURiskModel) {
	t.Helper()
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPUNode{}, &api.HardwareFaultFeedbackReview{}); e != nil {
		t.Fatal(e)
	}
	p := fixture(t, 3)
	for i := range p.Nodes {
		p.Nodes[i].Cards = []Card{{AssetID: uint(i + 1), UUID: fmt.Sprint("NEW-GPU-", i), State: "active"}}
	}
	p = reseal(t, p)
	q, e := Record(context.Background(), db, p)
	if e != nil {
		t.Fatal(e)
	}
	d := rankerFixture(t)
	a := zeroRanker(d, strings.Repeat("a", 64))
	a.Purpose = "offline_exploration_not_promoted"
	root := t.TempDir()
	path := filepath.Join(root, "model.json")
	sha := testFile(t, path, a)
	raw, _ := os.ReadFile(path)
	prep, _ := digest(a.Preprocessor)
	m := api.GPURiskModel{ArtifactSHA: sha, PreprocessorSHA: prep, DatasetSHA: a.DatasetSHA, DatasetManifestSHA: d.SHA, Version: RankerVersion, Target: Target, FeatureContract: HistoryFeatureVersion, Status: "offline_candidate", Artifact: string(raw), TrainingThrough: d.Spec.TestEnd, AvailableAt: q.Cutoff.Add(-time.Hour), TrainingIdentities: api.StringList{"serial:sn-3"}}
	if e := db.Create(&m).Error; e != nil {
		t.Fatal(e)
	}
	return db, q, m
}
func TestCandidateFrozenRequestRankingAndIdempotence(t *testing.T) {
	db, q, m := candidateDB(t)
	ctx := context.Background()
	r, e := BuildModelRequest(ctx, db, q.ID, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Rows) != 3 || !contains(r.Rows[2].Blockers, "training_entity_overlap") {
		t.Fatal(r)
	}
	// A late source assertion cannot rewrite q's frozen historical features.
	v := sourceInput(q)
	v.Context.GPUModel = "late-only"
	data, _ := json.Marshal(v)
	if e := db.Create(&api.GPURiskEvidenceRevision{Kind: v.Kind, Key: v.Key, Revision: 1, AvailableAt: time.Now().UTC(), SHA: strings.Repeat("e", 64), Data: string(data)}).Error; e != nil {
		t.Fatal(e)
	}
	again, e := BuildModelRequest(ctx, db, q.ID, m.ID)
	if e != nil || again.SHA != r.SHA {
		t.Fatal("late source rewrote cutoff", e)
	}
	b, e := RecordModelScores(ctx, db, r)
	if e != nil || b.Status != "completed" {
		t.Fatal(b, e)
	}
	repeated, e := RecordModelScores(ctx, db, r)
	if e != nil || repeated.ID != b.ID {
		t.Fatal(repeated, e)
	}
	var rows []api.GPURiskModelScore
	db.Where("batch_id = ?", b.ID).Order("node_id").Find(&rows)
	if len(rows) != 3 || rows[0].Rank != 1 || rows[0].TieCount != 2 || rows[0].EligibleCount != 2 || rows[0].TopFivePercentK != 1 || rows[2].RawScore != nil || rows[2].Rank != 0 {
		t.Fatal(rows)
	}
	r.Rows[0].Features.Numeric["episode_count"] = numberPtr(999)
	if _, e := RecordModelScores(ctx, db, r); e == nil {
		t.Fatal("edited request accepted")
	}
	var current api.GPURiskQuery
	db.First(&current, q.ID)
	if current.ResultSHA != q.ResultSHA {
		t.Fatal("R1 result rewritten")
	}
	var baseline int64
	db.Model(&api.GPURiskScore{}).Where("query_id = ?", q.ID).Count(&baseline)
	if baseline != 6 {
		t.Fatal("baseline rows altered")
	}
	if p := os.Getenv("ATLAS_GPU_RISK_MODEL_REQUEST_FIXTURE"); p != "" {
		testFile(t, p, again)
	}
}
func TestCandidateAvailabilityAndPhysicalIsolation(t *testing.T) {
	db, q, m := candidateDB(t)
	db.Model(&m).Update("available_at", q.Cutoff)
	if _, e := BuildModelRequest(context.Background(), db, q.ID, m.ID); e == nil {
		t.Fatal("model equal cutoff leaked")
	}
	db.Model(&m).Updates(map[string]any{"available_at": q.Cutoff.Add(-time.Hour), "training_through": q.Cutoff})
	if _, e := BuildModelRequest(context.Background(), db, q.ID, m.ID); e == nil {
		t.Fatal("training equal cutoff leaked")
	}
	db.Model(&m).Updates(map[string]any{"training_through": q.Cutoff.Add(-24 * time.Hour), "training_identities": api.StringList{"gpu:NEW-GPU-0"}})
	request, e := BuildModelRequest(context.Background(), db, q.ID, m.ID)
	if e != nil || !contains(request.Rows[0].Blockers, "training_entity_overlap") {
		t.Fatal(request, e)
	}
}
func TestCandidateAtomicNumericalFailure(t *testing.T) {
	db, q, m := candidateDB(t)
	a, e := modelArtifact(m)
	if e != nil {
		t.Fatal(e)
	}
	// New OOV values can overflow an otherwise finite model; no partial ranking.
	a.Layers[0].Weights[0][12] = 1e308
	a.Preprocessor.Numeric[6].Mean = -1e20
	root := filepath.Join(t.TempDir(), "overflow.json")
	sha := testFile(t, root, a)
	raw, _ := os.ReadFile(root)
	prep, _ := digest(a.Preprocessor)
	db.Model(&m).Updates(map[string]any{"artifact_sha": sha, "artifact": string(raw), "preprocessor_sha": prep})
	r, e := BuildModelRequest(context.Background(), db, q.ID, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	batch, e := RecordModelScores(context.Background(), db, r)
	if e != nil || batch.Status != "failed" || batch.ErrorCode != "numerical_inference_failed" {
		t.Fatal(batch, e)
	}
	var rows []api.GPURiskModelScore
	db.Where("batch_id = ?", batch.ID).Find(&rows)
	if len(rows) != 3 {
		t.Fatal(rows)
	}
	for _, row := range rows {
		if row.RawScore != nil || row.Rank != 0 || row.SignalStatus != "failed" {
			t.Fatal("partial failed ranking retained", row)
		}
	}
}
func TestCandidateAPIExplicitBatchIntegrityAndPrivacy(t *testing.T) {
	db, q, m := candidateDB(t)
	r, e := BuildModelRequest(context.Background(), db, q.ID, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	b, e := RecordModelScores(context.Background(), db, r)
	if e != nil {
		t.Fatal(e)
	}
	h := Handler{DB: db}
	get := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		return rr
	}
	for path, want := range map[string]int{"/api/v1/gpu-risk/models": 200, "/api/v1/gpu-risk/models?limit=201": 400, "/api/v1/gpu-risk/models?limit=1&limit=2": 400, fmt.Sprintf("/api/v1/gpu-risk/queries/%d/model-batches", q.ID): 200, fmt.Sprintf("/api/v1/gpu-risk/queries/%d/model-scores", q.ID): 400, fmt.Sprintf("/api/v1/gpu-risk/queries/%d/model-scores?batch_id=999999", q.ID): 404} {
		rr := get(path)
		if rr.Code != want {
			t.Fatal(path, rr.Code, rr.Body.String())
		}
	}
	models := get("/api/v1/gpu-risk/models").Body.String()
	if strings.Contains(models, "weights") || strings.Contains(models, "NEW-GPU") || strings.Contains(models, "training_identities") {
		t.Fatal("private artifacts exposed")
	}
	path := fmt.Sprintf("/api/v1/gpu-risk/queries/%d/model-scores?batch_id=%d&limit=1", q.ID, b.ID)
	rr := get(path)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "next_after_node_id\":1") {
		t.Fatal(rr.Code, rr.Body.String())
	}
	cached := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("If-None-Match", rr.Header().Get("ETag"))
	h.ServeHTTP(cached, req)
	if cached.Code != 304 {
		t.Fatal(cached.Code)
	}
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest("POST", "/api/v1/gpu-risk/models", nil))
	if post.Code != 405 {
		t.Fatal(post.Code)
	}
	db.Model(&api.GPURiskModelScore{}).Where("batch_id = ? AND node_id = ?", b.ID, 1).Update("rank", 99)
	if rr := get(path); rr.Code != 500 {
		t.Fatal("tampered batch returned", rr.Code)
	}
}
func TestRegistryRejectsSyntheticAndForgedReadyDataset(t *testing.T) {
	db := testDB(t)
	d := rankerFixture(t)
	root := t.TempDir()
	dp, ap, sp := filepath.Join(root, "d.json"), filepath.Join(root, "a.json"), filepath.Join(root, "s.json")
	ds := testFile(t, dp, d)
	a := zeroRanker(d, ds)
	as := testFile(t, ap, a)
	scores := RankerScores{Version: "gpu-host-mlp-scores-v1", DatasetSHA: ds, ArtifactSHA: as}
	for _, r := range d.Rows {
		scores.Rows = append(scores.Rows, RankerScore{QueryID: r.QueryID, NodeID: r.NodeID, UncalibratedScore: .5})
	}
	ss := testFile(t, sp, scores)
	if _, e := RegisterRanker(context.Background(), db, dp, ds, ap, as, sp, ss); e == nil {
		t.Fatal("synthetic model admitted")
	}
	d.Choices = []string{}
	d.SHA = ""
	d.SHA, _ = digest(d)
	ds = testFile(t, dp, d)
	a = zeroRanker(d, ds)
	a.Purpose = "offline_exploration_not_promoted"
	as = testFile(t, ap, a)
	scores.ArtifactSHA = as
	scores.DatasetSHA = ds
	ss = testFile(t, sp, scores)
	if _, e := RegisterRanker(context.Background(), db, dp, ds, ap, as, sp, ss); e == nil {
		t.Fatal("forged ready metadata admitted without DB provenance")
	}
	var count int64
	db.Model(&api.GPURiskModel{}).Count(&count)
	if count != 0 {
		t.Fatal("rejected model was persisted")
	}
}
func TestCandidateConcurrentBatchPostgres(t *testing.T) {
	if os.Getenv("ATLAS_GPU_RISK_R2_TEST_PG") == "" {
		t.Skip("isolated PostgreSQL")
	}
	db, q, m := candidateDB(t)
	r, e := BuildModelRequest(context.Background(), db, q.ID, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	var schema string
	db.Raw("SELECT current_schema()").Scan(&schema)
	other, e := gorm.Open(postgres.Open(os.Getenv("ATLAS_GPU_RISK_R2_TEST_PG")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := other.DB()
	raw.SetMaxOpenConns(1)
	defer raw.Close()
	if other.Exec("SET search_path TO "+schema).Error != nil {
		t.Fatal("test schema unavailable")
	}
	var wg sync.WaitGroup
	ids := make(chan uint, 2)
	errs := make(chan error, 2)
	for _, client := range []*gorm.DB{db, other} {
		wg.Add(1)
		go func(client *gorm.DB) {
			defer wg.Done()
			b, e := RecordModelScores(context.Background(), client, r)
			ids <- b.ID
			errs <- e
		}(client)
	}
	wg.Wait()
	close(ids)
	close(errs)
	var id uint
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for value := range ids {
		if id != 0 && id != value {
			t.Fatal("duplicate batches")
		}
		id = value
	}
	var count int64
	db.Model(&api.GPURiskModelScore{}).Where("batch_id = ?", id).Count(&count)
	if count != 3 {
		t.Fatal(count)
	}
}

// Fully synthetic historical rows are built through the real R1/R2/R3 paths.
// Direct timestamps model already-known history only in this isolated fixture.
func TestRegistryAdmissionRebuildsReadyDatabaseDataset(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPUNode{}, &api.HardwareFaultFeedbackRequest{}, &api.HardwareFaultFeedbackReview{}); e != nil {
		t.Fatal(e)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	spec := DatasetSpec{Anchor: start, TrainEnd: start.AddDate(0, 0, 59), ValidationEnd: start.AddDate(0, 0, 119), TestEnd: start.AddDate(0, 0, 180), Profile: "paper_core"}
	nodeID := uint(0)
	addEvidence := func(v EvidenceInput, at time.Time) {
		t.Helper()
		data, _ := json.Marshal(v)
		sha, _ := digest([]any{v, at})
		if e := db.Create(&api.GPURiskEvidenceRevision{Kind: v.Kind, Key: v.Key, Revision: 1, SHA: sha, AvailableAt: at, Data: string(data)}).Error; e != nil {
			t.Fatal(e)
		}
	}
	for index, split := range []string{"train", "validation", "test"} {
		positive, negative := 5, 20
		if index == 0 {
			positive, negative = 10, 60
		}
		cutoff := start.AddDate(0, 0, index*60)
		p := Snapshot{Contract: Contract, Target: Target, Cutoff: cutoff, Nodes: []Node{}}
		for n := 0; n < positive+negative; n++ {
			nodeID++
			serial := fmt.Sprintf("registered-fixture-%s-%d", split, n)
			uuid := ""
			for trial := 0; trial < 10000; trial++ {
				v := fmt.Sprintf("REGISTER-%s-%d-%d", split, n, trial)
				if componentSplit("gpu:"+v) == split {
					uuid = v
					break
				}
			}
			if uuid == "" {
				t.Fatal("partition fixture")
			}
			ip := fmt.Sprint("fixture-node-", nodeID)
			if e := db.Create(&api.GPUNode{ID: nodeID, NodeIP: ip, AssetSerial: serial, Lifecycle: "active"}).Error; e != nil {
				t.Fatal(e)
			}
			p.Nodes = append(p.Nodes, Node{ID: nodeID, IP: ip, Serial: serial, Lifecycle: "active", ScopeStatus: "in_use", Expected: 1, Observed: 1, AvailableAt: cutoff.Add(-time.Hour), Cards: []Card{{AssetID: nodeID, UUID: uuid, State: "active"}}})
		}
		p = reseal(t, p)
		q, e := Record(context.Background(), db, p)
		if e != nil {
			t.Fatal(e)
		}
		if e := db.Model(&q).Update("completed_at", cutoff.Add(time.Second)).Error; e != nil {
			t.Fatal(e)
		}
		for n, node := range p.Nodes {
			entity := "serial:" + node.Serial
			base := EvidenceInput{NodeID: node.ID, EntityRef: entity, Owner: "synthetic", EvidenceSHA: strings.Repeat("a", 64)}
			contextInput := base
			contextInput.Kind = "host_context"
			contextInput.Key = fmt.Sprint("context-", node.ID)
			contextInput.SourceVersion = HistorySourceVersion
			contextInput.State = "active"
			contextInput.From = cutoff.Add(-40 * 24 * time.Hour)
			contextInput.Through = cutoff.Add(-time.Minute)
			contextInput.Context = &HostContext{HostType: "host", GPUModel: "fixture", Manufacturer: "fixture", MainBoard: "fixture", QuotaGroup: "fixture", DataCenter: "fixture", Rack: "fixture"}
			addEvidence(contextInput, cutoff.Add(-time.Minute))
			history := base
			history.Kind = "coverage"
			history.Key = fmt.Sprint("history-", node.ID)
			history.QueryID = q.ID
			history.Source = "hardware_fault_ledger"
			history.Complete = true
			history.From = cutoff.Add(-40 * 24 * time.Hour)
			history.Through = cutoff
			addEvidence(history, cutoff.Add(-time.Minute))
			for _, source := range []string{"hardware_fault_ledger", "monitoring", "identity"} {
				v := base
				v.Kind = "coverage"
				v.Key = fmt.Sprintf("future-%d-%s", node.ID, source)
				v.QueryID = q.ID
				v.Source = source
				v.Complete = true
				v.From = cutoff
				v.Through = cutoff.Add(7 * 24 * time.Hour)
				addEvidence(v, cutoff.Add(7*24*time.Hour))
			}
			if n < positive {
				for j := 0; j < 3; j++ {
					episode := ""
					for trial := 0; trial < 10000; trial++ {
						candidate := fmt.Sprintf("fixture-%s-%d-%d-%d", split, n, j, trial)
						if componentSplit("episode:"+candidate) == split {
							episode = candidate
							break
						}
					}
					onset := cutoff.Add(time.Duration(j+1) * time.Hour)
					feedback := api.HardwareFaultFeedbackRequest{RequestKey: episode, Status: "reported", NodeIP: node.IP, TargetScope: "node", FaultType: "gpu_hardware_failure", FaultOccurredAt: onset, Operator: "synthetic"}
					if e := db.Create(&feedback).Error; e != nil {
						t.Fatal(e)
					}
					sha, _ := digest([]string{episode, "review"})
					review := api.HardwareFaultFeedbackReview{FeedbackRequestID: feedback.ID, Revision: 1, Decision: "confirmed_hardware", Reviewer: "synthetic", ConfirmedFaultType: "gpu_hardware_failure", ConfirmedOnsetAt: &onset, TargetScope: "node", EpisodeKey: episode, EvidenceKeys: api.StringList{"synthetic-proof"}, ReviewSHA256: sha, ReviewedAt: onset, CreatedAt: onset}
					if e := db.Create(&review).Error; e != nil {
						t.Fatal(e)
					}
					identity := base
					identity.Kind = "identity"
					identity.Key = episode
					identity.FeedbackID = feedback.ID
					identity.ReviewSHA = sha
					identity.From = cutoff
					identity.Through = cutoff.Add(7 * 24 * time.Hour)
					addEvidence(identity, onset)
				}
			}
		}
		batch, e := RecordOutcomes(context.Background(), db, q.ID, cutoff.Add(8*24*time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		spec.BatchIDs = append(spec.BatchIDs, batch.ID)
	}
	dataset, e := BuildDataset(context.Background(), db, spec)
	if e != nil || !dataset.Ready {
		t.Fatal("database fixture not ready", dataset.Counts, dataset.Blockers, e)
	}
	dir := t.TempDir()
	dp, ap, sp := filepath.Join(dir, "dataset.json"), filepath.Join(dir, "model.json"), filepath.Join(dir, "scores.json")
	ds := testFile(t, dp, dataset)
	a := zeroRanker(dataset, ds)
	a.Purpose = "offline_exploration_not_promoted"
	as := testFile(t, ap, a)
	scores := RankerScores{Version: "gpu-host-mlp-scores-v1", ArtifactSHA: as, DatasetSHA: ds}
	for _, r := range dataset.Rows {
		scores.Rows = append(scores.Rows, RankerScore{QueryID: r.QueryID, NodeID: r.NodeID, Generation: r.Generation, UncalibratedScore: .5})
	}
	ss := testFile(t, sp, scores)
	model, e := RegisterRanker(context.Background(), db, dp, ds, ap, as, sp, ss)
	if e != nil || model.Status != "offline_candidate" || len(model.TrainingIdentities) < 120 {
		t.Fatal(model.ID, e)
	}
	same, e := RegisterRanker(context.Background(), db, dp, ds, ap, as, sp, ss)
	if e != nil || same.ID != model.ID {
		t.Fatal("admission not idempotent", e)
	}
}

func TestCandidateBrowserFixture(t *testing.T) {
	path := os.Getenv("ATLAS_GPU_RISK_MODEL_BROWSER_FIXTURE")
	if path == "" {
		t.Skip("explicit synthetic browser fixture")
	}
	db, q, m := candidateDB(t)
	request, e := BuildModelRequest(context.Background(), db, q.ID, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	batch, e := RecordModelScores(context.Background(), db, request)
	if e != nil {
		t.Fatal(e)
	}
	paths := []string{"/api/v1/gpu-risk/overview", "/api/v1/gpu-risk/models?limit=50", fmt.Sprintf("/api/v1/gpu-risk/queries/%d/model-batches?limit=50", q.ID), fmt.Sprintf("/api/v1/gpu-risk/queries/%d/model-scores?batch_id=%d&limit=100", q.ID, batch.ID), fmt.Sprintf("/api/v1/gpu-risk/queries/%d/nodes?policy=health-inverse-v1&limit=100", q.ID), fmt.Sprintf("/api/v1/gpu-risk/queries/%d/nodes?policy=uniform-v1&limit=100", q.ID), fmt.Sprintf("/api/v1/gpu-risk/queries/%d/outcomes?limit=100", q.ID)}
	responses := map[string]json.RawMessage{}
	for _, p := range paths {
		rr := httptest.NewRecorder()
		Handler{DB: db}.ServeHTTP(rr, httptest.NewRequest("GET", p, nil))
		if rr.Code != 200 {
			t.Fatal(p, rr.Code)
		}
		responses[p] = rr.Body.Bytes()
	}
	testFile(t, path, map[string]any{"source": "synthetic_fixture_not_production_efficacy", "responses": responses})
}

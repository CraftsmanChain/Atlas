package gpurisk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
)

func datasetSpec(q api.GPURiskQuery, b api.GPURiskOutcomeBatch) DatasetSpec {
	return DatasetSpec{BatchIDs: []uint{b.ID}, Anchor: q.Cutoff, TrainEnd: time.Now().UTC().Add(-2 * time.Hour), ValidationEnd: time.Now().UTC().Add(-time.Hour), TestEnd: time.Now().UTC(), Profile: "paper_core"}
}
func TestDatasetPinnedDeterministicMissingAndReadOnly(t *testing.T) {
	db, q, _ := outcomeDB(t)
	b, e := RecordOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	spec := datasetSpec(q, b)
	a, e := BuildDataset(context.Background(), db, spec)
	if e != nil {
		t.Fatal(e)
	}
	again, e := BuildDataset(context.Background(), db, spec)
	if e != nil || a.SHA != again.SHA || a.FeatureSHA != again.FeatureSHA || a.SplitSHA != again.SplitSHA {
		t.Fatal("non-deterministic dataset", e)
	}
	if path := os.Getenv("ATLAS_GPU_RISK_DATASET_FIXTURE"); path != "" {
		data, _ := json.MarshalIndent(map[string]any{"source": "synthetic_fixture_not_production_efficacy", "dataset": a}, "", "  ")
		if e := os.WriteFile(path, append(data, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if len(a.Rows) != 2 || a.Ready || a.PaperCoreComplete || a.Rows[0].Features.Numeric["episode_count"] != nil || a.Rows[0].Features.NoHistory != nil {
		t.Fatal("missing history became zero/ready", a)
	}
	if a.Rows[0].Features.Categorical["gpu_model"] != nil || a.Rows[0].Features.BySeverity != nil {
		t.Fatal("invented context")
	}
	var count int64
	db.Model(&api.GPURiskOutcomeBatch{}).Count(&count)
	if count != 1 {
		t.Fatal("export wrote a batch")
	}
	spec.BatchIDs = []uint{b.ID, b.ID}
	if _, e := BuildDataset(context.Background(), db, spec); e == nil {
		t.Fatal("duplicate batch accepted")
	}
	spec = datasetSpec(q, b)
	spec.Profile = "atlas_extended"
	if _, e := BuildDataset(context.Background(), db, spec); e == nil {
		t.Fatal("mixed profile")
	}
	spec = datasetSpec(q, b)
	db.Model(&api.GPURiskNodeOutcome{}).Where("batch_id = ? AND node_id = ?", b.ID, 1).Update("status", "matured_negative")
	if _, e := BuildDataset(context.Background(), db, spec); e == nil {
		t.Fatal("tampered label accepted")
	}
}
func TestHistoryCutoffKnownAtAndIndependentEpisodes(t *testing.T) {
	db, q, _ := outcomeDB(t)
	f, r := seedReview(t, db, q, "historical", -10*24*time.Hour)
	old := q.Cutoff.Add(-time.Hour)
	db.Model(&api.HardwareFaultFeedbackReview{}).Where("id = ?", r.ID).Updates(map[string]any{"reviewed_at": old, "created_at": old})
	link := identity(t, db, q, f, r)
	// This fixture deliberately models immutable evidence that was already known
	// before q, independently of today's append time. Production imports cannot do this.
	var v EvidenceInput
	v = EvidenceInput{Kind: "identity", Key: "past", Owner: "fixture", EvidenceSHA: strings.Repeat("a", 64), NodeID: 1, EntityRef: "serial:sn-1", FeedbackID: f.ID, ReviewSHA: r.ReviewSHA256, From: r.ConfirmedOnsetAt.Add(-time.Hour), Through: q.Cutoff.Add(-time.Minute)}
	encoded, _ := json.Marshal(v)
	data := string(encoded)
	db.Model(&api.GPURiskEvidenceRevision{}).Where("id = ?", link.ID).Updates(map[string]any{"data": data, "available_at": old})
	var ev []api.GPURiskEvidenceRevision
	var reviews []api.HardwareFaultFeedbackReview
	db.Find(&ev)
	db.Find(&reviews)
	var member api.GPURiskMember
	db.Where("query_id = ? AND node_id = ?", q.ID, 1).First(&member)
	features, e := historyAt(context.Background(), member, q, ev, reviews)
	if e != nil {
		t.Fatal(e)
	}
	if features.Numeric["episode_count"] == nil || *features.Numeric["episode_count"] != 1 || features.Numeric["mean_interval_days"] != nil {
		t.Fatal(features)
	}
	ev[0].AvailableAt = q.Cutoff
	features, e = historyAt(context.Background(), member, q, ev, reviews)
	if e != nil || features.Numeric["episode_count"] != nil {
		t.Fatal("equal cutoff leaked", features, e)
	}
	ev[0].AvailableAt = old
	reviews[0].ReviewedAt = q.Cutoff
	features, e = historyAt(context.Background(), member, q, ev, reviews)
	if e != nil || features.Numeric["episode_count"] != nil {
		t.Fatal("late review leaked", features, e)
	}
}
func TestComponentIsolationAndCoreSchema(t *testing.T) {
	graph := components{}
	graph.join("node:a", "gpu:one")
	graph.join("node:b", "gpu:one")
	if graph.root("node:a") != graph.root("node:b") || componentSplit(graph.root("node:a")) != componentSplit(graph.root("node:b")) {
		t.Fatal("moved GPU crosses partitions")
	}
	for _, field := range CoreSchema() {
		if strings.Contains(field.Name, "repair") || strings.Contains(field.Name, "90d") || strings.Contains(field.Name, "telemetry") {
			t.Fatal("extended feature in core", field)
		}
	}
}

func TestDatasetFeatureFingerprintDoesNotIncludeFutureLabels(t *testing.T) {
	db, q, _ := outcomeDB(t)
	first, e := RecordOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	spec := datasetSpec(q, first)
	a, e := BuildDataset(context.Background(), db, spec)
	if e != nil {
		t.Fatal(e)
	}
	f, r := seedReview(t, db, q, "late-label", time.Hour)
	identity(t, db, q, f, r)
	second, e := RecordOutcomes(context.Background(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	spec.BatchIDs = []uint{second.ID}
	b, e := BuildDataset(context.Background(), db, spec)
	if e != nil {
		t.Fatal(e)
	}
	if a.FeatureSHA != b.FeatureSHA || a.InputSHA == b.InputSHA || a.SHA == b.SHA {
		t.Fatal("future label changed historical features or was not fingerprinted")
	}
}
func TestDatasetCompletePhysicalIdentityAndPurge(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPUNode{}, &api.HardwareFaultFeedbackRequest{}, &api.HardwareFaultFeedbackReview{}); e != nil {
		t.Fatal(e)
	}
	p := fixture(t, 1)
	p.Cutoff = time.Now().UTC().Add(-9 * 24 * time.Hour).Truncate(time.Microsecond)
	p.Nodes[0].AvailableAt = p.Cutoff.Add(-time.Hour)
	uuid := ""
	for i := 0; i < 1000; i++ {
		candidate := fmt.Sprint("DATASET-", i)
		if componentSplit("gpu:"+candidate) == "train" {
			uuid = candidate
			break
		}
	}
	if uuid == "" {
		t.Fatal("fixture partition")
	}
	p.Nodes[0].Cards = []Card{{AssetID: 1, UUID: uuid, State: "active"}}
	p = reseal(t, p)
	q, e := Record(context.Background(), db, p)
	if e != nil {
		t.Fatal(e)
	}
	// Fixture an actually historical batch; production export never backdates rows.
	db.Model(&api.GPURiskQuery{}).Where("id = ?", q.ID).Update("completed_at", q.Cutoff.Add(time.Minute))
	for _, source := range []string{"hardware_fault_ledger", "monitoring", "identity"} {
		record := cover(t, db, q, 1, source, true)
		db.Model(&api.GPURiskEvidenceRevision{}).Where("id = ?", record.ID).Update("available_at", q.Cutoff.Add(7*24*time.Hour+time.Minute))
	}
	b, e := RecordOutcomes(context.Background(), db, q.ID, q.Cutoff.Add(8*24*time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	spec := datasetSpec(q, b)
	a, e := BuildDataset(context.Background(), db, spec)
	if e != nil {
		t.Fatal(e)
	}
	if len(a.Rows) != 1 || !a.Rows[0].Eligible || a.Counts["train_control_components"] != 1 || a.Ready {
		t.Fatal(a.Rows, a.Counts)
	}
	spec.TrainEnd = q.Cutoff.Add(7 * 24 * time.Hour)
	a, e = BuildDataset(context.Background(), db, spec)
	if e != nil {
		t.Fatal(e)
	}
	if a.Rows[0].Eligible || !contains(a.Rows[0].Blockers, "label_purge_or_late_availability") {
		t.Fatal("purge missing", a.Rows)
	}
}

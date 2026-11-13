package gpurisk

import (
	"atlas/pkg/api"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAvailableProfileRegistryAdmission(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPUNode{}, &api.HardwareFaultFeedbackRequest{}, &api.HardwareFaultFeedbackReview{}); e != nil {
		t.Fatal(e)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	spec := DatasetSpec{Anchor: start, TrainEnd: start.AddDate(0, 0, 59), ValidationEnd: start.AddDate(0, 0, 119), TestEnd: start.AddDate(0, 0, 180), Profile: AvailableContextProfile}
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
			contextInput.Context = &HostContext{}
			addEvidence(contextInput, cutoff.Add(-time.Minute))
			history := base
			history.Kind = "coverage"
			history.Key = fmt.Sprint("history-", node.ID)
			history.QueryID = q.ID
			history.Source = "hardware_fault_ledger"
			history.Complete = true
			history.From = cutoff.Add(-40 * 24 * time.Hour)
			history.Through = cutoff.Add(-2 * time.Minute)
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
					addEvidence(identity, identity.Through)
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
	if dataset.PaperCoreComplete || dataset.ProfileFeatureComplete == nil || !*dataset.ProfileFeatureComplete || dataset.Counts["context_missing:rack"] != len(dataset.Rows) {
		t.Fatal("optional source diagnostics lost", dataset.Counts)
	}

	dir := t.TempDir()
	dp, ap, sp := filepath.Join(dir, "dataset.json"), filepath.Join(dir, "model.json"), filepath.Join(dir, "scores.json")
	ds := testFile(t, dp, dataset)
	a := zeroRanker(dataset, ds)
	a.Version = AvailableArtifactVersion
	a.Profile = AvailableRankerProfile
	a.FeatureContract = AvailableFeatureContract
	a.Availability = availabilityManifest()
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

	// Synthetic prospective cohort uses fresh identities and a controlled availability time.
	prospective := fixture(t, 3)
	for i := range prospective.Nodes {
		prospective.Nodes[i].Cards = []Card{{AssetID: uint(1000 + i), UUID: fmt.Sprint("AVAILABLE-NEW-", i), State: "active"}}
	}
	prospective = reseal(t, prospective)
	q, err := Record(context.Background(), db, prospective)
	if err != nil {
		t.Fatal(err)
	}
	db.Model(&model).Update("available_at", q.Cutoff.Add(-time.Hour))
	request, err := BuildModelRequest(context.Background(), db, q.ID, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := RecordModelScores(context.Background(), db, request)
	if err != nil || batch.Status != "blocked" {
		t.Fatal("prospective missing sources were not blocked", batch, err)
	}
	// Supply genuinely pre-cutoff history with Through <= availability. A
	// covered zero-episode host has known empty state counts without invented states.
	for _, n := range prospective.Nodes {
		v := EvidenceInput{Kind: "coverage", Key: fmt.Sprint("prospective-history-", n.ID), QueryID: q.ID, NodeID: n.ID, Generation: n.Generation, EntityRef: "serial:" + n.Serial, Source: "hardware_fault_ledger", Complete: true, Owner: "synthetic", EvidenceSHA: strings.Repeat("a", 64), From: q.Cutoff.Add(-24 * time.Hour), Through: q.Cutoff.Add(-2 * time.Minute)}
		addEvidence(v, q.Cutoff.Add(-time.Minute))
	}
	happy, err := BuildModelRequest(context.Background(), db, q.ID, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	happyBatch, err := RecordModelScores(context.Background(), db, happy)
	if err != nil || happyBatch.Status != "completed" {
		t.Fatal("legal missing-context cohort failed", happyBatch, err)
	}
	var happyScores []api.GPURiskModelScore
	db.Where("batch_id = ?", happyBatch.ID).Order("node_id").Find(&happyScores)
	if len(happyScores) != 3 {
		t.Fatal(happyScores)
	}
	for _, row := range happyScores {
		if row.RawScore == nil || row.SignalStatus != "scored_with_unknowns" || len(row.Blockers) > 0 || len(row.Warnings) != 7 {
			t.Fatal("optional-only path", row)
		}
		for _, w := range row.Warnings {
			if !optionalContextBlocker(w) {
				t.Fatal(w)
			}
		}
	}
	n := prospective.Nodes[0]
	conflict := EvidenceInput{Kind: "coverage", Key: "prospective-incomplete", QueryID: q.ID, NodeID: n.ID, EntityRef: "serial:" + n.Serial, Source: "hardware_fault_ledger", Owner: "synthetic", EvidenceSHA: strings.Repeat("a", 64), From: q.Cutoff.Add(-time.Hour), Through: q.Cutoff.Add(-2 * time.Minute)}
	addEvidence(conflict, q.Cutoff.Add(-time.Minute))
	blocked, err := BuildModelRequest(context.Background(), db, q.ID, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedScores, _, err := candidateScores(context.Background(), a, blocked)
	if err != nil || blockedScores[0].RawScore != nil || !contains(blockedScores[0].Blockers, "history_coverage_conflict") {
		t.Fatal("required conflict bypassed", err, blockedScores)
	}
	request.Availability.SHA = strings.Repeat("f", 64)
	if _, err := RecordModelScores(context.Background(), db, request); err == nil {
		t.Fatal("edited policy accepted")
	}
}

func TestAvailablePolicyClosedAndStrictSerialization(t *testing.T) {
	for _, name := range []string{"host_type", "gpu_model", "manufacturer", "main_board", "quota_group", "data_center", "rack"} {
		if profileBlocks([]string{"host_context_unknown:" + name}) {
			t.Fatal(name)
		}
	}
	for _, b := range []string{"history_coverage_unknown", "history_coverage_conflict", "historical_episode_conflict", "episode_state_unknown", "episode_state_conflict", "host_context_conflict:rack", "host_context_unknown:new_field", "new_unknown"} {
		if !profileBlocks([]string{b}) {
			t.Fatal("required blocker ignored", b)
		}
	}
	d := rankerFixture(t)
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "availability_manifest") || strings.Contains(string(raw), "profile_feature_complete") {
		t.Fatal("strict serialization changed")
	}
	if !datasetTuple(d) {
		t.Fatal("strict tuple rejected")
	}
	a := zeroRanker(d, strings.Repeat("a", 64))
	if !artifactTuple(a) {
		t.Fatal("strict artifact")
	}
	d.Spec.Profile = AvailableContextProfile
	d.Version = AvailableDatasetVersion
	d.FeatureContract = AvailableFeatureContract
	complete := true
	d.ProfileFeatureComplete = &complete
	d.Availability = availabilityManifest()
	d.PaperCoreComplete = false
	if path := os.Getenv("ATLAS_AVAILABLE_CONTEXT_FIXTURE"); path != "" {
		fixture := d
		fixture.Rows = append([]DatasetRow{}, d.Rows...)
		for i := range fixture.Rows {
			for _, name := range []string{"host_type", "gpu_model", "manufacturer", "main_board", "quota_group", "data_center", "rack"} {
				fixture.Rows[i].Features.Categorical[name] = nil
				fixture.Rows[i].Features.Blockers = append(fixture.Rows[i].Features.Blockers, "host_context_unknown:"+name)
			}
		}
		featureRows, _ := coreFeatureRows(fixture)
		fixture.FeatureSHA, _ = digest([]any{fixture.Schema, featureRows})
		fixture.SplitSHA, _ = digest(fixture.Spec)
		fixture.SHA = ""
		fixture.SHA, _ = digest(fixture)
		testFile(t, path, fixture)
	}
	if !datasetFeatureReady(d) || modelDatasetTuple(a, d) {
		t.Fatal("profile cross pair accepted")
	}
	a.Version = AvailableArtifactVersion
	a.Profile = AvailableRankerProfile
	a.FeatureContract = AvailableFeatureContract
	a.Availability = availabilityManifest()
	if !modelDatasetTuple(a, d) {
		t.Fatal("available tuple")
	}
	a.Availability.OptionalBlockers = append(a.Availability.OptionalBlockers, "history_coverage_unknown")
	if artifactTuple(a) {
		t.Fatal("caller policy accepted")
	}
}
func TestAvailableScoringRequiresSourcesAndPolicy(t *testing.T) {
	db, q, m := candidateDB(t)
	var a RankerArtifact
	json.Unmarshal([]byte(m.Artifact), &a)
	a.Version = AvailableArtifactVersion
	a.Profile = AvailableRankerProfile
	a.FeatureContract = AvailableFeatureContract
	a.Availability = availabilityManifest()
	path := filepath.Join(t.TempDir(), "model.json")
	sha := testFile(t, path, a)
	raw, _ := json.Marshal(a)
	db.Model(&m).Updates(map[string]any{"artifact_sha": sha, "artifact": string(raw), "version": a.Version, "feature_contract": a.FeatureContract})
	request, e := BuildModelRequest(context.Background(), db, q.ID, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	if request.Version != "gpu-host-model-score-request-v2" || !validAvailability(request.Availability) {
		t.Fatal(request)
	}
	scores, _, e := candidateScores(context.Background(), a, request)
	if e != nil || scores[0].RawScore != nil || !contains(scores[0].Blockers, "history_coverage_unknown") {
		t.Fatal("missing history scored", e)
	}
	request.Availability.SHA = strings.Repeat("0", 64)
	if _, _, e = candidateScores(context.Background(), a, request); e == nil {
		t.Fatal("bad policy scored")
	}
}

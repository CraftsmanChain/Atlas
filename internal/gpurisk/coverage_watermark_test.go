package gpurisk

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"testing"
	"time"

	"atlas/pkg/api"
)

// The same normally appended assertion cannot satisfy the old Through>=cutoff
// predicate, but must satisfy R3-C's bounded source lag contract.
func TestCoverageWatermarkOldPredicateBlockedR3CCovered(t *testing.T) {
	db, q, _ := outcomeDB(t)
	through := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	v := EvidenceInput{Kind: "coverage", Key: "history-watermark", Owner: "fixture", EvidenceSHA: stringSHA(), NodeID: 1, QueryID: q.ID, EntityRef: "serial:sn-1", From: through.Add(-40 * 24 * time.Hour), Through: through, Source: "hardware_fault_ledger", Complete: true}
	record := appendInput(t, db, v)
	q.Cutoff = record.AvailableAt.Add(time.Minute)
	if !record.AvailableAt.Before(q.Cutoff) || v.Through.After(record.AvailableAt) {
		t.Fatal("fixture violates ingestion contract")
	}
	if !v.Through.Before(q.Cutoff) {
		t.Fatal("old predicate unexpectedly covered")
	}
	f, err := historyAt(context.Background(), api.GPURiskMember{NodeID: 1, EntityRef: v.EntityRef}, q, []api.GPURiskEvidenceRevision{record}, nil)
	if err != nil || f.HistoryUnknown || contains(f.Blockers, "history_coverage_unknown") {
		t.Fatal("R3-C must cover a fresh as-known source", f, err)
	}
}
func stringSHA() string { return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }

func TestCoverageWatermarkAsKnownAndLagBoundaries(t *testing.T) {
	db, persisted, _ := outcomeDB(t)
	through := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	v := EvidenceInput{Kind: "coverage", Key: "boundaries", Owner: "fixture", EvidenceSHA: stringSHA(), NodeID: 1, QueryID: persisted.ID, EntityRef: "serial:sn-1", From: through.Add(-40 * 24 * time.Hour), Through: through, Source: "hardware_fault_ledger", Complete: true}
	record := appendInput(t, db, v)
	for _, tc := range []struct {
		name    string
		cutoff  time.Time
		covered bool
	}{
		{"equal_ingestion_excluded", record.AvailableAt, false},
		{"exact_max_lag_included", through.Add(MaxSourceLag), true},
		{"beyond_max_lag_unknown", through.Add(MaxSourceLag + time.Microsecond), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := persisted
			q.Cutoff = tc.cutoff
			f, err := historyAt(context.Background(), api.GPURiskMember{NodeID: 1, EntityRef: v.EntityRef}, q, []api.GPURiskEvidenceRevision{record}, nil)
			if err != nil || f.HistoryUnknown == tc.covered || contains(f.Blockers, "history_coverage_unknown") == tc.covered {
				t.Fatal(f, err)
			}
			if !tc.covered && (f.Numeric["episode_count"] != nil || f.NoHistory != nil) {
				t.Fatal("unknown history was zero-filled", f)
			}
			if tc.name == "equal_ingestion_excluded" && (f.HistoryCoverageDays != nil || contains(f.EvidenceSHAs, record.SHA)) {
				t.Fatal("equal-cutoff evidence leaked", f)
			}
			if tc.covered && (f.HistoryCoverageDays == nil || *f.HistoryCoverageDays != 40) {
				t.Fatal("bounded unknown tail counted as exposure", f)
			}
		})
	}
}

// Simulate a weeks-long CLI sequence using a server clock. Every evidence row
// goes through AppendEvidence, including validation and server timestamping; no
// revision is imported, backfilled, or updated. Queries use the same simulated
// completion clock at INSERT, because Record's wall clock is otherwise real.
func TestCoverageWatermarkCompleteSourcesPaperCoreReady(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&api.GPUNode{}, &api.HardwareFaultFeedbackRequest{}, &api.HardwareFaultFeedbackReview{}); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-50 * 24 * time.Hour).Truncate(time.Microsecond)
	clock := base
	previousClock := evidenceNow
	evidenceNow = func() time.Time { return clock }
	t.Cleanup(func() { evidenceNow = previousClock })
	if err := db.Callback().Create().Before("gorm:create").Register("fixture_query_clock", func(tx *gorm.DB) {
		if q, ok := tx.Statement.Dest.(*api.GPURiskQuery); ok {
			at := clock
			q.CompletedAt = &at
		}
	}); err != nil {
		t.Fatal(err)
	}
	spec := DatasetSpec{Anchor: base, TrainEnd: base.Add(12 * 24 * time.Hour), ValidationEnd: base.Add(24 * 24 * time.Hour), TestEnd: base.Add(36 * 24 * time.Hour), Profile: "paper_core"}
	nextID := uint(1)
	for splitIndex, split := range []string{"train", "validation", "test"} {
		positives, controls, perPositive := 5, 20, 2
		if split == "train" {
			positives, controls, perPositive = 10, 60, 3
		}
		cutoff := base.Add(time.Duration(splitIndex) * 12 * 24 * time.Hour)
		p := fixture(t, positives+controls)
		p.Cutoff = cutoff.Add(-24 * time.Hour)
		episodeKeys := map[uint][]string{}
		for i := range p.Nodes {
			n := &p.Nodes[i]
			n.ID = nextID
			nextID++
			n.Serial = fmt.Sprintf("r3c-%d", n.ID)
			n.IP = fmt.Sprintf("r3c-node-%d", n.ID)
			n.AvailableAt = p.Cutoff.Add(-time.Hour)
			uuid := ""
			for probe := 0; ; probe++ {
				candidate := fmt.Sprintf("R3C-%d-%d", n.ID, probe)
				if componentSplit("gpu:"+candidate) == split {
					uuid = candidate
					break
				}
			}
			n.Cards = []Card{{AssetID: n.ID, UUID: uuid, State: "active"}}
			if i < positives {
				for probe := 0; ; probe++ {
					stem := fmt.Sprintf("r3c-%d-%d-", n.ID, probe)
					if componentSplit("episode:"+stem+"0") == split {
						for j := 0; j < perPositive; j++ {
							episodeKeys[n.ID] = append(episodeKeys[n.ID], fmt.Sprint(stem, j))
						}
						break
					}
				}
			}
			if err := db.Create(&api.GPUNode{ID: n.ID, NodeIP: n.IP, AssetSerial: n.Serial}).Error; err != nil {
				t.Fatal(err)
			}
		}
		clock = p.Cutoff
		prior, err := Record(context.Background(), db, reseal(t, p))
		if err != nil {
			t.Fatal(err)
		}
		clock = cutoff.Add(-time.Hour)
		for _, n := range p.Nodes {
			entity := "serial:" + n.Serial
			v := EvidenceInput{Kind: "coverage", Key: fmt.Sprintf("history-%d", n.ID), Owner: "fixture", EvidenceSHA: stringSHA(), NodeID: n.ID, QueryID: prior.ID, EntityRef: entity, From: cutoff.Add(-40 * 24 * time.Hour), Through: clock, Source: "hardware_fault_ledger", Complete: true}
			appendInput(t, db, v)
			v = sourceInput(api.GPURiskQuery{Cutoff: cutoff})
			v.Through = clock
			v.Key = fmt.Sprintf("context-%d", n.ID)
			v.NodeID = n.ID
			v.EntityRef = entity
			appendInput(t, db, v)
		}
		clock = cutoff
		p.Cutoff = cutoff
		q, err := Record(context.Background(), db, reseal(t, p))
		if err != nil {
			t.Fatal(err)
		}
		clock = cutoff.Add(8 * 24 * time.Hour)
		for _, n := range p.Nodes {
			entity := "serial:" + n.Serial
			for _, source := range []string{"hardware_fault_ledger", "monitoring", "identity"} {
				appendInput(t, db, EvidenceInput{Kind: "coverage", Key: fmt.Sprintf("outcome-%d-%s", n.ID, source), Owner: "fixture", EvidenceSHA: stringSHA(), NodeID: n.ID, QueryID: q.ID, EntityRef: entity, From: cutoff, Through: cutoff.Add(7 * 24 * time.Hour), Source: source, Complete: true})
			}
			for j, key := range episodeKeys[n.ID] {
				onset := cutoff.Add(time.Duration(j+1) * time.Hour)
				feedback := api.HardwareFaultFeedbackRequest{RequestKey: key, Status: "reported", NodeIP: n.IP, TargetScope: "node", FaultType: "gpu_hardware_failure", FaultOccurredAt: onset, Operator: "fixture", CreatedAt: clock}
				if err := db.Create(&feedback).Error; err != nil {
					t.Fatal(err)
				}
				review := api.HardwareFaultFeedbackReview{FeedbackRequestID: feedback.ID, Revision: 1, Decision: "confirmed_hardware", Reviewer: "fixture", ReviewNote: "synthetic", ConfirmedOnsetAt: &onset, TargetScope: "node", EpisodeKey: key, EvidenceKeys: api.StringList{"fixture"}, ReviewSHA256: fmt.Sprintf("%064x", feedback.ID), ReviewedAt: clock, CreatedAt: clock}
				if err := db.Create(&review).Error; err != nil {
					t.Fatal(err)
				}
				appendInput(t, db, EvidenceInput{Kind: "identity", Key: key, Owner: "fixture", EvidenceSHA: stringSHA(), NodeID: n.ID, EntityRef: entity, FeedbackID: feedback.ID, ReviewSHA: review.ReviewSHA256, From: onset, Through: onset})
			}
		}
		batch, err := RecordOutcomes(context.Background(), db, q.ID, clock.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		spec.BatchIDs = append(spec.BatchIDs, batch.ID)
	}
	dataset, err := BuildDataset(context.Background(), db, spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range dataset.Rows {
		if len(row.Features.Blockers) != 0 || !row.Eligible {
			t.Fatalf("row %d: features=%v row=%v", row.NodeID, row.Features.Blockers, row.Blockers)
		}
	}
	for _, split := range []string{"train", "validation", "test"} {
		episodes, positive, control := 10, 5, 20
		if split == "train" {
			episodes, positive, control = 30, 10, 60
		}
		for key, want := range map[string]int{"independent_episodes": episodes, "positive_components": positive, "control_components": control} {
			if got := dataset.Counts[split+"_"+key]; got != want {
				t.Fatalf("%s %s = %d, want %d", split, key, got, want)
			}
		}
	}
	if !dataset.PaperCoreComplete || !dataset.Ready {
		t.Fatal(dataset.Counts, dataset.Blockers)
	}
	if !contains(dataset.Choices, CoverageWatermarkContract) {
		t.Fatal("missing fingerprint contract")
	}
	fingerprint, err := digest(func() Dataset { d := dataset; d.SHA = ""; return d }())
	if err != nil || fingerprint != dataset.SHA {
		t.Fatal("fingerprint", err)
	}
	old := dataset
	old.SHA = ""
	old.Choices = append([]string{}, dataset.Choices[1:]...)
	oldSHA, _ := digest(old)
	if oldSHA == dataset.SHA {
		t.Fatal("watermark contract not fingerprinted")
	}
	t.Logf("paper_core_complete=%v ready=%v counts=%v SHA=%s", dataset.PaperCoreComplete, dataset.Ready, dataset.Counts, dataset.SHA)
}

package gpurisk

import (
	"atlas/pkg/api"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"sort"
	"strings"
	"testing"
	"time"
)

// Frozen pre-optimization BuildDataset oracle, copied from 0477688.
// Only its name and historyAt callee name differ; no prepared helper is used.
func buildDatasetBefore(ctx context.Context, db *gorm.DB, spec DatasetSpec) (Dataset, error) {
	out := Dataset{FeatureContract: HistoryFeatureVersion, Version: DatasetVersion, Target: Target, Spec: spec, Schema: CoreSchema(), Rows: []DatasetRow{}, Counts: map[string]int{"eligible": 0, "unknown": 0, "history_unknown": 0, "no_signal": 0}, Blockers: []string{}, Choices: []string{CoverageWatermarkContract, "paper_core only; unavailable source fields remain null", "3-day sampling from explicit UTC anchor; earliest pinned query per UTC date", "deterministic connected-component 60/20/20 assignment; time purge >=8d", "no preprocessing fit, no balancing, no training"}}
	if (spec.Profile != "paper_core" && spec.Profile != AvailableContextProfile) || len(spec.BatchIDs) == 0 || len(spec.BatchIDs) > 200 || spec.Anchor.IsZero() || !spec.Anchor.Before(spec.TrainEnd) || !spec.TrainEnd.Before(spec.ValidationEnd) || !spec.ValidationEnd.Before(spec.TestEnd) || spec.TestEnd.After(time.Now()) {
		return out, ErrInput
	}
	if spec.Profile == AvailableContextProfile {
		out.Version = AvailableDatasetVersion
		out.FeatureContract = AvailableFeatureContract
		out.Availability = availabilityManifest()
		out.Choices[1] = "available-context profile; only seven static context unknowns optional; all other blockers required"
	}
	ids := append([]uint{}, spec.BatchIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if id == 0 || i > 0 && ids[i-1] == id {
			return out, ErrInput
		}
	}
	spec.BatchIDs = ids
	spec.Anchor = spec.Anchor.UTC()
	spec.TrainEnd = spec.TrainEnd.UTC()
	spec.ValidationEnd = spec.ValidationEnd.UTC()
	spec.TestEnd = spec.TestEnd.UTC()
	out.Spec = spec
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var evidence []api.GPURiskEvidenceRevision
		var reviews []api.HardwareFaultFeedbackReview
		if e := tx.Order("id").Limit(100001).Find(&evidence).Error; e != nil {
			return e
		}
		if e := tx.Order("feedback_request_id,revision").Limit(100001).Find(&reviews).Error; e != nil {
			return e
		}
		if len(evidence) > 100000 || len(reviews) > 100000 {
			return ErrInput
		}
		graph := components{}
		inputs := []any{}
		type rowKey struct {
			query, node uint
			generation  int
		}
		gpuComplete := map[rowKey]bool{}
		noSignal := map[rowKey]bool{}
		seenPinnedQuery := map[uint]bool{}
		queryDay := map[string]uint{}
		queryTimes := map[uint]time.Time{}
		var maxAsOf time.Time
		for _, id := range ids {
			var batch api.GPURiskOutcomeBatch
			if e := tx.First(&batch, id).Error; e != nil {
				return e
			}
			batch.AsOf = batch.AsOf.UTC()
			if seenPinnedQuery[batch.QueryID] {
				return ErrInput
			}
			seenPinnedQuery[batch.QueryID] = true
			if batch.AsOf.After(maxAsOf) {
				maxAsOf = batch.AsOf
			}
			if batch.Version != OutcomeVersion {
				return ErrInput
			}
			var q api.GPURiskQuery
			if e := tx.First(&q, batch.QueryID).Error; e != nil {
				return e
			}
			q.Cutoff = q.Cutoff.UTC()
			if q.Contract != Contract || q.Target != Target || q.Status != "completed" || q.HorizonMinutes != 10080 {
				return ErrInput
			}
			var scope api.GPURiskScope
			if e := tx.Where("sha = ?", q.ScopeSHA).First(&scope).Error; e != nil {
				return e
			}
			var snapshot Snapshot
			if json.Unmarshal([]byte(scope.Input), &snapshot) != nil {
				return ErrInput
			}
			if snapshot.SHA != q.ScopeSHA || !snapshot.Cutoff.Equal(q.Cutoff) {
				return ErrInput
			}
			if e := validate(snapshot, time.Now()); e != nil {
				return e
			}
			var members []api.GPURiskMember
			var labels []api.GPURiskNodeOutcome
			if e := tx.Where("query_id = ?", q.ID).Order("node_id,generation").Find(&members).Error; e != nil {
				return e
			}
			if e := tx.Where("batch_id = ?", batch.ID).Order("node_id,generation").Find(&labels).Error; e != nil {
				return e
			}
			if len(out.Rows)+len(members) > 100000 {
				return ErrInput
			}
			if len(members) != q.MemberCount || len(labels) != q.MemberCount || batch.MemberCount != q.MemberCount {
				return ErrInput
			}
			expectedMembers, expectedScores, e := Build(snapshot)
			if e != nil {
				return e
			}
			// Count signal absence per frozen member, never per policy score.
			for _, score := range expectedScores {
				if score.Policy == HealthInverse && score.SignalStatus == "no_signal" {
					noSignal[rowKey{q.ID, score.NodeID, score.Generation}] = true
				}
			}
			resultSHA, _ := digest([]any{expectedMembers, expectedScores})
			if resultSHA != q.ResultSHA {
				return fmt.Errorf("%w: ranking digest", ErrInput)
			}
			normalizedMembers := append([]api.GPURiskMember{}, members...)
			for i := range normalizedMembers {
				normalizedMembers[i].ID = 0
				normalizedMembers[i].QueryID = 0
			}
			a, _ := digest(expectedMembers)
			b, _ := digest(normalizedMembers)
			if a != b {
				return fmt.Errorf("%w: member digest", ErrInput)
			}
			normalizedLabels := append([]api.GPURiskNodeOutcome{}, labels...)
			for i := range normalizedLabels {
				normalizedLabels[i].ID = 0
				normalizedLabels[i].BatchID = 0
			}
			outcomeSHA, _ := digest([]any{OutcomeVersion, q.Key, batch.AsOf, batch.InputSHA, normalizedLabels})
			if outcomeSHA != batch.SHA {
				return fmt.Errorf("%w: outcome digest", ErrInput)
			}
			inputs = append(inputs, []any{q.Key, q.ScopeSHA, q.ResultSHA, batch.SHA})
			day := q.Cutoff.UTC().Format("2006-01-02")
			queryTimes[q.ID] = q.Cutoff
			if old, ok := queryDay[day]; !ok || q.Cutoff.Before(queryTimes[old]) || q.Cutoff.Equal(queryTimes[old]) && q.ID < old {
				queryDay[day] = q.ID
			}
			for _, n := range snapshot.Nodes {
				memberKey := ""
				for _, m := range members {
					if m.NodeID == n.ID && m.Generation == n.Generation {
						memberKey = m.EntityRef
						break
					}
				}
				if memberKey == "" {
					continue
				}
				graph.root(memberKey)
				complete := len(n.Cards) > 0 && n.Expected == len(n.Cards) && n.Observed <= len(n.Cards)
				for _, card := range n.Cards {
					if card.UUID == "" || card.State != "active" || strings.HasPrefix(strings.ToLower(card.UUID), "mig") {
						complete = false
						continue
					}
					graph.join(memberKey, "gpu:"+card.UUID)
				}
				gpuComplete[rowKey{q.ID, n.ID, n.Generation}] = complete
			}
			for i, m := range members {
				label := labels[i]
				if label.NodeID != m.NodeID || label.Generation != m.Generation {
					return ErrInput
				}
				features, e := historyAtBefore(ctx, m, q, evidence, reviews)
				if e != nil {
					return e
				}
				row := DatasetRow{QueryID: q.ID, BatchID: batch.ID, NodeID: m.NodeID, Generation: m.Generation, Cutoff: q.Cutoff, EntityRef: m.EntityRef, Features: features, Label: label.Positive, EpisodeKeys: append([]string{}, label.EpisodeKeys...), Blockers: []string{}}
				if m.Status != "included" {
					row.Blockers = append(row.Blockers, "scope:"+m.Reason)
				}
				if label.Status != "matured_positive" && label.Status != "matured_negative" || label.Intervention {
					row.Blockers = append(row.Blockers, "label:"+label.Status)
				}
				if label.Positive == nil {
					row.Blockers = append(row.Blockers, "unknown_label")
				}
				if q.Cutoff.Before(spec.Anchor) || !q.Cutoff.Before(spec.TestEnd) {
					row.Blockers = append(row.Blockers, "outside_time_range")
				}
				days := int(q.Cutoff.Sub(spec.Anchor).Hours() / 24)
				if days < 0 || days%3 != 0 {
					row.Blockers = append(row.Blockers, "query_cadence")
				}
				boundary := spec.TrainEnd
				row.Split = "train"
				if !q.Cutoff.Before(spec.TrainEnd) {
					row.Split = "validation"
					boundary = spec.ValidationEnd
				}
				if !q.Cutoff.Before(spec.ValidationEnd) {
					row.Split = "test"
					boundary = spec.TestEnd
				}
				if q.Cutoff.Add(8*24*time.Hour).After(boundary) || batch.AsOf.After(boundary) {
					row.Blockers = append(row.Blockers, "label_purge_or_late_availability")
				}
				for _, key := range label.EpisodeKeys {
					if m.EntityRef != "" {
						graph.join(m.EntityRef, "episode:"+key)
					}
				}
				out.Rows = append(out.Rows, row)
			}
		}
		graphEvidence := []string{}
		for _, e := range evidence {
			if e.Kind != "identity" || e.AvailableAt.After(maxAsOf) {
				continue
			}
			var v EvidenceInput
			if json.Unmarshal([]byte(e.Data), &v) != nil {
				return ErrInput
			}
			if v.EntityRef != "" && v.GPUUUID != "" {
				graph.join(v.EntityRef, "gpu:"+v.GPUUUID)
				graphEvidence = append(graphEvidence, e.SHA)
			}
		}
		sort.Strings(graphEvidence)
		sort.Slice(out.Rows, func(i, j int) bool {
			a, b := out.Rows[i], out.Rows[j]
			if !a.Cutoff.Equal(b.Cutoff) {
				return a.Cutoff.Before(b.Cutoff)
			}
			if a.QueryID != b.QueryID {
				return a.QueryID < b.QueryID
			}
			return a.NodeID < b.NodeID
		})
		seenQuery := map[uint]bool{}
		for i := range out.Rows {
			r := &out.Rows[i]
			if r.EntityRef == "" {
				r.Blockers = append(r.Blockers, "identity_unknown")
			} else {
				root := graph.root(r.EntityRef)
				hash := sha256.Sum256([]byte(root))
				r.Component = hex.EncodeToString(hash[:])
				if componentSplit(root) != r.Split {
					r.Blockers = append(r.Blockers, "entity_partition")
				}
			}
			if !gpuComplete[rowKey{r.QueryID, r.NodeID, r.Generation}] {
				r.Blockers = append(r.Blockers, "gpu_identity_incomplete")
			}
			if queryDay[r.Cutoff.UTC().Format("2006-01-02")] != r.QueryID {
				r.Blockers = append(r.Blockers, "duplicate_query_date")
			}
			// Core can be exported with null fields, but incomplete semantics are not a
			// paper-compatible training certification. Readiness exposes this blocker.
			if contains(r.Features.Blockers, "historical_episode_conflict") {
				r.Blockers = append(r.Blockers, "core_feature_contract_incomplete")
			}
			r.Eligible = len(r.Blockers) == 0
			out.Counts["rows"]++
			// These diagnostics overlap eligibility and each other; unknown means
			// an unknown outcome label, history_unknown means missing coverage.
			if r.Label == nil {
				out.Counts["unknown"]++
			}
			if r.Features.HistoryUnknown {
				out.Counts["history_unknown"]++
			}
			if noSignal[rowKey{r.QueryID, r.NodeID, r.Generation}] {
				out.Counts["no_signal"]++
			}
			if !seenQuery[r.QueryID] {
				out.Counts["queries"]++
				seenQuery[r.QueryID] = true
			}
			if r.Eligible {
				out.Counts["eligible"]++
				out.Counts[r.Split+"_eligible"]++
			} else {
				out.Counts["excluded"]++
			}
		}
		featureRows, e := coreFeatureRows(out)
		if e != nil {
			return e
		}
		out.InputSHA, e = digest([]any{inputs, featureRows, graphEvidence})
		if e != nil {
			return e
		}
		out.FeatureSHA, e = digest([]any{out.Schema, featureRows})
		if e != nil {
			return e
		}
		out.SplitSHA, e = digest([]any{spec, graph, out.Rows})
		return e
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	out.PaperCoreComplete = true
	positiveNodes := map[string]map[string]bool{}
	controls := map[string]map[string]bool{}
	episodes := map[string]map[string]bool{}
	for _, split := range []string{"train", "validation", "test"} {
		positiveNodes[split] = map[string]bool{}
		controls[split] = map[string]bool{}
		episodes[split] = map[string]bool{}
	}
	for _, r := range out.Rows {
		if len(r.Features.Blockers) > 0 {
			out.PaperCoreComplete = false
		}
		if !r.Eligible || r.Label == nil {
			continue
		}
		if *r.Label {
			positiveNodes[r.Split][r.Component] = true
			for _, ep := range r.EpisodeKeys {
				episodes[r.Split][ep] = true
			}
		} else {
			controls[r.Split][r.Component] = true
		}
	}
	out.Ready = true
	for _, split := range []string{"train", "validation", "test"} {
		out.Counts[split+"_positive_components"] = len(positiveNodes[split])
		out.Counts[split+"_control_components"] = len(controls[split])
		out.Counts[split+"_independent_episodes"] = len(episodes[split])
		minEP, minPositive, minControl := 10, 5, 20
		if split == "train" {
			minEP, minPositive, minControl = 30, 10, 60
		}
		if len(episodes[split]) < minEP || len(positiveNodes[split]) < minPositive || len(controls[split]) < minControl {
			out.Ready = false
			out.Blockers = append(out.Blockers, split+":independent_episode_readiness_not_met")
		}
	}
	out.ExplorationReady = out.Ready
	out.Ready = out.ExplorationReady && out.PaperCoreComplete
	if spec.Profile == AvailableContextProfile {
		complete := true
		for _, r := range out.Rows {
			for _, name := range []string{"host_type", "gpu_model", "manufacturer", "main_board", "quota_group", "data_center", "rack"} {
				if r.Features.Categorical[name] == nil {
					out.Counts["context_missing:"+name]++
				}
			}
			if profileBlocks(r.Features.Blockers) {
				complete = false
			}
		}
		out.ProfileFeatureComplete = &complete
		out.Ready = out.ExplorationReady && complete
	}
	if !datasetFeatureReady(out) {
		if spec.Profile == AvailableContextProfile {
			out.Blockers = append(out.Blockers, "profile_required_source_fields_incomplete")
		} else {
			out.Blockers = append(out.Blockers, "paper_core_source_fields_incomplete")
		}
	}

	sha, e := digest(out)
	out.SHA = sha
	return out, e
}

func TestDatasetPreparedFullFrozenOracle(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&api.GPUNode{}, &api.HardwareFaultFeedbackRequest{}, &api.HardwareFaultFeedbackReview{}); err != nil {
		t.Fatal(err)
	}
	q0, _, es, rs := historyReviewedFixture()
	if err := db.Create(&rs).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&es).Error; err != nil {
		t.Fatal(err)
	}
	spec := DatasetSpec{Anchor: q0.Cutoff, TrainEnd: q0.Cutoff.Add(30 * 24 * time.Hour), ValidationEnd: q0.Cutoff.Add(60 * 24 * time.Hour), TestEnd: q0.Cutoff.Add(90 * 24 * time.Hour)}
	for n := 0; n < 3; n++ {
		p := fixture(t, 12)
		p.Cutoff = q0.Cutoff.Add(time.Duration(n) * 3 * 24 * time.Hour)
		for i := range p.Nodes {
			p.Nodes[i].AvailableAt = p.Cutoff.Add(-time.Hour)
		}
		p = reseal(t, p)
		q, err := Record(context.Background(), db, p)
		if err != nil {
			t.Fatal(err)
		}
		// Synthetic historical fixture: pin completion time as well as cutoff,
		// so both implementations receive reproducible as-of inputs.
		if err := db.Model(&api.GPURiskQuery{}).Where("id = ?", q.ID).Update("completed_at", q.Cutoff.Add(time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
		b, err := RecordOutcomes(context.Background(), db, q.ID, q0.Cutoff.Add(15*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		spec.BatchIDs = append(spec.BatchIDs, b.ID)
	}
	for _, profile := range []string{"paper_core", AvailableContextProfile} {
		t.Run(profile, func(t *testing.T) {
			spec.Profile = profile
			old, e := buildDatasetBefore(context.Background(), db, spec)
			got, f := BuildDataset(context.Background(), db, spec)
			if e != nil || f != nil {
				t.Fatal(e, f)
			}
			a, e := json.Marshal(old)
			z, f := json.Marshal(got)
			if e != nil || f != nil || !bytes.Equal(a, z) {
				t.Fatal("full JSON mismatch", e, f)
			}
			if len(got.Rows) != 36 || got.FeatureSHA != old.FeatureSHA || got.SplitSHA != old.SplitSHA || got.InputSHA != old.InputSHA || got.SHA != old.SHA {
				t.Fatal("rows or four SHA mismatch")
			}
			sum := sha256.Sum256(z)
			t.Logf("rows=%d bytes=%d JSON=%x feature=%s split=%s input=%s dataset=%s", len(got.Rows), len(z), sum, got.FeatureSHA, got.SplitSHA, got.InputSHA, got.SHA)
		})
	}
}

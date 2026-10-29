package gpurisk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ScoreRequestVersion = "gpu-host-model-score-request-v1"
const CandidateScorerVersion = "gpu-host-mlp-go-f64-v1"

type ModelRequestRow struct {
	NodeID     uint            `json:"node_id"`
	Generation int             `json:"generation"`
	EntityRef  string          `json:"entity_ref"`
	Features   HistoryFeatures `json:"features"`
	Blockers   []string        `json:"blockers"`
}
type ModelScoreRequest struct {
	Scorer          string            `json:"scorer"`
	Version         string            `json:"version"`
	Target          string            `json:"target"`
	FeatureContract string            `json:"feature_contract"`
	SchemaSHA       string            `json:"schema_sha"`
	QueryID         uint              `json:"query_id"`
	ModelID         uint              `json:"model_id"`
	Cutoff          time.Time         `json:"cutoff"`
	ScopeSHA        string            `json:"scope_sha"`
	RankingSHA      string            `json:"ranking_sha"`
	ArtifactSHA     string            `json:"artifact_sha"`
	PreprocessorSHA string            `json:"preprocessor_sha"`
	Rows            []ModelRequestRow `json:"rows"`
	SHA             string            `json:"sha"`
}

func frozenQuery(db *gorm.DB, id uint) (api.GPURiskQuery, Snapshot, []api.GPURiskMember, error) {
	var q api.GPURiskQuery
	var p Snapshot
	var scope api.GPURiskScope
	var members []api.GPURiskMember
	if db.First(&q, id).Error != nil || q.Target != Target || q.Contract != Contract || q.Status != "completed" || q.HorizonMinutes != 10080 || db.Where("sha = ?", q.ScopeSHA).First(&scope).Error != nil || json.Unmarshal([]byte(scope.Input), &p) != nil || validate(p, time.Now()) != nil || p.SHA != q.ScopeSHA || !p.Cutoff.Equal(q.Cutoff) {
		return q, p, members, ErrInput
	}
	q.Cutoff = q.Cutoff.UTC()
	expected, scores, e := Build(p)
	if e != nil {
		return q, p, members, e
	}
	hash, _ := digest([]any{expected, scores})
	if hash != q.ResultSHA {
		return q, p, members, ErrInput
	}
	if db.Where("query_id = ?", q.ID).Order("node_id,generation").Find(&members).Error != nil || len(members) != q.MemberCount {
		return q, p, members, ErrInput
	}
	normalized := append([]api.GPURiskMember{}, members...)
	for i := range normalized {
		normalized[i].ID = 0
		normalized[i].QueryID = 0
	}
	a, _ := digest(normalized)
	b, _ := digest(expected)
	if a != b {
		return q, p, members, ErrInput
	}
	return q, p, members, nil
}
func modelArtifact(m api.GPURiskModel) (RankerArtifact, error) {
	var a RankerArtifact
	h := sha256.Sum256([]byte(m.Artifact))
	if hex.EncodeToString(h[:]) != m.ArtifactSHA || json.Unmarshal([]byte(m.Artifact), &a) != nil || validateRanker(a) != nil || a.Purpose != "offline_exploration_not_promoted" || m.Version != RankerVersion || m.Target != Target || m.FeatureContract != HistoryFeatureVersion || m.Status != "offline_candidate" || a.DatasetSHA != m.DatasetSHA || a.DatasetManifestSHA != m.DatasetManifestSHA {
		return a, ErrInput
	}
	hash, _ := digest(a.Preprocessor)
	if hash != m.PreprocessorSHA {
		return a, ErrInput
	}
	return a, nil
}

// RegisterRanker accepts only a real, ready dataset re-exported from this DB.
// Parity alone is not admission; synthetic artifacts are categorically rejected.
func RegisterRanker(ctx context.Context, db *gorm.DB, dp, ds, ap, as, sp, ss string) (api.GPURiskModel, error) {
	var out api.GPURiskModel
	report, e := VerifyRankerFiles(dp, ds, ap, as, sp, ss)
	if e != nil || report.Purpose != "offline_exploration_not_promoted" {
		return out, ErrInput
	}
	var d Dataset
	var a RankerArtifact
	if pinnedJSON(dp, ds, &d) != nil || pinnedJSON(ap, as, &a) != nil || !d.Ready || !d.PaperCoreComplete || !d.ExplorationReady {
		return out, ErrInput
	}
	rebuilt, e := BuildDataset(ctx, db, d.Spec)
	if e != nil || rebuilt.SHA != d.SHA || !rebuilt.Ready {
		return out, ErrInput
	}
	// Retain the verified bytes, not an unpinned second filesystem read.
	raw, e := readPinnedBytes(ap, as)
	if e != nil {
		return out, e
	}
	prep, _ := digest(a.Preprocessor)
	parity, _ := digest(report)
	identities := map[string]bool{}
	e = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seen := map[uint]bool{}
		for _, r := range d.Rows {
			identities[r.EntityRef] = true
			if seen[r.QueryID] {
				continue
			}
			seen[r.QueryID] = true
			_, scope, _, err := frozenQuery(tx, r.QueryID)
			if err != nil {
				return err
			}
			for _, n := range scope.Nodes {
				for _, c := range n.Cards {
					if c.UUID != "" {
						identities["gpu:"+c.UUID] = true
					}
				}
			}
		}
		var evs []api.GPURiskEvidenceRevision
		if tx.Where("kind = ?", "identity").Limit(100001).Find(&evs).Error != nil || len(evs) > 100000 {
			return ErrInput
		}
		for _, ev := range evs {
			if ev.AvailableAt.After(d.Spec.TestEnd) {
				continue
			}
			var v EvidenceInput
			if json.Unmarshal([]byte(ev.Data), &v) != nil {
				return ErrInput
			}
			if identities[v.EntityRef] && v.GPUUUID != "" {
				identities["gpu:"+v.GPUUUID] = true
			}
		}
		refs := api.StringList{}
		for id := range identities {
			refs = append(refs, id)
		}
		sort.Strings(refs)
		out = api.GPURiskModel{ArtifactSHA: as, PreprocessorSHA: prep, DatasetSHA: ds, DatasetManifestSHA: d.SHA, ParitySHA: parity, Version: RankerVersion, Target: Target, FeatureContract: HistoryFeatureVersion, Status: "offline_candidate", TrainingThrough: d.Spec.TestEnd.UTC(), AvailableAt: time.Now().UTC().Truncate(time.Microsecond), Artifact: string(raw), TrainingIdentities: refs}
		if out.TrainingThrough.After(out.AvailableAt) {
			return ErrInput
		}
		if tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&out).Error != nil {
			return ErrInput
		}
		return tx.Where("artifact_sha = ?", as).First(&out).Error
	})
	return out, e
}
func readPinnedBytes(path, sha string) ([]byte, error) {
	data, e := readBoundedFile(path)
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(data)
	if hex.EncodeToString(h[:]) != sha {
		return nil, ErrInput
	}
	return data, nil
}

// BuildModelRequest reads a repeatable snapshot and filters all evidence at q.
// Training entities/physical UUIDs cannot enter a prospective certification cohort.
func BuildModelRequest(ctx context.Context, db *gorm.DB, queryID, modelID uint) (ModelScoreRequest, error) {
	out := ModelScoreRequest{Scorer: CandidateScorerVersion, Version: ScoreRequestVersion, Target: Target, FeatureContract: HistoryFeatureVersion, Rows: []ModelRequestRow{}}
	out.SchemaSHA, _ = digest(CoreSchema())
	e := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q, p, members, err := frozenQuery(tx, queryID)
		if err != nil {
			return err
		}
		var model api.GPURiskModel
		if tx.First(&model, modelID).Error != nil {
			return ErrInput
		}
		if _, e := modelArtifact(model); e != nil {
			return e
		}
		if !model.AvailableAt.Before(q.Cutoff) || !model.TrainingThrough.Before(q.Cutoff) {
			return ErrInput
		}
		out.QueryID = q.ID
		out.ModelID = model.ID
		out.Cutoff = q.Cutoff
		out.ScopeSHA = q.ScopeSHA
		out.RankingSHA = q.ResultSHA
		out.ArtifactSHA = model.ArtifactSHA
		out.PreprocessorSHA = model.PreprocessorSHA
		var evidence []api.GPURiskEvidenceRevision
		var reviews []api.HardwareFaultFeedbackReview
		if tx.Order("id").Limit(100001).Find(&evidence).Error != nil || tx.Order("feedback_request_id,revision").Limit(100001).Find(&reviews).Error != nil || len(evidence) > 100000 || len(reviews) > 100000 {
			return ErrInput
		}
		training := map[string]bool{}
		for _, id := range model.TrainingIdentities {
			training[id] = true
		}
		nodes := map[uint]Node{}
		for _, n := range p.Nodes {
			nodes[n.ID] = n
		}
		for _, m := range members {
			f, e := historyAt(ctx, m, q, evidence, reviews)
			if e != nil {
				return e
			}
			row := ModelRequestRow{NodeID: m.NodeID, Generation: m.Generation, EntityRef: m.EntityRef, Features: f, Blockers: []string{}}
			if m.Status != "included" {
				row.Blockers = append(row.Blockers, "scope:"+m.Reason)
			}
			n := nodes[m.NodeID]
			complete := n.Expected == len(n.Cards) && len(n.Cards) > 0 && n.Observed <= len(n.Cards)
			overlap := training[m.EntityRef]
			for _, c := range n.Cards {
				if c.UUID == "" || c.State != "active" || strings.HasPrefix(strings.ToLower(c.UUID), "mig") {
					complete = false
				}
				if training["gpu:"+c.UUID] {
					overlap = true
				}
			}
			if !complete {
				row.Blockers = append(row.Blockers, "gpu_identity_incomplete")
			}
			if overlap {
				row.Blockers = append(row.Blockers, "training_entity_overlap")
			}
			for _, b := range f.Blockers {
				if strings.Contains(b, "conflict") {
					row.Blockers = append(row.Blockers, b)
				}
			}
			out.Rows = append(out.Rows, row)
		}
		if len(out.Rows) == 0 || len(out.Rows) > 10000 {
			return ErrInput
		}
		out.SHA, _ = digest(out)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, e
}

func candidateScores(ctx context.Context, a RankerArtifact, r ModelScoreRequest) ([]api.GPURiskModelScore, bool, error) {
	rows := []api.GPURiskModelScore{}
	eligible := []int{}
	failed := false
	if len(r.Rows)*len(a.Layers[0].Weights[0]) > 2000000 {
		return rows, false, ErrInput
	}
	for _, m := range r.Rows {
		if e := ctx.Err(); e != nil {
			return nil, false, e
		}
		row := api.GPURiskModelScore{NodeID: m.NodeID, Generation: m.Generation, Warnings: append(api.StringList{}, m.Features.Blockers...), Blockers: append(api.StringList{}, m.Blockers...), SignalStatus: "blocked"}
		if len(row.Blockers) == 0 {
			z, e := rankerLogit(a, m.Features)
			if e != nil {
				failed = true
				row.Blockers = append(row.Blockers, "numerical_inference_failed")
			} else {
				row.RawScore = &z
				row.SignalStatus = "scored"
				if len(m.Features.Blockers) > 0 {
					row.SignalStatus = "scored_with_unknowns"
				}
				eligible = append(eligible, len(rows))
			}
		}
		rows = append(rows, row)
	}
	if failed {
		for i := range rows {
			rows[i].RawScore = nil
			rows[i].SignalStatus = "failed"
			rows[i].Blockers = append(rows[i].Blockers, "batch_inference_failed")
		}
		return rows, true, nil
	}
	sort.Slice(eligible, func(i, j int) bool {
		a, b := rows[eligible[i]], rows[eligible[j]]
		if *a.RawScore == *b.RawScore {
			return a.NodeID < b.NodeID
		}
		return *a.RawScore > *b.RawScore
	})
	for i := 0; i < len(eligible); {
		end := i + 1
		for end < len(eligible) && *rows[eligible[end]].RawScore == *rows[eligible[i]].RawScore {
			end++
		}
		for k := i; k < end; k++ {
			rows[eligible[k]].Rank = i + 1
			rows[eligible[k]].TieCount = end - i
		}
		i = end
	}
	for i := range rows {
		rows[i].EligibleCount = len(eligible)
		rows[i].TopFivePercentK = int(math.Ceil(float64(len(eligible)) * .05))
	}
	return rows, false, nil
}

// RecordModelScores validates a pinned request against the authoritative DB,
// computes all nodes in Go and atomically records a separate immutable batch.
func RecordModelScores(ctx context.Context, db *gorm.DB, request ModelScoreRequest) (api.GPURiskModelBatch, error) {
	var batch api.GPURiskModelBatch
	expected, e := BuildModelRequest(ctx, db, request.QueryID, request.ModelID)
	if e != nil {
		return batch, e
	}
	sha, _ := digest(request)
	want, _ := digest(expected)
	if sha != want {
		return batch, ErrInput
	}
	var model api.GPURiskModel
	if db.First(&model, request.ModelID).Error != nil {
		return batch, ErrInput
	}
	a, e := modelArtifact(model)
	if e != nil {
		return batch, e
	}
	rows, failed, e := candidateScores(ctx, a, request)
	if e != nil {
		return batch, e
	}
	data, _ := json.Marshal(request)
	result, _ := digest(rows)
	status, code := "completed", ""
	eligible := 0
	for _, r := range rows {
		if r.RawScore != nil {
			eligible++
		}
	}
	if eligible == 0 {
		status = "blocked"
	}
	if failed {
		status = "failed"
		code = "numerical_inference_failed"
	}
	batch = api.GPURiskModelBatch{Scorer: CandidateScorerVersion, RequestSHA: request.SHA, QueryID: request.QueryID, ModelID: request.ModelID, ArtifactSHA: request.ArtifactSHA, ResultSHA: result, MemberCount: len(rows), Status: status, ErrorCode: code, Input: string(data), CreatedAt: time.Now().UTC()}
	e = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Model admission and query rows are immutable. Serialize same-request writes.
		if tx.Dialector.Name() == "postgres" {
			if e := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", request.SHA).Error; e != nil {
				return e
			}
		}
		inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&batch)
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected == 0 {
			return tx.Where("request_sha = ?", request.SHA).First(&batch).Error
		}
		for i := range rows {
			rows[i].BatchID = batch.ID
		}
		return tx.CreateInBatches(&rows, 100).Error
	})
	return batch, e
}

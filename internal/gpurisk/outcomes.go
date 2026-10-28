package gpurisk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type projection struct {
	Key                        string
	NodeID                     uint
	Generation                 int
	EntityRef                  string
	From, Through, AvailableAt time.Time
	SHAs                       []string
	Conflict                   bool
}

func reviewWindow(r api.HardwareFaultFeedbackReview) (time.Time, time.Time) {
	var a, b time.Time
	if r.ConfirmedOnsetAt != nil {
		a = *r.ConfirmedOnsetAt
		b = a
	}
	if r.ConfirmedWindowStartAt != nil {
		a = *r.ConfirmedWindowStartAt
	}
	if r.ConfirmedWindowEndAt != nil {
		b = *r.ConfirmedWindowEndAt
	}
	return a, b
}

// EvaluateOutcomes is a bounded, read-only as-of projection. RecordOutcomes
// persists its immutable batch separately; neither path mutates predictions.
func EvaluateOutcomes(ctx context.Context, db *gorm.DB, queryID uint, asOf time.Time) (api.GPURiskOutcomeBatch, []api.GPURiskNodeOutcome, error) {
	var batch api.GPURiskOutcomeBatch
	rows := []api.GPURiskNodeOutcome{}
	if asOf.IsZero() || asOf.After(time.Now()) {
		return batch, rows, ErrInput
	}
	asOf = asOf.UTC().Truncate(time.Microsecond)
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var q api.GPURiskQuery
		if err := tx.Where("id = ? AND status = ?", queryID, "completed").First(&q).Error; err != nil {
			return err
		}
		if q.Contract != Contract || q.Target != Target || q.HorizonMinutes != 10080 || q.Cutoff.After(asOf) || q.CompletedAt == nil || q.CompletedAt.After(asOf) {
			return ErrInput
		}
		var members []api.GPURiskMember
		if err := tx.Where("query_id = ?", q.ID).Order("node_id,generation").Find(&members).Error; err != nil {
			return err
		}
		if len(members) != q.MemberCount || len(members) > MaxNodes {
			return ErrInput
		}
		var evs []api.GPURiskEvidenceRevision
		if err := tx.Where(timeSQL(tx, "available_at", "<="), asOf).Order("id").Limit(100001).Find(&evs).Error; err != nil {
			return err
		}
		if len(evs) > 100000 {
			return ErrInput
		}
		// The latest revision available AT asOf supersedes only the same chain.
		latest := map[string]api.GPURiskEvidenceRevision{}
		for _, e := range evs {
			if e.AvailableAt.After(asOf) {
				continue
			}
			k := e.Kind + ":" + e.Key
			if old, ok := latest[k]; !ok || e.Revision > old.Revision {
				latest[k] = e
			}
		}
		evidences := []api.GPURiskEvidenceRevision{}
		for _, e := range latest {
			evidences = append(evidences, e)
		}
		sort.Slice(evidences, func(i, j int) bool { return evidences[i].SHA < evidences[j].SHA })
		type assertion struct {
			Input  EvidenceInput
			Record api.GPURiskEvidenceRevision
		}
		assertions := []assertion{}
		for _, e := range evidences {
			var v EvidenceInput
			if json.Unmarshal([]byte(e.Data), &v) != nil {
				return ErrInput
			}
			assertions = append(assertions, assertion{v, e})
		}
		var reviews []api.HardwareFaultFeedbackReview
		if err := tx.Where(timeSQL(tx, "reviewed_at", "<=")+" AND "+timeSQL(tx, "created_at", "<="), asOf, asOf).Order("feedback_request_id, revision DESC").Limit(100001).Find(&reviews).Error; err != nil {
			return err
		}
		if len(reviews) > 100000 {
			return ErrInput
		}
		selected := []api.HardwareFaultFeedbackReview{}
		seen := map[uint]bool{}
		for _, r := range reviews {
			if r.ReviewedAt.After(asOf) || r.CreatedAt.After(asOf) {
				continue
			}
			if !seen[r.FeedbackRequestID] {
				selected = append(selected, r)
				seen[r.FeedbackRequestID] = true
			}
		}
		sort.Slice(selected, func(i, j int) bool { return selected[i].FeedbackRequestID < selected[j].FeedbackRequestID })
		end := q.Cutoff.Add(7 * 24 * time.Hour)
		episodes := map[string]projection{}
		excludedEpisodes := map[string]bool{}
		for _, r := range selected {
			if r.Decision == "excluded_no_hardware" && r.EpisodeKey != "" {
				excludedEpisodes[r.EpisodeKey] = true
			}
		}
		unresolved := false
		for _, r := range selected {
			if r.Decision == "excluded_no_hardware" || r.Decision == "context_only" {
				continue
			}
			a, b := reviewWindow(r)
			if a.IsZero() || b.IsZero() || b.Before(a) {
				unresolved = true
				continue
			}
			if !b.After(q.Cutoff) || a.After(end) {
				continue
			}
			found := false
			for _, ev := range assertions {
				v := ev.Input
				if v.Kind != "identity" || v.FeedbackID != r.FeedbackRequestID || v.ReviewSHA != r.ReviewSHA256 || v.From.After(a) || v.Through.Before(b) {
					continue
				}
				found = true
				k := r.EpisodeKey
				if k == "" {
					unresolved = true
					continue
				}
				p := projection{Key: k, NodeID: v.NodeID, Generation: v.Generation, EntityRef: v.EntityRef, From: a, Through: b, AvailableAt: ev.Record.AvailableAt, SHAs: []string{r.ReviewSHA256, ev.Record.SHA}, Conflict: r.Decision != "confirmed_hardware" || len(r.EvidenceKeys) == 0 || excludedEpisodes[k]}
				if old, ok := episodes[k]; ok {
					if old.NodeID != p.NodeID || old.Generation != p.Generation || old.EntityRef != p.EntityRef || !old.From.Equal(p.From) || !old.Through.Equal(p.Through) {
						old.Conflict = true
					}
					old.Conflict = old.Conflict || p.Conflict
					old.SHAs = append(old.SHAs, p.SHAs...)
					episodes[k] = old
				} else {
					episodes[k] = p
				}
			}
			if !found {
				unresolved = true
			}
		}
		// Unreviewed feedback potentially in this horizon must also prevent negatives.
		var feedback []api.HardwareFaultFeedbackRequest
		if err := tx.Where(timeSQL(tx, "created_at", "<="), asOf).Order("id").Limit(100001).Find(&feedback).Error; err != nil {
			return err
		}
		if len(feedback) > 100000 {
			return ErrInput
		}
		type candidate struct {
			ID                         uint
			From, Through, AvailableAt time.Time
		}
		candidates := []candidate{}
		for _, f := range feedback {
			if f.CreatedAt.After(asOf) {
				continue
			}
			if seen[f.ID] {
				continue
			}
			a, b := f.FaultOccurredAt, f.FaultOccurredAt
			if f.FaultWindowStartAt != nil {
				a = *f.FaultWindowStartAt
			}
			if f.FaultWindowEndAt != nil {
				b = *f.FaultWindowEndAt
			}
			if a.IsZero() || b.After(q.Cutoff) && !a.After(end) {
				candidates = append(candidates, candidate{f.ID, a, b, f.CreatedAt})
				unresolved = true
			}
		}
		inputSHA, err := digest([]any{OutcomeVersion, q.Key, q.ResultSHA, selected, evidences, candidates})
		if err != nil {
			return err
		}
		episodeKeys := []string{}
		for k := range episodes {
			episodeKeys = append(episodeKeys, k)
		}
		sort.Strings(episodeKeys)
		for _, m := range members {
			row := api.GPURiskNodeOutcome{NodeID: m.NodeID, Generation: m.Generation, Status: "pending_horizon", EpisodeKeys: api.StringList{}, EvidenceSHAs: api.StringList{}, Blockers: api.StringList{}}
			conflict, ambiguous, positive := false, false, false
			for _, k := range episodeKeys {
				p := episodes[k]
				if p.NodeID != m.NodeID || p.Generation != m.Generation {
					continue
				}
				row.EvidenceSHAs = append(row.EvidenceSHAs, p.SHAs...)
				if p.EntityRef != m.EntityRef || p.Conflict {
					conflict = true
					continue
				}
				if !p.From.After(q.Cutoff) || p.Through.After(end) {
					ambiguous = true
					continue
				}
				positive = true
				row.EpisodeKeys = append(row.EpisodeKeys, k)
			}
			coverage := map[string]bool{}
			censored := false
			for _, ev := range assertions {
				v := ev.Input
				if v.NodeID != m.NodeID || v.Generation != m.Generation || v.EntityRef != m.EntityRef {
					continue
				}
				if v.Kind == "coverage" && v.QueryID == q.ID && !v.From.After(q.Cutoff) && !v.Through.Before(end) {
					row.EvidenceSHAs = append(row.EvidenceSHAs, ev.Record.SHA)
					if v.Complete {
						coverage[v.Source] = true
					} else {
						censored = true
					}
				}
				if (v.Kind == "repair" || v.Kind == "intervention") && v.State != "withdrawn" && v.Through.After(q.Cutoff) && !v.From.After(end) {
					row.Intervention = true
					row.EvidenceSHAs = append(row.EvidenceSHAs, ev.Record.SHA)
				}
			}
			if m.Status != "included" {
				row.Status = "identity_conflict"
				row.Blockers = append(row.Blockers, m.Reason)
			} else if conflict {
				row.Status = "identity_conflict"
				row.Blockers = append(row.Blockers, "episode_mapping_conflict")
			} else if ambiguous {
				row.Status = "ambiguous"
				row.Blockers = append(row.Blockers, "onset_crosses_boundary")
			} else if positive {
				yes := true
				row.Status = "matured_positive"
				row.Positive = &yes
			} else if row.Intervention {
				row.Status = "intervention_affected"
				row.Blockers = append(row.Blockers, "intervention_in_horizon")
			} else if !asOf.Before(end) {
				row.Status = "pending_watermark"
				if unresolved {
					row.Blockers = append(row.Blockers, "unresolved_feedback")
				}
				for _, source := range []string{"hardware_fault_ledger", "monitoring", "identity"} {
					if !coverage[source] {
						row.Blockers = append(row.Blockers, "missing_coverage:"+source)
					}
				}
				if asOf.Before(end.Add(24 * time.Hour)) {
					row.Blockers = append(row.Blockers, "late_report_buffer")
				}
				if censored {
					row.Status = "censored"
					row.Blockers = append(row.Blockers, "incomplete_observation")
				}
				if len(row.Blockers) == 0 {
					no := false
					row.Status = "matured_negative"
					row.Positive = &no
				}
			}
			sort.Strings(row.EvidenceSHAs)
			rows = append(rows, row)
		}
		sha, e := digest([]any{OutcomeVersion, q.Key, asOf, inputSHA, rows})
		if e != nil {
			return e
		}
		batch = api.GPURiskOutcomeBatch{QueryID: q.ID, Version: OutcomeVersion, AsOf: asOf, InputSHA: inputSHA, SHA: sha, MemberCount: len(rows)}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return batch, rows, err
}

func RecordOutcomes(ctx context.Context, db *gorm.DB, queryID uint, asOf time.Time) (api.GPURiskOutcomeBatch, error) {
	return recordOutcomes(ctx, db, queryID, asOf, nil)
}
func recordOutcomes(ctx context.Context, db *gorm.DB, queryID uint, asOf time.Time, lease *Lease) (api.GPURiskOutcomeBatch, error) {
	batch, rows, err := EvaluateOutcomes(ctx, db, queryID, asOf)
	if err != nil {
		return batch, err
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if lease != nil {
			fence := tx.Model(&api.GPURiskJob{}).Where("key = ? AND owner = ? AND token = ? AND status = ? AND lease_until > ?", lease.Key, lease.Owner, lease.Token, "running", time.Now().UTC()).Update("status", "completed")
			if fence.Error != nil {
				return fence.Error
			}
			if fence.RowsAffected != 1 {
				return ErrFence
			}
		}
		var existing api.GPURiskOutcomeBatch
		e := tx.Where("sha = ?", batch.SHA).First(&existing).Error
		if e == nil {
			batch = existing
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&batch)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return tx.Where("sha = ?", batch.SHA).First(&batch).Error
		}
		for i := range rows {
			rows[i].BatchID = batch.ID
		}
		if len(rows) == 0 {
			return fmt.Errorf("empty ranking cohort")
		}
		return tx.CreateInBatches(rows, 100).Error
	})
	return batch, err
}

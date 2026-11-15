package gpurisk

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CoreRecurrenceStrategy is route R1 of plan v5: rank nodes by the number of
// automatic critical episodes (core-event ledger) that started in the 90 days
// before cutoff, ties broken by recency of the last core event (any age):
//
//	score = episodes_90d + 1/(1 + days_since_last_core_event)
//
// Only events with ObservedAt strictly before cutoff and imported before the
// batch is computed are used. Shadow only: no probability, alert or action.
const CoreRecurrenceStrategy = "core-recurrence-v1"

const coreRecurrenceWindow = 90 * 24 * time.Hour

// CoreCohortStrategy is route R4 of plan v5: R1 plus cold-start cohort terms
// from the 11-16 M3 retrospective (hypothesis, frozen here, judged only on
// prospective batches):
//
//	score = R1 + 0.5*[node first seen in DCGM < 14 days before cutoff] + 0.3*[RTX 4090]
//
// A node never seen in DCGM before cutoff counts as young; unknown model is not 4090.
const CoreCohortStrategy = "core-recurrence-cohort-v1"

const (
	cohortYoungDays   = 14
	cohortYoungWeight = 0.5
	cohort4090Weight  = 0.3
)

// ShadowStrategies are frozen daily, in order, by the shadow stage.
var ShadowStrategies = []string{ShadowStrategy, CoreRecurrenceStrategy, CoreCohortStrategy}

// ComputeShadowStrategy is the read-only projection of a named strategy.
func ComputeShadowStrategy(ctx context.Context, db *gorm.DB, queryID uint, now time.Time, strategy string) (api.GPURiskShadowBatch, []api.GPURiskShadowScore, error) {
	switch strategy {
	case ShadowStrategy:
		return ComputeShadow(ctx, db, queryID, now)
	case CoreRecurrenceStrategy:
		return computeCoreRoute(ctx, db, queryID, now, false)
	case CoreCohortStrategy:
		return computeCoreRoute(ctx, db, queryID, now, true)
	}
	return api.GPURiskShadowBatch{}, nil, ErrInput
}

// ComputeCoreRecurrence projects R1 for a frozen query.
func ComputeCoreRecurrence(ctx context.Context, db *gorm.DB, queryID uint, now time.Time) (api.GPURiskShadowBatch, []api.GPURiskShadowScore, error) {
	return computeCoreRoute(ctx, db, queryID, now, false)
}

func computeCoreRoute(ctx context.Context, db *gorm.DB, queryID uint, now time.Time, cohort bool) (api.GPURiskShadowBatch, []api.GPURiskShadowScore, error) {
	strategy := CoreRecurrenceStrategy
	if cohort {
		strategy = CoreCohortStrategy
	}
	var batch api.GPURiskShadowBatch
	rows := []api.GPURiskShadowScore{}
	now = now.UTC().Truncate(time.Microsecond)
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var q api.GPURiskQuery
		if err := tx.Where("id = ? AND status = ?", queryID, "completed").First(&q).Error; err != nil {
			return err
		}
		if q.Contract != Contract || q.CompletedAt == nil || q.Cutoff.After(now) {
			return ErrInput
		}
		cutoff := q.Cutoff.UTC()
		run, err := coreLedgerReady(tx, cutoff)
		if err != nil {
			return err
		}
		var members []api.GPURiskMember
		if err := tx.Where("query_id = ?", q.ID).Order("node_id,generation").Find(&members).Error; err != nil {
			return err
		}
		if len(members) != q.MemberCount || len(members) == 0 || len(members) > MaxNodes {
			return ErrInput
		}
		// Known-at-freeze guard: events imported up to this transaction's
		// start. The caller's now can predate a ledger import in the same
		// operations tick, so wall-clock time is used here.
		known := time.Now().UTC()
		if known.Before(now) {
			known = now
		}
		var events []api.GPUCoreEvent
		if err := tx.Where(timeSQL(tx, "observed_at", "<")+" AND "+timeSQL(tx, "imported_at", "<"), cutoff, known.Add(time.Microsecond)).Order("id").Limit(500001).Find(&events).Error; err != nil {
			return err
		}
		if len(events) > 500000 {
			return ErrInput
		}
		keep := events[:0]
		ids := []uint{}
		for _, e := range events {
			if e.ObservedAt.Before(cutoff) && !e.ImportedAt.After(known) {
				keep = append(keep, e)
				ids = append(ids, e.ID)
			}
		}
		eps := CoreEpisodes(keep)
		obs := map[string]api.GPUNodeObservation{}
		obsKeys := []string{}
		if cohort {
			var all []api.GPUNodeObservation
			if err := tx.Order("node_ip").Find(&all).Error; err != nil {
				return err
			}
			for _, o := range all {
				obs[o.NodeIP] = o
				obsKeys = append(obsKeys, o.NodeIP+"|"+o.FirstSeenDay+"|"+o.Model)
			}
			if len(all) == 0 {
				return ErrCoreLedgerStale
			}
		}
		inputSHA, err := digest([]any{strategy, CoreEventLedgerVersion, q.Key, q.ResultSHA, run.ID, ids, obsKeys})
		if err != nil {
			return err
		}
		score := map[int]float64{}
		for i, m := range members {
			row := api.GPURiskShadowScore{NodeID: m.NodeID, Generation: m.Generation}
			n90, last := 0, time.Time{}
			for _, ep := range eps[strings.TrimSpace(m.NodeIP)] {
				if !ep.Start.Before(cutoff.Add(-coreRecurrenceWindow)) {
					n90++
				}
				if ep.Last.After(last) {
					last = ep.Last
				}
			}
			row.History = n90
			s := float64(n90)
			if !last.IsZero() {
				d := cutoff.Sub(last).Hours() / 24
				row.DaysSince = &d
				s += 1 / (1 + math.Max(d, 0))
			}
			if cohort {
				ip := strings.TrimSpace(m.NodeIP)
				young := true
				if o, ok := obs[ip]; ok {
					if first, e := time.ParseInLocation("2006-01-02", o.FirstSeenDay, shanghai); e == nil && first.Before(cutoff) {
						young = cutoff.Sub(first) < cohortYoungDays*24*time.Hour
					}
					if strings.Contains(o.Model, "4090") {
						s += cohort4090Weight
					}
				}
				if young {
					s += cohortYoungWeight
				}
			}
			if m.Status == "included" {
				score[i] = s
			}
			rows = append(rows, row)
		}
		for i := range rows {
			s, ok := score[i]
			if !ok {
				continue
			}
			v := s
			rows[i].RawScore = &v
			higher, tie := 0, 0
			for _, o := range score {
				if o > s {
					higher++
				} else if o == s {
					tie++
				}
			}
			rows[i].Rank, rows[i].TieCount = higher+1, tie
		}
		timing := "retrospective"
		if !now.After(q.CompletedAt.UTC().Add(ShadowProspectiveWindow)) {
			timing = "prospective"
		}
		sha, err := digest([]any{strategy, q.Key, inputSHA, rows})
		if err != nil {
			return err
		}
		batch = api.GPURiskShadowBatch{QueryID: q.ID, Strategy: strategy, Cutoff: cutoff, ComputedAt: now, Timing: timing, InputSHA: inputSHA, SHA: sha, MemberCount: len(rows), ScoredCount: len(score)}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return batch, rows, err
}

// RecordShadowStrategy freezes a strategy batch for a query exactly once.
func RecordShadowStrategy(ctx context.Context, db *gorm.DB, queryID uint, now time.Time, strategy string) (api.GPURiskShadowBatch, error) {
	var existing api.GPURiskShadowBatch
	if e := db.WithContext(ctx).Where("query_id = ? AND strategy = ?", queryID, strategy).First(&existing).Error; e == nil {
		return existing, nil
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return existing, e
	}
	batch, rows, err := ComputeShadowStrategy(ctx, db, queryID, now, strategy)
	if err != nil {
		return batch, err
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&batch)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return tx.Where("query_id = ? AND strategy = ?", queryID, strategy).First(&batch).Error
		}
		for i := range rows {
			rows[i].BatchID = batch.ID
		}
		return tx.CreateInBatches(rows, 200).Error
	})
	return batch, err
}

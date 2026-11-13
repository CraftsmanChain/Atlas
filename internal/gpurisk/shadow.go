package gpurisk

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ShadowStrategy is the route-v2 main candidate (M2' simplified, decided
// 2026-11-13): an equal-weight average of three rank-normalised signals, each
// built only from rows available strictly before the frozen query cutoff:
//
//	history   - node hardware incident episodes (feedback merged per node within
//	            72h), excluding feedback whose latest pre-cutoff review is
//	            excluded_no_hardware or context_only;
//	recency   - days since the most recent such episode (none = least risky);
//	precursor - critical XID rule events on the node first observed in the 30
//	            days before cutoff.
//
// It is a shadow ranking only: no probability, no alert, no action.
const ShadowStrategy = "precursor-recency-history-v1"

// ShadowProspectiveWindow: a batch computed within this time after the query
// was frozen is recorded as prospective; anything later is retrospective and
// is excluded from the pre-registered E2' evaluation.
const ShadowProspectiveWindow = 6 * time.Hour

const shadowEpisodeMerge = 72 * time.Hour
const shadowPrecursorWindow = 30 * 24 * time.Hour

// shadowCriticalXIDs are XID codes treated as hardware precursors.
var shadowCriticalXIDs = []float64{48, 62, 63, 64, 74, 79, 92, 94, 95, 119, 154}

type shadowNode struct {
	episodes  int
	lastOnset time.Time
	precursor int
}

// ComputeShadow is a read-only projection of the shadow strategy for a query.
func ComputeShadow(ctx context.Context, db *gorm.DB, queryID uint, now time.Time) (api.GPURiskShadowBatch, []api.GPURiskShadowScore, error) {
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
		var members []api.GPURiskMember
		if err := tx.Where("query_id = ?", q.ID).Order("node_id,generation").Find(&members).Error; err != nil {
			return err
		}
		if len(members) != q.MemberCount || len(members) == 0 || len(members) > MaxNodes {
			return ErrInput
		}

		// Reviews known strictly before cutoff (latest revision per feedback).
		var reviews []api.HardwareFaultFeedbackReview
		if err := tx.Where(timeSQL(tx, "reviewed_at", "<")+" AND "+timeSQL(tx, "created_at", "<"), cutoff, cutoff).Order("feedback_request_id, revision DESC, id DESC").Limit(100001).Find(&reviews).Error; err != nil {
			return err
		}
		if len(reviews) > 100000 {
			return ErrInput
		}
		latest := map[uint]api.HardwareFaultFeedbackReview{}
		for _, r := range reviews {
			if _, ok := latest[r.FeedbackRequestID]; !ok && r.ReviewedAt.Before(cutoff) && r.CreatedAt.Before(cutoff) {
				latest[r.FeedbackRequestID] = r
			}
		}
		var feedback []api.HardwareFaultFeedbackRequest
		if err := tx.Where(timeSQL(tx, "created_at", "<"), cutoff).Order("id").Limit(100001).Find(&feedback).Error; err != nil {
			return err
		}
		if len(feedback) > 100000 {
			return ErrInput
		}
		onsets := map[string][]time.Time{}
		inputKeys := []string{}
		for _, f := range feedback {
			if !f.CreatedAt.Before(cutoff) {
				continue
			}
			ip, onset := strings.TrimSpace(f.NodeIP), f.FaultOccurredAt
			if f.FaultWindowStartAt != nil {
				onset = *f.FaultWindowStartAt
			}
			sha := ""
			if r, ok := latest[f.ID]; ok {
				if r.Decision == "excluded_no_hardware" || r.Decision == "context_only" {
					continue
				}
				sha = r.ReviewSHA256
				if r.Decision == "confirmed_hardware" {
					if a, _ := reviewWindow(r); !a.IsZero() {
						onset = a
					}
					if v := strings.TrimSpace(r.ConfirmedNodeIP); v != "" {
						ip = v
					}
				}
			}
			if ip == "" || onset.IsZero() || !onset.Before(cutoff) {
				continue
			}
			onsets[ip] = append(onsets[ip], onset.UTC())
			inputKeys = append(inputKeys, f.RequestKey+"|"+sha)
		}

		var events []api.GPUFaultEvent
		if err := tx.Where("rule_code = ? AND observed_value IN ? AND "+timeSQL(tx, "created_at", "<")+" AND "+timeSQL(tx, "first_observed_at", ">=")+" AND "+timeSQL(tx, "first_observed_at", "<"), "recent_xid_change", shadowCriticalXIDs, cutoff, cutoff.Add(-shadowPrecursorWindow), cutoff).Order("id").Limit(200001).Find(&events).Error; err != nil {
			return err
		}
		if len(events) > 200000 {
			return ErrInput
		}
		nodes := map[string]*shadowNode{}
		get := func(ip string) *shadowNode {
			if nodes[ip] == nil {
				nodes[ip] = &shadowNode{}
			}
			return nodes[ip]
		}
		eventIDs := []uint{}
		for _, e := range events {
			// timeSQL widens "<" on SQLite; exact bounds are enforced here.
			if !e.CreatedAt.Before(cutoff) || !e.FirstObservedAt.Before(cutoff) || e.FirstObservedAt.Before(cutoff.Add(-shadowPrecursorWindow)) {
				continue
			}
			get(strings.TrimSpace(e.NodeIP)).precursor++
			eventIDs = append(eventIDs, e.ID)
		}
		for ip, ts := range onsets {
			sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
			n := get(ip)
			var start time.Time
			for _, t := range ts {
				if start.IsZero() || t.Sub(start) > shadowEpisodeMerge {
					n.episodes++
					start = t
				}
				n.lastOnset = t
			}
		}
		sort.Strings(inputKeys)
		inputSHA, err := digest([]any{ShadowStrategy, q.Key, q.ResultSHA, inputKeys, eventIDs})
		if err != nil {
			return err
		}

		// Rank-normalise each signal over included members (average ranks for ties).
		idx := []int{}
		for i, m := range members {
			if m.Status == "included" {
				idx = append(idx, i)
			}
		}
		hist := make([]float64, len(idx))
		rec := make([]float64, len(idx))
		pre := make([]float64, len(idx))
		for k, i := range idx {
			n := nodes[strings.TrimSpace(members[i].NodeIP)]
			if n == nil {
				n = &shadowNode{}
			}
			hist[k] = float64(n.episodes)
			pre[k] = float64(n.precursor)
			if !n.lastOnset.IsZero() {
				rec[k] = -cutoff.Sub(n.lastOnset).Hours() / 24 // more recent = larger
			} else {
				rec[k] = -1e12
			}
		}
		ph, pr, pp := percentile(hist), percentile(rec), percentile(pre)
		score := map[int]float64{}
		for k, i := range idx {
			score[i] = (ph[k] + pr[k] + pp[k]) / 3
		}
		for i, m := range members {
			row := api.GPURiskShadowScore{NodeID: m.NodeID, Generation: m.Generation}
			if n := nodes[strings.TrimSpace(m.NodeIP)]; n != nil {
				row.History, row.Precursor = n.episodes, n.precursor
				if !n.lastOnset.IsZero() {
					d := cutoff.Sub(n.lastOnset).Hours() / 24
					row.DaysSince = &d
				}
			}
			if s, ok := score[i]; ok {
				v := s
				row.RawScore = &v
				higher, tie := 0, 0
				for _, o := range score {
					if o > s {
						higher++
					} else if o == s {
						tie++
					}
				}
				row.Rank, row.TieCount = higher+1, tie
			}
			rows = append(rows, row)
		}
		timing := "retrospective"
		if !now.After(q.CompletedAt.UTC().Add(ShadowProspectiveWindow)) {
			timing = "prospective"
		}
		sha, err := digest([]any{ShadowStrategy, q.Key, inputSHA, rows})
		if err != nil {
			return err
		}
		batch = api.GPURiskShadowBatch{QueryID: q.ID, Strategy: ShadowStrategy, Cutoff: cutoff, ComputedAt: now, Timing: timing, InputSHA: inputSHA, SHA: sha, MemberCount: len(rows), ScoredCount: len(idx)}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return batch, rows, err
}

// percentile maps values to average-rank percentiles in [0,1] (ties averaged).
func percentile(v []float64) []float64 {
	out := make([]float64, len(v))
	if len(v) <= 1 {
		return out
	}
	order := make([]int, len(v))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return v[order[a]] < v[order[b]] })
	for i := 0; i < len(order); {
		j := i
		for j+1 < len(order) && v[order[j+1]] == v[order[i]] {
			j++
		}
		avg := float64(i+j) / 2 / float64(len(v)-1)
		for k := i; k <= j; k++ {
			out[order[k]] = avg
		}
		i = j + 1
	}
	return out
}

// RecordShadow freezes the shadow batch for a query exactly once. If a batch for
// (query, strategy) already exists it is returned unchanged and never recomputed.
func RecordShadow(ctx context.Context, db *gorm.DB, queryID uint, now time.Time) (api.GPURiskShadowBatch, error) {
	var existing api.GPURiskShadowBatch
	if e := db.WithContext(ctx).Where("query_id = ? AND strategy = ?", queryID, ShadowStrategy).First(&existing).Error; e == nil {
		return existing, nil
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return existing, e
	}
	batch, rows, err := ComputeShadow(ctx, db, queryID, now)
	if err != nil {
		return batch, err
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&batch)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return tx.Where("query_id = ? AND strategy = ?", queryID, ShadowStrategy).First(&batch).Error
		}
		for i := range rows {
			rows[i].BatchID = batch.ID
		}
		return tx.CreateInBatches(rows, 200).Error
	})
	return batch, err
}

// ShadowDaily freezes the shadow ranking of the newest completed query.
func ShadowDaily(ctx context.Context, db *gorm.DB, now time.Time) error {
	var q api.GPURiskQuery
	if e := db.WithContext(ctx).Where("status = ? AND contract = ?", "completed", Contract).Order("cutoff DESC,id DESC").First(&q).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		return e
	}
	_, err := RecordShadow(ctx, db, q.ID, now)
	return err
}

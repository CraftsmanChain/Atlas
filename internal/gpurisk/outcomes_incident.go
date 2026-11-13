package gpurisk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IncidentOutcomeVersion pins the node-level hardware incident target (route v2
// target T-A). It is a separate, additive label contract: batches carry this
// version and are never consumed by gpu-node-outcome-v1 (confirmed GPU
// hardware, T-C) datasets, which stay byte-identical.
//
// T-A semantics, all evaluated strictly as-of:
//   - A positive is any feedback whose latest as-of review is confirmed_hardware
//     (any target scope: gpu, multi_gpu, baseboard or node) whose confirmed
//     onset window lies entirely inside (cutoff, cutoff+7d] and whose confirmed
//     node IP is an included member of the frozen query. No per-query identity
//     ingest is required: the unit is the node, not a GPU slot.
//   - An onset window that crosses either boundary makes that node ambiguous.
//   - Contrary identity evidence (an identity interval on the same node IP that
//     covers the onset with a different host serial than the frozen member)
//     makes that node identity_conflict, never a positive. Missing identity at
//     onset is not contrary evidence: monitoring blind spots are frequently the
//     failure itself.
//   - Unreviewed or needs_evidence feedback overlapping the horizon blocks only
//     the node it names. Feedback without a node IP fails closed for the whole
//     query, as in v1.
//   - Negatives keep every v1 safeguard: full horizon elapsed, 24h late report
//     buffer, complete coverage assertions from all three sources for this
//     member and query, and no intervention in the horizon.
const IncidentOutcomeVersion = "gpu-node-incident-outcome-v1"

// IncidentTarget is the CLI/report name of the T-A target.
const IncidentTarget = "node_hardware_incident"

type incidentHit struct {
	key      string
	from, to time.Time
	sha      string
}

// EvaluateIncidentOutcomes is the read-only T-A projection for one frozen query.
func EvaluateIncidentOutcomes(ctx context.Context, db *gorm.DB, queryID uint, asOf time.Time) (api.GPURiskOutcomeBatch, []api.GPURiskNodeOutcome, error) {
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
		end := q.Cutoff.Add(7 * 24 * time.Hour)
		var members []api.GPURiskMember
		if err := tx.Where("query_id = ?", q.ID).Order("node_id,generation").Find(&members).Error; err != nil {
			return err
		}
		if len(members) != q.MemberCount || len(members) > MaxNodes {
			return ErrInput
		}
		included := map[string][]int{}
		for i, m := range members {
			if m.Status == "included" && strings.TrimSpace(m.NodeIP) != "" {
				ip := strings.TrimSpace(m.NodeIP)
				included[ip] = append(included[ip], i)
			}
		}

		// Latest evidence revision per chain available at asOf (coverage and interventions).
		var evs []api.GPURiskEvidenceRevision
		if err := tx.Where(timeSQL(tx, "available_at", "<="), asOf).Order("id").Limit(100001).Find(&evs).Error; err != nil {
			return err
		}
		if len(evs) > 100000 {
			return ErrInput
		}
		latest := map[string]api.GPURiskEvidenceRevision{}
		for _, e := range evs {
			if e.AvailableAt.After(asOf) || (e.Kind != "coverage" && e.Kind != "repair" && e.Kind != "intervention") {
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
		inputs := make([]EvidenceInput, len(evidences))
		for i, e := range evidences {
			if json.Unmarshal([]byte(e.Data), &inputs[i]) != nil {
				return ErrInput
			}
		}

		// Latest review per feedback known at asOf.
		var reviews []api.HardwareFaultFeedbackReview
		if err := tx.Where(timeSQL(tx, "reviewed_at", "<=")+" AND "+timeSQL(tx, "created_at", "<="), asOf, asOf).Order("feedback_request_id, revision DESC, id DESC").Limit(100001).Find(&reviews).Error; err != nil {
			return err
		}
		if len(reviews) > 100000 {
			return ErrInput
		}
		latestReview := map[uint]api.HardwareFaultFeedbackReview{}
		for _, r := range reviews {
			if r.ReviewedAt.After(asOf) || r.CreatedAt.After(asOf) {
				continue
			}
			if _, ok := latestReview[r.FeedbackRequestID]; !ok {
				latestReview[r.FeedbackRequestID] = r
			}
		}
		var feedback []api.HardwareFaultFeedbackRequest
		if err := tx.Where(timeSQL(tx, "created_at", "<="), asOf).Order("id").Limit(100001).Find(&feedback).Error; err != nil {
			return err
		}
		if len(feedback) > 100000 {
			return ErrInput
		}

		hits := map[string][]incidentHit{}
		ambiguous := map[string][]string{}
		unresolved := map[string]bool{}
		globalUnresolved := false
		inputKeys := []string{}
		for _, f := range feedback {
			if f.CreatedAt.After(asOf) {
				continue
			}
			r, reviewed := latestReview[f.ID]
			if reviewed && (r.Decision == "excluded_no_hardware" || r.Decision == "context_only") {
				inputKeys = append(inputKeys, fmt.Sprintf("f%d:r%s:%s", f.ID, r.ReviewSHA256, r.Decision))
				continue
			}
			ip := strings.TrimSpace(f.NodeIP)
			var a, b time.Time
			confirmed := reviewed && r.Decision == "confirmed_hardware"
			if confirmed {
				a, b = reviewWindow(r)
				if v := strings.TrimSpace(r.ConfirmedNodeIP); v != "" {
					ip = v
				}
				inputKeys = append(inputKeys, fmt.Sprintf("f%d:r%s:%s", f.ID, r.ReviewSHA256, r.Decision))
			} else {
				a, b = f.FaultOccurredAt, f.FaultOccurredAt
				if f.FaultWindowStartAt != nil {
					a = *f.FaultWindowStartAt
				}
				if f.FaultWindowEndAt != nil {
					b = *f.FaultWindowEndAt
				}
				sha := ""
				if reviewed {
					sha = r.ReviewSHA256
				}
				inputKeys = append(inputKeys, fmt.Sprintf("f%d:r%s:open", f.ID, sha))
			}
			if a.IsZero() || b.IsZero() || b.Before(a) {
				// An unusable fault time cannot be placed: fail closed on its node, or
				// on the whole query when the node is unknown.
				if ip == "" {
					globalUnresolved = true
				} else {
					unresolved[ip] = true
				}
				continue
			}
			if !b.After(q.Cutoff) || a.After(end) {
				continue // entirely outside this query's horizon
			}
			if ip == "" {
				globalUnresolved = true
				continue
			}
			if !confirmed {
				unresolved[ip] = true
				continue
			}
			key := strings.TrimSpace(r.EpisodeKey)
			if key == "" {
				key = fmt.Sprintf("hwfb-%d", f.ID)
			}
			if !a.After(q.Cutoff) || b.After(end) {
				ambiguous[ip] = append(ambiguous[ip], key)
				continue
			}
			hits[ip] = append(hits[ip], incidentHit{key: key, from: a, to: b, sha: r.ReviewSHA256})
		}
		sort.Strings(inputKeys)

		// Contrary identity evidence: same IP, interval covering onset, different serial.
		conflicts := map[string]bool{}
		identityKeys := []string{}
		for ip, hs := range hits {
			idx, ok := included[ip]
			if !ok {
				continue
			}
			for _, h := range hs {
				var serials []string
				if err := tx.Model(&api.HistoricalGPUIdentityInterval{}).Where("node_ip = ? AND first_seen_at <= ? AND last_seen_at >= ? AND host_serial <> ''", ip, h.to, h.from).Distinct().Pluck("host_serial", &serials).Error; err != nil {
					return err
				}
				sort.Strings(serials)
				identityKeys = append(identityKeys, ip+"|"+h.key+"|"+strings.Join(serials, ","))
				for _, i := range idx {
					m := members[i]
					if !strings.HasPrefix(m.EntityRef, "serial:") || len(serials) == 0 {
						continue
					}
					want := strings.TrimPrefix(m.EntityRef, "serial:")
					match := false
					for _, s := range serials {
						if s == want {
							match = true
						}
					}
					if !match {
						conflicts[fmt.Sprintf("%s|%d|%d", ip, m.NodeID, m.Generation)] = true
					}
				}
			}
		}
		sort.Strings(identityKeys)

		inputSHA, err := digest([]any{IncidentOutcomeVersion, q.Key, q.ResultSHA, inputKeys, identityKeys, evidences, globalUnresolved})
		if err != nil {
			return err
		}
		for _, m := range members {
			ip := strings.TrimSpace(m.NodeIP)
			row := api.GPURiskNodeOutcome{NodeID: m.NodeID, Generation: m.Generation, Status: "pending_horizon", EpisodeKeys: api.StringList{}, EvidenceSHAs: api.StringList{}, Blockers: api.StringList{}}
			coverage := map[string]bool{}
			censored := false
			for i, v := range inputs {
				if v.NodeID != m.NodeID || v.Generation != m.Generation || v.EntityRef != m.EntityRef {
					continue
				}
				if v.Kind == "coverage" && v.QueryID == q.ID && !v.From.After(q.Cutoff) && !v.Through.Before(end) {
					row.EvidenceSHAs = append(row.EvidenceSHAs, evidences[i].SHA)
					if v.Complete {
						coverage[v.Source] = true
					} else {
						censored = true
					}
				}
				if (v.Kind == "repair" || v.Kind == "intervention") && v.State != "withdrawn" && v.Through.After(q.Cutoff) && !v.From.After(end) {
					row.Intervention = true
					row.EvidenceSHAs = append(row.EvidenceSHAs, evidences[i].SHA)
				}
			}
			for _, h := range hits[ip] {
				row.EpisodeKeys = append(row.EpisodeKeys, h.key)
				row.EvidenceSHAs = append(row.EvidenceSHAs, h.sha)
			}
			switch {
			case m.Status != "included":
				row.Status = "identity_conflict"
				row.Blockers = append(row.Blockers, m.Reason)
			case conflicts[fmt.Sprintf("%s|%d|%d", ip, m.NodeID, m.Generation)]:
				row.Status = "identity_conflict"
				row.Blockers = append(row.Blockers, "incident_serial_mismatch")
			case len(ambiguous[ip]) > 0 && len(hits[ip]) == 0:
				row.Status = "ambiguous"
				row.Blockers = append(row.Blockers, "onset_crosses_boundary")
				row.EpisodeKeys = append(row.EpisodeKeys, ambiguous[ip]...)
			case len(hits[ip]) > 0:
				yes := true
				row.Status = "matured_positive"
				row.Positive = &yes
			case row.Intervention:
				row.Status = "intervention_affected"
				row.Blockers = append(row.Blockers, "intervention_in_horizon")
			case !asOf.Before(end):
				row.Status = "pending_watermark"
				if unresolved[ip] || globalUnresolved {
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
			sort.Strings(row.EpisodeKeys)
			sort.Strings(row.EvidenceSHAs)
			rows = append(rows, row)
		}
		sha, e := digest([]any{IncidentOutcomeVersion, q.Key, asOf, inputSHA, rows})
		if e != nil {
			return e
		}
		batch = api.GPURiskOutcomeBatch{QueryID: q.ID, Version: IncidentOutcomeVersion, AsOf: asOf, InputSHA: inputSHA, SHA: sha, MemberCount: len(rows)}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return batch, rows, err
}

// RecordIncidentOutcomes persists an immutable T-A batch (idempotent by SHA).
func RecordIncidentOutcomes(ctx context.Context, db *gorm.DB, queryID uint, asOf time.Time) (api.GPURiskOutcomeBatch, error) {
	return recordIncidentOutcomes(ctx, db, queryID, asOf, nil)
}

func recordIncidentOutcomes(ctx context.Context, db *gorm.DB, queryID uint, asOf time.Time, lease *Lease) (api.GPURiskOutcomeBatch, error) {
	batch, rows, err := EvaluateIncidentOutcomes(ctx, db, queryID, asOf)
	if err != nil {
		return batch, err
	}
	if len(rows) == 0 {
		return batch, fmt.Errorf("empty ranking cohort")
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
		return tx.CreateInBatches(rows, 100).Error
	})
	return batch, err
}

package gpurisk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// ErrCoverageSourceIncomplete marks a canonical source whose durable record does
// not currently support an honest completeness watermark. The check is refused
// rather than synthesized; a degraded source is never certified complete.
var ErrCoverageSourceIncomplete = errors.New("coverage source has no complete canonical durable record")

// CoverageCheckInput names the history source an operator pulled in full. It
// deliberately carries no timestamp: ObservedAt is the server clock taken while
// the source is actually read and is never accepted from a caller.
type CoverageCheckInput struct {
	Source string
	Owner  string
}

// RecordSourceCoverage reads the named source and appends an immutable record
// that it was complete through the moment of that read. RecordCount and
// CheckSHA256 come from the rows actually read, never from the caller, so the
// record restates what was visible when it ran. ObservedAt is the server clock;
// VerifiedThrough is the moment the full pull completed (rule A). A new row is
// always appended, and consumers take the latest.
//
// Two canonical durable tables are read live, so their pull completes now:
// hardware_fault_ledger (HardwareFaultFeedbackRequest) and identity
// (HistoricalGPUIdentityInterval). The monitoring source has no Atlas table of
// raw samples; its canonical durable record is MonitoringHistoryAudit, the
// system's own bounded read of the configured source, so the monitoring pull is
// the newest completed audit and a partial or failed newest audit is refused.
func RecordSourceCoverage(ctx context.Context, db *gorm.DB, in CoverageCheckInput) (api.GPURiskSourceCoverageCheck, error) {
	var out api.GPURiskSourceCoverageCheck
	source := strings.TrimSpace(in.Source)
	owner := strings.TrimSpace(in.Owner)
	if owner == "" || len(owner) > 128 {
		return out, ErrInput
	}
	now := evidenceNow().UTC().Truncate(time.Microsecond)
	keys, through, err := readSourceCoverage(ctx, db, source, now)
	if err != nil {
		return out, err
	}
	sha, err := digest(keys)
	if err != nil {
		return out, err
	}
	out = api.GPURiskSourceCoverageCheck{
		Source:          source,
		VerifiedThrough: through,
		ObservedAt:      now,
		RecordCount:     len(keys),
		CheckSHA256:     sha,
		Owner:           owner,
		Contract:        CoverageWatermarkContract,
		CreatedAt:       now,
	}
	if err := db.WithContext(ctx).Create(&out).Error; err != nil {
		return api.GPURiskSourceCoverageCheck{}, err
	}
	return out, nil
}

// readSourceCoverage reads the stable record keys of one canonical source and
// its legacy (v2) completeness watermark. It never writes.
func readSourceCoverage(ctx context.Context, db *gorm.DB, source string, now time.Time) ([]string, time.Time, error) {
	keys := []string{}
	through := now
	switch source {
	case "hardware_fault_ledger":
		var err error
		keys, err = ledgerKeys(ctx, db)
		if err != nil {
			return nil, time.Time{}, err
		}
	case "identity":
		var err error
		keys, err = identityKeys(ctx, db)
		if err != nil {
			return nil, time.Time{}, err
		}
	case "monitoring":
		var rows []api.MonitoringHistoryAudit
		if err := db.WithContext(ctx).Order("source_key, id").Find(&rows).Error; err != nil {
			return nil, time.Time{}, err
		}
		for _, r := range rows {
			keys = append(keys, fmt.Sprintf("%s|%d", r.SourceKey, r.ID))
		}
		pulled, err := monitoringWatermark(rows, now)
		if err != nil {
			return nil, time.Time{}, err
		}
		through = pulled
	default:
		return nil, time.Time{}, ErrInput
	}
	return keys, through, nil
}

func ledgerKeys(ctx context.Context, db *gorm.DB) ([]string, error) {
	keys := []string{}
	var rows []api.HardwareFaultFeedbackRequest
	if err := db.WithContext(ctx).Order("request_key").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		keys = append(keys, r.RequestKey)
	}
	return keys, nil
}

func identityKeys(ctx context.Context, db *gorm.DB) ([]string, error) {
	keys := []string{}
	var rows []api.HistoricalGPUIdentityInterval
	if err := db.WithContext(ctx).Order("interval_key").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		keys = append(keys, r.IntervalKey)
	}
	return keys, nil
}

// MemberCoverageVersion pins the per-member coverage assertion contract. A
// member assertion restates the source-level coverage check for one frozen
// member: it is derived, never a second, independent judgement of completeness.
const MemberCoverageVersion = "gpu-host-member-coverage-v1"

// memberCoverageKey is the stable evidence key shared with ingestPrepare, so a
// member has one coverage chain per source no matter which entry minted it.
func memberCoverageKey(queryID, nodeID uint, generation int, source string) string {
	return fmt.Sprintf("ingest-coverage-%d-%d-%d-%s", queryID, nodeID, generation, source)
}

// RecordMemberCoverage appends one coverage assertion per included member of a
// query, all derived from the newest signed source-level coverage check. The
// batch is refused fail-closed (ErrCoverageSourceIncomplete) unless that check
// still reaches into the bounded unknown tail (MaxSourceLag) and never shows a
// future observation, because a member can never be more complete than the
// source that observed it. From/Through/Complete are taken from the check, not
// the caller; the operator only supplies Owner. EvidenceSHA is the
// deterministic pin over the query, its members and the check that justified
// them, so an unchanged state re-runs idempotently instead of minting a
// revision.
func RecordMemberCoverage(ctx context.Context, db *gorm.DB, queryID uint, source, owner string) ([]api.GPURiskEvidenceRevision, error) {
	source = strings.TrimSpace(source)
	owner = strings.TrimSpace(owner)
	if queryID == 0 || owner == "" || len(owner) > 128 {
		return nil, ErrInput
	}
	if source != "hardware_fault_ledger" && source != "monitoring" && source != "identity" {
		return nil, ErrInput
	}
	plan := []EvidenceInput{}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := evidenceNow().UTC().Truncate(time.Microsecond)
		var q api.GPURiskQuery
		if e := tx.Take(&q, queryID).Error; e != nil {
			return ErrInput
		}
		if q.Status != "completed" || q.Contract != Contract || q.Target != Target || q.HorizonMinutes != 10080 || !validSHA(q.ScopeSHA) {
			return ErrInput
		}
		var checks []api.GPURiskSourceCoverageCheck
		query := tx.Where("source = ?", source)
		if source == "hardware_fault_ledger" {
			// v3: the ledger is an irregular offline import. Only an explicit
			// operator delivery declaration states how far it is complete; a
			// legacy "table read at now" check is never used for members.
			query = query.Where("contract = ?", LedgerDeclaredContract)
		}
		if e := query.Order("observed_at DESC, id DESC").Limit(1).Find(&checks).Error; e != nil {
			return e
		}
		if len(checks) != 1 {
			return ErrCoverageSourceIncomplete
		}
		check := checks[0]
		through := check.VerifiedThrough.UTC().Truncate(time.Microsecond)
		if through.IsZero() || through.Before(q.Cutoff.Add(-MaxSourceLag)) || check.ObservedAt.After(now) || through.After(now) {
			return ErrCoverageSourceIncomplete
		}
		from := q.Cutoff.UTC().Truncate(time.Microsecond)
		if through.Before(from) {
			// evidenceValid requires Through >= From; a watermark inside the tail
			// is only ever asserted from itself forward.
			from = through
		}
		var members []api.GPURiskMember
		if e := tx.Where("query_id = ? AND status = ?", q.ID, "included").Order("node_id, generation, entity_ref").Find(&members).Error; e != nil {
			return e
		}
		if len(members) == 0 {
			return ErrInput
		}
		pin, e := digest([]any{MemberCoverageVersion, q.ID, q.ScopeSHA, source, check.ID, check.CheckSHA256, through, check.ObservedAt.UTC(), members})
		if e != nil {
			return e
		}
		for _, m := range members {
			if m.EntityRef == "" {
				return ErrInput
			}
			v := EvidenceInput{
				Kind: "coverage", Key: memberCoverageKey(q.ID, m.NodeID, m.Generation, source),
				NodeID: m.NodeID, Generation: m.Generation, EntityRef: m.EntityRef,
				QueryID: q.ID, Source: source, From: from, Through: through,
				Complete: true, Owner: owner, EvidenceSHA: pin,
			}
			var heads []api.GPURiskEvidenceRevision
			if e := tx.Where("kind = ? AND key = ?", v.Kind, v.Key).Order("revision DESC").Limit(1).Find(&heads).Error; e != nil {
				return e
			}
			if len(heads) == 1 {
				// Compare semantic content without the CAS token: re-deriving the
				// same watermark must reuse the head, while a changed watermark
				// re-fences onto it as a new revision.
				var prior EvidenceInput
				if json.Unmarshal([]byte(heads[0].Data), &prior) != nil {
					return ErrInput
				}
				left, right := v, prior
				left.PreviousSHA, right.PreviousSHA = "", ""
				l, _ := digest(left)
				r, _ := digest(right)
				if l == r {
					v = prior
				} else {
					v.PreviousSHA = heads[0].SHA
				}
			}
			plan = append(plan, v)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	out := make([]api.GPURiskEvidenceRevision, 0, len(plan))
	for _, v := range plan {
		row, e := AppendEvidence(ctx, db, v)
		if e != nil {
			return out, e
		}
		out = append(out, row)
	}
	return out, nil
}

// monitoringWatermark derives the completeness watermark of the monitoring
// source from its canonical durable record. Every source key in the audit table
// must have a newest audit whose bounded read actually completed — status
// "success" (any failed sub-probe demotes an audit to "partial") with real GPU
// series observed — and the watermark is the oldest of those completed pulls,
// because coverage is only as good as the least recently verified source. The
// missing research metric families are a capability limitation this exporter
// version reports on every audit, healthy ones included, so they do not by
// themselves mean the pull was incomplete; the audit status is the honest
// signal. A stale, partial or failed newest audit means the pull is not
// complete, so the check is refused (fail-closed) and never synthesized from an
// older healthy row.
func monitoringWatermark(rows []api.MonitoringHistoryAudit, now time.Time) (time.Time, error) {
	if len(rows) == 0 {
		return time.Time{}, ErrCoverageSourceIncomplete
	}
	newest := map[string]api.MonitoringHistoryAudit{}
	for _, r := range rows {
		cur, ok := newest[r.SourceKey]
		if !ok || r.FinishedAt.After(cur.FinishedAt) || (r.FinishedAt.Equal(cur.FinishedAt) && r.ID > cur.ID) {
			newest[r.SourceKey] = r
		}
	}
	through := time.Time{}
	for _, r := range newest {
		if r.Status != "success" || r.CurrentGPUSeries <= 0 {
			return time.Time{}, ErrCoverageSourceIncomplete
		}
		at := r.FinishedAt.UTC().Truncate(time.Microsecond)
		if at.IsZero() || at.After(now) {
			return time.Time{}, ErrCoverageSourceIncomplete
		}
		if through.IsZero() || at.Before(through) {
			through = at
		}
	}
	return through, nil
}

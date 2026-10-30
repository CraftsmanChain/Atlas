package gpurisk

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// IngestAssertion is an operator assertion. Complete is nullable so an unsigned
// coverage placeholder cannot accidentally become a statement of completeness.
// No ingestion timestamp is accepted anywhere in this contract.
type IngestAssertion struct {
	EvidenceInput
	Complete *bool `json:"complete"`
}
type IngestReview struct {
	ID                uint       `json:"id"`
	FeedbackRequestID uint       `json:"feedback_request_id"`
	Revision          int        `json:"revision"`
	ReviewSHA256      string     `json:"review_sha256"`
	Decision          string     `json:"decision"`
	TargetScope       string     `json:"target_scope"`
	Onset             *time.Time `json:"onset"`
	A                 time.Time  `json:"a"`
	B                 time.Time  `json:"b"`
	SourceSHA         string     `json:"source_sha"`
}
type IngestQuery struct {
	ID             uint      `json:"id"`
	Cutoff         time.Time `json:"cutoff"`
	ScopeSHA       string    `json:"scope_sha"`
	HorizonMinutes int       `json:"horizon_minutes"`
}
type IngestMapping struct {
	NodeID            uint                                `json:"node_id"`
	Generation        int                                 `json:"generation"`
	EntityRef         string                              `json:"entity_ref"`
	FrozenUUIDPresent bool                                `json:"frozen_uuid_present"`
	CoveringIntervals []api.HistoricalGPUIdentityInterval `json:"covering_intervals"`
	HostSerialAgree   bool                                `json:"host_serial_agree"`
	HistorySHA        string                              `json:"history_sha"`
}
type IngestManifest struct {
	Mode       string            `json:"mode"`
	Version    string            `json:"version"`
	Review     IngestReview      `json:"review"`
	Query      IngestQuery       `json:"query"`
	Mapping    IngestMapping     `json:"mapping"`
	Assertions []IngestAssertion `json:"assertions"`
	Blockers   []string          `json:"blockers"`
	PinSHA     string            `json:"pin_sha"`
}

// IngestPrepare only reads frozen scopes and historical sources. reviewID names
// a review row; the latest revision of that row's feedback request is selected,
// including withdrawals (never resurrect an older confirmed revision).
func IngestPrepare(ctx context.Context, db *gorm.DB, reviewID, queryID uint) (IngestManifest, error) {
	return IngestPrepareMode(ctx, db, reviewID, queryID, false)
}

// IngestPrepareMode uses the same frozen mapping checks in both modes.
func IngestPrepareMode(ctx context.Context, db *gorm.DB, reviewID, queryID uint, identityOnly bool) (IngestManifest, error) {
	var out IngestManifest
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		out, e = ingestPrepare(tx, reviewID, queryID, identityOnly)
		return e
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, err
}
func ingestPrepare(tx *gorm.DB, reviewID, queryID uint, identityOnly bool) (IngestManifest, error) {
	mode := "full"
	if identityOnly {
		mode = "identity-only"
	}
	m := IngestManifest{Mode: mode, Version: "gpu-evidence-ingest-v1", Assertions: []IngestAssertion{}, Blockers: []string{}}
	block := func(s string) { m.Blockers = append(m.Blockers, s) }
	if reviewID == 0 || queryID == 0 {
		return m, ErrInput
	}
	var seed api.HardwareFaultFeedbackReview
	if e := tx.Take(&seed, reviewID).Error; e != nil {
		return m, ErrInput
	}
	var reviews []api.HardwareFaultFeedbackReview
	if e := tx.Where("feedback_request_id = ?", seed.FeedbackRequestID).Order("revision DESC, id DESC").Find(&reviews).Error; e != nil {
		return m, e
	}
	if len(reviews) == 0 {
		return m, ErrInput
	}
	r := reviews[0]
	if len(reviews) > 1 && reviews[1].Revision == r.Revision {
		block("review_revision_ambiguous")
	}
	a, b := reviewWindow(r)
	rsha, _ := digest(r)
	m.Review = IngestReview{r.ID, r.FeedbackRequestID, r.Revision, r.ReviewSHA256, r.Decision, r.TargetScope, r.ConfirmedOnsetAt, a, b, rsha}
	if r.Decision != "confirmed_hardware" || !validSHA(r.ReviewSHA256) {
		block("review_not_confirmed")
	}
	if a.IsZero() || b.IsZero() || b.Before(a) {
		block("window_ambiguous")
	}
	var q api.GPURiskQuery
	if e := tx.Take(&q, queryID).Error; e != nil {
		return m, ErrInput
	}
	m.Query = IngestQuery{q.ID, q.Cutoff, q.ScopeSHA, q.HorizonMinutes}
	if q.Status != "completed" || q.Contract != Contract || q.Target != Target || q.HorizonMinutes != 10080 || !validSHA(q.ScopeSHA) {
		block("query_contract")
	}
	var scopes []api.GPURiskScope
	if e := tx.Where("sha = ?", q.ScopeSHA).Find(&scopes).Error; e != nil {
		return m, e
	}
	var p Snapshot
	if len(scopes) != 1 || json.Unmarshal([]byte(scopes[0].Input), &p) != nil {
		block("scope_missing")
	} else {
		sealed, e := seal(p)
		if e != nil || sealed.SHA != q.ScopeSHA || p.SHA != q.ScopeSHA || !p.Cutoff.Equal(q.Cutoff) || p.Contract != q.Contract || p.Target != q.Target {
			block("scope_digest")
		}
	}
	candidates := []Node{}
	for _, n := range p.Nodes {
		if n.IP == r.ConfirmedNodeIP && r.ConfirmedGPUUUID != "" {
			for _, c := range n.Cards {
				if c.UUID == r.ConfirmedGPUUUID {
					candidates = append(candidates, n)
				}
			}
		}
	}
	if len(candidates) == 0 {
		block("frozen_uuid_missing")
	} else if len(candidates) != 1 {
		block("candidate_ambiguous")
	} else {
		n := candidates[0]
		m.Mapping.NodeID = n.ID
		m.Mapping.Generation = n.Generation
		m.Mapping.FrozenUUIDPresent = true
		var members []api.GPURiskMember
		if e := tx.Where("query_id = ? AND node_id = ? AND generation = ?", q.ID, n.ID, n.Generation).Find(&members).Error; e != nil {
			return m, e
		}
		if len(members) != 1 || n.Generation < 0 {
			block("generation_unproven")
		} else {
			m.Mapping.EntityRef = members[0].EntityRef
			if n.Serial == "" || members[0].EntityRef != "serial:"+n.Serial || members[0].NodeIP != n.IP || members[0].Status != "included" {
				block("member_identity_conflict")
			}
		}
	}
	// Read all matching sources before testing windows: Go time comparisons avoid
	// SQLite's mixed-offset text ordering and bind the entire source set for drift.
	var intervals []api.HistoricalGPUIdentityInterval
	if e := tx.Where("node_ip = ? AND gpu_uuid = ?", r.ConfirmedNodeIP, r.ConfirmedGPUUUID).Order("id").Find(&intervals).Error; e != nil {
		return m, e
	}
	m.Mapping.HistorySHA, _ = digest(intervals)
	m.Mapping.CoveringIntervals = []api.HistoricalGPUIdentityInterval{}
	m.Mapping.HostSerialAgree = true
	for _, v := range intervals {
		if !v.FirstSeenAt.After(a) && !v.LastSeenAt.Before(b) {
			m.Mapping.CoveringIntervals = append(m.Mapping.CoveringIntervals, v)
			if v.HostSerial == "" || m.Mapping.EntityRef != "serial:"+v.HostSerial {
				m.Mapping.HostSerialAgree = false
				block("serial_conflict")
			}
			if v.GPUIndex != r.ConfirmedGPUIndex {
				block("slot_conflict")
			}
		}
	}
	if len(m.Mapping.CoveringIntervals) == 0 {
		m.Mapping.HostSerialAgree = false
		block("covering_interval_missing")
	}
	if r.ConfirmedGPUIndex < 0 {
		block("slot_missing")
	}
	m.PinSHA, _ = digest([]any{m.Version, m.Mode, m.Review, m.Query, m.Mapping})
	if len(m.Blockers) == 0 {
		idx := r.ConfirmedGPUIndex
		base := EvidenceInput{NodeID: m.Mapping.NodeID, Generation: m.Mapping.Generation, EntityRef: m.Mapping.EntityRef}
		id := base
		id.Kind = "identity"
		id.Key = fmt.Sprintf("ingest-identity-%d-%d", q.ID, r.FeedbackRequestID)
		id.From = a
		id.Through = b
		id.FeedbackID = r.FeedbackRequestID
		id.ReviewSHA = r.ReviewSHA256
		id.GPUUUID = r.ConfirmedGPUUUID
		id.GPUIndex = &idx
		id.EvidenceSHA = m.PinSHA
		m.Assertions = append(m.Assertions, IngestAssertion{EvidenceInput: id})
		if !identityOnly {
			for _, s := range []string{"hardware_fault_ledger", "monitoring", "identity"} {
				v := base
				v.Kind = "coverage"
				v.Key = fmt.Sprintf("ingest-coverage-%d-%d-%d-%s", q.ID, base.NodeID, base.Generation, s)
				v.QueryID = q.ID
				v.Source = s
				v.From = q.Cutoff
				v.Through = q.Cutoff.Add(7 * 24 * time.Hour)
				incomplete := false
				m.Assertions = append(m.Assertions, IngestAssertion{EvidenceInput: v, Complete: &incomplete})
			}
		}
		for i := range m.Assertions {
			var latest []api.GPURiskEvidenceRevision
			v := &m.Assertions[i]
			if e := tx.Where("kind = ? AND key = ?", v.Kind, v.Key).Order("revision DESC").Limit(1).Find(&latest).Error; e != nil {
				return m, e
			}
			if len(latest) == 1 {
				v.PreviousSHA = latest[0].SHA
			}
		}
	}
	if identityOnly {
		block("missing_identity_signature")
	} else {
		block("missing_three_source_signatures")
	}
	return m, nil
}

// IngestApply atomically revalidates provenance, then delegates validation,
// timestamps, idempotence and CAS to AppendEvidence. No migration is performed.
func IngestApply(ctx context.Context, db *gorm.DB, approved IngestManifest) ([]api.GPURiskEvidenceRevision, error) {
	if approved.Mode == "" {
		approved.Mode = "full"
	}
	if approved.Mode != "full" && approved.Mode != "identity-only" {
		return nil, ErrInput
	}
	out := []api.GPURiskEvidenceRevision{}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fresh, e := ingestPrepare(tx, approved.Review.ID, approved.Query.ID, approved.Mode == "identity-only")
		if e != nil {
			return e
		}
		pin, _ := digest([]any{approved.Version, approved.Mode, approved.Review, approved.Query, approved.Mapping})
		if pin != approved.PinSHA || pin != fresh.PinSHA {
			return ErrFence
		}
		if approved.Mode == "identity-only" {
			if fresh.Mode != "identity-only" || len(fresh.Blockers) != 1 || fresh.Blockers[0] != "missing_identity_signature" || len(approved.Assertions) != 1 {
				return ErrInput
			}
			for _, b := range approved.Blockers {
				if b != "missing_identity_signature" {
					return ErrInput
				}
			}
			a := approved.Assertions[0]
			if a.Kind != "identity" || a.Complete != nil || strings.TrimSpace(a.Owner) == "" || a.EvidenceSHA != approved.PinSHA {
				return ErrInput
			}
		} else {
			if len(fresh.Blockers) != 1 || fresh.Blockers[0] != "missing_three_source_signatures" || len(approved.Assertions) != 4 {
				return ErrInput
			}
			for _, b := range approved.Blockers {
				if b != "missing_three_source_signatures" {
					return ErrInput
				}
			}
		}
		for i, a := range approved.Assertions {
			want := fresh.Assertions[i].EvidenceInput
			got := a.EvidenceInput
			if strings.TrimSpace(got.Owner) == "" || !validSHA(got.EvidenceSHA) {
				return ErrInput
			}
			if i > 0 && a.Complete == nil {
				return ErrInput
			}
			if i == 0 && (a.Complete != nil || got.EvidenceSHA != approved.PinSHA) {
				return ErrInput
			}
			// Only signer fields, explicit completeness and CAS are operator editable.
			normalized := got
			normalized.Owner = want.Owner
			normalized.EvidenceSHA = want.EvidenceSHA
			normalized.PreviousSHA = want.PreviousSHA
			x, _ := digest(normalized)
			y, _ := digest(want)
			if x != y {
				return ErrInput
			}
			if a.Complete != nil {
				got.Complete = *a.Complete
			}
			got.From = got.From.UTC().Truncate(time.Microsecond)
			got.Through = got.Through.UTC().Truncate(time.Microsecond)
			// A fresh prepare pins the current head. Compare semantic content
			// without its CAS token so re-preparing does not manufacture a revision.
			// Exact original-package replay also remains safe; any other stale
			// token is left to AppendEvidence's existing fence.
			var heads []api.GPURiskEvidenceRevision
			if e := tx.Where("kind = ? AND key = ?", got.Kind, got.Key).Order("revision DESC").Limit(1).Find(&heads).Error; e != nil {
				return e
			}
			if len(heads) == 1 {
				var prior EvidenceInput
				if json.Unmarshal([]byte(heads[0].Data), &prior) != nil {
					return ErrInput
				}
				left, right := got, prior
				left.PreviousSHA = ""
				right.PreviousSHA = ""
				l, _ := digest(left)
				r, _ := digest(right)
				if l == r && (got.PreviousSHA == heads[0].SHA || got.PreviousSHA == prior.PreviousSHA) {
					got = prior
				}
			}
			row, e := AppendEvidence(ctx, tx, got)
			if e != nil {
				return e
			}
			out = append(out, row)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	return out, nil
}

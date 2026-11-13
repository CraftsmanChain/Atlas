package gpurisk

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

func ingestFixture(t *testing.T) (*gorm.DB, api.GPURiskQuery, api.HardwareFaultFeedbackReview, Snapshot) {
	t.Helper()
	db, q, p := outcomeDB(t)
	if e := db.AutoMigrate(&api.HistoricalGPUIdentityInterval{}, &api.GPURiskSourceCoverageCheck{}); e != nil {
		t.Fatal(e)
	}
	p.Nodes[0].Cards = []Card{{AssetID: 1, UUID: "GPU-ingest", State: "active"}}
	q, p = ingestScope(t, db, q, p)
	_, r := seedReview(t, db, q, "ingest", time.Hour*24)
	r.ConfirmedNodeIP = p.Nodes[0].IP
	r.ConfirmedGPUUUID = "GPU-ingest"
	r.ConfirmedGPUIndex = 0
	if e := db.Save(&r).Error; e != nil {
		t.Fatal(e)
	}
	h := api.HistoricalGPUIdentityInterval{IntervalKey: "ingest", SourceKey: "history", NodeIP: r.ConfirmedNodeIP, HostSerial: p.Nodes[0].Serial, GPUUUID: r.ConfirmedGPUUUID, GPUIndex: 0, FirstSeenAt: q.Cutoff.Add(-time.Hour), LastSeenAt: q.Cutoff.Add(7 * 24 * time.Hour)}
	if e := db.Create(&h).Error; e != nil {
		t.Fatal(e)
	}
	return db, q, r, p
}
func ingestScope(t *testing.T, db *gorm.DB, q api.GPURiskQuery, p Snapshot) (api.GPURiskQuery, Snapshot) {
	t.Helper()
	p = reseal(t, p)
	data, _ := json.Marshal(p)
	if e := db.Model(&api.GPURiskScope{}).Where("sha = ?", q.ScopeSHA).Updates(map[string]any{"sha": p.SHA, "input": string(data)}).Error; e != nil {
		t.Fatal(e)
	}
	q.ScopeSHA = p.SHA
	if e := db.Save(&q).Error; e != nil {
		t.Fatal(e)
	}
	return q, p
}
func prepared(t *testing.T, db *gorm.DB, q api.GPURiskQuery, r api.HardwareFaultFeedbackReview, identityOnly ...bool) IngestManifest {
	t.Helper()
	m, e := IngestPrepareMode(t.Context(), db, r.ID, q.ID, len(identityOnly) > 0 && identityOnly[0])
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func signIngest(t *testing.T, m IngestManifest) IngestManifest {
	t.Helper()
	want := 4
	if m.Mode == "identity-only" {
		want = 1
	}
	if len(m.Assertions) != want {
		t.Fatal(m.Blockers)
	}
	for i := range m.Assertions {
		m.Assertions[i].Owner = fmt.Sprintf("responsible-%d", i)
		if i > 0 {
			v := false
			m.Assertions[i].Complete = &v
			m.Assertions[i].EvidenceSHA = strings.Repeat("a", 64)
		}
	}
	return m
}
func TestIngestMappingFailClosed(t *testing.T) {
	cases := []struct {
		name, block string
		change      func(*testing.T, *gorm.DB, *api.GPURiskQuery, *api.HardwareFaultFeedbackReview, *Snapshot)
	}{
		{"missing UUID", "frozen_uuid_missing", func(t *testing.T, d *gorm.DB, q *api.GPURiskQuery, r *api.HardwareFaultFeedbackReview, p *Snapshot) {
			p.Nodes[0].Cards = nil
			*q, *p = ingestScope(t, d, *q, *p)
		}},
		{"multiple candidates", "candidate_ambiguous", func(t *testing.T, d *gorm.DB, q *api.GPURiskQuery, r *api.HardwareFaultFeedbackReview, p *Snapshot) {
			p.Nodes[1].IP = p.Nodes[0].IP
			p.Nodes[1].Cards = p.Nodes[0].Cards
			*q, *p = ingestScope(t, d, *q, *p)
		}},
		{"serial", "serial_conflict", func(t *testing.T, d *gorm.DB, q *api.GPURiskQuery, r *api.HardwareFaultFeedbackReview, p *Snapshot) {
			d.Model(&api.HistoricalGPUIdentityInterval{}).Where("id > 0").Update("host_serial", "wrong")
		}},
		{"slot", "slot_conflict", func(t *testing.T, d *gorm.DB, q *api.GPURiskQuery, r *api.HardwareFaultFeedbackReview, p *Snapshot) {
			d.Model(&api.HistoricalGPUIdentityInterval{}).Where("id > 0").Update("gpu_index", 1)
		}},
		{"generation", "generation_unproven", func(t *testing.T, d *gorm.DB, q *api.GPURiskQuery, r *api.HardwareFaultFeedbackReview, p *Snapshot) {
			p.Nodes[0].Generation++
			*q, *p = ingestScope(t, d, *q, *p)
		}},
		{"no interval", "covering_interval_missing", func(t *testing.T, d *gorm.DB, q *api.GPURiskQuery, r *api.HardwareFaultFeedbackReview, p *Snapshot) {
			d.Where("id > 0").Delete(&api.HistoricalGPUIdentityInterval{})
		}},
		{"ambiguous window", "window_ambiguous", func(t *testing.T, d *gorm.DB, q *api.GPURiskQuery, r *api.HardwareFaultFeedbackReview, p *Snapshot) {
			d.Model(r).Update("confirmed_onset_at", nil)
		}},
	}
	for _, identityOnly := range []bool{false, true} {
		for _, c := range cases {
			t.Run(fmt.Sprintf("identityOnly=%t/%s", identityOnly, c.name), func(t *testing.T) {
				db, q, r, p := ingestFixture(t)
				c.change(t, db, &q, &r, &p)
				m := prepared(t, db, q, r, identityOnly)
				if !contains(m.Blockers, c.block) {
					t.Fatal(m.Blockers)
				}
				if _, e := IngestApply(t.Context(), db, m); e == nil {
					t.Fatal("accepted blocked mapping")
				}
				var n int64
				db.Model(&api.GPURiskEvidenceRevision{}).Count(&n)
				if n != 0 {
					t.Fatal(n)
				}
			})
		}
	}
}
func TestIngestLateAtomicIdempotentAndOldAsOf(t *testing.T) {
	db, q, r, _ := ingestFixture(t)
	old := time.Now().UTC().Truncate(time.Microsecond)
	before, _, e := EvaluateOutcomes(t.Context(), db, q.ID, old)
	if e != nil {
		t.Fatal(e)
	}
	m := prepared(t, db, q, r)
	if _, e := IngestApply(t.Context(), db, m); !errors.Is(e, ErrInput) {
		t.Fatal(e)
	}
	_, rows := evaluate(t, db, q)
	if rows[0].Positive != nil {
		t.Fatal("unsigned package produced a label", rows)
	}
	m = signIngest(t, m)
	start := time.Now().UTC().Truncate(time.Microsecond)
	result, e := IngestApply(t.Context(), db, m)
	if e != nil {
		t.Fatal(e)
	}
	again, e := IngestApply(t.Context(), db, m)
	if e != nil {
		t.Fatal(e)
	}
	fresh := signIngest(t, prepared(t, db, q, r))
	if same, e := IngestApply(t.Context(), db, fresh); e != nil || same[0].ID != result[0].ID {
		t.Fatal("reprepare duplicated", same, e)
	}
	for i, v := range result {
		if v.ID != again[i].ID || v.AvailableAt.Before(start) || v.AvailableAt.Before(q.Cutoff) {
			t.Fatal(v, again[i])
		}
	}
	after, _, e := EvaluateOutcomes(t.Context(), db, q.ID, old)
	if e != nil || before.SHA != after.SHA {
		t.Fatal("old as_of drift", e)
	}
	var evs []api.GPURiskEvidenceRevision
	db.Find(&evs)
	var members []api.GPURiskMember
	db.Where("query_id = ?", q.ID).Order("node_id").Find(&members)
	features, e := historyAt(t.Context(), members[0], q, evs, []api.HardwareFaultFeedbackReview{r})
	if e != nil || len(features.EvidenceSHAs) != 0 {
		t.Fatal("future ingestion leaked into old features", features, e)
	}
	// A partially attempted package must roll back the earlier identity append.
	bad := prepared(t, db, q, r)
	bad = signIngest(t, bad)
	bad.Assertions[0].Owner = "new identity signer"
	bad.Assertions[2].PreviousSHA = strings.Repeat("f", 64)
	if _, e := IngestApply(t.Context(), db, bad); !errors.Is(e, ErrFence) {
		t.Fatal(e)
	}
	var count int64
	db.Model(&api.GPURiskEvidenceRevision{}).Count(&count)
	if count != 4 {
		t.Fatal("partial package persisted", count)
	}
}
func TestIngestPinnedDriftAndReviewRetry(t *testing.T) {
	for _, change := range []string{"review", "history", "member", "scope", "assertion"} {
		t.Run(change, func(t *testing.T) {
			db, q, r, _ := ingestFixture(t)
			m := signIngest(t, prepared(t, db, q, r))
			switch change {
			case "review":
				r.ID = 0
				r.Revision++
				r.ReviewSHA256 = strings.Repeat("e", 64)
				if e := db.Create(&r).Error; e != nil {
					t.Fatal(e)
				}
			case "history":
				db.Model(&api.HistoricalGPUIdentityInterval{}).Where("id > 0").Update("observation_count", 99)
			case "member":
				db.Model(&api.GPURiskMember{}).Where("query_id = ? AND node_id = ?", q.ID, 1).Update("entity_ref", "serial:drift")
			case "scope":
				db.Model(&q).Update("scope_sha", strings.Repeat("d", 64))
			case "assertion":
				m.Assertions[0].Generation++
			}
			if _, e := IngestApply(t.Context(), db, m); !errors.Is(e, ErrFence) && !errors.Is(e, ErrInput) {
				t.Fatal(e)
			}
			if change == "review" {
				fresh := signIngest(t, prepared(t, db, q, r))
				if fresh.Review.Revision != 2 {
					t.Fatal(fresh)
				}
				if _, e := IngestApply(t.Context(), db, fresh); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestIngestPrepareReadOnlyConnection(t *testing.T) {
	db, q, r, _ := ingestFixture(t)
	guard := "PRAGMA query_only = ON"
	off := "PRAGMA query_only = OFF"
	if db.Dialector.Name() == "postgres" {
		guard = "SET default_transaction_read_only = on"
		off = "SET default_transaction_read_only = off"
	}
	if e := db.Exec(guard).Error; e != nil {
		t.Fatal(e)
	}
	defer db.Exec(off)
	m := prepared(t, db, q, r)
	if len(m.Assertions) != 4 {
		t.Fatal(m.Blockers)
	}
	if e := db.Exec("UPDATE gpu_risk_queries SET status = 'changed' WHERE id = ?", q.ID).Error; e == nil {
		t.Fatal("read-only connection permitted mutation")
	}
	if _, e := IngestApply(t.Context(), db, signIngest(t, m)); e == nil {
		t.Fatal("read-only connection accepted apply")
	}
}

func TestIngestIdentityOnlyLifecycle(t *testing.T) {
	db, q, r, _ := ingestFixture(t)
	old := time.Now().UTC().Truncate(time.Microsecond)
	before, _, e := EvaluateOutcomes(t.Context(), db, q.ID, old)
	if e != nil {
		t.Fatal(e)
	}
	m := prepared(t, db, q, r, true)
	if m.Mode != "identity-only" || len(m.Assertions) != 1 || m.Assertions[0].Kind != "identity" || len(m.Blockers) != 1 || m.Blockers[0] != "missing_identity_signature" {
		t.Fatal(m)
	}
	full, e := IngestPrepare(t.Context(), db, r.ID, q.ID)
	if e != nil || full.Mode != "full" || len(full.Assertions) != 4 || full.PinSHA == m.PinSHA {
		t.Fatal(full, e)
	}
	for _, field := range []string{"owner", "sha", "complete", "kind", "blocker", "mode", "unknown mode", "assertion", "cas"} {
		t.Run(field, func(t *testing.T) {
			bad := signIngest(t, prepared(t, db, q, r, true))
			switch field {
			case "owner":
				bad.Assertions[0].Owner = " "
			case "sha":
				bad.Assertions[0].EvidenceSHA = strings.Repeat("f", 64)
			case "complete":
				v := false
				bad.Assertions[0].Complete = &v
			case "kind":
				bad.Assertions[0].Kind = "coverage"
			case "blocker":
				bad.Blockers = []string{"missing_three_source_signatures"}
			case "mode":
				bad.Mode = "full"
			case "unknown mode":
				bad.Mode = "other"
			case "assertion":
				bad.Assertions[0].Generation++
			case "cas":
				bad.Assertions[0].PreviousSHA = strings.Repeat("f", 64)
			}
			if _, e := IngestApply(t.Context(), db, bad); e == nil {
				t.Fatal("accepted invalid identity-only package")
			}
		})
	}
	var count int64
	if e := db.Model(&api.GPURiskEvidenceRevision{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatal(count, e)
	}
	m = signIngest(t, m)
	start := time.Now().UTC().Truncate(time.Microsecond)
	rows, e := IngestApply(t.Context(), db, m)
	if e != nil || len(rows) != 1 || rows[0].Kind != "identity" || rows[0].AvailableAt.Before(start) {
		t.Fatal(rows, e)
	}
	for _, retry := range []IngestManifest{m, signIngest(t, prepared(t, db, q, r, true))} {
		same, e := IngestApply(t.Context(), db, retry)
		if e != nil || len(same) != 1 || same[0].ID != rows[0].ID {
			t.Fatal(same, e)
		}
	}
	stale := m
	stale.Assertions = append([]IngestAssertion(nil), m.Assertions...)
	stale.Assertions[0].Owner = "changed signer"
	if _, e := IngestApply(t.Context(), db, stale); !errors.Is(e, ErrFence) {
		t.Fatal(e)
	}
	if e := db.Model(&api.GPURiskEvidenceRevision{}).Count(&count).Error; e != nil || count != 1 {
		t.Fatal(count, e)
	}
	after, _, e := EvaluateOutcomes(t.Context(), db, q.ID, old)
	if e != nil || before.SHA != after.SHA {
		t.Fatal("old as_of drift", before.SHA, after.SHA, e)
	}
	var evs []api.GPURiskEvidenceRevision
	if e := db.Find(&evs).Error; e != nil {
		t.Fatal(e)
	}
	var member api.GPURiskMember
	if e := db.Where("query_id = ? AND node_id = ?", q.ID, m.Mapping.NodeID).Take(&member).Error; e != nil {
		t.Fatal(e)
	}
	h, e := historyAt(t.Context(), member, q, evs, []api.HardwareFaultFeedbackReview{r})
	if e != nil || len(h.EvidenceSHAs) != 0 {
		t.Fatal("old H(q) polluted", h, e)
	}
}

func TestIngestIdentityOnlyFutureThroughRejected(t *testing.T) {
	db, q, r, _ := ingestFixture(t)
	// OBSERVED: a source-confirmed future window is rejected by AppendEvidence;
	// the manifest's Through is never edited to evade the ingestion clock.
	future := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	if e := db.Model(&r).Update("confirmed_onset_at", future).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&api.HistoricalGPUIdentityInterval{}).Where("id > 0").Update("last_seen_at", future).Error; e != nil {
		t.Fatal(e)
	}
	m := signIngest(t, prepared(t, db, q, r, true))
	if !m.Assertions[0].Through.After(time.Now()) {
		t.Fatal(m)
	}
	if _, e := IngestApply(t.Context(), db, m); !errors.Is(e, ErrInput) {
		t.Fatal(e)
	}
	var count int64
	if e := db.Model(&api.GPURiskEvidenceRevision{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatal(count, e)
	}
}

func TestIngestIdentityOnlyNewQueryAndReadOnly(t *testing.T) {
	db, q, r, p := ingestFixture(t)
	// DERIVED: an already confirmed fault can be attributed for a new query
	// while full coverage still extends beyond real ingestion time.
	p.Cutoff = time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	q.Cutoff = p.Cutoff
	q, _ = ingestScope(t, db, q, p)
	full := signIngest(t, prepared(t, db, q, r))
	if _, e := IngestApply(t.Context(), db, full); !errors.Is(e, ErrInput) {
		t.Fatal(e)
	}
	guard, off := "PRAGMA query_only = ON", "PRAGMA query_only = OFF"
	if db.Dialector.Name() == "postgres" {
		guard, off = "SET default_transaction_read_only = on", "SET default_transaction_read_only = off"
	}
	defer db.Exec(off)
	if e := db.Exec(guard).Error; e != nil {
		t.Fatal(e)
	}
	m := signIngest(t, prepared(t, db, q, r, true))
	if _, e := IngestApply(t.Context(), db, m); e == nil {
		t.Fatal("read-only apply accepted")
	}
	if e := db.Exec(off).Error; e != nil {
		t.Fatal(e)
	}
	if rows, e := IngestApply(t.Context(), db, m); e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
}

func TestIngestIdentityOnlyPinnedDrift(t *testing.T) {
	for _, change := range []string{"review", "history", "member", "scope"} {
		t.Run(change, func(t *testing.T) {
			db, q, r, _ := ingestFixture(t)
			m := signIngest(t, prepared(t, db, q, r, true))
			var e error
			switch change {
			case "review":
				e = db.Model(&r).Update("review_sha256", strings.Repeat("e", 64)).Error
			case "history":
				e = db.Model(&api.HistoricalGPUIdentityInterval{}).Where("id > 0").Update("observation_count", 99).Error
			case "member":
				e = db.Model(&api.GPURiskMember{}).Where("query_id = ? AND node_id = ?", q.ID, m.Mapping.NodeID).Update("entity_ref", "serial:drift").Error
			case "scope":
				e = db.Model(&q).Update("scope_sha", strings.Repeat("d", 64)).Error
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e := IngestApply(t.Context(), db, m); !errors.Is(e, ErrFence) {
				t.Fatal(e)
			}
		})
	}
}

func TestIngestEmptyModeMeansFull(t *testing.T) {
	db, q, r, _ := ingestFixture(t)
	m := signIngest(t, prepared(t, db, q, r))
	m.Mode = ""
	if rows, e := IngestApply(t.Context(), db, m); e != nil || len(rows) != 4 {
		t.Fatal(rows, e)
	}
}

// signFullWithLedgerComplete signs the four-assertion full package, marking only
// the hardware_fault_ledger coverage (index 1) complete. Complete is not in the
// pinned digest, so the signer still states it explicitly.
func signFullWithLedgerComplete(t *testing.T, m IngestManifest) IngestManifest {
	t.Helper()
	if len(m.Assertions) != 4 {
		t.Fatal(m.Blockers)
	}
	for i := range m.Assertions {
		m.Assertions[i].Owner = fmt.Sprintf("responsible-%d", i)
		if i > 0 {
			m.Assertions[i].EvidenceSHA = strings.Repeat("a", 64)
			v := i == 1
			m.Assertions[i].Complete = &v
		}
	}
	return m
}

func TestIngestCoverageFromSourceCheck(t *testing.T) {
	db, q, r, _ := ingestFixture(t)
	// No check: byte-for-byte the previous fail-closed placeholder.
	ledger := prepared(t, db, q, r).Assertions[1]
	if ledger.Source != "hardware_fault_ledger" || ledger.Complete == nil || *ledger.Complete || !ledger.Through.Equal(q.Cutoff.Add(7*24*time.Hour)) || !ledger.From.Equal(q.Cutoff) {
		t.Fatal(ledger)
	}
	check, e := RecordSourceCoverage(t.Context(), db, CoverageCheckInput{Source: "hardware_fault_ledger", Owner: "platform-admin"})
	if e != nil {
		t.Fatal(e)
	}
	m := prepared(t, db, q, r)
	ledger = m.Assertions[1]
	if ledger.Complete == nil || !*ledger.Complete || !ledger.Through.Equal(check.VerifiedThrough) || !ledger.From.Equal(q.Cutoff) {
		t.Fatal(ledger)
	}
	// Untouched sources stay fail-closed.
	for _, i := range []int{2, 3} {
		if m.Assertions[i].Complete == nil || *m.Assertions[i].Complete {
			t.Fatal(m.Assertions[i])
		}
	}
	rows, e := IngestApply(t.Context(), db, signFullWithLedgerComplete(t, m))
	if e != nil || len(rows) != 4 {
		t.Fatal(rows, e)
	}
	// The honest assertion clears the structural gate for a later query whose
	// cutoff is after its server AvailableAt, and only that gate.
	var evs []api.GPURiskEvidenceRevision
	if e := db.Order("id").Find(&evs).Error; e != nil {
		t.Fatal(e)
	}
	var member api.GPURiskMember
	if e := db.Where("query_id = ? AND node_id = ?", q.ID, m.Mapping.NodeID).Take(&member).Error; e != nil {
		t.Fatal(e)
	}
	later := q
	later.Cutoff = check.VerifiedThrough.Add(time.Hour)
	f, e := historyAt(t.Context(), member, later, evs, []api.HardwareFaultFeedbackReview{r})
	if e != nil || f.HistoryUnknown || contains(f.Blockers, "history_coverage_unknown") {
		t.Fatal("structural gate not cleared", f, e)
	}
}

func TestIngestCoverageCheckBoundaryAndExpiry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		verified   time.Duration
		observedAt time.Duration
		accept     bool
	}{
		{"tail_equal_accepted", -MaxSourceLag, 0, true},
		{"tail_minus_1us_rejected", -MaxSourceLag - time.Microsecond, 0, false},
		{"future_observed_rejected", 0, time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, q, r, _ := ingestFixture(t)
			row := api.GPURiskSourceCoverageCheck{
				Source:          "hardware_fault_ledger",
				VerifiedThrough: q.Cutoff.Add(tc.verified).UTC().Truncate(time.Microsecond),
				ObservedAt:      time.Now().UTC().Add(tc.observedAt).Truncate(time.Microsecond),
				CheckSHA256:     strings.Repeat("c", 64),
				Owner:           "platform-admin",
				Contract:        CoverageWatermarkContract,
			}
			if e := db.Create(&row).Error; e != nil {
				t.Fatal(e)
			}
			ledger := prepared(t, db, q, r).Assertions[1]
			fallback := q.Cutoff.Add(7 * 24 * time.Hour)
			if tc.accept {
				if ledger.Complete == nil || !*ledger.Complete || !ledger.Through.Equal(row.VerifiedThrough) || ledger.From.After(ledger.Through) {
					t.Fatal(ledger)
				}
				return
			}
			if ledger.Complete == nil || *ledger.Complete || !ledger.Through.Equal(fallback) {
				t.Fatal("not fail-closed", ledger)
			}
		})
	}
}

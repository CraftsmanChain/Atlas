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
	if e := db.AutoMigrate(&api.HistoricalGPUIdentityInterval{}); e != nil {
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
func prepared(t *testing.T, db *gorm.DB, q api.GPURiskQuery, r api.HardwareFaultFeedbackReview) IngestManifest {
	t.Helper()
	m, e := IngestPrepare(t.Context(), db, r.ID, q.ID)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func signIngest(t *testing.T, m IngestManifest) IngestManifest {
	t.Helper()
	if len(m.Assertions) != 4 {
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
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, q, r, p := ingestFixture(t)
			c.change(t, db, &q, &r, &p)
			m := prepared(t, db, q, r)
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

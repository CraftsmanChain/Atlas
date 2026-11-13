package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"atlas/internal/gpurisk"
	"atlas/pkg/api"
	"atlas/pkg/storage"
)

// P1 round 2: ledger-declare mints the anchor query's ledger member coverage
// in the same command; without an anchor it still succeeds.
func TestLedgerDeclareCLIMintsAnchorCoverage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger-anchor.db")
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	store, e := storage.InitDB(path)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := store.DB.DB()
	defer raw.Close()
	old := time.Now().UTC().Add(-2 * time.Hour)
	if e := store.Create(&api.GPUNode{NodeIP: "ANCHOR-NODE", AssetSerial: "ANCHOR-SN", Lifecycle: "active", CreatedAt: old, UpdatedAt: old}).Error; e != nil {
		t.Fatal(e)
	}
	// No query yet: declaration recorded, no anchor, success.
	if e := run([]string{"ledger-declare", "-driver", "sqlite", "-through", time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), "-owner", "operator"}); e != nil {
		t.Fatal("declaration without anchor failed", e)
	}
	p, e := gpurisk.Capture(t.Context(), store.DB)
	if e != nil {
		t.Fatal(e)
	}
	q, e := gpurisk.Record(t.Context(), store.DB, p)
	if e != nil {
		t.Fatal(e)
	}
	if q.Status != "completed" {
		t.Fatal("query status", q.Status)
	}
	// Synthetic fixture: a bare captured node is scope-unconfirmed; mark its
	// member included so the query has a coverable host.
	if r := store.DB.Model(&api.GPURiskMember{}).Where("query_id = ?", q.ID).Updates(map[string]any{"status": "included", "entity_ref": "serial:ANCHOR-SN"}); r.Error != nil || r.RowsAffected != 1 {
		t.Fatal("fixture member", r.Error, r.RowsAffected)
	}
	time.Sleep(5 * time.Millisecond)
	through := time.Now().UTC().Truncate(time.Microsecond)
	if !through.After(q.Cutoff) {
		t.Fatal("clock")
	}
	if e := run([]string{"ledger-declare", "-driver", "sqlite", "-through", through.Format(time.RFC3339Nano), "-owner", "operator"}); e != nil {
		t.Fatal(e)
	}
	var revs []api.GPURiskEvidenceRevision
	store.DB.Where("kind = ?", "coverage").Find(&revs)
	if len(revs) != 1 {
		t.Fatal("anchor coverage not minted", len(revs))
	}
	var v gpurisk.EvidenceInput
	if e := json.Unmarshal([]byte(revs[0].Data), &v); e != nil {
		t.Fatal(e)
	}
	if v.QueryID != q.ID || v.Source != "hardware_fault_ledger" || !v.Complete || !v.From.Equal(q.Cutoff) || !v.Through.Equal(through) || v.Owner != gpurisk.CoverageOwner {
		t.Fatalf("unexpected anchor coverage: %+v", v)
	}
	if _, _, minted, e := gpurisk.MintLedgerAnchorCoverage(t.Context(), store.DB); e != nil || minted != 0 {
		t.Fatal("re-mint not idempotent", minted, e)
	}
}

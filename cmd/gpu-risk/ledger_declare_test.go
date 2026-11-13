package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/internal/gpurisk"
	"atlas/pkg/api"
	"atlas/pkg/storage"
)

// P1 taskcard §4 item 2 (CLI side).
func TestLedgerDeclareCLIAndDeprecatedLedgerCoverageCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger-declare.db")
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	store, e := storage.InitDB(path)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := store.DB.DB()
	defer raw.Close()
	count := func() int64 {
		var n int64
		store.DB.Model(&api.GPURiskSourceCoverageCheck{}).Count(&n)
		return n
	}
	// coverage-check for the ledger is deprecated and refused before any write.
	if e := run([]string{"coverage-check", "-driver", "sqlite", "-source", "hardware_fault_ledger", "-owner", "ops"}); e == nil || !strings.Contains(e.Error(), "deprecated") || !strings.Contains(e.Error(), "ledger-declare") {
		t.Fatal("deprecated ledger coverage-check accepted", e)
	}
	now := time.Now().UTC()
	through := now.Add(-time.Hour).Format(time.RFC3339Nano)
	for _, args := range [][]string{
		{"ledger-declare", "-driver", "sqlite"},
		{"ledger-declare", "-driver", "sqlite", "-through", through},
		{"ledger-declare", "-driver", "sqlite", "-owner", "operator"},
		{"ledger-declare", "-driver", "sqlite", "-through", through, "-owner", "   "},
		{"ledger-declare", "-driver", "sqlite", "-through", "not-a-time", "-owner", "operator"},
		{"ledger-declare", "-driver", "sqlite", "-through", now.Add(time.Hour).Format(time.RFC3339Nano), "-owner", "operator"},
		{"ledger-declare", "-driver", "sqlite", "-through", through, "-owner", "operator", "-query", "1"},
		{"coverage-members", "-driver", "sqlite", "-query", "1", "-source", "monitoring", "-owner", "ops", "-through", through},
	} {
		if e := run(args); e == nil {
			t.Fatal("invalid ledger-declare accepted", args)
		}
	}
	if count() != 0 {
		t.Fatal("rejected declaration wrote a row")
	}
	if e := run([]string{"ledger-declare", "-driver", "sqlite", "-through", through, "-owner", "operator"}); e != nil {
		t.Fatal(e)
	}
	var row api.GPURiskSourceCoverageCheck
	if e := store.DB.Order("id DESC").First(&row).Error; e != nil {
		t.Fatal(e)
	}
	if row.Source != "hardware_fault_ledger" || row.Contract != gpurisk.LedgerDeclaredContract || row.Owner != "operator" || row.RecordCount != 0 || row.VerifiedThrough.Format(time.RFC3339Nano) != now.Add(-time.Hour).Truncate(time.Microsecond).Format(time.RFC3339Nano) {
		t.Fatalf("unexpected declaration row: %+v", row)
	}
	regress := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	if e := run([]string{"ledger-declare", "-driver", "sqlite", "-through", regress, "-owner", "operator"}); e == nil || !strings.Contains(e.Error(), "regresses") {
		t.Fatal("regressing declaration accepted", e)
	}
	if count() != 1 {
		t.Fatal("unexpected rows", count())
	}
	// identity coverage-check uses the backfill watermark: none -> refused.
	if e := run([]string{"coverage-check", "-driver", "sqlite", "-source", "identity", "-owner", "ops"}); e == nil || !strings.Contains(e.Error(), "no complete canonical durable record") {
		t.Fatal("identity without completed backfill accepted", e)
	}
	if count() != 1 {
		t.Fatal("refused identity check wrote a row")
	}
}

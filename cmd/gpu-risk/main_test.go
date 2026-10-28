package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"atlas/internal/gpurisk"
	"atlas/pkg/api"
	"atlas/pkg/storage"
	"fmt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPlanReadOnlyAndExplicitRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local.db")
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	if run([]string{"plan", "-driver", "sqlite"}) == nil {
		t.Fatal("created absent plan DB")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("plan created database")
	}
	db, e := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	if e := db.AutoMigrate(&api.InfrastructureAsset{}, &api.GPUNode{}, &api.GPUAsset{}, &api.GPUHealthScore{}, &api.HealthEvaluationRun{}); e != nil {
		t.Fatal(e)
	}
	old := time.Now().UTC().Add(-time.Hour)
	if e := db.Create(&api.GPUNode{NodeIP: "local", AssetSerial: "SN", Lifecycle: "active", CreatedAt: old, UpdatedAt: old}).Error; e != nil {
		t.Fatal(e)
	}
	if e := run([]string{"-driver", "sqlite"}); e != nil {
		t.Fatal(e)
	}
	if db.Migrator().HasTable(&api.GPURiskQuery{}) {
		t.Fatal("plan migrated database")
	}
	if e := run([]string{"record", "-driver", "sqlite"}); e == nil {
		t.Fatal("record accepted missing plan")
	}
	p, e := gpurisk.Capture(t.Context(), db)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(p)
	input := filepath.Join(t.TempDir(), "plan.json")
	if e := os.WriteFile(input, data, 0600); e != nil {
		t.Fatal(e)
	}
	if e := run([]string{"record", "-driver", "sqlite", "-input", input}); e != nil {
		t.Fatal(e)
	}
	if e := run([]string{"record", "-driver", "sqlite", "-input", input}); e != nil {
		t.Fatal(e)
	}
	var count int64
	db.Model(&api.GPURiskQuery{}).Count(&count)
	if count != 1 {
		t.Fatal("CLI retry duplicated")
	}
	raw, _ := db.DB()
	raw.Close()
}

func TestExplicitEvidenceAndOutcomeCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli-outcomes.db")
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	store, e := storage.InitDB(path)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := store.DB.DB()
	defer raw.Close()
	old := time.Now().UTC().Add(-time.Hour)
	if e := store.Create(&api.GPUNode{NodeIP: "CLI-NODE", AssetSerial: "CLI-SN", Lifecycle: "active", CreatedAt: old, UpdatedAt: old}).Error; e != nil {
		t.Fatal(e)
	}
	p, e := gpurisk.Capture(t.Context(), store.DB)
	if e != nil {
		t.Fatal(e)
	}
	q, e := gpurisk.Record(t.Context(), store.DB, p)
	if e != nil {
		t.Fatal(e)
	}
	v := gpurisk.EvidenceInput{Kind: "coverage", Key: "cli-coverage", Owner: "operator", EvidenceSHA: fmt.Sprintf("%064d", 1), NodeID: p.Nodes[0].ID, EntityRef: "serial:CLI-SN", QueryID: q.ID, From: q.Cutoff.Add(-time.Hour), Through: q.Cutoff, Source: "monitoring", Complete: false}
	data, _ := json.Marshal(v)
	input := filepath.Join(t.TempDir(), "evidence.json")
	os.WriteFile(input, data, 0600)
	if e := run([]string{"evidence", "-driver", "sqlite", "-input", input}); e != nil {
		t.Fatal(e)
	}
	asOf := time.Now().UTC().Format(time.RFC3339Nano)
	args := []string{"-driver", "sqlite", "-query", fmt.Sprint(q.ID), "-as-of", asOf}
	if e := run(append([]string{"outcome-plan"}, args...)); e != nil {
		t.Fatal(e)
	}
	var count int64
	store.Model(&api.GPURiskOutcomeBatch{}).Count(&count)
	if count != 0 {
		t.Fatal("dry run wrote outcome")
	}
	for i := 0; i < 2; i++ {
		if e := run(append([]string{"outcomes"}, args...)); e != nil {
			t.Fatal(e)
		}
	}
	store.Model(&api.GPURiskOutcomeBatch{}).Count(&count)
	if count != 1 {
		t.Fatal("CLI retry duplicated outcome")
	}
}

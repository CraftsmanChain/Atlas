package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"atlas/internal/gpurisk"
	"atlas/pkg/api"
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

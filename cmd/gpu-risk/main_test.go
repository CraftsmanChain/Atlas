package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

	// The same controlled command accepts versioned historical host context.
	host := gpurisk.EvidenceInput{Kind: "host_context", Key: "cli-host", Owner: "operator", SourceVersion: gpurisk.HistorySourceVersion, EvidenceSHA: fmt.Sprintf("%064d", 2), NodeID: p.Nodes[0].ID, EntityRef: "serial:CLI-SN", From: q.Cutoff.Add(-time.Hour), Through: q.Cutoff, State: "active", Context: &gpurisk.HostContext{GPUModel: "fixture-only"}}
	data, _ = json.Marshal(host)
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
	var batch api.GPURiskOutcomeBatch
	store.First(&batch)
	now := time.Now().UTC()
	spec := gpurisk.DatasetSpec{BatchIDs: []uint{batch.ID}, Profile: "paper_core", Anchor: now.Add(-30 * 24 * time.Hour), TrainEnd: now.Add(-20 * 24 * time.Hour), ValidationEnd: now.Add(-10 * 24 * time.Hour), TestEnd: now}
	encoded, _ := json.Marshal(spec)
	specFile := filepath.Join(t.TempDir(), "dataset-spec.json")
	os.WriteFile(specFile, encoded, 0600)
	if e := run([]string{"dataset", "-driver", "sqlite", "-input", specFile}); e != nil {
		t.Fatal(e)
	}
	store.Model(&api.GPURiskOutcomeBatch{}).Count(&count)
	if count != 1 {
		t.Fatal("dataset export wrote results")
	}

}

func TestVerifyModelHasNoDatabaseDependency(t *testing.T) {
	t.Setenv("ATLAS_GPU_RISK_DSN", "")
	err := run([]string{"verify-model"})
	if err == nil || !strings.Contains(err.Error(), "parity rejected") {
		t.Fatal("file-only verifier attempted database access", err)
	}
}

func TestCandidateCLIReadOnlyRequestAndStrictRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate-cli.db")
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	store, e := storage.InitDB(path)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := store.DB.DB()
	defer raw.Close()
	if e := run([]string{"model-request", "-driver", "sqlite", "-query", "1", "-model", "1"}); e == nil {
		t.Fatal("absent query/model accepted")
	}
	for _, model := range []any{&api.GPURiskModel{}, &api.GPURiskModelBatch{}, &api.GPURiskModelScore{}} {
		var n int64
		store.DB.Model(model).Count(&n)
		if n != 0 {
			t.Fatal("request wrote records")
		}
	}
	input := filepath.Join(t.TempDir(), "request.json")
	os.WriteFile(input, []byte(`{"query_id":1,"model_id":1,"unknown":true}`), 0600)
	if e := run([]string{"model-record", "-driver", "sqlite", "-input", input}); e == nil {
		t.Fatal("unknown request field accepted")
	}
}

func TestIngestExplicitFlagsAndStrictInput(t *testing.T) {
	for _, args := range [][]string{{"evidence-ingest-prepare"}, {"evidence-ingest-prepare", "-query", "1"}, {"evidence-ingest-apply"}} {
		if e := run(args); e == nil {
			t.Fatal("missing required flags accepted", args)
		}
	}
	path := filepath.Join(t.TempDir(), "ingest.db")
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	if e := run([]string{"evidence-ingest-prepare", "-driver", "sqlite", "-query", "1", "-review", "1"}); e == nil {
		t.Fatal("absent DB accepted")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("prepare created database")
	}
	for _, payload := range []string{
		`{"available_at":"2020-01-01T00:00:00Z"}`,
		`{"assertions":[{"available_at":"2020-01-01T00:00:00Z"}]}`,
		`{"review":{"available_at":"2020-01-01T00:00:00Z"}}`,
		`{} {}`,
	} {
		file := filepath.Join(t.TempDir(), "approved.json")
		if e := os.WriteFile(file, []byte(payload), 0600); e != nil {
			t.Fatal(e)
		}
		e := run([]string{"evidence-ingest-apply", "-driver", "sqlite", "-input", file})
		if e == nil || (!strings.Contains(e.Error(), "invalid approved manifest") && !strings.Contains(e.Error(), "trailing approved manifest")) {
			t.Fatal("strict input failed", e)
		}
	}
}

func TestCoverageMembersCLIStrictInput(t *testing.T) {
	for _, args := range [][]string{
		{"coverage-members"},
		{"coverage-members", "-driver", "sqlite", "-query", "1"},
		{"coverage-members", "-driver", "sqlite", "-source", "monitoring"},
	} {
		if e := run(args); e == nil {
			t.Fatal("missing required flags accepted", args)
		}
	}
	path := filepath.Join(t.TempDir(), "coverage-members-cli.db")
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	store, e := storage.InitDB(path)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := store.DB.DB()
	defer raw.Close()
	if e := run([]string{"coverage-members", "-driver", "sqlite", "-query", "1", "-source", "monitoring", "-owner", "ops"}); e == nil || !strings.Contains(e.Error(), "invalid ranking snapshot") {
		t.Fatal("absent query accepted", e)
	}
	var count int64
	store.DB.Model(&api.GPURiskEvidenceRevision{}).Count(&count)
	if count != 0 {
		t.Fatal("failed batch wrote evidence", count)
	}
}

func TestCoverageCheckCLIStrictInput(t *testing.T) {
	for _, args := range [][]string{
		{"coverage-check"},
		{"coverage-check", "-driver", "sqlite", "-source", "hardware_fault_ledger"},
		{"coverage-check", "-driver", "sqlite", "-owner", "ops"},
	} {
		if e := run(args); e == nil {
			t.Fatal("missing required flags accepted", args)
		}
	}
	path := filepath.Join(t.TempDir(), "coverage-cli.db")
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	store, e := storage.InitDB(path)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := store.DB.DB()
	defer raw.Close()
	if e := run([]string{"coverage-check", "-driver", "sqlite", "-source", "monitoring", "-owner", "ops"}); e == nil || !strings.Contains(e.Error(), "no complete canonical durable record") {
		t.Fatal("incomplete monitoring source accepted", e)
	}
	var count int64
	store.DB.Model(&api.GPURiskSourceCoverageCheck{}).Count(&count)
	if count != 0 {
		t.Fatal("rejected source wrote a check row")
	}
	if e := run([]string{"coverage-check", "-driver", "sqlite", "-source", "hardware_fault_ledger", "-owner", "ops"}); e != nil {
		t.Fatal(e)
	}
	var row api.GPURiskSourceCoverageCheck
	if e := store.DB.Order("id DESC").First(&row).Error; e != nil {
		t.Fatal(e)
	}
	if row.Source != "hardware_fault_ledger" || row.Owner != "ops" || row.RecordCount != 0 || !row.ObservedAt.Equal(row.VerifiedThrough) {
		t.Fatalf("unexpected coverage row: %+v", row)
	}
}

func TestCoveragePlanRejectsUnapprovedArgumentsAndAbsentDatabase(t *testing.T) {
	t.Setenv("ATLAS_GPU_RISK_DSN", filepath.Join(t.TempDir(), "missing.sqlite"))
	for _, args := range [][]string{
		{"coverage-plan"},
		{"coverage-plan", "-query", "1", "-owner", "admin"},
		{"coverage-plan", "-query", "1", "-as-of", "invalid"},
		{"coverage-plan", "-query", "1", "-as-of", "2999-01-01T00:00:00Z"},
		{"coverage-plan", "-query", "1", "-driver", "sqlite"},
	} {
		if e := run(args); e == nil {
			t.Fatal("accepted", args)
		}
	}
	if _, e := os.Stat(os.Getenv("ATLAS_GPU_RISK_DSN")); !os.IsNotExist(e) {
		t.Fatal("created absent DB", e)
	}
}

func TestCoveragePlanSQLiteReadOnlySuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage-plan.sqlite")
	db, e := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	if e := db.AutoMigrate(&api.GPURiskQuery{}, &api.GPURiskMember{}, &api.GPURiskEvidenceRevision{}, &api.GPURiskSourceCoverageCheck{}); e != nil {
		t.Fatal(e)
	}
	cutoff := time.Now().UTC().Add(-time.Hour)
	completed := cutoff.Add(time.Second)
	q := api.GPURiskQuery{Key: strings.Repeat("a", 64), ScopeSHA: strings.Repeat("b", 64), ResultSHA: strings.Repeat("c", 64), Contract: gpurisk.Contract, Target: gpurisk.Target, Cutoff: cutoff, HorizonMinutes: 10080, Status: "completed", MemberCount: 1, CompletedAt: &completed}
	if e := db.Create(&q).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&api.GPURiskMember{QueryID: q.ID, NodeID: 1, EntityRef: "node", Status: "included"}).Error; e != nil {
		t.Fatal(e)
	}
	raw, _ := db.DB()
	if e := raw.Close(); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	args := []string{"coverage-plan", "-driver", "sqlite", "-query", fmt.Sprint(q.ID), "-as-of", cutoff.Add(time.Minute).Format(time.RFC3339Nano)}
	for i := 0; i < 2; i++ {
		if e := run(args); e != nil {
			t.Fatal(e)
		}
	}
	after, e := os.ReadFile(path)
	if e != nil || string(before) != string(after) {
		t.Fatal("read-only report changed database", e)
	}
}

func TestCoreSourcePlanSQLiteReadOnlySuccessAndArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core-plan.sqlite")
	store, e := storage.InitDB(path)
	if e != nil {
		t.Fatal(e)
	}
	old := time.Now().UTC().Add(-time.Hour)
	if e := store.Create(&api.GPUNode{NodeIP: "core-node", AssetSerial: "core-sn", Lifecycle: "active", CreatedAt: old, UpdatedAt: old}).Error; e != nil {
		t.Fatal(e)
	}
	snapshot, e := gpurisk.Capture(t.Context(), store.DB)
	if e != nil {
		t.Fatal(e)
	}
	q, e := gpurisk.Record(t.Context(), store.DB, snapshot)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := store.DB.DB()
	raw.Close()
	before, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	args := []string{"core-source-plan", "-driver", "sqlite", "-query", fmt.Sprint(q.ID), "-as-of", time.Now().UTC().Format(time.RFC3339Nano)}
	for i := 0; i < 2; i++ {
		if e := run(args); e != nil {
			t.Fatal(e)
		}
	}
	after, e := os.ReadFile(path)
	if e != nil || string(after) != string(before) {
		t.Fatal("modified readonly DB", e)
	}
	for _, args := range [][]string{{"core-source-plan"}, {"core-source-plan", "-query", "1", "-owner", "admin"}, {"core-source-plan", "-query", "1", "-as-of", "bad"}, {"core-source-plan", "-query", "1", "-as-of", "2999-01-01T00:00:00Z"}} {
		if e := run(args); e == nil {
			t.Fatal("accepted", args)
		}
	}
	missing := filepath.Join(t.TempDir(), "absent.sqlite")
	t.Setenv("ATLAS_GPU_RISK_DSN", missing)
	if e := run([]string{"core-source-plan", "-driver", "sqlite", "-query", "1"}); e == nil {
		t.Fatal("created missing db")
	}
	if _, e := os.Stat(missing); !os.IsNotExist(e) {
		t.Fatal("created DB", e)
	}
}

func TestContextPrepareSQLiteReadOnlySuccessAndArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context-prepare.sqlite")
	store, e := storage.InitDB(path)
	if e != nil {
		t.Fatal(e)
	}
	old := time.Now().UTC().Add(-time.Hour)
	if e := store.Create(&api.GPUNode{NodeIP: "core-node", AssetSerial: "core-sn", Lifecycle: "active", CreatedAt: old, UpdatedAt: old}).Error; e != nil {
		t.Fatal(e)
	}
	snapshot, e := gpurisk.Capture(t.Context(), store.DB)
	if e != nil {
		t.Fatal(e)
	}
	q, e := gpurisk.Record(t.Context(), store.DB, snapshot)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := store.DB.DB()
	raw.Close()
	before, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	args := []string{"context-prepare", "-driver", "sqlite", "-query", fmt.Sprint(q.ID), "-as-of", time.Now().UTC().Format(time.RFC3339Nano)}
	for i := 0; i < 2; i++ {
		if e := run(args); e != nil {
			t.Fatal(e)
		}
	}
	after, e := os.ReadFile(path)
	if e != nil || string(after) != string(before) {
		t.Fatal("modified readonly DB", e)
	}
	for _, args := range [][]string{{"context-prepare"}, {"context-prepare", "-query", "1", "-owner", "admin"}, {"context-prepare", "-query", "1", "-as-of", "bad"}, {"context-prepare", "-query", "1", "-as-of", "2999-01-01T00:00:00Z"}} {
		args = append(args, "-driver", "sqlite")
		if e := run(args); e == nil {
			t.Fatal("accepted", args)
		}
	}
	missing := filepath.Join(t.TempDir(), "absent.sqlite")
	t.Setenv("ATLAS_GPU_RISK_DSN", missing)
	if e := run([]string{"context-prepare", "-driver", "sqlite", "-query", "1"}); e == nil {
		t.Fatal("created missing db")
	}
	if _, e := os.Stat(missing); !os.IsNotExist(e) {
		t.Fatal("created DB", e)
	}
}

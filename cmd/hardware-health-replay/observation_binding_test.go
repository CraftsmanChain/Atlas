package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/internal/diskhealth"
	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func observationCLIInputs(t *testing.T) (string, string, string, *storage.DB) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "existing.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&api.InfrastructureAsset{}, &api.PhysicalDiskSnapshot{}, &api.DiskHealthEpisode{}, &api.DiskObservationScope{}, &api.DiskInstallation{}, &api.DiskInstallationReconcile{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	wrapped := &storage.DB{DB: db}
	raw, err := os.ReadFile("../../internal/diskhealth/testdata/input.json")
	if err != nil {
		t.Fatal(err)
	}
	var input diskhealth.InputManifest
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	input.Inputs = input.Inputs[:1]
	smart, err := os.ReadFile("../../internal/diskhealth/testdata/" + input.Inputs[0].File)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, input.Inputs[0].File), smart, 0600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "input.json")
	writeInput := func() {
		t.Helper()
		data, _ := json.Marshal(input)
		if err := os.WriteFile(manifestPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeInput()
	cutoff, _ := time.Parse(time.RFC3339, "2021-11-16T04:10:00Z")
	report, err := diskhealth.Build(manifestPath, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	service := diskhealth.NewService(wrapped)
	if _, err := service.PersistReport(report, ""); err != nil {
		t.Fatal(err)
	}
	var snapshot api.PhysicalDiskSnapshot
	if err := db.First(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	m := diskhealth.InstallationReconcileManifest{Schema: diskhealth.InstallationReconcileSchema, ScopeID: "fixture-cli-observation", AssetKey: "fixture:cli-observation-host", HostGeneration: "host-1", Node: snapshot.Node, SnapshotScope: snapshot.Scope, SnapshotGeneration: snapshot.Generation, Disposition: "test", HostIdentityEvidenceSHA256: diskhealth.Digest([]byte("synthetic-host-proof")), Actor: "synthetic-cli-test", ObservedAt: cutoff, AvailableAt: cutoff.Add(time.Second), Complete: true, EnumerationBoundary: "single public fixture", ExpectedEntityIDs: []string{snapshot.EntityID}, KnownInstallationIDs: []string{}, Members: []diskhealth.InstallationReconcileMember{{EntityID: snapshot.EntityID, SnapshotKey: snapshot.SnapshotKey, InputSHA256: snapshot.InputSHA256}}, Closures: []diskhealth.InstallationReconcileClosure{}, Failures: []diskhealth.InstallationReconcileFailure{}}
	mraw, _ := json.Marshal(m)
	if _, err := service.ReconcileInstallations(m, diskhealth.Digest(mraw)); err != nil {
		t.Fatal(err)
	}
	var scope api.DiskObservationScope
	if err := db.First(&scope).Error; err != nil {
		t.Fatal(err)
	}
	binding := diskhealth.ObservationBinding{Schema: diskhealth.ObservationBindingSchema, ScopeID: scope.ScopeID, ExpectedRosterVersion: "1", AssetIdentitySHA256: scope.AssetIdentitySHA256, Node: scope.Node, SnapshotScope: scope.SnapshotScope, SnapshotGeneration: scope.SnapshotGeneration, EntityIDs: []string{snapshot.EntityID}}
	braw, _ := json.MarshalIndent(binding, "", "  ")
	bindingPath := filepath.Join(dir, "binding.json")
	if err := os.WriteFile(bindingPath, braw, 0600); err != nil {
		t.Fatal(err)
	}
	// The subsequent batch has a new observation, rather than republishing a seed.
	input.Inputs[0].ObservedAt = input.Inputs[0].ObservedAt.Add(30 * time.Minute)
	input.Inputs[0].AvailableAt = input.Inputs[0].AvailableAt.Add(30 * time.Minute)
	writeInput()
	return dbPath, bindingPath, manifestPath, wrapped
}

func TestObservationCLIPreflightAndPostCommitReceipt(t *testing.T) {
	dbPath, bindingPath, manifestPath, db := observationCLIInputs(t)
	t.Setenv("ATLAS_OBSERVATION_TEST_DSN", dbPath)
	base := []string{"--installation-binding", bindingPath, "--db-dsn-env", "ATLAS_OBSERVATION_TEST_DSN"}
	var out, stderr bytes.Buffer
	if code := run(append(base, "--check-installation-binding"), &out, &stderr); code != 0 {
		t.Fatalf("preflight code=%d %s", code, stderr.String())
	}
	var count int64
	db.Model(&api.PhysicalDiskSnapshot{}).Count(&count)
	if count != 1 || !strings.Contains(out.String(), "entities=1") {
		t.Fatal("preflight wrote evidence or omitted count")
	}
	receiptPath := filepath.Join(t.TempDir(), "persistence.json")
	reportDir := filepath.Join(t.TempDir(), "report")
	args := append(base, "--manifest", manifestPath, "--as-of", "2021-11-16T04:40:00Z", "--out-dir", reportDir, "--persistence-receipt", receiptPath, "--deadline", time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano))
	out.Reset()
	stderr.Reset()
	if code := run(args, &out, &stderr); code != 0 {
		t.Fatalf("persistence code=%d %s", code, stderr.String())
	}
	raw, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt diskhealth.ObservationPersistenceReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	reportRaw, err := os.ReadFile(filepath.Join(reportDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Verified || receipt.SnapshotsCreated != 1 || len(receipt.Entities) != 1 || receipt.ReportSHA256 != diskhealth.Digest(reportRaw) || receipt.Entities[0].ReportSHA256 != receipt.ReportSHA256 {
		t.Fatalf("receipt not bound to actual committed output: %+v", receipt)
	}
	if err := writePersistenceReceipt(receiptPath, receipt); err == nil {
		t.Fatal("existing receipt was overwritten")
	}
	after, _ := os.ReadFile(receiptPath)
	if !bytes.Equal(raw, after) {
		t.Fatal("receipt changed on retry")
	}
}

func TestObservationCLIReceiptFailureRequiresPauseAfterCommit(t *testing.T) {
	dbPath, bindingPath, manifestPath, db := observationCLIInputs(t)
	t.Setenv("ATLAS_OBSERVATION_TEST_DSN", dbPath)
	var out, stderr bytes.Buffer
	args := []string{"--installation-binding", bindingPath, "--db-dsn-env", "ATLAS_OBSERVATION_TEST_DSN", "--manifest", manifestPath, "--as-of", "2021-11-16T04:40:00Z", "--out-dir", filepath.Join(t.TempDir(), "report"), "--persistence-receipt", filepath.Join(t.TempDir(), "missing", "receipt.json")}
	if code := run(args, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "persistence state may be unknown; pause required") {
		t.Fatalf("receipt failure not marked unknown: code=%d %s", code, stderr.String())
	}
	var count int64
	db.Model(&api.PhysicalDiskSnapshot{}).Count(&count)
	if count != 2 {
		t.Fatalf("postcommit receipt failure retried or hid persistence: %d", count)
	}
	if strings.Contains(out.String(), "Persisted bound report") {
		t.Fatal("failed receipt claimed success")
	}
}

func TestObservationCLIExistingDBAndSafeErrors(t *testing.T) {
	for _, dsn := range []string{filepath.Join(t.TempDir(), "missing.db"), "file:" + filepath.Join(t.TempDir(), "missing.db")} {
		if db, err := openExistingReplayDB(context.Background(), "sqlite", dsn); err == nil {
			sqlDB, _ := db.DB.DB()
			sqlDB.Close()
			t.Fatal("created missing observation database")
		}
	}
	_, bindingPath, _, _ := observationCLIInputs(t)
	t.Setenv("ATLAS_OBSERVATION_TEST_DSN", "postgres://SYNTHETIC-PRIVATE-DO-NOT-ECHO:bad@127.0.0.1:1/no_db?sslmode=disable&connect_timeout=1")
	var out, stderr bytes.Buffer
	if code := run([]string{"--installation-binding", bindingPath, "--db-dsn-env", "ATLAS_OBSERVATION_TEST_DSN", "--db-driver", "postgres", "--check-installation-binding"}, &out, &stderr); code != 1 {
		t.Fatalf("invalid private database accepted: %d", code)
	}
	if strings.Contains(out.String()+stderr.String(), "SYNTHETIC-PRIVATE-DO-NOT-ECHO") {
		t.Fatal("database reference leaked")
	}
}

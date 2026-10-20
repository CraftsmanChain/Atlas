package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/internal/diskhealth"
	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func cliManifest(t *testing.T) (string, []byte) {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	m := diskhealth.InstallationReconcileManifest{Schema: diskhealth.InstallationReconcileSchema, ScopeID: "fixture-cli-roster", AssetKey: "fixture:cli-host", HostGeneration: "host-1", Node: "fixture-node", SnapshotScope: "snapshot-scope", SnapshotGeneration: "snapshot-generation", Disposition: "test", HostIdentityEvidenceSHA256: diskhealth.Digest([]byte("synthetic-host-proof")), Actor: "synthetic-cli-test", ObservedAt: now, AvailableAt: now.Add(time.Second), Complete: true, EnumerationBoundary: "explicit synthetic fixture with zero devices", ExpectedEntityIDs: []string{}, KnownInstallationIDs: []string{}, Members: []diskhealth.InstallationReconcileMember{}, Closures: []diskhealth.InstallationReconcileClosure{}, Failures: []diskhealth.InstallationReconcileFailure{}}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path, raw
}

func TestInstallationCLIPlanOnlyDoesNotOpenDatabase(t *testing.T) {
	path, raw := cliManifest(t)
	dbPath := filepath.Join(t.TempDir(), "must-not-exist.db")
	t.Setenv("ATLAS_CLI_TEST_DSN", dbPath)
	var out, stderr bytes.Buffer
	if code := run([]string{"--manifest", path, "--db-dsn-env", "ATLAS_CLI_TEST_DSN"}, &out, &stderr); code != 0 {
		t.Fatalf("plan rejected: code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("plan-only opened or created database")
	}
	if !strings.Contains(out.String(), `"mode":"plan_only"`) || !strings.Contains(out.String(), diskhealth.Digest(raw)) || !strings.Contains(out.String(), "database_binding_and_expected_version_not_checked") {
		t.Fatalf("plan claims too much: %s", out.String())
	}
}

func TestInstallationCLIApplyAndReplay(t *testing.T) {
	path, raw := cliManifest(t)
	dbPath := filepath.Join(t.TempDir(), "atlas.db")
	db, err := storage.InitDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	t.Setenv("ATLAS_CLI_TEST_DSN", dbPath)
	args := []string{"--manifest", path, "--manifest-sha256", diskhealth.Digest(raw), "--apply", "--db-dsn-env", "ATLAS_CLI_TEST_DSN"}
	for i := 0; i < 2; i++ {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code != 0 {
			t.Fatalf("apply failed: code=%d stderr=%s", code, stderr.String())
		}
		var result diskhealth.InstallationReconcileResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.RosterVersion != 1 || result.Idempotent != (i == 1) {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
	var count int64
	db.Model(&api.DiskInstallationReconcile{}).Count(&count)
	if count != 1 {
		t.Fatalf("CLI replay duplicated audit: %d", count)
	}
}

func TestInstallationCLIRejectsHashUnknownFieldsAndSecretsInErrors(t *testing.T) {
	path, raw := cliManifest(t)
	for _, args := range [][]string{{"--manifest", path, "--apply"}, {"--manifest", path, "--manifest-sha256", diskhealth.Digest([]byte("wrong"))}, {"--manifest", path, "unexpected"}} {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code == 0 {
			t.Fatal("invalid CLI invocation accepted")
		}
	}
	unknown := append([]byte(nil), raw[:len(raw)-1]...)
	unknown = append(unknown, []byte(`,"password":"SYNTHETIC-DO-NOT-ECHO"}`)...)
	if err := os.WriteFile(path, unknown, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if run([]string{"--manifest", path}, &out, &stderr) == 0 {
		t.Fatal("credential field accepted")
	}
	if strings.Contains(stderr.String(), "SYNTHETIC-DO-NOT-ECHO") {
		t.Fatal("invalid field payload leaked")
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATLAS_CLI_PRIVATE_TEST_DSN", "invalid-driver-secret-SYNTHETIC-DO-NOT-ECHO://")
	out.Reset()
	stderr.Reset()
	if run([]string{"--manifest", path, "--manifest-sha256", diskhealth.Digest(raw), "--apply", "--db-driver", "postgres", "--db-dsn-env", "ATLAS_CLI_PRIVATE_TEST_DSN"}, &out, &stderr) == 0 {
		t.Fatal("invalid private DSN accepted")
	}
	if strings.Contains(out.String()+stderr.String(), "SYNTHETIC-DO-NOT-ECHO") {
		t.Fatal("DSN leaked")
	}
}

package diskhealth

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var installationTestNow = time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)

func installationTestDB(t *testing.T) (*storage.DB, *Service) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "installations.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&api.InfrastructureAsset{}, &api.PhysicalDiskSnapshot{}, &api.DiskHealthEpisode{}, &api.DiskObservationScope{}, &api.DiskInstallation{}, &api.DiskInstallationReconcile{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	wrapped := &storage.DB{DB: db}
	s := NewService(wrapped)
	s.now = func() time.Time { return installationTestNow }
	return wrapped, s
}

func installationTestSnapshot(t *testing.T, db *storage.DB, name string) api.PhysicalDiskSnapshot {
	t.Helper()
	identity, _ := json.Marshal([]string{"snapshot-scope", "snapshot-generation", "nvme_controller", "SYNTHETIC-" + name})
	snapshot := api.PhysicalDiskSnapshot{SnapshotKey: Digest([]byte("snapshot-" + name)), EntityID: Digest(identity), Current: true, EntityType: "nvme_controller", IdentityStatus: "bound", Scope: "snapshot-scope", Generation: "snapshot-generation", Node: "fixture-node", Serial: "SYNTHETIC-" + name, SourceKind: "fixture", InputSHA256: Digest([]byte("input-" + name)), ObservedAt: installationTestNow.Add(-2 * time.Hour), AvailableAt: installationTestNow.Add(-2*time.Hour + time.Second), Level: "unknown", AssessmentStatus: "insufficient_data", HitsJSON: "[]", EvidenceJSON: "[]", DiskJSON: "null", MemoryJSON: "null"}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func installationTestManifest(snapshots ...api.PhysicalDiskSnapshot) InstallationReconcileManifest {
	m := InstallationReconcileManifest{Schema: InstallationReconcileSchema, ScopeID: "fixture-roster", AssetKey: "fixture:host", HostGeneration: "host-installation-1", Node: "fixture-node", SnapshotScope: "snapshot-scope", SnapshotGeneration: "snapshot-generation", Disposition: "test", HostIdentityEvidenceSHA256: Digest([]byte("synthetic-host-evidence")), Actor: "synthetic-test", ObservedAt: installationTestNow.Add(-time.Hour), AvailableAt: installationTestNow.Add(-time.Hour + time.Second), Complete: true, EnumerationBoundary: "all explicit synthetic disk members", ExpectedEntityIDs: []string{}, KnownInstallationIDs: []string{}, Members: []InstallationReconcileMember{}, Closures: []InstallationReconcileClosure{}, Failures: []InstallationReconcileFailure{}}
	for _, snapshot := range snapshots {
		m.ExpectedEntityIDs = append(m.ExpectedEntityIDs, snapshot.EntityID)
		m.Members = append(m.Members, InstallationReconcileMember{EntityID: snapshot.EntityID, SnapshotKey: snapshot.SnapshotKey, InputSHA256: snapshot.InputSHA256})
	}
	return m
}

func installationTestApply(t *testing.T, s *Service, m InstallationReconcileManifest) InstallationReconcileResult {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ReconcileInstallations(m, Digest(raw))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func installationTestReject(t *testing.T, s *Service, m InstallationReconcileManifest, want string) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileInstallations(m, Digest(raw)); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want rejection %q; got %v", want, err)
	}
}

func installationTestActive(t *testing.T, db *storage.DB) []api.DiskInstallation {
	t.Helper()
	var rows []api.DiskInstallation
	if err := db.Where("state = ?", "active").Order("installation_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestInstallationReconcileExplicitLifecycleAndReplay(t *testing.T) {
	db, s := installationTestDB(t)
	a, b := installationTestSnapshot(t, db, "A"), installationTestSnapshot(t, db, "B")
	m := installationTestManifest(a)
	first := installationTestApply(t, s, m)
	if first.RosterVersion != 1 || first.OpenedCount != 1 || first.Idempotent {
		t.Fatalf("unexpected initial result: %+v", first)
	}
	duplicate := installationTestApply(t, s, m)
	if !duplicate.Idempotent || duplicate.RosterVersion != 1 {
		t.Fatalf("manifest replay changed roster: %+v", duplicate)
	}
	old := installationTestActive(t, db)[0]
	// A complete enumeration that omits A still cannot retire it.
	missing := installationTestManifest(b)
	missing.ExpectedRosterVersion = 1
	missing.KnownInstallationIDs = []string{old.InstallationID}
	missing.Actor = "missing-is-not-retired"
	r := installationTestApply(t, s, missing)
	if r.OpenedCount != 1 || r.ClosedCount != 0 || len(installationTestActive(t, db)) != 2 {
		t.Fatalf("missing member was retired: %+v", r)
	}
	var currentB api.DiskInstallation
	if err := db.Where("entity_id = ? AND state = ?", b.EntityID, "active").First(&currentB).Error; err != nil {
		t.Fatal(err)
	}
	replace := installationTestManifest(b)
	replace.ExpectedRosterVersion = 2
	replace.KnownInstallationIDs = []string{old.InstallationID, currentB.InstallationID}
	replace.Closures = []InstallationReconcileClosure{{InstallationID: old.InstallationID, Reason: "explicit_replacement", EvidenceSHA256: Digest([]byte("replacement-evidence"))}}
	r = installationTestApply(t, s, replace)
	if r.RosterVersion != 3 || r.OpenedCount != 0 || r.ClosedCount != 1 || len(installationTestActive(t, db)) != 1 {
		t.Fatalf("explicit closure failed: %+v", r)
	}
	// Old evidence may be replayed, but it cannot mutate the installed roster.
	row := Result{Reference: "old-replay", EntityID: a.EntityID, EntityType: a.EntityType, IdentityStatus: "bound", Scope: a.Scope, Generation: a.Generation, Node: a.Node, Serial: a.Serial, SourceKind: a.SourceKind, InputSHA256: a.InputSHA256, ObservedAt: a.ObservedAt, AvailableAt: a.AvailableAt, RuleVersion: RuleVersion, AssessmentStatus: "insufficient_data", Level: "unknown"}
	if _, err := s.PersistReport(Report{Schema: SchemaVersion, RuleVersion: ReportRuleVersion, AsOf: installationTestNow, Rows: []Result{row}}, ""); err != nil {
		t.Fatal(err)
	}
	if len(installationTestActive(t, db)) != 1 {
		t.Fatal("old report resurrected an installation")
	}
	var closed api.DiskInstallation
	if err := db.Where("installation_id = ?", old.InstallationID).First(&closed).Error; err != nil {
		t.Fatal(err)
	}
	if closed.State != "superseded" || closed.ActiveEntityID != nil || closed.ClosedAt == nil || closed.CloseRosterVersion == nil || *closed.CloseRosterVersion != 3 {
		t.Fatalf("missing closure audit: %+v", closed)
	}
	// Explicitly observed reinsertion creates a new segment, preserving old A.
	reinsert := installationTestManifest(a, b)
	reinsert.ExpectedRosterVersion = 3
	reinsert.KnownInstallationIDs = []string{currentB.InstallationID}
	reinsert.Actor = "explicit-reinsertion"
	r = installationTestApply(t, s, reinsert)
	if r.OpenedCount != 1 || len(installationTestActive(t, db)) != 2 {
		t.Fatalf("reinsertion failed: %+v", r)
	}
	var aSegments int64
	db.Model(&api.DiskInstallation{}).Where("entity_id = ?", a.EntityID).Count(&aSegments)
	if aSegments != 2 {
		t.Fatalf("reinsertion overwrote historical segment: %d", aSegments)
	}
}

func TestInstallationReconcileRejectsIncompleteEvidenceAndMixedHistory(t *testing.T) {
	for _, test := range []struct {
		name, want string
		mutate     func(*InstallationReconcileManifest, *storage.DB, api.PhysicalDiskSnapshot)
	}{
		{"incomplete", "incomplete", func(m *InstallationReconcileManifest, _ *storage.DB, _ api.PhysicalDiskSnapshot) { m.Complete = false }},
		{"failure", "incomplete", func(m *InstallationReconcileManifest, _ *storage.DB, _ api.PhysicalDiskSnapshot) {
			m.Failures = []InstallationReconcileFailure{{Member: "device-1", Code: "timeout"}}
		}},
		{"snapshot-sha", "snapshot evidence not found", func(m *InstallationReconcileManifest, _ *storage.DB, _ api.PhysicalDiskSnapshot) {
			m.Members[0].InputSHA256 = Digest([]byte("wrong"))
		}},
		{"node", "binding conflict", func(m *InstallationReconcileManifest, _ *storage.DB, _ api.PhysicalDiskSnapshot) {
			m.Node = "other-node"
		}},
		{"generation", "binding conflict", func(m *InstallationReconcileManifest, _ *storage.DB, _ api.PhysicalDiskSnapshot) {
			m.SnapshotGeneration = "other-generation"
		}},
		{"future-cutoff", "future", func(m *InstallationReconcileManifest, _ *storage.DB, _ api.PhysicalDiskSnapshot) {
			m.AvailableAt = installationTestNow.Add(time.Second)
		}},
		{"evidence-after-cutoff", "crosses cutoff", func(m *InstallationReconcileManifest, _ *storage.DB, _ api.PhysicalDiskSnapshot) {
			m.ObservedAt = installationTestNow.Add(-3 * time.Hour)
			m.AvailableAt = m.ObservedAt.Add(time.Second)
		}},
		{"historical-node", "history contains identity conflicts", func(_ *InstallationReconcileManifest, db *storage.DB, a api.PhysicalDiskSnapshot) {
			a.ID = 0
			a.SnapshotKey = Digest([]byte("mixed-old"))
			a.Current = false
			a.Node = "different-old-node"
			if err := db.Create(&a).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"provisional", "binding conflict", func(_ *InstallationReconcileManifest, db *storage.DB, a api.PhysicalDiskSnapshot) {
			db.Model(&a).Update("identity_status", "provisional")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, s := installationTestDB(t)
			a := installationTestSnapshot(t, db, "A")
			m := installationTestManifest(a)
			test.mutate(&m, db, a)
			installationTestReject(t, s, m, test.want)
			if len(installationTestActive(t, db)) != 0 {
				t.Fatal("rejection left active installation")
			}
			var n int64
			db.Model(&api.DiskObservationScope{}).Count(&n)
			if n != 0 {
				t.Fatal("rejection left scope")
			}
		})
	}
}

func TestInstallationReconcileStableAssetAndScopeBinding(t *testing.T) {
	db, s := installationTestDB(t)
	a := installationTestSnapshot(t, db, "A")
	asset := api.InfrastructureAsset{AssetKey: "asset_machine:test-host", Source: "asset_machine", SourceID: "stable-id", DataCenterID: "test-dc", SerialNumber: "SYNTHETIC-HOST", InUse: false, Present: true}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&api.PhysicalDiskSnapshot{}).Where("id = ?", a.ID).Update("source_kind", "operator_snapshot").Error; err != nil {
		t.Fatal(err)
	}
	m := installationTestManifest(a)
	m.AssetKey = asset.AssetKey
	installationTestApply(t, s, m) // explicit bounded test supports not-in-use asset
	active := installationTestActive(t, db)[0]
	next := m
	next.ExpectedRosterVersion = 1
	next.KnownInstallationIDs = []string{active.InstallationID}
	next.Actor = "next"
	if err := db.Model(&asset).Updates(map[string]any{"ip_address": "192.0.2.5", "name": "renamed", "present": false}).Error; err != nil {
		t.Fatal(err)
	}
	installationTestApply(t, s, next) // endpoint/name/liveness do not change stable identity
	next.ExpectedRosterVersion = 2
	next.Actor = "identity-change"
	if err := db.Model(&asset).Update("serial_number", "SYNTHETIC-OTHER-HOST").Error; err != nil {
		t.Fatal(err)
	}
	installationTestReject(t, s, next, "asset identity changed")
	if err := db.Model(&asset).Update("serial_number", "SYNTHETIC-HOST").Error; err != nil {
		t.Fatal(err)
	}
	duplicate := m
	duplicate.ScopeID = "second-roster"
	duplicate.Actor = "parallel-host-roster"
	installationTestReject(t, s, duplicate, "active observation scope")
	duplicate.HostGeneration = "new-host-generation"
	duplicate.SnapshotGeneration = "new-generation"
	installationTestReject(t, s, duplicate, "active observation scope")
	next.HostGeneration = "changed-binding"
	installationTestReject(t, s, next, "scope identity binding conflict")
	fleet := m
	fleet.ScopeID = "fleet-scope"
	fleet.AssetKey = "asset_machine:fleet"
	fleet.Disposition = "fleet"
	fleetAsset := api.InfrastructureAsset{AssetKey: fleet.AssetKey, Source: "asset_machine", InUse: false, Present: true}
	if err := db.Create(&fleetAsset).Error; err != nil {
		t.Fatal(err)
	}
	installationTestReject(t, s, fleet, "not present and in use")
}

func TestInstallationReconcileKnownRosterAndClosureGuards(t *testing.T) {
	db, s := installationTestDB(t)
	a := installationTestSnapshot(t, db, "A")
	m := installationTestManifest(a)
	installationTestApply(t, s, m)
	active := installationTestActive(t, db)[0]
	stale := m
	stale.Actor = "different-manifest"
	installationTestReject(t, s, stale, "expected roster version conflict")
	missing := m
	missing.ExpectedRosterVersion = 1
	missing.Actor = "partial-known"
	installationTestReject(t, s, missing, "known installations")
	contradict := m
	contradict.ExpectedRosterVersion = 1
	contradict.KnownInstallationIDs = []string{active.InstallationID}
	contradict.Closures = []InstallationReconcileClosure{{InstallationID: active.InstallationID, Reason: "retire", EvidenceSHA256: Digest([]byte("close"))}}
	installationTestReject(t, s, contradict, "contradicts observed")
	// Device locator rename is irrelevant to physical identity/roster membership.
	if err := db.Model(&a).Update("device", "/dev/renamed-nvme").Error; err != nil {
		t.Fatal(err)
	}
	noChange := m
	noChange.ExpectedRosterVersion = 1
	noChange.KnownInstallationIDs = []string{active.InstallationID}
	noChange.Actor = "path-renamed"
	r := installationTestApply(t, s, noChange)
	if r.OpenedCount != 0 || r.ClosedCount != 0 {
		t.Fatalf("path rename created segment: %+v", r)
	}
}

func TestInstallationReconcileRollbackIncludesClosuresAndAudit(t *testing.T) {
	db, s := installationTestDB(t)
	a, b := installationTestSnapshot(t, db, "A"), installationTestSnapshot(t, db, "B")
	m := installationTestManifest(a)
	installationTestApply(t, s, m)
	old := installationTestActive(t, db)[0]
	if err := db.Callback().Create().Before("gorm:create").Register("fail-installation-audit", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "disk_installation_reconciles" {
			tx.AddError(errors.New("synthetic audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	replace := installationTestManifest(b)
	replace.ExpectedRosterVersion = 1
	replace.KnownInstallationIDs = []string{old.InstallationID}
	replace.Closures = []InstallationReconcileClosure{{InstallationID: old.InstallationID, Reason: "replacement", EvidenceSHA256: Digest([]byte("close"))}}
	installationTestReject(t, s, replace, "synthetic audit failure")
	rows := installationTestActive(t, db)
	if len(rows) != 1 || rows[0].InstallationID != old.InstallationID {
		t.Fatalf("rollback lost old member: %+v", rows)
	}
	var scope api.DiskObservationScope
	db.First(&scope, "scope_id = ?", m.ScopeID)
	if scope.RosterVersion != 1 {
		t.Fatal("rollback advanced roster")
	}
	var audits, segments int64
	db.Model(&api.DiskInstallationReconcile{}).Count(&audits)
	db.Model(&api.DiskInstallation{}).Count(&segments)
	if audits != 1 || segments != 1 {
		t.Fatalf("rollback retained writes: audits=%d segments=%d", audits, segments)
	}
}

func TestInstallationManifestStrictSHAAndExplicitFields(t *testing.T) {
	db, s := installationTestDB(t)
	a := installationTestSnapshot(t, db, "A")
	m := installationTestManifest(a)
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseInstallationReconcileManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileInstallations(parsed, Digest([]byte("wrong"))); err == nil {
		t.Fatal("wrong manifest SHA accepted")
	}
	parsed.Actor = "mutation-after-binding"
	if _, err := s.ReconcileInstallations(parsed, Digest(raw)); err == nil {
		t.Fatal("modified bound manifest accepted")
	}
	parsed, err = ParseInstallationReconcileManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileInstallations(parsed, Digest(raw)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"schema":"x","schema":"y"}`, string(raw[:len(raw)-1]) + `,"password":"do-not-print"}`, strings.Repeat(" ", InstallationManifestMaxBytes+1), strings.Replace(string(raw), `"known_installation_ids": []`, `"known_installation_ids": null`, 1), strings.Replace(string(raw), `"expected_roster_version": 0,`, "", 1)} {
		if _, err := ParseInstallationReconcileManifest([]byte(bad)); err == nil {
			t.Fatal("invalid/unknown/implicit manifest accepted")
		}
	}
}

func TestInstallationSQLiteConcurrentVersionAndIdempotency(t *testing.T) {
	db, s := installationTestDB(t)
	a := installationTestSnapshot(t, db, "A")
	m := installationTestManifest(a)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, actor := range []string{"first", "second"} {
		wg.Add(1)
		go func(actor string) {
			defer wg.Done()
			candidate := m
			candidate.Actor = actor
			raw, _ := json.Marshal(candidate)
			_, err := s.ReconcileInstallations(candidate, Digest(raw))
			results <- err
		}(actor)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if strings.Contains(err.Error(), "expected roster version conflict") {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 || len(installationTestActive(t, db)) != 1 {
		t.Fatalf("concurrent first-create outcomes success=%d conflicts=%d", success, conflicts)
	}
}

func TestInstallationRegisteredReportSourceKindCannotChange(t *testing.T) {
	for _, originalKind := range []string{"fixture", "operator_snapshot"} {
		t.Run(originalKind, func(t *testing.T) {
			db, s := installationTestDB(t)
			a := installationTestSnapshot(t, db, "registered-source")
			m := installationTestManifest(a)
			otherKind := "operator_snapshot"
			if originalKind == "operator_snapshot" {
				otherKind = "fixture"
				m.AssetKey = "asset_machine:registered-source-test"
				asset := api.InfrastructureAsset{AssetKey: m.AssetKey, Source: "asset_machine", SourceID: "synthetic-asset", SerialNumber: "SYNTHETIC-HOST", Present: true}
				if err := db.Create(&asset).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&a).Update("source_kind", originalKind).Error; err != nil {
					t.Fatal(err)
				}
			}
			installationTestApply(t, s, m)
			bad := Result{Reference: "forbidden-source-change", EntityID: a.EntityID, EntityType: a.EntityType, IdentityStatus: "bound", Scope: a.Scope, Generation: a.Generation, Node: a.Node, Serial: a.Serial, SourceKind: otherKind, InputSHA256: Digest([]byte("different-source-input")), ObservedAt: installationTestNow.Add(-time.Minute), AvailableAt: installationTestNow.Add(-time.Minute + time.Second), RuleVersion: RuleVersion, AssessmentStatus: "insufficient_data", Level: "unknown"}
			// A preceding valid new entity must roll back as part of the report.
			prefix := bad
			prefix.EntityID = Digest([]byte("unregistered-prefix"))
			prefix.Serial = "SYNTHETIC-PREFIX"
			prefix.SourceKind = originalKind
			if _, err := s.PersistReport(Report{Schema: SchemaVersion, RuleVersion: ReportRuleVersion, AsOf: installationTestNow, Rows: []Result{prefix, bad}}, ""); err == nil {
				t.Fatal("registered source-kind switch accepted")
			}
			var snapshots int64
			db.Model(&api.PhysicalDiskSnapshot{}).Count(&snapshots)
			if snapshots != 1 {
				t.Fatalf("rejected report retained prefix/current writes: %d", snapshots)
			}
			var latest api.PhysicalDiskSnapshot
			if err := db.Where("entity_id = ? AND current = ?", a.EntityID, true).First(&latest).Error; err != nil {
				t.Fatal(err)
			}
			if latest.SnapshotKey != a.SnapshotKey || latest.SourceKind != originalKind {
				t.Fatal("rejected source-kind switch overwrote latest snapshot")
			}
		})
	}
}

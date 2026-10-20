package diskhealth

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

func observationTestBinding(t *testing.T, db *storage.DB, scopeID string) ObservationBinding {
	t.Helper()
	var scope api.DiskObservationScope
	if err := db.Where("scope_id = ?", scopeID).First(&scope).Error; err != nil {
		t.Fatal(err)
	}
	var active []api.DiskInstallation
	if err := db.Where("scope_id = ? AND state = ?", scopeID, "active").Order("entity_id").Find(&active).Error; err != nil {
		t.Fatal(err)
	}
	b := ObservationBinding{Schema: ObservationBindingSchema, ScopeID: scope.ScopeID, ExpectedRosterVersion: "1", AssetIdentitySHA256: scope.AssetIdentitySHA256, Node: scope.Node, SnapshotScope: scope.SnapshotScope, SnapshotGeneration: scope.SnapshotGeneration, EntityIDs: []string{}}
	for _, row := range active {
		b.EntityIDs = append(b.EntityIDs, row.EntityID)
	}
	raw, _ := json.MarshalIndent(b, "", "  ")
	b, err := ParseObservationBinding(raw)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func observationTestReport(rows ...api.PhysicalDiskSnapshot) Report {
	r := Report{Schema: SchemaVersion, RuleVersion: ReportRuleVersion, AsOf: installationTestNow, Rows: []Result{}}
	for _, row := range rows {
		r.Rows = append(r.Rows, Result{Reference: "pilot-" + row.EntityID, EntityID: row.EntityID, EntityType: row.EntityType, IdentityStatus: "bound", Scope: row.Scope, Generation: row.Generation, Node: row.Node, Serial: row.Serial, SourceKind: row.SourceKind, InputSHA256: Digest([]byte("pilot-new-" + row.EntityID)), ObservedAt: installationTestNow.Add(-time.Minute), AvailableAt: installationTestNow.Add(-time.Minute + time.Second), RuleVersion: RuleVersion, AssessmentStatus: "insufficient_data", Level: "unknown"})
	}
	return r
}

func TestObservationBindingParseFrozenVersionAndSHA(t *testing.T) {
	db, s := installationTestDB(t)
	a := installationTestSnapshot(t, db, "observation-A")
	installationTestApply(t, s, installationTestManifest(a))
	b := observationTestBinding(t, db, "fixture-roster")
	sha, err := b.SHA256()
	if err != nil || sha != Digest(b.raw) {
		t.Fatalf("binding is not byte-bound: %v", err)
	}
	b.Node = "changed"
	if _, err := b.SHA256(); err == nil {
		t.Fatal("mutated parsed binding accepted")
	}
	for _, mutate := range []func(*ObservationBinding){
		func(b *ObservationBinding) { b.ExpectedRosterVersion = "01" },
		func(b *ObservationBinding) { b.ExpectedRosterVersion = "18446744073709551616" },
		func(b *ObservationBinding) { b.EntityIDs = append(b.EntityIDs, b.EntityIDs[0]) },
		func(b *ObservationBinding) { b.AssetIdentitySHA256 = strings.Repeat("z", 64) },
	} {
		b = observationTestBinding(t, db, "fixture-roster")
		mutate(&b)
		raw, _ := json.Marshal(b)
		if _, err := ParseObservationBinding(raw); err == nil {
			t.Fatal("invalid binding accepted")
		}
	}
}

func TestObservationBoundPersistenceRechecksPreflightAndReportsCommit(t *testing.T) {
	db, s := installationTestDB(t)
	a, b := installationTestSnapshot(t, db, "pilot-A"), installationTestSnapshot(t, db, "pilot-B")
	installationTestApply(t, s, installationTestManifest(a, b))
	binding := observationTestBinding(t, db, "fixture-roster")
	if err := s.CheckObservationBinding(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	report := observationTestReport(a, b)
	sha := Digest([]byte("actual report bytes"))
	receipt, err := s.PersistReportWithBinding(context.Background(), report, sha, binding)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Verified || receipt.Schema != ObservationPersistenceSchema || receipt.SnapshotsCreated != 2 || receipt.Duplicate != 0 || receipt.CompletedAt.IsZero() || receipt.RosterVersion != "1" || len(receipt.Entities) != 2 {
		t.Fatalf("incomplete receipt: %+v", receipt)
	}
	for _, entity := range receipt.Entities {
		var row api.PhysicalDiskSnapshot
		if err := db.Where("id = ?", entity.SnapshotID).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.ReportSHA256 != sha || entity.ReportSHA256 != sha || row.InputSHA256 != entity.InputSHA256 || !row.AvailableAt.Equal(entity.AvailableAt) {
			t.Fatal("receipt differs from committed snapshot")
		}
	}
	if !sort.SliceIsSorted(receipt.Entities, func(i, j int) bool { return receipt.Entities[i].EntityID < receipt.Entities[j].EntityID }) {
		t.Fatal("receipt entity order changed")
	}
	duplicate, err := s.PersistReportWithBinding(context.Background(), report, sha, binding)
	if err != nil || duplicate.SnapshotsCreated != 0 || duplicate.Duplicate != 2 {
		t.Fatalf("duplicate receipt: %+v %v", duplicate, err)
	}
	// Successful preflight cannot authorize a later, changed roster.
	if err := db.Model(&api.DiskObservationScope{}).Where("scope_id = ?", binding.ScopeID).Update("roster_version", 2).Error; err != nil {
		t.Fatal(err)
	}
	report.Rows[0].InputSHA256 = Digest([]byte("after-preflight-version-change"))
	failed, err := s.PersistReportWithBinding(context.Background(), report, sha, binding)
	if err == nil || failed.Verified || len(failed.Entities) != 0 {
		t.Fatal("stale version persisted after preflight")
	}
	var count int64
	db.Model(&api.PhysicalDiskSnapshot{}).Count(&count)
	if count != 4 {
		t.Fatalf("stale version wrote rows: %d", count)
	}
}

func TestObservationBindingRejectsIdentitySetAndRollbackDeadline(t *testing.T) {
	for _, test := range []string{"changed-active-set", "changed-inventory", "wrong-node", "duplicate-entity", "cancel-during-write"} {
		t.Run(test, func(t *testing.T) {
			db, s := installationTestDB(t)
			a, b := installationTestSnapshot(t, db, "reject-A"), installationTestSnapshot(t, db, "reject-B")
			installationTestApply(t, s, installationTestManifest(a, b))
			binding := observationTestBinding(t, db, "fixture-roster")
			if err := s.CheckObservationBinding(context.Background(), binding); err != nil {
				t.Fatal(err)
			}
			report := observationTestReport(a, b)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch test {
			case "changed-active-set":
				db.Model(&api.DiskInstallation{}).Where("entity_id = ?", a.EntityID).Update("state", "superseded")
			case "changed-inventory":
				db.Model(&api.DiskObservationScope{}).Where("scope_id = ?", binding.ScopeID).Update("asset_identity_sha256", Digest([]byte("new-host")))
			case "wrong-node":
				report.Rows[1].Node = "different-node"
			case "duplicate-entity":
				report.Rows[1] = report.Rows[0]
			case "cancel-during-write":
				name := "test:cancel-observation-create"
				if err := db.Callback().Create().After("gorm:create").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "physical_disk_snapshots" {
						cancel()
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer db.Callback().Create().Remove(name)
			}
			if receipt, err := s.PersistReportWithBinding(ctx, report, Digest([]byte("report")), binding); err == nil || receipt.Verified {
				t.Fatal("invalid/cancelled batch accepted")
			}
			var count int64
			if err := db.Model(&api.PhysicalDiskSnapshot{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 2 {
				t.Fatalf("failed batch wrote partial snapshots: %d", count)
			}
		})
	}
}

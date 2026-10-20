package diskhealth

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"atlas/pkg/api"
)

// This is the field-shaped migration case: six historical identities plus six
// replacement identities are twelve evidence entities, but only six installs.
func TestDiskInstallationSixNewIdentitiesDoNotJoinOrReactivateOldSix(t *testing.T) {
	db, service := newDiskServiceTestDB(t)
	service.now = func() time.Time { return cutoff }
	old := make([]Report, 0, 6)
	newIDs := make([]string, 0, 6)
	members := make([]InstallationReconcileMember, 0, 6)
	for i := 0; i < 6; i++ {
		a := installationViewReport(t, fmt.Sprintf("old-six-%d", i), cutoff.Add(-72*time.Hour), false)
		b := installationViewReport(t, fmt.Sprintf("new-six-%d", i), cutoff.Add(-10*time.Minute), false)
		old = append(old, a)
		for _, report := range []Report{a, b} {
			if _, err := service.PersistReport(report, ""); err != nil {
				t.Fatal(err)
			}
		}
		var snapshot api.PhysicalDiskSnapshot
		if err := db.Where("entity_id = ? AND current = ?", b.Rows[0].EntityID, true).First(&snapshot).Error; err != nil {
			t.Fatal(err)
		}
		newIDs = append(newIDs, snapshot.EntityID)
		members = append(members, InstallationReconcileMember{EntityID: snapshot.EntityID, SnapshotKey: snapshot.SnapshotKey, InputSHA256: snapshot.InputSHA256})
		if a.Rows[0].EntityID == b.Rows[0].EntityID {
			t.Fatal("replacement identity reused old entity")
		}
	}
	m := InstallationReconcileManifest{Schema: InstallationReconcileSchema, ScopeID: "fixture-six-new", AssetKey: "fixture:six-replacement-host", HostGeneration: "host-1", Node: old[0].Rows[0].Node, SnapshotScope: old[0].Rows[0].Scope, SnapshotGeneration: old[0].Rows[0].Generation, Disposition: "test", HostIdentityEvidenceSHA256: Digest([]byte("SYNTHETIC-SIX-DISK-HOST")), Actor: "six-disk-regression", ObservedAt: cutoff.Add(-2 * time.Minute), AvailableAt: cutoff.Add(-time.Minute), Complete: true, EnumerationBoundary: "six explicit synthetic replacement devices", ExpectedEntityIDs: newIDs, KnownInstallationIDs: []string{}, Members: members, Closures: []InstallationReconcileClosure{}, Failures: []InstallationReconcileFailure{}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ReconcileInstallations(m, Digest(raw))
	if err != nil || result.OpenedCount != 6 || result.ClosedCount != 0 {
		t.Fatalf("register new six: %+v %v", result, err)
	}
	for _, report := range old {
		if _, err := service.PersistReport(report, ""); err != nil {
			t.Fatal(err)
		}
	}
	active, err := service.ListDisks(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertInstallationViewCounts(t, active.Summary, "active", 6, 6, 0, 6, 1)
	if active.Total != 6 || len(active.Data) != 6 {
		t.Fatal("old six contaminated installed denominator")
	}
	for _, row := range active.Data {
		history, err := service.History(row.EntityID, 100, 0)
		if err != nil || history.Total != 1 || len(history.Rows) != 1 {
			t.Fatalf("new identity history includes old disk: %v", err)
		}
		for _, window := range history.Windows {
			if window.CoverageStatus != "collecting" || window.SampleCount != 1 {
				t.Fatal("old and new identities fabricated a trend")
			}
		}
		for _, before := range old {
			if row.EntityID == before.Rows[0].EntityID {
				t.Fatal("old report reactivated an unregistered identity")
			}
		}
	}
	all, err := service.ListDisks(ListOptions{View: "all"})
	if err != nil || all.Total != 12 {
		t.Fatalf("old evidence was lost: total=%d err=%v", all.Total, err)
	}
}

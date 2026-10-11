package nodeaccess

import (
	"testing"
	"time"
)

func TestHistoricalPlanUsesSharedCommandRegistry(t *testing.T) {
	at := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	controller := 31
	for _, commands := range [][]AcceptanceRequest{
		{{ID: "disk.smart", Protocol: "ata", Device: "/dev/sdb"}},
		{{ID: "disk.smart", Protocol: "nvme", Device: "/dev/nvme1n1"}},
		{{ID: "node.kernel"}, {ID: "node.boot_id"}, {ID: "node.memory"}, {ID: "node.storage"}, {ID: "memory.edac.counters", Controller: &controller}, {ID: "memory.edac.driver", Controller: &controller}},
	} {
		plan, err := PlanAcceptance(AcceptanceScope{Schema: "hardware-evidence-scope-v1", ScopeID: "fixture", Node: "192.0.2.1", Username: "fixture", CredentialProfileID: "fixture", IssuedAt: at.Add(-time.Minute), ExpiresAt: at.Add(time.Minute), Commands: commands}, at)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateAcceptancePlan(plan, at); err != nil {
			t.Fatal(err)
		}
		if err := ValidateAcceptancePlan(plan, at.Add(2*time.Minute)); err == nil {
			t.Fatal("expired historical capture accepted")
		}
		plan.Commands[0].Argv = append(plan.Commands[0].Argv, "--unsafe")
		if err := ValidateAcceptancePlan(plan, at); err == nil {
			t.Fatal("unregistered argv accepted")
		}
	}
}

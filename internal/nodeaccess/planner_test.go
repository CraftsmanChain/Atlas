package nodeaccess

import (
	"errors"
	"testing"
	"time"

	"atlas/pkg/config"
)

func TestPlannerBuildsPreviewForManagedNodeAndReadyCommands(t *testing.T) {
	db := connectivityTestDB(t)
	access := NewService(config.NodeAccessConfig{SkillID: SkillID, SkillVersion: SkillVersion, MaxCommandsPerNode: 3}, nil)
	planner := NewPlanner(db, access)
	now := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	planner.now = func() time.Time { return now }
	plan, err := planner.Build("10.114.4.25", []string{"node.identity", "gpu.snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "preview_only" || plan.ExecutionEnabled || !plan.NoCommandExecuted ||
		len(plan.Commands) != 2 || plan.ExpiresAt.Sub(plan.CreatedAt) != 15*time.Minute {
		t.Fatalf("unexpected plan: %#v", plan)
	}
}

func TestPlannerRejectsUnmanagedUnsafeParameterizedAndDuplicateCommands(t *testing.T) {
	db := connectivityTestDB(t)
	access := NewService(config.NodeAccessConfig{MaxCommandsPerNode: 3}, nil)
	planner := NewPlanner(db, access)
	tests := []struct {
		name     string
		node     string
		commands []string
		target   error
	}{
		{name: "unmanaged", node: "10.114.4.99", commands: []string{"node.identity"}, target: ErrNodeNotManaged},
		{name: "approval", node: "10.114.4.25", commands: []string{"maintenance.node_reboot"}, target: ErrPlanCommandNotReady},
		{name: "parameters", node: "10.114.4.25", commands: []string{"logs.kernel_window"}, target: ErrPlanCommandNotReady},
		{name: "duplicate", node: "10.114.4.25", commands: []string{"node.identity", "node.identity"}, target: ErrPlanCommandDuplicated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := planner.Build(test.node, test.commands); !errors.Is(err, test.target) {
				t.Fatalf("expected %v, got %v", test.target, err)
			}
		})
	}
}

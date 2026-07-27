package nodeaccess

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"atlas/pkg/storage"
)

var (
	ErrPlanCommandsRequired  = errors.New("at least one registered command is required")
	ErrPlanCommandLimit      = errors.New("command count exceeds the per-node budget")
	ErrPlanCommandUnknown    = errors.New("unknown registered command")
	ErrPlanCommandNotReady   = errors.New("registered command requires bounded parameters or human approval")
	ErrPlanCommandDuplicated = errors.New("duplicate registered command")
)

type Planner struct {
	db     *storage.DB
	access *Service
	now    func() time.Time
}

func NewPlanner(db *storage.DB, access *Service) *Planner {
	return &Planner{db: db, access: access, now: time.Now}
}

func (p *Planner) Build(node string, commandIDs []string) (*ExecutionPlan, error) {
	node = strings.TrimSpace(node)
	if err := validateManagedNode(p.db, node); err != nil {
		return nil, err
	}
	if len(commandIDs) == 0 {
		return nil, ErrPlanCommandsRequired
	}
	overview := p.access.Overview()
	if len(commandIDs) > overview.Budget.MaxCommandsPerNode {
		return nil, ErrPlanCommandLimit
	}
	available := make(map[string]CommandDefinition)
	for _, definition := range commandCatalog() {
		available[definition.ID] = definition
	}
	commands := make([]CommandDefinition, 0, len(commandIDs))
	seen := make(map[string]bool, len(commandIDs))
	for _, rawID := range commandIDs {
		id := strings.TrimSpace(rawID)
		definition, ok := available[id]
		if !ok {
			return nil, ErrPlanCommandUnknown
		}
		if seen[id] {
			return nil, ErrPlanCommandDuplicated
		}
		if definition.ApprovalClass != "read_only" || definition.PlanningStatus != "ready" {
			return nil, ErrPlanCommandNotReady
		}
		seen[id] = true
		commands = append(commands, definition)
	}
	createdAt := p.now()
	expiresAt := createdAt.Add(15 * time.Minute)
	digest := sha256.Sum256([]byte(node + "|" + strings.Join(commandIDs, ",") + "|" + createdAt.UTC().Format(time.RFC3339Nano)))
	return &ExecutionPlan{
		PlanID:  "plan-" + hex.EncodeToString(digest[:8]),
		SkillID: firstNonEmpty(overview.SkillID, SkillID), SkillVersion: firstNonEmpty(overview.SkillVersion, SkillVersion),
		NodeIP: node, Status: "preview_only", ApprovalClass: "read_only",
		Commands: commands, Budget: overview.Budget, CreatedAt: createdAt, ExpiresAt: expiresAt,
		ExecutionEnabled: false, NoCommandExecuted: true,
	}, nil
}

package nodeaccess

import (
	"context"
	"time"
)

const (
	SkillID      = "atlas-node-evidence"
	SkillVersion = "v0.5.1"
)

type Text struct {
	ZH string `json:"zh"`
	EN string `json:"en"`
}

type Budget struct {
	ConnectTimeoutSeconds int `json:"connect_timeout_seconds"`
	CommandTimeoutSeconds int `json:"command_timeout_seconds"`
	MaxOutputBytes        int `json:"max_output_bytes"`
	MaxConcurrentNodes    int `json:"max_concurrent_nodes"`
	MaxCommandsPerNode    int `json:"max_commands_per_node"`
	MaxLogLines           int `json:"max_log_lines"`
	DefaultWindowMinutes  int `json:"default_window_minutes"`
}

type CommandDefinition struct {
	ID             string `json:"id"`
	Category       string `json:"category"`
	ApprovalClass  string `json:"approval_class"`
	PlanningStatus string `json:"planning_status"`
	CollectionMode string `json:"collection_mode"`
	Purpose        Text   `json:"purpose"`
	Preview        string `json:"preview"`
}

type SkillDefinition struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Class   string `json:"class"`
	Status  string `json:"status"`
	Purpose Text   `json:"purpose"`
}

type AlertEvidencePolicy struct {
	Category          string   `json:"category"`
	IssueTypes        []string `json:"issue_types"`
	Semantics         string   `json:"semantics"`
	CollectionTrigger string   `json:"collection_trigger"`
	Purpose           Text     `json:"purpose"`
}

type CollectionStatusSummary struct {
	WaitingRecovery int `json:"waiting_recovery"`
	Completed       int `json:"completed"`
	Partial         int `json:"partial"`
	Failed          int `json:"failed"`
}

type CredentialProfileStatus struct {
	ID              string `json:"id"`
	Priority        int    `json:"priority"`
	Username        string `json:"username_masked"`
	AuthType        string `json:"auth_type"`
	SecretProvider  string `json:"secret_provider"`
	Enabled         bool   `json:"enabled"`
	SecretAvailable bool   `json:"secret_available"`
	Status          string `json:"status"`
}

type Overview struct {
	SkillID             string                    `json:"skill_id"`
	SkillVersion        string                    `json:"skill_version"`
	Status              string                    `json:"status"`
	Enabled             bool                      `json:"enabled"`
	ExecutionEnabled    bool                      `json:"execution_enabled"`
	NoArbitraryShell    bool                      `json:"no_arbitrary_shell"`
	NoChangeExecuted    bool                      `json:"no_change_executed"`
	EncryptionReady     bool                      `json:"encryption_ready"`
	ManagementReady     bool                      `json:"management_ready"`
	SecureWriteOnly     bool                      `json:"secure_write_only"`
	InsecureHTTPAllowed bool                      `json:"insecure_http_allowed"`
	ConnectivityEnabled bool                      `json:"connectivity_check_enabled"`
	KnownHostsReady     bool                      `json:"known_hosts_ready"`
	DefaultReadOnlyMode bool                      `json:"default_read_only_collection"`
	Budget              Budget                    `json:"budget"`
	CredentialProfiles  []CredentialProfileStatus `json:"credential_profiles"`
	Skills              []SkillDefinition         `json:"skills"`
	Commands            []CommandDefinition       `json:"commands"`
	AlertPolicies       []AlertEvidencePolicy     `json:"alert_evidence_policies"`
	CollectionSummary   CollectionStatusSummary   `json:"collection_summary"`
	Boundaries          []Text                    `json:"boundaries"`
	GeneratedAt         time.Time                 `json:"generated_at"`
}

type CredentialAttempt struct {
	ProfileID string `json:"profile_id"`
	Outcome   string `json:"outcome"`
}

type AuthenticationResult struct {
	Node                  string              `json:"node"`
	Status                string              `json:"status"`
	CredentialProfileID   string              `json:"credential_profile_id,omitempty"`
	Attempts              []CredentialAttempt `json:"attempts"`
	AlertRequired         bool                `json:"alert_required"`
	NoCredentialDisclosed bool                `json:"no_credential_disclosed"`
}

type ReadOnlyCommand struct {
	ID             string
	Kind           string
	Command        string
	MaxOutputBytes int
}

type CommandOutcome struct {
	CommandID string
	Kind      string
	Status    string
	Output    string
	Truncated bool
}

type ExecutionResult struct {
	Node                  string
	Status                string
	CredentialProfileID   string
	Attempts              []CredentialAttempt
	Outcomes              []CommandOutcome
	NoCredentialDisclosed bool
}

type ReadOnlyExecutor interface {
	Execute(ctx context.Context, node, username, authType string, secret []byte, commands []ReadOnlyCommand, timeout time.Duration) ([]CommandOutcome, error)
}

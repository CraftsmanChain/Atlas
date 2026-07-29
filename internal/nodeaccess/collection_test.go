package nodeaccess

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
)

type executorFunc func(context.Context, string, string, string, []byte, []ReadOnlyCommand, time.Duration) ([]CommandOutcome, error)

func (f executorFunc) Execute(
	ctx context.Context,
	node, username, authType string,
	secret []byte,
	commands []ReadOnlyCommand,
	timeout time.Duration,
) ([]CommandOutcome, error) {
	return f(ctx, node, username, authType, secret, commands, timeout)
}

func TestCollectionUsesRegisteredCommandsAndPersistsSanitizedEvidence(t *testing.T) {
	db := connectivityTestDB(t)
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	event := api.GPUFaultEvent{
		EpisodeKey: "GPU-A:xid:1", Source: "health_rule", State: "open",
		NodeIP: "10.114.4.25", GPUUUID: "GPU-A", GPUIndex: 3,
		RuleCode: "xid_critical", Domain: "stability", Severity: "critical",
		FirstObservedAt: now.Add(-time.Hour), LastObservedAt: now,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	access := NewService(config.NodeAccessConfig{
		Enabled: true, CommandTimeout: "3s", MaxOutputBytes: 12 * 1024, MaxCommandsPerNode: 3,
		CredentialProfiles: []config.NodeCredentialProfile{
			{ID: "node-a", Priority: 10, Username: "atlas", AuthType: "password", SecretRef: "env:A", Enabled: true},
		},
	}, mapResolver{"env:A": "credential-secret"})
	executor := executorFunc(func(
		_ context.Context, node, username, authType string, secret []byte,
		commands []ReadOnlyCommand, timeout time.Duration,
	) ([]CommandOutcome, error) {
		if node != event.NodeIP || username != "atlas" || authType != "password" || string(secret) != "credential-secret" {
			t.Fatal("unexpected execution identity")
		}
		if timeout != 3*time.Second || len(commands) != 3 {
			t.Fatalf("unexpected execution budget timeout=%s commands=%d", timeout, len(commands))
		}
		wantIDs := []string{"node.identity", "gpu.snapshot", "logs.kernel_window"}
		for index, command := range commands {
			if command.ID != wantIDs[index] || command.MaxOutputBytes != 4096 {
				t.Fatalf("unexpected registered command: %#v", command)
			}
		}
		return []CommandOutcome{
			{CommandID: commands[0].ID, Kind: commands[0].Kind, Status: "completed", Output: "node-a\npassword=should-not-persist"},
			{CommandID: commands[1].ID, Kind: commands[1].Kind, Status: "completed", Output: "3, GPU-A, NVIDIA H100"},
			{CommandID: commands[2].ID, Kind: commands[2].Kind, Status: "completed", Output: "NVRM: Xid 79", Truncated: true},
		}, nil
	})
	collector := NewCollectionService(db, access, executor, 1)
	collector.now = func() time.Time { now = now.Add(time.Second); return now }
	collection, err := collector.Collect(context.Background(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if collection.Status != "completed" || collection.CommandCount != 3 ||
		!collection.OutputTruncated || !collection.ReadOnly || !collection.NoCredentialDisclosed {
		t.Fatalf("unexpected collection: %#v", collection)
	}
	if strings.Contains(collection.Records[0].Output, "should-not-persist") ||
		!strings.Contains(collection.Records[0].Output, "[REDACTED]") {
		t.Fatalf("sensitive output was not redacted: %q", collection.Records[0].Output)
	}
	var stored api.NodeEvidenceCollection
	if err := db.Preload("Records").First(&stored, collection.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.CredentialProfileID != "node-a" || len(stored.Records) != 3 {
		t.Fatalf("unexpected stored collection: %#v", stored)
	}
}

func TestExecuteReadOnlyRotatesRejectedCredentialsAndEnforcesBudget(t *testing.T) {
	service := NewService(config.NodeAccessConfig{
		CommandTimeout: "2s", MaxCommandsPerNode: 1,
		CredentialProfiles: []config.NodeCredentialProfile{
			{ID: "a", Priority: 10, Username: "first", AuthType: "password", SecretRef: "env:A", Enabled: true},
			{ID: "b", Priority: 20, Username: "second", AuthType: "password", SecretRef: "env:B", Enabled: true},
		},
	}, mapResolver{"env:A": "wrong", "env:B": "right"})
	var users []string
	executor := executorFunc(func(
		_ context.Context, _, username, _ string, _ []byte,
		commands []ReadOnlyCommand, _ time.Duration,
	) ([]CommandOutcome, error) {
		users = append(users, username)
		if username == "first" {
			return nil, ErrAuthenticationRejected
		}
		return []CommandOutcome{{CommandID: commands[0].ID, Status: "completed"}}, nil
	})
	result := service.ExecuteReadOnly(context.Background(), "10.114.4.25", []ReadOnlyCommand{{ID: "node.identity"}}, executor)
	if result.Status != "completed" || result.CredentialProfileID != "b" ||
		len(users) != 2 || users[0] != "first" || users[1] != "second" {
		t.Fatalf("unexpected rotation result=%#v users=%#v", result, users)
	}
	rejected := service.ExecuteReadOnly(context.Background(), "10.114.4.25", []ReadOnlyCommand{{ID: "a"}, {ID: "b"}}, executor)
	if rejected.Status != "command_budget_rejected" {
		t.Fatalf("expected command budget rejection, got %#v", rejected)
	}
}

func TestCollectionRejectsUnmanagedEventNodeBeforeExecution(t *testing.T) {
	db := connectivityTestDB(t)
	event := api.GPUFaultEvent{
		EpisodeKey: "retired:xid", NodeIP: "10.114.4.99", RuleCode: "xid",
		FirstObservedAt: time.Now(), LastObservedAt: time.Now(),
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	called := false
	access := NewService(config.NodeAccessConfig{Enabled: true}, mapResolver{})
	collector := NewCollectionService(db, access, executorFunc(func(
		context.Context, string, string, string, []byte, []ReadOnlyCommand, time.Duration,
	) ([]CommandOutcome, error) {
		called = true
		return nil, errors.New("must not execute")
	}), 1)
	if _, err := collector.Collect(context.Background(), event.ID); !errors.Is(err, ErrNodeNotManaged) {
		t.Fatalf("expected unmanaged node rejection, got %v", err)
	}
	if called {
		t.Fatal("executor must not run for unmanaged nodes")
	}
}

func TestPendingCollectionRunsOnceForOpenManagedEvents(t *testing.T) {
	db := connectivityTestDB(t)
	now := time.Now()
	openEvent := api.GPUFaultEvent{
		EpisodeKey: "open", State: "open", NodeIP: "10.114.4.25",
		FirstObservedAt: now, LastObservedAt: now,
	}
	recoveredEvent := api.GPUFaultEvent{
		EpisodeKey: "recovered", State: "recovered", NodeIP: "10.114.4.25",
		FirstObservedAt: now, LastObservedAt: now,
	}
	if err := db.Create(&openEvent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&recoveredEvent).Error; err != nil {
		t.Fatal(err)
	}
	access := NewService(config.NodeAccessConfig{
		Enabled: true, MaxCommandsPerNode: 3,
		CredentialProfiles: []config.NodeCredentialProfile{
			{ID: "a", Username: "atlas", AuthType: "password", SecretRef: "env:A", Enabled: true},
		},
	}, mapResolver{"env:A": "secret"})
	calls := 0
	collector := NewCollectionService(db, access, executorFunc(func(
		_ context.Context, _ string, _ string, _ string, _ []byte,
		commands []ReadOnlyCommand, _ time.Duration,
	) ([]CommandOutcome, error) {
		calls++
		outcomes := make([]CommandOutcome, 0, len(commands))
		for _, command := range commands {
			outcomes = append(outcomes, CommandOutcome{
				CommandID: command.ID, Kind: command.Kind, Status: "completed", Output: "ok",
			})
		}
		return outcomes, nil
	}), 1)
	collector.collectPending(context.Background(), 10)
	collector.collectPending(context.Background(), 10)
	if calls != 1 {
		t.Fatalf("open event must be collected once, got %d calls", calls)
	}
	var collections []api.NodeEvidenceCollection
	if err := db.Find(&collections).Error; err != nil {
		t.Fatal(err)
	}
	if len(collections) != 1 || collections[0].FaultEventID != openEvent.ID {
		t.Fatalf("unexpected pending collections: %#v", collections)
	}
}

func TestOfflineNodeCollectionWaitsForRecovery(t *testing.T) {
	db := connectivityTestDB(t)
	now := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	if err := db.Model(&api.GPUNode{}).Where("node_ip = ?", "10.114.4.25").Update("state", "offline").Error; err != nil {
		t.Fatal(err)
	}
	issue := api.PlatformIssue{
		IssueKey: "node_state:10.114.4.25", Category: "availability", IssueType: "node_state",
		NodeIP: "10.114.4.25", DetectionSource: "inventory_node", DetectionState: "active",
		Status: "open", FirstDetectedAt: now.Add(-time.Hour), LastDetectedAt: now,
	}
	if err := db.Create(&issue).Error; err != nil {
		t.Fatal(err)
	}
	access := NewService(config.NodeAccessConfig{
		Enabled: true, MaxCommandsPerNode: 4,
		CredentialProfiles: []config.NodeCredentialProfile{
			{ID: "a", Username: "atlas", AuthType: "password", SecretRef: "env:A", Enabled: true},
		},
	}, mapResolver{"env:A": "secret"})
	calls := 0
	collector := NewCollectionService(db, access, executorFunc(func(
		_ context.Context, _ string, _ string, _ string, _ []byte,
		commands []ReadOnlyCommand, _ time.Duration,
	) ([]CommandOutcome, error) {
		calls++
		outcomes := make([]CommandOutcome, 0, len(commands))
		for _, command := range commands {
			outcomes = append(outcomes, CommandOutcome{
				CommandID: command.ID, Kind: command.Kind, Status: "completed", Output: "ok",
			})
		}
		return outcomes, nil
	}), 1)

	collector.collectPending(context.Background(), 10)
	if calls != 0 {
		t.Fatalf("offline node must not be contacted, got %d calls", calls)
	}
	var waiting api.NodeEvidenceCollection
	if err := db.Where("platform_issue_id = ?", issue.ID).First(&waiting).Error; err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "waiting_recovery" || waiting.Trigger != "after_recovery" {
		t.Fatalf("unexpected waiting collection: %#v", waiting)
	}

	recoveredAt := now.Add(5 * time.Minute)
	if err := db.Model(&issue).Updates(map[string]any{
		"detection_state": "cleared", "status": "resolved", "source_recovered_at": &recoveredAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&api.GPUNode{}).Where("node_ip = ?", issue.NodeIP).Update("state", "up").Error; err != nil {
		t.Fatal(err)
	}
	collector.collectPending(context.Background(), 10)
	if calls != 1 {
		t.Fatalf("recovered node must be collected once, got %d calls", calls)
	}
	if err := db.Preload("Records").First(&waiting, waiting.ID).Error; err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "completed" || len(waiting.Records) != 4 {
		t.Fatalf("unexpected recovered collection: %#v", waiting)
	}
	wantIDs := []string{"node.identity", "node.recovery_context", "gpu.snapshot", "logs.kernel_window"}
	for index, record := range waiting.Records {
		if record.CommandID != wantIDs[index] {
			t.Fatalf("unexpected recovery command order: %#v", waiting.Records)
		}
	}
}

func TestFailedCollectionRetryCreatesNewAuditRecord(t *testing.T) {
	db := connectivityTestDB(t)
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	event := api.GPUFaultEvent{
		EpisodeKey: "retry:xid", State: "open", NodeIP: "10.114.4.25",
		RuleCode: "xid_critical", FirstObservedAt: now.Add(-time.Hour), LastObservedAt: now,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	previous := api.NodeEvidenceCollection{
		FaultEventID: event.ID, NodeIP: event.NodeIP, Trigger: "immediate",
		Status: "failed", FailureCode: "credential_exhausted",
		NoCredentialDisclosed: true, ReadOnly: true, StartedAt: now, FinishedAt: now,
	}
	if err := db.Create(&previous).Error; err != nil {
		t.Fatal(err)
	}
	access := NewService(config.NodeAccessConfig{
		Enabled: true, MaxCommandsPerNode: 3,
		CredentialProfiles: []config.NodeCredentialProfile{
			{ID: "working", Username: "atlas", AuthType: "password", SecretRef: "env:A", Enabled: true},
		},
	}, mapResolver{"env:A": "secret"})
	collector := NewCollectionService(db, access, executorFunc(func(
		_ context.Context, _ string, _ string, _ string, _ []byte,
		commands []ReadOnlyCommand, _ time.Duration,
	) ([]CommandOutcome, error) {
		outcomes := make([]CommandOutcome, 0, len(commands))
		for _, command := range commands {
			outcomes = append(outcomes, CommandOutcome{
				CommandID: command.ID, Kind: command.Kind, Status: "completed", Output: "ok",
			})
		}
		return outcomes, nil
	}), 1)

	retry, err := collector.Retry(context.Background(), previous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID == previous.ID || retry.RetryOfCollectionID != previous.ID ||
		retry.Trigger != "manual_retry" || retry.Status != "completed" ||
		retry.FaultEventID != event.ID || len(retry.Records) != 3 {
		t.Fatalf("unexpected retry collection: %#v", retry)
	}
	var storedPrevious api.NodeEvidenceCollection
	if err := db.First(&storedPrevious, previous.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedPrevious.Status != "failed" || storedPrevious.FailureCode != "credential_exhausted" {
		t.Fatalf("retry must preserve the original audit record: %#v", storedPrevious)
	}
	if _, err := collector.Retry(context.Background(), retry.ID); !errors.Is(err, ErrEvidenceCollectionNotRetryable) {
		t.Fatalf("completed collection must not be retryable, got %v", err)
	}
}

func TestFailedRecoveryCollectionRetryUsesRecoveryCommands(t *testing.T) {
	db := connectivityTestDB(t)
	now := time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC)
	recoveredAt := now.Add(10 * time.Minute)
	issue := api.PlatformIssue{
		IssueKey: "node_state:retry", Category: "availability", IssueType: "node_state",
		NodeIP: "10.114.4.25", DetectionSource: "inventory_node", DetectionState: "cleared",
		Status: "resolved", FirstDetectedAt: now.Add(-time.Hour), LastDetectedAt: now,
		SourceRecoveredAt: &recoveredAt,
	}
	if err := db.Create(&issue).Error; err != nil {
		t.Fatal(err)
	}
	previous := api.NodeEvidenceCollection{
		PlatformIssueID: issue.ID, NodeIP: issue.NodeIP, Trigger: "after_recovery",
		Status: "failed", FailureCode: "credential_exhausted",
		NoCredentialDisclosed: true, ReadOnly: true, StartedAt: now, FinishedAt: now,
	}
	if err := db.Create(&previous).Error; err != nil {
		t.Fatal(err)
	}
	access := NewService(config.NodeAccessConfig{
		Enabled: true, MaxCommandsPerNode: 4,
		CredentialProfiles: []config.NodeCredentialProfile{
			{ID: "working", Username: "atlas", AuthType: "password", SecretRef: "env:A", Enabled: true},
		},
	}, mapResolver{"env:A": "secret"})
	var commandIDs []string
	collector := NewCollectionService(db, access, executorFunc(func(
		_ context.Context, _ string, _ string, _ string, _ []byte,
		commands []ReadOnlyCommand, _ time.Duration,
	) ([]CommandOutcome, error) {
		outcomes := make([]CommandOutcome, 0, len(commands))
		for _, command := range commands {
			commandIDs = append(commandIDs, command.ID)
			outcomes = append(outcomes, CommandOutcome{
				CommandID: command.ID, Kind: command.Kind, Status: "completed", Output: "ok",
			})
		}
		return outcomes, nil
	}), 1)

	retry, err := collector.Retry(context.Background(), previous.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"node.identity", "node.recovery_context", "gpu.snapshot", "logs.kernel_window"}
	if retry.PlatformIssueID != issue.ID || retry.RetryOfCollectionID != previous.ID ||
		len(commandIDs) != len(want) {
		t.Fatalf("unexpected recovery retry: %#v commands=%#v", retry, commandIDs)
	}
	for index := range want {
		if commandIDs[index] != want[index] {
			t.Fatalf("unexpected recovery retry commands: %#v", commandIDs)
		}
	}
}

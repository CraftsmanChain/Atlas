package nodeaccess

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

var (
	ErrFaultEventNotFound             = errors.New("fault event not found")
	ErrEvidenceCollectionNotFound     = errors.New("node evidence collection not found")
	ErrEvidenceCollectionNotRetryable = errors.New("node evidence collection is not retryable")
)

type CollectionService struct {
	db       *storage.DB
	access   *Service
	executor ReadOnlyExecutor
	slots    chan struct{}
	now      func() time.Time
}

func NewCollectionService(db *storage.DB, access *Service, executor ReadOnlyExecutor, maxConcurrentNodes int) *CollectionService {
	if maxConcurrentNodes <= 0 {
		maxConcurrentNodes = 2
	}
	return &CollectionService{
		db: db, access: access, executor: executor,
		slots: make(chan struct{}, maxConcurrentNodes), now: time.Now,
	}
}

func (s *CollectionService) Enabled() bool {
	return s != nil && s.access != nil && s.executor != nil && s.access.cfg.Enabled
}

func (s *CollectionService) Collect(ctx context.Context, eventID uint) (*api.NodeEvidenceCollection, error) {
	if !s.Enabled() {
		return nil, ErrConnectivityUnavailable
	}
	var event api.GPUFaultEvent
	if err := s.db.First(&event, eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrFaultEventNotFound
		}
		return nil, err
	}
	collection := &api.NodeEvidenceCollection{
		FaultEventID: event.ID, NodeIP: event.NodeIP, Trigger: "immediate",
	}
	return s.executeCollection(ctx, collection, collectionCommands(event, s.access.cfg.MaxOutputBytes))
}

// Retry creates a new audit envelope instead of mutating the previous
// collection. Only failed or partial collections can be retried, and the
// original event/condition determines the same registered read-only commands.
func (s *CollectionService) Retry(ctx context.Context, collectionID uint) (*api.NodeEvidenceCollection, error) {
	if !s.Enabled() {
		return nil, ErrConnectivityUnavailable
	}
	var previous api.NodeEvidenceCollection
	if err := s.db.First(&previous, collectionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEvidenceCollectionNotFound
		}
		return nil, err
	}
	if previous.Status != "failed" && previous.Status != "partial" {
		return nil, ErrEvidenceCollectionNotRetryable
	}
	retry := &api.NodeEvidenceCollection{
		FaultEventID: previous.FaultEventID, PlatformIssueID: previous.PlatformIssueID,
		RetryOfCollectionID: previous.ID, NodeIP: previous.NodeIP, Trigger: "manual_retry",
	}
	switch {
	case previous.FaultEventID > 0:
		var event api.GPUFaultEvent
		if err := s.db.First(&event, previous.FaultEventID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrFaultEventNotFound
			}
			return nil, err
		}
		return s.executeCollection(ctx, retry, collectionCommands(event, s.access.cfg.MaxOutputBytes))
	case previous.PlatformIssueID > 0:
		var issue api.PlatformIssue
		if err := s.db.First(&issue, previous.PlatformIssueID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrEvidenceCollectionNotFound
			}
			return nil, err
		}
		return s.executeCollection(ctx, retry, recoveryCollectionCommands(issue, s.access.cfg.MaxOutputBytes))
	default:
		return nil, ErrEvidenceCollectionNotRetryable
	}
}

func (s *CollectionService) executeCollection(ctx context.Context, collection *api.NodeEvidenceCollection, commands []ReadOnlyCommand) (*api.NodeEvidenceCollection, error) {
	if _, err := managedNode(s.db, collection.NodeIP); err != nil {
		return nil, err
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	startedAt := s.now()
	result := s.access.ExecuteReadOnly(ctx, collection.NodeIP, commands, s.executor)
	finishedAt := s.now()
	collection.Status = result.Status
	collection.CredentialProfileID = result.CredentialProfileID
	collection.NoCredentialDisclosed = result.NoCredentialDisclosed
	collection.ReadOnly = true
	collection.StartedAt = startedAt
	collection.FinishedAt = finishedAt
	collection.CommandCount = 0
	collection.OutputBytes = 0
	collection.OutputTruncated = false
	collection.FailureCode = ""
	records := make([]api.NodeEvidenceRecord, 0, len(result.Outcomes))
	allCompleted := len(result.Outcomes) > 0
	for _, outcome := range result.Outcomes {
		output := sanitizeEvidenceOutput(outcome.Output)
		record := api.NodeEvidenceRecord{
			CommandID: outcome.CommandID, Kind: outcome.Kind, Status: outcome.Status,
			Output: output, OutputBytes: len([]byte(output)), Truncated: outcome.Truncated,
			ObservedAt: finishedAt,
		}
		records = append(records, record)
		collection.CommandCount++
		collection.OutputBytes += record.OutputBytes
		collection.OutputTruncated = collection.OutputTruncated || record.Truncated
		allCompleted = allCompleted && record.Status == "completed"
	}
	if result.Status != "completed" {
		collection.Status, collection.FailureCode = "failed", result.Status
	} else if !allCompleted {
		collection.Status = "partial"
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if collection.ID == 0 {
			if err := tx.Create(collection).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Model(collection).Updates(map[string]any{
				"status": collection.Status, "credential_profile_id": collection.CredentialProfileID,
				"command_count": collection.CommandCount, "output_bytes": collection.OutputBytes,
				"output_truncated": collection.OutputTruncated, "failure_code": collection.FailureCode,
				"no_credential_disclosed": collection.NoCredentialDisclosed, "read_only": collection.ReadOnly,
				"started_at": collection.StartedAt, "finished_at": collection.FinishedAt,
			}).Error; err != nil {
				return err
			}
		}
		for index := range records {
			records[index].CollectionID = collection.ID
			if err := tx.Create(&records[index]).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	collection.Records = records
	return collection, nil
}

func (s *CollectionService) List(eventID uint, limit int) ([]api.NodeEvidenceCollection, error) {
	if limit <= 0 || limit > 101 {
		limit = 20
	}
	query := s.db.Preload("Records", func(tx *gorm.DB) *gorm.DB {
		return tx.Select(
			"id", "collection_id", "command_id", "kind", "status",
			"output_bytes", "truncated", "observed_at", "created_at",
		).Order("id ASC")
	}).Order("id DESC").Limit(limit)
	if eventID > 0 {
		query = query.Where("fault_event_id = ?", eventID)
	}
	var rows []api.NodeEvidenceCollection
	return rows, query.Find(&rows).Error
}

// Run performs bounded default collection for new, open fault events. A
// collection audit row, including a failed one, suppresses automatic retries;
// operators can make an explicit retry through the protected API.
func (s *CollectionService) Run(ctx context.Context, interval time.Duration) {
	if !s.Enabled() {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	s.collectPending(ctx, 10)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.collectPending(ctx, 10)
		}
	}
}

func (s *CollectionService) collectPending(ctx context.Context, limit int) {
	s.queueOfflineRecoveryCollections(limit)
	s.collectRecoveredNodes(ctx, limit)

	var eventIDs []uint
	err := s.db.Model(&api.GPUFaultEvent{}).
		Select("gpu_fault_events.id").
		Joins("JOIN gpu_nodes ON gpu_nodes.node_ip = gpu_fault_events.node_ip AND gpu_nodes.lifecycle <> ?", "retired").
		Where("gpu_fault_events.state = ?", "open").
		Where("NOT EXISTS (?)",
			s.db.Model(&api.NodeEvidenceCollection{}).
				Select("1").
				Where("node_evidence_collections.fault_event_id = gpu_fault_events.id"),
		).
		Order("gpu_fault_events.id ASC").
		Limit(limit).
		Find(&eventIDs).Error
	if err != nil {
		return
	}
	for _, eventID := range eventIDs {
		if ctx.Err() != nil {
			return
		}
		_, _ = s.Collect(ctx, eventID)
	}
}

// queueOfflineRecoveryCollections models an offline node as a durable
// condition. Collection waits until inventory observes the same managed node
// as up again, because SSH evidence is unavailable while the condition is
// active.
func (s *CollectionService) queueOfflineRecoveryCollections(limit int) {
	var issues []api.PlatformIssue
	err := s.db.Model(&api.PlatformIssue{}).
		Joins("JOIN gpu_nodes ON gpu_nodes.node_ip = platform_issues.node_ip").
		Where("platform_issues.detection_source = ? AND platform_issues.issue_type = ?", "inventory_node", "node_state").
		Where("platform_issues.detection_state = ? AND gpu_nodes.state = ? AND gpu_nodes.lifecycle <> ?", "active", "offline", "retired").
		Where("NOT EXISTS (?)",
			s.db.Model(&api.NodeEvidenceCollection{}).
				Select("1").
				Where("node_evidence_collections.platform_issue_id = platform_issues.id"),
		).
		Order("platform_issues.id ASC").
		Limit(limit).
		Find(&issues).Error
	if err != nil {
		return
	}
	for _, issue := range issues {
		now := s.now()
		_ = s.db.Create(&api.NodeEvidenceCollection{
			PlatformIssueID: issue.ID, NodeIP: issue.NodeIP, Trigger: "after_recovery",
			Status: "waiting_recovery", FailureCode: "node_offline",
			NoCredentialDisclosed: true, ReadOnly: true, StartedAt: now, FinishedAt: now,
		}).Error
	}
}

func (s *CollectionService) collectRecoveredNodes(ctx context.Context, limit int) {
	var pending []api.NodeEvidenceCollection
	err := s.db.Model(&api.NodeEvidenceCollection{}).
		Joins("JOIN platform_issues ON platform_issues.id = node_evidence_collections.platform_issue_id").
		Joins("JOIN gpu_nodes ON gpu_nodes.node_ip = node_evidence_collections.node_ip").
		Where("node_evidence_collections.status = ? AND node_evidence_collections.trigger = ?", "waiting_recovery", "after_recovery").
		Where("platform_issues.detection_state = ? AND gpu_nodes.state = ? AND gpu_nodes.lifecycle <> ?", "cleared", "up", "retired").
		Order("node_evidence_collections.id ASC").
		Limit(limit).
		Find(&pending).Error
	if err != nil {
		return
	}
	for index := range pending {
		if ctx.Err() != nil {
			return
		}
		var issue api.PlatformIssue
		if err := s.db.First(&issue, pending[index].PlatformIssueID).Error; err != nil {
			continue
		}
		_, _ = s.executeCollection(ctx, &pending[index], recoveryCollectionCommands(issue, s.access.cfg.MaxOutputBytes))
	}
}

func collectionCommands(event api.GPUFaultEvent, totalOutputBytes int) []ReadOnlyCommand {
	if totalOutputBytes <= 0 {
		totalOutputBytes = 1024 * 1024
	}
	perCommand := totalOutputBytes / 3
	if perCommand < 4096 {
		perCommand = 4096
	}
	start := event.FirstObservedAt.Add(-30 * time.Minute)
	end := event.LastObservedAt.Add(30 * time.Minute)
	if end.Sub(start) > 2*time.Hour {
		start = end.Add(-2 * time.Hour)
	}
	return []ReadOnlyCommand{
		{
			ID: "node.identity", Kind: "node_inventory", MaxOutputBytes: perCommand,
			Command: "hostname; uname -srmo; sed -n '1,8p' /etc/os-release",
		},
		{
			ID: "gpu.snapshot", Kind: "gpu_snapshot", MaxOutputBytes: perCommand,
			Command: "nvidia-smi --query-gpu=index,uuid,name,pci.bus_id,driver_version,temperature.gpu,power.draw,power.limit,clocks.current.sm,clocks.current.memory,utilization.gpu,utilization.memory,pstate --format=csv,noheader,nounits",
		},
		{
			ID: "logs.kernel_window", Kind: "node_log", MaxOutputBytes: perCommand,
			Command: fmt.Sprintf(
				"journalctl -k --since=@%d --until=@%d --no-pager -n 2000",
				start.Unix(), end.Unix(),
			),
		},
	}
}

func recoveryCollectionCommands(issue api.PlatformIssue, totalOutputBytes int) []ReadOnlyCommand {
	if totalOutputBytes <= 0 {
		totalOutputBytes = 1024 * 1024
	}
	perCommand := totalOutputBytes / 4
	if perCommand < 4096 {
		perCommand = 4096
	}
	start := issue.FirstDetectedAt.Add(-30 * time.Minute)
	end := issue.LastDetectedAt.Add(30 * time.Minute)
	if issue.SourceRecoveredAt != nil {
		end = issue.SourceRecoveredAt.Add(30 * time.Minute)
	}
	if end.Sub(start) > 24*time.Hour {
		start = end.Add(-24 * time.Hour)
	}
	return []ReadOnlyCommand{
		{
			ID: "node.identity", Kind: "node_inventory", MaxOutputBytes: perCommand,
			Command: "hostname; uname -srmo; sed -n '1,8p' /etc/os-release",
		},
		{
			ID: "node.recovery_context", Kind: "recovery_context", MaxOutputBytes: perCommand,
			Command: "uptime -s; who -b; journalctl --list-boots --no-pager | tail -n 10",
		},
		{
			ID: "gpu.snapshot", Kind: "gpu_snapshot", MaxOutputBytes: perCommand,
			Command: "nvidia-smi --query-gpu=index,uuid,name,pci.bus_id,driver_version,temperature.gpu,pstate --format=csv,noheader,nounits",
		},
		{
			ID: "logs.kernel_window", Kind: "node_log", MaxOutputBytes: perCommand,
			Command: fmt.Sprintf(
				"journalctl -k --since=@%d --until=@%d --no-pager -n 2000",
				start.Unix(), end.Unix(),
			),
		},
	}
}

var sensitiveEvidencePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(password|passwd|token|secret|authorization)\s*[:=]\s*\S+`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

func sanitizeEvidenceOutput(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (unicode.IsPrint(r) && r != '\r') {
			return r
		}
		return -1
	}, value)
	for _, pattern := range sensitiveEvidencePatterns {
		value = pattern.ReplaceAllString(value, "[REDACTED]")
	}
	return strings.TrimSpace(value)
}

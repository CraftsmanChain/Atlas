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

var ErrFaultEventNotFound = errors.New("fault event not found")

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
	if _, err := managedNode(s.db, event.NodeIP); err != nil {
		return nil, err
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	startedAt := s.now()
	commands := collectionCommands(event, s.access.cfg.MaxOutputBytes)
	result := s.access.ExecuteReadOnly(ctx, event.NodeIP, commands, s.executor)
	finishedAt := s.now()
	collection := &api.NodeEvidenceCollection{
		FaultEventID: event.ID, NodeIP: event.NodeIP, Status: result.Status,
		CredentialProfileID:   result.CredentialProfileID,
		NoCredentialDisclosed: result.NoCredentialDisclosed, ReadOnly: true,
		StartedAt: startedAt, FinishedAt: finishedAt,
	}
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
		if err := tx.Create(collection).Error; err != nil {
			return err
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
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	query := s.db.Preload("Records").Order("id DESC").Limit(limit)
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
	timeFormat := "2006-01-02 15:04:05Z07:00"
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
				"journalctl -k --since %q --until %q --no-pager -n 2000",
				start.Format(timeFormat), end.Format(timeFormat),
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

package prediction

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

type LabelSummary struct {
	Total             int            `json:"total"`
	Confirmed         int            `json:"confirmed"`
	StrongProxy       int            `json:"strong_proxy"`
	WeakProxy         int            `json:"weak_proxy"`
	Excluded          int            `json:"excluded"`
	AffectedGPUs      int            `json:"affected_gpus"`
	ByEventType       map[string]int `json:"by_event_type"`
	ByModel           map[string]int `json:"by_model"`
	Materialized      bool           `json:"materialized"`
	LatestAvailableAt *time.Time     `json:"latest_available_at,omitempty"`
}

func (s *Service) RunLabelSync(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		if err := s.SyncLabels(); err != nil && ctx.Err() == nil {
			log.Printf("prediction label reconciliation failed: %v", err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// SyncLabels materializes deterministic GPU rule episodes as proxy labels and
// upgrades them only when a complete, training-eligible hardware resolution
// confirms the event. It is idempotent and never creates hardware labels from
// availability, access, inventory, or telemetry-quality issues.
func (s *Service) SyncLabels() error {
	s.labelMu.Lock()
	defer s.labelMu.Unlock()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var events []api.GPUFaultEvent
		if err := tx.Order("id").Find(&events).Error; err != nil {
			return err
		}
		for _, event := range events {
			if err := materializeProxyLabel(tx, event); err != nil {
				return err
			}
		}
		return upgradeConfirmedLabels(tx)
	})
}

func materializeProxyLabel(tx *gorm.DB, event api.GPUFaultEvent) error {
	labelKey := fmt.Sprintf("gpu-fault-event:%d", event.ID)
	entityKey := strings.TrimSpace(event.GPUUUID)
	if entityKey == "" {
		entityKey = fmt.Sprintf("gpu-asset:%d", event.GPUAssetID)
	}
	availableAt := event.CreatedAt
	if availableAt.IsZero() {
		availableAt = event.FirstObservedAt
	}
	values := map[string]any{
		"hardware_class": "gpu", "entity_type": "gpu", "entity_key": entityKey,
		"gpu_asset_id": event.GPUAssetID, "gpu_uuid": event.GPUUUID, "node_ip": event.NodeIP,
		"model_name": event.ModelName, "event_type": event.RuleCode, "rule_version": event.RuleVersion,
		"label_value": 1, "source_type": "gpu_fault_event", "source_record_id": event.ID,
		"label_contract_version": LabelContractVersion, "occurred_at": event.FirstObservedAt,
		"available_at": availableAt, "excluded": false, "exclusion_reason": "",
		"quality_tier": proxyQualityTier(event.RuleCode), "confirmation_resolution_id": 0,
		"confirmed_at": nil,
	}
	var label api.FailureLabel
	err := tx.Where("label_key = ?", labelKey).First(&label).Error
	if err == gorm.ErrRecordNotFound {
		label = api.FailureLabel{
			LabelKey: labelKey, HardwareClass: "gpu", EntityType: "gpu", EntityKey: entityKey,
			GPUAssetID: event.GPUAssetID, GPUUUID: event.GPUUUID, NodeIP: event.NodeIP,
			ModelName: event.ModelName, EventType: event.RuleCode, RuleVersion: event.RuleVersion,
			LabelValue: 1, QualityTier: proxyQualityTier(event.RuleCode),
			SourceType: "gpu_fault_event", SourceRecordID: event.ID,
			LabelContractVersion: LabelContractVersion, OccurredAt: event.FirstObservedAt,
			AvailableAt: availableAt,
		}
		return tx.Create(&label).Error
	}
	if err != nil {
		return err
	}
	return tx.Model(&label).Updates(values).Error
}

func upgradeConfirmedLabels(tx *gorm.DB) error {
	var issues []api.PlatformIssue
	if err := tx.Where("category = ? AND source_record_id > 0", "hardware_fault").Find(&issues).Error; err != nil {
		return err
	}
	if len(issues) == 0 {
		return nil
	}
	issueByID := make(map[uint]api.PlatformIssue, len(issues))
	issueIDs := make([]uint, 0, len(issues))
	for _, issue := range issues {
		issueByID[issue.ID] = issue
		issueIDs = append(issueIDs, issue.ID)
	}
	var resolutions []api.IssueResolution
	if err := tx.Where("issue_id IN ?", issueIDs).Order("issue_id, created_at, id").Find(&resolutions).Error; err != nil {
		return err
	}
	latestByIssue := make(map[uint]api.IssueResolution, len(issueIDs))
	for _, resolution := range resolutions {
		latestByIssue[resolution.IssueID] = resolution
	}
	for issueID, resolution := range latestByIssue {
		if resolution.Status != "resolved" || !resolution.TrainingEligible || !completeHardwareConfirmation(resolution) {
			continue
		}
		issue := issueByID[issueID]
		var label api.FailureLabel
		if err := tx.Where("label_key = ?", fmt.Sprintf("gpu-fault-event:%d", issue.SourceRecordID)).First(&label).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			return err
		}
		confirmedAt := resolution.CreatedAt
		if err := tx.Model(&label).Updates(map[string]any{
			"quality_tier": "confirmed", "confirmation_resolution_id": resolution.ID,
			"confirmed_at": &confirmedAt, "available_at": confirmedAt,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func completeHardwareConfirmation(resolution api.IssueResolution) bool {
	return strings.TrimSpace(resolution.RootCause) != "" &&
		strings.TrimSpace(resolution.Solution) != "" &&
		strings.TrimSpace(resolution.ResolutionProcess) != "" &&
		strings.TrimSpace(resolution.Result) != ""
}

func proxyQualityTier(ruleCode string) string {
	switch strings.ToLower(strings.TrimSpace(ruleCode)) {
	case "uncorrectable_remapped_rows", "ecc_dbe", "xid_critical", "xid_repeated":
		return "strong_proxy"
	default:
		return "weak_proxy"
	}
}

func (s *Service) Labels(limit int) (LabelSummary, []api.FailureLabel, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var all []api.FailureLabel
	if err := s.db.Order("available_at DESC, id DESC").Find(&all).Error; err != nil {
		return LabelSummary{}, nil, err
	}
	summary := LabelSummary{
		Total: len(all), ByEventType: map[string]int{}, ByModel: map[string]int{},
		Materialized: len(all) > 0,
	}
	gpus := map[string]struct{}{}
	for _, label := range all {
		switch label.QualityTier {
		case "confirmed":
			summary.Confirmed++
		case "strong_proxy":
			summary.StrongProxy++
		case "weak_proxy":
			summary.WeakProxy++
		}
		if label.Excluded {
			summary.Excluded++
		}
		summary.ByEventType[label.EventType]++
		summary.ByModel[label.ModelName]++
		if label.EntityKey != "" {
			gpus[label.EntityKey] = struct{}{}
		}
		if summary.LatestAvailableAt == nil || label.AvailableAt.After(*summary.LatestAvailableAt) {
			availableAt := label.AvailableAt
			summary.LatestAvailableAt = &availableAt
		}
	}
	summary.AffectedGPUs = len(gpus)
	rows := all
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return summary, rows, nil
}

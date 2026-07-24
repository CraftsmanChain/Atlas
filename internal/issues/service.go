package issues

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

type Service struct {
	db  *storage.DB
	now func() time.Time
	mu  sync.Mutex
}

type detectedIssue struct {
	key, category, issueType, title, description string
	entityType, entityKey, nodeIP, gpuUUID       string
	severity, source, detectionState             string
	sourceRecordID                               uint
	firstDetectedAt, lastDetectedAt              time.Time
	sourceRecoveredAt                            *time.Time
}

func NewService(db *storage.DB) *Service { return &Service{db: db, now: time.Now} }

func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		if err := s.SyncDetectedIssues(); err != nil && ctx.Err() == nil {
			log.Printf("issue reconciliation failed: %v", err)
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

// SyncDetectedIssues materializes current platform findings into a durable,
// normalized issue ledger. Manual resolution history is never overwritten.
func (s *Service) SyncDetectedIssues() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		activeBySource := map[string]map[string]bool{
			"inventory_node": {}, "inventory_gpu": {}, "collector_target": {}, "health_score": {}, "telemetry_continuity": {}, "source_consistency": {},
		}

		var nodes []api.GPUNode
		if err := tx.Where("lifecycle <> ? AND state <> ?", "retired", "up").Find(&nodes).Error; err != nil {
			return err
		}
		for _, node := range nodes {
			severity := map[string]string{"offline": "critical", "degraded": "warning", "unknown": "attention"}[node.State]
			if severity == "" {
				severity = "attention"
			}
			item := detectedIssue{key: "node_state:" + node.NodeIP, category: "availability", issueType: "node_state", title: fmt.Sprintf("Node %s is %s", node.NodeIP, node.State), description: fmt.Sprintf("Observed GPU node state is %s (%d/%d GPUs visible)", node.State, node.ObservedGPUCount, node.ExpectedGPUCount), entityType: "node", entityKey: node.NodeIP, nodeIP: node.NodeIP, severity: severity, source: "inventory_node", detectionState: "active", sourceRecordID: node.ID, lastDetectedAt: fallbackTime(node.LastSyncedAt, now)}
			activeBySource[item.source][item.key] = true
			if err := upsertDetected(tx, item, now); err != nil {
				return err
			}
		}

		activeNodeIDs := tx.Model(&api.GPUNode{}).Select("id").Where("lifecycle <> ?", "retired")
		var assets []api.GPUAsset
		if err := tx.Where("node_id IN (?) AND state <> ?", activeNodeIDs, "active").Find(&assets).Error; err != nil {
			return err
		}
		for _, asset := range assets {
			severity := "attention"
			if asset.State == "conflict" {
				severity = "critical"
			} else if asset.State == "uuid_unknown" {
				severity = "warning"
			}
			item := detectedIssue{key: "gpu_state:" + asset.AssetKey, category: "inventory", issueType: "gpu_state", title: fmt.Sprintf("GPU %s is %s", asset.AssetKey, asset.State), description: fmt.Sprintf("GPU slot state=%s, sample_state=%s, UUID=%s", asset.State, asset.SampleState, fallback(asset.CurrentUUID, "unknown")), entityType: "gpu", entityKey: asset.AssetKey, nodeIP: asset.NodeIP, gpuUUID: asset.CurrentUUID, severity: severity, source: "inventory_gpu", detectionState: "active", sourceRecordID: asset.ID, lastDetectedAt: fallbackTime(asset.LastSyncedAt, now)}
			activeBySource[item.source][item.key] = true
			if err := upsertDetected(tx, item, now); err != nil {
				return err
			}
		}

		activeNodeIPs := tx.Model(&api.GPUNode{}).Select("node_ip").Where("lifecycle <> ?", "retired")
		var targets []api.CollectorTarget
		if err := tx.Where("node_ip IN (?) AND health <> ?", activeNodeIPs, "up").Find(&targets).Error; err != nil {
			return err
		}
		for _, target := range targets {
			severity := "warning"
			if target.Suppressed {
				severity = "info"
			} else if target.Health == "missing" {
				severity = "attention"
			}
			item := detectedIssue{key: "target_health:" + target.TargetKey, category: "data_quality", issueType: "target_health", title: fmt.Sprintf("%s target on %s is %s", target.Job, target.NodeIP, target.Health), description: strings.TrimSpace(strings.Join([]string{target.ReasonCode, target.LastError}, ": ")), entityType: "target", entityKey: target.TargetKey, nodeIP: target.NodeIP, severity: severity, source: "collector_target", detectionState: "active", sourceRecordID: target.ID, lastDetectedAt: fallbackTime(target.LastSyncedAt, now)}
			activeBySource[item.source][item.key] = true
			if err := upsertDetected(tx, item, now); err != nil {
				return err
			}
		}

		var unknownScores []api.GPUHealthScore
		if err := tx.Where("current = ? AND (score IS NULL OR level = ?)", true, "unknown").Find(&unknownScores).Error; err != nil {
			return err
		}
		for _, score := range unknownScores {
			key := fmt.Sprintf("health_unknown:%d", score.GPUAssetID)
			item := detectedIssue{key: key, category: "data_quality", issueType: "health_score_unknown", title: fmt.Sprintf("GPU health is unknown on %s GPU %d", score.NodeIP, score.GPUIndex), description: fmt.Sprintf("Health score cannot be calculated; data confidence=%s", score.DataConfidence), entityType: "gpu", entityKey: fmt.Sprintf("%d", score.GPUAssetID), nodeIP: score.NodeIP, gpuUUID: score.GPUUUID, severity: "attention", source: "health_score", detectionState: "active", sourceRecordID: score.ID, lastDetectedAt: fallbackTime(score.EvaluatedAt, now)}
			activeBySource[item.source][item.key] = true
			if err := upsertDetected(tx, item, now); err != nil {
				return err
			}
		}

		var currentSnapshots []api.GPUFeatureSnapshot
		if err := tx.Model(&api.GPUFeatureSnapshot{}).
			Joins("JOIN gpu_health_scores ON gpu_health_scores.feature_snapshot_id = gpu_feature_snapshots.id").
			Where("gpu_health_scores.current = ?", true).
			Find(&currentSnapshots).Error; err != nil {
			return err
		}
		for _, snapshot := range currentSnapshots {
			presence, hasPresence := snapshot.Metrics["gpu_metric_presence_ratio_1h"]
			age, hasAge := snapshot.Metrics["gpu_metric_sample_age_seconds"]
			if !hasPresence || !hasAge || (presence >= 95 && age <= 60) {
				continue
			}
			severity := "attention"
			if presence < 80 || age > 300 {
				severity = "warning"
			}
			key := fmt.Sprintf("telemetry_continuity:%d", snapshot.GPUAssetID)
			item := detectedIssue{key: key, category: "data_quality", issueType: "metric_continuity", title: fmt.Sprintf("GPU telemetry continuity degraded on %s GPU %d", snapshot.NodeIP, snapshot.GPUIndex), description: fmt.Sprintf("DCGM GPU metric presence_1h=%.1f%%, sample_age=%.1fs", presence, age), entityType: "gpu", entityKey: fmt.Sprintf("%d", snapshot.GPUAssetID), nodeIP: snapshot.NodeIP, gpuUUID: snapshot.GPUUUID, severity: severity, source: "telemetry_continuity", detectionState: "active", sourceRecordID: snapshot.ID, lastDetectedAt: fallbackTime(snapshot.ObservedAt, now)}
			activeBySource[item.source][item.key] = true
			if err := upsertDetected(tx, item, now); err != nil {
				return err
			}
		}

		var faultEvents []api.GPUFaultEvent
		if err := tx.Find(&faultEvents).Error; err != nil {
			return err
		}
		for _, event := range faultEvents {
			detectionState := "active"
			if event.State == "recovered" {
				detectionState = "cleared"
			}
			item := detectedIssue{key: fmt.Sprintf("fault_event:%d", event.ID), category: "hardware_fault", issueType: event.RuleCode, title: fmt.Sprintf("%s on %s GPU %d", event.RuleCode, event.NodeIP, event.GPUIndex), description: event.Evidence, entityType: "gpu", entityKey: event.EpisodeKey, nodeIP: event.NodeIP, gpuUUID: event.GPUUUID, severity: event.Severity, source: "health_rule", detectionState: detectionState, sourceRecordID: event.ID, firstDetectedAt: event.FirstObservedAt, lastDetectedAt: event.LastObservedAt, sourceRecoveredAt: event.RecoveredAt}
			if err := upsertDetected(tx, item, now); err != nil {
				return err
			}
		}

		for source, keys := range activeBySource {
			if err := clearMissing(tx, source, keys, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func upsertDetected(tx *gorm.DB, item detectedIssue, now time.Time) error {
	var issue api.PlatformIssue
	err := tx.Where("issue_key = ?", item.key).First(&issue).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	if err == gorm.ErrRecordNotFound {
		status := "open"
		var resolvedAt *time.Time
		if item.detectionState == "cleared" {
			status, resolvedAt = "resolved", item.sourceRecoveredAt
		}
		first := item.firstDetectedAt
		if first.IsZero() {
			first = item.lastDetectedAt
		}
		if first.IsZero() {
			first = now
		}
		last := item.lastDetectedAt
		if last.IsZero() {
			last = now
		}
		issue = api.PlatformIssue{IssueKey: item.key, Category: item.category, IssueType: item.issueType, Title: item.title, Description: item.description, EntityType: item.entityType, EntityKey: item.entityKey, NodeIP: item.nodeIP, GPUUUID: item.gpuUUID, Severity: item.severity, Status: status, DetectionState: item.detectionState, DetectionSource: item.source, SourceRecordID: item.sourceRecordID, FirstDetectedAt: first, LastDetectedAt: last, SourceRecoveredAt: item.sourceRecoveredAt, ResolvedAt: resolvedAt}
		return tx.Create(&issue).Error
	}
	updates := map[string]any{"category": item.category, "issue_type": item.issueType, "title": item.title, "description": item.description, "entity_type": item.entityType, "entity_key": item.entityKey, "node_ip": item.nodeIP, "gpu_uuid": item.gpuUUID, "severity": item.severity, "detection_state": item.detectionState, "source_record_id": item.sourceRecordID, "last_detected_at": fallbackTime(item.lastDetectedAt, now), "source_recovered_at": item.sourceRecoveredAt}
	if issue.DetectionState == "cleared" && item.detectionState == "active" && issue.Status == "resolved" {
		updates["status"], updates["resolved_at"] = "open", nil
	}
	if item.detectionState == "cleared" && (issue.Status == "open" || issue.Status == "in_progress") {
		resolvedAt := item.sourceRecoveredAt
		if resolvedAt == nil {
			resolvedAt = &now
		}
		updates["status"], updates["resolved_at"] = "resolved", resolvedAt
	}
	return tx.Model(&issue).Updates(updates).Error
}

func clearMissing(tx *gorm.DB, source string, active map[string]bool, now time.Time) error {
	var issues []api.PlatformIssue
	if err := tx.Where("detection_source = ? AND detection_state = ?", source, "active").Find(&issues).Error; err != nil {
		return err
	}
	for _, issue := range issues {
		if active[issue.IssueKey] {
			continue
		}
		updates := map[string]any{"detection_state": "cleared", "source_recovered_at": &now}
		if issue.Status == "open" || issue.Status == "in_progress" {
			updates["status"], updates["resolved_at"] = "resolved", &now
		}
		if err := tx.Model(&issue).Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}

func fallback(value, fallbackValue string) string {
	if strings.TrimSpace(value) == "" {
		return fallbackValue
	}
	return value
}

func fallbackTime(value, fallbackValue time.Time) time.Time {
	if value.IsZero() {
		return fallbackValue
	}
	return value
}

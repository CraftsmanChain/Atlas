package inventory

import (
	"context"
	"fmt"
	"log"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"atlas/internal/platformconfig"
	"atlas/internal/prometheus"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type prometheusReader interface {
	BaseURL() string
	ActiveTargets(context.Context) ([]prometheus.Target, error)
	Query(context.Context, string) ([]prometheus.Sample, error)
}

type assetSource interface {
	Sync(context.Context) (*platformconfig.AssetSyncResult, error)
	LastGPUCatalog() (map[string]string, error)
}

type Service struct {
	db     *storage.DB
	prom   prometheusReader
	assets assetSource
	config config.InventoryConfig
	now    func() time.Time
}

type gpuSample struct {
	prometheus.Sample
	source string
}

const (
	TaskTargetStatus        = "target_status"
	TaskIdentityIncremental = "identity_incremental"
	TaskFullReconcile       = "full_reconcile"
)

func NewService(db *storage.DB, prom prometheusReader, cfg config.InventoryConfig) *Service {
	return &Service{db: db, prom: prom, config: cfg, now: time.Now}
}

func NewServiceWithAssets(db *storage.DB, prom prometheusReader, cfg config.InventoryConfig, assets assetSource) *Service {
	service := NewService(db, prom, cfg)
	service.assets = assets
	return service
}

// Run establishes a full baseline, then performs one combined monitoring
// reconciliation every interval. Timers are reset only after a run finishes,
// so a slow Prometheus query can never overlap or queue another observation.
func (s *Service) Run(ctx context.Context, monitoringInterval, fullInterval time.Duration) {
	if monitoringInterval <= 0 {
		monitoringInterval = 10 * time.Minute
	}
	if fullInterval <= 0 {
		fullInterval = 24 * time.Hour
	}
	s.syncAndLog(ctx, TaskFullReconcile)
	monitoringTimer := time.NewTimer(monitoringInterval)
	fullTimer := time.NewTimer(fullInterval)
	defer monitoringTimer.Stop()
	defer fullTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-monitoringTimer.C:
			s.syncAndLog(ctx, TaskIdentityIncremental)
			monitoringTimer.Reset(monitoringInterval)
		case <-fullTimer.C:
			s.syncAndLog(ctx, TaskFullReconcile)
			fullTimer.Reset(fullInterval)
		}
	}
}

func (s *Service) syncAndLog(ctx context.Context, taskType string) {
	var err error
	switch taskType {
	case TaskTargetStatus:
		_, err = s.SyncTargets(ctx)
	case TaskIdentityIncremental:
		_, err = s.SyncIdentity(ctx)
	default:
		_, err = s.SyncFull(ctx)
	}
	if err != nil && ctx.Err() == nil {
		log.Printf("inventory sync failed: task=%s error=%v", taskType, err)
	}
}

// Sync remains the compatibility entry point and performs a full reconciliation.
func (s *Service) Sync(ctx context.Context) (*api.InventorySyncRun, error) {
	return s.SyncFull(ctx)
}

func (s *Service) SyncTargets(ctx context.Context) (*api.InventorySyncRun, error) {
	now := s.now()
	run, err := s.startRun(now, TaskTargetStatus)
	if err != nil {
		return nil, err
	}
	targets, err := s.prom.ActiveTargets(ctx)
	if err != nil {
		return s.failRun(run, err)
	}
	catalog, err := s.loadGPUCatalog(ctx)
	if err != nil {
		return s.failRun(run, err)
	}
	nodeIPs, err := s.trackedNodeIPs(catalog)
	if err != nil {
		return s.failRun(run, err)
	}
	if len(nodeIPs) == 0 {
		return s.failRun(run, fmt.Errorf("target sync found zero tracked GPU nodes"))
	}
	changes, err := s.persistTargets(now, run.ID, nodeIPs, indexTargets(targets, s.config))
	if err != nil {
		return s.failRun(run, err)
	}
	run.NodeCount = len(nodeIPs)
	run.TargetCount = len(nodeIPs) * len(s.config.TargetJobs)
	run.ChangeCount = changes
	s.populateInventoryCounts(run)
	return s.finishRun(run)
}

func (s *Service) SyncIdentity(ctx context.Context) (*api.InventorySyncRun, error) {
	return s.syncInventory(ctx, TaskIdentityIncremental, false, false)
}

func (s *Service) SyncFull(ctx context.Context) (*api.InventorySyncRun, error) {
	return s.syncInventory(ctx, TaskFullReconcile, true, true)
}

func (s *Service) syncInventory(ctx context.Context, taskType string, recoverHistory, retireMissing bool) (*api.InventorySyncRun, error) {
	now := s.now()
	run, err := s.startRun(now, taskType)
	if err != nil {
		return nil, err
	}

	targets, err := s.prom.ActiveTargets(ctx)
	if err != nil {
		return s.failRun(run, err)
	}
	current, err := s.prom.Query(ctx, "DCGM_FI_DEV_GPU_UTIL")
	if err != nil {
		return s.failRun(run, err)
	}

	catalog, err := s.loadGPUCatalog(ctx)
	if err != nil {
		return s.failRun(run, err)
	}
	targetIndex := indexTargets(targets, s.config)
	nodeIPs := filterGPUNodeIPs(discoverNodes(targets, current, s.config), current, catalog)
	if len(nodeIPs) == 0 {
		return s.failRun(run, fmt.Errorf("inventory discovery returned zero GPU nodes"))
	}

	samples := make(map[string]gpuSample)
	mergeSamples(samples, current, "current", true)
	missingDCGM := make([]string, 0)
	for _, nodeIP := range nodeIPs {
		if targetIndex[targetKey("dcgm_exporter", nodeIP)].Health != "up" {
			missingDCGM = append(missingDCGM, nodeIP)
		}
	}
	if recoverHistory && len(missingDCGM) > 0 && strings.TrimSpace(s.config.HistoryWindow) != "" {
		parts := make([]string, 0, len(missingDCGM))
		for _, ip := range missingDCGM {
			// PromQL string literals consume one escaping layer before the
			// regex parser sees the pattern.
			parts = append(parts, strings.ReplaceAll(regexp.QuoteMeta(ip), `\`, `\\`))
		}
		query := fmt.Sprintf(`last_over_time(DCGM_FI_DEV_GPU_UTIL{instance=~"%s"}[%s])`, strings.Join(parts, "|"), s.config.HistoryWindow)
		history, historyErr := s.prom.Query(ctx, query)
		if historyErr != nil {
			log.Printf("inventory history recovery skipped: query=%q error=%v", query, historyErr)
		} else {
			mergeSamples(samples, history, "history", false)
		}
	}

	// A non-nil catalog is an authoritative asset snapshot (LXOP or the
	// configured asset file). Apply removals on every monitoring
	// reconciliation so state/IP changes do not linger until the daily full
	// reconciliation.
	retireMissing = retireMissing || catalog != nil
	changes, knownUUIDs, err := s.persistInventory(now, run.ID, nodeIPs, samples, targetIndex, catalog, retireMissing)
	if err != nil {
		return s.failRun(run, err)
	}
	targetChanges, targetErr := s.persistTargets(now, run.ID, nodeIPs, targetIndex)
	if targetErr != nil {
		return s.failRun(run, targetErr)
	}
	changes += targetChanges
	run.NodeCount = len(nodeIPs)
	run.GPUCount = len(nodeIPs) * s.config.ExpectedGPUCount
	run.KnownUUIDCount = knownUUIDs
	run.TargetCount = len(nodeIPs) * len(s.config.TargetJobs)
	run.ChangeCount = changes
	return s.finishRun(run)
}

func (s *Service) loadGPUCatalog(ctx context.Context) (map[string]string, error) {
	if s.assets == nil {
		return loadGPUCatalog(s.config.AssetFile)
	}
	result, err := s.assets.Sync(ctx)
	if err != nil {
		cached, cacheErr := s.assets.LastGPUCatalog()
		if cacheErr == nil && len(cached) > 0 {
			log.Printf("LXOP asset sync failed; using last successful snapshot: %v", err)
			return cached, nil
		}
		return nil, err
	}
	if result.Configured {
		if len(result.GPUCatalog) == 0 {
			return nil, fmt.Errorf("LXOP asset sync returned no active GPU nodes")
		}
		return result.GPUCatalog, nil
	}
	return loadGPUCatalog(s.config.AssetFile)
}

func (s *Service) startRun(now time.Time, taskType string) (*api.InventorySyncRun, error) {
	run := &api.InventorySyncRun{TaskType: taskType, Status: "running", Source: s.prom.BaseURL(), StartedAt: now}
	if err := s.db.Create(run).Error; err != nil {
		return nil, err
	}
	return run, nil
}

func (s *Service) failRun(run *api.InventorySyncRun, err error) (*api.InventorySyncRun, error) {
	finished := s.now()
	run.Status, run.FinishedAt, run.ErrorMessage = "failed", &finished, err.Error()
	_ = s.db.Save(run).Error
	return run, err
}

func (s *Service) finishRun(run *api.InventorySyncRun) (*api.InventorySyncRun, error) {
	finished := s.now()
	run.Status, run.FinishedAt = "success", &finished
	if err := s.db.Save(run).Error; err != nil {
		return nil, err
	}
	return run, nil
}

func (s *Service) populateInventoryCounts(run *api.InventorySyncRun) {
	activeNodeIDs := s.db.Model(&api.GPUNode{}).Select("id").Where("lifecycle <> ?", "retired")
	var gpuCount, knownUUIDCount int64
	s.db.Model(&api.GPUAsset{}).Where("node_id IN (?)", activeNodeIDs).Count(&gpuCount)
	s.db.Model(&api.GPUAsset{}).Where("node_id IN (?) AND current_uuid <> ''", activeNodeIDs).Count(&knownUUIDCount)
	run.GPUCount, run.KnownUUIDCount = int(gpuCount), int(knownUUIDCount)
}

func (s *Service) persistInventory(now time.Time, runID uint, nodeIPs []string, samples map[string]gpuSample, targets map[string]prometheus.Target, catalog map[string]string, retireMissing bool) (int, int, error) {
	changeCount, knownUUIDs := 0, 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// Keep excluded assets for audit/history, but remove them from the
		// default GPU domain exposed by APIs.
		if retireMissing && len(nodeIPs) > 0 {
			var retired []api.GPUNode
			if err := tx.Where("node_ip NOT IN ? AND lifecycle <> ?", nodeIPs, "retired").Find(&retired).Error; err != nil {
				return err
			}
			retiredNodeIDs := make([]uint, 0, len(retired))
			for _, node := range retired {
				retiredNodeIDs = append(retiredNodeIDs, node.ID)
				if err := tx.Model(&node).Update("lifecycle", "retired").Error; err != nil {
					return err
				}
				if err := s.createChange(tx, runID, now, "node_retired", node.NodeIP, node.NodeIP, node.Lifecycle, "retired"); err != nil {
					return err
				}
				changeCount++
			}
			if len(retiredNodeIDs) > 0 {
				retiredAssetIDs := tx.Model(&api.GPUAsset{}).Select("id").Where("node_id IN ?", retiredNodeIDs)
				if err := tx.Model(&api.GPUHealthScore{}).
					Where("gpu_asset_id IN (?) AND current = ?", retiredAssetIDs, true).
					Update("current", false).Error; err != nil {
					return err
				}
			}
		}
		for _, nodeIP := range nodeIPs {
			var existing api.GPUNode
			hasExisting := tx.Where("node_ip = ?", nodeIP).First(&existing).Error == nil
			node := existing
			oldState, oldLifecycle := node.State, node.Lifecycle
			if !hasExisting {
				node.FirstSeenAt = now
				node.Lifecycle = "discovered"
			}
			node.NodeIP = nodeIP
			if canonicalHostname := catalog[nodeIP]; canonicalHostname != "" {
				node.Hostname = canonicalHostname
			}
			node.BMCIP = mapPrefix(nodeIP, s.config.NodePrefix, s.config.BMCPrefix)
			node.ExpectedGPUCount = s.config.ExpectedGPUCount
			node.LastSyncedAt = now
			node.State = nodeState(nodeIP, targets, s.config)
			observed := 0
			var latestObserved time.Time
			for slot := 0; slot < s.config.ExpectedGPUCount; slot++ {
				if sample, ok := samples[slotKey(nodeIP, slot)]; ok {
					observed++
					if sample.Timestamp.After(latestObserved) {
						latestObserved = sample.Timestamp
					}
					if node.Hostname == "" {
						node.Hostname = sample.Metric["Hostname"]
					}
				}
			}
			node.ObservedGPUCount = observed
			if latestObserved.IsZero() || latestObserved.Unix() <= 0 {
				latestObserved = now
			}
			if node.State == "up" || observed > 0 {
				node.LastSeenAt = latestObserved
			}
			if node.Lifecycle == "retired" {
				node.Lifecycle = "active"
			}
			if node.Lifecycle == "discovered" && (node.State == "up" || observed > 0) {
				node.Lifecycle = "active"
			}
			if err := tx.Save(&node).Error; err != nil {
				return err
			}
			if !hasExisting {
				if err := s.createChange(tx, runID, now, "node_added", nodeIP, nodeIP, "", node.Lifecycle); err != nil {
					return err
				}
				changeCount++
			} else {
				if oldState != "" && oldState != node.State {
					if err := s.createChange(tx, runID, now, "node_state_changed", nodeIP, nodeIP, oldState, node.State); err != nil {
						return err
					}
					changeCount++
				}
				if oldLifecycle != "" && oldLifecycle != node.Lifecycle {
					if err := s.createChange(tx, runID, now, "node_lifecycle_changed", nodeIP, nodeIP, oldLifecycle, node.Lifecycle); err != nil {
						return err
					}
					changeCount++
				}
			}

			for slot := 0; slot < s.config.ExpectedGPUCount; slot++ {
				key := slotKey(nodeIP, slot)
				var asset api.GPUAsset
				hasAsset := tx.Where("asset_key = ?", key).First(&asset).Error == nil
				if !hasAsset {
					asset.FirstSeenAt = now
				}
				oldUUID, oldState := asset.CurrentUUID, asset.State
				asset.AssetKey, asset.NodeID, asset.NodeIP, asset.GPUIndex = key, node.ID, nodeIP, slot
				asset.LastSyncedAt = now
				if sample, ok := samples[key]; ok {
					metric := sample.Metric
					asset.CurrentUUID = metric["UUID"]
					asset.Device = metric["device"]
					asset.Model = metric["model"]
					asset.ModelName = metric["modelName"]
					asset.PCIBusID = metric["pci_bus_id"]
					asset.HostSerial = metric["sn"]
					asset.DriverVersion = metric["DCGM_FI_DRIVER_VERSION"]
					asset.SampleState = sample.source
					asset.LastSeenAt = sample.Timestamp
					if asset.LastSeenAt.IsZero() || asset.LastSeenAt.Unix() <= 0 {
						asset.LastSeenAt = now
					}
					if asset.CurrentUUID == "" {
						asset.State = "uuid_unknown"
					} else if sample.source == "history" {
						asset.State = "history_only"
					} else {
						asset.State = "active"
					}
				} else if asset.CurrentUUID != "" {
					asset.State, asset.SampleState = "history_only", "missing"
				} else {
					asset.State, asset.SampleState = "uuid_unknown", "missing"
				}
				if asset.CurrentUUID != "" {
					knownUUIDs++
				}
				if oldUUID != "" && asset.CurrentUUID != "" && oldUUID != asset.CurrentUUID {
					if err := s.createChange(tx, runID, now, "gpu_uuid_changed", nodeIP, key, oldUUID, asset.CurrentUUID); err != nil {
						return err
					}
					changeCount++
				}
				if hasAsset && oldState != "" && oldState != asset.State {
					if err := s.createChange(tx, runID, now, "gpu_state_changed", nodeIP, key, oldState, asset.State); err != nil {
						return err
					}
					changeCount++
				}
				if err := tx.Save(&asset).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	return changeCount, knownUUIDs, err
}

func (s *Service) persistTargets(now time.Time, runID uint, nodeIPs []string, targets map[string]prometheus.Target) (int, error) {
	changeCount := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, nodeIP := range nodeIPs {
			var node api.GPUNode
			hasNode := tx.Where("node_ip = ?", nodeIP).First(&node).Error == nil
			if hasNode {
				oldState := node.State
				newState := nodeState(nodeIP, targets, s.config)
				if err := tx.Model(&node).Updates(map[string]any{"state": newState, "target_synced_at": now}).Error; err != nil {
					return err
				}
				if oldState != "" && oldState != newState {
					if err := s.createChange(tx, runID, now, "node_state_changed", nodeIP, nodeIP, oldState, newState); err != nil {
						return err
					}
					changeCount++
				}
			}

			for _, job := range s.config.TargetJobs {
				targetIP := nodeIP
				if job == "ipmi_exporter" {
					targetIP = mapPrefix(nodeIP, s.config.NodePrefix, s.config.BMCPrefix)
				}
				t := targets[targetKey(job, targetIP)]
				health := strings.ToLower(strings.TrimSpace(t.Health))
				if health == "" {
					health = "missing"
				}
				assessment := assessTarget(job, nodeIP, health, t.LastError, targets)
				key := targetKey(job, nodeIP)
				var existing api.CollectorTarget
				hasExisting := tx.Where("target_key = ?", key).First(&existing).Error == nil
				record := api.CollectorTarget{TargetKey: key, Job: job, Instance: t.Labels["instance"], TargetIP: targetIP, NodeIP: nodeIP, Health: health, ReasonCode: assessment.reasonCode, Suppressed: assessment.suppressed, SuppressionReason: assessment.suppressionReason, LastError: t.LastError, ScrapeURL: t.ScrapeURL, LastSyncedAt: now}
				if !t.LastScrape.IsZero() {
					record.LastScrapeAt = &t.LastScrape
				}
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "target_key"}},
					DoUpdates: clause.AssignmentColumns([]string{"job", "instance", "target_ip", "node_ip", "health", "reason_code", "suppressed", "suppression_reason", "last_error", "scrape_url", "last_scrape_at", "last_synced_at", "updated_at"}),
				}).Create(&record).Error; err != nil {
					return err
				}
				if !hasExisting {
					if err := s.createChange(tx, runID, now, "target_added", nodeIP, key, "", health); err != nil {
						return err
					}
					changeCount++
				} else if existing.Health != health {
					if err := s.createChange(tx, runID, now, "target_state_changed", nodeIP, key, existing.Health, health); err != nil {
						return err
					}
					changeCount++
				} else if assessmentValue(existing.ReasonCode, existing.Suppressed, existing.SuppressionReason) != assessmentValue(record.ReasonCode, record.Suppressed, record.SuppressionReason) {
					if err := s.createChange(tx, runID, now, "target_classification_changed", nodeIP, key, assessmentValue(existing.ReasonCode, existing.Suppressed, existing.SuppressionReason), assessmentValue(record.ReasonCode, record.Suppressed, record.SuppressionReason)); err != nil {
						return err
					}
					changeCount++
				}
			}
		}
		return nil
	})
	return changeCount, err
}

func (s *Service) createChange(tx *gorm.DB, runID uint, now time.Time, eventType, nodeIP, assetKey, oldValue, newValue string) error {
	event := api.AssetChangeEvent{SyncRunID: runID, EventType: eventType, NodeIP: nodeIP, AssetKey: assetKey, OldValue: oldValue, NewValue: newValue, Source: s.prom.BaseURL(), CreatedAt: now}
	return tx.Create(&event).Error
}

func (s *Service) trackedNodeIPs(catalog map[string]string) ([]string, error) {
	if catalog != nil {
		return filterGPUNodeIPs(nil, nil, catalog), nil
	}
	var nodes []api.GPUNode
	if err := s.db.Where("lifecycle <> ?", "retired").Order("node_ip ASC").Find(&nodes).Error; err != nil {
		return nil, err
	}
	result := make([]string, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, node.NodeIP)
	}
	return result, nil
}

func filterGPUNodeIPs(nodeIPs []string, samples []prometheus.Sample, catalog map[string]string) []string {
	if catalog != nil {
		filtered := make([]string, 0, len(catalog))
		for nodeIP := range catalog {
			filtered = append(filtered, nodeIP)
		}
		sort.Slice(filtered, func(i, j int) bool {
			return bytesForIP(filtered[i]) < bytesForIP(filtered[j])
		})
		return filtered
	}
	telemetryNames := make(map[string]string)
	for _, sample := range samples {
		ip := sample.Metric["host_ip"]
		if ip == "" {
			ip = instanceIP(sample.Metric["instance"])
		}
		if hostname := strings.TrimSpace(sample.Metric["Hostname"]); hostname != "" {
			telemetryNames[ip] = hostname
		}
	}
	filtered := make([]string, 0, len(nodeIPs))
	for _, nodeIP := range nodeIPs {
		hostname := telemetryNames[nodeIP]
		if isGPUHostname(hostname) {
			filtered = append(filtered, nodeIP)
		}
	}
	return filtered
}

func discoverNodes(targets []prometheus.Target, samples []prometheus.Sample, cfg config.InventoryConfig) []string {
	set := make(map[string]struct{})
	for _, target := range targets {
		job, ip := target.Labels["job"], instanceIP(target.Labels["instance"])
		if job == "ipmi_exporter" && strings.HasPrefix(ip, cfg.BMCPrefix) && lastOctet(ip) >= cfg.BMCLastOctetMin {
			set[mapPrefix(ip, cfg.BMCPrefix, cfg.NodePrefix)] = struct{}{}
		} else if contains(cfg.TargetJobs, job) && strings.HasPrefix(ip, cfg.NodePrefix) {
			set[ip] = struct{}{}
		}
	}
	for _, sample := range samples {
		ip := sample.Metric["host_ip"]
		if ip == "" {
			ip = instanceIP(sample.Metric["instance"])
		}
		if strings.HasPrefix(ip, cfg.NodePrefix) {
			set[ip] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for ip := range set {
		result = append(result, ip)
	}
	sort.Slice(result, func(i, j int) bool {
		return bytesForIP(result[i]) < bytesForIP(result[j])
	})
	return result
}

func indexTargets(targets []prometheus.Target, cfg config.InventoryConfig) map[string]prometheus.Target {
	result := make(map[string]prometheus.Target)
	for _, target := range targets {
		job, ip := target.Labels["job"], instanceIP(target.Labels["instance"])
		if !contains(cfg.TargetJobs, job) {
			continue
		}
		result[targetKey(job, ip)] = target
	}
	return result
}

func mergeSamples(dst map[string]gpuSample, rows []prometheus.Sample, source string, overwrite bool) {
	for _, row := range rows {
		ip := row.Metric["host_ip"]
		if ip == "" {
			ip = instanceIP(row.Metric["instance"])
		}
		slot, err := strconv.Atoi(row.Metric["gpu"])
		if err != nil || ip == "" || row.Metric["UUID"] == "" {
			continue
		}
		key := slotKey(ip, slot)
		if _, exists := dst[key]; !exists || overwrite {
			dst[key] = gpuSample{Sample: row, source: source}
		}
	}
}

func nodeState(nodeIP string, targets map[string]prometheus.Target, cfg config.InventoryConfig) string {
	if strings.EqualFold(targets[targetKey("node_exporter", nodeIP)].Health, "up") {
		return "up"
	}
	if strings.EqualFold(targets[targetKey("dcgm_exporter", nodeIP)].Health, "up") || strings.EqualFold(targets[targetKey("gpu_exporter", nodeIP)].Health, "up") {
		return "degraded"
	}
	bmcIP := mapPrefix(nodeIP, cfg.NodePrefix, cfg.BMCPrefix)
	if strings.EqualFold(targets[targetKey("ipmi_exporter", bmcIP)].Health, "up") {
		return "degraded"
	}
	return "offline"
}

func slotKey(ip string, slot int) string { return fmt.Sprintf("%s:%d", ip, slot) }
func targetKey(job, ip string) string    { return job + "|" + ip }

func instanceIP(instance string) string {
	host, _, err := net.SplitHostPort(instance)
	if err == nil {
		return host
	}
	return instance
}

func mapPrefix(ip, from, to string) string {
	if strings.HasPrefix(ip, from) {
		return to + strings.TrimPrefix(ip, from)
	}
	return ""
}

func lastOctet(ip string) int {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return -1
	}
	value, _ := strconv.Atoi(parts[3])
	return value
}

func bytesForIP(value string) string {
	parsed := net.ParseIP(value).To4()
	if parsed == nil {
		return value
	}
	return fmt.Sprintf("%03d.%03d.%03d.%03d", parsed[0], parsed[1], parsed[2], parsed[3])
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

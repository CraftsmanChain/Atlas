package health

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"atlas/internal/features"
	"atlas/internal/prometheus"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

const CurrentRuleVersion = "gpu-health-v1.6.0"

type prometheusReader interface {
	BaseURL() string
	Query(context.Context, string) ([]prometheus.Sample, error)
}

type alertSink interface {
	Process(*api.AlertEvent) error
}

type Service struct {
	db               *storage.DB
	prom             prometheusReader
	config           config.HealthConfig
	historyRetention time.Duration
	now              func() time.Time
	alerts           alertSink
}

type metricSpec struct {
	key, query, source string
	priority           int
}

var metricSpecs = func() []metricSpec {
	catalogSpecs := features.HealthMetricSpecs()
	result := make([]metricSpec, 0, len(catalogSpecs))
	for _, spec := range catalogSpecs {
		result = append(result, metricSpec{key: spec.Key, query: spec.Query, source: spec.Source, priority: spec.Priority})
	}
	// These monitoring-only windows deliberately stay outside the versioned
	// training feature contract. They drive operational thermal alerts and must
	// not invalidate an already governed prediction artifact.
	result = append(result,
		metricSpec{key: "gpu_temp_min_15m", query: "min_over_time(DCGM_FI_DEV_GPU_TEMP[15m])", source: "dcgm_exporter"},
		metricSpec{key: "gpu_temp_min_5m", query: "min_over_time(DCGM_FI_DEV_GPU_TEMP[5m])", source: "dcgm_exporter"},
	)
	return result
}()

type metricObservation struct {
	value      float64
	observedAt time.Time
}

type featureValue struct {
	metrics           api.FloatMap
	sources           api.StringMap
	sourcesAvailable  api.StringList
	fallbackCount     int
	sourceDifferences api.StringList
	observedAt        time.Time
}

type ruleHit struct {
	code, domain, severity string
	deduction              int
	value                  float64
	threshold, evidence    string
}

type scoreResult struct {
	score                                                *int
	level                                                string
	stability, memory, thermal, power, link, performance int
	hits                                                 []ruleHit
	evidence                                             api.StringList
}

func NewService(db *storage.DB, prom prometheusReader, cfg config.HealthConfig) *Service {
	return &Service{db: db, prom: prom, config: cfg, historyRetention: ParseHistoryRetention(cfg.HistoryRetention), now: time.Now}
}

// NewServiceWithAlerts preserves health evaluation as the source of truth while
// emitting notifications only for newly opened sustained-thermal episodes.
func NewServiceWithAlerts(db *storage.DB, prom prometheusReader, cfg config.HealthConfig, alerts alertSink) *Service {
	service := NewService(db, prom, cfg)
	service.alerts = alerts
	return service
}

// ParseHistoryRetention accepts Go durations and an operator-friendly integer
// day suffix such as "365d". Invalid or non-positive values preserve one year
// of prediction history instead of silently reverting to the old 35-day cap.
func ParseHistoryRetention(value string) time.Duration {
	const fallback = 365 * 24 * time.Hour
	value = strings.TrimSpace(value)
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
		return fallback
	}
	retention, err := time.ParseDuration(value)
	if err != nil || retention <= 0 {
		return fallback
	}
	return retention
}

func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	s.evaluateAndLog(ctx)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.evaluateAndLog(ctx)
			timer.Reset(interval)
		}
	}
}

func (s *Service) evaluateAndLog(ctx context.Context) {
	if _, err := s.Evaluate(ctx); err != nil && ctx.Err() == nil {
		log.Printf("GPU health evaluation failed: %v", err)
	}
}

func (s *Service) Evaluate(ctx context.Context) (*api.HealthEvaluationRun, error) {
	now := s.now()
	// Rule semantics and version ship together. A stale deployment-node config
	// must not stamp new evaluations with the previous rule contract.
	ruleVersion := CurrentRuleVersion
	run := &api.HealthEvaluationRun{Status: "running", RuleVersion: ruleVersion, Source: s.prom.BaseURL(), StartedAt: now}
	if err := s.db.Create(run).Error; err != nil {
		return nil, err
	}
	fail := func(err error) (*api.HealthEvaluationRun, error) {
		finished := s.now()
		run.Status, run.FinishedAt, run.ErrorMessage = "failed", &finished, err.Error()
		_ = s.db.Save(run).Error
		return run, err
	}

	candidates := make(map[string]map[string]map[string]metricObservation)
	successfulQueries := 0
	for _, spec := range metricSpecs {
		rows, err := s.prom.Query(ctx, spec.query)
		if err != nil {
			log.Printf("health metric query skipped: key=%s error=%v", spec.key, err)
			continue
		}
		successfulQueries++
		for _, row := range rows {
			uuid := normalizeGPUUUID(firstNonEmpty(row.Metric["UUID"], row.Metric["uuid"]))
			if uuid == "" || math.IsNaN(row.Value) || math.IsInf(row.Value, 0) {
				continue
			}
			if candidates[uuid] == nil {
				candidates[uuid] = make(map[string]map[string]metricObservation)
			}
			if candidates[uuid][spec.key] == nil {
				candidates[uuid][spec.key] = make(map[string]metricObservation)
			}
			candidates[uuid][spec.key][spec.source] = metricObservation{value: row.Value, observedAt: row.Timestamp}
		}
	}
	if successfulQueries == 0 {
		return fail(fmt.Errorf("all health metric queries failed"))
	}

	activeNodeIDs := s.db.Model(&api.GPUNode{}).Select("id").Where("lifecycle <> ?", "retired")
	var assets []api.GPUAsset
	if err := s.db.Where("node_id IN (?) AND current_node_identity = ? AND current_uuid <> ''", activeNodeIDs, true).Order("node_ip ASC, gpu_index ASC").Find(&assets).Error; err != nil {
		return fail(err)
	}
	run.AssetCount = len(assets)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&api.GPUHealthScore{}).Where("current = ?", true).Update("current", false).Error; err != nil {
			return err
		}
		for _, asset := range assets {
			value := mergeFeatureCandidates(candidates[normalizeGPUUUID(asset.CurrentUUID)])
			metrics := api.FloatMap{}
			sources := api.StringMap{}
			sourcesAvailable := api.StringList{}
			fallbackCount := 0
			consistencyCandidates := api.StringList{}
			observedAt := now
			if value != nil {
				metrics, sources, sourcesAvailable = value.metrics, value.sources, value.sourcesAvailable
				fallbackCount, consistencyCandidates, observedAt = value.fallbackCount, value.sourceDifferences, value.observedAt
			}
			expected := expectedMetricKeys(asset.ModelName)
			available := availableCount(metrics, expected)
			confidence := dataConfidence(asset.State, available, len(expected))
			confidence = degradeConfidence(confidence, fallbackCount)
			snapshot := api.GPUFeatureSnapshot{EvaluationRunID: run.ID, GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, NodeIP: asset.NodeIP, GPUIndex: asset.GPUIndex, ModelName: asset.ModelName, Metrics: metrics, MetricSources: sources, SourcesAvailable: sourcesAvailable, FallbackMetricCount: fallbackCount, ConsistencyCandidates: consistencyCandidates, ConsistencyCandidateCount: len(consistencyCandidates), ConsistencyIssues: api.StringList{}, ConsistencyIssueCount: 0, FeatureCatalogVersion: features.CatalogVersion, FeatureVersions: features.HealthFeatureVersions(), AvailableMetricCount: available, ExpectedMetricCount: len(expected), DataConfidence: confidence, ObservedAt: observedAt}
			if err := tx.Create(&snapshot).Error; err != nil {
				return err
			}
			result := evaluateRules(metrics, asset.ModelName, confidence)
			score := api.GPUHealthScore{EvaluationRunID: run.ID, FeatureSnapshotID: snapshot.ID, GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, NodeIP: asset.NodeIP, GPUIndex: asset.GPUIndex, ModelName: asset.ModelName, Score: result.score, Level: result.level, DataConfidence: confidence, StabilityScore: result.stability, MemoryScore: result.memory, ThermalScore: result.thermal, PowerScore: result.power, InterconnectScore: result.link, PerformanceScore: result.performance, Evidence: result.evidence, RuleVersion: ruleVersion, Current: true, EvaluatedAt: now}
			if err := tx.Create(&score).Error; err != nil {
				return err
			}
			if result.score == nil {
				run.UnknownCount++
			} else {
				run.ScoredCount++
			}
			for _, hit := range result.hits {
				record := api.GPUHealthRuleHit{HealthScoreID: score.ID, GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, RuleCode: hit.code, Domain: hit.domain, Severity: hit.severity, Deduction: hit.deduction, ObservedValue: hit.value, Threshold: hit.threshold, Evidence: hit.evidence, RuleVersion: ruleVersion, EvaluatedAt: now}
				if err := tx.Create(&record).Error; err != nil {
					return err
				}
				run.RuleHitCount++
			}
			if err := reconcileFaultEvents(tx, asset, score, result.hits, confidence, now, ruleVersion); err != nil {
				return err
			}
		}
		if err := s.pruneHistory(tx, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	finished := s.now()
	run.Status, run.FinishedAt = "success", &finished
	if err := s.db.Save(run).Error; err != nil {
		return nil, err
	}
	if _, err := features.RefreshHistoricalBaselines(s.db, finished, false); err != nil {
		log.Printf("historical feature baseline refresh failed without affecting health scores: %v", err)
	}
	s.notifyNewSustainedThermalAlerts(now)
	return run, nil
}

func (s *Service) notifyNewSustainedThermalAlerts(observedAt time.Time) {
	if s.alerts == nil {
		return
	}
	var events []api.GPUFaultEvent
	if err := s.db.Where("source = ? AND state = ? AND first_observed_at = ? AND rule_code IN ?", healthRuleEventSource, "open", observedAt, []string{"gpu_temp_sustained_15m", "gpu_temp_sustained_5m_critical"}).Find(&events).Error; err != nil {
		log.Printf("sustained thermal notification lookup failed: %v", err)
		return
	}
	for _, event := range events {
		var node api.GPUNode
		_ = s.db.Where("node_ip = ?", event.NodeIP).First(&node).Error
		duration := "15m"
		if event.RuleCode == "gpu_temp_sustained_5m_critical" {
			duration = "5m"
		}
		host := firstNonEmpty(node.Hostname, event.NodeIP)
		alert := &api.AlertEvent{
			Source: "atlas_thermal_monitor", Host: host, Level: event.Severity,
			Message: "持续 GPU 高温告警", Timestamp: event.FirstObservedAt,
			Labels: api.StringMap{
				"alert_topic": "GPU sustained high temperature", "hostname": host, "host_ip": event.NodeIP,
				"gpu_index": fmt.Sprintf("%d", event.GPUIndex), "gpu_uuid": event.GPUUUID, "gpu_model": event.ModelName,
				"temperature_celsius": fmt.Sprintf("%.1f", event.ObservedValue), "threshold": event.Threshold,
				"sustained_duration": duration, "evidence_collection": "nvidia-smi -q -d PERFORMANCE (read-only follow-up)",
			},
		}
		if err := s.alerts.Process(alert); err != nil {
			log.Printf("sustained thermal notification failed for %s: %v", event.EpisodeKey, err)
		}
	}
}

func (s *Service) pruneHistory(tx *gorm.DB, now time.Time) error {
	retention := s.historyRetention
	if retention <= 0 {
		retention = ParseHistoryRetention("")
	}
	cutoff := now.Add(-retention)
	if err := tx.Where("evaluated_at < ?", cutoff).Delete(&api.GPUHealthRuleHit{}).Error; err != nil {
		return err
	}
	if err := tx.Where("evaluated_at < ?", cutoff).Delete(&api.GPUHealthScore{}).Error; err != nil {
		return err
	}
	return tx.Where("observed_at < ?", cutoff).Delete(&api.GPUFeatureSnapshot{}).Error
}

func expectedMetricKeys(model string) []string {
	return features.ExpectedHealthKeys(model)
}

func availableCount(metrics api.FloatMap, keys []string) int {
	count := 0
	for _, key := range keys {
		if _, ok := metrics[key]; ok {
			count++
		}
	}
	return count
}

func dataConfidence(assetState string, available, expected int) string {
	if assetState != "active" || expected == 0 {
		return "D"
	}
	ratio := float64(available) / float64(expected)
	switch {
	case ratio >= .90:
		return "A"
	case ratio >= .75:
		return "B"
	case ratio >= .50:
		return "C"
	default:
		return "D"
	}
}

func mergeFeatureCandidates(candidates map[string]map[string]metricObservation) *featureValue {
	if len(candidates) == 0 {
		return nil
	}
	result := &featureValue{metrics: api.FloatMap{}, sources: api.StringMap{}}
	sourceSet := map[string]bool{}
	for key, bySource := range candidates {
		var selected metricObservation
		source := ""
		if observation, ok := bySource["dcgm_exporter"]; ok {
			selected, source = observation, "dcgm_exporter"
		} else if observation, ok := bySource["gpu_exporter"]; ok {
			selected, source = observation, "gpu_exporter"
		}
		if source == "" {
			continue
		}
		result.metrics[key] = selected.value
		result.sources[key] = source
		sourceSet[source] = true
		if selected.observedAt.After(result.observedAt) {
			result.observedAt = selected.observedAt
		}
		if source == "gpu_exporter" && hasMetricSource(key, "dcgm_exporter") {
			result.fallbackCount++
		}
		dcgm, hasDCGM := bySource["dcgm_exporter"]
		gpu, hasGPU := bySource["gpu_exporter"]
		if hasDCGM && hasGPU && inconsistentMetric(key, dcgm.value, gpu.value) {
			result.sourceDifferences = append(result.sourceDifferences, fmt.Sprintf("%s: dcgm=%.3f gpu_exporter=%.3f", key, dcgm.value, gpu.value))
		}
	}
	for source := range sourceSet {
		result.sourcesAvailable = append(result.sourcesAvailable, source)
	}
	sort.Strings(result.sourcesAvailable)
	sort.Strings(result.sourceDifferences)
	return result
}

func hasMetricSource(key, source string) bool {
	for _, spec := range metricSpecs {
		if spec.key == key && spec.source == source {
			return true
		}
	}
	return false
}

func inconsistentMetric(key string, dcgm, gpu float64) bool {
	absoluteTolerance, relativeTolerance, comparable := consistencyTolerance(key)
	if !comparable {
		return false
	}
	tolerance := math.Max(absoluteTolerance, relativeTolerance*math.Max(math.Abs(dcgm), math.Abs(gpu)))
	return math.Abs(dcgm-gpu) > tolerance
}

func consistencyTolerance(key string) (absolute, relative float64, comparable bool) {
	switch key {
	case "gpu_temp_max_15m":
		return 3, 0, true
	case "memory_temp_max_15m":
		return 5, 0, true
	case "gpu_util_avg_15m":
		return 10, 0, true
	case "sm_clock_avg_15m":
		return 150, .10, true
	case "uncorrectable_remapped_rows", "uncorrectable_remapped_rows_delta_1h",
		"uncorrectable_remapped_rows_delta_24h", "correctable_remapped_rows",
		"correctable_remapped_rows_delta_1h", "correctable_remapped_rows_delta_24h", "row_remap_failure":
		return 0, 0, true
	default:
		return 0, 0, false
	}
}

func degradeConfidence(confidence string, fallbackCount int) string {
	steps := 0
	if fallbackCount > 0 {
		steps++
	}
	levels := []string{"A", "B", "C", "D"}
	index := len(levels) - 1
	for candidate, level := range levels {
		if confidence == level {
			index = candidate
			break
		}
	}
	index += steps
	if index >= len(levels) {
		index = len(levels) - 1
	}
	return levels[index]
}

func normalizeGPUUUID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 4 && strings.EqualFold(value[:4], "GPU-") {
		value = value[4:]
	}
	return strings.ToLower(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

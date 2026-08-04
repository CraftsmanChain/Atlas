package history

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	promclient "atlas/internal/prometheus"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

var researchMetricFamilies = api.StringList{
	"DCGM_EXP_GPU_HEALTH_STATUS",
	"DCGM_EXP_XID_ERRORS_TOTAL",
	"DCGM_FI_DEV_CLOCK_THROTTLE_REASONS",
	"DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS",
	"DCGM_FI_DEV_GPU_NVLINK_ERRORS",
	"DCGM_FI_DEV_GPU_TEMP",
	"DCGM_FI_DEV_GPU_UTIL",
	"DCGM_FI_DEV_MEMORY_TEMP",
	"DCGM_FI_DEV_PCIE_REPLAY_COUNTER",
	"DCGM_FI_DEV_POWER_VIOLATION",
	"DCGM_FI_DEV_RELIABILITY_VIOLATION",
	"DCGM_FI_DEV_ROW_REMAP_FAILURE",
	"DCGM_FI_DEV_THERMAL_VIOLATION",
	"DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS",
	"DCGM_FI_DEV_XID_ERRORS",
	"nvidia_smi_clocks_event_reasons_active",
	"nvidia_smi_ecc_errors_corrected_aggregate_total",
	"nvidia_smi_ecc_errors_uncorrected_aggregate_total",
	"nvidia_smi_ecc_errors_uncorrected_volatile_total",
	"nvidia_smi_pcie_link_gen_current",
	"nvidia_smi_pcie_link_width_current",
	"nvidia_smi_remapped_rows_pending",
	"nvidia_smi_reset_status_reset_required",
}

type SourceStatus struct {
	ID          string                      `json:"id"`
	Name        string                      `json:"name"`
	Type        string                      `json:"type"`
	BaseURL     string                      `json:"base_url"`
	TenantID    string                      `json:"tenant_id,omitempty"`
	Enabled     bool                        `json:"enabled"`
	ReadOnly    bool                        `json:"read_only"`
	Execution   string                      `json:"execution"`
	DatasetDir  string                      `json:"dataset_dir"`
	LatestAudit *api.MonitoringHistoryAudit `json:"latest_audit,omitempty"`
}

type Service struct {
	db                    *storage.DB
	config                config.HistoryConfig
	timeout               time.Duration
	now                   func() time.Time
	mu                    sync.Mutex
	backfillMu            sync.Mutex
	backfillRunning       bool
	datasetMu             sync.Mutex
	featureMu             sync.Mutex
	featureRunning        bool
	preparationMu         sync.Mutex
	controlFeatureMu      sync.Mutex
	controlFeatureRunning bool
	matrixMu              sync.Mutex
	matrixRunning         bool
	baselineMu            sync.Mutex
	baselineRunning       bool
	replayMu              sync.Mutex
	replayRunning         bool
	coverageMu            sync.Mutex
	coverageRunning       bool
}

func NewService(db *storage.DB, cfg config.HistoryConfig, timeout time.Duration) *Service {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	service := &Service{db: db, config: cfg, timeout: timeout, now: time.Now}
	now := time.Now()
	_ = db.Model(&api.HistoryBackfillRun{}).Where("status IN ?", []string{"queued", "running"}).Updates(map[string]any{
		"status": "interrupted", "error_message": "Atlas restarted before the backfill completed", "finished_at": &now,
	}).Error
	_ = db.Model(&api.TrainingFeatureBuild{}).Where("status IN ?", []string{"queued", "running"}).Updates(map[string]any{
		"status": "interrupted", "error_message": "Atlas restarted before feature extraction completed", "finished_at": &now,
	}).Error
	_ = db.Model(&api.TrainingPreparationBuild{}).Where("status = ?", "running").Updates(map[string]any{
		"status": "interrupted", "error_message": "Atlas restarted before training preparation completed", "finished_at": &now,
	}).Error
	_ = db.Model(&api.TrainingControlFeatureBuild{}).Where("status IN ?", []string{"queued", "running"}).Updates(map[string]any{
		"status": "interrupted", "error_message": "Atlas restarted before healthy-control extraction completed", "finished_at": &now,
	}).Error
	_ = db.Model(&api.TrainingMatrixBuild{}).Where("status IN ?", []string{"queued", "running"}).Updates(map[string]any{
		"status": "interrupted", "error_message": "Atlas restarted before training matrix assembly completed", "finished_at": &now,
	}).Error
	_ = db.Model(&api.BaselineModelBuild{}).Where("status IN ?", []string{"queued", "running"}).Updates(map[string]any{
		"status": "interrupted", "error_message": "Atlas restarted before baseline training completed", "finished_at": &now,
	}).Error
	_ = db.Model(&api.PredictionFeatureReplayRun{}).Where("status IN ?", []string{"queued", "running"}).Updates(map[string]any{
		"status": "interrupted", "error_message": "Atlas restarted before feature replay completed", "finished_at": &now,
	}).Error
	_ = db.Model(&api.PredictionLiveCoverageAudit{}).Where("status IN ?", []string{"queued", "running"}).Updates(map[string]any{
		"status": "interrupted", "error_message": "Atlas restarted before live coverage audit completed", "finished_at": &now,
	}).Error
	return service
}

func ResearchMetricFamilies() api.StringList {
	return append(api.StringList(nil), researchMetricFamilies...)
}

func (s *Service) Sources() ([]SourceStatus, error) {
	result := make([]SourceStatus, 0, len(s.config.Sources))
	for _, source := range s.config.Sources {
		item := SourceStatus{
			ID: source.ID, Name: source.Name, Type: source.Type, BaseURL: source.BaseURL,
			TenantID: source.TenantID, Enabled: source.Enabled, ReadOnly: true,
			Execution: "atlas_deployment_node", DatasetDir: s.config.DatasetDir,
		}
		var latest api.MonitoringHistoryAudit
		query := s.db.Where("source_key = ?", source.ID).Order("finished_at DESC, id DESC").Limit(1).Find(&latest)
		if query.Error != nil {
			return nil, query.Error
		}
		if query.RowsAffected > 0 {
			item.LatestAudit = &latest
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Service) Audits(limit int) ([]api.MonitoringHistoryAudit, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []api.MonitoringHistoryAudit
	err := s.db.Order("finished_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) AuditAll(ctx context.Context) ([]api.MonitoringHistoryAudit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows := make([]api.MonitoringHistoryAudit, 0, len(s.config.Sources))
	for _, source := range s.config.Sources {
		if !source.Enabled {
			continue
		}
		row := s.auditSource(ctx, source)
		if err := s.db.Create(&row).Error; err != nil {
			return rows, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	run := func() {
		if _, err := s.AuditAll(ctx); err != nil && ctx.Err() == nil {
			log.Printf("monitoring history audit failed: %v", err)
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

func (s *Service) auditSource(ctx context.Context, source config.HistorySourceConfig) api.MonitoringHistoryAudit {
	started := s.now()
	row := api.MonitoringHistoryAudit{
		SourceKey: source.ID, SourceName: source.Name, SourceType: source.Type,
		BaseURL: source.BaseURL, Status: "success",
		RequiredMetricFamilies: ResearchMetricFamilies(),
		Capabilities:           api.StringList{"instant_query", "range_query", "label_values"},
		Details: api.StringMap{
			"execution":   "atlas_deployment_node",
			"read_only":   "true",
			"dataset_dir": s.config.DatasetDir,
		},
		StartedAt: started,
	}
	if source.Type != "prometheus" {
		row.Capabilities = append(row.Capabilities, "raw_export")
	}
	baseURL := strings.TrimRight(source.BaseURL, "/")
	if source.Type == "victoriametrics-cluster" {
		baseURL += "/select/" + source.TenantID + "/prometheus"
	}
	if source.AuthType != "" && source.AuthType != "none" {
		row.Status = "failed"
		row.ErrorMessage = "authenticated history sources are not enabled in the first read-only adapter"
		row.FinishedAt = s.now()
		return row
	}
	client, err := promclient.NewClient(baseURL, s.timeout)
	if err != nil {
		row.Status = "failed"
		row.ErrorMessage = err.Error()
		row.FinishedAt = s.now()
		return row
	}
	errorsFound := []string{}
	build, err := client.BuildInfo(ctx)
	if err != nil {
		errorsFound = append(errorsFound, "build_info: "+err.Error())
	} else {
		row.SourceVersion = build.Version
		row.Details["source_revision"] = build.Revision
	}
	flags, err := client.Flags(ctx)
	if err != nil {
		errorsFound = append(errorsFound, "flags: "+err.Error())
	} else {
		row.ConfiguredRetention = firstNonEmpty(flags["storage.tsdb.retention.time"], flags["storage.tsdb.retention"])
		row.Details["query_max_samples"] = flags["query.max-samples"]
		row.Details["query_max_concurrency"] = flags["query.max-concurrency"]
	}
	targets, err := client.ActiveTargets(ctx)
	if err != nil {
		errorsFound = append(errorsFound, "targets: "+err.Error())
	} else {
		for _, target := range targets {
			switch target.Labels["job"] {
			case "dcgm_exporter":
				row.DCGMTargetCount++
			case "gpu_exporter":
				row.GPUExporterTargetCount++
			}
		}
	}
	end := s.now()
	lookback := retentionLookback(row.ConfiguredRetention)
	families, err := client.LabelValues(ctx, "__name__", `{__name__=~"DCGM_FI_DEV_.+|DCGM_EXP_.+|nvidia_smi_.+"}`, end.Add(-lookback), end)
	if err != nil {
		errorsFound = append(errorsFound, "metric_families: "+err.Error())
	} else {
		sort.Strings(families)
		row.MetricFamilies = families
		row.MissingMetricFamilies = missingFamilies(families, researchMetricFamilies)
	}
	if samples, queryErr := client.Query(ctx, "count(count by(UUID) (DCGM_FI_DEV_GPU_UTIL))"); queryErr != nil {
		errorsFound = append(errorsFound, "gpu_series: "+queryErr.Error())
	} else if len(samples) > 0 {
		row.CurrentGPUSeries = int(samples[0].Value)
	}
	if samples, queryErr := client.Query(ctx, "quantile(0.5, count_over_time(DCGM_FI_DEV_GPU_UTIL[5m]))"); queryErr != nil {
		errorsFound = append(errorsFound, "scrape_interval: "+queryErr.Error())
	} else if len(samples) > 0 && samples[0].Value > 0 {
		row.ScrapeIntervalSeconds = 300 / samples[0].Value
	}
	if samples, queryErr := client.Query(ctx, "max(timestamp(DCGM_FI_DEV_GPU_UTIL))"); queryErr != nil {
		errorsFound = append(errorsFound, "latest_sample: "+queryErr.Error())
	} else if len(samples) > 0 && samples[0].Value > 0 {
		latest := time.Unix(int64(samples[0].Value), 0)
		row.LatestSampleAt = &latest
	}
	if series, queryErr := client.QueryRange(ctx, "count(count by(UUID) (DCGM_FI_DEV_GPU_UTIL))", end.Add(-lookback), end, 24*time.Hour); queryErr != nil {
		errorsFound = append(errorsFound, "earliest_sample: "+queryErr.Error())
	} else {
		for _, item := range series {
			for _, point := range item.Values {
				if point.Value <= 0 {
					continue
				}
				earliest := point.Timestamp
				row.EarliestSampleAt = &earliest
				break
			}
			if row.EarliestSampleAt != nil {
				break
			}
		}
	}
	if len(errorsFound) > 0 {
		row.Status = "partial"
		row.ErrorMessage = strings.Join(errorsFound, "; ")
	}
	row.FinishedAt = s.now()
	return row
}

func retentionLookback(value string) time.Duration {
	value = strings.TrimSpace(value)
	if strings.HasSuffix(value, "y") {
		var years int
		if _, err := fmt.Sscanf(value, "%dy", &years); err == nil && years > 0 {
			return time.Duration(years) * 365 * 24 * time.Hour
		}
	}
	if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
		return parsed
	}
	return 365 * 24 * time.Hour
}

func missingFamilies(available, required []string) api.StringList {
	found := make(map[string]struct{}, len(available))
	for _, name := range available {
		found[name] = struct{}{}
	}
	result := api.StringList{}
	for _, name := range required {
		if _, ok := found[name]; !ok {
			result = append(result, name)
		}
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ValidateDatasetDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("history dataset directory is required")
	}
	return nil
}

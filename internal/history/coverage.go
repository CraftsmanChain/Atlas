package history

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	promclient "atlas/internal/prometheus"
	"atlas/pkg/api"
)

const (
	liveCoverageVersion       = "gpu-live-24h-coverage-v1"
	liveCoverageFreshnessSLA  = 15 * time.Minute
	liveCoverageMinimumRatio  = 0.70
	liveCoverageFleetRatio    = 0.80
	liveCoverageMinimumGPUs   = 30
	liveCoverageUUIDChunkSize = 16
)

type coverageMetricReport struct {
	SampleCount int        `json:"sample_count"`
	Coverage    float64    `json:"coverage"`
	LatestAt    *time.Time `json:"latest_at,omitempty"`
	Status      string     `json:"status"`
}

type coverageGPUReport struct {
	GPUAssetID uint                            `json:"gpu_asset_id"`
	GPUUUID    string                          `json:"gpu_uuid"`
	NodeIP     string                          `json:"node_ip"`
	GPUIndex   int                             `json:"gpu_index"`
	Status     string                          `json:"status"`
	Metrics    map[string]coverageMetricReport `json:"metrics"`
}

type liveCoverageReport struct {
	Version       string              `json:"version"`
	AuditKey      string              `json:"audit_key"`
	ModelKey      string              `json:"model_key"`
	ModelVersion  string              `json:"model_version"`
	WindowMinutes int                 `json:"window_minutes"`
	StepSeconds   int                 `json:"step_seconds"`
	MinimumRatio  float64             `json:"minimum_ratio"`
	FreshnessSLA  int                 `json:"freshness_sla_seconds"`
	SourceMetrics []string            `json:"source_metrics"`
	GPUs          []coverageGPUReport `json:"gpus"`
	CreatedAt     time.Time           `json:"created_at"`
}

func (s *Service) LiveCoverageAudits(limit int) ([]api.PredictionLiveCoverageAudit, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.PredictionLiveCoverageAudit
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) StartLiveCoverageAudit(modelSpecID uint) (api.PredictionLiveCoverageAudit, error) {
	s.coverageMu.Lock()
	defer s.coverageMu.Unlock()
	if s.coverageRunning {
		return api.PredictionLiveCoverageAudit{}, fmt.Errorf("a live coverage audit is already running")
	}
	var spec api.PredictionModelSpec
	query := s.db.Where("current = ? AND status = ?", true, "shadow_candidate")
	if modelSpecID > 0 {
		query = query.Where("id = ?", modelSpecID)
	}
	result := query.Order("trained_at DESC, id DESC").Limit(1).Find(&spec)
	if result.Error != nil {
		return api.PredictionLiveCoverageAudit{}, result.Error
	}
	if result.RowsAffected == 0 {
		return api.PredictionLiveCoverageAudit{}, fmt.Errorf("a current shadow model candidate is required")
	}
	var parity api.PredictionFeatureParityAudit
	result = s.db.Where("model_spec_id = ?", spec.ID).Limit(1).Find(&parity)
	allowedStatus := parity.Status == "live_coverage_required" || parity.Status == "shadow_runtime_required" || parity.Status == "blocked_live_coverage"
	if result.Error != nil || result.RowsAffected == 0 || !allowedStatus || parity.TrainingFeatureCount <= 0 || parity.ReplayVerifiedCount != parity.TrainingFeatureCount {
		return api.PredictionLiveCoverageAudit{}, fmt.Errorf("a passed historical value replay is required")
	}
	var baseline api.BaselineModelBuild
	var matrix api.TrainingMatrixBuild
	var preparation api.TrainingPreparationBuild
	var featureBuild api.TrainingFeatureBuild
	if err := s.db.First(&baseline, spec.SourceBaselineBuildID).Error; err != nil {
		return api.PredictionLiveCoverageAudit{}, err
	}
	if err := s.db.First(&matrix, baseline.SourceMatrixBuildID).Error; err != nil {
		return api.PredictionLiveCoverageAudit{}, err
	}
	if err := s.db.First(&preparation, matrix.SourcePreparationBuildID).Error; err != nil {
		return api.PredictionLiveCoverageAudit{}, err
	}
	if err := s.db.First(&featureBuild, preparation.SourceFeatureBuildID).Error; err != nil {
		return api.PredictionLiveCoverageAudit{}, err
	}
	started := s.now()
	key := liveCoverageVersion + "-" + strconv.FormatInt(started.UTC().UnixNano(), 10)
	expected := int(featureLookback/featureQueryStep) + 1
	run := api.PredictionLiveCoverageAudit{
		AuditKey: key, Version: liveCoverageVersion, Status: "queued", ModelSpecID: spec.ID,
		ModelKey: spec.ModelKey, ModelVersion: spec.Version, SourceKey: featureBuild.SourceKey, ScopeModelName: spec.ScopeModelName,
		WindowMinutes: int(featureLookback / time.Minute), QueryStepSeconds: int(featureQueryStep / time.Second),
		ExpectedSamples: expected, MinimumSamples: int(math.Ceil(float64(expected) * liveCoverageMinimumRatio)),
		FreshnessSLASeconds: int(liveCoverageFreshnessSLA / time.Second), SourceMetricCount: len(parity.SourceMetrics),
		OutputDir: filepath.Join(s.config.DatasetDir, "live-coverage", key), StartedAt: started,
	}
	if err := s.db.Create(&run).Error; err != nil {
		return run, err
	}
	s.coverageRunning = true
	go s.executeLiveCoverageAudit(run.ID)
	return run, nil
}

func (s *Service) executeLiveCoverageAudit(id uint) {
	defer func() { s.coverageMu.Lock(); s.coverageRunning = false; s.coverageMu.Unlock() }()
	var audit api.PredictionLiveCoverageAudit
	if err := s.db.First(&audit, id).Error; err != nil {
		return
	}
	if err := s.db.Model(&audit).Update("status", "running").Error; err != nil {
		return
	}
	if err := s.buildLiveCoverageAudit(&audit); err != nil {
		finished := s.now()
		_ = s.db.Model(&audit).Updates(map[string]any{"status": "failed", "error_message": err.Error(), "finished_at": &finished}).Error
	}
}

func (s *Service) buildLiveCoverageAudit(audit *api.PredictionLiveCoverageAudit) error {
	var spec api.PredictionModelSpec
	var parity api.PredictionFeatureParityAudit
	if err := s.db.First(&spec, audit.ModelSpecID).Error; err != nil {
		return err
	}
	if err := s.db.Where("model_spec_id = ?", spec.ID).First(&parity).Error; err != nil {
		return err
	}
	var assets []api.GPUAsset
	if err := s.db.Where("current_node_identity = ? AND state = ? AND model_name = ? AND current_uuid <> ?", true, "active", spec.ScopeModelName, "").Order("node_ip, gpu_index").Find(&assets).Error; err != nil {
		return err
	}
	if len(assets) == 0 {
		return fmt.Errorf("no active GPUs match model scope %q", spec.ScopeModelName)
	}
	source, err := s.resolveSource(audit.SourceKey)
	if err != nil {
		return err
	}
	client, err := s.historyClient(source)
	if err != nil {
		return err
	}
	end := s.now()
	start := end.Add(-featureLookback)
	byGPU := map[string]map[string][]promclient.RangePoint{}
	for offset := 0; offset < len(assets); offset += liveCoverageUUIDChunkSize {
		limit := offset + liveCoverageUUIDChunkSize
		if limit > len(assets) {
			limit = len(assets)
		}
		uuids := make([]string, 0, limit-offset)
		for _, asset := range assets[offset:limit] {
			uuids = append(uuids, asset.CurrentUUID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		series, queryErr := client.QueryRange(ctx, historicalMetricQueryUUIDs(uuids), start, end, featureQueryStep)
		cancel()
		if queryErr != nil {
			return fmt.Errorf("coverage query chunk %d: %w", offset/liveCoverageUUIDChunkSize, queryErr)
		}
		for uuid, values := range canonicalSeriesByGPU(series) {
			byGPU[uuid] = values
		}
	}
	report := liveCoverageReport{Version: liveCoverageVersion, AuditKey: audit.AuditKey, ModelKey: spec.ModelKey, ModelVersion: spec.Version, WindowMinutes: audit.WindowMinutes, StepSeconds: audit.QueryStepSeconds, MinimumRatio: liveCoverageMinimumRatio, FreshnessSLA: audit.FreshnessSLASeconds, SourceMetrics: append([]string(nil), parity.SourceMetrics...), GPUs: make([]coverageGPUReport, 0, len(assets)), CreatedAt: s.now()}
	for _, asset := range assets {
		gpu := coverageGPUReport{GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, NodeIP: asset.NodeIP, GPUIndex: asset.GPUIndex, Status: "eligible", Metrics: map[string]coverageMetricReport{}}
		values := byGPU[normalizeHistoricalGPUUUID(asset.CurrentUUID)]
		for _, metric := range parity.SourceMetrics {
			points := pointsInWindow(values[metric], start, end)
			coverage := math.Min(1, float64(len(points))/float64(audit.ExpectedSamples))
			item := coverageMetricReport{SampleCount: len(points), Coverage: coverage, Status: "passed"}
			audit.MetricPairCount++
			if len(points) == 0 {
				item.Status = "missing"
				audit.MissingMetricPairCount++
			} else {
				latest := points[len(points)-1].Timestamp
				item.LatestAt = &latest
				if len(points) < audit.MinimumSamples {
					item.Status = "sparse"
					audit.SparseMetricPairCount++
				} else if end.Sub(latest) > liveCoverageFreshnessSLA {
					item.Status = "stale"
					audit.StaleMetricPairCount++
				} else {
					audit.PassingMetricPairCount++
				}
			}
			if item.Status != "passed" {
				gpu.Status = "blocked"
			}
			gpu.Metrics[metric] = item
		}
		if gpu.Status == "eligible" {
			audit.EligibleGPUCount++
		} else {
			audit.BlockedGPUCount++
		}
		report.GPUs = append(report.GPUs, gpu)
	}
	audit.TargetGPUCount = len(assets)
	audit.EligibleRatio = float64(audit.EligibleGPUCount) / float64(audit.TargetGPUCount)
	minimumGPUs := liveCoverageMinimumGPUs
	if minimumGPUs > audit.TargetGPUCount {
		minimumGPUs = audit.TargetGPUCount
	}
	status := "passed"
	blocking := api.StringList{}
	if audit.EligibleGPUCount < minimumGPUs {
		status = "failed"
		blocking = append(blocking, "eligible_gpu_count_below_minimum")
	}
	if audit.EligibleRatio < liveCoverageFleetRatio {
		status = "failed"
		blocking = append(blocking, "eligible_gpu_ratio_below_80_percent")
	}
	audit.BlockingReasons = blocking
	if err := os.MkdirAll(audit.OutputDir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(audit.OutputDir, "coverage_report.json")
	if err := writeJSONAtomic(path, report); err != nil {
		return err
	}
	checksum, err := fileSHA256(path)
	if err != nil {
		return err
	}
	finished := s.now()
	if err := s.db.Model(audit).Updates(map[string]any{"status": status, "target_gpu_count": audit.TargetGPUCount, "eligible_gpu_count": audit.EligibleGPUCount, "blocked_gpu_count": audit.BlockedGPUCount, "metric_pair_count": audit.MetricPairCount, "passing_metric_pair_count": audit.PassingMetricPairCount, "missing_metric_pair_count": audit.MissingMetricPairCount, "sparse_metric_pair_count": audit.SparseMetricPairCount, "stale_metric_pair_count": audit.StaleMetricPairCount, "eligible_ratio": audit.EligibleRatio, "blocking_reasons": blocking, "report_path": path, "report_sha256": checksum, "finished_at": &finished}).Error; err != nil {
		return err
	}
	parityStatus := "shadow_runtime_required"
	parityReasons := api.StringList{"shadow_scoring_runtime_not_enabled"}
	if status != "passed" {
		parityStatus = "blocked_live_coverage"
		parityReasons = append(api.StringList(nil), blocking...)
	}
	return s.db.Model(&parity).Updates(map[string]any{"status": parityStatus, "blocking_reasons": parityReasons, "scoring_allowed": false, "audited_at": s.now()}).Error
}

func canonicalSeriesByGPU(series []promclient.RangeSeries) map[string]map[string][]promclient.RangePoint {
	grouped := map[string][]promclient.RangeSeries{}
	for _, item := range series {
		uuid := item.Metric["UUID"]
		if uuid == "" {
			uuid = item.Metric["uuid"]
		}
		uuid = normalizeHistoricalGPUUUID(uuid)
		if uuid != "" {
			grouped[uuid] = append(grouped[uuid], item)
		}
	}
	result := make(map[string]map[string][]promclient.RangePoint, len(grouped))
	for uuid, rows := range grouped {
		result[uuid] = canonicalSeries(rows)
	}
	return result
}

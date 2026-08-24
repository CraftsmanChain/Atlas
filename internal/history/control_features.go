package history

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	promclient "atlas/internal/prometheus"
	"atlas/pkg/api"
)

const (
	controlFeatureDatasetVersion = "gpu-healthy-control-features-v1"
	minimumTelemetryContinuity   = 0.70
)

type ControlFeatureBuildRequest struct {
	SourcePreparationBuildID uint `json:"source_preparation_build_id"`
	MaxUniqueWindows         int  `json:"max_unique_windows,omitempty"`
}

type controlFeatureRow struct {
	Request             healthyControlRequest `json:"request"`
	Feature             extractedFeatureRow   `json:"feature"`
	TelemetryContinuity float64               `json:"telemetry_continuity"`
	PositiveGPUMeanUtil *float64              `json:"positive_gpu_mean_util,omitempty"`
	ControlGPUMeanUtil  *float64              `json:"control_gpu_mean_util,omitempty"`
	PositiveLoadBucket  string                `json:"positive_load_bucket,omitempty"`
	ControlLoadBucket   string                `json:"control_load_bucket,omitempty"`
	TrainingStatus      string                `json:"training_status"`
	ExclusionReason     string                `json:"exclusion_reason,omitempty"`
}

type controlFeatureQualityReport struct {
	ControlFeatureDatasetKey string    `json:"control_feature_dataset_key"`
	Version                  string    `json:"version"`
	SourcePreparedDatasetKey string    `json:"source_prepared_dataset_key"`
	FeatureContractVersion   string    `json:"feature_contract_version"`
	RequestCount             int       `json:"request_count"`
	UniqueWindowCount        int       `json:"unique_window_count"`
	EligibleRequestCount     int       `json:"eligible_request_count"`
	TelemetryCensoredCount   int       `json:"telemetry_censored_count"`
	LowCoverageCount         int       `json:"low_coverage_count"`
	DiscontinuousCount       int       `json:"discontinuous_count"`
	LoadUnknownCount         int       `json:"load_unknown_count"`
	LoadMismatchCount        int       `json:"load_mismatch_count"`
	ExtractionFailedCount    int       `json:"extraction_failed_count"`
	TelemetryPolicy          string    `json:"telemetry_policy"`
	LoadPolicy               string    `json:"load_policy"`
	CreatedAt                time.Time `json:"created_at"`
}

type controlQueryTask struct {
	key     string
	request healthyControlRequest
}

type controlQueryResult struct {
	key     string
	feature extractedFeatureRow
	err     error
}

func (s *Service) ControlFeatureBuilds(limit int) ([]api.TrainingControlFeatureBuild, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.TrainingControlFeatureBuild
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) StartControlFeatureBuild(request ControlFeatureBuildRequest) (api.TrainingControlFeatureBuild, error) {
	s.controlFeatureMu.Lock()
	defer s.controlFeatureMu.Unlock()
	if s.controlFeatureRunning {
		return api.TrainingControlFeatureBuild{}, fmt.Errorf("a healthy-control feature extraction is already running")
	}
	if request.MaxUniqueWindows < 0 || request.MaxUniqueWindows > 10000 {
		return api.TrainingControlFeatureBuild{}, fmt.Errorf("max_unique_windows must be between 0 and 10000")
	}
	var preparation api.TrainingPreparationBuild
	query := s.db.Where("(status = ? AND version = ?) OR (status = ? AND version = ?)",
		"completed", preparationDatasetVersion,
		"manual_feedback_ready_pending_control_extraction", manualPreparationVersion,
	)
	if request.SourcePreparationBuildID > 0 {
		query = query.Where("id = ?", request.SourcePreparationBuildID)
	}
	result := query.Order("finished_at DESC, id DESC").Limit(1).Find(&preparation)
	if result.Error != nil {
		return api.TrainingControlFeatureBuild{}, result.Error
	}
	if result.RowsAffected == 0 {
		return api.TrainingControlFeatureBuild{}, fmt.Errorf("a completed historical preparation or manual feedback preparation ready for control extraction is required")
	}
	sourceKey := ""
	featureContractVersion := ""
	if preparation.SourceKind == "manual_feedback_feature_request" {
		var source api.ManualFeedbackFeatureRequestBuild
		if err := s.db.First(&source, preparation.SourceManualFeedbackFeatureRequestBuildID).Error; err != nil {
			return api.TrainingControlFeatureBuild{}, err
		}
		sourceKey = source.SourceKey
		featureContractVersion = source.FeatureContractVersion
	} else {
		var source api.TrainingFeatureBuild
		if err := s.db.First(&source, preparation.SourceFeatureBuildID).Error; err != nil {
			return api.TrainingControlFeatureBuild{}, err
		}
		sourceKey = source.SourceKey
		featureContractVersion = source.FeatureContractVersion
	}
	if _, err := s.resolveSource(sourceKey); err != nil {
		return api.TrainingControlFeatureBuild{}, err
	}
	started := s.now()
	key := controlFeatureDatasetVersion + "-" + strconv.FormatInt(started.UTC().UnixNano(), 10)
	build := api.TrainingControlFeatureBuild{
		ControlFeatureDatasetKey: key, Version: controlFeatureDatasetVersion, Status: "queued",
		SourcePreparationBuildID: preparation.ID, SourcePreparedDatasetKey: preparation.PreparedDatasetKey,
		SourceKey: sourceKey, FeatureContractVersion: featureContractVersion,
		OutputDir: filepath.Join(s.config.DatasetDir, "control-features", key), StartedAt: started,
	}
	if err := s.db.Create(&build).Error; err != nil {
		return build, err
	}
	s.controlFeatureRunning = true
	go s.executeControlFeatureBuild(build.ID, request.MaxUniqueWindows)
	return build, nil
}

func (s *Service) executeControlFeatureBuild(buildID uint, maxUniqueWindows int) {
	defer func() {
		s.controlFeatureMu.Lock()
		s.controlFeatureRunning = false
		s.controlFeatureMu.Unlock()
	}()
	var build api.TrainingControlFeatureBuild
	if err := s.db.First(&build, buildID).Error; err != nil {
		return
	}
	if err := s.db.Model(&build).Update("status", "running").Error; err != nil {
		return
	}
	if err := s.buildControlFeatures(&build, maxUniqueWindows); err != nil {
		finished := s.now()
		_ = s.db.Model(&build).Updates(map[string]any{
			"status": "failed", "error_message": err.Error(), "finished_at": &finished,
		}).Error
	}
}

func (s *Service) buildControlFeatures(build *api.TrainingControlFeatureBuild, maxUniqueWindows int) error {
	var preparation api.TrainingPreparationBuild
	if err := s.db.First(&preparation, build.SourcePreparationBuildID).Error; err != nil {
		return err
	}
	if err := verifyFileSHA256(preparation.ControlRequestsPath, preparation.ControlRequestsSHA256); err != nil {
		return fmt.Errorf("control request checksum: %w", err)
	}
	if err := verifyFileSHA256(preparation.PreparedSamplesPath, preparation.PreparedSamplesSHA256); err != nil {
		return fmt.Errorf("prepared sample checksum: %w", err)
	}
	requests, err := readJSONLines[healthyControlRequest](preparation.ControlRequestsPath)
	if err != nil {
		return err
	}
	prepared, err := readJSONLines[preparedTrainingSample](preparation.PreparedSamplesPath)
	if err != nil {
		return err
	}
	positiveBySample := make(map[string]extractedFeatureRow, len(prepared))
	for _, item := range prepared {
		if item.TrainingStatus == "eligible" || item.TrainingStatus == "eligible_pending_controls" {
			positiveBySample[item.Sample.SampleKey] = item.Sample
		}
	}
	tasksByKey := map[string]controlQueryTask{}
	for _, request := range requests {
		key := controlFeatureWindowKey(request)
		if _, exists := tasksByKey[key]; !exists {
			tasksByKey[key] = controlQueryTask{key: key, request: request}
		}
	}
	taskKeys := make([]string, 0, len(tasksByKey))
	for key := range tasksByKey {
		taskKeys = append(taskKeys, key)
	}
	sort.Strings(taskKeys)
	if maxUniqueWindows > 0 && len(taskKeys) > maxUniqueWindows {
		taskKeys = taskKeys[:maxUniqueWindows]
	}
	selected := make(map[string]bool, len(taskKeys))
	tasks := make([]controlQueryTask, 0, len(taskKeys))
	for _, key := range taskKeys {
		selected[key] = true
		tasks = append(tasks, tasksByKey[key])
	}
	selectedRequests := make([]healthyControlRequest, 0, len(requests))
	for _, request := range requests {
		if selected[controlFeatureWindowKey(request)] {
			selectedRequests = append(selectedRequests, request)
		}
	}
	build.RequestCount, build.UniqueWindowCount = len(selectedRequests), len(tasks)
	if err := s.db.Model(build).Updates(map[string]any{
		"request_count": build.RequestCount, "unique_window_count": build.UniqueWindowCount,
	}).Error; err != nil {
		return err
	}
	if len(tasks) == 0 {
		return fmt.Errorf("no healthy-control windows are available")
	}
	if err := os.MkdirAll(build.OutputDir, 0o750); err != nil {
		return err
	}
	source, err := s.resolveSource(build.SourceKey)
	if err != nil {
		return err
	}
	client, err := s.historyClient(source)
	if err != nil {
		return err
	}
	workerCount := s.config.MaxConcurrency
	if workerCount <= 0 {
		workerCount = 1
	}
	if workerCount > len(tasks) {
		workerCount = len(tasks)
	}
	jobs := make(chan controlQueryTask)
	results := make(chan controlQueryResult, len(tasks))
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range jobs {
				feature, queryErr := s.extractControlFeature(client, build, task)
				results <- controlQueryResult{key: task.key, feature: feature, err: queryErr}
			}
		}()
	}
	go func() {
		for _, task := range tasks {
			jobs <- task
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	featuresByWindow := make(map[string]extractedFeatureRow, len(tasks))
	for result := range results {
		featuresByWindow[result.key] = result.feature
		build.ProcessedUniqueWindows++
		if build.ProcessedUniqueWindows%10 == 0 || build.ProcessedUniqueWindows == len(tasks) {
			_ = s.db.Model(build).Update("processed_unique_windows", build.ProcessedUniqueWindows).Error
		}
	}
	rows := make([]controlFeatureRow, 0, len(selectedRequests))
	for _, request := range selectedRequests {
		feature := featuresByWindow[controlFeatureWindowKey(request)]
		row := qualifyControlFeature(request, feature, positiveBySample[request.PairedSampleKey], preparation.MinimumMetricCoverage)
		rows = append(rows, row)
		build.CompletedRequestCount++
		switch row.ExclusionReason {
		case "":
			build.EligibleRequestCount++
		case "extraction_failed":
			build.ExtractionFailedCount++
		case "telemetry_censored":
			build.TelemetryCensoredCount++
		case "metric_coverage_below_threshold":
			build.LowCoverageCount++
		case "telemetry_discontinuous":
			build.DiscontinuousCount++
		case "load_unknown":
			build.LoadUnknownCount++
		case "load_mismatch":
			build.LoadMismatchCount++
		}
	}
	featurePath := filepath.Join(build.OutputDir, "control_features.jsonl")
	checksum, err := writeJSONLines(featurePath, rows)
	if err != nil {
		return err
	}
	reportPath := filepath.Join(build.OutputDir, "quality_report.json")
	report := controlFeatureQualityReport{
		ControlFeatureDatasetKey: build.ControlFeatureDatasetKey, Version: build.Version,
		SourcePreparedDatasetKey: build.SourcePreparedDatasetKey, FeatureContractVersion: build.FeatureContractVersion,
		RequestCount: build.RequestCount, UniqueWindowCount: build.UniqueWindowCount,
		EligibleRequestCount: build.EligibleRequestCount, TelemetryCensoredCount: build.TelemetryCensoredCount,
		LowCoverageCount: build.LowCoverageCount, DiscontinuousCount: build.DiscontinuousCount,
		LoadUnknownCount: build.LoadUnknownCount, LoadMismatchCount: build.LoadMismatchCount,
		ExtractionFailedCount: build.ExtractionFailedCount,
		TelemetryPolicy:       "metric coverage must meet the source preparation threshold and core telemetry continuity must be at least 70%",
		LoadPolicy:            "positive and healthy-control trailing-24h mean GPU utilization must be in the same idle/moderate/high bucket",
		CreatedAt:             s.now(),
	}
	if err := writeJSONAtomic(reportPath, report); err != nil {
		return err
	}
	status, errorMessage := "completed", ""
	if build.EligibleRequestCount == 0 {
		status, errorMessage = "failed", "no healthy-control request passed telemetry and load quality gates"
	}
	finished := s.now()
	return s.db.Model(build).Updates(map[string]any{
		"status": status, "processed_unique_windows": build.ProcessedUniqueWindows,
		"completed_request_count": build.CompletedRequestCount, "eligible_request_count": build.EligibleRequestCount,
		"telemetry_censored_count": build.TelemetryCensoredCount, "low_coverage_count": build.LowCoverageCount,
		"discontinuous_count": build.DiscontinuousCount, "load_unknown_count": build.LoadUnknownCount,
		"load_mismatch_count": build.LoadMismatchCount, "extraction_failed_count": build.ExtractionFailedCount,
		"feature_path": featurePath, "feature_sha256": checksum, "quality_report_path": reportPath,
		"error_message": errorMessage, "finished_at": &finished,
	}).Error
}

func (s *Service) extractControlFeature(client *promclient.Client, build *api.TrainingControlFeatureBuild, task controlQueryTask) (extractedFeatureRow, error) {
	request := task.request
	window := datasetWindow{
		SampleKey: task.key, EpisodeKey: request.EpisodeKey, NodeIP: request.NodeIP,
		GPUUUID: request.GPUUUID, ModelName: request.ModelName, HorizonMinutes: request.HorizonMinutes,
		FeatureCutoffAt: request.FeatureCutoffAt,
	}
	featureBuild := api.TrainingFeatureBuild{
		SourceDatasetKey: build.SourcePreparedDatasetKey, FeatureContractVersion: build.FeatureContractVersion,
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	series, err := client.QueryRange(ctx, historicalMetricQuery(request.GPUUUID),
		request.FeatureCutoffAt.Add(-featureLookback), request.FeatureCutoffAt, featureQueryStep)
	cancel()
	if err != nil {
		return emptyExtractedFeatureRow(&featureBuild, window, err.Error()), err
	}
	return summarizeFeatureWindow(&featureBuild, window, canonicalSeries(series)), nil
}

func qualifyControlFeature(request healthyControlRequest, feature, positive extractedFeatureRow, minimumCoverage float64) controlFeatureRow {
	row := controlFeatureRow{Request: request, Feature: feature, TrainingStatus: "excluded"}
	row.TelemetryContinuity = coreTelemetryContinuity(feature)
	positiveUtil, positiveOK := positive.Features["gpu_util_mean_24h"]
	controlUtil, controlOK := feature.Features["gpu_util_mean_24h"]
	if positiveOK {
		row.PositiveGPUMeanUtil = floatPointer(positiveUtil)
		row.PositiveLoadBucket = gpuLoadBucket(positiveUtil)
	}
	if controlOK {
		row.ControlGPUMeanUtil = floatPointer(controlUtil)
		row.ControlLoadBucket = gpuLoadBucket(controlUtil)
	}
	switch {
	case feature.ExtractionError != "":
		row.ExclusionReason = "extraction_failed"
	case feature.MetricCoverage == 0:
		row.ExclusionReason = "telemetry_censored"
	case feature.MetricCoverage < minimumCoverage:
		row.ExclusionReason = "metric_coverage_below_threshold"
	case row.TelemetryContinuity < minimumTelemetryContinuity:
		row.ExclusionReason = "telemetry_discontinuous"
	case !positiveOK || !controlOK:
		row.ExclusionReason = "load_unknown"
	case row.PositiveLoadBucket != row.ControlLoadBucket:
		row.ExclusionReason = "load_mismatch"
	default:
		row.TrainingStatus = "eligible"
	}
	return row
}

func coreTelemetryContinuity(feature extractedFeatureRow) float64 {
	expected := float64(featureLookback/featureQueryStep) + 1
	minimum := float64(1)
	for _, metric := range []string{"gpu_temp", "power_usage", "gpu_util"} {
		count, exists := feature.Features[metric+"_sample_count_24h"]
		if !exists {
			return 0
		}
		minimum = math.Min(minimum, count/expected)
	}
	return math.Min(1, minimum)
}

func gpuLoadBucket(utilization float64) string {
	switch {
	case utilization < 10:
		return "idle"
	case utilization < 60:
		return "moderate"
	default:
		return "high"
	}
}

func controlFeatureWindowKey(request healthyControlRequest) string {
	return normalizeHistoricalGPUUUID(request.GPUUUID) + "|" + request.FeatureCutoffAt.UTC().Format(time.RFC3339Nano)
}

func floatPointer(value float64) *float64 { return &value }

func readJSONLines[T any](path string) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	rows := make([]T, 0, 1024)
	for scanner.Scan() {
		var row T
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, scanner.Err()
}

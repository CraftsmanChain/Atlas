package history

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"atlas/internal/features"
	"atlas/internal/featurestats"
	promclient "atlas/internal/prometheus"
	"atlas/pkg/api"
)

const (
	featureDatasetVersion = "gpu-historical-features-v2"
	featureLookback       = 24 * time.Hour
	featureQueryStep      = 5 * time.Minute
)

type FeatureBuildRequest struct {
	SourceDatasetBuildID uint `json:"source_dataset_build_id"`
	MaxEpisodes          int  `json:"max_episodes,omitempty"`
}

type historicalMetric struct {
	Name      string
	Canonical string
	Priority  int
}

var historicalFeatureMetrics = []historicalMetric{
	{Name: "DCGM_FI_DEV_GPU_TEMP", Canonical: "gpu_temp"},
	{Name: "nvidia_smi_temperature_gpu", Canonical: "gpu_temp", Priority: 1},
	{Name: "DCGM_FI_DEV_MEMORY_TEMP", Canonical: "memory_temp"},
	{Name: "nvidia_smi_temperature_memory", Canonical: "memory_temp", Priority: 1},
	{Name: "DCGM_FI_DEV_POWER_USAGE", Canonical: "power_usage"},
	{Name: "nvidia_smi_power_draw_watts", Canonical: "power_usage", Priority: 1},
	{Name: "DCGM_FI_DEV_GPU_UTIL", Canonical: "gpu_util"},
	{Name: "nvidia_smi_utilization_gpu_ratio", Canonical: "gpu_util_ratio", Priority: 1},
	{Name: "DCGM_FI_DEV_MEM_COPY_UTIL", Canonical: "mem_copy_util"},
	{Name: "nvidia_smi_utilization_memory_ratio", Canonical: "mem_copy_util_ratio", Priority: 1},
	{Name: "DCGM_FI_DEV_SM_CLOCK", Canonical: "sm_clock"},
	{Name: "nvidia_smi_clocks_current_sm_clock_hz", Canonical: "sm_clock_hz", Priority: 1},
	{Name: "DCGM_FI_DEV_MEM_CLOCK", Canonical: "mem_clock"},
	{Name: "nvidia_smi_clocks_current_memory_clock_hz", Canonical: "mem_clock_hz", Priority: 1},
	{Name: "DCGM_FI_DEV_FB_USED", Canonical: "fb_used"},
	{Name: "nvidia_smi_memory_used_bytes", Canonical: "fb_used_bytes", Priority: 1},
	{Name: "DCGM_FI_DEV_FB_FREE", Canonical: "fb_free"},
	{Name: "nvidia_smi_memory_free_bytes", Canonical: "fb_free_bytes", Priority: 1},
	{Name: "DCGM_FI_DEV_XID_ERRORS", Canonical: "xid_current"},
	{Name: "DCGM_FI_DEV_PCIE_REPLAY_COUNTER", Canonical: "pcie_replay_counter"},
	{Name: "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", Canonical: "uncorrectable_remapped_rows"},
	{Name: "nvidia_smi_remapped_rows_uncorrectable", Canonical: "uncorrectable_remapped_rows", Priority: 1},
	{Name: "DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS", Canonical: "correctable_remapped_rows"},
	{Name: "nvidia_smi_remapped_rows_correctable", Canonical: "correctable_remapped_rows", Priority: 1},
	{Name: "DCGM_FI_DEV_ROW_REMAP_FAILURE", Canonical: "row_remap_failure"},
	{Name: "nvidia_smi_remapped_rows_failure", Canonical: "row_remap_failure", Priority: 1},
	{Name: "nvidia_smi_ecc_errors_uncorrected_aggregate_total", Canonical: "uncorrected_ecc_aggregate"},
	{Name: "nvidia_smi_ecc_errors_uncorrected_volatile_total", Canonical: "uncorrected_ecc_volatile"},
	{Name: "nvidia_smi_pcie_link_width_current", Canonical: "pcie_link_width_current"},
	{Name: "nvidia_smi_reset_status_reset_required", Canonical: "gpu_reset_required"},
}

type extractedFeatureRow struct {
	SampleKey             string             `json:"sample_key"`
	FeatureDatasetVersion string             `json:"feature_dataset_version"`
	SourceDatasetKey      string             `json:"source_dataset_key"`
	EpisodeKey            string             `json:"episode_key"`
	NodeIP                string             `json:"node_ip"`
	GPUUUID               string             `json:"gpu_uuid"`
	ModelName             string             `json:"model_name"`
	HorizonMinutes        int                `json:"horizon_minutes"`
	FeatureCutoffAt       time.Time          `json:"feature_cutoff_at"`
	LabelOnsetAt          time.Time          `json:"label_onset_at"`
	LabelWeight           float64            `json:"label_weight"`
	FeatureContract       string             `json:"feature_contract_version"`
	LookbackMinutes       int                `json:"lookback_minutes"`
	QueryStepSeconds      int                `json:"query_step_seconds"`
	MetricCoverage        float64            `json:"metric_coverage"`
	AvailableMetrics      int                `json:"available_metrics"`
	ExpectedMetrics       int                `json:"expected_metrics"`
	MissingMetrics        []string           `json:"missing_metrics"`
	Features              map[string]float64 `json:"features"`
	ExtractionError       string             `json:"extraction_error,omitempty"`
}

type featureQualityReport struct {
	FeatureDatasetKey     string         `json:"feature_dataset_key"`
	Version               string         `json:"version"`
	SourceDatasetKey      string         `json:"source_dataset_key"`
	FeatureContract       string         `json:"feature_contract_version"`
	PointInTimeRule       string         `json:"point_in_time_rule"`
	LookbackMinutes       int            `json:"lookback_minutes"`
	QueryStepSeconds      int            `json:"query_step_seconds"`
	EpisodeCount          int            `json:"episode_count"`
	WindowCount           int            `json:"window_count"`
	CompletedWindows      int            `json:"completed_windows"`
	FailedWindows         int            `json:"failed_windows"`
	MetricCount           int            `json:"metric_count"`
	FeatureColumnCount    int            `json:"feature_column_count"`
	FeatureColumns        []string       `json:"feature_columns"`
	AverageCoverage       float64        `json:"average_metric_coverage"`
	MinimumCoverage       float64        `json:"minimum_metric_coverage"`
	MetricAvailableCounts map[string]int `json:"metric_available_window_counts"`
	CreatedAt             time.Time      `json:"created_at"`
}

type episodeFeatureResult struct {
	index int
	rows  []extractedFeatureRow
	err   error
}

func (s *Service) FeatureBuilds(limit int) ([]api.TrainingFeatureBuild, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.TrainingFeatureBuild
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) StartFeatureBuild(request FeatureBuildRequest) (api.TrainingFeatureBuild, error) {
	s.featureMu.Lock()
	defer s.featureMu.Unlock()
	if s.featureRunning {
		return api.TrainingFeatureBuild{}, fmt.Errorf("a historical feature extraction is already running")
	}
	var sourceBuild api.TrainingDatasetBuild
	query := s.db.Where("status = ? AND version = ?", "completed", datasetBuildVersion)
	if request.SourceDatasetBuildID > 0 {
		query = query.Where("id = ?", request.SourceDatasetBuildID)
	}
	result := query.Order("finished_at DESC, id DESC").Limit(1).Find(&sourceBuild)
	if result.Error != nil {
		return api.TrainingFeatureBuild{}, result.Error
	}
	if result.RowsAffected == 0 {
		return api.TrainingFeatureBuild{}, fmt.Errorf("a completed cohort manifest is required before feature extraction")
	}
	if request.MaxEpisodes < 0 || request.MaxEpisodes > 10000 {
		return api.TrainingFeatureBuild{}, fmt.Errorf("max_episodes must be between 0 and 10000")
	}
	if _, err := s.resolveSource(sourceBuild.SourceKey); err != nil {
		return api.TrainingFeatureBuild{}, err
	}
	started := s.now()
	key := featureDatasetVersion + "-" + strconv.FormatInt(started.UTC().UnixNano(), 10)
	build := api.TrainingFeatureBuild{
		FeatureDatasetKey: key, Version: featureDatasetVersion, Status: "queued",
		SourceKey: sourceBuild.SourceKey, SourceDatasetBuildID: sourceBuild.ID,
		SourceDatasetKey: sourceBuild.DatasetKey, FeatureContractVersion: features.CatalogVersion,
		LookbackMinutes: int(featureLookback / time.Minute), QueryStepSeconds: int(featureQueryStep / time.Second),
		MetricCount: len(canonicalHistoricalMetrics()), OutputDir: filepath.Join(s.config.DatasetDir, "features", key),
		StartedAt: started,
	}
	if err := s.db.Create(&build).Error; err != nil {
		return build, err
	}
	s.featureRunning = true
	go s.executeFeatureBuild(build.ID, request.MaxEpisodes)
	return build, nil
}

func (s *Service) executeFeatureBuild(buildID uint, maxEpisodes int) {
	defer func() {
		s.featureMu.Lock()
		s.featureRunning = false
		s.featureMu.Unlock()
	}()
	var build api.TrainingFeatureBuild
	if err := s.db.First(&build, buildID).Error; err != nil {
		return
	}
	if err := s.db.Model(&build).Update("status", "running").Error; err != nil {
		return
	}
	if err := s.buildHistoricalFeatures(&build, maxEpisodes); err != nil {
		finished := s.now()
		_ = s.db.Model(&build).Updates(map[string]any{
			"status": "failed", "error_message": err.Error(), "finished_at": &finished,
		}).Error
	}
}

func (s *Service) buildHistoricalFeatures(build *api.TrainingFeatureBuild, maxEpisodes int) error {
	var sourceBuild api.TrainingDatasetBuild
	if err := s.db.First(&sourceBuild, build.SourceDatasetBuildID).Error; err != nil {
		return err
	}
	if err := verifyFileSHA256(sourceBuild.WindowManifestPath, sourceBuild.WindowManifestSHA256); err != nil {
		return fmt.Errorf("source cohort checksum: %w", err)
	}
	windows, err := readDatasetWindows(sourceBuild.WindowManifestPath)
	if err != nil {
		return err
	}
	grouped := groupDatasetWindows(windows)
	if maxEpisodes > 0 && len(grouped) > maxEpisodes {
		grouped = grouped[:maxEpisodes]
	}
	totalWindows := 0
	for _, episode := range grouped {
		totalWindows += len(episode)
	}
	if err := s.db.Model(build).Updates(map[string]any{
		"episode_count": len(grouped), "window_count": totalWindows,
	}).Error; err != nil {
		return err
	}
	build.EpisodeCount = len(grouped)
	build.WindowCount = totalWindows
	if err := os.MkdirAll(build.OutputDir, 0o750); err != nil {
		return fmt.Errorf("create feature dataset directory: %w", err)
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
	if workerCount > len(grouped) {
		workerCount = len(grouped)
	}
	jobs := make(chan int)
	results := make(chan episodeFeatureResult, len(grouped))
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				rows, queryErr := s.extractEpisodeFeatures(client, build, grouped[index])
				results <- episodeFeatureResult{index: index, rows: rows, err: queryErr}
			}
		}()
	}
	go func() {
		for index := range grouped {
			jobs <- index
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()

	ordered := make([][]extractedFeatureRow, len(grouped))
	processed, failedWindows := 0, 0
	for result := range results {
		ordered[result.index] = result.rows
		if result.err != nil {
			failedWindows += len(grouped[result.index])
		}
		processed++
		if processed%10 == 0 || processed == len(grouped) {
			_ = s.db.Model(build).Updates(map[string]any{
				"processed_episodes": processed, "failed_windows": failedWindows,
				"completed_windows": totalWindows - failedWindows,
			}).Error
		}
	}
	rows := make([]extractedFeatureRow, 0, totalWindows)
	for _, episodeRows := range ordered {
		rows = append(rows, episodeRows...)
	}
	featurePath := filepath.Join(build.OutputDir, "features.jsonl")
	checksum, err := writeFeatureRows(featurePath, rows)
	if err != nil {
		return err
	}
	report := buildFeatureQualityReport(*build, rows)
	reportPath := filepath.Join(build.OutputDir, "quality_report.json")
	if err := writeJSONAtomic(reportPath, report); err != nil {
		return err
	}
	status, errorMessage := "completed", ""
	if report.FailedWindows > 0 {
		status = "completed_with_errors"
	}
	if report.CompletedWindows == 0 && report.WindowCount > 0 {
		status = "failed"
		errorMessage = "all Prometheus feature queries failed; inspect extraction_error in features.jsonl"
	}
	finished := s.now()
	return s.db.Model(build).Updates(map[string]any{
		"status": status, "processed_episodes": report.EpisodeCount,
		"completed_windows": report.CompletedWindows, "failed_windows": report.FailedWindows,
		"feature_column_count":    report.FeatureColumnCount,
		"average_metric_coverage": report.AverageCoverage, "minimum_metric_coverage": report.MinimumCoverage,
		"feature_path": featurePath, "feature_sha256": checksum, "quality_report_path": reportPath,
		"error_message": errorMessage, "finished_at": &finished,
	}).Error
}

func readDatasetWindows(path string) ([]datasetWindow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open sample-window manifest: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	rows := make([]datasetWindow, 0, 1024)
	for scanner.Scan() {
		var row datasetWindow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("decode sample-window manifest: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, scanner.Err()
}

func groupDatasetWindows(rows []datasetWindow) [][]datasetWindow {
	byEpisode := map[string][]datasetWindow{}
	keys := make([]string, 0)
	for _, row := range rows {
		if _, exists := byEpisode[row.EpisodeKey]; !exists {
			keys = append(keys, row.EpisodeKey)
		}
		byEpisode[row.EpisodeKey] = append(byEpisode[row.EpisodeKey], row)
	}
	sort.Strings(keys)
	result := make([][]datasetWindow, 0, len(keys))
	for _, key := range keys {
		episode := byEpisode[key]
		sort.Slice(episode, func(i, j int) bool { return episode[i].FeatureCutoffAt.Before(episode[j].FeatureCutoffAt) })
		result = append(result, episode)
	}
	return result
}

func (s *Service) extractEpisodeFeatures(client *promclient.Client, build *api.TrainingFeatureBuild, windows []datasetWindow) ([]extractedFeatureRow, error) {
	if len(windows) == 0 {
		return nil, nil
	}
	start := windows[0].FeatureCutoffAt.Add(-featureLookback)
	end := windows[len(windows)-1].FeatureCutoffAt
	query := historicalMetricQuery(windows[0].GPUUUID)
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	series, err := client.QueryRange(ctx, query, start, end, featureQueryStep)
	cancel()
	if err != nil {
		rows := make([]extractedFeatureRow, 0, len(windows))
		for _, window := range windows {
			rows = append(rows, emptyExtractedFeatureRow(build, window, err.Error()))
		}
		return rows, err
	}
	points := canonicalSeries(series)
	rows := make([]extractedFeatureRow, 0, len(windows))
	for _, window := range windows {
		rows = append(rows, summarizeFeatureWindow(build, window, points))
	}
	return rows, nil
}

func historicalMetricQuery(uuid string) string {
	return historicalMetricQueryUUIDs([]string{uuid})
}

func historicalMetricQueryUUIDs(uuids []string) string {
	names := make([]string, 0, len(historicalFeatureMetrics))
	for _, metric := range historicalFeatureMetrics {
		names = append(names, regexp.QuoteMeta(metric.Name))
	}
	sort.Strings(names)
	uuidParts := make([]string, 0, len(uuids))
	for _, uuid := range uuids {
		if value := strings.TrimSpace(uuid); value != "" {
			uuidParts = append(uuidParts, regexp.QuoteMeta(value))
		}
	}
	sort.Strings(uuidParts)
	uuidExpression := "(?i)^(" + strings.Join(uuidParts, "|") + ")$"
	metricExpression := "^(" + strings.Join(names, "|") + ")$"
	return fmt.Sprintf(`{__name__=~%q,UUID=~%q} or {__name__=~%q,uuid=~%q}`,
		metricExpression, uuidExpression, metricExpression, uuidExpression)
}

func canonicalHistoricalMetrics() []string {
	set := map[string]struct{}{}
	for _, metric := range historicalFeatureMetrics {
		set[normalizedCanonicalName(metric.Canonical)] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func normalizedCanonicalName(name string) string {
	return strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, "_ratio"), "_hz"), "_bytes")
}

type canonicalPoints struct {
	priority int
	points   []promclient.RangePoint
}

func canonicalSeries(series []promclient.RangeSeries) map[string][]promclient.RangePoint {
	byName := map[string]canonicalPoints{}
	raw := map[string]historicalMetric{}
	for _, metric := range historicalFeatureMetrics {
		raw[metric.Name] = metric
	}
	for _, item := range series {
		spec, exists := raw[item.Metric["__name__"]]
		if !exists {
			continue
		}
		name := normalizedCanonicalName(spec.Canonical)
		points := normalizeHistoricalPoints(spec.Canonical, item.Values)
		current, exists := byName[name]
		if !exists || spec.Priority < current.priority {
			byName[name] = canonicalPoints{priority: spec.Priority, points: points}
		} else if spec.Priority == current.priority {
			current.points = mergeRangePointsMax(current.points, points)
			byName[name] = current
		}
	}
	result := make(map[string][]promclient.RangePoint, len(byName))
	for name, value := range byName {
		result[name] = value.points
	}
	return result
}

func mergeRangePointsMax(left, right []promclient.RangePoint) []promclient.RangePoint {
	byTimestamp := make(map[int64]promclient.RangePoint, len(left)+len(right))
	for _, point := range append(append([]promclient.RangePoint(nil), left...), right...) {
		key := point.Timestamp.UnixNano()
		existing, exists := byTimestamp[key]
		if !exists || point.Value > existing.Value {
			byTimestamp[key] = point
		}
	}
	result := make([]promclient.RangePoint, 0, len(byTimestamp))
	for _, point := range byTimestamp {
		result = append(result, point)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Timestamp.Before(result[j].Timestamp) })
	return result
}

func normalizeHistoricalPoints(name string, points []promclient.RangePoint) []promclient.RangePoint {
	scale := float64(1)
	switch {
	case strings.HasSuffix(name, "_ratio"):
		scale = 100
	case strings.HasSuffix(name, "_hz"):
		scale = 1.0 / 1_000_000
	case strings.HasSuffix(name, "_bytes"):
		scale = 1.0 / 1_048_576
	}
	result := make([]promclient.RangePoint, len(points))
	for index, point := range points {
		result[index] = point
		result[index].Value *= scale
	}
	return result
}

func summarizeFeatureWindow(build *api.TrainingFeatureBuild, window datasetWindow, all map[string][]promclient.RangePoint) extractedFeatureRow {
	row := emptyExtractedFeatureRow(build, window, "")
	start := window.FeatureCutoffAt.Add(-featureLookback)
	for _, metric := range expectedHistoricalMetrics(window.ModelName) {
		points := pointsInWindow(all[metric], start, window.FeatureCutoffAt)
		if len(points) == 0 {
			row.MissingMetrics = append(row.MissingMetrics, metric)
			continue
		}
		row.AvailableMetrics++
		featurestats.AddTrailing24hStatistics(row.Features, metric, points)
	}
	row.MetricCoverage = float64(row.AvailableMetrics) / float64(row.ExpectedMetrics)
	return row
}

func emptyExtractedFeatureRow(build *api.TrainingFeatureBuild, window datasetWindow, extractionError string) extractedFeatureRow {
	metrics := expectedHistoricalMetrics(window.ModelName)
	row := extractedFeatureRow{
		SampleKey: window.SampleKey, FeatureDatasetVersion: featureDatasetVersion,
		SourceDatasetKey: build.SourceDatasetKey, EpisodeKey: window.EpisodeKey,
		NodeIP: window.NodeIP, GPUUUID: window.GPUUUID, ModelName: window.ModelName,
		HorizonMinutes: window.HorizonMinutes, FeatureCutoffAt: window.FeatureCutoffAt,
		LabelOnsetAt: window.LabelOnsetAt, LabelWeight: window.LabelWeight,
		FeatureContract: features.CatalogVersion, LookbackMinutes: int(featureLookback / time.Minute),
		QueryStepSeconds: int(featureQueryStep / time.Second), ExpectedMetrics: len(metrics),
		Features: map[string]float64{}, ExtractionError: extractionError,
	}
	if extractionError != "" {
		row.MissingMetrics = metrics
	}
	return row
}

func pointsInWindow(points []promclient.RangePoint, start, end time.Time) []promclient.RangePoint {
	result := make([]promclient.RangePoint, 0, len(points))
	for _, point := range points {
		if !point.Timestamp.Before(start) && !point.Timestamp.After(end) && !math.IsNaN(point.Value) && !math.IsInf(point.Value, 0) {
			result = append(result, point)
		}
	}
	return result
}

func writeFeatureRows(path string, rows []extractedFeatureRow) (string, error) {
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", err
	}
	checksum := sha256.New()
	encoder := json.NewEncoder(io.MultiWriter(file, checksum))
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			_ = file.Close()
			return "", err
		}
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, path); err != nil {
		return "", err
	}
	return hex.EncodeToString(checksum.Sum(nil)), nil
}

func buildFeatureQualityReport(build api.TrainingFeatureBuild, rows []extractedFeatureRow) featureQualityReport {
	report := featureQualityReport{
		FeatureDatasetKey: build.FeatureDatasetKey, Version: featureDatasetVersion,
		SourceDatasetKey: build.SourceDatasetKey, FeatureContract: features.CatalogVersion,
		PointInTimeRule: "every Prometheus sample timestamp must be <= feature_cutoff_at and strictly before label_onset_at",
		LookbackMinutes: int(featureLookback / time.Minute), QueryStepSeconds: int(featureQueryStep / time.Second),
		EpisodeCount: build.EpisodeCount, WindowCount: len(rows), MetricCount: len(canonicalHistoricalMetrics()),
		MetricAvailableCounts: map[string]int{}, MinimumCoverage: 1, CreatedAt: time.Now(),
	}
	for _, row := range rows {
		if row.ExtractionError != "" {
			report.FailedWindows++
		} else {
			report.CompletedWindows++
		}
		report.AverageCoverage += row.MetricCoverage
		report.MinimumCoverage = math.Min(report.MinimumCoverage, row.MetricCoverage)
		for _, metric := range expectedHistoricalMetrics(row.ModelName) {
			missing := false
			for _, missingMetric := range row.MissingMetrics {
				if metric == missingMetric {
					missing = true
					break
				}
			}
			if !missing {
				report.MetricAvailableCounts[metric]++
			}
		}
	}
	if len(rows) > 0 {
		report.AverageCoverage /= float64(len(rows))
	} else {
		report.MinimumCoverage = 0
	}
	report.FeatureColumns = historicalFeatureColumns()
	report.FeatureColumnCount = len(report.FeatureColumns)
	return report
}

func expectedHistoricalMetrics(modelName string) []string {
	metrics := canonicalHistoricalMetrics()
	model := strings.ToUpper(strings.TrimSpace(modelName))
	if model == "" || strings.Contains(model, "H100") || strings.Contains(model, "H200") {
		return metrics
	}
	result := make([]string, 0, len(metrics)-2)
	for _, metric := range metrics {
		if metric == "uncorrected_ecc_aggregate" || metric == "uncorrected_ecc_volatile" {
			continue
		}
		result = append(result, metric)
	}
	return result
}

func historicalFeatureColumns() []string {
	suffixes := []string{
		"last_24h", "mean_24h", "min_24h", "max_24h", "stddev_24h",
		"delta_24h", "slope_per_hour_24h", "sample_count_24h",
	}
	columns := make([]string, 0, len(canonicalHistoricalMetrics())*len(suffixes))
	for _, metric := range canonicalHistoricalMetrics() {
		for _, suffix := range suffixes {
			columns = append(columns, metric+"_"+suffix)
		}
	}
	return columns
}

func verifyFileSHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, strings.TrimSpace(expected)) {
		return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

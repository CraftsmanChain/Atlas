package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"atlas/pkg/api"
)

const preparationDatasetVersion = "gpu-training-preparation-v1"

type PreparationBuildRequest struct {
	SourceFeatureBuildID uint    `json:"source_feature_build_id"`
	MinimumCoverage      float64 `json:"minimum_coverage,omitempty"`
	ControlsPerPositive  int     `json:"controls_per_positive,omitempty"`
}

type preparedTrainingSample struct {
	Sample          extractedFeatureRow `json:"sample"`
	LabelValue      int                 `json:"label_value"`
	TrainingStatus  string              `json:"training_status"`
	ExclusionReason string              `json:"exclusion_reason,omitempty"`
	Split           string              `json:"split,omitempty"`
}

type healthyControlRequest struct {
	ControlKey        string    `json:"control_key"`
	PairedSampleKey   string    `json:"paired_sample_key"`
	EpisodeKey        string    `json:"episode_key"`
	GPUUUID           string    `json:"gpu_uuid"`
	NodeIP            string    `json:"node_ip"`
	ModelName         string    `json:"model_name"`
	DataCenterID      string    `json:"data_center_id,omitempty"`
	DriverVersion     string    `json:"driver_version,omitempty"`
	HorizonMinutes    int       `json:"horizon_minutes"`
	FeatureCutoffAt   time.Time `json:"feature_cutoff_at"`
	LookbackMinutes   int       `json:"lookback_minutes"`
	Split             string    `json:"split"`
	LabelValue        int       `json:"label_value"`
	Eligibility       string    `json:"eligibility"`
	ContaminationRule string    `json:"contamination_rule"`
}

type preparationManifest struct {
	PreparedDatasetKey      string    `json:"prepared_dataset_key"`
	Version                 string    `json:"version"`
	SourceFeatureDatasetKey string    `json:"source_feature_dataset_key"`
	MinimumCoverage         float64   `json:"minimum_metric_coverage"`
	ControlsPerPositive     int       `json:"controls_per_positive"`
	TrainEndAt              time.Time `json:"train_end_at"`
	ValidationEndAt         time.Time `json:"validation_end_at"`
	SplitPolicy             string    `json:"split_policy"`
	QualityPolicy           string    `json:"quality_policy"`
	ControlPolicy           string    `json:"control_policy"`
	PreparedSamples         string    `json:"prepared_samples"`
	PreparedSamplesSHA256   string    `json:"prepared_samples_sha256"`
	ControlRequests         string    `json:"control_requests"`
	ControlRequestsSHA256   string    `json:"control_requests_sha256"`
	CreatedAt               time.Time `json:"created_at"`
}

type entityTimeRange struct {
	min time.Time
	max time.Time
}

func (s *Service) PreparationBuilds(limit int) ([]api.TrainingPreparationBuild, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.TrainingPreparationBuild
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) BuildTrainingPreparation(request PreparationBuildRequest) (api.TrainingPreparationBuild, error) {
	s.preparationMu.Lock()
	defer s.preparationMu.Unlock()

	minimumCoverage := request.MinimumCoverage
	if minimumCoverage == 0 {
		minimumCoverage = 0.7
	}
	if minimumCoverage < 0.5 || minimumCoverage > 1 {
		return api.TrainingPreparationBuild{}, fmt.Errorf("minimum_coverage must be between 0.5 and 1")
	}
	controlsPerPositive := request.ControlsPerPositive
	if controlsPerPositive == 0 {
		controlsPerPositive = 3
	}
	if controlsPerPositive < 1 || controlsPerPositive > 5 {
		return api.TrainingPreparationBuild{}, fmt.Errorf("controls_per_positive must be between 1 and 5")
	}
	var source api.TrainingFeatureBuild
	query := s.db.Where("status = ? AND version = ?", "completed", featureDatasetVersion)
	if request.SourceFeatureBuildID > 0 {
		query = query.Where("id = ?", request.SourceFeatureBuildID)
	}
	result := query.Order("finished_at DESC, id DESC").Limit(1).Find(&source)
	if result.Error != nil {
		return api.TrainingPreparationBuild{}, result.Error
	}
	if result.RowsAffected == 0 {
		return api.TrainingPreparationBuild{}, fmt.Errorf("a completed full feature dataset %s is required", featureDatasetVersion)
	}
	var cohort api.TrainingDatasetBuild
	if err := s.db.First(&cohort, source.SourceDatasetBuildID).Error; err != nil {
		return api.TrainingPreparationBuild{}, err
	}
	if source.EpisodeCount != cohort.EpisodeCount || source.WindowCount != cohort.WindowCount {
		return api.TrainingPreparationBuild{}, fmt.Errorf("full feature extraction is required; source has %d/%d episodes and %d/%d windows",
			source.EpisodeCount, cohort.EpisodeCount, source.WindowCount, cohort.WindowCount)
	}
	if err := verifyFileSHA256(source.FeaturePath, source.FeatureSHA256); err != nil {
		return api.TrainingPreparationBuild{}, fmt.Errorf("source feature checksum: %w", err)
	}
	started := s.now()
	key := preparationDatasetVersion + "-" + strconv.FormatInt(started.UTC().UnixNano(), 10)
	build := api.TrainingPreparationBuild{
		PreparedDatasetKey: key, Version: preparationDatasetVersion, Status: "running",
		SourceFeatureBuildID: source.ID, SourceFeatureDatasetKey: source.FeatureDatasetKey,
		MinimumMetricCoverage: minimumCoverage, SourceWindowCount: source.WindowCount,
		OutputDir: filepath.Join(s.config.DatasetDir, "prepared", key), StartedAt: started,
	}
	if err := s.db.Create(&build).Error; err != nil {
		return build, err
	}
	if err := s.buildTrainingPreparation(&build, source, controlsPerPositive); err != nil {
		finished := s.now()
		_ = s.db.Model(&build).Updates(map[string]any{
			"status": "failed", "error_message": err.Error(), "finished_at": &finished,
		}).Error
		build.Status, build.ErrorMessage, build.FinishedAt = "failed", err.Error(), &finished
		return build, err
	}
	return build, s.db.First(&build, build.ID).Error
}

func (s *Service) buildTrainingPreparation(build *api.TrainingPreparationBuild, source api.TrainingFeatureBuild, controlsPerPositive int) error {
	rows, err := readExtractedFeatureRows(source.FeaturePath)
	if err != nil {
		return err
	}
	eligibleForSplit := make([]extractedFeatureRow, 0, len(rows))
	for _, row := range rows {
		if row.ExtractionError == "" && row.MetricCoverage >= build.MinimumMetricCoverage {
			eligibleForSplit = append(eligibleForSplit, row)
		}
	}
	trainEnd, validationEnd, err := splitBoundaries(eligibleForSplit)
	if err != nil {
		return err
	}
	splits := entityIsolatedSplits(eligibleForSplit, trainEnd, validationEnd)
	prepared := make([]preparedTrainingSample, 0, len(rows))
	for _, row := range rows {
		item := preparedTrainingSample{Sample: row, LabelValue: 1}
		switch {
		case row.ExtractionError != "":
			item.TrainingStatus, item.ExclusionReason = "excluded", "extraction_failed"
			build.ExtractionFailedCount++
		case row.MetricCoverage == 0:
			item.TrainingStatus, item.ExclusionReason = "excluded", "telemetry_censored"
			build.TelemetryCensoredCount++
		case row.MetricCoverage < build.MinimumMetricCoverage:
			item.TrainingStatus, item.ExclusionReason = "excluded", "metric_coverage_below_threshold"
			build.LowCoverageCount++
		case splits[row.GPUUUID] == "":
			item.TrainingStatus, item.ExclusionReason = "excluded", "entity_crosses_time_split_boundary"
			build.EntityTimeConflictCount++
		default:
			item.TrainingStatus, item.Split = "eligible", splits[row.GPUUUID]
			build.EligiblePositiveCount++
			switch item.Split {
			case "train":
				build.TrainCount++
			case "validation":
				build.ValidationCount++
			case "test":
				build.TestCount++
			}
		}
		prepared = append(prepared, item)
	}

	var intervals []api.HistoricalGPUIdentityInterval
	if err := s.db.Order("first_seen_at, id").Find(&intervals).Error; err != nil {
		return err
	}
	intervalsByGPU := map[string][]api.HistoricalGPUIdentityInterval{}
	for _, interval := range intervals {
		key := normalizeHistoricalGPUUUID(interval.GPUUUID)
		intervalsByGPU[key] = append(intervalsByGPU[key], interval)
	}
	faultsByGPU := faultTimesByGPU(rows)
	controls := make([]healthyControlRequest, 0, build.EligiblePositiveCount*controlsPerPositive)
	for _, item := range prepared {
		if item.TrainingStatus != "eligible" {
			continue
		}
		selected := healthyControlCutoffs(item.Sample, intervalsByGPU[normalizeHistoricalGPUUUID(item.Sample.GPUUUID)], faultsByGPU[normalizeHistoricalGPUUUID(item.Sample.GPUUUID)], controlsPerPositive)
		if len(selected) < controlsPerPositive {
			build.ControlShortfallCount += controlsPerPositive - len(selected)
		}
		for _, selectedControl := range selected {
			material := item.Sample.SampleKey + "|" + selectedControl.cutoff.UTC().Format(time.RFC3339Nano)
			sum := sha256.Sum256([]byte(material))
			controls = append(controls, healthyControlRequest{
				ControlKey: hex.EncodeToString(sum[:]), PairedSampleKey: item.Sample.SampleKey,
				EpisodeKey: item.Sample.EpisodeKey, GPUUUID: item.Sample.GPUUUID, NodeIP: selectedControl.interval.NodeIP,
				ModelName: selectedControl.interval.ModelName, DataCenterID: selectedControl.interval.DataCenterID,
				DriverVersion: selectedControl.interval.DriverVersion, HorizonMinutes: item.Sample.HorizonMinutes,
				FeatureCutoffAt: selectedControl.cutoff, LookbackMinutes: int(featureLookback / time.Minute),
				Split: item.Split, LabelValue: 0, Eligibility: "pending_telemetry_and_load_validation",
				ContaminationRule: "outside every known fault interval [-168h,+72h] with stable GPU identity for the full lookback",
			})
		}
	}
	build.ControlRequestCount = len(controls)
	if err := os.MkdirAll(build.OutputDir, 0o750); err != nil {
		return err
	}
	preparedPath := filepath.Join(build.OutputDir, "prepared_positive_samples.jsonl")
	preparedSHA, err := writeJSONLines(preparedPath, prepared)
	if err != nil {
		return err
	}
	controlPath := filepath.Join(build.OutputDir, "healthy_control_requests.jsonl")
	controlSHA, err := writeJSONLines(controlPath, controls)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(build.OutputDir, "manifest.json")
	manifest := preparationManifest{
		PreparedDatasetKey: build.PreparedDatasetKey, Version: preparationDatasetVersion,
		SourceFeatureDatasetKey: source.FeatureDatasetKey, MinimumCoverage: build.MinimumMetricCoverage,
		ControlsPerPositive: controlsPerPositive, TrainEndAt: trainEnd, ValidationEndAt: validationEnd,
		SplitPolicy:     "strict time order plus GPU UUID isolation; entities crossing a boundary are excluded",
		QualityPolicy:   "extraction errors, zero telemetry, and metric coverage below threshold are excluded without zero imputation",
		ControlPolicy:   "same-GPU stable-identity historical cutoffs; exclude every known fault [-168h,+72h]; telemetry and load validation remains required",
		PreparedSamples: filepath.Base(preparedPath), PreparedSamplesSHA256: preparedSHA,
		ControlRequests: filepath.Base(controlPath), ControlRequestsSHA256: controlSHA, CreatedAt: s.now(),
	}
	if err := writeJSONAtomic(manifestPath, manifest); err != nil {
		return err
	}
	finished := s.now()
	return s.db.Model(build).Updates(map[string]any{
		"status": "completed", "eligible_positive_count": build.EligiblePositiveCount,
		"telemetry_censored_count": build.TelemetryCensoredCount, "low_coverage_count": build.LowCoverageCount,
		"extraction_failed_count": build.ExtractionFailedCount, "entity_time_conflict_count": build.EntityTimeConflictCount,
		"train_count": build.TrainCount, "validation_count": build.ValidationCount, "test_count": build.TestCount,
		"control_request_count": build.ControlRequestCount, "control_shortfall_count": build.ControlShortfallCount,
		"train_end_at": &trainEnd, "validation_end_at": &validationEnd,
		"manifest_path": manifestPath, "prepared_samples_path": preparedPath, "prepared_samples_sha256": preparedSHA,
		"control_requests_path": controlPath, "control_requests_sha256": controlSHA, "finished_at": &finished,
	}).Error
}

func readExtractedFeatureRows(path string) ([]extractedFeatureRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	rows := make([]extractedFeatureRow, 0, 1024)
	for scanner.Scan() {
		var row extractedFeatureRow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, scanner.Err()
}

func splitBoundaries(rows []extractedFeatureRow) (time.Time, time.Time, error) {
	onsetsByEpisode := map[string]time.Time{}
	for _, row := range rows {
		onsetsByEpisode[row.EpisodeKey] = row.LabelOnsetAt
	}
	onsets := make([]time.Time, 0, len(onsetsByEpisode))
	for _, onset := range onsetsByEpisode {
		onsets = append(onsets, onset)
	}
	sort.Slice(onsets, func(i, j int) bool { return onsets[i].Before(onsets[j]) })
	if len(onsets) < 3 {
		return time.Time{}, time.Time{}, fmt.Errorf("at least three eligible episodes are required for time splits")
	}
	index := func(ratio float64) int {
		value := int(float64(len(onsets)) * ratio)
		if value <= 0 {
			return 0
		}
		if value >= len(onsets) {
			return len(onsets) - 1
		}
		return value - 1
	}
	return onsets[index(0.70)], onsets[index(0.85)], nil
}

func entityIsolatedSplits(rows []extractedFeatureRow, trainEnd, validationEnd time.Time) map[string]string {
	ranges := map[string]entityTimeRange{}
	for _, row := range rows {
		current := ranges[row.GPUUUID]
		if current.min.IsZero() || row.LabelOnsetAt.Before(current.min) {
			current.min = row.LabelOnsetAt
		}
		if current.max.IsZero() || row.LabelOnsetAt.After(current.max) {
			current.max = row.LabelOnsetAt
		}
		ranges[row.GPUUUID] = current
	}
	result := map[string]string{}
	for gpu, current := range ranges {
		switch {
		case !current.max.After(trainEnd):
			result[gpu] = "train"
		case current.min.After(trainEnd) && !current.max.After(validationEnd):
			result[gpu] = "validation"
		case current.min.After(validationEnd):
			result[gpu] = "test"
		default:
			result[gpu] = ""
		}
	}
	return result
}

type selectedControl struct {
	cutoff   time.Time
	interval api.HistoricalGPUIdentityInterval
}

func healthyControlCutoffs(sample extractedFeatureRow, intervals []api.HistoricalGPUIdentityInterval, faultTimes []time.Time, limit int) []selectedControl {
	offsets := []time.Duration{14 * 24 * time.Hour, 28 * 24 * time.Hour, 42 * 24 * time.Hour, 56 * 24 * time.Hour, 84 * 24 * time.Hour, 112 * 24 * time.Hour, 140 * 24 * time.Hour, 168 * 24 * time.Hour}
	result := make([]selectedControl, 0, limit)
	for _, offset := range offsets {
		cutoff := sample.LabelOnsetAt.Add(-offset)
		if faultWindowContaminated(cutoff.Add(-featureLookback), cutoff, faultTimes) {
			continue
		}
		for _, interval := range intervals {
			if !interval.FirstSeenAt.After(cutoff.Add(-featureLookback)) && !interval.LastSeenAt.Before(cutoff) {
				result = append(result, selectedControl{cutoff: cutoff, interval: interval})
				break
			}
		}
		if len(result) == limit {
			break
		}
	}
	return result
}

func faultWindowContaminated(windowStart, windowEnd time.Time, faultTimes []time.Time) bool {
	policy := CurrentTrainingCohortPolicy()
	for _, fault := range faultTimes {
		start := fault.Add(-time.Duration(policy.HealthyCensorBeforeHours) * time.Hour)
		end := fault.Add(time.Duration(policy.HealthyCensorAfterHours) * time.Hour)
		if !windowEnd.Before(start) && !windowStart.After(end) {
			return true
		}
	}
	return false
}

func faultTimesByGPU(rows []extractedFeatureRow) map[string][]time.Time {
	set := map[string]map[int64]time.Time{}
	for _, row := range rows {
		gpu := normalizeHistoricalGPUUUID(row.GPUUUID)
		if set[gpu] == nil {
			set[gpu] = map[int64]time.Time{}
		}
		set[gpu][row.LabelOnsetAt.UnixNano()] = row.LabelOnsetAt
	}
	result := map[string][]time.Time{}
	for gpu, values := range set {
		for _, value := range values {
			result[gpu] = append(result[gpu], value)
		}
		sort.Slice(result[gpu], func(i, j int) bool { return result[gpu][i].Before(result[gpu][j]) })
	}
	return result
}

func writeJSONLines[T any](path string, rows []T) (string, error) {
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	encoder := json.NewEncoder(io.MultiWriter(file, hash))
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
	return hex.EncodeToString(hash.Sum(nil)), nil
}

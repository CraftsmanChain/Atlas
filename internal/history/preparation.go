package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"atlas/pkg/api"
)

const (
	preparationDatasetVersion   = "gpu-training-preparation-v5"
	manualPreparationVersion    = "manual-feedback-training-preparation-v1"
	correlatedEventBucket       = 5 * time.Minute
	correlatedEventMinimumGPUs  = 32
	correlatedEventMinimumNodes = 10
	minimumEvaluationSplitRatio = 0.05
)

type PreparationBuildRequest struct {
	SourceFeatureBuildID                      uint    `json:"source_feature_build_id"`
	SourceManualFeedbackFeatureRequestBuildID uint    `json:"source_manual_feedback_feature_request_build_id,omitempty"`
	MinimumCoverage                           float64 `json:"minimum_coverage,omitempty"`
	ControlsPerPositive                       int     `json:"controls_per_positive,omitempty"`
}

type preparedTrainingSample struct {
	Sample          extractedFeatureRow   `json:"sample"`
	LabelMetadata   trainingLabelMetadata `json:"label_metadata"`
	LabelValue      int                   `json:"label_value"`
	TrainingStatus  string                `json:"training_status"`
	ExclusionReason string                `json:"exclusion_reason,omitempty"`
	Split           string                `json:"split,omitempty"`
}

type trainingLabelMetadata struct {
	EventTypes           []string `json:"event_types"`
	EventCodes           []string `json:"event_codes,omitempty"`
	DriverVersions       []string `json:"driver_versions,omitempty"`
	RuleDecisionVersions []string `json:"rule_decision_versions,omitempty"`
	LabelSources         []string `json:"label_sources"`
	HardwareCertainties  []string `json:"hardware_certainties,omitempty"`
	IdentityEvidence     []string `json:"identity_evidence,omitempty"`
}

type currentLabelEpisode struct {
	Eligible bool
	Metadata trainingLabelMetadata
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
	CorrelatedEventPolicy   string    `json:"correlated_event_policy"`
	PreparedSamples         string    `json:"prepared_samples"`
	PreparedSamplesSHA256   string    `json:"prepared_samples_sha256"`
	ControlRequests         string    `json:"control_requests"`
	ControlRequestsSHA256   string    `json:"control_requests_sha256"`
	CreatedAt               time.Time `json:"created_at"`
}

type manualPreparationManifest struct {
	PreparedDatasetKey                        string    `json:"prepared_dataset_key"`
	Version                                   string    `json:"version"`
	SourceKind                                string    `json:"source_kind"`
	SourceFeatureDatasetKey                   string    `json:"source_feature_dataset_key"`
	SourceManualFeedbackFeatureRequestBuildID uint      `json:"source_manual_feedback_feature_request_build_id"`
	SourceFeatureSHA256                       string    `json:"source_feature_sha256"`
	MinimumCoverage                           float64   `json:"minimum_metric_coverage"`
	ControlsPerPositive                       int       `json:"controls_per_positive"`
	SourceWindowCount                         int       `json:"source_window_count"`
	EligiblePositiveCount                     int       `json:"eligible_positive_count"`
	Status                                    string    `json:"status"`
	SplitPolicy                               string    `json:"split_policy"`
	QualityPolicy                             string    `json:"quality_policy"`
	ControlPolicy                             string    `json:"control_policy"`
	PreparedSamples                           string    `json:"prepared_samples"`
	PreparedSamplesSHA256                     string    `json:"prepared_samples_sha256"`
	ControlRequests                           string    `json:"control_requests"`
	ControlRequestsSHA256                     string    `json:"control_requests_sha256"`
	CreatedAt                                 time.Time `json:"created_at"`
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
	if request.SourceManualFeedbackFeatureRequestBuildID > 0 {
		return s.buildManualFeedbackTrainingPreparation(request.SourceManualFeedbackFeatureRequestBuildID, minimumCoverage, controlsPerPositive)
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
		SourceKind: "historical_feature_build", SourceFeatureBuildID: source.ID, SourceFeatureDatasetKey: source.FeatureDatasetKey,
		MinimumMetricCoverage: minimumCoverage, SourceWindowCount: source.WindowCount,
		OutputDir: filepath.Join(s.config.DatasetDir, "prepared", key), StartedAt: started,
	}
	if err := s.db.Create(&build).Error; err != nil {
		return build, err
	}
	if err := s.buildTrainingPreparation(&build, source, cohort, controlsPerPositive); err != nil {
		finished := s.now()
		_ = s.db.Model(&build).Updates(map[string]any{
			"status": "failed", "error_message": err.Error(), "finished_at": &finished,
			"eligible_positive_count":  build.EligiblePositiveCount,
			"telemetry_censored_count": build.TelemetryCensoredCount, "low_coverage_count": build.LowCoverageCount,
			"extraction_failed_count": build.ExtractionFailedCount, "positive_discontinuous_count": build.PositiveDiscontinuousCount, "label_ineligible_count": build.LabelIneligibleCount, "correlated_event_count": build.CorrelatedEventCount,
			"entity_time_conflict_count": build.EntityTimeConflictCount,
			"train_count":                build.TrainCount, "validation_count": build.ValidationCount, "test_count": build.TestCount,
			"train_end_at": build.TrainEndAt, "validation_end_at": build.ValidationEndAt,
		}).Error
		build.Status, build.ErrorMessage, build.FinishedAt = "failed", err.Error(), &finished
		return build, err
	}
	return build, s.db.First(&build, build.ID).Error
}

func (s *Service) buildManualFeedbackTrainingPreparation(sourceID uint, minimumCoverage float64, controlsPerPositive int) (api.TrainingPreparationBuild, error) {
	var source api.ManualFeedbackFeatureRequestBuild
	result := s.db.Where("id = ? AND version = ? AND status IN ?", sourceID, manualFeedbackFeatureRequestVersion, []string{
		"features_ready_pending_training_preparation",
		"features_ready_with_errors_pending_training_preparation",
	}).Limit(1).Find(&source)
	if result.Error != nil {
		return api.TrainingPreparationBuild{}, result.Error
	}
	if result.RowsAffected == 0 {
		return api.TrainingPreparationBuild{}, fmt.Errorf("a features_ready manual feedback feature request is required")
	}
	if source.FeaturePath == "" || source.FeatureSHA256 == "" {
		return api.TrainingPreparationBuild{}, fmt.Errorf("manual feedback feature request is missing feature artifacts")
	}
	if err := verifyFileSHA256(source.FeaturePath, source.FeatureSHA256); err != nil {
		return api.TrainingPreparationBuild{}, fmt.Errorf("manual feedback feature checksum: %w", err)
	}
	rows, err := readExtractedFeatureRows(source.FeaturePath)
	if err != nil {
		return api.TrainingPreparationBuild{}, err
	}
	started := s.now()
	key := manualPreparationVersion + "-" + strconv.FormatInt(started.UTC().UnixNano(), 10)
	build := api.TrainingPreparationBuild{
		PreparedDatasetKey: key, Version: manualPreparationVersion, Status: "running",
		SourceKind: "manual_feedback_feature_request", SourceManualFeedbackFeatureRequestBuildID: source.ID,
		SourceFeatureDatasetKey: source.RequestKey, MinimumMetricCoverage: minimumCoverage, SourceWindowCount: source.WindowCount,
		OutputDir: filepath.Join(s.config.DatasetDir, "prepared", key), StartedAt: started,
	}
	if err := s.db.Create(&build).Error; err != nil {
		return build, err
	}
	if err := s.writeManualFeedbackTrainingPreparation(&build, source, rows, controlsPerPositive); err != nil {
		finished := s.now()
		_ = s.db.Model(&build).Updates(map[string]any{
			"status": "failed", "error_message": err.Error(), "finished_at": &finished,
			"telemetry_censored_count": build.TelemetryCensoredCount, "low_coverage_count": build.LowCoverageCount,
			"extraction_failed_count": build.ExtractionFailedCount, "positive_discontinuous_count": build.PositiveDiscontinuousCount,
			"eligible_positive_count": build.EligiblePositiveCount, "control_shortfall_count": build.ControlShortfallCount,
		}).Error
		build.Status, build.ErrorMessage, build.FinishedAt = "failed", err.Error(), &finished
		return build, err
	}
	return build, s.db.First(&build, build.ID).Error
}

func (s *Service) writeManualFeedbackTrainingPreparation(build *api.TrainingPreparationBuild, source api.ManualFeedbackFeatureRequestBuild, rows []extractedFeatureRow, controlsPerPositive int) error {
	prepared := make([]preparedTrainingSample, 0, len(rows))
	for _, row := range rows {
		item := preparedTrainingSample{
			Sample:     row,
			LabelValue: 1,
			LabelMetadata: trainingLabelMetadata{
				EventTypes:           []string{"manual_hardware_fault_feedback"},
				RuleDecisionVersions: []string{manualFeedbackFeatureRequestVersion},
				LabelSources:         []string{"manual_hardware_fault_feedback"},
				HardwareCertainties:  []string{"operator_confirmed_hardware_repair"},
			},
		}
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
		case positiveTelemetryContinuity(row) < minimumTelemetryContinuity:
			item.TrainingStatus, item.ExclusionReason = "excluded", "positive_telemetry_discontinuous"
			build.PositiveDiscontinuousCount++
		default:
			item.TrainingStatus, item.Split = "eligible_pending_controls", "pending_control_sampling"
			build.EligiblePositiveCount++
		}
		prepared = append(prepared, item)
	}
	build.ControlShortfallCount = build.EligiblePositiveCount * controlsPerPositive
	build.Status = "manual_feedback_ready_pending_control_sampling"
	if build.EligiblePositiveCount == 0 {
		build.Status = "manual_feedback_blocked_by_quality_gate"
	}
	if err := os.MkdirAll(build.OutputDir, 0o750); err != nil {
		return err
	}
	preparedPath := filepath.Join(build.OutputDir, "prepared_manual_feedback_positive_samples.jsonl")
	preparedSHA, err := writeJSONLines(preparedPath, prepared)
	if err != nil {
		return err
	}
	controls := make([]healthyControlRequest, 0)
	controlPath := filepath.Join(build.OutputDir, "healthy_control_requests.jsonl")
	controlSHA, err := writeJSONLines(controlPath, controls)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(build.OutputDir, "manifest.json")
	manifest := manualPreparationManifest{
		PreparedDatasetKey: build.PreparedDatasetKey, Version: manualPreparationVersion,
		SourceKind: "manual_feedback_feature_request", SourceFeatureDatasetKey: source.RequestKey,
		SourceManualFeedbackFeatureRequestBuildID: source.ID, SourceFeatureSHA256: source.FeatureSHA256,
		MinimumCoverage: build.MinimumMetricCoverage, ControlsPerPositive: controlsPerPositive,
		SourceWindowCount: build.SourceWindowCount, EligiblePositiveCount: build.EligiblePositiveCount, Status: build.Status,
		SplitPolicy:     "manual feedback positives remain pending_control_sampling; train/validation/test split is deferred until control samples and matrix governance are available",
		QualityPolicy:   "manual feedback positives must pass extraction, nonzero telemetry, minimum metric coverage, and core telemetry continuity; no zero imputation is applied",
		ControlPolicy:   "healthy controls are not generated in this step; same-GPU/control-window extraction remains a downstream gated workflow",
		PreparedSamples: filepath.Base(preparedPath), PreparedSamplesSHA256: preparedSHA,
		ControlRequests: filepath.Base(controlPath), ControlRequestsSHA256: controlSHA, CreatedAt: s.now(),
	}
	if err := writeJSONAtomic(manifestPath, manifest); err != nil {
		return err
	}
	finished := s.now()
	return s.db.Model(build).Updates(map[string]any{
		"status": build.Status, "eligible_positive_count": build.EligiblePositiveCount,
		"telemetry_censored_count": build.TelemetryCensoredCount, "low_coverage_count": build.LowCoverageCount,
		"extraction_failed_count": build.ExtractionFailedCount, "positive_discontinuous_count": build.PositiveDiscontinuousCount,
		"control_request_count": build.ControlRequestCount, "control_shortfall_count": build.ControlShortfallCount,
		"manifest_path": manifestPath, "prepared_samples_path": preparedPath, "prepared_samples_sha256": preparedSHA,
		"control_requests_path": controlPath, "control_requests_sha256": controlSHA, "finished_at": &finished,
	}).Error
}

func (s *Service) buildTrainingPreparation(build *api.TrainingPreparationBuild, source api.TrainingFeatureBuild, cohort api.TrainingDatasetBuild, controlsPerPositive int) error {
	rows, err := readExtractedFeatureRows(source.FeaturePath)
	if err != nil {
		return err
	}
	if err := verifyFileSHA256(cohort.WindowManifestPath, cohort.WindowManifestSHA256); err != nil {
		return fmt.Errorf("source cohort checksum: %w", err)
	}
	windows, err := readDatasetWindows(cohort.WindowManifestPath)
	if err != nil {
		return err
	}
	labelEpisodes, err := s.currentLabelEpisodes(windows)
	if err != nil {
		return err
	}
	correlatedEpisodes := correlatedFleetEpisodes(windows)
	eligibleForSplit := make([]extractedFeatureRow, 0, len(rows))
	for _, row := range rows {
		if row.ExtractionError == "" && row.MetricCoverage >= build.MinimumMetricCoverage && positiveTelemetryContinuity(row) >= minimumTelemetryContinuity && labelEpisodes[row.EpisodeKey].Eligible && !correlatedEpisodes[row.EpisodeKey] {
			eligibleForSplit = append(eligibleForSplit, row)
		}
	}
	trainEnd, validationEnd, err := splitBoundaries(eligibleForSplit)
	if err != nil {
		return err
	}
	build.TrainEndAt, build.ValidationEndAt = &trainEnd, &validationEnd
	splits := entityIsolatedSplits(eligibleForSplit, trainEnd, validationEnd)
	prepared := make([]preparedTrainingSample, 0, len(rows))
	for _, row := range rows {
		item := preparedTrainingSample{Sample: row, LabelMetadata: labelEpisodes[row.EpisodeKey].Metadata, LabelValue: 1}
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
		case positiveTelemetryContinuity(row) < minimumTelemetryContinuity:
			item.TrainingStatus, item.ExclusionReason = "excluded", "positive_telemetry_discontinuous"
			build.PositiveDiscontinuousCount++
		case !labelEpisodes[row.EpisodeKey].Eligible:
			item.TrainingStatus, item.ExclusionReason = "excluded", "label_not_currently_eligible"
			build.LabelIneligibleCount++
		case correlatedEpisodes[row.EpisodeKey]:
			item.TrainingStatus, item.ExclusionReason = "excluded", "correlated_fleet_event"
			build.CorrelatedEventCount++
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
	minimumEvaluationCount := int(float64(build.EligiblePositiveCount)*minimumEvaluationSplitRatio + 0.999999)
	if minimumEvaluationCount < 1 {
		minimumEvaluationCount = 1
	}
	if build.TrainCount == 0 || build.ValidationCount < minimumEvaluationCount || build.TestCount < minimumEvaluationCount {
		return fmt.Errorf("unsafe training split: train=%d validation=%d test=%d; validation and test each require at least %d eligible windows",
			build.TrainCount, build.ValidationCount, build.TestCount, minimumEvaluationCount)
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
		SplitPolicy:           "strict time order plus GPU UUID isolation; entities crossing a boundary are excluded",
		QualityPolicy:         "extraction errors, zero telemetry, core telemetry continuity below 70%, low coverage, labels ineligible under " + historicalRuleDecisionVersion + ", and correlated fleet events are excluded without zero imputation",
		ControlPolicy:         "same-GPU stable-identity historical cutoffs; exclude every known fault [-168h,+72h]; telemetry and load validation remains required",
		CorrelatedEventPolicy: "exclude same-event five-minute buckets affecting at least 32 GPUs across at least 10 nodes from GPU hardware training",
		PreparedSamples:       filepath.Base(preparedPath), PreparedSamplesSHA256: preparedSHA,
		ControlRequests: filepath.Base(controlPath), ControlRequestsSHA256: controlSHA, CreatedAt: s.now(),
	}
	if err := writeJSONAtomic(manifestPath, manifest); err != nil {
		return err
	}
	finished := s.now()
	return s.db.Model(build).Updates(map[string]any{
		"status": "completed", "eligible_positive_count": build.EligiblePositiveCount,
		"telemetry_censored_count": build.TelemetryCensoredCount, "low_coverage_count": build.LowCoverageCount,
		"extraction_failed_count": build.ExtractionFailedCount, "positive_discontinuous_count": build.PositiveDiscontinuousCount, "label_ineligible_count": build.LabelIneligibleCount, "entity_time_conflict_count": build.EntityTimeConflictCount,
		"correlated_event_count": build.CorrelatedEventCount,
		"train_count":            build.TrainCount, "validation_count": build.ValidationCount, "test_count": build.TestCount,
		"control_request_count": build.ControlRequestCount, "control_shortfall_count": build.ControlShortfallCount,
		"train_end_at": &trainEnd, "validation_end_at": &validationEnd,
		"manifest_path": manifestPath, "prepared_samples_path": preparedPath, "prepared_samples_sha256": preparedSHA,
		"control_requests_path": controlPath, "control_requests_sha256": controlSHA, "finished_at": &finished,
	}).Error
}

func positiveTelemetryContinuity(row extractedFeatureRow) float64 {
	if row.QueryStepSeconds <= 0 || row.LookbackMinutes <= 0 {
		return 0
	}
	expected := row.LookbackMinutes*60/row.QueryStepSeconds + 1
	minimum := float64(expected)
	for _, metric := range []string{"gpu_temp", "power_usage", "gpu_util"} {
		count, exists := row.Features[metric+"_sample_count_24h"]
		if !exists {
			return 0
		}
		if count < minimum {
			minimum = count
		}
	}
	return math.Min(1, minimum/float64(expected))
}

func (s *Service) currentLabelEligibleEpisodes(windows []datasetWindow) (map[string]bool, error) {
	episodes, err := s.currentLabelEpisodes(windows)
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(episodes))
	for key, episode := range episodes {
		result[key] = episode.Eligible
	}
	return result, nil
}

func (s *Service) currentLabelEpisodes(windows []datasetWindow) (map[string]currentLabelEpisode, error) {
	ids := make([]uint, 0)
	seen := map[uint]bool{}
	for _, window := range windows {
		for _, id := range window.CandidateIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	var candidates []api.HistoricalFaultCandidate
	if len(ids) > 0 {
		if err := s.db.Where("id IN ?", ids).Find(&candidates).Error; err != nil {
			return nil, err
		}
	}
	byID := map[uint]api.HistoricalFaultCandidate{}
	for _, candidate := range candidates {
		if candidate.ReviewedAt == nil {
			rule := decideHistoricalCandidate(candidate, candidate.IdentityEvidenceStatus)
			candidate.RuleDecision = rule.Decision
			candidate.RuleConfidence = rule.Confidence
			candidate.RuleDecisionVersion = historicalRuleDecisionVersion
		}
		byID[candidate.ID] = candidate
	}
	result := map[string]currentLabelEpisode{}
	for _, window := range windows {
		if len(window.CandidateIDs) == 0 {
			result[window.EpisodeKey] = currentLabelEpisode{Eligible: true, Metadata: trainingLabelMetadata{
				EventTypes: append([]string(nil), window.EventTypes...), LabelSources: []string{"legacy_manifest"},
			}}
			continue
		}
		episode := result[window.EpisodeKey]
		for _, id := range window.CandidateIDs {
			candidate, exists := byID[id]
			if !exists {
				continue
			}
			eligibility := candidateDatasetEligibility(candidate)
			if eligibility == "rule_positive_proxy" || eligibility == "operator_accepted_proxy" {
				episode.Eligible = true
				episode.Metadata.EventTypes = appendUnique(episode.Metadata.EventTypes, candidate.EventType)
				episode.Metadata.EventCodes = appendUnique(episode.Metadata.EventCodes, candidate.EventCode)
				episode.Metadata.DriverVersions = appendUnique(episode.Metadata.DriverVersions, firstNonEmpty(candidate.Labels["DCGM_FI_DRIVER_VERSION"], candidate.Labels["driver_version"]))
				episode.Metadata.RuleDecisionVersions = appendUnique(episode.Metadata.RuleDecisionVersions, candidate.RuleDecisionVersion)
				episode.Metadata.LabelSources = appendUnique(episode.Metadata.LabelSources, datasetLabelSource(candidate))
				episode.Metadata.HardwareCertainties = appendUnique(episode.Metadata.HardwareCertainties, candidate.HardwareCertainty)
				episode.Metadata.IdentityEvidence = appendUnique(episode.Metadata.IdentityEvidence, candidate.IdentityEvidenceStatus)
			}
		}
		sort.Strings(episode.Metadata.EventTypes)
		sort.Strings(episode.Metadata.EventCodes)
		sort.Strings(episode.Metadata.DriverVersions)
		sort.Strings(episode.Metadata.RuleDecisionVersions)
		sort.Strings(episode.Metadata.LabelSources)
		sort.Strings(episode.Metadata.HardwareCertainties)
		sort.Strings(episode.Metadata.IdentityEvidence)
		result[window.EpisodeKey] = episode
	}
	return result, nil
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
	unique := make([]time.Time, 0, len(onsets))
	cumulative := make([]int, 0, len(onsets))
	for _, onset := range onsets {
		if len(unique) == 0 || !onset.Equal(unique[len(unique)-1]) {
			unique = append(unique, onset)
			cumulative = append(cumulative, 0)
		}
		cumulative[len(cumulative)-1]++
	}
	if len(unique) < 3 {
		return time.Time{}, time.Time{}, fmt.Errorf("at least three distinct eligible episode onsets are required for time splits")
	}
	for index := 1; index < len(cumulative); index++ {
		cumulative[index] += cumulative[index-1]
	}
	closestBoundary := func(target float64, first, last int) int {
		selected := first
		selectedDistance := math.Abs(float64(cumulative[first]) - float64(len(onsets))*target)
		for index := first + 1; index <= last; index++ {
			distance := math.Abs(float64(cumulative[index]) - float64(len(onsets))*target)
			if distance < selectedDistance {
				selected, selectedDistance = index, distance
			}
		}
		return selected
	}
	trainIndex := closestBoundary(0.70, 0, len(unique)-3)
	validationIndex := closestBoundary(0.85, trainIndex+1, len(unique)-2)
	return unique[trainIndex], unique[validationIndex], nil
}

type correlatedEventGroup struct {
	episodes map[string]struct{}
	gpus     map[string]struct{}
	nodes    map[string]struct{}
}

func correlatedFleetEpisodes(windows []datasetWindow) map[string]bool {
	groups := map[string]*correlatedEventGroup{}
	seenEpisodes := map[string]bool{}
	for _, window := range windows {
		if seenEpisodes[window.EpisodeKey] {
			continue
		}
		seenEpisodes[window.EpisodeKey] = true
		bucket := window.LabelOnsetAt.Truncate(correlatedEventBucket).UTC().Format(time.RFC3339)
		for _, eventType := range window.EventTypes {
			key := bucket + "|" + eventType
			group := groups[key]
			if group == nil {
				group = &correlatedEventGroup{
					episodes: map[string]struct{}{}, gpus: map[string]struct{}{}, nodes: map[string]struct{}{},
				}
				groups[key] = group
			}
			group.episodes[window.EpisodeKey] = struct{}{}
			group.gpus[normalizeHistoricalGPUUUID(window.GPUUUID)] = struct{}{}
			group.nodes[window.NodeIP] = struct{}{}
		}
	}
	result := map[string]bool{}
	for _, group := range groups {
		if len(group.gpus) < correlatedEventMinimumGPUs || len(group.nodes) < correlatedEventMinimumNodes {
			continue
		}
		for episode := range group.episodes {
			result[episode] = true
		}
	}
	return result
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

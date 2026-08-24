package history

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
)

const (
	trainingMatrixVersion      = "gpu-supervised-training-matrix-v4"
	manualTrainingMatrixStatus = "manual_feedback_matrix_ready_pending_training_gate"
)

type cohortReadinessPolicy struct {
	MinimumTrainPositiveGPUs      int `json:"minimum_train_positive_gpus"`
	MinimumEvaluationPositiveGPUs int `json:"minimum_evaluation_positive_gpus"`
	MinimumTrainPositives         int `json:"minimum_train_positives"`
	MinimumTrainControls          int `json:"minimum_train_controls"`
	MinimumEvaluationPositives    int `json:"minimum_evaluation_positives"`
	MinimumEvaluationControls     int `json:"minimum_evaluation_controls"`
}

type cohortSplitReadiness struct {
	PositiveCount int `json:"positive_count"`
	ControlCount  int `json:"control_count"`
	PositiveGPUs  int `json:"positive_gpus"`
	ControlGPUs   int `json:"control_gpus"`
}

type cohortStratumReadiness struct {
	EventType       string                          `json:"event_type"`
	ModelName       string                          `json:"model_name"`
	HorizonMinutes  int                             `json:"horizon_minutes"`
	Status          string                          `json:"status"`
	BlockingReasons []string                        `json:"blocking_reasons"`
	Deficits        []cohortReadinessDeficit        `json:"deficits"`
	Splits          map[string]cohortSplitReadiness `json:"splits"`
}

type cohortReadinessReport struct {
	MatrixKey          string                   `json:"matrix_key"`
	Policy             cohortReadinessPolicy    `json:"policy"`
	ReadyStrata        int                      `json:"ready_strata"`
	InsufficientStrata int                      `json:"insufficient_strata"`
	Deficits           []cohortReadinessDeficit `json:"deficits"`
	RecommendedNextRun []string                 `json:"recommended_next_run"`
	Strata             []cohortStratumReadiness `json:"strata"`
}

type cohortReadinessDeficit struct {
	EventType      string `json:"event_type"`
	ModelName      string `json:"model_name"`
	HorizonMinutes int    `json:"horizon_minutes"`
	Split          string `json:"split"`
	Metric         string `json:"metric"`
	Actual         int    `json:"actual"`
	Required       int    `json:"required"`
	Shortfall      int    `json:"shortfall"`
}

type TrainingMatrixBuildRequest struct {
	SourceControlBuildID uint `json:"source_control_build_id"`
}

type trainingMatrixRow struct {
	RowKey          string                `json:"row_key"`
	SampleKey       string                `json:"sample_key"`
	PairedSampleKey string                `json:"paired_sample_key,omitempty"`
	SampleKind      string                `json:"sample_kind"`
	LabelValue      int                   `json:"label_value"`
	EvidenceWeight  float64               `json:"evidence_weight"`
	ClassWeight     float64               `json:"class_weight"`
	TrainingWeight  float64               `json:"training_weight"`
	Split           string                `json:"split"`
	HorizonMinutes  int                   `json:"horizon_minutes"`
	GPUUUID         string                `json:"gpu_uuid"`
	NodeIP          string                `json:"node_ip"`
	ModelName       string                `json:"model_name"`
	FeatureCutoffAt time.Time             `json:"feature_cutoff_at"`
	LabelOnsetAt    *time.Time            `json:"label_onset_at,omitempty"`
	LabelMetadata   trainingLabelMetadata `json:"label_metadata"`
	LoadBucket      string                `json:"load_bucket"`
	MetricCoverage  float64               `json:"metric_coverage"`
	Features        map[string]float64    `json:"features"`
}

type matrixManifest struct {
	TrainingMatrixKey      string                `json:"training_matrix_key"`
	Version                string                `json:"version"`
	SourcePreparedDataset  string                `json:"source_prepared_dataset_key"`
	SourceControlDataset   string                `json:"source_control_dataset_key"`
	FeatureContractVersion string                `json:"feature_contract_version"`
	FeatureColumns         []string              `json:"feature_columns"`
	Counts                 map[string]int        `json:"counts"`
	PointInTimePolicy      string                `json:"point_in_time_policy"`
	SplitPolicy            string                `json:"split_policy"`
	MissingValuePolicy     string                `json:"missing_value_policy"`
	WeightPolicy           string                `json:"weight_policy"`
	CohortReadiness        cohortReadinessReport `json:"cohort_readiness"`
	MatrixFile             string                `json:"matrix_file"`
	MatrixSHA256           string                `json:"matrix_sha256"`
	CreatedAt              time.Time             `json:"created_at"`
}

func (s *Service) TrainingMatrixBuilds(limit int) ([]api.TrainingMatrixBuild, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.TrainingMatrixBuild
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) TrainingMatrixReadiness(id uint) (cohortReadinessReport, error) {
	var build api.TrainingMatrixBuild
	if err := s.db.First(&build, id).Error; err != nil {
		return cohortReadinessReport{}, err
	}
	if (build.Status != "completed" && build.Status != manualTrainingMatrixStatus) || build.Version != trainingMatrixVersion || build.ManifestPath == "" {
		return cohortReadinessReport{}, fmt.Errorf("completed training matrix manifest is required")
	}
	base, err := filepath.Abs(s.config.DatasetDir)
	if err != nil {
		return cohortReadinessReport{}, err
	}
	path, err := filepath.Abs(build.ManifestPath)
	if err != nil {
		return cohortReadinessReport{}, err
	}
	relative, err := filepath.Rel(base, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return cohortReadinessReport{}, fmt.Errorf("training matrix manifest is outside the configured dataset directory")
	}
	file, err := os.Open(path)
	if err != nil {
		return cohortReadinessReport{}, err
	}
	defer file.Close()
	var manifest matrixManifest
	if err := json.NewDecoder(io.LimitReader(file, 32<<20)).Decode(&manifest); err != nil {
		return cohortReadinessReport{}, fmt.Errorf("decode training matrix manifest: %w", err)
	}
	if manifest.TrainingMatrixKey != build.TrainingMatrixKey {
		return cohortReadinessReport{}, fmt.Errorf("training matrix manifest key mismatch")
	}
	return manifest.CohortReadiness, nil
}

func (s *Service) StartTrainingMatrixBuild(request TrainingMatrixBuildRequest) (api.TrainingMatrixBuild, error) {
	s.matrixMu.Lock()
	defer s.matrixMu.Unlock()
	if s.matrixRunning {
		return api.TrainingMatrixBuild{}, fmt.Errorf("a training matrix build is already running")
	}
	var control api.TrainingControlFeatureBuild
	query := s.db.Where("status = ? AND version = ?", "completed", controlFeatureDatasetVersion)
	if request.SourceControlBuildID > 0 {
		query = query.Where("id = ?", request.SourceControlBuildID)
	}
	result := query.Order("finished_at DESC, id DESC").Limit(1).Find(&control)
	if result.Error != nil {
		return api.TrainingMatrixBuild{}, result.Error
	}
	if result.RowsAffected == 0 {
		return api.TrainingMatrixBuild{}, fmt.Errorf("a completed healthy-control feature dataset is required")
	}
	var preparation api.TrainingPreparationBuild
	if err := s.db.First(&preparation, control.SourcePreparationBuildID).Error; err != nil {
		return api.TrainingMatrixBuild{}, err
	}
	if preparation.Version != preparationDatasetVersion && preparation.Version != manualPreparationVersion {
		return api.TrainingMatrixBuild{}, fmt.Errorf("healthy-control features must come from historical or manual feedback training preparation")
	}
	started := s.now()
	key := trainingMatrixVersion + "-" + strconv.FormatInt(started.UTC().UnixNano(), 10)
	build := api.TrainingMatrixBuild{
		TrainingMatrixKey: key, Version: trainingMatrixVersion, Status: "queued",
		SourcePreparationBuildID: preparation.ID, SourcePreparedDatasetKey: preparation.PreparedDatasetKey,
		SourceControlBuildID: control.ID, SourceControlDatasetKey: control.ControlFeatureDatasetKey,
		FeatureContractVersion: control.FeatureContractVersion,
		OutputDir:              filepath.Join(s.config.DatasetDir, "training-matrices", key), StartedAt: started,
	}
	if err := s.db.Create(&build).Error; err != nil {
		return build, err
	}
	s.matrixRunning = true
	go s.executeTrainingMatrixBuild(build.ID)
	return build, nil
}

func (s *Service) executeTrainingMatrixBuild(id uint) {
	defer func() { s.matrixMu.Lock(); s.matrixRunning = false; s.matrixMu.Unlock() }()
	var build api.TrainingMatrixBuild
	if err := s.db.First(&build, id).Error; err != nil {
		return
	}
	if err := s.db.Model(&build).Update("status", "running").Error; err != nil {
		return
	}
	if err := s.buildTrainingMatrix(&build); err != nil {
		finished := s.now()
		_ = s.db.Model(&build).Updates(map[string]any{"status": "failed", "error_message": err.Error(), "finished_at": &finished,
			"duplicate_count": build.DuplicateCount, "entity_split_conflict_count": build.EntitySplitConflictCount,
			"point_in_time_violation_count": build.PointInTimeViolationCount, "pairing_violation_count": build.PairingViolationCount,
			"contract_violation_count": build.ContractViolationCount}).Error
	}
}

func (s *Service) buildTrainingMatrix(build *api.TrainingMatrixBuild) error {
	var preparation api.TrainingPreparationBuild
	var controlBuild api.TrainingControlFeatureBuild
	if err := s.db.First(&preparation, build.SourcePreparationBuildID).Error; err != nil {
		return err
	}
	if err := s.db.First(&controlBuild, build.SourceControlBuildID).Error; err != nil {
		return err
	}
	if err := verifyFileSHA256(preparation.PreparedSamplesPath, preparation.PreparedSamplesSHA256); err != nil {
		return fmt.Errorf("positive checksum: %w", err)
	}
	if err := verifyFileSHA256(controlBuild.FeaturePath, controlBuild.FeatureSHA256); err != nil {
		return fmt.Errorf("control checksum: %w", err)
	}
	positives, err := readJSONLines[preparedTrainingSample](preparation.PreparedSamplesPath)
	if err != nil {
		return err
	}
	controls, err := readJSONLines[controlFeatureRow](controlBuild.FeaturePath)
	if err != nil {
		return err
	}
	manualFeedbackMatrix := preparation.Version == manualPreparationVersion || preparation.SourceKind == "manual_feedback_feature_request"
	rows, columns, audit := assembleTrainingMatrixWithOptions(positives, controls, build.FeatureContractVersion, matrixAssemblyOptions{
		IncludePendingControlSampling: manualFeedbackMatrix,
	})
	build.DuplicateCount, build.EntitySplitConflictCount = audit.duplicates, audit.entityConflicts
	build.PointInTimeViolationCount, build.PairingViolationCount = audit.pointInTime, audit.pairing
	build.ContractViolationCount = audit.contract
	if audit.duplicates+audit.entityConflicts+audit.pointInTime+audit.pairing+audit.contract > 0 {
		return fmt.Errorf("training matrix audit failed: duplicates=%d entity_split=%d point_in_time=%d pairing=%d contract=%d", audit.duplicates, audit.entityConflicts, audit.pointInTime, audit.pairing, audit.contract)
	}
	if len(rows) == 0 {
		return fmt.Errorf("no eligible training rows")
	}
	applyClassWeights(rows)
	for _, row := range rows {
		build.SampleCount++
		if row.LabelValue == 1 {
			build.PositiveCount++
		} else {
			build.ControlCount++
		}
		switch row.Split + ":" + strconv.Itoa(row.LabelValue) {
		case "train:1":
			build.TrainPositiveCount++
		case "train:0":
			build.TrainControlCount++
		case "validation:1":
			build.ValidationPositiveCount++
		case "validation:0":
			build.ValidationControlCount++
		case "test:1":
			build.TestPositiveCount++
		case "test:0":
			build.TestControlCount++
		}
	}
	if !manualFeedbackMatrix && (build.TrainPositiveCount == 0 || build.TrainControlCount == 0 || build.ValidationPositiveCount == 0 || build.ValidationControlCount == 0 || build.TestPositiveCount == 0 || build.TestControlCount == 0) {
		return fmt.Errorf("every split must contain both labels")
	}
	build.FeatureColumnCount = len(columns)
	if err := os.MkdirAll(build.OutputDir, 0o750); err != nil {
		return err
	}
	matrixPath := filepath.Join(build.OutputDir, "training_matrix.jsonl")
	checksum, err := writeJSONLines(matrixPath, rows)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(build.OutputDir, "manifest.json")
	manifest := matrixManifest{
		TrainingMatrixKey: build.TrainingMatrixKey, Version: build.Version,
		SourcePreparedDataset: build.SourcePreparedDatasetKey, SourceControlDataset: build.SourceControlDatasetKey,
		FeatureContractVersion: build.FeatureContractVersion, FeatureColumns: columns,
		Counts: map[string]int{"samples": build.SampleCount, "positives": build.PositiveCount, "controls": build.ControlCount,
			"train_positive": build.TrainPositiveCount, "train_control": build.TrainControlCount,
			"validation_positive": build.ValidationPositiveCount, "validation_control": build.ValidationControlCount,
			"test_positive": build.TestPositiveCount, "test_control": build.TestControlCount},
		PointInTimePolicy:  "positive feature_cutoff_at is strictly before label_onset_at; controls remain outside known fault intervals",
		SplitPolicy:        "time ordered and GPU UUID isolated; paired controls inherit the positive split",
		MissingValuePolicy: "sparse feature maps preserve missingness; loaders must use null/NaN plus train-only imputation and must never convert absence to zero",
		WeightPolicy:       "training_weight=evidence_weight*per-split-and-horizon balanced class weight; evaluation must report unweighted metrics",
		CohortReadiness:    evaluateCohortReadiness(build.TrainingMatrixKey, rows),
		MatrixFile:         filepath.Base(matrixPath), MatrixSHA256: checksum, CreatedAt: s.now(),
	}
	if err := writeJSONAtomic(manifestPath, manifest); err != nil {
		return err
	}
	status := "completed"
	errorMessage := ""
	if manualFeedbackMatrix {
		status = manualTrainingMatrixStatus
		errorMessage = "manual feedback matrix is governance-only until train/validation/test split gates and cohort sample thresholds are satisfied"
	}
	finished := s.now()
	return s.db.Model(build).Updates(map[string]any{"status": status, "feature_column_count": build.FeatureColumnCount,
		"positive_count": build.PositiveCount, "control_count": build.ControlCount, "sample_count": build.SampleCount,
		"train_positive_count": build.TrainPositiveCount, "train_control_count": build.TrainControlCount,
		"validation_positive_count": build.ValidationPositiveCount, "validation_control_count": build.ValidationControlCount,
		"test_positive_count": build.TestPositiveCount, "test_control_count": build.TestControlCount,
		"matrix_path": matrixPath, "matrix_sha256": checksum, "manifest_path": manifestPath, "error_message": errorMessage, "finished_at": &finished}).Error
}

func currentCohortReadinessPolicy() cohortReadinessPolicy {
	return cohortReadinessPolicy{
		MinimumTrainPositiveGPUs: 10, MinimumEvaluationPositiveGPUs: 5,
		MinimumTrainPositives: 30, MinimumTrainControls: 60,
		MinimumEvaluationPositives: 10, MinimumEvaluationControls: 20,
	}
}

type mutableCohortSplit struct {
	positive, control         int
	positiveGPUs, controlGPUs map[string]bool
}

func evaluateCohortReadiness(matrixKey string, rows []trainingMatrixRow) cohortReadinessReport {
	policy := currentCohortReadinessPolicy()
	type stratumKey struct {
		eventType string
		modelName string
		horizon   int
	}
	grouped := map[stratumKey]map[string]*mutableCohortSplit{}
	for _, row := range rows {
		events := row.LabelMetadata.EventTypes
		if len(events) == 0 {
			events = []string{"unknown"}
		}
		model := strings.TrimSpace(row.ModelName)
		if model == "" {
			model = "unknown"
		}
		for _, eventType := range events {
			eventType = strings.TrimSpace(eventType)
			if eventType == "" {
				eventType = "unknown"
			}
			key := stratumKey{eventType: eventType, modelName: model, horizon: row.HorizonMinutes}
			if grouped[key] == nil {
				grouped[key] = map[string]*mutableCohortSplit{}
			}
			if grouped[key][row.Split] == nil {
				grouped[key][row.Split] = &mutableCohortSplit{positiveGPUs: map[string]bool{}, controlGPUs: map[string]bool{}}
			}
			split := grouped[key][row.Split]
			gpu := normalizeHistoricalGPUUUID(row.GPUUUID)
			if row.LabelValue == 1 {
				split.positive++
				if gpu != "" {
					split.positiveGPUs[gpu] = true
				}
			} else {
				split.control++
				if gpu != "" {
					split.controlGPUs[gpu] = true
				}
			}
		}
	}
	report := cohortReadinessReport{MatrixKey: matrixKey, Policy: policy}
	for key, splitRows := range grouped {
		item := cohortStratumReadiness{EventType: key.eventType, ModelName: key.modelName, HorizonMinutes: key.horizon, Status: "exploratory_ready", BlockingReasons: []string{}, Deficits: []cohortReadinessDeficit{}, Splits: map[string]cohortSplitReadiness{}}
		splitNames := []string{"train", "validation", "test"}
		if _, exists := splitRows["pending_control_sampling"]; exists {
			splitNames = append(splitNames, "pending_control_sampling")
		}
		addDeficit := func(splitName, metric string, actual, required int) {
			if actual >= required {
				return
			}
			deficit := cohortReadinessDeficit{
				EventType: key.eventType, ModelName: key.modelName, HorizonMinutes: key.horizon,
				Split: splitName, Metric: metric, Actual: actual, Required: required, Shortfall: required - actual,
			}
			item.Deficits = append(item.Deficits, deficit)
			report.Deficits = append(report.Deficits, deficit)
		}
		for _, splitName := range splitNames {
			split := splitRows[splitName]
			if split == nil {
				split = &mutableCohortSplit{positiveGPUs: map[string]bool{}, controlGPUs: map[string]bool{}}
			}
			item.Splits[splitName] = cohortSplitReadiness{PositiveCount: split.positive, ControlCount: split.control, PositiveGPUs: len(split.positiveGPUs), ControlGPUs: len(split.controlGPUs)}
			minimumPositives, minimumControls, minimumGPUs := policy.MinimumEvaluationPositives, policy.MinimumEvaluationControls, policy.MinimumEvaluationPositiveGPUs
			if splitName == "train" {
				minimumPositives, minimumControls, minimumGPUs = policy.MinimumTrainPositives, policy.MinimumTrainControls, policy.MinimumTrainPositiveGPUs
			}
			if split.positive < minimumPositives {
				item.BlockingReasons = append(item.BlockingReasons, fmt.Sprintf("%s_positive_count_%d_lt_%d", splitName, split.positive, minimumPositives))
				addDeficit(splitName, "positive_count", split.positive, minimumPositives)
			}
			if split.control < minimumControls {
				item.BlockingReasons = append(item.BlockingReasons, fmt.Sprintf("%s_control_count_%d_lt_%d", splitName, split.control, minimumControls))
				addDeficit(splitName, "control_count", split.control, minimumControls)
			}
			if len(split.positiveGPUs) < minimumGPUs {
				item.BlockingReasons = append(item.BlockingReasons, fmt.Sprintf("%s_positive_gpus_%d_lt_%d", splitName, len(split.positiveGPUs), minimumGPUs))
				addDeficit(splitName, "positive_gpus", len(split.positiveGPUs), minimumGPUs)
			}
		}
		if len(item.BlockingReasons) > 0 {
			item.Status = "insufficient_data"
			report.InsufficientStrata++
		} else {
			report.ReadyStrata++
		}
		report.Strata = append(report.Strata, item)
	}
	sort.Slice(report.Strata, func(i, j int) bool {
		if report.Strata[i].EventType != report.Strata[j].EventType {
			return report.Strata[i].EventType < report.Strata[j].EventType
		}
		if report.Strata[i].ModelName != report.Strata[j].ModelName {
			return report.Strata[i].ModelName < report.Strata[j].ModelName
		}
		return report.Strata[i].HorizonMinutes < report.Strata[j].HorizonMinutes
	})
	sort.Slice(report.Deficits, func(i, j int) bool {
		left, right := report.Deficits[i], report.Deficits[j]
		if left.Shortfall != right.Shortfall {
			return left.Shortfall > right.Shortfall
		}
		if left.EventType != right.EventType {
			return left.EventType < right.EventType
		}
		if left.ModelName != right.ModelName {
			return left.ModelName < right.ModelName
		}
		if left.HorizonMinutes != right.HorizonMinutes {
			return left.HorizonMinutes < right.HorizonMinutes
		}
		if left.Split != right.Split {
			return left.Split < right.Split
		}
		return left.Metric < right.Metric
	})
	report.RecommendedNextRun = matrixReadinessRecommendations(report)
	return report
}

func matrixReadinessRecommendations(report cohortReadinessReport) []string {
	recommendations := []string{}
	hasPendingControlSampling, needsTrain, needsEvaluation, needsGPUCoverage, needsControls := false, false, false, false, false
	for _, stratum := range report.Strata {
		if pending := stratum.Splits["pending_control_sampling"]; pending.PositiveCount > 0 || pending.ControlCount > 0 {
			hasPendingControlSampling = true
		}
	}
	for _, deficit := range report.Deficits {
		if deficit.Split == "train" {
			needsTrain = true
		}
		if deficit.Split == "validation" || deficit.Split == "test" {
			needsEvaluation = true
		}
		if deficit.Metric == "positive_gpus" {
			needsGPUCoverage = true
		}
		if deficit.Metric == "control_count" {
			needsControls = true
		}
	}
	if hasPendingControlSampling {
		recommendations = append(recommendations, "rerun manual-feedback training preparation and healthy-control extraction so pending_control_sampling positives receive leakage-safe splits")
	}
	if needsControls {
		recommendations = append(recommendations, "increase eligible healthy controls outside every known fault exclusion window before rebuilding the matrix")
	}
	if needsGPUCoverage {
		recommendations = append(recommendations, "accumulate confirmed faults from more distinct historical GPU identities for each fault/model/horizon stratum")
	}
	if needsTrain {
		recommendations = append(recommendations, "collect enough early time-ordered positives and controls to satisfy the train split gate")
	}
	if needsEvaluation {
		recommendations = append(recommendations, "collect later time-isolated positives and controls so validation and test splits can evaluate generalization")
	}
	if report.InsufficientStrata == 0 {
		recommendations = append(recommendations, "train only the exploratory-ready fault/model/horizon strata, then bind baseline artifact SHA256 before shadow evaluation")
	}
	return uniqueSortedStrings(recommendations)
}

type matrixAudit struct{ duplicates, entityConflicts, pointInTime, pairing, contract int }

type matrixAssemblyOptions struct {
	IncludePendingControlSampling bool
}

func assembleTrainingMatrix(positives []preparedTrainingSample, controls []controlFeatureRow, contract string) ([]trainingMatrixRow, []string, matrixAudit) {
	return assembleTrainingMatrixWithOptions(positives, controls, contract, matrixAssemblyOptions{})
}

func assembleTrainingMatrixWithOptions(positives []preparedTrainingSample, controls []controlFeatureRow, contract string, options matrixAssemblyOptions) ([]trainingMatrixRow, []string, matrixAudit) {
	rows := make([]trainingMatrixRow, 0, len(positives)+len(controls))
	audit := matrixAudit{}
	positiveByKey := map[string]preparedTrainingSample{}
	splitByGPU := map[string]string{}
	seen := map[string]bool{}
	columns := map[string]bool{}
	add := func(row trainingMatrixRow) {
		if seen[row.RowKey] {
			audit.duplicates++
			return
		}
		seen[row.RowKey] = true
		gpu := normalizeHistoricalGPUUUID(row.GPUUUID)
		if previous := splitByGPU[gpu]; previous != "" && previous != row.Split {
			audit.entityConflicts++
		} else {
			splitByGPU[gpu] = row.Split
		}
		for name := range row.Features {
			columns[name] = true
		}
		rows = append(rows, row)
	}
	for _, item := range positives {
		if item.TrainingStatus != "eligible" && !(options.IncludePendingControlSampling && item.TrainingStatus == "eligible_pending_controls") {
			continue
		}
		positiveByKey[item.Sample.SampleKey] = item
		if item.Sample.FeatureContract != contract {
			audit.contract++
			continue
		}
		if !item.Sample.FeatureCutoffAt.Before(item.Sample.LabelOnsetAt) {
			audit.pointInTime++
			continue
		}
		onset := item.Sample.LabelOnsetAt
		weight := item.Sample.LabelWeight
		if weight <= 0 {
			weight = 1
		}
		add(trainingMatrixRow{RowKey: "positive:" + item.Sample.SampleKey, SampleKey: item.Sample.SampleKey, SampleKind: "positive", LabelValue: 1, EvidenceWeight: weight,
			Split: item.Split, HorizonMinutes: item.Sample.HorizonMinutes, GPUUUID: item.Sample.GPUUUID, NodeIP: item.Sample.NodeIP, ModelName: item.Sample.ModelName,
			FeatureCutoffAt: item.Sample.FeatureCutoffAt, LabelOnsetAt: &onset, LabelMetadata: item.LabelMetadata,
			LoadBucket: gpuLoadBucket(item.Sample.Features["gpu_util_mean_24h"]), MetricCoverage: item.Sample.MetricCoverage, Features: item.Sample.Features})
	}
	for _, item := range controls {
		if item.TrainingStatus != "eligible" {
			continue
		}
		paired, ok := positiveByKey[item.Request.PairedSampleKey]
		if !ok || item.Request.Split != paired.Split || item.Request.HorizonMinutes != paired.Sample.HorizonMinutes || normalizeHistoricalGPUUUID(item.Request.GPUUUID) != normalizeHistoricalGPUUUID(paired.Sample.GPUUUID) {
			audit.pairing++
			continue
		}
		if item.Feature.FeatureContract != contract {
			audit.contract++
			continue
		}
		add(trainingMatrixRow{RowKey: "control:" + item.Request.ControlKey, SampleKey: item.Request.ControlKey, PairedSampleKey: item.Request.PairedSampleKey, SampleKind: "healthy_control", LabelValue: 0, EvidenceWeight: 1,
			Split: item.Request.Split, HorizonMinutes: item.Request.HorizonMinutes, GPUUUID: item.Request.GPUUUID, NodeIP: item.Request.NodeIP, ModelName: item.Request.ModelName,
			FeatureCutoffAt: item.Request.FeatureCutoffAt, LabelMetadata: paired.LabelMetadata,
			LoadBucket: item.ControlLoadBucket, MetricCoverage: item.Feature.MetricCoverage, Features: item.Feature.Features})
	}
	columnList := make([]string, 0, len(columns))
	for name := range columns {
		columnList = append(columnList, name)
	}
	sort.Strings(columnList)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Split != rows[j].Split {
			return rows[i].Split < rows[j].Split
		}
		if rows[i].HorizonMinutes != rows[j].HorizonMinutes {
			return rows[i].HorizonMinutes < rows[j].HorizonMinutes
		}
		return rows[i].RowKey < rows[j].RowKey
	})
	return rows, columnList, audit
}

func applyClassWeights(rows []trainingMatrixRow) {
	type counts struct{ positive, control int }
	byGroup := map[string]*counts{}
	for _, row := range rows {
		key := row.Split + ":" + strconv.Itoa(row.HorizonMinutes)
		if byGroup[key] == nil {
			byGroup[key] = &counts{}
		}
		if row.LabelValue == 1 {
			byGroup[key].positive++
		} else {
			byGroup[key].control++
		}
	}
	for index := range rows {
		row := &rows[index]
		count := byGroup[row.Split+":"+strconv.Itoa(row.HorizonMinutes)]
		total := float64(count.positive + count.control)
		if row.LabelValue == 1 {
			row.ClassWeight = total / (2 * float64(count.positive))
		} else {
			row.ClassWeight = total / (2 * float64(count.control))
		}
		row.TrainingWeight = row.EvidenceWeight * row.ClassWeight
	}
}

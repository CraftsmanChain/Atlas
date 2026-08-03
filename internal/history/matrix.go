package history

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"atlas/pkg/api"
)

const trainingMatrixVersion = "gpu-supervised-training-matrix-v2"

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
	TrainingMatrixKey      string         `json:"training_matrix_key"`
	Version                string         `json:"version"`
	SourcePreparedDataset  string         `json:"source_prepared_dataset_key"`
	SourceControlDataset   string         `json:"source_control_dataset_key"`
	FeatureContractVersion string         `json:"feature_contract_version"`
	FeatureColumns         []string       `json:"feature_columns"`
	Counts                 map[string]int `json:"counts"`
	PointInTimePolicy      string         `json:"point_in_time_policy"`
	SplitPolicy            string         `json:"split_policy"`
	MissingValuePolicy     string         `json:"missing_value_policy"`
	WeightPolicy           string         `json:"weight_policy"`
	MatrixFile             string         `json:"matrix_file"`
	MatrixSHA256           string         `json:"matrix_sha256"`
	CreatedAt              time.Time      `json:"created_at"`
}

func (s *Service) TrainingMatrixBuilds(limit int) ([]api.TrainingMatrixBuild, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.TrainingMatrixBuild
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
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
	if preparation.Version != preparationDatasetVersion {
		return api.TrainingMatrixBuild{}, fmt.Errorf("healthy-control features must come from training preparation %s", preparationDatasetVersion)
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
	rows, columns, audit := assembleTrainingMatrix(positives, controls, build.FeatureContractVersion)
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
	if build.TrainPositiveCount == 0 || build.TrainControlCount == 0 || build.ValidationPositiveCount == 0 || build.ValidationControlCount == 0 || build.TestPositiveCount == 0 || build.TestControlCount == 0 {
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
		MatrixFile:         filepath.Base(matrixPath), MatrixSHA256: checksum, CreatedAt: s.now(),
	}
	if err := writeJSONAtomic(manifestPath, manifest); err != nil {
		return err
	}
	finished := s.now()
	return s.db.Model(build).Updates(map[string]any{"status": "completed", "feature_column_count": build.FeatureColumnCount,
		"positive_count": build.PositiveCount, "control_count": build.ControlCount, "sample_count": build.SampleCount,
		"train_positive_count": build.TrainPositiveCount, "train_control_count": build.TrainControlCount,
		"validation_positive_count": build.ValidationPositiveCount, "validation_control_count": build.ValidationControlCount,
		"test_positive_count": build.TestPositiveCount, "test_control_count": build.TestControlCount,
		"matrix_path": matrixPath, "matrix_sha256": checksum, "manifest_path": manifestPath, "finished_at": &finished}).Error
}

type matrixAudit struct{ duplicates, entityConflicts, pointInTime, pairing, contract int }

func assembleTrainingMatrix(positives []preparedTrainingSample, controls []controlFeatureRow, contract string) ([]trainingMatrixRow, []string, matrixAudit) {
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
		if item.TrainingStatus != "eligible" {
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

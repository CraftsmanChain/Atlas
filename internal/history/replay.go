package history

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"atlas/internal/featurestats"
	promclient "atlas/internal/prometheus"
	"atlas/pkg/api"
)

const (
	featureReplayVersion     = "gpu-feature-value-replay-v1"
	replayAbsoluteTolerance  = 1e-9
	replayRelativeTolerance  = 1e-9
	defaultReplaySampleCount = 12
	maximumReplaySampleCount = 60
)

type FeatureReplayRequest struct {
	ModelSpecID uint `json:"model_spec_id,omitempty"`
	SampleCount int  `json:"sample_count,omitempty"`
}

type replayArtifact struct {
	Models []struct {
		HorizonMinutes int      `json:"horizon_minutes"`
		FeatureColumns []string `json:"feature_columns"`
	} `json:"models"`
}

type replayColumnResult struct {
	Compared             int     `json:"compared"`
	Mismatched           int     `json:"mismatched"`
	MissingTraining      int     `json:"missing_training"`
	MissingReplay        int     `json:"missing_replay"`
	MaximumAbsoluteError float64 `json:"maximum_absolute_error"`
	MaximumRelativeError float64 `json:"maximum_relative_error"`
}

type replaySampleResult struct {
	RowKey          string    `json:"row_key"`
	GPUUUID         string    `json:"gpu_uuid"`
	Split           string    `json:"split"`
	LabelValue      int       `json:"label_value"`
	FeatureCutoffAt time.Time `json:"feature_cutoff_at"`
	Status          string    `json:"status"`
	Compared        int       `json:"compared"`
	Mismatched      int       `json:"mismatched"`
	MissingTraining int       `json:"missing_training"`
	MissingReplay   int       `json:"missing_replay"`
	ErrorMessage    string    `json:"error_message,omitempty"`
}

type featureReplayReport struct {
	Version                       string                         `json:"version"`
	ReplayKey                     string                         `json:"replay_key"`
	ModelKey                      string                         `json:"model_key"`
	ModelVersion                  string                         `json:"model_version"`
	SourceMatrixKey               string                         `json:"source_matrix_key"`
	TransformationContractVersion string                         `json:"transformation_contract_version"`
	AbsoluteTolerance             float64                        `json:"absolute_tolerance"`
	RelativeTolerance             float64                        `json:"relative_tolerance"`
	Columns                       map[string]*replayColumnResult `json:"columns"`
	Samples                       []replaySampleResult           `json:"samples"`
	CreatedAt                     time.Time                      `json:"created_at"`
}

func (s *Service) FeatureReplayRuns(limit int) ([]api.PredictionFeatureReplayRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.PredictionFeatureReplayRun
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) StartFeatureReplay(request FeatureReplayRequest) (api.PredictionFeatureReplayRun, error) {
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	if s.replayRunning {
		return api.PredictionFeatureReplayRun{}, fmt.Errorf("a feature value replay is already running")
	}
	if request.SampleCount == 0 {
		request.SampleCount = defaultReplaySampleCount
	}
	if request.SampleCount < 1 || request.SampleCount > maximumReplaySampleCount {
		return api.PredictionFeatureReplayRun{}, fmt.Errorf("sample_count must be between 1 and %d", maximumReplaySampleCount)
	}
	var spec api.PredictionModelSpec
	query := s.db.Where("current = ? AND status = ?", true, "shadow_candidate")
	if request.ModelSpecID > 0 {
		query = query.Where("id = ?", request.ModelSpecID)
	}
	result := query.Order("trained_at DESC, id DESC").Limit(1).Find(&spec)
	if result.Error != nil {
		return api.PredictionFeatureReplayRun{}, result.Error
	}
	if result.RowsAffected == 0 {
		return api.PredictionFeatureReplayRun{}, fmt.Errorf("a current shadow model candidate is required")
	}
	var baseline api.BaselineModelBuild
	if err := s.db.First(&baseline, spec.SourceBaselineBuildID).Error; err != nil {
		return api.PredictionFeatureReplayRun{}, err
	}
	var matrix api.TrainingMatrixBuild
	if err := s.db.First(&matrix, baseline.SourceMatrixBuildID).Error; err != nil {
		return api.PredictionFeatureReplayRun{}, err
	}
	var preparation api.TrainingPreparationBuild
	if err := s.db.First(&preparation, matrix.SourcePreparationBuildID).Error; err != nil {
		return api.PredictionFeatureReplayRun{}, err
	}
	var featureBuild api.TrainingFeatureBuild
	if err := s.db.First(&featureBuild, preparation.SourceFeatureBuildID).Error; err != nil {
		return api.PredictionFeatureReplayRun{}, err
	}
	started := s.now()
	key := featureReplayVersion + "-" + strconv.FormatInt(started.UTC().UnixNano(), 10)
	run := api.PredictionFeatureReplayRun{
		ReplayKey: key, Version: featureReplayVersion, Status: "queued",
		ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version,
		SourceBaselineBuildID: baseline.ID, SourceMatrixBuildID: matrix.ID, SourceKey: featureBuild.SourceKey,
		TransformationContractVersion: featurestats.Trailing24hContractVersion,
		RequestedSampleCount:          request.SampleCount,
		OutputDir:                     filepath.Join(s.config.DatasetDir, "feature-replays", key), StartedAt: started,
	}
	if err := s.db.Create(&run).Error; err != nil {
		return run, err
	}
	s.replayRunning = true
	go s.executeFeatureReplay(run.ID)
	return run, nil
}

func (s *Service) executeFeatureReplay(id uint) {
	defer func() { s.replayMu.Lock(); s.replayRunning = false; s.replayMu.Unlock() }()
	var run api.PredictionFeatureReplayRun
	if err := s.db.First(&run, id).Error; err != nil {
		return
	}
	if err := s.db.Model(&run).Update("status", "running").Error; err != nil {
		return
	}
	if err := s.buildFeatureReplay(&run); err != nil {
		finished := s.now()
		_ = s.db.Model(&run).Updates(map[string]any{"status": "failed", "error_message": err.Error(), "finished_at": &finished}).Error
	}
}

func (s *Service) buildFeatureReplay(run *api.PredictionFeatureReplayRun) error {
	var spec api.PredictionModelSpec
	var baseline api.BaselineModelBuild
	var matrix api.TrainingMatrixBuild
	if err := s.db.First(&spec, run.ModelSpecID).Error; err != nil {
		return err
	}
	if err := s.db.First(&baseline, run.SourceBaselineBuildID).Error; err != nil {
		return err
	}
	if err := s.db.First(&matrix, run.SourceMatrixBuildID).Error; err != nil {
		return err
	}
	if err := verifyFileSHA256(spec.ArtifactURI, spec.ArtifactSHA256); err != nil {
		return fmt.Errorf("model artifact integrity: %w", err)
	}
	if err := verifyFileSHA256(matrix.MatrixPath, matrix.MatrixSHA256); err != nil {
		return fmt.Errorf("training matrix integrity: %w", err)
	}
	var artifact replayArtifact
	file, err := os.Open(spec.ArtifactURI)
	if err != nil {
		return err
	}
	err = json.NewDecoder(file).Decode(&artifact)
	_ = file.Close()
	if err != nil {
		return err
	}
	columns := replayFeatureColumns(artifact, spec.HorizonMinutes)
	if len(columns) == 0 {
		return fmt.Errorf("registered model horizon has no feature columns")
	}
	rows, err := readJSONLines[trainingMatrixRow](matrix.MatrixPath)
	if err != nil {
		return err
	}
	selected := selectReplayRows(rows, spec, columns, run.RequestedSampleCount)
	if len(selected) == 0 {
		return fmt.Errorf("no matrix rows match the registered model scope")
	}
	run.SelectedSampleCount, run.TrainingFeatureCount = len(selected), len(columns)
	if err := s.db.Model(run).Updates(map[string]any{"selected_sample_count": len(selected), "training_feature_count": len(columns)}).Error; err != nil {
		return err
	}
	source, err := s.resolveSource(run.SourceKey)
	if err != nil {
		return err
	}
	client, err := s.historyClient(source)
	if err != nil {
		return err
	}
	report := featureReplayReport{
		Version: featureReplayVersion, ReplayKey: run.ReplayKey, ModelKey: spec.ModelKey,
		ModelVersion: spec.Version, SourceMatrixKey: matrix.TrainingMatrixKey,
		TransformationContractVersion: featurestats.Trailing24hContractVersion,
		AbsoluteTolerance:             replayAbsoluteTolerance, RelativeTolerance: replayRelativeTolerance,
		Columns: map[string]*replayColumnResult{}, Samples: make([]replaySampleResult, 0, len(selected)), CreatedAt: s.now(),
	}
	for _, column := range columns {
		report.Columns[column] = &replayColumnResult{}
	}
	for _, row := range selected {
		result := s.replayOneRow(client, row, columns, report.Columns)
		report.Samples = append(report.Samples, result)
		if result.Status == "completed" {
			run.CompletedSampleCount++
		} else {
			run.FailedSampleCount++
		}
		run.ComparedValueCount += result.Compared
		run.MismatchCount += result.Mismatched
		run.MissingTrainingValueCount += result.MissingTraining
		run.MissingReplayValueCount += result.MissingReplay
	}
	failedColumns := api.StringList{}
	for column, result := range report.Columns {
		if result.Compared > 0 && result.Mismatched == 0 {
			run.VerifiedColumnCount++
		}
		if result.Compared == 0 || result.Mismatched > 0 || result.MissingReplay > 0 {
			failedColumns = append(failedColumns, column)
		}
		run.MaximumAbsoluteError = math.Max(run.MaximumAbsoluteError, result.MaximumAbsoluteError)
		run.MaximumRelativeError = math.Max(run.MaximumRelativeError, result.MaximumRelativeError)
	}
	sort.Strings(failedColumns)
	run.FailedColumns = failedColumns
	status := "passed"
	blocking := api.StringList{}
	if run.FailedSampleCount > 0 {
		status = "failed"
		blocking = append(blocking, "prometheus_replay_query_failed")
	}
	if run.VerifiedColumnCount != run.TrainingFeatureCount || run.MismatchCount > 0 || run.MissingReplayValueCount > 0 {
		status = "failed"
		blocking = append(blocking, "feature_value_parity_failed")
	}
	run.BlockingReasons = blocking
	if err := os.MkdirAll(run.OutputDir, 0o750); err != nil {
		return err
	}
	reportPath := filepath.Join(run.OutputDir, "replay_report.json")
	if err := writeJSONAtomic(reportPath, report); err != nil {
		return err
	}
	checksum, err := fileSHA256(reportPath)
	if err != nil {
		return err
	}
	finished := s.now()
	updates := map[string]any{
		"status": status, "completed_sample_count": run.CompletedSampleCount, "failed_sample_count": run.FailedSampleCount,
		"compared_value_count": run.ComparedValueCount, "verified_column_count": run.VerifiedColumnCount,
		"mismatch_count": run.MismatchCount, "missing_training_value_count": run.MissingTrainingValueCount,
		"missing_replay_value_count": run.MissingReplayValueCount, "maximum_absolute_error": run.MaximumAbsoluteError,
		"maximum_relative_error": run.MaximumRelativeError, "failed_columns": run.FailedColumns,
		"blocking_reasons": run.BlockingReasons, "report_path": reportPath, "report_sha256": checksum, "finished_at": &finished,
	}
	if err := s.db.Model(run).Updates(updates).Error; err != nil {
		return err
	}
	return s.updateParityFromReplay(spec.ID, status, run.VerifiedColumnCount, blocking)
}

func (s *Service) replayOneRow(client *promclient.Client, row trainingMatrixRow, columns []string, results map[string]*replayColumnResult) replaySampleResult {
	result := replaySampleResult{RowKey: row.RowKey, GPUUUID: row.GPUUUID, Split: row.Split, LabelValue: row.LabelValue, FeatureCutoffAt: row.FeatureCutoffAt, Status: "completed"}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	series, err := client.QueryRange(ctx, historicalMetricQuery(row.GPUUUID), row.FeatureCutoffAt.Add(-featureLookback), row.FeatureCutoffAt, featureQueryStep)
	cancel()
	if err != nil {
		result.Status, result.ErrorMessage = "failed", err.Error()
		return result
	}
	canonical := canonicalSeries(series)
	replayed := map[string]float64{}
	sources := map[string]struct{}{}
	for _, column := range columns {
		source, _, _ := featurestats.ParseTrailing24hColumn(column)
		sources[source] = struct{}{}
	}
	for source := range sources {
		points := pointsInWindow(canonical[source], row.FeatureCutoffAt.Add(-featureLookback), row.FeatureCutoffAt)
		if len(points) > 0 {
			featurestats.AddTrailing24hStatistics(replayed, source, points)
		}
	}
	for _, column := range columns {
		columnResult := results[column]
		expected, expectedExists := row.Features[column]
		actual, actualExists := replayed[column]
		if !expectedExists {
			columnResult.MissingTraining++
			result.MissingTraining++
			continue
		}
		if !actualExists {
			columnResult.MissingReplay++
			result.MissingReplay++
			continue
		}
		absolute := math.Abs(actual - expected)
		relative := absolute / math.Max(1, math.Abs(expected))
		columnResult.Compared++
		result.Compared++
		columnResult.MaximumAbsoluteError = math.Max(columnResult.MaximumAbsoluteError, absolute)
		columnResult.MaximumRelativeError = math.Max(columnResult.MaximumRelativeError, relative)
		if absolute > replayAbsoluteTolerance && relative > replayRelativeTolerance {
			columnResult.Mismatched++
			result.Mismatched++
		}
	}
	return result
}

func replayFeatureColumns(artifact replayArtifact, horizon int) []string {
	for _, model := range artifact.Models {
		if model.HorizonMinutes == horizon {
			return append([]string(nil), model.FeatureColumns...)
		}
	}
	return nil
}

func selectReplayRows(rows []trainingMatrixRow, spec api.PredictionModelSpec, columns []string, limit int) []trainingMatrixRow {
	grouped := map[string][]trainingMatrixRow{}
	for _, row := range rows {
		if row.HorizonMinutes != spec.HorizonMinutes || row.ModelName != spec.ScopeModelName || !stringSliceContains(row.LabelMetadata.EventTypes, spec.ScopeEventType) {
			continue
		}
		key := row.Split + ":" + strconv.Itoa(row.LabelValue)
		grouped[key] = append(grouped[key], row)
	}
	keys := []string{"train:1", "train:0", "validation:1", "validation:0", "test:1", "test:0"}
	for _, key := range keys {
		sort.Slice(grouped[key], func(i, j int) bool {
			left, right := replayPresentCount(grouped[key][i], columns), replayPresentCount(grouped[key][j], columns)
			if left != right {
				return left > right
			}
			return grouped[key][i].RowKey < grouped[key][j].RowKey
		})
	}
	selected := make([]trainingMatrixRow, 0, limit)
	for offset := 0; len(selected) < limit; offset++ {
		added := false
		for _, key := range keys {
			if offset < len(grouped[key]) && len(selected) < limit {
				selected = append(selected, grouped[key][offset])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return selected
}

func replayPresentCount(row trainingMatrixRow, columns []string) int {
	count := 0
	for _, column := range columns {
		if _, exists := row.Features[column]; exists {
			count++
		}
	}
	return count
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Service) updateParityFromReplay(modelSpecID uint, status string, verified int, replayReasons api.StringList) error {
	var audit api.PredictionFeatureParityAudit
	result := s.db.Where("model_spec_id = ?", modelSpecID).Limit(1).Find(&audit)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("feature contract parity audit is required before value replay")
	}
	parityStatus := "live_coverage_required"
	reasons := api.StringList{"live_24h_coverage_not_verified"}
	if status != "passed" {
		parityStatus = "blocked_replay"
		reasons = append(api.StringList(nil), replayReasons...)
	}
	return s.db.Model(&audit).Updates(map[string]any{
		"status": parityStatus, "replay_verified_count": verified,
		"blocking_reasons": reasons, "scoring_allowed": false, "audited_at": s.now(),
	}).Error
}

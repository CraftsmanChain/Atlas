package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm/clause"

	"atlas/pkg/api"
)

const featureDistributionSnapshotVersion = "gpu-feature-distribution-snapshot-v1"

type FeatureDistributionSnapshotRequest struct {
	SourceBaselineBuildID uint `json:"source_baseline_build_id,omitempty"`
}

type featureDistributionMaterialization struct {
	Version               string                                      `json:"version"`
	DistributionRole      string                                      `json:"distribution_role"`
	SourceBaselineBuildID uint                                        `json:"source_baseline_build_id"`
	SourceMatrixBuildID   uint                                        `json:"source_matrix_build_id"`
	TrainingMatrixKey     string                                      `json:"training_matrix_key"`
	BaselineModelKey      string                                      `json:"baseline_model_key"`
	BaselineVersion       string                                      `json:"baseline_version"`
	ArtifactVersion       string                                      `json:"artifact_version"`
	ArtifactModelCount    int                                         `json:"artifact_model_count"`
	FeatureContract       string                                      `json:"feature_contract_version"`
	FeatureName           string                                      `json:"feature_name"`
	SampleCount           int                                         `json:"sample_count"`
	MissingCount          int                                         `json:"missing_count"`
	MissingRatio          float64                                     `json:"missing_ratio"`
	Mean                  float64                                     `json:"mean"`
	Stddev                float64                                     `json:"stddev"`
	Minimum               float64                                     `json:"minimum"`
	P25                   float64                                     `json:"p25"`
	P50                   float64                                     `json:"p50"`
	P75                   float64                                     `json:"p75"`
	P90                   float64                                     `json:"p90"`
	P95                   float64                                     `json:"p95"`
	P99                   float64                                     `json:"p99"`
	Maximum               float64                                     `json:"maximum"`
	BinEdges              api.FloatList                               `json:"bin_edges"`
	BinProportions        api.FloatList                               `json:"bin_proportions"`
	BlockingReasons       api.StringList                              `json:"blocking_reasons"`
	NoRawSamplesStored    bool                                        `json:"no_raw_samples_stored"`
	Inputs                featureDistributionMaterializationInputRefs `json:"inputs"`
}

type featureDistributionMaterializationInputRefs struct {
	ArtifactSHA256 string `json:"artifact_sha256"`
	MatrixSHA256   string `json:"matrix_sha256"`
}

func (s *Service) FeatureDistributionSnapshots(limit int) ([]api.PredictionFeatureDistributionSnapshot, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []api.PredictionFeatureDistributionSnapshot
	err := s.db.Order("observed_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) MaterializeTrainingFeatureDistributions(request FeatureDistributionSnapshotRequest) ([]api.PredictionFeatureDistributionSnapshot, error) {
	var baseline api.BaselineModelBuild
	query := s.db.Where("status = ? AND artifact_path <> ? AND source_matrix_build_id <> ?", "completed", "", 0)
	if request.SourceBaselineBuildID > 0 {
		query = query.Where("id = ?", request.SourceBaselineBuildID)
	}
	result := query.Order("finished_at DESC, id DESC").Limit(1).Find(&baseline)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("a completed baseline model artifact is required")
	}
	if baseline.ArtifactSHA256 != "" {
		if err := verifyFileSHA256(baseline.ArtifactPath, baseline.ArtifactSHA256); err != nil {
			return nil, fmt.Errorf("baseline artifact checksum: %w", err)
		}
	}
	var artifact baselineArtifact
	if err := readJSONFile(baseline.ArtifactPath, &artifact); err != nil {
		return nil, fmt.Errorf("decode baseline artifact: %w", err)
	}
	columns := uniqueBaselineFeatureColumns(artifact)
	if len(columns) == 0 {
		return nil, fmt.Errorf("baseline artifact has no feature columns")
	}
	var matrix api.TrainingMatrixBuild
	if err := s.db.First(&matrix, baseline.SourceMatrixBuildID).Error; err != nil {
		return nil, err
	}
	if matrix.Status != "completed" || matrix.MatrixPath == "" {
		return nil, fmt.Errorf("completed training matrix is required")
	}
	if err := verifyFileSHA256(matrix.MatrixPath, matrix.MatrixSHA256); err != nil {
		return nil, fmt.Errorf("matrix checksum: %w", err)
	}
	rows, err := readJSONLines[trainingMatrixRow](matrix.MatrixPath)
	if err != nil {
		return nil, err
	}
	rows, err = scopedTrainingDistributionRows(matrix.TrainingMatrixKey, rows, baseline)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("baseline training split contains no rows")
	}
	now := s.now()
	snapshots := make([]api.PredictionFeatureDistributionSnapshot, 0, len(columns))
	for _, column := range columns {
		snapshot := trainingFeatureDistributionSnapshot(baseline, matrix, artifact, rows, column, now)
		snapshots = append(snapshots, snapshot)
	}
	for index := range snapshots {
		if err := s.db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "snapshot_key"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"version", "status", "distribution_role", "source_baseline_build_id", "model_key", "model_version",
				"feature_contract_version", "scope_model_name", "source_key", "feature_name", "sample_count",
				"missing_count", "missing_ratio", "mean", "stddev", "minimum", "p25", "p50", "p75", "p90", "p95",
				"p99", "maximum", "bin_edges", "bin_proportions", "report_sha256", "blocking_reasons", "observed_at", "updated_at",
			}),
		}).Create(&snapshots[index]).Error; err != nil {
			return nil, err
		}
	}
	return snapshots, nil
}

func readJSONFile(path string, out any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	return decoder.Decode(out)
}

func uniqueBaselineFeatureColumns(artifact baselineArtifact) []string {
	seen := map[string]bool{}
	columns := make([]string, 0)
	for _, model := range artifact.Models {
		for _, column := range model.FeatureColumns {
			column = strings.TrimSpace(column)
			if column == "" || seen[column] {
				continue
			}
			seen[column] = true
			columns = append(columns, column)
		}
	}
	sort.Strings(columns)
	return columns
}

func scopedTrainingDistributionRows(matrixKey string, rows []trainingMatrixRow, baseline api.BaselineModelBuild) ([]trainingMatrixRow, error) {
	if baseline.ScopeEventType != "" {
		var err error
		rows, err = scopedReadyMatrixRows(matrixKey, rows, baseline.ScopeEventType, baseline.ScopeModelName)
		if err != nil {
			return nil, err
		}
	}
	result := make([]trainingMatrixRow, 0, len(rows))
	for _, row := range rows {
		if row.Split == "train" {
			result = append(result, row)
		}
	}
	return result, nil
}

func trainingFeatureDistributionSnapshot(baseline api.BaselineModelBuild, matrix api.TrainingMatrixBuild, artifact baselineArtifact, rows []trainingMatrixRow, column string, observedAt time.Time) api.PredictionFeatureDistributionSnapshot {
	values := make([]float64, 0, len(rows))
	missing := 0
	for _, row := range rows {
		value, ok := row.Features[column]
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			missing++
			continue
		}
		values = append(values, value)
	}
	stats := describeDistribution(values)
	blocking := api.StringList{}
	status := "completed"
	if len(values) == 0 {
		status = "blocked_no_values"
		blocking = append(blocking, "no numeric training values for feature")
	}
	total := len(rows)
	missingRatio := 0.0
	if total > 0 {
		missingRatio = float64(missing) / float64(total)
	}
	snapshot := api.PredictionFeatureDistributionSnapshot{
		SnapshotKey:            "training-" + strconv.FormatUint(uint64(baseline.ID), 10) + "-" + stableKeyPart(column),
		Version:                featureDistributionSnapshotVersion,
		Status:                 status,
		DistributionRole:       "training",
		SourceBaselineBuildID:  baseline.ID,
		ModelKey:               baseline.BaselineModelKey,
		ModelVersion:           baseline.Version,
		FeatureContractVersion: baseline.FeatureContractVersion,
		ScopeModelName:         baseline.ScopeModelName,
		SourceKey:              matrix.TrainingMatrixKey,
		FeatureName:            column,
		SampleCount:            len(values),
		MissingCount:           missing,
		MissingRatio:           missingRatio,
		Mean:                   stats.mean,
		Stddev:                 stats.stddev,
		Minimum:                stats.minimum,
		P25:                    stats.p25,
		P50:                    stats.p50,
		P75:                    stats.p75,
		P90:                    stats.p90,
		P95:                    stats.p95,
		P99:                    stats.p99,
		Maximum:                stats.maximum,
		BinEdges:               stats.binEdges,
		BinProportions:         stats.binProportions,
		BlockingReasons:        blocking,
		ObservedAt:             observedAt,
	}
	snapshot.ReportSHA256 = featureDistributionSnapshotSHA(snapshot, matrix, baseline, artifact)
	return snapshot
}

func (s *Service) liveShadowFeatureDistributionSnapshots(spec api.PredictionModelSpec, run api.PredictionShadowScoringRun, columns []string, valuesByFeature map[string][]float64, observedAt time.Time, reportSHA string) ([]api.PredictionFeatureDistributionSnapshot, error) {
	trainingByFeature := map[string]api.PredictionFeatureDistributionSnapshot{}
	if spec.SourceBaselineBuildID > 0 {
		var training []api.PredictionFeatureDistributionSnapshot
		if err := s.db.Where("source_baseline_build_id = ? AND distribution_role = ? AND status IN ?", spec.SourceBaselineBuildID, "training", []string{"completed", "passed"}).Order("observed_at DESC, id DESC").Find(&training).Error; err != nil {
			return nil, err
		}
		for _, snapshot := range training {
			if _, exists := trainingByFeature[snapshot.FeatureName]; !exists {
				trainingByFeature[snapshot.FeatureName] = snapshot
			}
		}
	}
	snapshots := make([]api.PredictionFeatureDistributionSnapshot, 0, len(columns))
	for _, column := range columns {
		values := valuesByFeature[column]
		training := trainingByFeature[column]
		stats := describeDistributionWithEdges(values, training.BinEdges)
		blocking := api.StringList{}
		status := "completed"
		if len(values) == 0 {
			status = "blocked_no_values"
			blocking = append(blocking, "no scored live-shadow values for feature")
		}
		if len(training.BinEdges) < 2 {
			status = "blocked_missing_training_bins"
			blocking = append(blocking, "matching training histogram bins are required before live-shadow PSI/KS")
		}
		missing := run.ScoredGPUCount - len(values)
		if missing < 0 {
			missing = 0
		}
		missingRatio := 0.0
		if run.ScoredGPUCount > 0 {
			missingRatio = float64(missing) / float64(run.ScoredGPUCount)
		}
		snapshot := api.PredictionFeatureDistributionSnapshot{
			SnapshotKey:            "live-shadow-" + strconv.FormatUint(uint64(spec.SourceBaselineBuildID), 10) + "-" + strconv.FormatUint(uint64(run.ID), 10) + "-" + stableKeyPart(column),
			Version:                featureDistributionSnapshotVersion,
			Status:                 status,
			DistributionRole:       "live_shadow",
			SourceBaselineBuildID:  spec.SourceBaselineBuildID,
			ModelSpecID:            spec.ID,
			ModelKey:               spec.ModelKey,
			ModelVersion:           spec.Version,
			FeatureContractVersion: spec.FeatureContractVersion,
			ScopeModelName:         spec.ScopeModelName,
			SourceKey:              run.RunKey,
			FeatureName:            column,
			SampleCount:            len(values),
			MissingCount:           missing,
			MissingRatio:           missingRatio,
			Mean:                   stats.mean,
			Stddev:                 stats.stddev,
			Minimum:                stats.minimum,
			P25:                    stats.p25,
			P50:                    stats.p50,
			P75:                    stats.p75,
			P90:                    stats.p90,
			P95:                    stats.p95,
			P99:                    stats.p99,
			Maximum:                stats.maximum,
			BinEdges:               stats.binEdges,
			BinProportions:         stats.binProportions,
			BlockingReasons:        uniqueSortedStrings(blocking),
			ObservedAt:             observedAt,
		}
		snapshot.ReportSHA256 = liveFeatureDistributionSnapshotSHA(snapshot, run, reportSHA, training.ReportSHA256)
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

type distributionStats struct {
	mean, stddev, minimum, p25, p50, p75, p90, p95, p99, maximum float64
	binEdges, binProportions                                     api.FloatList
}

func describeDistribution(values []float64) distributionStats {
	return describeDistributionWithEdges(values, nil)
}

func describeDistributionWithEdges(values []float64, histogramEdges api.FloatList) distributionStats {
	if len(values) == 0 {
		return distributionStats{binEdges: api.FloatList{}, binProportions: api.FloatList{}}
	}
	sortedValues := append([]float64(nil), values...)
	sort.Float64s(sortedValues)
	stats := distributionStats{
		minimum: sortedValues[0],
		p25:     percentileSorted(sortedValues, 0.25),
		p50:     percentileSorted(sortedValues, 0.50),
		p75:     percentileSorted(sortedValues, 0.75),
		p90:     percentileSorted(sortedValues, 0.90),
		p95:     percentileSorted(sortedValues, 0.95),
		p99:     percentileSorted(sortedValues, 0.99),
		maximum: sortedValues[len(sortedValues)-1],
	}
	for _, value := range sortedValues {
		stats.mean += value
	}
	stats.mean /= float64(len(sortedValues))
	if len(sortedValues) > 1 {
		for _, value := range sortedValues {
			delta := value - stats.mean
			stats.stddev += delta * delta
		}
		stats.stddev = math.Sqrt(stats.stddev / float64(len(sortedValues)-1))
	}
	if len(histogramEdges) >= 2 {
		stats.binEdges, stats.binProportions = distributionHistogramWithEdges(sortedValues, histogramEdges)
	} else {
		stats.binEdges, stats.binProportions = distributionHistogram(sortedValues, 10)
	}
	return stats
}

func percentileSorted(sortedValues []float64, quantile float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	position := quantile * float64(len(sortedValues)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sortedValues[lower]
	}
	weight := position - float64(lower)
	return sortedValues[lower]*(1-weight) + sortedValues[upper]*weight
}

func distributionHistogram(sortedValues []float64, requestedBins int) (api.FloatList, api.FloatList) {
	if len(sortedValues) == 0 {
		return api.FloatList{}, api.FloatList{}
	}
	if requestedBins <= 0 {
		requestedBins = 10
	}
	minimum, maximum := sortedValues[0], sortedValues[len(sortedValues)-1]
	if minimum == maximum {
		return api.FloatList{minimum, maximum}, api.FloatList{1}
	}
	bins := requestedBins
	if len(sortedValues) < bins {
		bins = len(sortedValues)
	}
	edges := make(api.FloatList, bins+1)
	proportions := make(api.FloatList, bins)
	width := (maximum - minimum) / float64(bins)
	for index := 0; index <= bins; index++ {
		edges[index] = minimum + float64(index)*width
	}
	edges[len(edges)-1] = maximum
	for _, value := range sortedValues {
		index := int((value - minimum) / width)
		if index >= bins {
			index = bins - 1
		}
		if index < 0 {
			index = 0
		}
		proportions[index]++
	}
	for index := range proportions {
		proportions[index] /= float64(len(sortedValues))
	}
	return edges, proportions
}

func distributionHistogramWithEdges(sortedValues []float64, edges api.FloatList) (api.FloatList, api.FloatList) {
	if len(sortedValues) == 0 || len(edges) < 2 {
		return api.FloatList{}, api.FloatList{}
	}
	copiedEdges := append(api.FloatList(nil), edges...)
	proportions := make(api.FloatList, len(copiedEdges)-1)
	for _, value := range sortedValues {
		index := sort.Search(len(copiedEdges)-1, func(i int) bool { return value < copiedEdges[i+1] })
		if index >= len(proportions) {
			index = len(proportions) - 1
		}
		if index < 0 {
			index = 0
		}
		proportions[index]++
	}
	for index := range proportions {
		proportions[index] /= float64(len(sortedValues))
	}
	return copiedEdges, proportions
}

func stableKeyPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-", "|", "-", "_", "-")
	value = replacer.Replace(value)
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			builder.WriteRune(char)
		}
	}
	result := strings.Trim(builder.String(), "-")
	if len(result) > 48 {
		sum := sha256.Sum256([]byte(value))
		result = result[:32] + "-" + hex.EncodeToString(sum[:])[:12]
	}
	if result == "" {
		sum := sha256.Sum256([]byte(value))
		result = hex.EncodeToString(sum[:])[:24]
	}
	return result
}

func uniqueSortedStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func featureDistributionSnapshotSHA(snapshot api.PredictionFeatureDistributionSnapshot, matrix api.TrainingMatrixBuild, baseline api.BaselineModelBuild, artifact baselineArtifact) string {
	materialization := featureDistributionMaterialization{
		Version:               snapshot.Version,
		DistributionRole:      snapshot.DistributionRole,
		SourceBaselineBuildID: snapshot.SourceBaselineBuildID,
		SourceMatrixBuildID:   matrix.ID,
		TrainingMatrixKey:     matrix.TrainingMatrixKey,
		BaselineModelKey:      baseline.BaselineModelKey,
		BaselineVersion:       baseline.Version,
		ArtifactVersion:       artifact.Version,
		ArtifactModelCount:    len(artifact.Models),
		FeatureContract:       snapshot.FeatureContractVersion,
		FeatureName:           snapshot.FeatureName,
		SampleCount:           snapshot.SampleCount,
		MissingCount:          snapshot.MissingCount,
		MissingRatio:          snapshot.MissingRatio,
		Mean:                  snapshot.Mean,
		Stddev:                snapshot.Stddev,
		Minimum:               snapshot.Minimum,
		P25:                   snapshot.P25,
		P50:                   snapshot.P50,
		P75:                   snapshot.P75,
		P90:                   snapshot.P90,
		P95:                   snapshot.P95,
		P99:                   snapshot.P99,
		Maximum:               snapshot.Maximum,
		BinEdges:              append(api.FloatList(nil), snapshot.BinEdges...),
		BinProportions:        append(api.FloatList(nil), snapshot.BinProportions...),
		BlockingReasons:       append(api.StringList(nil), snapshot.BlockingReasons...),
		NoRawSamplesStored:    true,
		Inputs: featureDistributionMaterializationInputRefs{
			ArtifactSHA256: baseline.ArtifactSHA256,
			MatrixSHA256:   matrix.MatrixSHA256,
		},
	}
	payload, _ := json.Marshal(materialization)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func liveFeatureDistributionSnapshotSHA(snapshot api.PredictionFeatureDistributionSnapshot, run api.PredictionShadowScoringRun, runReportSHA, trainingSnapshotSHA string) string {
	payload, _ := json.Marshal(struct {
		Version               string         `json:"version"`
		DistributionRole      string         `json:"distribution_role"`
		SourceBaselineBuildID uint           `json:"source_baseline_build_id"`
		ModelSpecID           uint           `json:"model_spec_id"`
		ModelKey              string         `json:"model_key"`
		ModelVersion          string         `json:"model_version"`
		RunID                 uint           `json:"run_id"`
		RunKey                string         `json:"run_key"`
		RunReportSHA256       string         `json:"run_report_sha256"`
		TrainingSnapshotSHA   string         `json:"training_snapshot_sha256"`
		FeatureName           string         `json:"feature_name"`
		SampleCount           int            `json:"sample_count"`
		MissingCount          int            `json:"missing_count"`
		MissingRatio          float64        `json:"missing_ratio"`
		Mean                  float64        `json:"mean"`
		Stddev                float64        `json:"stddev"`
		Minimum               float64        `json:"minimum"`
		P25                   float64        `json:"p25"`
		P50                   float64        `json:"p50"`
		P75                   float64        `json:"p75"`
		P90                   float64        `json:"p90"`
		P95                   float64        `json:"p95"`
		P99                   float64        `json:"p99"`
		Maximum               float64        `json:"maximum"`
		BinEdges              api.FloatList  `json:"bin_edges"`
		BinProportions        api.FloatList  `json:"bin_proportions"`
		BlockingReasons       api.StringList `json:"blocking_reasons"`
		NoRawSamplesStored    bool           `json:"no_raw_samples_stored"`
		NoAlertEmitted        bool           `json:"no_alert_emitted"`
		NoActionExecuted      bool           `json:"no_action_executed"`
	}{
		Version: snapshot.Version, DistributionRole: snapshot.DistributionRole,
		SourceBaselineBuildID: snapshot.SourceBaselineBuildID, ModelSpecID: snapshot.ModelSpecID,
		ModelKey: snapshot.ModelKey, ModelVersion: snapshot.ModelVersion, RunID: run.ID, RunKey: run.RunKey,
		RunReportSHA256: runReportSHA, TrainingSnapshotSHA: trainingSnapshotSHA, FeatureName: snapshot.FeatureName,
		SampleCount: snapshot.SampleCount, MissingCount: snapshot.MissingCount, MissingRatio: snapshot.MissingRatio,
		Mean: snapshot.Mean, Stddev: snapshot.Stddev, Minimum: snapshot.Minimum, P25: snapshot.P25, P50: snapshot.P50,
		P75: snapshot.P75, P90: snapshot.P90, P95: snapshot.P95, P99: snapshot.P99, Maximum: snapshot.Maximum,
		BinEdges: append(api.FloatList(nil), snapshot.BinEdges...), BinProportions: append(api.FloatList(nil), snapshot.BinProportions...),
		BlockingReasons: append(api.StringList(nil), snapshot.BlockingReasons...), NoRawSamplesStored: true,
		NoAlertEmitted: run.NoAlertEmitted, NoActionExecuted: run.NoActionExecuted,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

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

const (
	featureDistributionSnapshotVersion = "gpu-feature-distribution-snapshot-v1"
	featureDistributionArchiveVersion  = "gpu-feature-distribution-archive-v1"
	featureDistributionMinimumPairs    = 1
)

type FeatureDistributionSnapshotRequest struct {
	SourceBaselineBuildID uint `json:"source_baseline_build_id,omitempty"`
}

type FeatureDistributionSnapshotQuery struct {
	Limit                  int
	Scope                  string
	SourceBaselineBuildID  uint
	ModelSpecID            uint
	DistributionRole       string
	FeatureContractVersion string
}

type FeatureDistributionArchiveScope struct {
	Name                   string `json:"name"`
	Status                 string `json:"status"`
	Limit                  int    `json:"limit"`
	SourceBaselineBuildID  uint   `json:"source_baseline_build_id,omitempty"`
	ModelSpecID            uint   `json:"model_spec_id,omitempty"`
	ModelKey               string `json:"model_key,omitempty"`
	ModelVersion           string `json:"model_version,omitempty"`
	DistributionRole       string `json:"distribution_role,omitempty"`
	FeatureContractVersion string `json:"feature_contract_version,omitempty"`
	ScopeModelName         string `json:"scope_model_name,omitempty"`
}

type FeatureDistributionArchive struct {
	Version                   string                                      `json:"version"`
	Mode                      string                                      `json:"mode"`
	ArchiveSHA256             string                                      `json:"archive_sha256"`
	Scope                     FeatureDistributionArchiveScope             `json:"scope"`
	ComparabilityStatus       string                                      `json:"comparability_status"`
	MinimumFeaturePairs       int                                         `json:"minimum_feature_pairs"`
	SnapshotCount             int                                         `json:"snapshot_count"`
	TrainingSnapshotCount     int                                         `json:"training_snapshot_count"`
	LiveShadowSnapshotCount   int                                         `json:"live_shadow_snapshot_count"`
	BaselineCount             int                                         `json:"baseline_count"`
	FeatureCount              int                                         `json:"feature_count"`
	PairedFeatureCount        int                                         `json:"paired_feature_count"`
	MissingTrainingFeatures   api.StringList                              `json:"missing_training_features"`
	MissingLiveShadowFeatures api.StringList                              `json:"missing_live_shadow_features"`
	LatestObservedAt          *time.Time                                  `json:"latest_observed_at,omitempty"`
	BlockingReasons           api.StringList                              `json:"blocking_reasons"`
	RawSamplesStored          bool                                        `json:"raw_samples_stored"`
	ScoringAllowed            bool                                        `json:"scoring_allowed"`
	AlertsEmitted             bool                                        `json:"alerts_emitted"`
	ActionsExecuted           bool                                        `json:"actions_executed"`
	Snapshots                 []api.PredictionFeatureDistributionSnapshot `json:"snapshots"`
	GeneratedAt               time.Time                                   `json:"generated_at"`
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
	return s.featureDistributionSnapshotsForQuery(FeatureDistributionSnapshotQuery{Limit: limit})
}

func (s *Service) featureDistributionSnapshotsForQuery(query FeatureDistributionSnapshotQuery) ([]api.PredictionFeatureDistributionSnapshot, error) {
	resolved, _, _, err := s.resolveFeatureDistributionQuery(query)
	if err != nil {
		return nil, err
	}
	return s.featureDistributionSnapshotsForResolvedQuery(resolved)
}

func (s *Service) featureDistributionSnapshotsForResolvedQuery(resolved FeatureDistributionSnapshotQuery) ([]api.PredictionFeatureDistributionSnapshot, error) {
	var rows []api.PredictionFeatureDistributionSnapshot
	if resolved.Scope == "validation" && resolved.SourceBaselineBuildID == 0 {
		return rows, nil
	}
	db := s.db.Model(&api.PredictionFeatureDistributionSnapshot{})
	if resolved.SourceBaselineBuildID > 0 {
		db = db.Where("source_baseline_build_id = ?", resolved.SourceBaselineBuildID)
	}
	if resolved.FeatureContractVersion != "" {
		db = db.Where("feature_contract_version = ?", resolved.FeatureContractVersion)
	}
	if resolved.DistributionRole != "" {
		db = db.Where("distribution_role = ?", resolved.DistributionRole)
	}
	if resolved.ModelSpecID > 0 {
		if resolved.DistributionRole == "live_shadow" {
			db = db.Where("model_spec_id = ?", resolved.ModelSpecID)
		} else if resolved.DistributionRole == "" {
			db = db.Where("(distribution_role = ? OR model_spec_id = ?)", "training", resolved.ModelSpecID)
		}
	}
	err := db.Order("observed_at DESC, id DESC").Limit(resolved.Limit).Find(&rows).Error
	return rows, err
}

func (s *Service) FeatureDistributionArchive(limit int) (FeatureDistributionArchive, error) {
	return s.FeatureDistributionArchiveForQuery(FeatureDistributionSnapshotQuery{Limit: limit})
}

func (s *Service) FeatureDistributionArchiveForQuery(query FeatureDistributionSnapshotQuery) (FeatureDistributionArchive, error) {
	resolved, scope, blocking, err := s.resolveFeatureDistributionQuery(query)
	if err != nil {
		return FeatureDistributionArchive{}, err
	}
	rows, err := s.featureDistributionSnapshotsForResolvedQuery(resolved)
	if err != nil {
		return FeatureDistributionArchive{}, err
	}
	baselines := map[uint]bool{}
	features := map[string]bool{}
	trainingFeatures := map[string]bool{}
	liveShadowFeatures := map[string]bool{}
	var latestObservedAt *time.Time
	archive := FeatureDistributionArchive{
		Version:             featureDistributionArchiveVersion,
		Mode:                "read_only_aggregate_distribution_archive",
		Scope:               scope,
		MinimumFeaturePairs: featureDistributionMinimumPairs,
		BlockingReasons:     append(api.StringList(nil), blocking...),
		RawSamplesStored:    false,
		ScoringAllowed:      false,
		AlertsEmitted:       false,
		ActionsExecuted:     false,
		Snapshots:           rows,
		GeneratedAt:         s.now(),
	}
	for _, row := range rows {
		archive.SnapshotCount++
		switch row.DistributionRole {
		case "training":
			archive.TrainingSnapshotCount++
			if row.FeatureName != "" {
				trainingFeatures[row.FeatureName] = true
			}
		case "live_shadow":
			archive.LiveShadowSnapshotCount++
			if row.FeatureName != "" {
				liveShadowFeatures[row.FeatureName] = true
			}
		}
		if row.SourceBaselineBuildID > 0 {
			baselines[row.SourceBaselineBuildID] = true
		}
		if row.FeatureName != "" {
			features[row.FeatureName] = true
		}
		observedAt := row.ObservedAt
		if latestObservedAt == nil || observedAt.After(*latestObservedAt) {
			latestObservedAt = &observedAt
		}
	}
	archive.BaselineCount = len(baselines)
	archive.FeatureCount = len(features)
	archive.PairedFeatureCount, archive.MissingTrainingFeatures, archive.MissingLiveShadowFeatures = featureDistributionPairSummary(trainingFeatures, liveShadowFeatures)
	archive.ComparabilityStatus = featureDistributionComparabilityStatus(archive)
	archive.BlockingReasons = append(archive.BlockingReasons, featureDistributionComparabilityBlockers(archive)...)
	archive.BlockingReasons = api.StringList(uniqueSortedStrings(archive.BlockingReasons))
	archive.LatestObservedAt = latestObservedAt
	archive.ArchiveSHA256 = featureDistributionArchiveSHA(archive)
	return archive, nil
}

func featureDistributionPairSummary(trainingFeatures, liveShadowFeatures map[string]bool) (int, api.StringList, api.StringList) {
	paired := 0
	missingTraining := []string{}
	missingLive := []string{}
	for feature := range trainingFeatures {
		if liveShadowFeatures[feature] {
			paired++
			continue
		}
		missingLive = append(missingLive, feature)
	}
	for feature := range liveShadowFeatures {
		if !trainingFeatures[feature] {
			missingTraining = append(missingTraining, feature)
		}
	}
	sort.Strings(missingTraining)
	sort.Strings(missingLive)
	return paired, api.StringList(missingTraining), api.StringList(missingLive)
}

func featureDistributionComparabilityStatus(archive FeatureDistributionArchive) string {
	if archive.Scope.Status == "blocked" {
		return "blocked_no_validation_scope"
	}
	if archive.TrainingSnapshotCount == 0 {
		return "blocked_no_training_snapshots"
	}
	if archive.LiveShadowSnapshotCount == 0 {
		return "blocked_no_live_shadow_snapshots"
	}
	if archive.PairedFeatureCount < archive.MinimumFeaturePairs {
		return "blocked_no_paired_features"
	}
	if len(archive.MissingTrainingFeatures) > 0 || len(archive.MissingLiveShadowFeatures) > 0 {
		return "exploratory_partial_feature_pairs"
	}
	return "comparable"
}

func featureDistributionComparabilityBlockers(archive FeatureDistributionArchive) api.StringList {
	switch archive.ComparabilityStatus {
	case "blocked_no_validation_scope":
		return api.StringList{"feature distribution comparability requires a validation scope"}
	case "blocked_no_training_snapshots":
		return api.StringList{"feature distribution comparability requires training snapshots"}
	case "blocked_no_live_shadow_snapshots":
		return api.StringList{"feature distribution comparability requires live shadow snapshots"}
	case "blocked_no_paired_features":
		return api.StringList{"feature distribution comparability requires at least one paired training/live feature"}
	default:
		return nil
	}
}

func (s *Service) resolveFeatureDistributionQuery(query FeatureDistributionSnapshotQuery) (FeatureDistributionSnapshotQuery, FeatureDistributionArchiveScope, api.StringList, error) {
	if query.Limit <= 0 || query.Limit > 500 {
		query.Limit = 100
	}
	query.Scope = strings.TrimSpace(query.Scope)
	if query.Scope == "" {
		query.Scope = "latest"
	}
	query.DistributionRole = strings.TrimSpace(query.DistributionRole)
	query.FeatureContractVersion = strings.TrimSpace(query.FeatureContractVersion)
	scope := FeatureDistributionArchiveScope{
		Name:                   query.Scope,
		Status:                 "scoped",
		Limit:                  query.Limit,
		SourceBaselineBuildID:  query.SourceBaselineBuildID,
		ModelSpecID:            query.ModelSpecID,
		DistributionRole:       query.DistributionRole,
		FeatureContractVersion: query.FeatureContractVersion,
	}
	blocking := api.StringList{}
	if query.Scope != "validation" {
		if query.ModelSpecID > 0 {
			var spec api.PredictionModelSpec
			result := s.db.Model(&api.PredictionModelSpec{}).Where("id = ?", query.ModelSpecID).Limit(1).Find(&spec)
			if result.Error != nil {
				return FeatureDistributionSnapshotQuery{}, FeatureDistributionArchiveScope{}, nil, result.Error
			}
			if result.RowsAffected == 0 {
				scope.Status = "blocked"
				blocking = append(blocking, "model spec scope requires an existing model spec")
				return query, scope, blocking, nil
			}
			if query.SourceBaselineBuildID == 0 {
				query.SourceBaselineBuildID = spec.SourceBaselineBuildID
			}
			if query.FeatureContractVersion == "" {
				query.FeatureContractVersion = spec.FeatureContractVersion
			}
			scope.Status = "model_spec_scope"
			scope.SourceBaselineBuildID = query.SourceBaselineBuildID
			scope.ModelSpecID = query.ModelSpecID
			scope.ModelKey = spec.ModelKey
			scope.ModelVersion = spec.Version
			scope.FeatureContractVersion = query.FeatureContractVersion
			scope.ScopeModelName = spec.ScopeModelName
			if query.SourceBaselineBuildID == 0 {
				scope.Status = "blocked"
				blocking = append(blocking, "model spec is missing source baseline build id")
			}
		}
		if query.SourceBaselineBuildID == 0 && query.ModelSpecID == 0 && query.DistributionRole == "" && query.FeatureContractVersion == "" {
			scope.Status = "unscoped_latest"
		}
		return query, scope, blocking, nil
	}

	var spec api.PredictionModelSpec
	specQuery := s.db.Model(&api.PredictionModelSpec{})
	if query.ModelSpecID > 0 {
		specQuery = specQuery.Where("id = ?", query.ModelSpecID)
	} else {
		specQuery = specQuery.Where("current = ? AND status = ? AND source_baseline_build_id <> ?", true, "shadow_candidate", 0).Order("trained_at DESC, id DESC")
	}
	result := specQuery.Limit(1).Find(&spec)
	if result.Error != nil {
		return FeatureDistributionSnapshotQuery{}, FeatureDistributionArchiveScope{}, nil, result.Error
	}
	if result.RowsAffected == 0 {
		scope.Status = "blocked"
		blocking = append(blocking, "validation scope requires a current shadow candidate model spec")
		return query, scope, blocking, nil
	}
	query.ModelSpecID = spec.ID
	if query.SourceBaselineBuildID == 0 {
		query.SourceBaselineBuildID = spec.SourceBaselineBuildID
	}
	if query.FeatureContractVersion == "" {
		query.FeatureContractVersion = spec.FeatureContractVersion
	}
	scope.Status = "validation_scope"
	scope.SourceBaselineBuildID = query.SourceBaselineBuildID
	scope.ModelSpecID = query.ModelSpecID
	scope.ModelKey = spec.ModelKey
	scope.ModelVersion = spec.Version
	scope.FeatureContractVersion = query.FeatureContractVersion
	scope.ScopeModelName = spec.ScopeModelName
	scope.DistributionRole = query.DistributionRole
	if query.SourceBaselineBuildID == 0 {
		scope.Status = "blocked"
		blocking = append(blocking, "validation model spec is missing source baseline build id")
	}
	return query, scope, blocking, nil
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

func featureDistributionArchiveSHA(archive FeatureDistributionArchive) string {
	type stableSnapshot struct {
		SnapshotKey            string         `json:"snapshot_key"`
		Version                string         `json:"version"`
		Status                 string         `json:"status"`
		DistributionRole       string         `json:"distribution_role"`
		SourceBaselineBuildID  uint           `json:"source_baseline_build_id"`
		ModelSpecID            uint           `json:"model_spec_id"`
		ModelKey               string         `json:"model_key"`
		ModelVersion           string         `json:"model_version"`
		FeatureContractVersion string         `json:"feature_contract_version"`
		ScopeModelName         string         `json:"scope_model_name"`
		SourceKey              string         `json:"source_key"`
		FeatureName            string         `json:"feature_name"`
		SampleCount            int            `json:"sample_count"`
		MissingCount           int            `json:"missing_count"`
		MissingRatio           float64        `json:"missing_ratio"`
		Mean                   float64        `json:"mean"`
		Stddev                 float64        `json:"stddev"`
		Minimum                float64        `json:"minimum"`
		P25                    float64        `json:"p25"`
		P50                    float64        `json:"p50"`
		P75                    float64        `json:"p75"`
		P90                    float64        `json:"p90"`
		P95                    float64        `json:"p95"`
		P99                    float64        `json:"p99"`
		Maximum                float64        `json:"maximum"`
		BinEdges               api.FloatList  `json:"bin_edges"`
		BinProportions         api.FloatList  `json:"bin_proportions"`
		ReportSHA256           string         `json:"report_sha256"`
		BlockingReasons        api.StringList `json:"blocking_reasons"`
		ObservedAt             time.Time      `json:"observed_at"`
	}
	snapshots := make([]stableSnapshot, 0, len(archive.Snapshots))
	for _, row := range archive.Snapshots {
		snapshots = append(snapshots, stableSnapshot{
			SnapshotKey:            row.SnapshotKey,
			Version:                row.Version,
			Status:                 row.Status,
			DistributionRole:       row.DistributionRole,
			SourceBaselineBuildID:  row.SourceBaselineBuildID,
			ModelSpecID:            row.ModelSpecID,
			ModelKey:               row.ModelKey,
			ModelVersion:           row.ModelVersion,
			FeatureContractVersion: row.FeatureContractVersion,
			ScopeModelName:         row.ScopeModelName,
			SourceKey:              row.SourceKey,
			FeatureName:            row.FeatureName,
			SampleCount:            row.SampleCount,
			MissingCount:           row.MissingCount,
			MissingRatio:           row.MissingRatio,
			Mean:                   row.Mean,
			Stddev:                 row.Stddev,
			Minimum:                row.Minimum,
			P25:                    row.P25,
			P50:                    row.P50,
			P75:                    row.P75,
			P90:                    row.P90,
			P95:                    row.P95,
			P99:                    row.P99,
			Maximum:                row.Maximum,
			BinEdges:               append(api.FloatList(nil), row.BinEdges...),
			BinProportions:         append(api.FloatList(nil), row.BinProportions...),
			ReportSHA256:           row.ReportSHA256,
			BlockingReasons:        append(api.StringList(nil), row.BlockingReasons...),
			ObservedAt:             row.ObservedAt,
		})
	}
	sort.Slice(snapshots, func(left, right int) bool {
		if snapshots[left].SnapshotKey != snapshots[right].SnapshotKey {
			return snapshots[left].SnapshotKey < snapshots[right].SnapshotKey
		}
		return snapshots[left].ObservedAt.Before(snapshots[right].ObservedAt)
	})
	payload, _ := json.Marshal(struct {
		Version                   string                          `json:"version"`
		Mode                      string                          `json:"mode"`
		Scope                     FeatureDistributionArchiveScope `json:"scope"`
		ComparabilityStatus       string                          `json:"comparability_status"`
		MinimumFeaturePairs       int                             `json:"minimum_feature_pairs"`
		SnapshotCount             int                             `json:"snapshot_count"`
		TrainingSnapshotCount     int                             `json:"training_snapshot_count"`
		LiveShadowSnapshotCount   int                             `json:"live_shadow_snapshot_count"`
		BaselineCount             int                             `json:"baseline_count"`
		FeatureCount              int                             `json:"feature_count"`
		PairedFeatureCount        int                             `json:"paired_feature_count"`
		MissingTrainingFeatures   api.StringList                  `json:"missing_training_features"`
		MissingLiveShadowFeatures api.StringList                  `json:"missing_live_shadow_features"`
		LatestObservedAt          *time.Time                      `json:"latest_observed_at,omitempty"`
		RawSamplesStored          bool                            `json:"raw_samples_stored"`
		ScoringAllowed            bool                            `json:"scoring_allowed"`
		AlertsEmitted             bool                            `json:"alerts_emitted"`
		ActionsExecuted           bool                            `json:"actions_executed"`
		BlockingReasons           api.StringList                  `json:"blocking_reasons"`
		Snapshots                 []stableSnapshot                `json:"snapshots"`
	}{
		Version:                 archive.Version,
		Mode:                    archive.Mode,
		Scope:                   archive.Scope,
		ComparabilityStatus:     archive.ComparabilityStatus,
		MinimumFeaturePairs:     archive.MinimumFeaturePairs,
		SnapshotCount:           archive.SnapshotCount,
		TrainingSnapshotCount:   archive.TrainingSnapshotCount,
		LiveShadowSnapshotCount: archive.LiveShadowSnapshotCount,
		BaselineCount:           archive.BaselineCount,
		FeatureCount:            archive.FeatureCount,
		PairedFeatureCount:      archive.PairedFeatureCount,
		MissingTrainingFeatures: append(api.StringList(nil), archive.MissingTrainingFeatures...),
		MissingLiveShadowFeatures: append(api.StringList(nil),
			archive.MissingLiveShadowFeatures...),
		LatestObservedAt: archive.LatestObservedAt,
		RawSamplesStored: archive.RawSamplesStored,
		ScoringAllowed:   archive.ScoringAllowed,
		AlertsEmitted:    archive.AlertsEmitted,
		ActionsExecuted:  archive.ActionsExecuted,
		BlockingReasons:  append(api.StringList(nil), archive.BlockingReasons...),
		Snapshots:        snapshots,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

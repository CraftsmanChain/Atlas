package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"atlas/pkg/api"
)

const modelRaceComparisonVersion = "gpu-model-race-comparison-v1"

type modelRaceBuildSummary struct {
	BuildID        uint            `json:"build_id"`
	ModelKey       string          `json:"model_key"`
	Version        string          `json:"version"`
	Algorithm      string          `json:"algorithm"`
	ArtifactSHA256 string          `json:"artifact_sha256"`
	MacroTest      baselineMetrics `json:"macro_test"`
	StableCount    int             `json:"stable_count"`
	CandidateCount int             `json:"candidate_count"`
}

type modelRaceDelta struct {
	BuildID     uint    `json:"build_id"`
	ReferenceID uint    `json:"reference_build_id"`
	ROCAUC      float64 `json:"roc_auc"`
	PRAUC       float64 `json:"pr_auc"`
	Precision   float64 `json:"precision"`
	Recall      float64 `json:"recall"`
}

type modelRaceHorizon struct {
	HorizonMinutes int                        `json:"horizon_minutes"`
	Metrics        map[string]baselineMetrics `json:"metrics_by_build_id"`
	Readiness      map[string]string          `json:"readiness_by_build_id"`
}

type ModelRaceComparison struct {
	Version                 string                  `json:"version"`
	ReferenceBuildID        uint                    `json:"reference_build_id"`
	BuildIDs                []uint                  `json:"build_ids"`
	SourceMatrixBuildID     uint                    `json:"source_matrix_build_id"`
	SourceTrainingMatrixKey string                  `json:"source_training_matrix_key"`
	MatrixSHA256            string                  `json:"matrix_sha256"`
	FeatureContractVersion  string                  `json:"feature_contract_version"`
	ScopeEventType          string                  `json:"scope_event_type,omitempty"`
	ScopeModelName          string                  `json:"scope_model_name,omitempty"`
	Builds                  []modelRaceBuildSummary `json:"builds"`
	Deltas                  []modelRaceDelta        `json:"deltas_vs_reference"`
	Horizons                []modelRaceHorizon      `json:"horizons"`
	ComparisonSHA256        string                  `json:"comparison_sha256"`
	GeneratedAt             time.Time               `json:"generated_at"`
}

// CompareBaselineModels performs a read-only comparison of explicitly named,
// completed builds. It refuses to compare artifacts from different immutable
// matrices, feature contracts, targets, or readiness scopes.
func (s *Service) CompareBaselineModels(referenceBuildID uint, challengerBuildIDs []uint) (ModelRaceComparison, error) {
	if referenceBuildID == 0 || len(challengerBuildIDs) == 0 || len(challengerBuildIDs) > 7 {
		return ModelRaceComparison{}, fmt.Errorf("one reference_build_id and 1-7 challenger_build_ids are required")
	}
	ids := append([]uint{referenceBuildID}, challengerBuildIDs...)
	seen := map[uint]bool{}
	for _, id := range ids {
		if id == 0 || seen[id] {
			return ModelRaceComparison{}, fmt.Errorf("build ids must be non-zero and unique")
		}
		seen[id] = true
	}
	builds := make([]api.BaselineModelBuild, len(ids))
	reports := make([]baselineReport, len(ids))
	for index, id := range ids {
		if err := s.db.First(&builds[index], id).Error; err != nil {
			return ModelRaceComparison{}, fmt.Errorf("baseline build %d: %w", id, err)
		}
		build := builds[index]
		if build.Status != "completed" || build.ArtifactPath == "" || build.ArtifactSHA256 == "" {
			return ModelRaceComparison{}, fmt.Errorf("baseline build %d is not an immutable completed artifact", id)
		}
		if err := verifyFileSHA256(build.ArtifactPath, build.ArtifactSHA256); err != nil {
			return ModelRaceComparison{}, fmt.Errorf("baseline build %d artifact checksum: %w", id, err)
		}
		report, err := s.BaselineModelReport(id)
		if err != nil {
			return ModelRaceComparison{}, fmt.Errorf("baseline build %d report: %w", id, err)
		}
		if report.Version != build.Version || report.Algorithm != build.Algorithm || report.MatrixKey != build.SourceTrainingMatrixKey {
			return ModelRaceComparison{}, fmt.Errorf("baseline build %d report provenance mismatch", id)
		}
		reports[index] = report
	}
	base := builds[0]
	baseReport := reports[0]
	for index := 1; index < len(builds); index++ {
		candidate, report := builds[index], reports[index]
		if candidate.SourceMatrixBuildID != base.SourceMatrixBuildID || candidate.SourceTrainingMatrixKey != base.SourceTrainingMatrixKey || candidate.FeatureContractVersion != base.FeatureContractVersion {
			return ModelRaceComparison{}, fmt.Errorf("build %d does not share reference build %d immutable matrix and feature contract", candidate.ID, base.ID)
		}
		if candidate.ScopeEventType != base.ScopeEventType || candidate.ScopeModelName != base.ScopeModelName || report.PredictionTarget != baseReport.PredictionTarget {
			return ModelRaceComparison{}, fmt.Errorf("build %d does not share reference build %d target and readiness scope", candidate.ID, base.ID)
		}
		if !sameHorizonSet(baseReport.Horizons, report.Horizons) {
			return ModelRaceComparison{}, fmt.Errorf("build %d does not share reference build %d horizons", candidate.ID, base.ID)
		}
	}
	var matrix api.TrainingMatrixBuild
	if err := s.db.First(&matrix, base.SourceMatrixBuildID).Error; err != nil {
		return ModelRaceComparison{}, fmt.Errorf("source matrix: %w", err)
	}
	if matrix.Status != "completed" || matrix.TrainingMatrixKey != base.SourceTrainingMatrixKey || matrix.FeatureContractVersion != base.FeatureContractVersion {
		return ModelRaceComparison{}, fmt.Errorf("source matrix provenance mismatch")
	}
	if err := verifyFileSHA256(matrix.MatrixPath, matrix.MatrixSHA256); err != nil {
		return ModelRaceComparison{}, fmt.Errorf("source matrix checksum: %w", err)
	}
	comparison := ModelRaceComparison{
		Version: modelRaceComparisonVersion, ReferenceBuildID: referenceBuildID, BuildIDs: ids,
		SourceMatrixBuildID: matrix.ID, SourceTrainingMatrixKey: matrix.TrainingMatrixKey,
		MatrixSHA256: matrix.MatrixSHA256, FeatureContractVersion: matrix.FeatureContractVersion,
		ScopeEventType: base.ScopeEventType, ScopeModelName: base.ScopeModelName,
		Builds: []modelRaceBuildSummary{}, Deltas: []modelRaceDelta{}, Horizons: []modelRaceHorizon{},
	}
	for index, build := range builds {
		report := reports[index]
		comparison.Builds = append(comparison.Builds, modelRaceBuildSummary{BuildID: build.ID, ModelKey: build.BaselineModelKey, Version: build.Version, Algorithm: build.Algorithm, ArtifactSHA256: build.ArtifactSHA256, MacroTest: report.MacroTest, StableCount: build.StatisticallyStableCount, CandidateCount: build.ShadowCandidateCount})
		if index > 0 {
			comparison.Deltas = append(comparison.Deltas, modelRaceDelta{BuildID: build.ID, ReferenceID: referenceBuildID, ROCAUC: report.MacroTest.ROCAUC - baseReport.MacroTest.ROCAUC, PRAUC: report.MacroTest.PRAUC - baseReport.MacroTest.PRAUC, Precision: report.MacroTest.Precision - baseReport.MacroTest.Precision, Recall: report.MacroTest.Recall - baseReport.MacroTest.Recall})
		}
		if build.FinishedAt != nil && build.FinishedAt.After(comparison.GeneratedAt) {
			comparison.GeneratedAt = *build.FinishedAt
		}
	}
	for horizonIndex, baseHorizon := range baseReport.Horizons {
		horizon := modelRaceHorizon{HorizonMinutes: baseHorizon.HorizonMinutes, Metrics: map[string]baselineMetrics{}, Readiness: map[string]string{}}
		for buildIndex, build := range builds {
			key := fmt.Sprintf("%d", build.ID)
			horizon.Metrics[key] = reports[buildIndex].Horizons[horizonIndex].Test
			horizon.Readiness[key] = reports[buildIndex].Horizons[horizonIndex].ReleaseReadiness
		}
		comparison.Horizons = append(comparison.Horizons, horizon)
	}
	material := comparison
	material.ComparisonSHA256 = ""
	encoded, err := json.Marshal(material)
	if err != nil {
		return ModelRaceComparison{}, err
	}
	digest := sha256.Sum256(encoded)
	comparison.ComparisonSHA256 = hex.EncodeToString(digest[:])
	return comparison, nil
}

func sameHorizonSet(left, right []baselineHorizonReport) bool {
	if len(left) != len(right) {
		return false
	}
	leftValues, rightValues := make([]int, len(left)), make([]int, len(right))
	for index := range left {
		leftValues[index] = left[index].HorizonMinutes
	}
	for index := range right {
		rightValues[index] = right[index].HorizonMinutes
	}
	sort.Ints(leftValues)
	sort.Ints(rightValues)
	for index := range leftValues {
		if leftValues[index] != rightValues[index] {
			return false
		}
	}
	return true
}

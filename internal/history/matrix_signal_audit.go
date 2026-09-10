package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"atlas/internal/features"
	"atlas/internal/featurestats"
	"atlas/pkg/api"
)

const trainingMatrixSignalAuditVersion = "gpu-training-signal-audit-v2"

type matrixSignalCoverage struct {
	Rows    int     `json:"rows"`
	Average float64 `json:"average"`
	Minimum float64 `json:"minimum"`
}

type matrixSignalFinding struct {
	Severity       string `json:"severity"`
	Code           string `json:"code"`
	Evidence       string `json:"evidence"`
	Recommendation string `json:"recommendation"`
}

type trainingMatrixSignalAudit struct {
	Version                         string                          `json:"version"`
	AuditSHA256                     string                          `json:"audit_sha256"`
	MatrixBuildID                   uint                            `json:"matrix_build_id"`
	MatrixKey                       string                          `json:"matrix_key"`
	MatrixSHA256                    string                          `json:"matrix_sha256"`
	PredictionTarget                string                          `json:"prediction_target"`
	RowCount                        int                             `json:"row_count"`
	ExpandedFeatureColumnCount      int                             `json:"expanded_feature_column_count"`
	TargetSafeFeatureColumnCount    int                             `json:"target_safe_feature_column_count"`
	ExcludedFeatureColumnCount      int                             `json:"excluded_feature_column_count"`
	ProhibitedSelectedColumnCount   int                             `json:"prohibited_selected_column_count"`
	ObservedSourceMetricCount       int                             `json:"observed_source_metric_count"`
	ObservedSourceMetrics           api.StringList                  `json:"observed_source_metrics"`
	SafeSourceMetricCount           int                             `json:"safe_source_metric_count"`
	SafeSourceMetrics               api.StringList                  `json:"safe_source_metrics"`
	ConfiguredHistoricalMetricCount int                             `json:"configured_historical_metric_count"`
	MissingConfiguredMetrics        api.StringList                  `json:"missing_configured_metrics"`
	ConfiguredCoreMetricCount       int                             `json:"configured_core_metric_count"`
	MissingCoreMetrics              api.StringList                  `json:"missing_core_metrics"`
	ConfiguredOptionalMetricCount   int                             `json:"configured_optional_metric_count"`
	MissingOptionalMetrics          api.StringList                  `json:"missing_optional_metrics"`
	ResearchMetricFamilyCount       int                             `json:"research_metric_family_count"`
	StructuralFeatureCount          int                             `json:"structural_feature_count"`
	MissingStructuralFeatures       api.StringList                  `json:"missing_structural_features"`
	LookbackMinutes                 []int                           `json:"lookback_minutes"`
	MaximumLookbackMinutes          int                             `json:"maximum_lookback_minutes"`
	HorizonMinutes                  []int                           `json:"horizon_minutes"`
	CoverageBySplit                 map[string]matrixSignalCoverage `json:"coverage_by_split"`
	Findings                        []matrixSignalFinding           `json:"findings"`
}

func (s *Service) TrainingMatrixSignalAudit(id uint) (trainingMatrixSignalAudit, error) {
	var build api.TrainingMatrixBuild
	if err := s.db.First(&build, id).Error; err != nil {
		return trainingMatrixSignalAudit{}, err
	}
	if (build.Status != "completed" && build.Status != manualTrainingMatrixStatus) || build.MatrixPath == "" || build.MatrixSHA256 == "" {
		return trainingMatrixSignalAudit{}, fmt.Errorf("completed training matrix artifact is required")
	}
	base, err := filepath.Abs(s.config.DatasetDir)
	if err != nil {
		return trainingMatrixSignalAudit{}, err
	}
	path, err := filepath.Abs(build.MatrixPath)
	if err != nil {
		return trainingMatrixSignalAudit{}, err
	}
	relative, err := filepath.Rel(base, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return trainingMatrixSignalAudit{}, fmt.Errorf("training matrix artifact is outside the configured dataset directory")
	}
	if err := verifyFileSHA256(path, build.MatrixSHA256); err != nil {
		return trainingMatrixSignalAudit{}, fmt.Errorf("training matrix checksum: %w", err)
	}
	rows, err := readJSONLines[trainingMatrixRow](path)
	if err != nil {
		return trainingMatrixSignalAudit{}, err
	}
	return buildTrainingMatrixSignalAudit(build, rows)
}

func buildTrainingMatrixSignalAudit(build api.TrainingMatrixBuild, rows []trainingMatrixRow) (trainingMatrixSignalAudit, error) {
	target, err := matrixPredictionTarget(rows)
	if err != nil {
		return trainingMatrixSignalAudit{}, err
	}
	featureAudit := auditBaselineFeaturesForTarget(rows, target)
	allSources := map[string]bool{}
	safeSources := map[string]bool{}
	lookbacks := map[int]bool{}
	horizons := map[int]bool{}
	coverage := map[string]*struct {
		rows int
		sum  float64
		min  float64
	}{}
	for _, row := range rows {
		horizons[row.HorizonMinutes] = true
		item := coverage[row.Split]
		if item == nil {
			item = &struct {
				rows int
				sum  float64
				min  float64
			}{min: math.Inf(1)}
			coverage[row.Split] = item
		}
		item.rows++
		item.sum += row.MetricCoverage
		item.min = math.Min(item.min, row.MetricCoverage)
		for column := range row.Features {
			source, _, duration, ok := featurestats.ParseTrailingRangeColumn(column)
			if !ok {
				source = column
			} else {
				lookbacks[int(duration.Minutes())] = true
			}
			allSources[source] = true
		}
	}
	for _, column := range featureAudit.SelectedColumns {
		source, _, _, ok := featurestats.ParseTrailingRangeColumn(column)
		if !ok {
			source = column
		}
		safeSources[source] = true
	}

	configured := stringSet(canonicalHistoricalMetrics())
	core := stringSet(requiredHistoricalMetrics("NVIDIA H100"))
	missingConfigured := make([]string, 0)
	missingCore := make([]string, 0)
	missingOptional := make([]string, 0)
	for metric := range configured {
		if !allSources[metric] {
			missingConfigured = append(missingConfigured, metric)
			if core[metric] {
				missingCore = append(missingCore, metric)
			} else {
				missingOptional = append(missingOptional, metric)
			}
		}
	}
	structural := predictionStructuralFeatureNames()
	missingStructural := make([]string, 0)
	structuralCount := 0
	for _, name := range structural {
		if allSources[name] {
			structuralCount++
		} else {
			missingStructural = append(missingStructural, name)
		}
	}

	result := trainingMatrixSignalAudit{
		Version: trainingMatrixSignalAuditVersion, MatrixBuildID: build.ID, MatrixKey: build.TrainingMatrixKey,
		MatrixSHA256: build.MatrixSHA256, PredictionTarget: target, RowCount: len(rows),
		ExpandedFeatureColumnCount: len(unionFeatureColumns(rows)), ObservedSourceMetrics: sortedSet(allSources),
		TargetSafeFeatureColumnCount: featureAudit.SelectedFeatureCount, ExcludedFeatureColumnCount: featureAudit.ExcludedFeatureCount,
		ProhibitedSelectedColumnCount: featureAudit.ProhibitedSelectedCount,
		SafeSourceMetrics:             sortedSet(safeSources), ConfiguredHistoricalMetricCount: len(configured),
		MissingConfiguredMetrics: api.StringList(missingConfigured), ResearchMetricFamilyCount: len(ResearchMetricFamilies()),
		ConfiguredCoreMetricCount: len(core), MissingCoreMetrics: api.StringList(missingCore),
		ConfiguredOptionalMetricCount: len(configured) - len(core), MissingOptionalMetrics: api.StringList(missingOptional),
		StructuralFeatureCount: structuralCount, MissingStructuralFeatures: api.StringList(missingStructural),
		LookbackMinutes: sortedIntSet(lookbacks), HorizonMinutes: sortedIntSet(horizons), CoverageBySplit: map[string]matrixSignalCoverage{},
		Findings: []matrixSignalFinding{},
	}
	result.ObservedSourceMetricCount = len(result.ObservedSourceMetrics)
	result.SafeSourceMetricCount = len(result.SafeSourceMetrics)
	if len(result.LookbackMinutes) > 0 {
		result.MaximumLookbackMinutes = result.LookbackMinutes[len(result.LookbackMinutes)-1]
	}
	sort.Strings(result.MissingConfiguredMetrics)
	sort.Strings(result.MissingCoreMetrics)
	sort.Strings(result.MissingOptionalMetrics)
	sort.Strings(result.MissingStructuralFeatures)
	for split, item := range coverage {
		minimum := item.min
		if math.IsInf(minimum, 1) {
			minimum = 0
		}
		result.CoverageBySplit[split] = matrixSignalCoverage{Rows: item.rows, Average: item.sum / float64(item.rows), Minimum: minimum}
	}

	if result.ExpandedFeatureColumnCount > result.ObservedSourceMetricCount*2 {
		result.Findings = append(result.Findings, matrixSignalFinding{
			Severity: "warning", Code: "derived_column_count_overstates_signal_breadth",
			Evidence:       fmt.Sprintf("%d derived columns come from only %d observed source metrics (%d safe for this target)", result.ExpandedFeatureColumnCount, result.ObservedSourceMetricCount, result.SafeSourceMetricCount),
			Recommendation: "measure feature breadth by independent source families and availability, not by derived column count",
		})
	}
	if len(result.MissingCoreMetrics) > 0 {
		result.Findings = append(result.Findings, matrixSignalFinding{
			Severity: "blocking", Code: "core_metrics_absent_from_matrix",
			Evidence:       fmt.Sprintf("%d of %d core historical metrics produced no matrix columns", len(result.MissingCoreMetrics), result.ConfiguredCoreMetricCount),
			Recommendation: "repair core telemetry availability or aliases before admitting affected windows",
		})
	}
	if len(result.MissingOptionalMetrics) > 0 {
		result.Findings = append(result.Findings, matrixSignalFinding{
			Severity: "info", Code: "optional_metrics_absent_from_matrix",
			Evidence:       fmt.Sprintf("%d of %d optional historical metrics produced no matrix columns", len(result.MissingOptionalMetrics), result.ConfiguredOptionalMetricCount),
			Recommendation: "retain missing values as unknown and use observed window coverage to decide feature-family admission",
		})
	}
	if structuralCount < len(structural) {
		result.Findings = append(result.Findings, matrixSignalFinding{
			Severity: "blocking", Code: "structural_observability_plane_missing",
			Evidence:       fmt.Sprintf("matrix contains %d of %d prediction-grade presence, gap, scrape and metric-family signals", structuralCount, len(structural)),
			Recommendation: "materialize point-in-time structural telemetry and keep collector-failure evidence distinguishable from hardware degradation",
		})
	}
	for _, horizon := range result.HorizonMinutes {
		if horizon > result.MaximumLookbackMinutes {
			result.Findings = append(result.Findings, matrixSignalFinding{
				Severity: "warning", Code: "lookback_shorter_than_prediction_horizon",
				Evidence:       fmt.Sprintf("maximum feature lookback is %d minutes while the matrix includes a %d-minute horizon", result.MaximumLookbackMinutes, horizon),
				Recommendation: "add point-in-time-safe multi-day trend and persistence features for long-horizon prediction",
			})
			break
		}
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return trainingMatrixSignalAudit{}, err
	}
	digest := sha256.Sum256(payload)
	result.AuditSHA256 = hex.EncodeToString(digest[:])
	return result, nil
}

func unionFeatureColumns(rows []trainingMatrixRow) map[string]bool {
	result := map[string]bool{}
	for _, row := range rows {
		for column := range row.Features {
			result[column] = true
		}
	}
	return result
}

func predictionStructuralFeatureNames() []string {
	result := make([]string, 0)
	for _, definition := range features.Builtins() {
		if definition.Domain != "availability" || !containsString([]string(definition.Purposes), "prediction") {
			continue
		}
		result = append(result, definition.Name)
	}
	sort.Strings(result)
	return result
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func sortedSet(values map[string]bool) api.StringList {
	result := make(api.StringList, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortedIntSet(values map[int]bool) []int {
	result := make([]int, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}

package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
)

const ValidationReadinessReportVersion = "prediction-validation-readiness-v2"
const validationFeatureDistributionArchiveVersion = "gpu-feature-distribution-archive-v1"
const validationFeatureDistributionMinimumPairs = 1

type ValidationFeatureDistributionArchiveScope struct {
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

type validationFeatureDistributionArchive struct {
	Version                   string                                      `json:"version"`
	Mode                      string                                      `json:"mode"`
	ArchiveSHA256             string                                      `json:"archive_sha256"`
	Scope                     ValidationFeatureDistributionArchiveScope   `json:"scope"`
	ComparabilityStatus       string                                      `json:"comparability_status"`
	MinimumFeaturePairs       int                                         `json:"minimum_feature_pairs"`
	SnapshotCount             int                                         `json:"snapshot_count"`
	TrainingSnapshotCount     int                                         `json:"training_snapshot_count"`
	LiveShadowSnapshotCount   int                                         `json:"live_shadow_snapshot_count"`
	BaselineCount             int                                         `json:"baseline_count"`
	FeatureCount              int                                         `json:"feature_count"`
	PairedFeatureCount        int                                         `json:"paired_feature_count"`
	MissingTrainingFeatures   []string                                    `json:"missing_training_features"`
	MissingLiveShadowFeatures []string                                    `json:"missing_live_shadow_features"`
	LatestObservedAt          *time.Time                                  `json:"latest_observed_at,omitempty"`
	BlockingReasons           []string                                    `json:"blocking_reasons"`
	RawSamplesStored          bool                                        `json:"raw_samples_stored"`
	ScoringAllowed            bool                                        `json:"scoring_allowed"`
	AlertsEmitted             bool                                        `json:"alerts_emitted"`
	ActionsExecuted           bool                                        `json:"actions_executed"`
	Snapshots                 []api.PredictionFeatureDistributionSnapshot `json:"snapshots"`
}

type ValidationReadinessReport struct {
	Version                            string                                    `json:"version"`
	FrameworkVersion                   string                                    `json:"framework_version"`
	Mode                               string                                    `json:"mode"`
	Status                             string                                    `json:"status"`
	ReadinessSHA256                    string                                    `json:"readiness_sha256"`
	LabelGateStatus                    string                                    `json:"label_gate_status"`
	LabelManifestVersion               string                                    `json:"label_manifest_version"`
	LabelManifestSHA256                string                                    `json:"label_manifest_sha256"`
	EvidenceBundleVersion              string                                    `json:"evidence_bundle_version"`
	EvidenceBundleSHA256               string                                    `json:"evidence_bundle_sha256"`
	EvidencePositive                   int                                       `json:"evidence_positive_labels"`
	EvidenceExcluded                   int                                       `json:"evidence_excluded_labels"`
	OutcomeReportVersion               string                                    `json:"outcome_report_version"`
	OutcomeStability                   string                                    `json:"outcome_stability_status"`
	OutcomeMaturity                    OutcomeMaturity                           `json:"outcome_maturity"`
	ChallengerVersion                  string                                    `json:"challenger_version"`
	ChallengerStatus                   string                                    `json:"challenger_status"`
	ChallengerConfidence               string                                    `json:"challenger_confidence_status"`
	ChallengerHistoricalSignal         string                                    `json:"challenger_historical_signal_status"`
	DataDriftVersion                   string                                    `json:"data_drift_version"`
	DataDriftSHA256                    string                                    `json:"data_drift_sha256"`
	DataDriftStatus                    string                                    `json:"data_drift_status"`
	DataDriftCoverage                  string                                    `json:"data_drift_coverage_quality_status"`
	DataDriftPSIProxy                  float64                                   `json:"data_drift_psi_proxy"`
	DataDriftKSProxy                   float64                                   `json:"data_drift_ks_proxy"`
	CalibrationDriftVersion            string                                    `json:"calibration_drift_version"`
	CalibrationDriftSHA256             string                                    `json:"calibration_drift_sha256"`
	CalibrationDriftStatus             string                                    `json:"calibration_drift_status"`
	CalibrationECEDelta                float64                                   `json:"calibration_ece_delta"`
	CalibrationBSSDelta                float64                                   `json:"calibration_brier_skill_score_delta"`
	FeatureDriftVersion                string                                    `json:"feature_drift_version"`
	FeatureDriftSHA256                 string                                    `json:"feature_drift_sha256"`
	FeatureDriftStatus                 string                                    `json:"feature_drift_status"`
	FeatureDriftColumns                int                                       `json:"feature_drift_feature_columns"`
	FeatureDriftDistributions          int                                       `json:"feature_drift_feature_distributions"`
	FeatureDriftCompared               int                                       `json:"feature_drift_compared_features"`
	FeatureDriftPassed                 int                                       `json:"feature_drift_passed_features"`
	FeatureDriftReview                 int                                       `json:"feature_drift_review_required_features"`
	FeatureDriftMaxPSI                 float64                                   `json:"feature_drift_maximum_psi"`
	FeatureDriftMaxKS                  float64                                   `json:"feature_drift_maximum_ks"`
	FeatureDriftPSIStatus              string                                    `json:"feature_drift_psi_status"`
	FeatureDriftKSStatus               string                                    `json:"feature_drift_ks_status"`
	FeatureDriftBlockers               []string                                  `json:"feature_drift_blocking_reasons"`
	FeatureDriftNextRun                []string                                  `json:"feature_drift_recommended_next_run"`
	FeatureDistributionArchiveVersion  string                                    `json:"feature_distribution_archive_version"`
	FeatureDistributionArchiveSHA256   string                                    `json:"feature_distribution_archive_sha256"`
	FeatureDistributionArchiveScope    ValidationFeatureDistributionArchiveScope `json:"feature_distribution_archive_scope"`
	FeatureDistributionComparability   string                                    `json:"feature_distribution_comparability_status"`
	FeatureDistributionMinimumPairs    int                                       `json:"feature_distribution_minimum_feature_pairs"`
	FeatureDistributionSnapshots       int                                       `json:"feature_distribution_snapshots"`
	FeatureDistributionTraining        int                                       `json:"feature_distribution_training_snapshots"`
	FeatureDistributionLiveShadow      int                                       `json:"feature_distribution_live_shadow_snapshots"`
	FeatureDistributionFeatures        int                                       `json:"feature_distribution_feature_count"`
	FeatureDistributionPairedFeatures  int                                       `json:"feature_distribution_paired_feature_count"`
	FeatureDistributionMissingTraining []string                                  `json:"feature_distribution_missing_training_features"`
	FeatureDistributionMissingLive     []string                                  `json:"feature_distribution_missing_live_shadow_features"`
	FeatureDistributionBlockers        []string                                  `json:"feature_distribution_blocking_reasons"`
	SevenDayRows                       int                                       `json:"seven_day_rows"`
	SevenDayNodes                      int                                       `json:"seven_day_nodes"`
	SevenDayPositives                  int                                       `json:"seven_day_positives"`
	SevenDay                           []ChallengerMetricSet                     `json:"seven_day"`
	BlockingReasons                    []string                                  `json:"blocking_reasons"`
	RecommendedNextRun                 []string                                  `json:"recommended_next_run"`
	GeneratedAt                        time.Time                                 `json:"generated_at"`
}

func (s *Service) ValidationReadinessReport() (ValidationReadinessReport, error) {
	labelManifest, err := s.LabelManifest()
	if err != nil {
		return ValidationReadinessReport{}, err
	}
	evidenceBundle, err := s.EvidenceBundleReport()
	if err != nil {
		return ValidationReadinessReport{}, err
	}
	outcomeReport, err := s.OutcomeReport()
	if err != nil {
		return ValidationReadinessReport{}, err
	}
	challengerReport, err := s.HeaRankChallengerReport()
	if err != nil {
		return ValidationReadinessReport{}, err
	}
	driftReport, err := s.DataDriftReport()
	if err != nil {
		return ValidationReadinessReport{}, err
	}
	calibrationReport, err := s.CalibrationDriftReport()
	if err != nil {
		return ValidationReadinessReport{}, err
	}
	featureDriftReport, err := s.FeatureDriftReport()
	if err != nil {
		return ValidationReadinessReport{}, err
	}
	distributionArchive, err := s.validationFeatureDistributionArchive()
	if err != nil {
		return ValidationReadinessReport{}, err
	}
	report := ValidationReadinessReport{
		Version:                           ValidationReadinessReportVersion,
		FrameworkVersion:                  FrameworkVersion,
		Mode:                              "read_only_validation_gate",
		LabelGateStatus:                   labelManifest.QualityGateStatus,
		LabelManifestVersion:              labelManifest.Version,
		LabelManifestSHA256:               labelManifest.ManifestSHA256,
		EvidenceBundleVersion:             evidenceBundle.Version,
		EvidenceBundleSHA256:              evidenceBundle.BundleSHA256,
		EvidencePositive:                  evidenceBundle.PositiveLabels,
		EvidenceExcluded:                  evidenceBundle.ExcludedLabels,
		OutcomeReportVersion:              outcomeReport.Version,
		OutcomeStability:                  outcomeReport.Stability.Status,
		OutcomeMaturity:                   outcomeReport.SampleMaturity,
		ChallengerVersion:                 challengerReport.Version,
		ChallengerStatus:                  challengerReport.Status,
		ChallengerConfidence:              challengerReport.ConfidenceStatus,
		ChallengerHistoricalSignal:        challengerHistoricalSignalStatus(challengerReport.SevenDay),
		DataDriftVersion:                  driftReport.Version,
		DataDriftSHA256:                   driftReport.ReportSHA256,
		DataDriftStatus:                   driftReport.Status,
		DataDriftCoverage:                 driftReport.CoverageQualityStatus,
		DataDriftPSIProxy:                 driftReport.PSIProxy,
		DataDriftKSProxy:                  driftReport.KSProxy,
		CalibrationDriftVersion:           calibrationReport.Version,
		CalibrationDriftSHA256:            calibrationReport.ReportSHA256,
		CalibrationDriftStatus:            calibrationReport.Status,
		CalibrationECEDelta:               calibrationReport.ECEDelta,
		CalibrationBSSDelta:               calibrationReport.BrierSkillScoreDelta,
		FeatureDriftVersion:               featureDriftReport.Version,
		FeatureDriftSHA256:                featureDriftReport.ReportSHA256,
		FeatureDriftStatus:                featureDriftReport.Status,
		FeatureDriftColumns:               featureDriftReport.FeatureColumnCount,
		FeatureDriftDistributions:         featureDriftReport.FeatureDistributionCount,
		FeatureDriftCompared:              featureDriftReport.ComparedFeatureCount,
		FeatureDriftPassed:                featureDriftReport.PassedFeatureCount,
		FeatureDriftReview:                featureDriftReport.ReviewRequiredFeatureCount,
		FeatureDriftMaxPSI:                featureDriftReport.MaximumPSI,
		FeatureDriftMaxKS:                 featureDriftReport.MaximumKS,
		FeatureDriftPSIStatus:             featureDriftReport.PSIStatus,
		FeatureDriftKSStatus:              featureDriftReport.KSStatus,
		FeatureDriftBlockers:              append([]string(nil), featureDriftReport.BlockingReasons...),
		FeatureDriftNextRun:               append([]string(nil), featureDriftReport.RecommendedNextRun...),
		FeatureDistributionArchiveVersion: distributionArchive.Version,
		FeatureDistributionArchiveSHA256:  distributionArchive.ArchiveSHA256,
		FeatureDistributionArchiveScope:   distributionArchive.Scope,
		FeatureDistributionComparability:  distributionArchive.ComparabilityStatus,
		FeatureDistributionMinimumPairs:   distributionArchive.MinimumFeaturePairs,
		FeatureDistributionSnapshots:      distributionArchive.SnapshotCount,
		FeatureDistributionTraining:       distributionArchive.TrainingSnapshotCount,
		FeatureDistributionLiveShadow:     distributionArchive.LiveShadowSnapshotCount,
		FeatureDistributionFeatures:       distributionArchive.FeatureCount,
		FeatureDistributionPairedFeatures: distributionArchive.PairedFeatureCount,
		FeatureDistributionMissingTraining: append([]string(nil),
			distributionArchive.MissingTrainingFeatures...),
		FeatureDistributionMissingLive: append([]string(nil),
			distributionArchive.MissingLiveShadowFeatures...),
		FeatureDistributionBlockers: append([]string(nil), distributionArchive.BlockingReasons...),
		SevenDay:                    challengerReport.SevenDay,
		GeneratedAt:                 s.now(),
		RecommendedNextRun: []string{
			"freeze the label manifest SHA256 before comparing challenger metrics",
			"archive the evidence bundle SHA256 with every offline validation run",
			"archive the data drift report SHA256 before comparing shadow candidates",
			"archive the calibration drift report SHA256 before comparing shadow candidates",
			"archive the feature drift readiness report SHA256 before interpreting per-feature drift gates",
			"archive the scoped feature distribution archive SHA256 before comparing feature drift metrics",
			"use the same matured outcome denominator for Logistic, rules, and HeaRank-style policies",
			"keep all validation read-only until label, evidence, outcome, drift, calibration, feature drift, and challenger gates pass together",
		},
	}
	report.SevenDayRows, report.SevenDayNodes, report.SevenDayPositives = validationReadinessSevenDaySummary(report.SevenDay)
	if report.ChallengerHistoricalSignal != "covered" {
		report.RecommendedNextRun = append(report.RecommendedNextRun, "do not interpret historical-risk challenger policies as comparable until their 7d signal coverage is covered")
	}
	report.BlockingReasons = validationReadinessBlockers(labelManifest, evidenceBundle, outcomeReport, challengerReport, driftReport, calibrationReport, featureDriftReport, distributionArchive)
	if len(report.BlockingReasons) > 0 {
		report.Status = "blocked"
		report.RecommendedNextRun = append(report.RecommendedNextRun, report.BlockingReasons...)
	} else if labelManifest.QualityGateStatus == "exploratory_ready" || outcomeReport.Stability.Status == "exploratory" || driftReport.Status == "exploratory_insufficient_shadow_runs" || calibrationReport.Status == "exploratory_insufficient_calibration_reports" || outcomeReport.SampleMaturity.Matured < 30 {
		report.Status = "exploratory_ready"
		report.RecommendedNextRun = append(report.RecommendedNextRun, "treat metrics as exploratory until more mature outcomes accumulate")
	} else {
		report.Status = "ready_for_offline_validation"
	}
	report.RecommendedNextRun = uniqueSorted(report.RecommendedNextRun)
	report.ReadinessSHA256 = validationReadinessChecksum(report)
	return report, nil
}

func validationReadinessSevenDaySummary(rows []ChallengerMetricSet) (int, int, int) {
	for _, row := range rows {
		if row.Policy == "logistic_probability" {
			return row.Rows, row.Nodes, row.Positives
		}
	}
	if len(rows) == 0 {
		return 0, 0, 0
	}
	return rows[0].Rows, rows[0].Nodes, rows[0].Positives
}

func challengerHistoricalSignalStatus(rows []ChallengerMetricSet) string {
	found := false
	status := "covered"
	for _, row := range rows {
		switch row.Policy {
		case "failure_count_prior", "recency_weighted_failure_prior", "severity_weighted_label_history":
			found = true
			switch row.SignalCoverageStatus {
			case "no_signal", "":
				return "no_signal"
			case "exploratory":
				status = "exploratory"
			}
		}
	}
	if !found {
		return "no_signal"
	}
	return status
}

func (s *Service) validationFeatureDistributionArchive() (validationFeatureDistributionArchive, error) {
	scope := ValidationFeatureDistributionArchiveScope{Name: "validation", Status: "validation_scope", Limit: 100}
	blockers := []string{}
	var spec api.PredictionModelSpec
	result := s.db.Model(&api.PredictionModelSpec{}).
		Where("current = ? AND status = ? AND source_baseline_build_id <> ?", true, "shadow_candidate", 0).
		Order("trained_at DESC, id DESC").
		Limit(1).
		Find(&spec)
	if result.Error != nil {
		return validationFeatureDistributionArchive{}, result.Error
	}
	if result.RowsAffected == 0 {
		scope.Status = "blocked"
		blockers = append(blockers, "validation scope requires a current shadow candidate model spec")
	}
	if spec.ID > 0 {
		scope.SourceBaselineBuildID = spec.SourceBaselineBuildID
		scope.ModelSpecID = spec.ID
		scope.ModelKey = spec.ModelKey
		scope.ModelVersion = spec.Version
		scope.FeatureContractVersion = spec.FeatureContractVersion
		scope.ScopeModelName = spec.ScopeModelName
	}

	rows := []api.PredictionFeatureDistributionSnapshot{}
	if spec.ID > 0 && spec.SourceBaselineBuildID > 0 {
		query := s.db.Model(&api.PredictionFeatureDistributionSnapshot{}).
			Where("source_baseline_build_id = ?", spec.SourceBaselineBuildID)
		if spec.FeatureContractVersion != "" {
			query = query.Where("feature_contract_version = ?", spec.FeatureContractVersion)
		}
		query = query.Where("(distribution_role = ? OR model_spec_id = ?)", "training", spec.ID)
		if err := query.Order("observed_at DESC, id DESC").Limit(scope.Limit).Find(&rows).Error; err != nil {
			return validationFeatureDistributionArchive{}, err
		}
	}

	baselines := map[uint]bool{}
	features := map[string]bool{}
	trainingFeatures := map[string]bool{}
	liveShadowFeatures := map[string]bool{}
	var latestObservedAt *time.Time
	archive := validationFeatureDistributionArchive{
		Version:             validationFeatureDistributionArchiveVersion,
		Mode:                "read_only_aggregate_distribution_archive",
		Scope:               scope,
		MinimumFeaturePairs: validationFeatureDistributionMinimumPairs,
		BlockingReasons:     append([]string(nil), blockers...),
		RawSamplesStored:    false,
		ScoringAllowed:      false,
		AlertsEmitted:       false,
		ActionsExecuted:     false,
		Snapshots:           rows,
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
	archive.PairedFeatureCount, archive.MissingTrainingFeatures, archive.MissingLiveShadowFeatures = validationFeatureDistributionPairSummary(trainingFeatures, liveShadowFeatures)
	archive.ComparabilityStatus = validationFeatureDistributionComparabilityStatus(archive)
	archive.BlockingReasons = append(archive.BlockingReasons, validationFeatureDistributionComparabilityBlockers(archive)...)
	archive.BlockingReasons = uniqueSorted(archive.BlockingReasons)
	archive.LatestObservedAt = latestObservedAt
	archive.ArchiveSHA256 = validationFeatureDistributionArchiveSHA(archive)
	return archive, nil
}

func validationFeatureDistributionPairSummary(trainingFeatures, liveShadowFeatures map[string]bool) (int, []string, []string) {
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
	return paired, missingTraining, missingLive
}

func validationFeatureDistributionComparabilityStatus(archive validationFeatureDistributionArchive) string {
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

func validationFeatureDistributionComparabilityBlockers(archive validationFeatureDistributionArchive) []string {
	switch archive.ComparabilityStatus {
	case "blocked_no_validation_scope":
		return []string{"feature distribution comparability requires a validation scope"}
	case "blocked_no_training_snapshots":
		return []string{"feature distribution comparability requires training snapshots"}
	case "blocked_no_live_shadow_snapshots":
		return []string{"feature distribution comparability requires live shadow snapshots"}
	case "blocked_no_paired_features":
		return []string{"feature distribution comparability requires at least one paired training/live feature"}
	default:
		return nil
	}
}

func validationReadinessBlockers(labelManifest LabelManifest, evidenceBundle EvidenceBundleReport, outcomeReport OutcomeReport, challengerReport HeaRankChallengerReport, driftReport DataDriftReport, calibrationReport CalibrationDriftReport, featureDriftReport FeatureDriftReport, distributionArchive validationFeatureDistributionArchive) []string {
	blockers := []string{}
	if labelManifest.QualityGateStatus == "blocked" {
		blockers = append(blockers, labelManifest.BlockingReasons...)
	}
	if evidenceBundle.BundleSHA256 == "" {
		blockers = append(blockers, "evidence bundle SHA256 is unavailable")
	}
	if evidenceBundle.TotalLabels != labelManifest.Total {
		blockers = append(blockers, "evidence bundle label count does not match label manifest")
	}
	if outcomeReport.SampleMaturity.Matured == 0 {
		blockers = append(blockers, "no mature shadow outcomes are available")
	}
	if outcomeReport.SampleMaturity.ProbabilityScored == 0 {
		blockers = append(blockers, "no probability-scored outcomes are available")
	}
	if outcomeReport.Stability.Status == "blocked" {
		blockers = append(blockers, outcomeReport.Stability.BlockingReasons...)
	}
	if challengerReport.Status != "ready_for_offline_comparison" {
		blockers = append(blockers, challengerReport.BlockingReasons...)
	}
	if driftReport.Status == "blocked_no_shadow_runs" || driftReport.Status == "review_required" {
		blockers = append(blockers, driftReport.BlockingReasons...)
	}
	if calibrationReport.Status == "blocked_no_calibration_reports" || calibrationReport.Status == "review_required" {
		blockers = append(blockers, calibrationReport.BlockingReasons...)
	}
	if strings.HasPrefix(featureDriftReport.Status, "blocked") || featureDriftReport.Status == "review_required" {
		blockers = append(blockers, featureDriftReport.BlockingReasons...)
	}
	if distributionArchive.ArchiveSHA256 == "" {
		blockers = append(blockers, "feature distribution archive SHA256 is unavailable")
	}
	if len(distributionArchive.BlockingReasons) > 0 {
		blockers = append(blockers, distributionArchive.BlockingReasons...)
	}
	if distributionArchive.Scope.Status == "blocked" {
		blockers = append(blockers, "scoped feature distribution archive is blocked")
	}
	return uniqueSorted(blockers)
}

func validationFeatureDistributionArchiveSHA(archive validationFeatureDistributionArchive) string {
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
		Version                   string                                    `json:"version"`
		Mode                      string                                    `json:"mode"`
		Scope                     ValidationFeatureDistributionArchiveScope `json:"scope"`
		ComparabilityStatus       string                                    `json:"comparability_status"`
		MinimumFeaturePairs       int                                       `json:"minimum_feature_pairs"`
		SnapshotCount             int                                       `json:"snapshot_count"`
		TrainingSnapshotCount     int                                       `json:"training_snapshot_count"`
		LiveShadowSnapshotCount   int                                       `json:"live_shadow_snapshot_count"`
		BaselineCount             int                                       `json:"baseline_count"`
		FeatureCount              int                                       `json:"feature_count"`
		PairedFeatureCount        int                                       `json:"paired_feature_count"`
		MissingTrainingFeatures   []string                                  `json:"missing_training_features"`
		MissingLiveShadowFeatures []string                                  `json:"missing_live_shadow_features"`
		LatestObservedAt          *time.Time                                `json:"latest_observed_at,omitempty"`
		RawSamplesStored          bool                                      `json:"raw_samples_stored"`
		ScoringAllowed            bool                                      `json:"scoring_allowed"`
		AlertsEmitted             bool                                      `json:"alerts_emitted"`
		ActionsExecuted           bool                                      `json:"actions_executed"`
		BlockingReasons           []string                                  `json:"blocking_reasons"`
		Snapshots                 []stableSnapshot                          `json:"snapshots"`
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
		MissingTrainingFeatures: append([]string(nil), archive.MissingTrainingFeatures...),
		MissingLiveShadowFeatures: append([]string(nil),
			archive.MissingLiveShadowFeatures...),
		LatestObservedAt: archive.LatestObservedAt,
		RawSamplesStored: archive.RawSamplesStored,
		ScoringAllowed:   archive.ScoringAllowed,
		AlertsEmitted:    archive.AlertsEmitted,
		ActionsExecuted:  archive.ActionsExecuted,
		BlockingReasons:  append([]string(nil), archive.BlockingReasons...),
		Snapshots:        snapshots,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func validationReadinessChecksum(report ValidationReadinessReport) string {
	fingerprint := struct {
		Version                            string                                    `json:"version"`
		FrameworkVersion                   string                                    `json:"framework_version"`
		Mode                               string                                    `json:"mode"`
		Status                             string                                    `json:"status"`
		LabelGateStatus                    string                                    `json:"label_gate_status"`
		LabelManifestVersion               string                                    `json:"label_manifest_version"`
		LabelManifestSHA256                string                                    `json:"label_manifest_sha256"`
		EvidenceBundleVersion              string                                    `json:"evidence_bundle_version"`
		EvidenceBundleSHA256               string                                    `json:"evidence_bundle_sha256"`
		EvidencePositive                   int                                       `json:"evidence_positive_labels"`
		EvidenceExcluded                   int                                       `json:"evidence_excluded_labels"`
		OutcomeReportVersion               string                                    `json:"outcome_report_version"`
		OutcomeStability                   string                                    `json:"outcome_stability_status"`
		OutcomeMaturity                    OutcomeMaturity                           `json:"outcome_maturity"`
		ChallengerVersion                  string                                    `json:"challenger_version"`
		ChallengerStatus                   string                                    `json:"challenger_status"`
		ChallengerConfidence               string                                    `json:"challenger_confidence_status"`
		ChallengerHistoricalSignal         string                                    `json:"challenger_historical_signal_status"`
		DataDriftVersion                   string                                    `json:"data_drift_version"`
		DataDriftSHA256                    string                                    `json:"data_drift_sha256"`
		DataDriftStatus                    string                                    `json:"data_drift_status"`
		DataDriftCoverage                  string                                    `json:"data_drift_coverage_quality_status"`
		DataDriftPSIProxy                  float64                                   `json:"data_drift_psi_proxy"`
		DataDriftKSProxy                   float64                                   `json:"data_drift_ks_proxy"`
		CalibrationDriftVersion            string                                    `json:"calibration_drift_version"`
		CalibrationDriftSHA256             string                                    `json:"calibration_drift_sha256"`
		CalibrationDriftStatus             string                                    `json:"calibration_drift_status"`
		CalibrationECEDelta                float64                                   `json:"calibration_ece_delta"`
		CalibrationBSSDelta                float64                                   `json:"calibration_brier_skill_score_delta"`
		FeatureDriftVersion                string                                    `json:"feature_drift_version"`
		FeatureDriftSHA256                 string                                    `json:"feature_drift_sha256"`
		FeatureDriftStatus                 string                                    `json:"feature_drift_status"`
		FeatureDriftColumns                int                                       `json:"feature_drift_feature_columns"`
		FeatureDriftDistributions          int                                       `json:"feature_drift_feature_distributions"`
		FeatureDriftCompared               int                                       `json:"feature_drift_compared_features"`
		FeatureDriftPassed                 int                                       `json:"feature_drift_passed_features"`
		FeatureDriftReview                 int                                       `json:"feature_drift_review_required_features"`
		FeatureDriftMaxPSI                 float64                                   `json:"feature_drift_maximum_psi"`
		FeatureDriftMaxKS                  float64                                   `json:"feature_drift_maximum_ks"`
		FeatureDriftPSIStatus              string                                    `json:"feature_drift_psi_status"`
		FeatureDriftKSStatus               string                                    `json:"feature_drift_ks_status"`
		FeatureDriftBlockers               []string                                  `json:"feature_drift_blocking_reasons"`
		FeatureDriftNextRun                []string                                  `json:"feature_drift_recommended_next_run"`
		FeatureDistributionArchiveVersion  string                                    `json:"feature_distribution_archive_version"`
		FeatureDistributionArchiveSHA256   string                                    `json:"feature_distribution_archive_sha256"`
		FeatureDistributionArchiveScope    ValidationFeatureDistributionArchiveScope `json:"feature_distribution_archive_scope"`
		FeatureDistributionComparability   string                                    `json:"feature_distribution_comparability_status"`
		FeatureDistributionMinimumPairs    int                                       `json:"feature_distribution_minimum_feature_pairs"`
		FeatureDistributionSnapshots       int                                       `json:"feature_distribution_snapshots"`
		FeatureDistributionTraining        int                                       `json:"feature_distribution_training_snapshots"`
		FeatureDistributionLiveShadow      int                                       `json:"feature_distribution_live_shadow_snapshots"`
		FeatureDistributionFeatures        int                                       `json:"feature_distribution_feature_count"`
		FeatureDistributionPairedFeatures  int                                       `json:"feature_distribution_paired_feature_count"`
		FeatureDistributionMissingTraining []string                                  `json:"feature_distribution_missing_training_features"`
		FeatureDistributionMissingLive     []string                                  `json:"feature_distribution_missing_live_shadow_features"`
		FeatureDistributionBlockers        []string                                  `json:"feature_distribution_blocking_reasons"`
		SevenDayRows                       int                                       `json:"seven_day_rows"`
		SevenDayNodes                      int                                       `json:"seven_day_nodes"`
		SevenDayPositives                  int                                       `json:"seven_day_positives"`
		SevenDay                           []ChallengerMetricSet                     `json:"seven_day"`
		BlockingReasons                    []string                                  `json:"blocking_reasons"`
		RecommendedNextRun                 []string                                  `json:"recommended_next_run"`
	}{
		Version: report.Version, FrameworkVersion: report.FrameworkVersion, Mode: report.Mode, Status: report.Status,
		LabelGateStatus: report.LabelGateStatus, LabelManifestVersion: report.LabelManifestVersion, LabelManifestSHA256: report.LabelManifestSHA256,
		EvidenceBundleVersion: report.EvidenceBundleVersion, EvidenceBundleSHA256: report.EvidenceBundleSHA256,
		EvidencePositive: report.EvidencePositive, EvidenceExcluded: report.EvidenceExcluded,
		OutcomeReportVersion: report.OutcomeReportVersion, OutcomeStability: report.OutcomeStability, OutcomeMaturity: report.OutcomeMaturity,
		ChallengerVersion: report.ChallengerVersion, ChallengerStatus: report.ChallengerStatus,
		ChallengerConfidence: report.ChallengerConfidence, ChallengerHistoricalSignal: report.ChallengerHistoricalSignal,
		DataDriftVersion: report.DataDriftVersion, DataDriftSHA256: report.DataDriftSHA256,
		DataDriftStatus: report.DataDriftStatus, DataDriftCoverage: report.DataDriftCoverage,
		DataDriftPSIProxy: report.DataDriftPSIProxy, DataDriftKSProxy: report.DataDriftKSProxy,
		CalibrationDriftVersion: report.CalibrationDriftVersion, CalibrationDriftSHA256: report.CalibrationDriftSHA256,
		CalibrationDriftStatus: report.CalibrationDriftStatus, CalibrationECEDelta: report.CalibrationECEDelta,
		CalibrationBSSDelta: report.CalibrationBSSDelta,
		FeatureDriftVersion: report.FeatureDriftVersion, FeatureDriftSHA256: report.FeatureDriftSHA256,
		FeatureDriftStatus: report.FeatureDriftStatus, FeatureDriftColumns: report.FeatureDriftColumns,
		FeatureDriftDistributions: report.FeatureDriftDistributions, FeatureDriftCompared: report.FeatureDriftCompared,
		FeatureDriftPassed: report.FeatureDriftPassed, FeatureDriftReview: report.FeatureDriftReview,
		FeatureDriftMaxPSI: report.FeatureDriftMaxPSI, FeatureDriftMaxKS: report.FeatureDriftMaxKS,
		FeatureDriftPSIStatus:             report.FeatureDriftPSIStatus,
		FeatureDriftKSStatus:              report.FeatureDriftKSStatus,
		FeatureDriftBlockers:              report.FeatureDriftBlockers,
		FeatureDriftNextRun:               report.FeatureDriftNextRun,
		FeatureDistributionArchiveVersion: report.FeatureDistributionArchiveVersion,
		FeatureDistributionArchiveSHA256:  report.FeatureDistributionArchiveSHA256,
		FeatureDistributionArchiveScope:   report.FeatureDistributionArchiveScope,
		FeatureDistributionComparability:  report.FeatureDistributionComparability,
		FeatureDistributionMinimumPairs:   report.FeatureDistributionMinimumPairs,
		FeatureDistributionSnapshots:      report.FeatureDistributionSnapshots,
		FeatureDistributionTraining:       report.FeatureDistributionTraining,
		FeatureDistributionLiveShadow:     report.FeatureDistributionLiveShadow,
		FeatureDistributionFeatures:       report.FeatureDistributionFeatures,
		FeatureDistributionPairedFeatures: report.FeatureDistributionPairedFeatures,
		FeatureDistributionMissingTraining: append([]string(nil),
			report.FeatureDistributionMissingTraining...),
		FeatureDistributionMissingLive: append([]string(nil),
			report.FeatureDistributionMissingLive...),
		FeatureDistributionBlockers: report.FeatureDistributionBlockers,
		SevenDayRows:                report.SevenDayRows, SevenDayNodes: report.SevenDayNodes, SevenDayPositives: report.SevenDayPositives,
		SevenDay: report.SevenDay, BlockingReasons: report.BlockingReasons, RecommendedNextRun: report.RecommendedNextRun,
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

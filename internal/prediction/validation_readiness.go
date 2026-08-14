package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

const ValidationReadinessReportVersion = "prediction-validation-readiness-v1"

type ValidationReadinessReport struct {
	Version                   string                `json:"version"`
	FrameworkVersion          string                `json:"framework_version"`
	Mode                      string                `json:"mode"`
	Status                    string                `json:"status"`
	ReadinessSHA256           string                `json:"readiness_sha256"`
	LabelGateStatus           string                `json:"label_gate_status"`
	LabelManifestVersion      string                `json:"label_manifest_version"`
	LabelManifestSHA256       string                `json:"label_manifest_sha256"`
	EvidenceBundleVersion     string                `json:"evidence_bundle_version"`
	EvidenceBundleSHA256      string                `json:"evidence_bundle_sha256"`
	EvidencePositive          int                   `json:"evidence_positive_labels"`
	EvidenceExcluded          int                   `json:"evidence_excluded_labels"`
	OutcomeReportVersion      string                `json:"outcome_report_version"`
	OutcomeStability          string                `json:"outcome_stability_status"`
	OutcomeMaturity           OutcomeMaturity       `json:"outcome_maturity"`
	ChallengerVersion         string                `json:"challenger_version"`
	ChallengerStatus          string                `json:"challenger_status"`
	ChallengerConfidence      string                `json:"challenger_confidence_status"`
	DataDriftVersion          string                `json:"data_drift_version"`
	DataDriftSHA256           string                `json:"data_drift_sha256"`
	DataDriftStatus           string                `json:"data_drift_status"`
	DataDriftCoverage         string                `json:"data_drift_coverage_quality_status"`
	DataDriftPSIProxy         float64               `json:"data_drift_psi_proxy"`
	DataDriftKSProxy          float64               `json:"data_drift_ks_proxy"`
	CalibrationDriftVersion   string                `json:"calibration_drift_version"`
	CalibrationDriftSHA256    string                `json:"calibration_drift_sha256"`
	CalibrationDriftStatus    string                `json:"calibration_drift_status"`
	CalibrationECEDelta       float64               `json:"calibration_ece_delta"`
	CalibrationBSSDelta       float64               `json:"calibration_brier_skill_score_delta"`
	FeatureDriftVersion       string                `json:"feature_drift_version"`
	FeatureDriftSHA256        string                `json:"feature_drift_sha256"`
	FeatureDriftStatus        string                `json:"feature_drift_status"`
	FeatureDriftColumns       int                   `json:"feature_drift_feature_columns"`
	FeatureDriftDistributions int                   `json:"feature_drift_feature_distributions"`
	FeatureDriftPSIStatus     string                `json:"feature_drift_psi_status"`
	FeatureDriftKSStatus      string                `json:"feature_drift_ks_status"`
	SevenDayRows              int                   `json:"seven_day_rows"`
	SevenDayNodes             int                   `json:"seven_day_nodes"`
	SevenDayPositives         int                   `json:"seven_day_positives"`
	SevenDay                  []ChallengerMetricSet `json:"seven_day"`
	BlockingReasons           []string              `json:"blocking_reasons"`
	RecommendedNextRun        []string              `json:"recommended_next_run"`
	GeneratedAt               time.Time             `json:"generated_at"`
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
	report := ValidationReadinessReport{
		Version:                   ValidationReadinessReportVersion,
		FrameworkVersion:          FrameworkVersion,
		Mode:                      "read_only_validation_gate",
		LabelGateStatus:           labelManifest.QualityGateStatus,
		LabelManifestVersion:      labelManifest.Version,
		LabelManifestSHA256:       labelManifest.ManifestSHA256,
		EvidenceBundleVersion:     evidenceBundle.Version,
		EvidenceBundleSHA256:      evidenceBundle.BundleSHA256,
		EvidencePositive:          evidenceBundle.PositiveLabels,
		EvidenceExcluded:          evidenceBundle.ExcludedLabels,
		OutcomeReportVersion:      outcomeReport.Version,
		OutcomeStability:          outcomeReport.Stability.Status,
		OutcomeMaturity:           outcomeReport.SampleMaturity,
		ChallengerVersion:         challengerReport.Version,
		ChallengerStatus:          challengerReport.Status,
		ChallengerConfidence:      challengerReport.ConfidenceStatus,
		DataDriftVersion:          driftReport.Version,
		DataDriftSHA256:           driftReport.ReportSHA256,
		DataDriftStatus:           driftReport.Status,
		DataDriftCoverage:         driftReport.CoverageQualityStatus,
		DataDriftPSIProxy:         driftReport.PSIProxy,
		DataDriftKSProxy:          driftReport.KSProxy,
		CalibrationDriftVersion:   calibrationReport.Version,
		CalibrationDriftSHA256:    calibrationReport.ReportSHA256,
		CalibrationDriftStatus:    calibrationReport.Status,
		CalibrationECEDelta:       calibrationReport.ECEDelta,
		CalibrationBSSDelta:       calibrationReport.BrierSkillScoreDelta,
		FeatureDriftVersion:       featureDriftReport.Version,
		FeatureDriftSHA256:        featureDriftReport.ReportSHA256,
		FeatureDriftStatus:        featureDriftReport.Status,
		FeatureDriftColumns:       featureDriftReport.FeatureColumnCount,
		FeatureDriftDistributions: featureDriftReport.FeatureDistributionCount,
		FeatureDriftPSIStatus:     featureDriftReport.PSIStatus,
		FeatureDriftKSStatus:      featureDriftReport.KSStatus,
		SevenDay:                  challengerReport.SevenDay,
		GeneratedAt:               s.now(),
		RecommendedNextRun: []string{
			"freeze the label manifest SHA256 before comparing challenger metrics",
			"archive the evidence bundle SHA256 with every offline validation run",
			"archive the data drift report SHA256 before comparing shadow candidates",
			"archive the calibration drift report SHA256 before comparing shadow candidates",
			"archive the feature drift readiness report SHA256 before interpreting per-feature drift gates",
			"use the same matured outcome denominator for Logistic, rules, and HeaRank-style policies",
			"keep all validation read-only until label, evidence, outcome, drift, calibration, feature drift, and challenger gates pass together",
		},
	}
	report.SevenDayRows, report.SevenDayNodes, report.SevenDayPositives = validationReadinessSevenDaySummary(report.SevenDay)
	report.BlockingReasons = validationReadinessBlockers(labelManifest, evidenceBundle, outcomeReport, challengerReport, driftReport, calibrationReport, featureDriftReport)
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

func validationReadinessBlockers(labelManifest LabelManifest, evidenceBundle EvidenceBundleReport, outcomeReport OutcomeReport, challengerReport HeaRankChallengerReport, driftReport DataDriftReport, calibrationReport CalibrationDriftReport, featureDriftReport FeatureDriftReport) []string {
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
	return uniqueSorted(blockers)
}

func validationReadinessChecksum(report ValidationReadinessReport) string {
	fingerprint := struct {
		Version                   string                `json:"version"`
		FrameworkVersion          string                `json:"framework_version"`
		Mode                      string                `json:"mode"`
		Status                    string                `json:"status"`
		LabelGateStatus           string                `json:"label_gate_status"`
		LabelManifestVersion      string                `json:"label_manifest_version"`
		LabelManifestSHA256       string                `json:"label_manifest_sha256"`
		EvidenceBundleVersion     string                `json:"evidence_bundle_version"`
		EvidenceBundleSHA256      string                `json:"evidence_bundle_sha256"`
		EvidencePositive          int                   `json:"evidence_positive_labels"`
		EvidenceExcluded          int                   `json:"evidence_excluded_labels"`
		OutcomeReportVersion      string                `json:"outcome_report_version"`
		OutcomeStability          string                `json:"outcome_stability_status"`
		OutcomeMaturity           OutcomeMaturity       `json:"outcome_maturity"`
		ChallengerVersion         string                `json:"challenger_version"`
		ChallengerStatus          string                `json:"challenger_status"`
		ChallengerConfidence      string                `json:"challenger_confidence_status"`
		DataDriftVersion          string                `json:"data_drift_version"`
		DataDriftSHA256           string                `json:"data_drift_sha256"`
		DataDriftStatus           string                `json:"data_drift_status"`
		DataDriftCoverage         string                `json:"data_drift_coverage_quality_status"`
		DataDriftPSIProxy         float64               `json:"data_drift_psi_proxy"`
		DataDriftKSProxy          float64               `json:"data_drift_ks_proxy"`
		CalibrationDriftVersion   string                `json:"calibration_drift_version"`
		CalibrationDriftSHA256    string                `json:"calibration_drift_sha256"`
		CalibrationDriftStatus    string                `json:"calibration_drift_status"`
		CalibrationECEDelta       float64               `json:"calibration_ece_delta"`
		CalibrationBSSDelta       float64               `json:"calibration_brier_skill_score_delta"`
		FeatureDriftVersion       string                `json:"feature_drift_version"`
		FeatureDriftSHA256        string                `json:"feature_drift_sha256"`
		FeatureDriftStatus        string                `json:"feature_drift_status"`
		FeatureDriftColumns       int                   `json:"feature_drift_feature_columns"`
		FeatureDriftDistributions int                   `json:"feature_drift_feature_distributions"`
		FeatureDriftPSIStatus     string                `json:"feature_drift_psi_status"`
		FeatureDriftKSStatus      string                `json:"feature_drift_ks_status"`
		SevenDayRows              int                   `json:"seven_day_rows"`
		SevenDayNodes             int                   `json:"seven_day_nodes"`
		SevenDayPositives         int                   `json:"seven_day_positives"`
		SevenDay                  []ChallengerMetricSet `json:"seven_day"`
		BlockingReasons           []string              `json:"blocking_reasons"`
		RecommendedNextRun        []string              `json:"recommended_next_run"`
	}{
		Version: report.Version, FrameworkVersion: report.FrameworkVersion, Mode: report.Mode, Status: report.Status,
		LabelGateStatus: report.LabelGateStatus, LabelManifestVersion: report.LabelManifestVersion, LabelManifestSHA256: report.LabelManifestSHA256,
		EvidenceBundleVersion: report.EvidenceBundleVersion, EvidenceBundleSHA256: report.EvidenceBundleSHA256,
		EvidencePositive: report.EvidencePositive, EvidenceExcluded: report.EvidenceExcluded,
		OutcomeReportVersion: report.OutcomeReportVersion, OutcomeStability: report.OutcomeStability, OutcomeMaturity: report.OutcomeMaturity,
		ChallengerVersion: report.ChallengerVersion, ChallengerStatus: report.ChallengerStatus,
		ChallengerConfidence: report.ChallengerConfidence,
		DataDriftVersion:     report.DataDriftVersion, DataDriftSHA256: report.DataDriftSHA256,
		DataDriftStatus: report.DataDriftStatus, DataDriftCoverage: report.DataDriftCoverage,
		DataDriftPSIProxy: report.DataDriftPSIProxy, DataDriftKSProxy: report.DataDriftKSProxy,
		CalibrationDriftVersion: report.CalibrationDriftVersion, CalibrationDriftSHA256: report.CalibrationDriftSHA256,
		CalibrationDriftStatus: report.CalibrationDriftStatus, CalibrationECEDelta: report.CalibrationECEDelta,
		CalibrationBSSDelta: report.CalibrationBSSDelta,
		FeatureDriftVersion: report.FeatureDriftVersion, FeatureDriftSHA256: report.FeatureDriftSHA256,
		FeatureDriftStatus: report.FeatureDriftStatus, FeatureDriftColumns: report.FeatureDriftColumns,
		FeatureDriftDistributions: report.FeatureDriftDistributions, FeatureDriftPSIStatus: report.FeatureDriftPSIStatus,
		FeatureDriftKSStatus: report.FeatureDriftKSStatus,
		SevenDayRows:         report.SevenDayRows, SevenDayNodes: report.SevenDayNodes, SevenDayPositives: report.SevenDayPositives,
		SevenDay: report.SevenDay, BlockingReasons: report.BlockingReasons, RecommendedNextRun: report.RecommendedNextRun,
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

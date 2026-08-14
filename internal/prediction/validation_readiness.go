package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const ValidationReadinessReportVersion = "prediction-validation-readiness-v1"

type ValidationReadinessReport struct {
	Version              string                `json:"version"`
	FrameworkVersion     string                `json:"framework_version"`
	Mode                 string                `json:"mode"`
	Status               string                `json:"status"`
	ReadinessSHA256      string                `json:"readiness_sha256"`
	LabelGateStatus      string                `json:"label_gate_status"`
	LabelManifestVersion string                `json:"label_manifest_version"`
	LabelManifestSHA256  string                `json:"label_manifest_sha256"`
	OutcomeReportVersion string                `json:"outcome_report_version"`
	OutcomeStability     string                `json:"outcome_stability_status"`
	OutcomeMaturity      OutcomeMaturity       `json:"outcome_maturity"`
	ChallengerVersion    string                `json:"challenger_version"`
	ChallengerStatus     string                `json:"challenger_status"`
	ChallengerConfidence string                `json:"challenger_confidence_status"`
	SevenDayRows         int                   `json:"seven_day_rows"`
	SevenDayNodes        int                   `json:"seven_day_nodes"`
	SevenDayPositives    int                   `json:"seven_day_positives"`
	SevenDay             []ChallengerMetricSet `json:"seven_day"`
	BlockingReasons      []string              `json:"blocking_reasons"`
	RecommendedNextRun   []string              `json:"recommended_next_run"`
	GeneratedAt          time.Time             `json:"generated_at"`
}

func (s *Service) ValidationReadinessReport() (ValidationReadinessReport, error) {
	labelManifest, err := s.LabelManifest()
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
	report := ValidationReadinessReport{
		Version:              ValidationReadinessReportVersion,
		FrameworkVersion:     FrameworkVersion,
		Mode:                 "read_only_validation_gate",
		LabelGateStatus:      labelManifest.QualityGateStatus,
		LabelManifestVersion: labelManifest.Version,
		LabelManifestSHA256:  labelManifest.ManifestSHA256,
		OutcomeReportVersion: outcomeReport.Version,
		OutcomeStability:     outcomeReport.Stability.Status,
		OutcomeMaturity:      outcomeReport.SampleMaturity,
		ChallengerVersion:    challengerReport.Version,
		ChallengerStatus:     challengerReport.Status,
		ChallengerConfidence: challengerReport.ConfidenceStatus,
		SevenDay:             challengerReport.SevenDay,
		GeneratedAt:          s.now(),
		RecommendedNextRun: []string{
			"freeze the label manifest SHA256 before comparing challenger metrics",
			"use the same matured outcome denominator for Logistic, rules, and HeaRank-style policies",
			"keep all validation read-only until label, outcome, and challenger gates pass together",
		},
	}
	report.SevenDayRows, report.SevenDayNodes, report.SevenDayPositives = validationReadinessSevenDaySummary(report.SevenDay)
	report.BlockingReasons = validationReadinessBlockers(labelManifest, outcomeReport, challengerReport)
	if len(report.BlockingReasons) > 0 {
		report.Status = "blocked"
		report.RecommendedNextRun = append(report.RecommendedNextRun, report.BlockingReasons...)
	} else if labelManifest.QualityGateStatus == "exploratory_ready" || outcomeReport.Stability.Status == "exploratory" || outcomeReport.SampleMaturity.Matured < 30 {
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

func validationReadinessBlockers(labelManifest LabelManifest, outcomeReport OutcomeReport, challengerReport HeaRankChallengerReport) []string {
	blockers := []string{}
	if labelManifest.QualityGateStatus == "blocked" {
		blockers = append(blockers, labelManifest.BlockingReasons...)
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
	return uniqueSorted(blockers)
}

func validationReadinessChecksum(report ValidationReadinessReport) string {
	fingerprint := struct {
		Version              string                `json:"version"`
		FrameworkVersion     string                `json:"framework_version"`
		Mode                 string                `json:"mode"`
		Status               string                `json:"status"`
		LabelGateStatus      string                `json:"label_gate_status"`
		LabelManifestVersion string                `json:"label_manifest_version"`
		LabelManifestSHA256  string                `json:"label_manifest_sha256"`
		OutcomeReportVersion string                `json:"outcome_report_version"`
		OutcomeStability     string                `json:"outcome_stability_status"`
		OutcomeMaturity      OutcomeMaturity       `json:"outcome_maturity"`
		ChallengerVersion    string                `json:"challenger_version"`
		ChallengerStatus     string                `json:"challenger_status"`
		ChallengerConfidence string                `json:"challenger_confidence_status"`
		SevenDayRows         int                   `json:"seven_day_rows"`
		SevenDayNodes        int                   `json:"seven_day_nodes"`
		SevenDayPositives    int                   `json:"seven_day_positives"`
		SevenDay             []ChallengerMetricSet `json:"seven_day"`
		BlockingReasons      []string              `json:"blocking_reasons"`
		RecommendedNextRun   []string              `json:"recommended_next_run"`
	}{
		Version: report.Version, FrameworkVersion: report.FrameworkVersion, Mode: report.Mode, Status: report.Status,
		LabelGateStatus: report.LabelGateStatus, LabelManifestVersion: report.LabelManifestVersion, LabelManifestSHA256: report.LabelManifestSHA256,
		OutcomeReportVersion: report.OutcomeReportVersion, OutcomeStability: report.OutcomeStability, OutcomeMaturity: report.OutcomeMaturity,
		ChallengerVersion: report.ChallengerVersion, ChallengerStatus: report.ChallengerStatus,
		ChallengerConfidence: report.ChallengerConfidence,
		SevenDayRows:         report.SevenDayRows, SevenDayNodes: report.SevenDayNodes, SevenDayPositives: report.SevenDayPositives,
		SevenDay: report.SevenDay, BlockingReasons: report.BlockingReasons, RecommendedNextRun: report.RecommendedNextRun,
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

package prediction

import "time"

const ValidationReadinessReportVersion = "prediction-validation-readiness-v1"

type ValidationReadinessReport struct {
	Version             string                `json:"version"`
	FrameworkVersion    string                `json:"framework_version"`
	Mode                string                `json:"mode"`
	Status              string                `json:"status"`
	LabelGateStatus     string                `json:"label_gate_status"`
	LabelManifestSHA256 string                `json:"label_manifest_sha256"`
	OutcomeMaturity     OutcomeMaturity       `json:"outcome_maturity"`
	ChallengerStatus    string                `json:"challenger_status"`
	SevenDay            []ChallengerMetricSet `json:"seven_day"`
	BlockingReasons     []string              `json:"blocking_reasons"`
	RecommendedNextRun  []string              `json:"recommended_next_run"`
	GeneratedAt         time.Time             `json:"generated_at"`
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
		Version:             ValidationReadinessReportVersion,
		FrameworkVersion:    FrameworkVersion,
		Mode:                "read_only_validation_gate",
		LabelGateStatus:     labelManifest.QualityGateStatus,
		LabelManifestSHA256: labelManifest.ManifestSHA256,
		OutcomeMaturity:     outcomeReport.SampleMaturity,
		ChallengerStatus:    challengerReport.Status,
		SevenDay:            challengerReport.SevenDay,
		GeneratedAt:         s.now(),
		RecommendedNextRun: []string{
			"freeze the label manifest SHA256 before comparing challenger metrics",
			"use the same matured outcome denominator for Logistic, rules, and HeaRank-style policies",
			"keep all validation read-only until label, outcome, and challenger gates pass together",
		},
	}
	report.BlockingReasons = validationReadinessBlockers(labelManifest, outcomeReport, challengerReport)
	if len(report.BlockingReasons) > 0 {
		report.Status = "blocked"
		report.RecommendedNextRun = append(report.RecommendedNextRun, report.BlockingReasons...)
	} else if labelManifest.QualityGateStatus == "exploratory_ready" || outcomeReport.SampleMaturity.Matured < 30 {
		report.Status = "exploratory_ready"
		report.RecommendedNextRun = append(report.RecommendedNextRun, "treat metrics as exploratory until more mature outcomes accumulate")
	} else {
		report.Status = "ready_for_offline_validation"
	}
	report.RecommendedNextRun = uniqueSorted(report.RecommendedNextRun)
	return report, nil
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
	if challengerReport.Status != "ready_for_offline_comparison" {
		blockers = append(blockers, challengerReport.BlockingReasons...)
	}
	return uniqueSorted(blockers)
}

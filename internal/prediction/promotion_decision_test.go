package prediction

import (
	"testing"
	"time"
)

func TestPromotionDecisionRequiresEveryFrozenGate(t *testing.T) {
	now := time.Date(2026, time.August, 21, 8, 0, 0, 0, time.UTC)
	cutoff := now.Add(-7 * 24 * time.Hour)
	source := DualTrackValidationReport{
		Version: DualTrackValidationReportVersion, FrameworkVersion: FrameworkVersion,
		Status: "comparable", ReportSHA256: "dual-track-sha",
		Alignment: DualTrackAlignment{
			Status: "aligned", ModelSpecID: 7, ModelKey: "gpu-model", ModelVersion: "build-7",
			HorizonMinutes: 10080, SnapshotCutoffAt: &cutoff,
		},
		Readiness: ValidationReadinessReport{
			Version: ValidationReadinessReportVersion, Status: "ready_for_offline_validation",
			ReadinessSHA256: "readiness-sha", ValidationSlicesSHA256: "slices-sha", BlockingReasons: []string{},
		},
		TemporalConsistency: DualTrackTemporalConsistency{
			Ranking:     TemporalTrackConsistency{Status: "consistent"},
			Probability: TemporalTrackConsistency{Status: "consistent"},
		},
		SliceAudit:         DualTrackSliceAudit{Version: DualTrackSliceAuditVersion},
		SliceComparability: DualTrackSliceComparability{Status: "comparable"},
		Safety:             RiskRankingSafety{ReadOnlyShadow: true, NoAlertEmitted: true, NoActionExecuted: true},
		GeneratedAt:        now,
	}

	report := promotionDecisionReport(source)
	if report.Status != "eligible_for_shadow_observation" || report.DecisionSHA256 == "" || len(report.BlockingReasons) != 0 || len(report.Gates) != 7 {
		t.Fatalf("unexpected eligible promotion decision: %+v", report)
	}
	later := source
	later.GeneratedAt = now.Add(time.Hour)
	if promotionDecisionReport(later).DecisionSHA256 != report.DecisionSHA256 {
		t.Fatal("promotion decision checksum must ignore generated_at")
	}

	source.TemporalConsistency.Probability.Status = "review_mixed_direction"
	source.Readiness.BlockingReasons = []string{"probability temporal consistency is review_mixed_direction"}
	blocked := promotionDecisionReport(source)
	if blocked.Status != "blocked" || len(blocked.BlockingReasons) == 0 || blocked.DecisionSHA256 == report.DecisionSHA256 {
		t.Fatalf("failed temporal consistency must block and change the decision fingerprint: %+v", blocked)
	}
}

func TestPromotionDecisionPreservesExploratoryReadiness(t *testing.T) {
	source := DualTrackValidationReport{
		Version: DualTrackValidationReportVersion, Status: "exploratory", ReportSHA256: "dual-track-sha",
		Alignment: DualTrackAlignment{Status: "aligned"},
		Readiness: ValidationReadinessReport{
			Version: ValidationReadinessReportVersion, Status: "exploratory_ready",
			ReadinessSHA256: "readiness-sha", ValidationSlicesSHA256: "slices-sha", BlockingReasons: []string{},
		},
		TemporalConsistency: DualTrackTemporalConsistency{
			Ranking:     TemporalTrackConsistency{Status: "consistent"},
			Probability: TemporalTrackConsistency{Status: "consistent"},
		},
		SliceAudit:         DualTrackSliceAudit{Version: DualTrackSliceAuditVersion},
		SliceComparability: DualTrackSliceComparability{Status: "exploratory_partial_comparability"},
		Safety:             RiskRankingSafety{ReadOnlyShadow: true, NoAlertEmitted: true, NoActionExecuted: true},
	}
	report := promotionDecisionReport(source)
	if report.Status != "exploratory_ready" || len(report.BlockingReasons) != 0 {
		t.Fatalf("exploratory evidence should remain non-promotable without becoming a hard failure: %+v", report)
	}
}

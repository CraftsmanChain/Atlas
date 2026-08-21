package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const PromotionDecisionReportVersion = "prediction-promotion-decision-v1"

type PromotionDecisionGate struct {
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	EvidenceVersion string   `json:"evidence_version,omitempty"`
	EvidenceSHA256  string   `json:"evidence_sha256,omitempty"`
	BlockingReasons []string `json:"blocking_reasons"`
}

type PromotionDecisionReport struct {
	Version            string                  `json:"version"`
	FrameworkVersion   string                  `json:"framework_version"`
	Mode               string                  `json:"mode"`
	Status             string                  `json:"status"`
	DecisionSHA256     string                  `json:"decision_sha256"`
	DualTrackVersion   string                  `json:"dual_track_version"`
	DualTrackSHA256    string                  `json:"dual_track_sha256"`
	ReadinessVersion   string                  `json:"readiness_version"`
	ReadinessSHA256    string                  `json:"readiness_sha256"`
	ModelSpecID        uint                    `json:"model_spec_id,omitempty"`
	ModelKey           string                  `json:"model_key,omitempty"`
	ModelVersion       string                  `json:"model_version,omitempty"`
	HorizonMinutes     int                     `json:"horizon_minutes"`
	SnapshotCutoffAt   *time.Time              `json:"snapshot_cutoff_at,omitempty"`
	Gates              []PromotionDecisionGate `json:"gates"`
	Safety             RiskRankingSafety       `json:"safety"`
	BlockingReasons    []string                `json:"blocking_reasons"`
	RecommendedNextRun []string                `json:"recommended_next_run"`
	GeneratedAt        time.Time               `json:"generated_at"`
}

func promotionDecisionReport(source DualTrackValidationReport) PromotionDecisionReport {
	report := PromotionDecisionReport{
		Version: PromotionDecisionReportVersion, FrameworkVersion: FrameworkVersion,
		Mode:             "read_only_shadow_promotion_decision",
		DualTrackVersion: source.Version, DualTrackSHA256: source.ReportSHA256,
		ReadinessVersion: source.Readiness.Version, ReadinessSHA256: source.Readiness.ReadinessSHA256,
		ModelSpecID: source.Alignment.ModelSpecID, ModelKey: source.Alignment.ModelKey,
		ModelVersion: source.Alignment.ModelVersion, HorizonMinutes: source.Alignment.HorizonMinutes,
		SnapshotCutoffAt: source.Alignment.SnapshotCutoffAt, Safety: source.Safety,
		Gates: []PromotionDecisionGate{}, BlockingReasons: []string{},
		RecommendedNextRun: []string{
			"keep scoring, alerts, and operational actions disabled",
			"archive this decision SHA256 with the bound dual-track and readiness reports",
		},
		GeneratedAt: source.GeneratedAt,
	}

	report.Gates = append(report.Gates,
		promotionGate("alignment", source.Alignment.Status == "aligned", false, source.Version, source.ReportSHA256, "prediction scope is not aligned to a frozen model, horizon, and cutoff"),
		promotionGate("dual_track_validation", source.Status == "comparable", source.Status == "exploratory", source.Version, source.ReportSHA256, "ranking and probability tracks are not jointly comparable"),
		promotionGate("validation_readiness", source.Readiness.Status == "ready_for_offline_validation", source.Readiness.Status == "exploratory_ready", source.Readiness.Version, source.Readiness.ReadinessSHA256, "validation readiness has not passed every evidence gate"),
		promotionGate("temporal_ranking_consistency", source.TemporalConsistency.Ranking.Status == "consistent", false, source.Version, source.ReportSHA256, "ranking direction is not consistent across independent cohorts"),
		promotionGate("temporal_probability_consistency", source.TemporalConsistency.Probability.Status == "consistent", false, source.Version, source.ReportSHA256, "probability quality is not consistent across independent cohorts"),
		promotionGate("slice_comparability", source.SliceComparability.Status == "comparable", source.SliceComparability.Status == "exploratory_partial_comparability" || source.SliceComparability.Status == "insufficient_sample", source.SliceAudit.Version, source.Readiness.ValidationSlicesSHA256, "prediction-time slice dimensions are not jointly comparable"),
		promotionGate("read_only_safety", source.Safety.ReadOnlyShadow && source.Safety.NoAlertEmitted && source.Safety.NoActionExecuted, false, source.Version, source.ReportSHA256, "read-only shadow safety guarantees are incomplete"),
	)

	exploratory := false
	for _, gate := range report.Gates {
		switch gate.Status {
		case "blocked":
			report.BlockingReasons = append(report.BlockingReasons, gate.BlockingReasons...)
		case "exploratory":
			exploratory = true
		}
	}
	report.BlockingReasons = append(report.BlockingReasons, source.Readiness.BlockingReasons...)
	report.BlockingReasons = uniqueSorted(report.BlockingReasons)
	if len(report.BlockingReasons) > 0 {
		report.Status = "blocked"
		report.RecommendedNextRun = append(report.RecommendedNextRun, report.BlockingReasons...)
	} else if exploratory {
		report.Status = "exploratory_ready"
		report.RecommendedNextRun = append(report.RecommendedNextRun, "accumulate more mature prospective outcomes before shadow observation eligibility")
	} else {
		report.Status = "eligible_for_shadow_observation"
		report.RecommendedNextRun = append(report.RecommendedNextRun, "continue bounded read-only shadow observation under the frozen evidence scope")
	}
	report.RecommendedNextRun = uniqueSorted(report.RecommendedNextRun)
	report.DecisionSHA256 = promotionDecisionChecksum(report)
	return report
}

func promotionGate(name string, passed, exploratory bool, version, sha, reason string) PromotionDecisionGate {
	gate := PromotionDecisionGate{Name: name, Status: "passed", EvidenceVersion: version, EvidenceSHA256: sha, BlockingReasons: []string{}}
	if passed {
		return gate
	}
	if exploratory {
		gate.Status = "exploratory"
		return gate
	}
	gate.Status = "blocked"
	gate.BlockingReasons = []string{reason}
	return gate
}

func promotionDecisionChecksum(report PromotionDecisionReport) string {
	fingerprint := report
	fingerprint.DecisionSHA256 = ""
	fingerprint.GeneratedAt = time.Time{}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

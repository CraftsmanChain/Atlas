package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"atlas/pkg/api"
)

const DualTrackValidationReportVersion = "prediction-dual-track-validation-v1"

type DualTrackAlignment struct {
	Status             string     `json:"status"`
	ModelSpecID        uint       `json:"model_spec_id,omitempty"`
	ModelKey           string     `json:"model_key,omitempty"`
	ModelVersion       string     `json:"model_version,omitempty"`
	HorizonMinutes     int        `json:"horizon_minutes"`
	SnapshotCutoffAt   *time.Time `json:"snapshot_cutoff_at,omitempty"`
	AlignedOutcomeRows int        `json:"aligned_outcome_rows"`
}

type RankingValidationTrack struct {
	Status          string       `json:"status"`
	Policy          string       `json:"policy"`
	ScoreSemantics  string       `json:"score_semantics"`
	SnapshotVersion string       `json:"snapshot_version"`
	SnapshotSHA256  string       `json:"snapshot_sha256"`
	Nodes           int          `json:"nodes"`
	MaturedRows     int          `json:"matured_rows"`
	PositiveRows    int          `json:"positive_rows"`
	Metrics         []RankingAtK `json:"ranking_at_k"`
	BlockingReasons []string     `json:"blocking_reasons"`
}

type ProbabilityValidationTrack struct {
	Status               string          `json:"status"`
	OutcomeReportVersion string          `json:"outcome_report_version"`
	HorizonMinutes       int             `json:"horizon_minutes"`
	Maturity             OutcomeMaturity `json:"maturity"`
	PositiveRows         int             `json:"positive_rows"`
	ProbabilityCoverage  float64         `json:"probability_coverage"`
	Rule                 AccuracyMetrics `json:"rule"`
	Final                AccuracyMetrics `json:"final"`
	CalibrationStatus    string          `json:"calibration_status"`
	BlockingReasons      []string        `json:"blocking_reasons"`
}

type DualTrackValidationEvidence struct {
	ReadinessVersion       string `json:"readiness_version"`
	ReadinessSHA256        string `json:"readiness_sha256"`
	ReadinessStatus        string `json:"readiness_status"`
	ChallengerVersion      string `json:"challenger_version"`
	ChallengerStatus       string `json:"challenger_status"`
	DataDriftStatus        string `json:"data_drift_status"`
	CalibrationDriftStatus string `json:"calibration_drift_status"`
	FeatureDriftStatus     string `json:"feature_drift_status"`
}

type DualTrackValidationReport struct {
	Version            string                      `json:"version"`
	FrameworkVersion   string                      `json:"framework_version"`
	Mode               string                      `json:"mode"`
	Status             string                      `json:"status"`
	ReportSHA256       string                      `json:"report_sha256"`
	Alignment          DualTrackAlignment          `json:"alignment"`
	Ranking            RankingValidationTrack      `json:"ranking_track"`
	Probability        ProbabilityValidationTrack  `json:"probability_track"`
	Evidence           DualTrackValidationEvidence `json:"evidence"`
	Readiness          ValidationReadinessReport   `json:"readiness"`
	Safety             RiskRankingSafety           `json:"safety"`
	Interpretation     []string                    `json:"interpretation"`
	RecommendedNextRun []string                    `json:"recommended_next_run"`
	GeneratedAt        time.Time                   `json:"generated_at"`
}

func (s *Service) DualTrackValidationReport() (DualTrackValidationReport, error) {
	rankingSnapshot, err := s.RiskRankingSnapshotReport()
	if err != nil {
		return DualTrackValidationReport{}, err
	}
	readiness, err := s.ValidationReadinessReport()
	if err != nil {
		return DualTrackValidationReport{}, err
	}
	report := DualTrackValidationReport{
		Version: DualTrackValidationReportVersion, FrameworkVersion: FrameworkVersion,
		Mode: "read_only_prospective_shadow", Safety: rankingSnapshot.Safety,
		Alignment: DualTrackAlignment{
			Status: "blocked_no_ranking_snapshot", ModelSpecID: rankingSnapshot.ModelSpecID,
			ModelKey: rankingSnapshot.ModelKey, ModelVersion: rankingSnapshot.ModelVersion,
			HorizonMinutes: rankingSnapshot.HorizonMinutes, SnapshotCutoffAt: rankingSnapshot.SnapshotCutoffAt,
		},
		Ranking: RankingValidationTrack{
			Status: "blocked", Policy: rankingSnapshot.Policy, ScoreSemantics: rankingSnapshot.ScoreSemantics,
			SnapshotVersion: rankingSnapshot.Version, SnapshotSHA256: rankingSnapshot.ReportSHA256,
			Nodes: rankingSnapshot.NodeCount, Metrics: []RankingAtK{}, BlockingReasons: append([]string(nil), rankingSnapshot.BlockingReasons...),
		},
		Probability: ProbabilityValidationTrack{
			Status: "blocked", OutcomeReportVersion: "prediction-outcome-report-v1",
			HorizonMinutes: rankingSnapshot.HorizonMinutes, CalibrationStatus: readiness.CalibrationDriftStatus,
			BlockingReasons: []string{"no ranking-aligned outcome cohort is available"},
		},
		Evidence: DualTrackValidationEvidence{
			ReadinessVersion: readiness.Version, ReadinessSHA256: readiness.ReadinessSHA256, ReadinessStatus: readiness.Status,
			ChallengerVersion: readiness.ChallengerVersion, ChallengerStatus: readiness.ChallengerStatus,
			DataDriftStatus: readiness.DataDriftStatus, CalibrationDriftStatus: readiness.CalibrationDriftStatus,
			FeatureDriftStatus: readiness.FeatureDriftStatus,
		},
		Readiness: readiness,
		Interpretation: []string{
			"ranking track answers who should be reviewed first and is evaluated with node Ranking@K metrics",
			"probability track answers event risk within a fixed horizon and is evaluated with discrimination and calibration metrics",
			"track statuses remain independent; no combined success rate is produced",
		},
		RecommendedNextRun: []string{
			"wait for the aligned outcome cohort to mature before interpreting either track",
			"compare both tracks on the same frozen cutoff without tuning from test outcomes",
		},
		GeneratedAt: s.now(),
	}
	if rankingSnapshot.Status != "shadow_snapshot_available" || rankingSnapshot.SnapshotCutoffAt == nil {
		if len(report.Ranking.BlockingReasons) == 0 {
			report.Ranking.BlockingReasons = []string{"risk ranking snapshot is unavailable"}
		}
		report.Status = "blocked"
		report.ReportSHA256 = dualTrackValidationChecksum(report)
		return report, nil
	}

	var rows []api.PredictionOutcomeEvaluation
	if err := s.db.Where("model_spec_id = ? AND horizon_minutes = ? AND prediction_evaluated_at = ?", rankingSnapshot.ModelSpecID, rankingSnapshot.HorizonMinutes, *rankingSnapshot.SnapshotCutoffAt).
		Order("node_ip ASC, gpu_uuid ASC, id ASC").Find(&rows).Error; err != nil {
		return DualTrackValidationReport{}, err
	}
	report.Alignment.AlignedOutcomeRows = len(rows)
	if len(rows) == 0 {
		report.Alignment.Status = "blocked_no_aligned_outcomes"
		report.Ranking.BlockingReasons = uniqueSorted(append(report.Ranking.BlockingReasons, "no outcomes align with the frozen ranking cutoff"))
		report.Status = "blocked"
		report.ReportSHA256 = dualTrackValidationChecksum(report)
		return report, nil
	}
	report.Alignment.Status = "aligned"
	accuracy := accuracyFromRows(rows, report.GeneratedAt)
	maturity := outcomeMaturity(rows)
	stability := outcomeStability(maturity, accuracy)
	positives := accuracy.Final.TP + accuracy.Final.FN
	report.Ranking.MaturedRows = maturity.Matured
	report.Ranking.PositiveRows = positives
	report.Ranking.Metrics = append([]RankingAtK(nil), accuracy.Final.NodeRankingAtK...)
	report.Ranking.Status, report.Ranking.BlockingReasons = dualTrackRankingStatus(rankingSnapshot, maturity, positives)
	report.Probability.Status = stability.Status
	report.Probability.Maturity = maturity
	report.Probability.PositiveRows = positives
	report.Probability.ProbabilityCoverage = stability.ProbabilityCoverage
	report.Probability.Rule = accuracy.Rule
	report.Probability.Final = accuracy.Final
	report.Probability.BlockingReasons = append([]string(nil), stability.BlockingReasons...)
	report.Status = dualTrackOverallStatus(report.Ranking.Status, report.Probability.Status)
	report.ReportSHA256 = dualTrackValidationChecksum(report)
	return report, nil
}

func dualTrackRankingStatus(snapshot RiskRankingSnapshotReport, maturity OutcomeMaturity, positives int) (string, []string) {
	blockers := append([]string(nil), snapshot.BlockingReasons...)
	if snapshot.Status != "shadow_snapshot_available" {
		return "blocked", uniqueSorted(blockers)
	}
	if maturity.Matured == 0 {
		blockers = append(blockers, "no mature ranking-aligned outcomes are available")
	}
	if positives == 0 {
		blockers = append(blockers, "no positive mature ranking-aligned outcomes are available")
	}
	if maturity.NodeEligible == 0 {
		blockers = append(blockers, "no node-eligible mature outcomes are available")
	}
	if len(blockers) > 0 {
		return "blocked", uniqueSorted(blockers)
	}
	if maturity.Matured < HeaRankMinimumSevenDayRows || maturity.NodeEligible < HeaRankMinimumSevenDayNodes || positives < HeaRankMinimumSevenDayPositives {
		return "exploratory", []string{"aligned ranking cohort is below the offline comparison sample gate"}
	}
	return "comparable", []string{}
}

func dualTrackOverallStatus(rankingStatus, probabilityStatus string) string {
	if rankingStatus == "blocked" || probabilityStatus == "blocked" {
		return "blocked"
	}
	if rankingStatus == "exploratory" || probabilityStatus == "exploratory" {
		return "exploratory"
	}
	return "comparable"
}

func dualTrackValidationChecksum(report DualTrackValidationReport) string {
	fingerprint := report
	fingerprint.ReportSHA256 = ""
	fingerprint.GeneratedAt = time.Time{}
	fingerprint.Readiness.GeneratedAt = time.Time{}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

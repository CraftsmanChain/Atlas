package prediction

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestTemporalProbabilityQualityMetrics(t *testing.T) {
	probabilities := []float64{0.9, 0.8, 0.2, 0.1, 1.2}
	actuals := []int{1, 0, 1, 0, 0}
	rows := make([]api.PredictionOutcomeEvaluation, 0, len(probabilities))
	for index := range probabilities {
		probability, actual := probabilities[index], actuals[index]
		rows = append(rows, api.PredictionOutcomeEvaluation{MaturityStatus: "matured", Probability: &probability, FinalActualValue: &actual, PredictedPositive: probability >= 0.5})
	}
	metrics := temporalProbabilityMetrics(rows)
	if metrics.Status != "exploratory" || metrics.ScoredRows != 4 || metrics.PositiveRows != 2 || metrics.NegativeRows != 2 || metrics.InvalidProbabilityRows != 1 || metrics.InvalidLabelRows != 0 || metrics.CalibrationBins != 10 || metrics.CalibrationBinsUsed != 4 {
		t.Fatalf("unexpected probability quality coverage: %+v", metrics)
	}
	if metrics.TP != 1 || metrics.FP != 1 || metrics.FN != 1 || metrics.TN != 1 || metrics.F1Score == nil || math.Abs(*metrics.F1Score-0.5) > 1e-12 {
		t.Fatalf("probability quality confusion matrix must use only valid audited rows: %+v", metrics)
	}
	if metrics.ROCAUC == nil || math.Abs(*metrics.ROCAUC-0.75) > 1e-12 {
		t.Fatalf("expected tie-safe ROC-AUC 0.75: %+v", metrics)
	}
	if metrics.PRAUCAveragePrecision == nil || math.Abs(*metrics.PRAUCAveragePrecision-5.0/6.0) > 1e-12 {
		t.Fatalf("expected average precision 5/6: %+v", metrics)
	}
	if metrics.ExpectedCalibrationError == nil || metrics.BrierScore == nil || metrics.NullBrierScore == nil || metrics.BrierSkillScore == nil {
		t.Fatalf("expected calibration and Brier metrics: %+v", metrics)
	}
	tieValues := []temporalScoredActual{{probability: 0.5, actual: 1}, {probability: 0.5, actual: 0}}
	if auc := temporalROCAUC(tieValues); auc == nil || math.Abs(*auc-0.5) > 1e-12 {
		t.Fatalf("equal scores should produce ROC-AUC 0.5: %v", auc)
	}
	if averagePrecision := temporalAveragePrecision(tieValues); averagePrecision == nil || math.Abs(*averagePrecision-0.5) > 1e-12 {
		t.Fatalf("equal scores should produce tie-invariant average precision 0.5: %v", averagePrecision)
	}
	positive, one := 0.8, 1
	singleClass := temporalProbabilityMetrics([]api.PredictionOutcomeEvaluation{{MaturityStatus: "matured", Probability: &positive, FinalActualValue: &one, PredictedPositive: true}})
	if singleClass.Status != "blocked_single_class" || singleClass.ROCAUC != nil || singleClass.PRAUCAveragePrecision != nil || singleClass.BrierSkillScore != nil || len(singleClass.BlockingReasons) == 0 {
		t.Fatalf("single-class cohorts must not expose discrimination or skill as comparable: %+v", singleClass)
	}
	empty := temporalProbabilityMetrics(nil)
	if empty.Status != "blocked_no_scored_rows" || empty.ScoredRows != 0 || len(empty.BlockingReasons) == 0 {
		t.Fatalf("empty cohorts must expose an explicit blocked quality state: %+v", empty)
	}
}

func TestDualTrackEmptyRankingMetricsSerializeAsArray(t *testing.T) {
	cohort := DualTrackTemporalCohort{NodeRankingAtK: nonNilRankingMetrics(nil)}
	if cohort.NodeRankingAtK == nil {
		t.Fatal("empty temporal Ranking@K must use a non-nil slice")
	}
	payload, err := json.Marshal(cohort)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte(`"node_ranking_at_k":[]`)) {
		t.Fatalf("empty temporal Ranking@K must serialize as []: %s", payload)
	}
}

func TestDualTrackProbabilityStatusConsumesQualityGate(t *testing.T) {
	if got := dualTrackProbabilityStatus("comparable", "blocked_single_class"); got != "blocked" {
		t.Fatalf("single-class quality must block the probability track, got %q", got)
	}
	if got := dualTrackProbabilityStatus("comparable", "exploratory"); got != "exploratory" {
		t.Fatalf("small-sample quality must keep the probability track exploratory, got %q", got)
	}
	if got := dualTrackProbabilityStatus("comparable", "comparable"); got != "comparable" {
		t.Fatalf("both gates must pass before the probability track is comparable, got %q", got)
	}
}

func TestDualTrackSliceAuditRequiresFrozenPredictionDimensions(t *testing.T) {
	one, zero := 1, 0
	rows := []api.PredictionOutcomeEvaluation{
		{MaturityStatus: "matured", FinalActualValue: &one, ModelName: "H100", ScopeEventType: "xid_94", DataCenterID: "dc-a", DriverVersion: "560", SliceContract: api.PredictionSliceContractVersion, RuleLabelQuality: "confirmed"},
		{MaturityStatus: "matured", FinalActualValue: &zero, ModelName: "H100", ScopeEventType: "xid_94", DataCenterID: "dc-a", DriverVersion: "560", SliceContract: api.PredictionSliceContractVersion},
	}
	audit := dualTrackSliceAudit(rows)
	if audit.Status != "ready" || audit.Version != DualTrackSliceAuditVersion || audit.ContractVersion != api.PredictionSliceContractVersion || len(audit.Dimensions) != 5 {
		t.Fatalf("fully frozen predictive dimensions should be ready: %+v", audit)
	}
	if audit.Dimensions[4].Status != "audit_only_post_outcome" || audit.Dimensions[4].ApplicableRows != 1 || audit.Dimensions[4].FrozenRows != 1 {
		t.Fatalf("label quality must remain an outcome-only audit dimension: %+v", audit.Dimensions[4])
	}
	rows[1].DriverVersion = ""
	partial := dualTrackSliceAudit(rows)
	if partial.Status != "partial" || partial.Dimensions[3].Status != "partial" || partial.Dimensions[3].MissingRows != 1 {
		t.Fatalf("partially frozen driver versions must not be marked ready: %+v", partial)
	}
	rows[1].DriverVersion = "560"
	rows[1].SliceContract = ""
	contractPartial := dualTrackSliceAudit(rows)
	if contractPartial.Status != "partial" || contractPartial.Dimensions[0].MissingRows != 1 {
		t.Fatalf("unversioned dimensions must not be accepted as frozen evidence: %+v", contractPartial)
	}
	legacy := dualTrackSliceAudit([]api.PredictionOutcomeEvaluation{{MaturityStatus: "matured", FinalActualValue: &zero}})
	if legacy.Status != "blocked_missing_frozen_dimensions" {
		t.Fatalf("legacy rows without frozen dimensions must be blocked: %+v", legacy)
	}
}

func TestDualTrackValidationSlicesUseOnlyReadyPredictiveDimensions(t *testing.T) {
	probabilities := []float64{0.9, 0.7, 0.3, 0.1}
	actuals := []int{1, 0, 0, 0}
	dataCenters := []string{"dc-a", "dc-a", "dc-b", "dc-b"}
	rows := make([]api.PredictionOutcomeEvaluation, 0, len(probabilities))
	for index := range probabilities {
		probability, actual := probabilities[index], actuals[index]
		rows = append(rows, api.PredictionOutcomeEvaluation{
			MaturityStatus: "matured", FinalActualValue: &actual, Probability: &probability, PredictedPositive: probability >= 0.5,
			NodeIP: "10.0.0." + string(rune('1'+index)), ModelName: "H100", ScopeEventType: "xid_94",
			DataCenterID: dataCenters[index], DriverVersion: "560", SliceContract: api.PredictionSliceContractVersion,
		})
	}
	audit := dualTrackSliceAudit(rows)
	slices := dualTrackValidationSlices(rows, RiskRankingSnapshotReport{Status: "shadow_snapshot_available"}, audit)
	if audit.Status != "ready" || len(slices) != 5 {
		t.Fatalf("four ready dimensions with two data centers should produce five slices: audit=%+v slices=%+v", audit, slices)
	}
	for _, slice := range slices {
		if slice.Dimension == "label_quality" {
			t.Fatalf("outcome-derived label quality must never become a predictive metric slice: %+v", slices)
		}
		if slice.NodeRankingAtK == nil || slice.ProbabilityMetrics.Status == "" {
			t.Fatalf("each predictive slice must expose non-null ranking and probability status: %+v", slice)
		}
	}
	ready, partial, comparableRanking, comparableProbability, blockers := validationSliceReadinessSummary(audit, slices)
	if ready != 4 || partial != 0 || comparableRanking != 0 || comparableProbability != 0 || len(blockers) != 0 {
		t.Fatalf("ready slice coverage must bind counts without pretending small slices are comparable: ready=%d partial=%d rank=%d prob=%d blockers=%v", ready, partial, comparableRanking, comparableProbability, blockers)
	}
	rows[0].DriverVersion = ""
	partialAudit := dualTrackSliceAudit(rows)
	partialSlices := dualTrackValidationSlices(rows, RiskRankingSnapshotReport{Status: "shadow_snapshot_available"}, partialAudit)
	for _, slice := range partialSlices {
		if slice.Dimension == "driver_version" {
			t.Fatalf("partially frozen dimensions must not emit comparison slices: %+v", partialSlices)
		}
	}
	_, partial, _, _, blockers = validationSliceReadinessSummary(partialAudit, partialSlices)
	if partial != 1 || len(blockers) == 0 {
		t.Fatalf("partial slice coverage must block readiness with a concrete reason: audit=%+v blockers=%v", partialAudit, blockers)
	}
}

func TestDualTrackTemporalConsistencyRequiresThreePositiveIndependentCohorts(t *testing.T) {
	lift, skill := 1.5, 0.2
	cohorts := make([]DualTrackTemporalCohort, 3)
	for index := range cohorts {
		cohorts[index] = DualTrackTemporalCohort{
			IndependentTimeBatch: true, MaturedRows: 30, PositiveRows: 3,
			NodeRankingAtK:     []RankingAtK{{K: 3, Lift: &lift}},
			ProbabilityMetrics: TemporalProbabilityMetrics{BrierSkillScore: &skill},
		}
	}
	consistency := dualTrackTemporalConsistency(cohorts)
	if consistency.Ranking.Status != "consistent" || consistency.Probability.Status != "consistent" || consistency.Ranking.PositiveDirectionCohorts != 3 || consistency.Probability.PositiveDirectionCohorts != 3 {
		t.Fatalf("three positive independent cohorts should pass direction consistency: %+v", consistency)
	}
	negativeLift, negativeSkill := 0.8, -0.1
	cohorts[0].NodeRankingAtK[0].Lift = &negativeLift
	cohorts[0].ProbabilityMetrics.BrierSkillScore = &negativeSkill
	mixed := dualTrackTemporalConsistency(cohorts)
	if mixed.Ranking.Status != "review_mixed_direction" || mixed.Probability.Status != "review_mixed_direction" {
		t.Fatalf("fewer than three positive cohorts must remain under review: %+v", mixed)
	}
	cohorts[0].IndependentTimeBatch = false
	insufficient := dualTrackTemporalConsistency(cohorts)
	if insufficient.Ranking.Status != "insufficient_independent_cohorts" || insufficient.Probability.Status != "insufficient_independent_cohorts" {
		t.Fatalf("overlapping cohorts must not satisfy the temporal gate: %+v", insufficient)
	}
}

func TestDualTrackValidationAlignsRankingAndProbabilityCohort(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 20, 0, 0, 0, time.UTC)
	service := NewService(db)
	service.now = func() time.Time { return now }
	empty, err := service.DualTrackValidationReport()
	if err != nil {
		t.Fatal(err)
	}
	if empty.Status != "blocked" || empty.Alignment.Status != "blocked_no_ranking_snapshot" || empty.ReportSHA256 == "" {
		t.Fatalf("empty dual-track report must be blocked and archived: %+v", empty)
	}

	threshold := 0.5
	spec := api.PredictionModelSpec{
		ModelKey: "gpu.failure.7d", Version: "model-v1", HardwareClass: "gpu", EntityType: "gpu",
		Task: "failure_probability", HorizonMinutes: 10080, Algorithm: "logistic_regression", Runtime: "go_native",
		Mode: "shadow", Status: "shadow_candidate", FeatureContractVersion: FeatureContractVersion,
		LabelContractVersion: LabelContractVersion, ScopeModelName: "H100", DecisionThreshold: &threshold,
	}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	finished := now.Add(-time.Hour)
	run := api.PredictionShadowScoringRun{
		RunKey: "shadow-run-dual", Version: "gpu-shadow-scoring-v3", Status: "completed",
		ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version, ScopeModelName: spec.ScopeModelName,
		TargetGPUCount: 4, ScoredGPUCount: 4, NoAlertEmitted: true, NoActionExecuted: true,
		StartedAt: finished.Add(-time.Hour), FinishedAt: &finished,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	cutoff := now.Add(-8 * 24 * time.Hour)
	probabilities := []float64{0.9, 0.7, 0.4, 0.2}
	actuals := []int{1, 0, 0, 0}
	for index := range probabilities {
		node := "10.0.0." + string(rune('1'+index))
		uuid := "GPU-" + string(rune('A'+index))
		prediction := api.HardwareRiskPrediction{
			ShadowRunID: run.ID, ModelSpecID: spec.ID, ModelVersion: spec.Version, HardwareClass: "gpu", EntityType: "gpu",
			EntityKey: uuid, GPUUUID: uuid, NodeIP: node, HorizonMinutes: spec.HorizonMinutes,
			Probability: &probabilities[index], Status: "shadow_scored", ObservedAt: cutoff, EvaluatedAt: cutoff,
		}
		if err := db.Create(&prediction).Error; err != nil {
			t.Fatal(err)
		}
		outcome := "tn"
		if actuals[index] == 1 {
			outcome = "tp"
		}
		actual := actuals[index]
		evaluation := api.PredictionOutcomeEvaluation{
			PredictionID: prediction.ID, ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version,
			EntityType: "gpu", EntityKey: uuid, GPUUUID: uuid, NodeIP: node, HorizonMinutes: spec.HorizonMinutes,
			Probability: &probabilities[index], DecisionThreshold: &threshold, PredictedPositive: probabilities[index] >= threshold,
			PredictionEvaluatedAt: cutoff, WindowStartAt: cutoff, WindowEndAt: cutoff.Add(7 * 24 * time.Hour),
			MaturityStatus: "matured", RuleActualValue: &actual, RuleOutcome: outcome, RuleDecisionVersion: OutcomeRuleVersion,
			FinalActualValue: &actual, FinalOutcome: outcome, FinalSource: "rule",
		}
		if err := db.Create(&evaluation).Error; err != nil {
			t.Fatal(err)
		}
	}
	seedHistoricalCohort := func(historicalCutoff time.Time, runKey, uuidPrefix string) {
		historicalFinished := historicalCutoff.Add(time.Hour)
		historicalRun := api.PredictionShadowScoringRun{
			RunKey: runKey, Version: "gpu-shadow-scoring-v3", Status: "completed",
			ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version, ScopeModelName: spec.ScopeModelName,
			TargetGPUCount: 4, ScoredGPUCount: 4, NoAlertEmitted: true, NoActionExecuted: true,
			StartedAt: historicalCutoff, FinishedAt: &historicalFinished,
		}
		if err := db.Create(&historicalRun).Error; err != nil {
			t.Fatal(err)
		}
		for index := range probabilities {
			node := "10.1.0." + string(rune('1'+index))
			uuid := uuidPrefix + string(rune('A'+index))
			prediction := api.HardwareRiskPrediction{
				ShadowRunID: historicalRun.ID, ModelSpecID: spec.ID, ModelVersion: spec.Version, HardwareClass: "gpu", EntityType: "gpu",
				EntityKey: uuid, GPUUUID: uuid, NodeIP: node, HorizonMinutes: spec.HorizonMinutes,
				Probability: &probabilities[index], Status: "shadow_scored", ObservedAt: historicalCutoff, EvaluatedAt: historicalCutoff,
			}
			if err := db.Create(&prediction).Error; err != nil {
				t.Fatal(err)
			}
			outcome := "tn"
			if actuals[index] == 1 {
				outcome = "tp"
			}
			actual := actuals[index]
			evaluation := api.PredictionOutcomeEvaluation{
				PredictionID: prediction.ID, ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version,
				EntityType: "gpu", EntityKey: uuid, GPUUUID: uuid, NodeIP: node, HorizonMinutes: spec.HorizonMinutes,
				Probability: &probabilities[index], DecisionThreshold: &threshold, PredictedPositive: probabilities[index] >= threshold,
				PredictionEvaluatedAt: historicalCutoff, WindowStartAt: historicalCutoff, WindowEndAt: historicalCutoff.Add(7 * 24 * time.Hour),
				MaturityStatus: "matured", RuleActualValue: &actual, RuleOutcome: outcome, RuleDecisionVersion: OutcomeRuleVersion,
				FinalActualValue: &actual, FinalOutcome: outcome, FinalSource: "rule",
			}
			if err := db.Create(&evaluation).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	seedHistoricalCohort(cutoff.Add(-24*time.Hour), "shadow-run-overlap", "GPU-O")
	seedHistoricalCohort(cutoff.Add(-8*24*time.Hour), "shadow-run-independent", "GPU-I")

	report, err := service.DualTrackValidationReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != DualTrackValidationReportVersion || report.Alignment.Status != "aligned" || report.Alignment.AlignedOutcomeRows != 4 || report.Alignment.SnapshotCutoffAt == nil || !report.Alignment.SnapshotCutoffAt.Equal(cutoff) {
		t.Fatalf("unexpected dual-track alignment: %+v", report)
	}
	if report.Status != "exploratory" || report.Ranking.Status != "exploratory" || report.Probability.Status != "exploratory" || report.Ranking.PositiveRows != 1 || report.Probability.Maturity.Matured != 4 || len(report.Ranking.Metrics) == 0 {
		t.Fatalf("tracks must remain independently exploratory on the aligned small cohort: %+v", report)
	}
	if report.Ranking.ScoreSemantics != "relative_priority_not_absolute_failure_probability" || report.ReportSHA256 == "" || report.Evidence.ReadinessSHA256 == "" {
		t.Fatalf("dual-track evidence binding is incomplete: %+v", report)
	}
	if report.TemporalSummary.CohortLimit != DualTrackTemporalCohortLimit || report.TemporalSummary.CohortCount != 3 || report.TemporalSummary.MaturedCohortCount != 3 || report.TemporalSummary.IndependentCohortCount != 2 || len(report.TemporalCohorts) != 3 {
		t.Fatalf("unexpected temporal cohort summary: %+v", report)
	}
	if !report.TemporalCohorts[0].PredictionCutoffAt.Equal(cutoff) || !report.TemporalCohorts[0].IndependentTimeBatch || report.TemporalCohorts[1].IndependentTimeBatch || !report.TemporalCohorts[2].IndependentTimeBatch {
		t.Fatalf("overlapping horizons must not count as independent time batches: %+v", report.TemporalCohorts)
	}
	if report.TemporalCohorts[0].ProbabilityMetrics.BrierScore == nil || report.TemporalCohorts[0].ProbabilityMetrics.NullBrierScore == nil || report.TemporalCohorts[0].ProbabilityMetrics.BrierSkillScore == nil || *report.TemporalCohorts[0].ProbabilityMetrics.BrierSkillScore <= 0 {
		t.Fatalf("mature probability cohorts must expose Brier skill against the null baseline: %+v", report.TemporalCohorts[0])
	}
	if report.TemporalCohorts[0].ProbabilityMetrics.ROCAUC == nil || *report.TemporalCohorts[0].ProbabilityMetrics.ROCAUC != 1 || report.TemporalCohorts[0].ProbabilityMetrics.PRAUCAveragePrecision == nil || *report.TemporalCohorts[0].ProbabilityMetrics.PRAUCAveragePrecision != 1 || report.TemporalCohorts[0].ProbabilityMetrics.ExpectedCalibrationError == nil || report.Probability.Quality.ROCAUC == nil {
		t.Fatalf("aligned cohorts must expose discrimination and calibration metrics: %+v", report.TemporalCohorts[0])
	}
	if report.TemporalConsistency.Ranking.Status != "insufficient_independent_cohorts" || report.TemporalConsistency.Probability.Status != "insufficient_independent_cohorts" || report.TemporalConsistency.Ranking.EvaluableIndependentCohorts != 2 || report.TemporalConsistency.Probability.EvaluableIndependentCohorts != 2 {
		t.Fatalf("two independent cohorts must not pass the three-cohort consistency gate: %+v", report.TemporalConsistency)
	}
	if report.Readiness.TemporalConsistencyVersion != DualTrackValidationReportVersion || report.Readiness.TemporalSummary.IndependentCohortCount != 2 || report.Readiness.TemporalConsistency.Ranking.Status != "insufficient_independent_cohorts" || report.Readiness.TemporalConsistency.Probability.Status != "insufficient_independent_cohorts" {
		t.Fatalf("validation readiness must consume temporal consistency gates: %+v", report.Readiness)
	}
	changedTemporal := report
	changedTemporal.TemporalSummary.IndependentCohortCount++
	if dualTrackValidationChecksum(report) == dualTrackValidationChecksum(changedTemporal) {
		t.Fatal("dual-track checksum should bind temporal cohort evidence")
	}
	service.now = func() time.Time { return now.Add(time.Hour) }
	later, err := service.DualTrackValidationReport()
	if err != nil || later.ReportSHA256 != report.ReportSHA256 || later.GeneratedAt.Equal(report.GeneratedAt) {
		t.Fatalf("dual-track checksum must ignore generated_at: before=%+v after=%+v err=%v", report, later, err)
	}
	response := httptest.NewRecorder()
	NewHandlerWithService(service).HandleDualTrackValidation(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/dual-track-validation?download=1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(DualTrackValidationReportVersion)) || response.Header().Get("Content-Disposition") == "" || response.Header().Get("ETag") == "" {
		t.Fatalf("dual-track validation download failed: %d %s", response.Code, response.Body.String())
	}
}

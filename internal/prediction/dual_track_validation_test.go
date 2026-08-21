package prediction

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

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

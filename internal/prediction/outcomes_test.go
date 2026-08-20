package prediction

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"atlas/internal/history"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestOutcomeReconciliationAndHumanOverride(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	threshold := 0.5
	spec := api.PredictionModelSpec{
		ModelKey: "gpu.failure.test", Version: "1.0.0", HardwareClass: "gpu",
		EntityType: "gpu", Task: "failure_probability", HorizonMinutes: 60,
		Algorithm: "test", Runtime: "test", Mode: "shadow", Status: "released",
		FeatureContractVersion: FeatureContractVersion, LabelContractVersion: LabelContractVersion,
		DecisionThreshold: &threshold, Current: true,
	}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	evaluatedAt := now.Add(-26 * time.Hour)
	expiresAt := evaluatedAt.Add(time.Hour)
	probabilities := []float64{0.9, 0.8, 0.2, 0.1}
	uuids := []string{"GPU-TP", "GPU-FP", "GPU-FN", "GPU-TN"}
	nodes := []string{"10.0.0.1", "10.0.0.2", "10.0.0.2", "10.0.0.3"}
	for index := range probabilities {
		prediction := api.HardwareRiskPrediction{
			ModelSpecID: spec.ID, HardwareClass: "gpu", EntityType: "gpu", EntityKey: uuids[index],
			GPUUUID: uuids[index], HorizonMinutes: 60, Probability: &probabilities[index],
			NodeIP: nodes[index], RiskLevel: "test", Status: "scored", EvaluatedAt: evaluatedAt, ExpiresAt: expiresAt,
		}
		if err := db.Create(&prediction).Error; err != nil {
			t.Fatal(err)
		}
	}
	for index, uuid := range []string{"GPU-TP", "GPU-FN"} {
		label := api.FailureLabel{
			LabelKey: "test-label-" + uuid, HardwareClass: "gpu", EntityType: "gpu",
			EntityKey: uuid, GPUUUID: uuid, EventType: "xid_critical", LabelValue: 1,
			QualityTier: "strong_proxy", SourceType: "test", SourceRecordID: uint(index + 1),
			LabelContractVersion: LabelContractVersion, OccurredAt: evaluatedAt.Add(30 * time.Minute),
			AvailableAt: evaluatedAt.Add(31 * time.Minute),
		}
		if err := db.Create(&label).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	if err := service.SyncOutcomes(); err != nil {
		t.Fatal(err)
	}
	summary, err := service.Accuracy()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Rule.TP != 1 || summary.Rule.FP != 1 || summary.Rule.FN != 1 || summary.Rule.TN != 1 || summary.Rule.Evaluated != 4 {
		t.Fatalf("unexpected rule confusion matrix: %+v", summary.Rule)
	}
	if summary.Rule.Precision == nil || *summary.Rule.Precision != 0.5 || summary.Rule.Recall == nil || *summary.Rule.Recall != 0.5 {
		t.Fatalf("unexpected rule metrics: %+v", summary.Rule)
	}
	assertRankingAtK(t, summary.Rule.RankingAtK, 1, 4, 2, 1, 1, 0.5, 1/(2.0/4.0))
	assertRankingAtK(t, summary.Rule.RankingAtK, 3, 4, 2, 2, 2.0/3.0, 1, (2.0/3.0)/(2.0/4.0))
	assertRankingAtK(t, summary.Rule.NodeRankingAtK, 3, 3, 2, 2, 2.0/3.0, 1, 1)
	var falsePositive api.PredictionOutcomeEvaluation
	if err := db.Where("gpu_uuid = ?", "GPU-FP").First(&falsePositive).Error; err != nil {
		t.Fatal(err)
	}
	overridden, err := service.OverrideOutcome(falsePositive.ID, OutcomeOverride{
		ActualValue: 1, Reason: "operator confirmed a board replacement", DecidedBy: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.RuleOutcome != "fp" || overridden.FinalOutcome != "tp" || overridden.FinalSource != "human_override" {
		t.Fatalf("human override lost rule provenance: %+v", overridden)
	}
	if err := service.SyncOutcomes(); err != nil {
		t.Fatal(err)
	}
	summary, err = service.Accuracy()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Rule.FP != 1 || summary.Final.TP != 2 || summary.Final.FP != 0 || summary.HumanOverrides != 1 {
		t.Fatalf("unexpected post-override metrics: %+v", summary)
	}
	assertRankingAtK(t, summary.Final.RankingAtK, 3, 4, 3, 3, 1, 1, 1/(3.0/4.0))
	assertRankingAtK(t, summary.Final.NodeRankingAtK, 3, 3, 2, 2, 2.0/3.0, 1, 1)
	report, err := service.OutcomeReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != "prediction-outcome-report-v1" || !report.Safety.ReadOnlyShadow || !report.Safety.NoActionTaken {
		t.Fatalf("unexpected report safety envelope: %+v", report)
	}
	if report.SampleMaturity.Total != 4 || report.SampleMaturity.Matured != 4 || report.SampleMaturity.NodeEligible != 3 {
		t.Fatalf("unexpected report maturity: %+v", report.SampleMaturity)
	}
	if len(report.Accuracy.Final.NodeRankingAtK) == 0 || len(report.Interpretation) == 0 || len(report.RecommendedNextRun) == 0 {
		t.Fatalf("report is missing review guidance: %+v", report)
	}
	if report.Stability.Status != "exploratory" || report.Stability.PositiveSamples != 3 || report.Stability.ProbabilityCoverage != 1 || report.Stability.RankingInterpretationStatus != "insufficient_matured_samples" || len(report.Stability.BlockingReasons) == 0 {
		t.Fatalf("unexpected outcome stability: %+v", report.Stability)
	}
	if report.Accuracy.Final.RankingAtK[0].Status != "insufficient_matured_samples" || report.Accuracy.Final.NodeRankingAtK[0].Status != "insufficient_matured_samples" {
		t.Fatalf("ranking status should explain small samples: %+v / %+v", report.Accuracy.Final.RankingAtK, report.Accuracy.Final.NodeRankingAtK)
	}
	if len(report.BaselineComparisons) != 2 {
		t.Fatalf("missing naive baseline comparisons: %+v", report.BaselineComparisons)
	}
	for _, baseline := range report.BaselineComparisons {
		if baseline.Name == "all_negative" && (baseline.Final.TP != 0 || baseline.Final.FN != 3 || baseline.Final.TN != 1 || baseline.Final.Evaluated != 4) {
			t.Fatalf("unexpected all-negative baseline: %+v", baseline.Final)
		}
		if baseline.Name == "all_positive" && (baseline.Final.TP != 3 || baseline.Final.FP != 1 || baseline.Final.TN != 0 || baseline.Final.Evaluated != 4) {
			t.Fatalf("unexpected all-positive baseline: %+v", baseline.Final)
		}
	}
}

func TestOutcomeCensoringAndHandlerValidation(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	spec := api.PredictionModelSpec{
		ModelKey: "gpu.failure.unreleased", Version: "1.0.0", HardwareClass: "gpu",
		EntityType: "gpu", Task: "failure_probability", HorizonMinutes: 60,
		Algorithm: "none", Runtime: "none", Mode: "shadow", Status: "data_readiness",
		FeatureContractVersion: FeatureContractVersion, LabelContractVersion: LabelContractVersion, Current: true,
	}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	prediction := api.HardwareRiskPrediction{
		ModelSpecID: spec.ID, HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-NOT-SCORED",
		GPUUUID: "GPU-NOT-SCORED", HorizonMinutes: 60, RiskLevel: "unavailable",
		Status: "not_scored", EvaluatedAt: time.Now().Add(-48 * time.Hour), ExpiresAt: time.Now().Add(-47 * time.Hour),
	}
	if err := db.Create(&prediction).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	if err := service.SyncOutcomes(); err != nil {
		t.Fatal(err)
	}
	summary, err := service.Accuracy()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Censored != 1 || summary.Rule.Evaluated != 0 || summary.Rule.Accuracy != nil {
		t.Fatalf("unscored prediction polluted accuracy: %+v", summary)
	}

	handler := NewHandlerWithService(service)
	body, _ := json.Marshal(OutcomeOverride{ActualValue: 1})
	response := httptest.NewRecorder()
	handler.HandleOutcome(response, httptest.NewRequest(http.MethodPatch, "/api/v1/prediction/outcomes/1", bytes.NewReader(body)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("override without reason must fail: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleOutcomeReport(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/outcome-report", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("outcome report failed: %d %s", response.Code, response.Body.String())
	}
	report, err := service.OutcomeReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Stability.Status != "blocked" || report.Stability.RankingInterpretationStatus != "no_scored_rows" || len(report.Stability.BlockingReasons) == 0 {
		t.Fatalf("censored-only report should be blocked: %+v", report.Stability)
	}
}

func TestModelGovernanceReportSummarizesDatasetModelAndGates(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	finished := now.Add(-time.Hour)
	dataset := api.TrainingDatasetBuild{
		DatasetKey: "cohort-1", Version: "dataset-v1", Status: "completed", SourceKey: "prometheus-main",
		Horizons: api.StringList{"60m", "360m"}, CandidateCount: 10, EligibleCandidateCount: 6,
		EpisodeCount: 5, WindowCount: 12, PendingReviewCount: 1, IdentityMissingCount: 2,
		ContextOnlyCount: 1, ExcludedCount: 1, StartedAt: now.Add(-2 * time.Hour), FinishedAt: &finished,
	}
	if err := db.Create(&dataset).Error; err != nil {
		t.Fatal(err)
	}
	feature := api.TrainingFeatureBuild{
		FeatureDatasetKey: "features-1", Version: "features-v1", Status: "completed", SourceKey: "prometheus-main",
		SourceDatasetBuildID: dataset.ID, SourceDatasetKey: dataset.DatasetKey, FeatureContractVersion: FeatureContractVersion,
		FeatureColumnCount: 8, AverageMetricCoverage: 0.92, MinimumMetricCoverage: 0.81, StartedAt: now.Add(-90 * time.Minute), FinishedAt: &finished,
	}
	if err := db.Create(&feature).Error; err != nil {
		t.Fatal(err)
	}
	prep := api.TrainingPreparationBuild{
		PreparedDatasetKey: "prepared-1", Version: "prep-v1", Status: "completed", SourceFeatureBuildID: feature.ID,
		SourceFeatureDatasetKey: feature.FeatureDatasetKey, EligiblePositiveCount: 4, TelemetryCensoredCount: 1,
		LowCoverageCount: 2, ExtractionFailedCount: 0, PositiveDiscontinuousCount: 1, LabelIneligibleCount: 1,
		CorrelatedEventCount: 1, EntityTimeConflictCount: 0, TrainCount: 6, ValidationCount: 2, TestCount: 2,
		StartedAt: now.Add(-80 * time.Minute), FinishedAt: &finished,
	}
	if err := db.Create(&prep).Error; err != nil {
		t.Fatal(err)
	}
	matrix := api.TrainingMatrixBuild{
		TrainingMatrixKey: "matrix-1", Version: "matrix-v1", Status: "completed", SourcePreparationBuildID: prep.ID,
		SourcePreparedDatasetKey: prep.PreparedDatasetKey, SourceControlBuildID: 7, SourceControlDatasetKey: "control-1",
		FeatureContractVersion: FeatureContractVersion, FeatureColumnCount: 8, SampleCount: 10, PositiveCount: 4, ControlCount: 6,
		TrainPositiveCount: 3, TrainControlCount: 3, ValidationPositiveCount: 1, ValidationControlCount: 1,
		TestPositiveCount: 1, TestControlCount: 1, MatrixSHA256: "sha-matrix", StartedAt: now.Add(-70 * time.Minute), FinishedAt: &finished,
	}
	if err := db.Create(&matrix).Error; err != nil {
		t.Fatal(err)
	}
	baseline := api.BaselineModelBuild{
		BaselineModelKey: "baseline-1", Version: "baseline-v1", Status: "completed", Algorithm: "logistic_regression",
		SourceMatrixBuildID: matrix.ID, SourceTrainingMatrixKey: matrix.TrainingMatrixKey, FeatureContractVersion: FeatureContractVersion,
		FeatureAuditStatus: "passed", StatisticallyStableCount: 1, ShadowCandidateCount: 1, TrainCount: 6, ValidationCount: 2,
		TestCount: 2, TestMacroROCAUC: 0.8, TestMacroPRAUC: 0.7, TestMacroPrecision: 0.6, TestMacroRecall: 0.5,
		ArtifactSHA256: "sha-model", StartedAt: now.Add(-60 * time.Minute), FinishedAt: &finished,
	}
	if err := db.Create(&baseline).Error; err != nil {
		t.Fatal(err)
	}
	threshold := 0.5
	spec := api.PredictionModelSpec{
		ModelKey: "gpu.failure.test", Version: "baseline-v1.build-1", HardwareClass: "gpu", EntityType: "gpu",
		Task: "failure_probability", HorizonMinutes: 60, Algorithm: "logistic_regression", Runtime: "atlas-go",
		Mode: "shadow", Status: "released", FeatureContractVersion: FeatureContractVersion, LabelContractVersion: LabelContractVersion,
		DatasetVersion: matrix.Version, SourceBaselineBuildID: baseline.ID, ArtifactSHA256: baseline.ArtifactSHA256,
		RegistryGateVersion: "gate-v1", DecisionThreshold: &threshold, Current: true,
	}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.PredictionFeatureParityAudit{
		ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version, SourceBaselineBuildID: baseline.ID,
		ArtifactSHA256: spec.ArtifactSHA256, FeatureContractVersion: FeatureContractVersion, TransformationContractVersion: "transform-v1",
		Status: "passed", TrainingFeatureCount: 8, ContractMatchedCount: 8, AuditedAt: now.Add(-50 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.PredictionFeatureReplayRun{
		ReplayKey: "replay-1", Version: "replay-v1", Status: "passed", ModelSpecID: spec.ID, ModelKey: spec.ModelKey,
		ModelVersion: spec.Version, SourceBaselineBuildID: baseline.ID, SourceMatrixBuildID: matrix.ID, SourceKey: "prometheus-main",
		TransformationContractVersion: "transform-v1", VerifiedColumnCount: 8, ComparedValueCount: 80, StartedAt: now.Add(-40 * time.Minute),
		FinishedAt: &finished,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.PredictionLiveCoverageAudit{
		AuditKey: "coverage-1", Version: "coverage-v1", Status: "passed", ModelSpecID: spec.ID, ModelKey: spec.ModelKey,
		ModelVersion: spec.Version, SourceKey: "prometheus-main", ScopeModelName: "NVIDIA H100", EligibleRatio: 0.95,
		StartedAt: now.Add(-30 * time.Minute), FinishedAt: &finished,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.PredictionShadowScoringRun{
		RunKey: "shadow-1", Version: "shadow-v1", Status: "completed", Trigger: "manual", ModelSpecID: spec.ID,
		ModelKey: spec.ModelKey, ModelVersion: spec.Version, ArtifactSHA256: spec.ArtifactSHA256, SourceKey: "prometheus-main",
		ScopeModelName: "NVIDIA H100", TransformationVersion: "transform-v1", ScoredGPUCount: 10, PositiveGPUCount: 2,
		PositiveRatio: 0.2, DistributionStatus: "passed", NoAlertEmitted: true, NoActionExecuted: true,
		StartedAt: now.Add(-20 * time.Minute), FinishedAt: &finished,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	report, err := service.ModelGovernanceReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != ModelGovernanceReportVersion || report.Dataset.MatrixKey != "matrix-1" || report.Dataset.MatrixSampleCount != 10 {
		t.Fatalf("unexpected dataset card: %+v", report.Dataset)
	}
	if len(report.Models) != 1 || report.Models[0].FeatureAuditStatus != "passed" || report.Models[0].TestMacroROCAUC != 0.8 {
		t.Fatalf("unexpected model card: %+v", report.Models)
	}
	if report.ShadowGates.FeatureParityStatus != "passed" || report.ShadowGates.ShadowDistributionStatus != "passed" || !report.ShadowGates.NoActionExecuted {
		t.Fatalf("unexpected shadow gates: %+v", report.ShadowGates)
	}
	if report.Outcome.Version != "prediction-outcome-report-v1" || len(report.Limitations) == 0 || len(report.RecommendedNextRun) == 0 {
		t.Fatalf("governance report missed safety guidance: %+v", report)
	}
	handler := NewHandlerWithService(service)
	response := httptest.NewRecorder()
	handler.HandleModelGovernance(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/model-governance", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(ModelGovernanceReportVersion)) {
		t.Fatalf("model governance handler failed: %d %s", response.Code, response.Body.String())
	}
}

func TestDataDriftReportComparesShadowScoreDistributions(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	baselineFinished := now.Add(-2 * time.Hour)
	latestFinished := now.Add(-time.Hour)
	coverageBaselineFinished := now.Add(-3 * time.Hour)
	coverageLatestFinished := now.Add(-30 * time.Minute)
	baselineMedian, baselineP90, baselineP95, baselineP99 := 0.10, 0.20, 0.25, 0.30
	latestMedian, latestP90, latestP95, latestP99 := 0.12, 0.23, 0.28, 0.34
	runs := []api.PredictionShadowScoringRun{
		{
			RunKey: "shadow-baseline", Version: "shadow-v1", Status: "completed", Trigger: "manual",
			ModelSpecID: 1, ModelKey: "gpu.failure.test", ModelVersion: "model-v1", ArtifactSHA256: "sha",
			SourceKey: "prometheus", ScopeModelName: "H100", TransformationVersion: "transform-v1",
			ScoredGPUCount: 100, PositiveGPUCount: 8, PositiveRatio: 0.08, MedianProbability: &baselineMedian,
			P90Probability: &baselineP90, P95Probability: &baselineP95, P99Probability: &baselineP99,
			DistributionStatus: "passed", NoAlertEmitted: true, NoActionExecuted: true,
			ReportSHA256: "baseline-report", StartedAt: baselineFinished.Add(-time.Minute), FinishedAt: &baselineFinished,
		},
		{
			RunKey: "shadow-latest", Version: "shadow-v1", Status: "completed", Trigger: "manual",
			ModelSpecID: 1, ModelKey: "gpu.failure.test", ModelVersion: "model-v1", ArtifactSHA256: "sha",
			SourceKey: "prometheus", ScopeModelName: "H100", TransformationVersion: "transform-v1",
			ScoredGPUCount: 100, PositiveGPUCount: 10, PositiveRatio: 0.10, MedianProbability: &latestMedian,
			P90Probability: &latestP90, P95Probability: &latestP95, P99Probability: &latestP99,
			DistributionStatus: "passed", NoAlertEmitted: true, NoActionExecuted: true,
			ReportSHA256: "latest-report", StartedAt: latestFinished.Add(-time.Minute), FinishedAt: &latestFinished,
		},
	}
	for index := range runs {
		if err := db.Create(&runs[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	coverageAudits := []api.PredictionLiveCoverageAudit{
		{
			AuditKey: "coverage-baseline", Version: "coverage-v1", Status: "passed", ModelSpecID: 1,
			ModelKey: "gpu.failure.test", ModelVersion: "model-v1", SourceKey: "prometheus", ScopeModelName: "H100",
			TargetGPUCount: 10, EligibleGPUCount: 9, MetricPairCount: 100, PassingMetricPairCount: 90,
			MissingMetricPairCount: 4, SparseMetricPairCount: 4, StaleMetricPairCount: 2, EligibleRatio: 0.90,
			ReportSHA256: "coverage-baseline-report", StartedAt: coverageBaselineFinished.Add(-time.Minute), FinishedAt: &coverageBaselineFinished,
		},
		{
			AuditKey: "coverage-latest", Version: "coverage-v1", Status: "passed", ModelSpecID: 1,
			ModelKey: "gpu.failure.test", ModelVersion: "model-v1", SourceKey: "prometheus", ScopeModelName: "H100",
			TargetGPUCount: 10, EligibleGPUCount: 9, MetricPairCount: 100, PassingMetricPairCount: 88,
			MissingMetricPairCount: 5, SparseMetricPairCount: 5, StaleMetricPairCount: 2, EligibleRatio: 0.90,
			ReportSHA256: "coverage-latest-report", StartedAt: coverageLatestFinished.Add(-time.Minute), FinishedAt: &coverageLatestFinished,
		},
	}
	for index := range coverageAudits {
		if err := db.Create(&coverageAudits[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	report, err := service.DataDriftReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != DataDriftReportVersion || report.Status != "passed" || report.CoverageQualityStatus != "passed" || report.ReportSHA256 == "" || report.Latest == nil || report.Baseline == nil || report.LatestCoverage == nil || report.BaselineCoverage == nil {
		t.Fatalf("unexpected data drift report: %+v", report)
	}
	if report.Latest.RunKey != "shadow-latest" || report.Baseline.RunKey != "shadow-baseline" || math.Abs(report.PositiveRatioDelta-0.02) > 1e-12 || report.KSProxy <= 0 || report.PSIProxy <= 0 {
		t.Fatalf("unexpected drift metrics: %+v", report)
	}
	if report.LatestCoverage.AuditKey != "coverage-latest" || report.BaselineCoverage.AuditKey != "coverage-baseline" || math.Abs(report.MetricPassRatioDelta-0.02) > 1e-12 {
		t.Fatalf("unexpected coverage drift metrics: %+v", report)
	}
	later := now.Add(time.Hour)
	service.now = func() time.Time { return later }
	laterReport, err := service.DataDriftReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.ReportSHA256 != laterReport.ReportSHA256 || report.GeneratedAt.Equal(laterReport.GeneratedAt) {
		t.Fatalf("data drift checksum should be stable across generated_at changes: before=%+v after=%+v", report, laterReport)
	}
	handler := NewHandlerWithService(service)
	response := httptest.NewRecorder()
	handler.HandleDataDriftReport(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/data-drift-report?download=1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(DataDriftReportVersion)) || response.Header().Get("Content-Disposition") == "" || response.Header().Get("ETag") == "" {
		t.Fatalf("data drift handler failed: %d %s", response.Code, response.Body.String())
	}
}

func TestCalibrationDriftReportComparesBaselineCalibration(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 11, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	baselinePath := dir + "/baseline.json"
	latestPath := dir + "/latest.json"
	if err := os.WriteFile(baselinePath, []byte(`{"horizons":[{"release_readiness":"shadow_candidate","test_calibration":{"status":"passed","ece":0.040,"model_brier":0.120,"null_brier":0.200,"brier_skill_score":0.400}},{"release_readiness":"shadow_candidate","test_calibration":{"status":"passed","ece":0.060,"model_brier":0.140,"null_brier":0.220,"brier_skill_score":0.360}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(latestPath, []byte(`{"horizons":[{"release_readiness":"shadow_candidate","test_calibration":{"status":"passed","ece":0.050,"model_brier":0.130,"null_brier":0.200,"brier_skill_score":0.350}},{"release_readiness":"shadow_candidate","test_calibration":{"status":"passed","ece":0.070,"model_brier":0.150,"null_brier":0.220,"brier_skill_score":0.340}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	baselineFinished := now.Add(-2 * time.Hour)
	latestFinished := now.Add(-time.Hour)
	builds := []api.BaselineModelBuild{
		{
			BaselineModelKey: "baseline-calibration", Version: "baseline-v1", Status: "completed", Algorithm: "logistic_regression",
			SourceMatrixBuildID: 1, SourceTrainingMatrixKey: "matrix", FeatureContractVersion: FeatureContractVersion,
			ReportPath: baselinePath, StartedAt: baselineFinished.Add(-time.Minute), FinishedAt: &baselineFinished,
		},
		{
			BaselineModelKey: "latest-calibration", Version: "baseline-v2", Status: "completed", Algorithm: "logistic_regression",
			SourceMatrixBuildID: 1, SourceTrainingMatrixKey: "matrix", FeatureContractVersion: FeatureContractVersion,
			ReportPath: latestPath, StartedAt: latestFinished.Add(-time.Minute), FinishedAt: &latestFinished,
		},
	}
	for index := range builds {
		if err := db.Create(&builds[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	report, err := service.CalibrationDriftReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != CalibrationDriftReportVersion || report.Status != "passed" || report.ReportSHA256 == "" || report.Latest == nil || report.Baseline == nil {
		t.Fatalf("unexpected calibration drift report: %+v", report)
	}
	if report.Latest.BaselineModelKey != "latest-calibration" || report.Baseline.BaselineModelKey != "baseline-calibration" || math.Abs(report.ECEDelta-0.01) > 1e-12 || report.BrierSkillScoreDelta >= 0 {
		t.Fatalf("unexpected calibration drift metrics: %+v", report)
	}
	later := now.Add(time.Hour)
	service.now = func() time.Time { return later }
	laterReport, err := service.CalibrationDriftReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.ReportSHA256 != laterReport.ReportSHA256 || report.GeneratedAt.Equal(laterReport.GeneratedAt) {
		t.Fatalf("calibration drift checksum should be stable across generated_at changes: before=%+v after=%+v", report, laterReport)
	}
	handler := NewHandlerWithService(service)
	response := httptest.NewRecorder()
	handler.HandleCalibrationDriftReport(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/calibration-drift-report?download=1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(CalibrationDriftReportVersion)) || response.Header().Get("Content-Disposition") == "" || response.Header().Get("ETag") == "" {
		t.Fatalf("calibration drift handler failed: %d %s", response.Code, response.Body.String())
	}
}

func TestHeaRankChallengerReportUsesSevenDayNodeOutcomes(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	empty, err := service.HeaRankChallengerReport()
	if err != nil {
		t.Fatal(err)
	}
	if empty.Status != "blocked_no_7d_mature_outcomes" || empty.ConfidenceStatus != "insufficient_sample" || len(empty.BlockingReasons) == 0 {
		t.Fatalf("empty challenger should be blocked: %+v", empty)
	}
	probabilities := []float64{0.9, 0.7, 0.2, 0.1}
	actuals := []int{1, 0, 1, 0}
	nodes := []string{"10.0.0.1", "10.0.0.2", "10.0.0.2", "10.0.0.3"}
	for index := range probabilities {
		predicted := probabilities[index] >= 0.5
		row := api.PredictionOutcomeEvaluation{
			PredictionID: uint(index + 1), ModelSpecID: 1, ModelKey: "gpu.failure.7d", ModelVersion: "test",
			EntityType: "gpu", EntityKey: nodes[index], GPUUUID: "GPU-7D", NodeIP: nodes[index], HorizonMinutes: 10080,
			Probability: &probabilities[index], DecisionThreshold: &probabilities[1], PredictedPositive: predicted,
			PredictionEvaluatedAt: now.Add(time.Duration(index-10) * time.Hour), WindowStartAt: now.Add(time.Duration(index-10) * time.Hour),
			WindowEndAt: now.Add(time.Duration(index) * time.Hour), MaturityStatus: "matured", RuleOutcome: classify(predicted, actuals[index]),
			RuleActualValue: &actuals[index], FinalOutcome: classify(predicted, actuals[index]), FinalActualValue: &actuals[index],
			FinalSource: "rule", RuleDecisionVersion: OutcomeRuleVersion,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&api.FailureLabel{
		LabelKey: "challenger-history-label", HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-7D", GPUUUID: "GPU-7D", NodeIP: "10.0.0.1", ModelName: "H100",
		EventType: "row_remap_failure", RuleVersion: "rule-v1", LabelValue: 1, QualityTier: "strong_proxy", SourceType: "test", SourceRecordID: 1,
		LabelContractVersion: LabelContractVersion, OccurredAt: now.Add(-12 * time.Hour), AvailableAt: now.Add(-12 * time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	report, err := service.HeaRankChallengerReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "blocked_insufficient_7d_sample" || report.ConfidenceStatus != "insufficient_sample" || len(report.SevenDay) != 7 || len(report.PolicyComparisons) != 6 || report.PolicyComparisons[0].Status != "blocked_insufficient_sample" || report.SevenDay[0].Policy != "logistic_probability" || report.SevenDay[1].Policy != "health_score_risk_prior" || report.SevenDay[2].Policy != "rule_hit_risk_prior" || report.SevenDay[4].Policy != "recency_weighted_failure_prior" || report.SevenDay[5].Policy != "severity_weighted_label_history" {
		t.Fatalf("unexpected challenger report: %+v", report)
	}
	if report.SevenDay[0].Rows != 4 || report.SevenDay[0].Nodes != 3 || report.SevenDay[0].Positives != 2 || len(report.SevenDay[0].RankingAtK) == 0 {
		t.Fatalf("unexpected logistic challenger metrics: %+v", report.SevenDay[0])
	}
	if report.SevenDay[5].NonZeroScoreRows != 1 || report.SevenDay[5].NonZeroScoreNodes != 1 || report.SevenDay[5].SignalCoverageStatus != "exploratory" {
		t.Fatalf("severity challenger must expose non-zero history-signal coverage: %+v", report.SevenDay[5])
	}
	if report.MinimumSevenDayRows != HeaRankMinimumSevenDayRows || report.MinimumSevenDayNodes != HeaRankMinimumSevenDayNodes || report.MinimumSevenDayPositives != HeaRankMinimumSevenDayPositives {
		t.Fatalf("unexpected challenger gates: %+v", report)
	}
	handler := NewHandlerWithService(service)
	response := httptest.NewRecorder()
	handler.HandleHeaRankChallenger(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/hearank-challenger", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(HeaRankChallengerReportVersion)) {
		t.Fatalf("challenger handler failed: %d %s", response.Code, response.Body.String())
	}
}

func TestHeaRankHistoricalRiskUsesOnlyClosedOutcomeWindows(t *testing.T) {
	cutoff := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	positive := 1
	rows := []api.PredictionOutcomeEvaluation{
		{NodeIP: "10.0.0.1", MaturityStatus: "matured", FinalActualValue: &positive, WindowEndAt: cutoff.Add(-90 * 24 * time.Hour)},
		{NodeIP: "10.0.0.1", MaturityStatus: "matured", FinalActualValue: &positive, WindowEndAt: cutoff.Add(time.Hour)},
		{NodeIP: "10.0.0.2", MaturityStatus: "matured", FinalActualValue: &positive, WindowEndAt: cutoff.Add(-45 * 24 * time.Hour)},
		{NodeIP: "10.0.0.3", MaturityStatus: "matured", FinalActualValue: &positive, WindowEndAt: cutoff},
	}
	history := challengerHistoryBefore(rows, cutoff)
	if history.PositiveCounts["10.0.0.1"] != 1 || history.PositiveCounts["10.0.0.2"] != 1 || len(history.PositiveCounts) != 2 {
		t.Fatalf("history must exclude outcomes whose window has not closed strictly before the cutoff: %+v", history)
	}
	if math.Abs(history.RecencyWeighted["10.0.0.1"]-0.5) > 1e-12 || math.Abs(history.RecencyWeighted["10.0.0.2"]-math.Sqrt(0.5)) > 1e-12 {
		t.Fatalf("unexpected recency-weighted history: %+v", history)
	}
}

func TestHeaRankSeverityWeightedLabelHistoryUsesOnlyAvailableEligibleLabels(t *testing.T) {
	cutoff := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	labels := []api.FailureLabel{
		{NodeIP: "10.0.0.1", EventType: "row_remap_failure", LabelValue: 1, QualityTier: "strong_proxy", OccurredAt: cutoff.Add(-2 * time.Hour), AvailableAt: cutoff.Add(-time.Hour)},
		{NodeIP: "10.0.0.1", EventType: "gpu_temp_sustained_5m_critical", LabelValue: 1, QualityTier: "confirmed", OccurredAt: cutoff.Add(-2 * time.Hour), AvailableAt: cutoff.Add(-time.Hour)},
		{NodeIP: "10.0.0.1", EventType: "recent_uncorrected_ecc", LabelValue: 1, QualityTier: "strong_proxy", OccurredAt: cutoff.Add(-time.Hour), AvailableAt: cutoff.Add(time.Hour)},
		{NodeIP: "10.0.0.2", EventType: "xid_repeated", LabelValue: 1, QualityTier: "weak_proxy", OccurredAt: cutoff.Add(-time.Hour), AvailableAt: cutoff.Add(-time.Hour)},
		{NodeIP: "10.0.0.3", EventType: "xid_repeated", LabelValue: 1, QualityTier: "strong_proxy", Excluded: true, OccurredAt: cutoff.Add(-time.Hour), AvailableAt: cutoff.Add(-time.Hour)},
		{NodeIP: "10.0.0.4", EventType: "xid_repeated", LabelValue: 1, QualityTier: "strong_proxy", OccurredAt: cutoff.Add(-time.Hour), AvailableAt: cutoff},
		{NodeIP: "10.0.0.5", EventType: "xid_repeated", LabelValue: 1, QualityTier: "strong_proxy", OccurredAt: cutoff, AvailableAt: cutoff.Add(-time.Hour)},
	}
	history := challengerHistoryWithLabelsBefore(nil, labels, cutoff)
	if history.SeverityWeightedLabels["10.0.0.1"] != 5 || len(history.SeverityWeightedLabels) != 1 {
		t.Fatalf("severity history must use only labels available at the cutoff and eligible for validation: %+v", history)
	}
}

func TestHeaRankHealthRiskUsesLatestScoreStrictlyBeforeCutoff(t *testing.T) {
	cutoff := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	low := 80
	lower := 60
	equalCutoff := 10
	scores := []api.GPUHealthScore{
		{ID: 1, NodeIP: "10.0.0.1", GPUUUID: "GPU-A", Score: &low, EvaluatedAt: cutoff.Add(-2 * time.Hour)},
		{ID: 2, NodeIP: "10.0.0.1", GPUUUID: "GPU-A", Score: &lower, EvaluatedAt: cutoff.Add(-time.Hour)},
		{ID: 3, NodeIP: "10.0.0.1", GPUUUID: "GPU-B", Score: &equalCutoff, EvaluatedAt: cutoff},
	}
	risk := healthRiskByNodeBefore(scores, cutoff)
	if risk["10.0.0.1"] != 40 || len(risk) != 1 {
		t.Fatalf("health risk must use each GPU's latest score strictly before the cutoff: %+v", risk)
	}
}

func TestHeaRankRuleHitRiskUsesLatestBatchStrictlyBeforeCutoff(t *testing.T) {
	cutoff := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	scores := []api.GPUHealthScore{
		{ID: 1, NodeIP: "10.0.0.1", GPUUUID: "GPU-A"},
		{ID: 2, NodeIP: "10.0.0.1", GPUUUID: "GPU-B"},
	}
	hits := []api.GPUHealthRuleHit{
		{HealthScoreID: 1, GPUUUID: "GPU-A", Severity: "critical", EvaluatedAt: cutoff.Add(-2 * time.Hour)},
		{HealthScoreID: 1, GPUUUID: "GPU-A", Severity: "warning", EvaluatedAt: cutoff.Add(-time.Hour)},
		{HealthScoreID: 1, GPUUUID: "GPU-A", Severity: "attention", EvaluatedAt: cutoff.Add(-time.Hour)},
		{HealthScoreID: 2, GPUUUID: "GPU-B", Severity: "critical", EvaluatedAt: cutoff},
	}
	risk := ruleHitRiskByNodeBefore(hits, scores, cutoff)
	if risk["10.0.0.1"] != 3 || len(risk) != 1 {
		t.Fatalf("rule-hit risk must use each GPU's latest batch strictly before the cutoff: %+v", risk)
	}
}

func TestHeaRankSignalCoverageStatus(t *testing.T) {
	if challengerSignalCoverageStatus(0, 0) != "no_signal" || challengerSignalCoverageStatus(2, 2) != "exploratory" || challengerSignalCoverageStatus(3, 1) != "exploratory" || challengerSignalCoverageStatus(3, 2) != "covered" {
		t.Fatalf("unexpected challenger signal coverage statuses")
	}
}

func TestHeaRankPolicyComparisonsRequireSignalAndSampleGates(t *testing.T) {
	rows := []ChallengerMetricSet{
		{Policy: "logistic_probability", SignalCoverageStatus: "covered"},
		{Policy: "failure_count_prior", SignalCoverageStatus: "no_signal"},
		{Policy: "threshold_binary", SignalCoverageStatus: "covered"},
	}
	comparisons := challengerPolicyComparisons(rows, "exploratory")
	if len(comparisons) != 2 || comparisons[0].Status != "blocked_challenger_signal" || comparisons[1].Status != "exploratory" {
		t.Fatalf("unexpected signal-gated comparisons: %+v", comparisons)
	}
	if comparisons = challengerPolicyComparisons(rows, "insufficient_sample"); comparisons[0].Status != "blocked_insufficient_sample" {
		t.Fatalf("sample gate must take priority: %+v", comparisons)
	}
}

func TestLabelManifestSummarizesGovernancePolicies(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 14, 0, 0, 0, time.UTC)
	confirmedAt := now.Add(-time.Hour)
	labels := []api.FailureLabel{
		{LabelKey: "confirmed", HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-1", GPUUUID: "GPU-1", ModelName: "H100", EventType: "xid_94", RuleVersion: "rule-v1", LabelValue: 1, QualityTier: "confirmed", SourceType: "human_resolution", SourceRecordID: 1, ConfirmationResolutionID: 7, LabelContractVersion: LabelContractVersion, OccurredAt: now.Add(-2 * time.Hour), AvailableAt: now.Add(-time.Hour), ConfirmedAt: &confirmedAt},
		{LabelKey: "strong", HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-2", GPUUUID: "GPU-2", ModelName: "H100", EventType: "xid_94", RuleVersion: "rule-v1", LabelValue: 1, QualityTier: "strong_proxy", SourceType: "rule", SourceRecordID: 2, LabelContractVersion: LabelContractVersion, OccurredAt: now.Add(-2 * time.Hour), AvailableAt: now.Add(-time.Hour)},
		{LabelKey: "excluded", HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-3", GPUUUID: "GPU-3", ModelName: "H100", EventType: "legacy", RuleVersion: "legacy-rule", LabelValue: 1, QualityTier: "weak_proxy", SourceType: "rule", SourceRecordID: 3, LabelContractVersion: LabelContractVersion, Excluded: true, ExclusionReason: "legacy lifetime counter", OccurredAt: now.Add(-2 * time.Hour), AvailableAt: now.Add(-time.Hour)},
	}
	for _, label := range labels {
		if err := db.Create(&label).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	manifest, err := service.LabelManifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != LabelManifestVersion || manifest.Total != 3 || manifest.Positive != 2 || manifest.Excluded != 1 || manifest.HumanConfirmed != 1 || manifest.ManifestSHA256 == "" {
		t.Fatalf("unexpected label manifest counts: %+v", manifest)
	}
	if manifest.ByEventType["xid_94"] != 2 || manifest.BySourceType["rule"] != 1 || manifest.ExclusionReasons["legacy lifetime counter"] != 1 || len(manifest.KnownGaps) == 0 || len(manifest.SampleLabelKeys) != 3 {
		t.Fatalf("unexpected label manifest breakdown: %+v", manifest)
	}
	if manifest.QualityGateStatus != "exploratory_ready" || len(manifest.BlockingReasons) != 0 || len(manifest.RecommendedNextRun) == 0 {
		t.Fatalf("unexpected label manifest gate: %+v", manifest)
	}
	later := now.Add(time.Hour)
	service.now = func() time.Time { return later }
	laterManifest, err := service.LabelManifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ManifestSHA256 != laterManifest.ManifestSHA256 || manifest.GeneratedAt.Equal(laterManifest.GeneratedAt) {
		t.Fatalf("manifest checksum should be stable across generated_at changes: before=%+v after=%+v", manifest, laterManifest)
	}
	handler := NewHandlerWithService(service)
	response := httptest.NewRecorder()
	handler.HandleLabelManifest(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/label-manifest?download=1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(LabelManifestVersion)) || response.Header().Get("Content-Disposition") == "" || response.Header().Get("ETag") == "" {
		t.Fatalf("label manifest handler failed: %d %s", response.Code, response.Body.String())
	}
}

func TestLabelManifestBlocksWeakOnlyLabels(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	for _, key := range []string{"weak-1", "weak-2"} {
		label := api.FailureLabel{
			LabelKey: key, HardwareClass: "gpu", EntityType: "gpu", EntityKey: key, GPUUUID: key,
			ModelName: "H100", EventType: "operational_only", RuleVersion: "rule-v1", LabelValue: 1,
			QualityTier: "weak_proxy", SourceType: "rule", SourceRecordID: 1, LabelContractVersion: LabelContractVersion,
			OccurredAt: now.Add(-2 * time.Hour), AvailableAt: now.Add(-time.Hour),
		}
		if err := db.Create(&label).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	manifest, err := service.LabelManifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.QualityGateStatus != "blocked" || len(manifest.BlockingReasons) == 0 || manifest.ManifestSHA256 == "" {
		t.Fatalf("weak-only labels should block validation: %+v", manifest)
	}
}

func TestFeatureDriftReportRequiresPersistedDistributions(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 15, 30, 0, 0, time.UTC)
	finished := now.Add(-time.Hour)
	artifactPath := t.TempDir() + "/baseline-artifact.json"
	artifactJSON := []byte(`{"version":"artifact-v1","models":[{"horizon_minutes":60,"feature_columns":["temperature","power","ecc_delta"],"means":[42.0,230.0,0.1],"scales":[5.0,40.0,1.0]}]}`)
	if err := os.WriteFile(artifactPath, artifactJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	artifactSHA, err := localFileSHA256(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	baseline := api.BaselineModelBuild{
		BaselineModelKey: "baseline-1", Version: "baseline-v1", Status: "completed", Algorithm: "logistic_regression",
		SourceMatrixBuildID: 1, SourceTrainingMatrixKey: "matrix-1", FeatureContractVersion: FeatureContractVersion,
		FeatureColumnCount: 3, HorizonCount: 1, TrainedModelCount: 1, ArtifactPath: artifactPath, ArtifactSHA256: artifactSHA,
		StartedAt: now.Add(-2 * time.Hour), FinishedAt: &finished,
	}
	if err := db.Create(&baseline).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.PredictionFeatureReplayRun{
		ReplayKey: "replay-1", Version: "replay-v1", Status: "passed", ModelSpecID: 1, ModelKey: "gpu.failure.test",
		ModelVersion: "baseline-v1", SourceBaselineBuildID: baseline.ID, SourceMatrixBuildID: 1, SourceKey: "prometheus-main",
		TransformationContractVersion: "transform-v1", TrainingFeatureCount: 3, VerifiedColumnCount: 3, ComparedValueCount: 30,
		ReportSHA256: "replay-sha", StartedAt: now.Add(-45 * time.Minute), FinishedAt: &finished,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	report, err := service.FeatureDriftReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != FeatureDriftReportVersion || report.Status != "blocked_feature_distribution_store_required" || report.ReportSHA256 == "" {
		t.Fatalf("unexpected feature drift report: %+v", report)
	}
	if report.FeatureColumnCount != 3 || report.FeatureDistributionCount != 0 || report.PSIStatus != "pending_distribution_store" || report.KSStatus != "pending_distribution_store" || len(report.SampleFeatures) != 3 {
		t.Fatalf("unexpected feature drift fields: %+v", report)
	}
	hasTrainingRecommendation, hasLiveRecommendation := false, false
	for _, recommendation := range report.RecommendedNextRun {
		if recommendation == "materialize training feature distributions from the completed baseline-bound training matrix" {
			hasTrainingRecommendation = true
		}
		if recommendation == "run read-only shadow scoring after live coverage passes to materialize live-shadow feature distributions" {
			hasLiveRecommendation = true
		}
	}
	if !hasTrainingRecommendation || !hasLiveRecommendation {
		t.Fatalf("expected dynamic feature-drift next-run guidance, got %+v", report.RecommendedNextRun)
	}
	if report.LatestReplay == nil || report.LatestReplay.VerifiedColumnCount != 3 || report.ArtifactLocalSHA256 != artifactSHA {
		t.Fatalf("unexpected artifact/replay binding: %+v", report)
	}
	later := now.Add(time.Hour)
	service.now = func() time.Time { return later }
	laterReport, err := service.FeatureDriftReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.ReportSHA256 != laterReport.ReportSHA256 || report.GeneratedAt.Equal(laterReport.GeneratedAt) {
		t.Fatalf("feature drift checksum should be stable across generated_at changes: before=%+v after=%+v", report, laterReport)
	}
	handler := NewHandlerWithService(service)
	response := httptest.NewRecorder()
	handler.HandleFeatureDriftReport(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/feature-drift-report?download=1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(FeatureDriftReportVersion)) || response.Header().Get("Content-Disposition") == "" || response.Header().Get("ETag") == "" {
		t.Fatalf("feature drift handler failed: %d %s", response.Code, response.Body.String())
	}
}

func TestFeatureDriftReportComputesPersistedDistributionMetrics(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 15, 45, 0, 0, time.UTC)
	finished := now.Add(-time.Hour)
	artifactPath := t.TempDir() + "/baseline-artifact.json"
	artifactJSON := []byte(`{"version":"artifact-v1","models":[{"horizon_minutes":60,"feature_columns":["temperature","power","ecc_delta"],"means":[42.0,230.0,0.1],"scales":[5.0,40.0,1.0]}]}`)
	if err := os.WriteFile(artifactPath, artifactJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	artifactSHA, err := localFileSHA256(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	baseline := api.BaselineModelBuild{
		BaselineModelKey: "baseline-1", Version: "baseline-v1", Status: "completed", Algorithm: "logistic_regression",
		SourceMatrixBuildID: 1, SourceTrainingMatrixKey: "matrix-1", FeatureContractVersion: FeatureContractVersion,
		FeatureColumnCount: 3, HorizonCount: 1, TrainedModelCount: 1, ArtifactPath: artifactPath, ArtifactSHA256: artifactSHA,
		StartedAt: now.Add(-2 * time.Hour), FinishedAt: &finished,
	}
	if err := db.Create(&baseline).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.PredictionFeatureReplayRun{
		ReplayKey: "replay-1", Version: "replay-v1", Status: "passed", ModelSpecID: 1, ModelKey: "gpu.failure.test",
		ModelVersion: "baseline-v1", SourceBaselineBuildID: baseline.ID, SourceMatrixBuildID: 1, SourceKey: "prometheus-main",
		TransformationContractVersion: "transform-v1", TrainingFeatureCount: 3, VerifiedColumnCount: 3, ComparedValueCount: 30,
		ReportSHA256: "replay-sha", StartedAt: now.Add(-45 * time.Minute), FinishedAt: &finished,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for _, feature := range []string{"temperature", "power", "ecc_delta"} {
		for _, role := range []string{"training", "live_shadow"} {
			proportions := api.FloatList{0.50, 0.50}
			mean := 1.0
			if role == "live_shadow" {
				proportions = api.FloatList{0.45, 0.55}
				mean = 1.1
			}
			row := api.PredictionFeatureDistributionSnapshot{
				SnapshotKey:            role + "-" + feature,
				Version:                "feature-distribution-v1",
				Status:                 "completed",
				DistributionRole:       role,
				SourceBaselineBuildID:  baseline.ID,
				ModelKey:               "gpu.failure.test",
				ModelVersion:           "baseline-v1",
				FeatureContractVersion: FeatureContractVersion,
				FeatureName:            feature,
				SampleCount:            100,
				Mean:                   mean,
				P50:                    mean,
				P95:                    mean + 1,
				BinEdges:               api.FloatList{0, 1, 2},
				BinProportions:         proportions,
				ObservedAt:             now.Add(-30 * time.Minute),
			}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	report, err := service.FeatureDriftReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "passed" || report.PSIStatus != "computed" || report.KSStatus != "computed" || report.FeatureDistributionCount != 6 || report.ComparedFeatureCount != 3 || report.PassedFeatureCount != 3 {
		t.Fatalf("unexpected computed feature drift report: %+v", report)
	}
	if report.MaximumPSI <= 0 || report.MaximumKS <= 0 || len(report.FeatureComparisons) != 3 || len(report.SampleDistributions) != 6 || len(report.BlockingReasons) != 0 {
		t.Fatalf("unexpected feature drift metrics: %+v", report)
	}
}

func TestFeatureDriftBlocksIncompatibleHistogramBins(t *testing.T) {
	comparisons := featureDriftComparisons([]string{"temperature"}, []api.PredictionFeatureDistributionSnapshot{
		{ID: 1, Status: "completed", DistributionRole: "training", FeatureName: "temperature", SampleCount: 100, BinEdges: api.FloatList{0, 1, 2}, BinProportions: api.FloatList{0.5, 0.5}},
		{ID: 2, Status: "completed", DistributionRole: "live_shadow", FeatureName: "temperature", SampleCount: 100, BinEdges: api.FloatList{0, 2, 4}, BinProportions: api.FloatList{0.5, 0.5}},
	})
	if len(comparisons) != 1 || comparisons[0].Status != "blocked_missing_bins" {
		t.Fatalf("expected incompatible bins to block PSI/KS: %+v", comparisons)
	}
}

func TestValidationReadinessCombinesLabelOutcomeAndChallengerGates(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 16, 0, 0, 0, time.UTC)
	confirmedAt := now.Add(-time.Hour)
	label := api.FailureLabel{
		LabelKey: "confirmed", HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-1", GPUUUID: "GPU-1",
		ModelName: "H100", EventType: "xid_94", RuleVersion: "rule-v1", LabelValue: 1, QualityTier: "confirmed",
		SourceType: "human_resolution", SourceRecordID: 1, ConfirmationResolutionID: 7, LabelContractVersion: LabelContractVersion,
		OccurredAt: now.Add(-2 * time.Hour), AvailableAt: now.Add(-time.Hour), ConfirmedAt: &confirmedAt,
	}
	if err := db.Create(&label).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	report, err := service.ValidationReadinessReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != ValidationReadinessReportVersion || report.Status != "blocked" || report.LabelGateStatus != "exploratory_ready" || report.LabelManifestSHA256 == "" || report.ReadinessSHA256 == "" {
		t.Fatalf("unexpected validation readiness report: %+v", report)
	}
	if report.LabelManifestVersion != LabelManifestVersion || report.LabelManifestSHA256 == "" || report.EvidenceBundleVersion != EvidenceBundleVersion || report.EvidenceBundleSHA256 == "" || report.EvidencePositive != 1 || report.OutcomeReportVersion != "prediction-outcome-report-v1" || report.OutcomeStability != "blocked" || report.ChallengerVersion != HeaRankChallengerReportVersion || report.ChallengerConfidence != "insufficient_sample" || report.ChallengerHistoricalSignal != "no_signal" || report.DataDriftVersion != DataDriftReportVersion || report.DataDriftSHA256 == "" || report.DataDriftStatus != "blocked_no_shadow_runs" || report.DataDriftCoverage != "exploratory_insufficient_coverage_audits" || report.CalibrationDriftVersion != CalibrationDriftReportVersion || report.CalibrationDriftSHA256 == "" || report.CalibrationDriftStatus != "blocked_no_calibration_reports" || report.FeatureDriftVersion != FeatureDriftReportVersion || report.FeatureDriftSHA256 == "" || report.FeatureDriftStatus != "blocked_no_baseline_artifact" || report.FeatureDriftCompared != 0 || report.FeatureDriftMaxPSI != 0 || report.FeatureDriftKSStatus != "pending_distribution_store" || report.FeatureDistributionArchiveVersion != validationFeatureDistributionArchiveVersion || report.FeatureDistributionArchiveSHA256 == "" || report.FeatureDistributionArchiveScope.Status != "blocked" || report.FeatureDistributionComparability != "blocked_no_validation_scope" || report.FeatureDistributionMinimumPairs != validationFeatureDistributionMinimumPairs || report.FeatureDistributionSnapshots != 0 || report.FeatureDistributionPairedFeatures != 0 {
		t.Fatalf("unexpected readiness bindings: %+v", report)
	}
	if !testStringContains(report.FeatureDistributionBlockers, "validation scope requires a current shadow candidate model spec") || !testStringContains(report.FeatureDistributionBlockers, "feature distribution comparability requires a validation scope") {
		t.Fatalf("expected feature distribution archive blockers: %+v", report)
	}
	if len(report.FeatureDriftBlockers) == 0 || report.FeatureDriftBlockers[0] != "no completed baseline model artifact is available" || len(report.FeatureDriftNextRun) == 0 {
		t.Fatalf("expected feature drift guidance to be bound into readiness: %+v", report)
	}
	if len(report.BlockingReasons) == 0 || len(report.RecommendedNextRun) == 0 {
		t.Fatalf("expected readiness blockers and next steps: %+v", report)
	}
	later := now.Add(time.Hour)
	service.now = func() time.Time { return later }
	laterReport, err := service.ValidationReadinessReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.ReadinessSHA256 != laterReport.ReadinessSHA256 || report.GeneratedAt.Equal(laterReport.GeneratedAt) {
		t.Fatalf("readiness checksum should be stable across generated_at changes: before=%+v after=%+v", report, laterReport)
	}
	handler := NewHandlerWithService(service)
	response := httptest.NewRecorder()
	handler.HandleValidationReadiness(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/validation-readiness?download=1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(ValidationReadinessReportVersion)) || response.Header().Get("Content-Disposition") == "" || response.Header().Get("ETag") == "" {
		t.Fatalf("validation readiness handler failed: %d %s", response.Code, response.Body.String())
	}
}

func TestValidationReadinessBindsScopedFeatureDistributionArchive(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	trainedAt := now.Add(-2 * time.Hour)
	spec := api.PredictionModelSpec{
		ModelKey: "gpu.failure.within_7d", Version: "0.3.0", HardwareClass: "gpu", EntityType: "node",
		Task: "node_risk_ranking", HorizonMinutes: 10080, Algorithm: "logistic_regression", Runtime: "atlas",
		Mode: "shadow", Status: "shadow_candidate", FeatureContractVersion: FeatureContractVersion,
		LabelContractVersion: LabelContractVersion, SourceBaselineBuildID: 11, ScopeModelName: "NVIDIA H100 80GB HBM3",
		Current: true, TrainedAt: &trainedAt,
	}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	rows := []api.PredictionFeatureDistributionSnapshot{
		{
			SnapshotKey: "training-11-temp", Version: "feature-distribution-v1", Status: "completed", DistributionRole: "training",
			SourceBaselineBuildID: 11, FeatureContractVersion: FeatureContractVersion, FeatureName: "gpu_temp_mean_24h",
			SampleCount: 100, BinEdges: api.FloatList{0, 50, 100}, BinProportions: api.FloatList{0.7, 0.3}, ReportSHA256: "training-sha", ObservedAt: now.Add(-time.Hour),
		},
		{
			SnapshotKey: "live-11-temp", Version: "feature-distribution-v1", Status: "completed", DistributionRole: "live_shadow",
			SourceBaselineBuildID: 11, ModelSpecID: spec.ID, FeatureContractVersion: FeatureContractVersion, FeatureName: "gpu_temp_mean_24h",
			SampleCount: 80, BinEdges: api.FloatList{0, 50, 100}, BinProportions: api.FloatList{0.6, 0.4}, ReportSHA256: "live-sha", ObservedAt: now,
		},
		{
			SnapshotKey: "other-baseline-temp", Version: "feature-distribution-v1", Status: "completed", DistributionRole: "training",
			SourceBaselineBuildID: 12, FeatureContractVersion: FeatureContractVersion, FeatureName: "gpu_temp_mean_24h",
			SampleCount: 120, BinEdges: api.FloatList{0, 50, 100}, BinProportions: api.FloatList{0.5, 0.5}, ReportSHA256: "other-sha", ObservedAt: now,
		},
	}
	for _, row := range rows {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	historyService := history.NewService(db, config.HistoryConfig{}, time.Second)
	historyServiceArchive, err := historyService.FeatureDistributionArchiveForQuery(history.FeatureDistributionSnapshotQuery{Scope: "validation", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if historyServiceArchive.SnapshotCount != 2 || historyServiceArchive.ArchiveSHA256 == "" {
		t.Fatalf("unexpected history archive: %+v", historyServiceArchive)
	}

	service := NewService(db)
	service.now = func() time.Time { return now }
	report, err := service.ValidationReadinessReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.FeatureDistributionArchiveVersion != historyServiceArchive.Version || report.FeatureDistributionArchiveSHA256 != historyServiceArchive.ArchiveSHA256 {
		t.Fatalf("readiness archive binding mismatch: readiness=%+v archive=%+v", report, historyServiceArchive)
	}
	if report.FeatureDistributionArchiveScope.Status != "validation_scope" || report.FeatureDistributionArchiveScope.SourceBaselineBuildID != 11 || report.FeatureDistributionArchiveScope.ModelSpecID != spec.ID {
		t.Fatalf("unexpected readiness archive scope: %+v", report.FeatureDistributionArchiveScope)
	}
	if report.FeatureDistributionSnapshots != 2 || report.FeatureDistributionTraining != 1 || report.FeatureDistributionLiveShadow != 1 || report.FeatureDistributionFeatures != 1 || report.FeatureDistributionPairedFeatures != 1 || report.FeatureDistributionComparability != "comparable" {
		t.Fatalf("unexpected readiness archive counts: %+v", report)
	}
	firstReadinessSHA := report.ReadinessSHA256
	if firstReadinessSHA == "" {
		t.Fatalf("readiness sha is missing: %+v", report)
	}

	if err := db.Create(&api.PredictionFeatureDistributionSnapshot{
		SnapshotKey: "live-11-power", Version: "feature-distribution-v1", Status: "completed", DistributionRole: "live_shadow",
		SourceBaselineBuildID: 11, ModelSpecID: spec.ID, FeatureContractVersion: FeatureContractVersion, FeatureName: "power_usage_mean_24h",
		SampleCount: 80, BinEdges: api.FloatList{0, 300, 600}, BinProportions: api.FloatList{0.8, 0.2}, ReportSHA256: "live-power-sha", ObservedAt: now.Add(time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	report, err = service.ValidationReadinessReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.FeatureDistributionArchiveSHA256 == historyServiceArchive.ArchiveSHA256 || report.ReadinessSHA256 == firstReadinessSHA {
		t.Fatalf("readiness sha should change when scoped distribution archive changes: before=%s/%s after=%s/%s", historyServiceArchive.ArchiveSHA256, firstReadinessSHA, report.FeatureDistributionArchiveSHA256, report.ReadinessSHA256)
	}
	if report.FeatureDistributionComparability != "exploratory_partial_feature_pairs" || report.FeatureDistributionPairedFeatures != 1 || !testStringContains(report.FeatureDistributionMissingTraining, "power_usage_mean_24h") {
		t.Fatalf("expected partial feature-pair comparability after live-only feature: %+v", report)
	}
}

func testStringContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func assertRankingAtK(t *testing.T, rows []RankingAtK, k, eligible, positives, hits int, precision, recall, lift float64) {
	t.Helper()
	var found *RankingAtK
	for index := range rows {
		if rows[index].K == k {
			found = &rows[index]
			break
		}
	}
	if found == nil {
		t.Fatalf("missing ranking@%d in %+v", k, rows)
	}
	if found.Eligible != eligible || found.Positives != positives || found.Hits != hits {
		t.Fatalf("unexpected ranking@%d counts: %+v", k, *found)
	}
	if found.Precision == nil || math.Abs(*found.Precision-precision) > 1e-12 {
		t.Fatalf("unexpected precision@%d: %+v expected %v", k, found.Precision, precision)
	}
	if found.Recall == nil || math.Abs(*found.Recall-recall) > 1e-12 {
		t.Fatalf("unexpected recall@%d: %+v expected %v", k, found.Recall, recall)
	}
	if found.NDCG == nil || *found.NDCG <= 0 || *found.NDCG > 1 {
		t.Fatalf("unexpected ndcg@%d: %+v", k, found.NDCG)
	}
	if found.Lift == nil || math.Abs(*found.Lift-lift) > 1e-12 {
		t.Fatalf("unexpected lift@%d: %+v expected %v", k, found.Lift, lift)
	}
}

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
	report, err := service.HeaRankChallengerReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "blocked_insufficient_7d_sample" || report.ConfidenceStatus != "insufficient_sample" || len(report.SevenDay) != 3 || report.SevenDay[0].Policy != "logistic_probability" {
		t.Fatalf("unexpected challenger report: %+v", report)
	}
	if report.SevenDay[0].Rows != 4 || report.SevenDay[0].Nodes != 3 || report.SevenDay[0].Positives != 2 || len(report.SevenDay[0].RankingAtK) == 0 {
		t.Fatalf("unexpected logistic challenger metrics: %+v", report.SevenDay[0])
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
	if report.LabelManifestVersion != LabelManifestVersion || report.OutcomeReportVersion != "prediction-outcome-report-v1" || report.ChallengerVersion != HeaRankChallengerReportVersion || report.ChallengerConfidence != "insufficient_sample" {
		t.Fatalf("unexpected readiness bindings: %+v", report)
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

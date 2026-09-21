package prediction

import (
	"bytes"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestObservabilityRankingValidationUsesIndependentPointInTimeCohorts(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	cutoffs := []time.Time{
		time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC),
	}
	for cohortIndex, cutoff := range cutoffs {
		finished := cutoff.Add(time.Minute)
		run := api.HealthEvaluationRun{Status: "success", RuleVersion: "gpu-health-v1", StartedAt: cutoff, FinishedAt: &finished, AssetCount: 40, ScoredCount: 40}
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		for nodeIndex := 0; nodeIndex < 40; nodeIndex++ {
			node := "10.0." + string(rune('1'+cohortIndex)) + "." + twoDigit(nodeIndex+1)
			uuid := "GPU-" + string(rune('A'+cohortIndex)) + "-" + twoDigit(nodeIndex+1)
			snapshot := api.GPUFeatureSnapshot{
				EvaluationRunID: run.ID, GPUAssetID: uint(nodeIndex + 1), GPUUUID: uuid, NodeIP: node, GPUIndex: 0,
				Metrics: api.FloatMap{observabilityMaxGapMetric: float64(100 - nodeIndex)}, ObservedAt: cutoff.Add(30 * time.Second),
			}
			if err := db.Create(&snapshot).Error; err != nil {
				t.Fatal(err)
			}
		}
		label := api.FailureLabel{
			LabelKey: "positive-" + twoDigit(cohortIndex), HardwareClass: "gpu", EntityType: "gpu",
			EntityKey: "GPU-" + string(rune('A'+cohortIndex)) + "-01", GPUUUID: "GPU-" + string(rune('A'+cohortIndex)) + "-01",
			EventType: "row_remap_failure", LabelValue: 1, QualityTier: "confirmed", SourceType: "human_resolution",
			SourceRecordID: uint(cohortIndex + 1), LabelContractVersion: LabelContractVersion,
			OccurredAt: cutoff.Add(24 * time.Hour), AvailableAt: cutoff.Add(48 * time.Hour),
		}
		if err := db.Create(&label).Error; err != nil {
			t.Fatal(err)
		}
	}

	latest := cutoffs[0].Add(time.Minute)
	boundaryLabels := []api.FailureLabel{
		{LabelKey: "at-cutoff", HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-A-02", GPUUUID: "GPU-A-02", EventType: "xid_critical", LabelValue: 1, QualityTier: "confirmed", SourceType: "rule", SourceRecordID: 101, LabelContractVersion: LabelContractVersion, OccurredAt: latest, AvailableAt: latest},
		{LabelKey: "weak-proxy", HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-A-03", GPUUUID: "GPU-A-03", EventType: "xid_critical", LabelValue: 1, QualityTier: "weak_proxy", SourceType: "rule", SourceRecordID: 102, LabelContractVersion: LabelContractVersion, OccurredAt: latest.Add(time.Hour), AvailableAt: latest.Add(2 * time.Hour)},
		{LabelKey: "future-available", HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-A-04", GPUUUID: "GPU-A-04", EventType: "xid_critical", LabelValue: 1, QualityTier: "confirmed", SourceType: "rule", SourceRecordID: 103, LabelContractVersion: LabelContractVersion, OccurredAt: latest.Add(time.Hour), AvailableAt: now.Add(24 * time.Hour)},
		{LabelKey: "non-gpu", HardwareClass: "server", EntityType: "server", EntityKey: "GPU-A-05", GPUUUID: "GPU-A-05", EventType: "server_fault", LabelValue: 1, QualityTier: "confirmed", SourceType: "rule", SourceRecordID: 104, LabelContractVersion: LabelContractVersion, OccurredAt: latest.Add(time.Hour), AvailableAt: latest.Add(2 * time.Hour)},
		{LabelKey: "excluded", HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-A-06", GPUUUID: "GPU-A-06", EventType: "xid_critical", LabelValue: 1, QualityTier: "confirmed", SourceType: "rule", SourceRecordID: 105, LabelContractVersion: LabelContractVersion, OccurredAt: latest.Add(time.Hour), AvailableAt: latest.Add(2 * time.Hour), Excluded: true},
	}
	if err := db.Create(&boundaryLabels).Error; err != nil {
		t.Fatal(err)
	}

	service := NewService(db)
	service.now = func() time.Time { return now }
	report, err := service.ObservabilityRankingValidationReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != ObservabilityRankingValidationVersion || report.FrameworkVersion != FrameworkVersion || report.Status != "comparable" || report.ReportSHA256 == "" {
		t.Fatalf("unexpected observability ranking report: %+v", report)
	}
	if report.CohortCount != 3 || report.EvaluableCohortCount != 3 || report.PositiveCohortCount != 3 || report.TemporalConsistency.Status != "consistent" || report.TemporalConsistency.EvaluableIndependentCohorts != 3 {
		t.Fatalf("independent cohort consistency is incomplete: %+v", report)
	}
	if !report.Cohorts[0].CutoffAt.Equal(cutoffs[0].Add(time.Minute)) {
		t.Fatalf("cohort cutoff must use the time when the health run evidence became complete: %+v", report.Cohorts[0])
	}
	if !report.Safety.ModelIndependent || !report.Safety.ReadOnlyShadow || !report.Safety.NoAlertEmitted || !report.Safety.NoActionExecuted {
		t.Fatalf("safety contract is incomplete: %+v", report.Safety)
	}
	for _, cohort := range report.Cohorts {
		if cohort.Status != "comparable" || cohort.NodeCount != 40 || cohort.ScoredNodeCount != 40 || cohort.PositiveNodeCount != 1 || cohort.MatchedLabelCount != 1 || cohort.ScoreCoverage != 1 {
			t.Fatalf("unexpected cohort: %+v", cohort)
		}
		if len(cohort.RankingAtPercent) != 1 {
			t.Fatalf("missing top-percent ranking: %+v", cohort)
		}
		top := cohort.RankingAtPercent[0]
		if top.Percent != 5 || top.Limit != 2 || top.Hits != 1 || top.Recall == nil || *top.Recall != 1 || top.Lift == nil || math.Abs(*top.Lift-20) > 1e-12 {
			t.Fatalf("unexpected top-5%% metric: %+v", top)
		}
	}

	service.now = func() time.Time { return now.Add(time.Hour) }
	later, err := service.ObservabilityRankingValidationReport()
	if err != nil || later.ReportSHA256 != report.ReportSHA256 || later.GeneratedAt.Equal(report.GeneratedAt) {
		t.Fatalf("checksum must ignore generated_at: before=%+v after=%+v err=%v", report, later, err)
	}

	handler := NewHandlerWithService(service)
	response := httptest.NewRecorder()
	handler.HandleObservabilityRankingValidation(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/observability-ranking-validation?download=1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(ObservabilityRankingValidationVersion)) || response.Header().Get("Content-Disposition") == "" || response.Header().Get("ETag") == "" {
		t.Fatalf("observability ranking download failed: %d %s", response.Code, response.Body.String())
	}
	conditional := httptest.NewRequest(http.MethodGet, "/api/v1/prediction/observability-ranking-validation", nil)
	conditional.Header.Set("If-None-Match", response.Header().Get("ETag"))
	conditionalResponse := httptest.NewRecorder()
	handler.HandleObservabilityRankingValidation(conditionalResponse, conditional)
	if conditionalResponse.Code != http.StatusNotModified {
		t.Fatalf("conditional request should return 304, got %d", conditionalResponse.Code)
	}
}

func TestObservabilityRankingValidationSelectsStablePolicyWithoutTuningPrimarySignal(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	cutoffs := []time.Time{
		time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC),
	}
	for cohortIndex, cutoff := range cutoffs {
		finished := cutoff.Add(time.Minute)
		run := api.HealthEvaluationRun{Status: "success", StartedAt: cutoff, FinishedAt: &finished, AssetCount: 40, ScoredCount: 40}
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		for nodeIndex := 0; nodeIndex < 40; nodeIndex++ {
			presence := 100.0
			maxGap := float64(200 - nodeIndex)
			if nodeIndex == 0 {
				presence = 50
				maxGap = 1
			}
			uuid := "GPU-RACE-" + string(rune('A'+cohortIndex)) + "-" + twoDigit(nodeIndex+1)
			snapshot := api.GPUFeatureSnapshot{
				EvaluationRunID: run.ID, GPUAssetID: uint(nodeIndex + 1), GPUUUID: uuid,
				NodeIP: "10.2." + string(rune('1'+cohortIndex)) + "." + twoDigit(nodeIndex+1), GPUIndex: 0,
				Metrics: api.FloatMap{
					observabilityMaxGapMetric:         maxGap,
					"gpu_metric_presence_ratio_1h":    presence,
					"gpu_metric_sample_age_seconds":   15,
					"gpu_uuid_presence_flap_count_1h": 0,
					"target_scrape_success_ratio_5m":  100,
					"target_scrape_samples_ratio_5m":  100,
				},
				ObservedAt: cutoff.Add(30 * time.Second),
			}
			if err := db.Create(&snapshot).Error; err != nil {
				t.Fatal(err)
			}
		}
		positiveUUID := "GPU-RACE-" + string(rune('A'+cohortIndex)) + "-01"
		label := api.FailureLabel{
			LabelKey: "race-positive-" + twoDigit(cohortIndex), HardwareClass: "gpu", EntityType: "gpu",
			EntityKey: positiveUUID, GPUUUID: positiveUUID, EventType: "row_remap_failure", LabelValue: 1,
			QualityTier: "confirmed", SourceType: "human_resolution", SourceRecordID: uint(cohortIndex + 1),
			LabelContractVersion: LabelContractVersion, OccurredAt: cutoff.Add(24 * time.Hour), AvailableAt: cutoff.Add(48 * time.Hour),
		}
		if err := db.Create(&label).Error; err != nil {
			t.Fatal(err)
		}
	}

	service := NewService(db)
	service.now = func() time.Time { return now }
	report, err := service.ObservabilityRankingValidationReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "comparable" || report.PrimaryPolicy != observabilityMaxGapPolicy || report.CandidatePolicy != observabilityPresenceDeficitPolicy {
		t.Fatalf("stable alternative policy should be selected without changing the primary compatibility fields: %+v", report)
	}
	maxGapSummary, ok := observabilityPolicySummaryByName(report.PolicySummaries, observabilityMaxGapPolicy)
	if !ok || maxGapSummary.TemporalConsistency.Status != "review_mixed_direction" || maxGapSummary.TemporalConsistency.PositiveDirectionCohorts != 0 {
		t.Fatalf("the failed max-gap hypothesis must remain explicit: %+v", maxGapSummary)
	}
	presenceSummary, ok := observabilityPolicySummaryByName(report.PolicySummaries, observabilityPresenceDeficitPolicy)
	if !ok || presenceSummary.TemporalConsistency.Status != "consistent" || presenceSummary.TemporalConsistency.PositiveDirectionCohorts != 3 {
		t.Fatalf("presence-deficit policy must pass all independent cohorts: %+v", presenceSummary)
	}
	for _, cohort := range report.Cohorts {
		if top := cohort.RankingAtPercent[0]; top.Hits != 0 {
			t.Fatalf("legacy max-gap fields must preserve the failed primary result: %+v", cohort)
		}
		presence, ok := observabilityPolicyResultByName(cohort.PolicyResults, observabilityPresenceDeficitPolicy)
		if !ok || presence.Status != "comparable" || presence.DistinctScoreCount != 2 || presence.RankingAtPercent[0].Hits != 1 {
			t.Fatalf("presence policy result is incomplete: %+v", presence)
		}
		constantAge, ok := observabilityPolicyResultByName(cohort.PolicyResults, observabilitySampleAgePolicy)
		if !ok || constantAge.Status != "no_discrimination" || constantAge.DistinctScoreCount != 1 {
			t.Fatalf("constant scores must not become an accidental ranking candidate: %+v", constantAge)
		}
	}
}

func TestObservabilityRankingValidationBlocksWithoutMatureCohorts(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-time.Hour)
	run := api.HealthEvaluationRun{Status: "success", StartedAt: now.Add(-7 * 24 * time.Hour), FinishedAt: &finished}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.GPUFeatureSnapshot{EvaluationRunID: run.ID, GPUUUID: "GPU-1", NodeIP: "10.0.0.1", Metrics: api.FloatMap{observabilityMaxGapMetric: 1}, ObservedAt: run.StartedAt.Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	report, err := service.ObservabilityRankingValidationReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "blocked_no_mature_cohorts" || report.CohortCount != 0 || report.ReportSHA256 == "" || len(report.BlockingReasons) == 0 {
		t.Fatalf("immature cohorts must stay blocked: %+v", report)
	}
}

func TestObservabilityRankingValidationExposesNoPositiveAndUnscoredPositiveStates(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-9 * 24 * time.Hour)
	finished := cutoff.Add(time.Minute)
	run := api.HealthEvaluationRun{Status: "success", StartedAt: cutoff, FinishedAt: &finished, AssetCount: 30, ScoredCount: 30}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	var lastSnapshot api.GPUFeatureSnapshot
	for index := 0; index < 30; index++ {
		snapshot := api.GPUFeatureSnapshot{
			EvaluationRunID: run.ID, GPUAssetID: uint(index + 1), GPUUUID: "GPU-EDGE-" + twoDigit(index+1),
			NodeIP: "10.1.0." + twoDigit(index+1), Metrics: api.FloatMap{observabilityMaxGapMetric: float64(index + 1)}, ObservedAt: cutoff.Add(-time.Minute),
		}
		if err := db.Create(&snapshot).Error; err != nil {
			t.Fatal(err)
		}
		lastSnapshot = snapshot
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	noPositive, err := service.ObservabilityRankingValidationReport()
	if err != nil {
		t.Fatal(err)
	}
	if noPositive.Status != "collecting_no_positive_cohorts" || len(noPositive.Cohorts) != 1 || noPositive.Cohorts[0].Status != "no_positives" {
		t.Fatalf("mature all-negative evidence must remain an explicit collecting state: %+v", noPositive)
	}

	if err := db.Model(&lastSnapshot).Update("metrics", api.FloatMap{}).Error; err != nil {
		t.Fatal(err)
	}
	positive := api.FailureLabel{
		LabelKey: "unscored-positive", HardwareClass: "gpu", EntityType: "gpu", EntityKey: lastSnapshot.GPUUUID,
		GPUUUID: lastSnapshot.GPUUUID, EventType: "row_remap_failure", LabelValue: 1, QualityTier: "confirmed",
		SourceType: "human_resolution", SourceRecordID: 1, LabelContractVersion: LabelContractVersion,
		OccurredAt: cutoff.Add(time.Hour), AvailableAt: cutoff.Add(2 * time.Hour),
	}
	if err := db.Create(&positive).Error; err != nil {
		t.Fatal(err)
	}
	blocked, err := service.ObservabilityRankingValidationReport()
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Status != "exploratory" || blocked.Cohorts[0].Status != "blocked_incomplete_signal" || blocked.Cohorts[0].UnscoredPositiveNodes != 1 || blocked.EvaluableCohortCount != 0 {
		t.Fatalf("an unscored positive node must block cohort comparability: %+v", blocked)
	}
}

func twoDigit(value int) string {
	return string([]byte{'0' + byte(value/10), '0' + byte(value%10)})
}

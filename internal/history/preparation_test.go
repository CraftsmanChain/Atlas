package history

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestTrainingPreparationGatesTelemetryAndBuildsLeakageSafeSplits(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:training-preparation-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	outputRoot := t.TempDir()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := make([]extractedFeatureRow, 0, 11)
	for index := 0; index < 10; index++ {
		gpu := fmt.Sprintf("GPU-%02d", index)
		onset := base.Add(time.Duration(index) * 24 * time.Hour)
		rows = append(rows, extractedFeatureRow{
			SampleKey: fmt.Sprintf("sample-%02d", index), EpisodeKey: fmt.Sprintf("episode-%02d", index),
			GPUUUID: gpu, NodeIP: fmt.Sprintf("10.0.0.%d", index+1), ModelName: "NVIDIA H100 80GB HBM3",
			HorizonMinutes: 60, FeatureCutoffAt: onset.Add(-time.Hour), LabelOnsetAt: onset,
			MetricCoverage: 0.8, ExpectedMetrics: 18, AvailableMetrics: 15, LookbackMinutes: 1440, QueryStepSeconds: 300,
			Features: map[string]float64{"gpu_temp_mean_24h": 50, "gpu_temp_sample_count_24h": 289, "power_usage_sample_count_24h": 289, "gpu_util_sample_count_24h": 289}, LabelWeight: 0.9,
		})
		interval := api.HistoricalGPUIdentityInterval{
			IntervalKey: "identity-" + gpu, SourceKey: "primary", BackfillRunID: 1,
			NodeIP: fmt.Sprintf("10.0.0.%d", index+1), GPUIndex: 0, GPUUUID: gpu,
			ModelName: "NVIDIA H100 80GB HBM3", DataCenterID: "dc-a", DriverVersion: "560",
			FirstSeenAt: onset.Add(-200 * 24 * time.Hour), LastSeenAt: onset.Add(24 * time.Hour),
			ObservationCount: 10, TransitionType: "initial_observation", EvidenceStrength: "strong",
		}
		if err := db.Create(&interval).Error; err != nil {
			t.Fatal(err)
		}
	}
	zeroOnset := base.Add(20 * 24 * time.Hour)
	rows = append(rows, extractedFeatureRow{
		SampleKey: "sample-zero", EpisodeKey: "episode-zero", GPUUUID: "GPU-ZERO",
		ModelName: "NVIDIA H100 80GB HBM3", HorizonMinutes: 60,
		FeatureCutoffAt: zeroOnset.Add(-time.Hour), LabelOnsetAt: zeroOnset,
		MetricCoverage: 0, ExpectedMetrics: 18, Features: map[string]float64{},
	})
	featureDir := filepath.Join(outputRoot, "features", "source")
	if err := os.MkdirAll(featureDir, 0o750); err != nil {
		t.Fatal(err)
	}
	featurePath := filepath.Join(featureDir, "features.jsonl")
	checksum, err := writeJSONLines(featurePath, rows)
	if err != nil {
		t.Fatal(err)
	}
	cohortDir := filepath.Join(outputRoot, "cohort")
	if err := os.MkdirAll(cohortDir, 0o750); err != nil {
		t.Fatal(err)
	}
	windows := make([]datasetWindow, 0, len(rows))
	for _, row := range rows {
		windows = append(windows, datasetWindow{
			SampleKey: row.SampleKey, EpisodeKey: row.EpisodeKey, NodeIP: row.NodeIP,
			GPUUUID: row.GPUUUID, ModelName: row.ModelName, EventTypes: []string{"xid_79_gpu_fallen_off_bus"},
			HorizonMinutes: row.HorizonMinutes, FeatureCutoffAt: row.FeatureCutoffAt, LabelOnsetAt: row.LabelOnsetAt,
		})
	}
	windowPath := filepath.Join(cohortDir, "sample_windows.jsonl")
	windowChecksum, err := writeJSONLines(windowPath, windows)
	if err != nil {
		t.Fatal(err)
	}
	finished := base
	cohort := api.TrainingDatasetBuild{
		DatasetKey: "cohort-v2", Version: datasetBuildVersion, Status: "completed",
		SourceKey: "primary", EpisodeCount: 11, WindowCount: 11,
		OutputDir: cohortDir, WindowManifestPath: windowPath, WindowManifestSHA256: windowChecksum,
		StartedAt: base, FinishedAt: &finished,
	}
	if err := db.Create(&cohort).Error; err != nil {
		t.Fatal(err)
	}
	source := api.TrainingFeatureBuild{
		FeatureDatasetKey: "features-v2", Version: featureDatasetVersion, Status: "completed",
		SourceKey: "primary", SourceDatasetBuildID: cohort.ID, SourceDatasetKey: cohort.DatasetKey,
		FeatureContractVersion: "1.9.0", EpisodeCount: 11, WindowCount: 11,
		CompletedWindows: 11, FeaturePath: featurePath, FeatureSHA256: checksum,
		OutputDir: featureDir, StartedAt: base, FinishedAt: &finished,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{DatasetDir: outputRoot}, time.Second)
	service.now = func() time.Time { return base.Add(30 * 24 * time.Hour) }
	build, err := service.BuildTrainingPreparation(PreparationBuildRequest{
		SourceFeatureBuildID: source.ID, MinimumCoverage: 0.7, ControlsPerPositive: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "completed" || build.EligiblePositiveCount != 10 ||
		build.TelemetryCensoredCount != 1 || build.LowCoverageCount != 0 ||
		build.TrainCount != 7 || build.ValidationCount != 1 || build.TestCount != 2 ||
		build.ControlRequestCount != 30 || build.ControlShortfallCount != 0 {
		t.Fatalf("unexpected preparation build: %+v", build)
	}
	if build.PreparedSamplesSHA256 == "" || build.ControlRequestsSHA256 == "" {
		t.Fatalf("preparation artifacts were not checksummed: %+v", build)
	}
}

func TestPositiveTelemetryContinuityRejectsSparseCoreSamples(t *testing.T) {
	row := extractedFeatureRow{LookbackMinutes: 1440, QueryStepSeconds: 300, Features: map[string]float64{"gpu_temp_sample_count_24h": 289, "power_usage_sample_count_24h": 289, "gpu_util_sample_count_24h": 20}}
	if continuity := positiveTelemetryContinuity(row); continuity >= minimumTelemetryContinuity {
		t.Fatalf("sparse positive telemetry continuity=%v", continuity)
	}
}

func TestEntityIsolationExcludesGPUCrossingTimeBoundary(t *testing.T) {
	trainEnd := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	validationEnd := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
	rows := []extractedFeatureRow{
		{GPUUUID: "GPU-CROSS", LabelOnsetAt: trainEnd.Add(-time.Hour)},
		{GPUUUID: "GPU-CROSS", LabelOnsetAt: trainEnd.Add(time.Hour)},
		{GPUUUID: "GPU-TEST", LabelOnsetAt: validationEnd.Add(time.Hour)},
	}
	splits := entityIsolatedSplits(rows, trainEnd, validationEnd)
	if splits["GPU-CROSS"] != "" || splits["GPU-TEST"] != "test" {
		t.Fatalf("unexpected entity-isolated splits: %+v", splits)
	}
}

func TestFaultContaminationChecksEntireFeatureWindow(t *testing.T) {
	fault := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	policy := CurrentTrainingCohortPolicy()
	censorEnd := fault.Add(time.Duration(policy.HealthyCensorAfterHours) * time.Hour)
	windowEnd := censorEnd.Add(time.Hour)
	windowStart := windowEnd.Add(-24 * time.Hour)
	if !faultWindowContaminated(windowStart, windowEnd, []time.Time{fault}) {
		t.Fatal("feature window overlapping the fault censor interval must be rejected")
	}
	if faultWindowContaminated(windowStart.Add(-14*24*time.Hour), windowEnd.Add(-14*24*time.Hour), []time.Time{fault}) {
		t.Fatal("feature window outside the fault censor interval must remain eligible")
	}
}

func TestCorrelatedFleetEpisodesExcludesOnlyCrossNodeShock(t *testing.T) {
	base := time.Date(2026, 7, 1, 8, 52, 0, 0, time.UTC)
	windows := make([]datasetWindow, 0, 41)
	for index := 0; index < 40; index++ {
		windows = append(windows, datasetWindow{
			EpisodeKey: fmt.Sprintf("fleet-%02d", index), GPUUUID: fmt.Sprintf("GPU-%02d", index),
			NodeIP: fmt.Sprintf("10.0.0.%d", index/4), EventTypes: []string{"gpu_dropout"},
			LabelOnsetAt: base.Add(time.Duration(index%4) * time.Second),
		})
	}
	windows = append(windows, datasetWindow{
		EpisodeKey: "single-node", GPUUUID: "GPU-SINGLE", NodeIP: "10.0.1.1",
		EventTypes: []string{"xid_79_gpu_fallen_off_bus"}, LabelOnsetAt: base,
	})
	excluded := correlatedFleetEpisodes(windows)
	if len(excluded) != 40 || !excluded["fleet-00"] || excluded["single-node"] {
		t.Fatalf("unexpected correlated fleet exclusions: %d %+v", len(excluded), excluded)
	}
}

func TestCurrentLabelEligibilitySeparatesXID109OperationsFromTraining(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:label-eligibility-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	xid109 := api.HistoricalFaultCandidate{CandidateKey: "xid109", SourceKey: "primary", BackfillRunID: 1, EntityType: "gpu", EventType: "xid_109_context_switch_timeout", OperationalPriority: "high", TrainingDisposition: "proxy_positive_after_review", ReviewStatus: "available_for_override", IdentityEvidenceStatus: "same_gpu_observed_after_event", RuleDecisionVersion: historicalRuleDecisionVersion, OnsetAt: time.Now(), DetectionWindowEndAt: time.Now()}
	xid94 := api.HistoricalFaultCandidate{CandidateKey: "xid94", SourceKey: "primary", BackfillRunID: 1, EntityType: "gpu", EventType: "xid_94_contained_ecc", EventCode: "94", OperationalPriority: "high", HardwareCertainty: "investigation_required", TrainingDisposition: "proxy_positive_after_review", ReviewStatus: "available_for_override", IdentityEvidenceStatus: "same_gpu_observed_after_event", RuleDecisionVersion: historicalRuleDecisionVersion, Labels: api.StringMap{"DCGM_FI_DRIVER_VERSION": "560.35.03"}, OnsetAt: time.Now(), DetectionWindowEndAt: time.Now()}
	if err := db.Create(&xid109).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xid94).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{}, time.Second)
	eligible, err := service.currentLabelEligibleEpisodes([]datasetWindow{{EpisodeKey: "e109", CandidateIDs: []uint{xid109.ID}}, {EpisodeKey: "e94", CandidateIDs: []uint{xid94.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if eligible["e109"] || !eligible["e94"] {
		t.Fatalf("unexpected current label eligibility: %+v", eligible)
	}
	episodes, err := service.currentLabelEpisodes([]datasetWindow{{EpisodeKey: "e109", CandidateIDs: []uint{xid109.ID}}, {EpisodeKey: "e94", CandidateIDs: []uint{xid94.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	metadata := episodes["e94"].Metadata
	if len(metadata.EventTypes) != 1 || metadata.EventTypes[0] != "xid_94_contained_ecc" ||
		len(metadata.DriverVersions) != 1 || metadata.DriverVersions[0] != "560.35.03" ||
		len(metadata.RuleDecisionVersions) != 1 || metadata.RuleDecisionVersions[0] != historicalRuleDecisionVersion {
		t.Fatalf("eligible label metadata was not preserved: %+v", metadata)
	}
}

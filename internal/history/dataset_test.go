package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestDatasetManifestDeduplicatesReplacementEpisodeAndEnforcesEligibility(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:history-dataset-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	identity := api.HistoricalGPUIdentityInterval{
		IntervalKey: "dataset-model-identity", SourceKey: "primary", BackfillRunID: 1,
		NodeIP: "10.0.0.9", GPUIndex: 0, GPUUUID: "GPU-old",
		PCIBusID: "0000:01:00.0", ModelName: "NVIDIA H100 80GB HBM3",
		FirstSeenAt: now.Add(-24 * time.Hour), LastSeenAt: now.Add(24 * time.Hour),
		ObservationCount: 2, TransitionType: "initial_observation", EvidenceStrength: "strong",
	}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	rows := []api.HistoricalFaultCandidate{
		datasetCandidate("replacement-ecc", "uncorrectable_remapped_rows", now.Add(-2*time.Hour),
			"replacement_after_event", "proxy_positive_after_review", "pending_review"),
		datasetCandidate("replacement-xid", "xid_94_contained_ecc", now.Add(-time.Hour),
			"replacement_after_event", "proxy_positive_after_review", "pending_review"),
		datasetCandidate("pending", "xid_79_gpu_fallen_off_bus", now,
			"same_gpu_observed_after_event", "proxy_positive_after_review", "pending_review"),
		datasetCandidate("identity-missing", "xid_109_context_switch_timeout", now,
			"alert_identity_missing", "proxy_positive_after_review", "pending_review"),
		datasetCandidate("context", "xid_31", now,
			"same_gpu_observed_after_event", "context_only", "pending_review"),
	}
	rows[0].ModelName, rows[0].IdentityIntervalID = "", identity.ID
	rows[1].ModelName, rows[1].IdentityIntervalID = "", identity.ID
	for index := range rows {
		if err := db.Create(&rows[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	finished := now.Add(-time.Minute)
	if err := db.Create(&api.HistoryBackfillRun{
		SourceKey: "primary", JobType: "gpu_identity_interval", Status: "completed",
		QueryVersion: identityBackfillQueryVersion, RangeStart: now.Add(-24 * time.Hour),
		RangeEnd: now, StartedAt: now.Add(-2 * time.Minute), FinishedAt: &finished,
	}).Error; err != nil {
		t.Fatal(err)
	}
	outputRoot := t.TempDir()
	service := NewService(db, config.HistoryConfig{
		DatasetDir: outputRoot,
		Sources: []config.HistorySourceConfig{{
			ID: "primary", Type: "prometheus", BaseURL: "http://127.0.0.1:9090", Enabled: true,
		}},
	}, time.Second)
	service.now = func() time.Time { return now }
	build, err := service.BuildDatasetManifest(DatasetBuildRequest{SourceKey: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "completed" || build.CandidateCount != 5 || build.EligibleCandidateCount != 2 ||
		build.EpisodeCount != 1 || build.WindowCount != 11 || build.PendingReviewCount != 1 ||
		build.IdentityMissingCount != 1 || build.ContextOnlyCount != 1 {
		t.Fatalf("unexpected dataset build: %+v", build)
	}
	if build.WindowManifestSHA256 == "" || filepath.Dir(build.ManifestPath) != build.OutputDir {
		t.Fatalf("dataset artifact metadata mismatch: %+v", build)
	}
	file, err := os.Open(build.WindowManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		var row datasetWindow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if !row.FeatureCutoffAt.Before(row.LabelOnsetAt) || len(row.CandidateIDs) != 2 ||
			row.ModelName != "NVIDIA H100 80GB HBM3" {
			t.Fatalf("point-in-time or episode merge mismatch: %+v", row)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 11 {
		t.Fatalf("sample-window rows=%d", count)
	}
}

func TestDatasetManifestRequiresCurrentIdentityBackfill(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:history-dataset-gate-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{
		DatasetDir: t.TempDir(),
		Sources: []config.HistorySourceConfig{{
			ID: "primary", Type: "prometheus", BaseURL: "http://127.0.0.1:9090", Enabled: true,
		}},
	}, time.Second)
	if _, err := service.BuildDatasetManifest(DatasetBuildRequest{SourceKey: "primary"}); err == nil {
		t.Fatal("expected current identity-backfill gate to block dataset construction")
	}
}

func datasetCandidate(key, eventType string, onset time.Time, identityStatus, disposition, reviewStatus string) api.HistoricalFaultCandidate {
	ruleDecision, ruleConfidence := "needs_human_review", float64(0)
	if identityStatus == "replacement_after_event" {
		ruleDecision, ruleConfidence = "positive_proxy", 0.95
	}
	if disposition == "context_only" {
		ruleDecision, ruleConfidence = "context_only", 1
	}
	candidate := api.HistoricalFaultCandidate{
		CandidateKey: key, SourceKey: "primary", BackfillRunID: 1, EntityType: "gpu",
		GPUUUID: "GPU-old", NodeIP: "10.0.0.9", PCIBusID: "0000:01:00.0",
		ModelName: "NVIDIA H100", EventType: eventType, EventCode: eventType,
		QualityTier: "strong_proxy", OperationalPriority: "high",
		HardwareCertainty: "investigation_required", TrainingDisposition: disposition,
		ReviewStatus: reviewStatus, IdentityEvidenceStatus: identityStatus,
		RuleDecision: ruleDecision, RuleDecisionVersion: historicalRuleDecisionVersion,
		RuleConfidence:   ruleConfidence,
		IdentityEvidence: api.StringMap{"successor_uuid": "GPU-new"},
		SourceMetric:     "ALERTS", OnsetAt: onset, DetectionWindowEndAt: onset,
	}
	if key == "identity-missing" {
		candidate.GPUUUID, candidate.PCIBusID = "", ""
	}
	return candidate
}

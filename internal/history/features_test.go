package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestHistoricalFeatureBuildBatchesMetricsAndEnforcesCutoff(t *testing.T) {
	cutoff := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	var queryCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		queryCount.Add(1)
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, "DCGM_FI_DEV_GPU_UTIL") ||
			!strings.Contains(query, "nvidia_smi_utilization_gpu_ratio") ||
			!strings.Contains(query, `GPU\\.TEST-1`) {
			t.Errorf("batch query lost metric or escaped identity: %s", query)
		}
		before := cutoff.Add(-time.Hour).Unix()
		atCutoff := cutoff.Unix()
		after := cutoff.Add(time.Hour).Unix()
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"__name__":"DCGM_FI_DEV_GPU_UTIL","UUID":"GPU.TEST-1"},"values":[[%d,"10"],[%d,"20"],[%d,"999"]]},
			{"metric":{"__name__":"nvidia_smi_utilization_gpu_ratio","uuid":"gpu.test-1"},"values":[[%d,"0.9"],[%d,"0.95"]]}
		]}}`, before, atCutoff, after, before, atCutoff)
	}))
	defer server.Close()

	db, err := storage.InitDB(fmt.Sprintf("file:history-features-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	outputRoot := t.TempDir()
	cohortDir := filepath.Join(outputRoot, "cohorts", "source")
	if err := os.MkdirAll(cohortDir, 0o750); err != nil {
		t.Fatal(err)
	}
	window := datasetWindow{
		SampleKey: "sample-1", DatasetVersion: datasetBuildVersion, EpisodeKey: "episode-1",
		NodeIP: "10.0.0.1", GPUUUID: "GPU.TEST-1", ModelName: "NVIDIA H100",
		HorizonMinutes: 60, FeatureCutoffAt: cutoff, LabelOnsetAt: cutoff.Add(time.Hour),
		Eligibility: "rule_positive_proxy", RuleDecision: "positive_proxy",
		LabelSource: "versioned_rule", LabelWeight: 0.9,
	}
	windowPath := filepath.Join(cohortDir, "sample_windows.jsonl")
	file, err := os.Create(windowPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(window); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(windowPath)
	if err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(content)
	finished := cutoff
	sourceBuild := api.TrainingDatasetBuild{
		DatasetKey: "source-cohort", Version: datasetBuildVersion, Status: "completed",
		SourceKey: "primary", WindowCount: 1, OutputDir: cohortDir,
		WindowManifestPath: windowPath, WindowManifestSHA256: hex.EncodeToString(checksum[:]),
		StartedAt: cutoff.Add(-time.Minute), FinishedAt: &finished,
	}
	if err := db.Create(&sourceBuild).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{
		DatasetDir: outputRoot, MaxConcurrency: 2,
		Sources: []config.HistorySourceConfig{{
			ID: "primary", Type: "prometheus", BaseURL: server.URL, Enabled: true,
		}},
	}, time.Second)
	service.now = func() time.Time { return cutoff.Add(2 * time.Hour) }
	build, err := service.StartFeatureBuild(FeatureBuildRequest{SourceDatasetBuildID: sourceBuild.ID})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := db.First(&build, build.ID).Error; err != nil {
			t.Fatal(err)
		}
		if build.Status != "queued" && build.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if build.Status != "completed" || build.CompletedWindows != 1 || build.FailedWindows != 0 {
		t.Fatalf("unexpected feature build: %+v", build)
	}
	if queryCount.Load() != 1 {
		t.Fatalf("expected one batched range query, got %d", queryCount.Load())
	}
	featureFile, err := os.Open(build.FeaturePath)
	if err != nil {
		t.Fatal(err)
	}
	defer featureFile.Close()
	var row extractedFeatureRow
	if err := json.NewDecoder(featureFile).Decode(&row); err != nil {
		t.Fatal(err)
	}
	if row.Features["gpu_util_max_24h"] != 20 || row.Features["gpu_util_last_24h"] != 20 {
		t.Fatalf("post-cutoff sample leaked or fallback priority failed: %+v", row.Features)
	}
	if !row.FeatureCutoffAt.Before(row.LabelOnsetAt) || build.FeatureSHA256 == "" || build.QualityReportPath == "" {
		t.Fatalf("feature artifact lost point-in-time provenance: %+v %+v", row, build)
	}
}

func TestFeatureBuildRequiresCompletedCohort(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:history-features-gate-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{}, time.Second)
	if _, err := service.StartFeatureBuild(FeatureBuildRequest{}); err == nil {
		t.Fatal("expected completed cohort gate")
	}
}

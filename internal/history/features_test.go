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

	promclient "atlas/internal/prometheus"
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
		if strings.Contains(query, "atlas_feature") {
			atCutoff := cutoff.Unix()
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[
				{"metric":{"atlas_feature":"gpu_metric_samples_1h"},"values":[[%d,"240"]]},
				{"metric":{"atlas_feature":"gpu_metric_presence_ratio_1h"},"values":[[%d,"100"]]},
				{"metric":{"atlas_feature":"gpu_metric_sample_age_seconds"},"values":[[%d,"8"]]},
				{"metric":{"atlas_feature":"gpu_uuid_presence_flap_count_1h"},"values":[[%d,"0"]]},
				{"metric":{"atlas_feature":"gpu_metric_gap_max_seconds_1h"},"values":[[%d,"15"]]},
				{"metric":{"atlas_feature":"target_scrape_success_ratio_5m"},"values":[[%d,"100"]]},
				{"metric":{"atlas_feature":"target_scrape_samples_ratio_5m"},"values":[[%d,"99"]]},
				{"metric":{"atlas_feature":"target_scrape_duration_ratio_5m"},"values":[[%d,"101"]]}
			]}}`, atCutoff, atCutoff, atCutoff, atCutoff, atCutoff, atCutoff, atCutoff, atCutoff)
			return
		}
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
		PredictionTarget: highPriorityXIDEventTarget,
		HorizonMinutes:   10080, FeatureCutoffAt: cutoff, LabelOnsetAt: cutoff.Add(7 * 24 * time.Hour),
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
		SourceKey: "primary", PredictionTarget: highPriorityXIDEventTarget, WindowCount: 1, OutputDir: cohortDir,
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
	if queryCount.Load() != 3 {
		t.Fatalf("expected one fine, one coarse, and one structural point query, got %d", queryCount.Load())
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
	if row.Features["gpu_util_max_1h"] != 20 || row.Features["gpu_util_mean_15m"] != 20 {
		t.Fatalf("multi-scale point-in-time features missing: %+v", row.Features)
	}
	if row.Features["gpu_util_max_30d"] != 20 || row.Features["gpu_util_last_7d"] != 20 || row.LookbackMinutes != int(featureLongLookback/time.Minute) || row.LongQueryStepSeconds != int(featureLongQueryStep/time.Second) {
		t.Fatalf("coarse long-range point-in-time features missing: %+v", row)
	}
	if row.StructuralCoverage != 1 || row.AvailableStructural != 8 || row.Features["gpu_metric_samples_1h"] != 240 || row.StructuralError != "" {
		t.Fatalf("structural observability features missing: %+v", row)
	}
	if build.StructuralFeatureCount != 8 || build.StructuralCompleteWindows != 1 || build.StructuralExtractionFailedWindows != 0 || build.AverageStructuralFeatureCoverage != 1 {
		t.Fatalf("structural quality summary missing: %+v", build)
	}
	if !row.FeatureCutoffAt.Before(row.LabelOnsetAt) || row.PredictionTarget != highPriorityXIDEventTarget || build.PredictionTarget != highPriorityXIDEventTarget || build.FeatureSHA256 == "" || build.QualityReportPath == "" {
		t.Fatalf("feature artifact lost point-in-time provenance: %+v %+v", row, build)
	}
}

func TestHistoricalStructuralFeatureQueryIsScopedAndComplete(t *testing.T) {
	query := historicalStructuralFeatureQuery("GPU.TEST-1")
	if !strings.Contains(query, `GPU\\.TEST-1`) || !strings.Contains(query, `job="dcgm_exporter"`) {
		t.Fatalf("structural query lost escaped GPU identity or exporter scope: %s", query)
	}
	for _, feature := range historicalStructuralFeatures {
		if !strings.Contains(query, `"`+feature+`"`) {
			t.Fatalf("structural query missing %s: %s", feature, query)
		}
	}
}

func TestApplyStructuralFeaturesKeepsFailureSeparateFromCoreExtraction(t *testing.T) {
	row := extractedFeatureRow{Features: map[string]float64{}}
	applyStructuralFeatures(&row, map[string]float64{
		"gpu_metric_samples_1h": 240,
		"unknown":               1,
	}, fmt.Errorf("structural timeout"))
	if row.ExtractionError != "" || row.StructuralError != "structural timeout" || row.StructuralCoverage != 0.125 || row.AvailableStructural != 1 || len(row.MissingStructural) != 7 {
		t.Fatalf("structural degradation must remain auditable without failing core extraction: %+v", row)
	}
	if row.Features["gpu_metric_samples_1h"] != 240 {
		t.Fatalf("available structural values must survive a partial query result: %+v", row.Features)
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

func TestExpectedHistoricalMetricsAreModelAware(t *testing.T) {
	if got := len(expectedHistoricalMetrics("NVIDIA H100 80GB HBM3")); got != 28 {
		t.Fatalf("H100 expected metrics=%d", got)
	}
	a100 := expectedHistoricalMetrics("NVIDIA A100")
	if len(a100) != 26 {
		t.Fatalf("A100 expected metrics=%d", len(a100))
	}
	for _, metric := range a100 {
		if metric == "uncorrected_ecc_aggregate" || metric == "uncorrected_ecc_volatile" {
			t.Fatalf("unsupported H100/H200-only metric leaked into A100 coverage: %s", metric)
		}
	}
	if got := len(requiredHistoricalMetrics("NVIDIA H100 80GB HBM3")); got != 10 {
		t.Fatalf("H100 required core metrics=%d", got)
	}
	if got := len(requiredHistoricalMetrics("NVIDIA GeForce RTX 4090")); got != 9 {
		t.Fatalf("4090 required core metrics=%d", got)
	}
}

func TestOptionalHistoricalSignalsDoNotReduceCoreCoverage(t *testing.T) {
	cutoff := time.Now().UTC()
	window := datasetWindow{ModelName: "NVIDIA H100", FeatureCutoffAt: cutoff}
	points := map[string][]promclient.RangePoint{}
	for _, metric := range requiredHistoricalMetrics(window.ModelName) {
		points[metric] = []promclient.RangePoint{{Timestamp: cutoff, Value: 1}}
	}
	row := summarizeFeatureWindow(&api.TrainingFeatureBuild{}, window, points)
	if row.MetricCoverage != 1 || row.ExpectedMetrics != 10 || row.AvailableMetrics != 10 {
		t.Fatalf("optional absence must not lower core coverage: %+v", row)
	}
	if row.OptionalMetrics != 18 || row.AvailableOptional != 0 || len(row.MissingOptional) != 18 {
		t.Fatalf("optional availability audit is incomplete: %+v", row)
	}
}

func TestFeatureQuerySegmentsMergeOnlyOverlappingLookbacks(t *testing.T) {
	onset := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	windows := []datasetWindow{
		{FeatureCutoffAt: onset.Add(-7 * 24 * time.Hour), HorizonMinutes: int(longRangeHorizon / time.Minute)},
		{FeatureCutoffAt: onset.Add(-24 * time.Hour)},
		{FeatureCutoffAt: onset.Add(-time.Hour)},
	}
	segments := featureQuerySegments(windows, featureLookback)
	if len(segments) != 2 {
		t.Fatalf("expected isolated 7d window plus merged 24h/1h windows, got %+v", segments)
	}
	queried := time.Duration(0)
	for _, segment := range segments {
		queried += segment.end.Sub(segment.start)
	}
	if queried != 71*time.Hour {
		t.Fatalf("expected 71h of fine-resolution scan instead of a contiguous 8d scan, got %s", queried)
	}
	start, end, ok := longRangeQueryBounds(windows)
	if !ok || end != windows[0].FeatureCutoffAt || end.Sub(start) != featureLongLookback {
		t.Fatalf("long-range query must cover exactly the 30d window used by the 7d target: %v %v %v", start, end, ok)
	}
}

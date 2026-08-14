package history

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestAuditPersistsBoundedPrometheusCoverage(t *testing.T) {
	prometheus := newTestPrometheus(t)
	defer prometheus.Close()
	db, err := storage.InitDB(fmt.Sprintf("file:history-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.HistoryConfig{
		Enabled: true, DatasetDir: "/mnt/public/atlas/training",
		Sources: []config.HistorySourceConfig{{
			ID: "primary", Name: "Primary", Type: "prometheus", BaseURL: prometheus.URL, Enabled: true,
		}},
	}
	service := NewService(db, cfg, time.Second)
	service.now = func() time.Time { return time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC) }
	rows, err := service.AuditAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	row := rows[0]
	if row.Status != "success" || row.SourceVersion != "2.54.0" || row.ConfiguredRetention != "5y" {
		t.Fatalf("unexpected audit: %+v", row)
	}
	if row.DCGMTargetCount != 1 || row.GPUExporterTargetCount != 1 || row.CurrentGPUSeries != 720 {
		t.Fatalf("unexpected coverage: %+v", row)
	}
	if row.ScrapeIntervalSeconds != 15 || row.LatestSampleAt == nil {
		t.Fatalf("unexpected cadence: %+v", row)
	}
	if len(row.MetricFamilies) != 3 || len(row.MissingMetricFamilies) == 0 {
		t.Fatalf("unexpected metric inventory: %+v", row)
	}
	sources, err := service.Sources()
	if err != nil || len(sources) != 1 || sources[0].LatestAudit == nil {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
}

func TestFeatureDistributionSnapshotsExposeReadOnlyHistory(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:feature-distributions-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 16, 20, 0, 0, time.UTC)
	rows := []api.PredictionFeatureDistributionSnapshot{
		{
			SnapshotKey: "training-temp", Version: "feature-distribution-v1", Status: "completed", DistributionRole: "training",
			SourceBaselineBuildID: 1, FeatureContractVersion: "atlas-prediction-features-v1", FeatureName: "temperature",
			SampleCount: 100, BinEdges: api.FloatList{0, 1, 2}, BinProportions: api.FloatList{0.5, 0.5}, ObservedAt: now.Add(-time.Hour),
		},
		{
			SnapshotKey: "live-temp", Version: "feature-distribution-v1", Status: "completed", DistributionRole: "live_shadow",
			SourceBaselineBuildID: 1, FeatureContractVersion: "atlas-prediction-features-v1", FeatureName: "temperature",
			SampleCount: 80, BinEdges: api.FloatList{0, 1, 2}, BinProportions: api.FloatList{0.45, 0.55}, ObservedAt: now,
		},
	}
	for _, row := range rows {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db, config.HistoryConfig{}, time.Second)
	list, err := service.FeatureDistributionSnapshots(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].SnapshotKey != "live-temp" || len(list[0].BinProportions) != 2 {
		t.Fatalf("unexpected distribution snapshots: %+v", list)
	}
	handler := NewHandler(service)
	response := httptest.NewRecorder()
	handler.HandleFeatureDistributions(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/history/feature-distributions?limit=2", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"raw_samples_stored":false`)) || !bytes.Contains(response.Body.Bytes(), []byte("live_shadow")) {
		t.Fatalf("feature distribution handler failed: %d %s", response.Code, response.Body.String())
	}
}

func TestMaterializeTrainingFeatureDistributionsFromBaselineMatrix(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:feature-distribution-materialize-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	matrixDir := filepath.Join(dir, "matrix")
	baselineDir := filepath.Join(dir, "baseline")
	if err := os.MkdirAll(matrixDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(baselineDir, 0o750); err != nil {
		t.Fatal(err)
	}
	rows := []trainingMatrixRow{
		{RowKey: "train-1", Split: "train", HorizonMinutes: 10080, ModelName: "NVIDIA H100 80GB HBM3", Features: map[string]float64{"gpu_temp_mean_24h": 40, "power_usage_mean_24h": 200}},
		{RowKey: "train-2", Split: "train", HorizonMinutes: 10080, ModelName: "NVIDIA H100 80GB HBM3", Features: map[string]float64{"gpu_temp_mean_24h": 50}},
		{RowKey: "test-1", Split: "test", HorizonMinutes: 10080, ModelName: "NVIDIA H100 80GB HBM3", Features: map[string]float64{"gpu_temp_mean_24h": 100, "power_usage_mean_24h": 260}},
	}
	matrixPath := filepath.Join(matrixDir, "training_matrix.jsonl")
	matrixSHA, err := writeJSONLines(matrixPath, rows)
	if err != nil {
		t.Fatal(err)
	}
	matrix := api.TrainingMatrixBuild{
		TrainingMatrixKey: "matrix-v4", Version: trainingMatrixVersion, Status: "completed",
		FeatureContractVersion: "atlas-prediction-features-v1", MatrixPath: matrixPath, MatrixSHA256: matrixSHA,
	}
	if err := db.Create(&matrix).Error; err != nil {
		t.Fatal(err)
	}
	artifact := baselineArtifact{Version: baselineModelVersion, Models: []logisticModel{{HorizonMinutes: 10080, FeatureColumns: []string{"gpu_temp_mean_24h", "power_usage_mean_24h"}}}}
	artifactPath := filepath.Join(baselineDir, "models.json")
	if err := writeJSONAtomic(artifactPath, artifact); err != nil {
		t.Fatal(err)
	}
	artifactSHA, err := fileSHA256(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 18, 10, 0, 0, time.UTC)
	baseline := api.BaselineModelBuild{
		BaselineModelKey: "baseline-v9", Version: baselineModelVersion, Status: "completed",
		SourceMatrixBuildID: matrix.ID, SourceTrainingMatrixKey: matrix.TrainingMatrixKey,
		FeatureContractVersion: matrix.FeatureContractVersion, FeatureColumnCount: 2,
		TrainCount: 2, TestCount: 1, ArtifactPath: artifactPath, ArtifactSHA256: artifactSHA, FinishedAt: &now,
	}
	if err := db.Create(&baseline).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{DatasetDir: dir}, time.Second)
	service.now = func() time.Time { return now.Add(time.Minute) }
	snapshots, err := service.MaterializeTrainingFeatureDistributions(FeatureDistributionSnapshotRequest{SourceBaselineBuildID: baseline.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("snapshots=%d", len(snapshots))
	}
	byFeature := map[string]api.PredictionFeatureDistributionSnapshot{}
	for _, snapshot := range snapshots {
		byFeature[snapshot.FeatureName] = snapshot
		if snapshot.Version != featureDistributionSnapshotVersion || snapshot.DistributionRole != "training" || snapshot.ReportSHA256 == "" {
			t.Fatalf("unexpected snapshot metadata: %+v", snapshot)
		}
	}
	if got := byFeature["gpu_temp_mean_24h"]; got.SampleCount != 2 || got.Mean != 45 || got.Maximum != 50 {
		t.Fatalf("temperature snapshot used wrong row scope: %+v", got)
	}
	if got := byFeature["power_usage_mean_24h"]; got.SampleCount != 1 || got.MissingCount != 1 || got.MissingRatio != 0.5 || len(got.BinProportions) != 1 {
		t.Fatalf("power snapshot missing accounting failed: %+v", got)
	}
	response := httptest.NewRecorder()
	body, _ := json.Marshal(FeatureDistributionSnapshotRequest{SourceBaselineBuildID: baseline.ID})
	handler := NewHandler(service)
	handler.HandleFeatureDistributions(response, httptest.NewRequest(http.MethodPost, "/api/v1/prediction/history/feature-distributions", bytes.NewReader(body)))
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"raw_samples_stored":false`) || !strings.Contains(response.Body.String(), `"actions_executed":false`) {
		t.Fatalf("POST materialize status=%d body=%s", response.Code, response.Body.String())
	}
	var persisted []api.PredictionFeatureDistributionSnapshot
	if err := db.Order("feature_name ASC").Find(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 2 {
		t.Fatalf("persisted=%d", len(persisted))
	}
}

func TestLiveShadowFeatureDistributionsReuseTrainingHistogramBins(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:live-feature-distribution-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 18, 40, 0, 0, time.UTC)
	training := api.PredictionFeatureDistributionSnapshot{
		SnapshotKey: "training-temp", Version: featureDistributionSnapshotVersion, Status: "completed", DistributionRole: "training",
		SourceBaselineBuildID: 7, FeatureContractVersion: "atlas-prediction-features-v1", FeatureName: "gpu_temp_mean_24h",
		SampleCount: 10, BinEdges: api.FloatList{0, 10, 20}, BinProportions: api.FloatList{0.4, 0.6}, ReportSHA256: "training-sha", ObservedAt: now.Add(-time.Hour),
	}
	if err := db.Create(&training).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{}, time.Second)
	spec := api.PredictionModelSpec{
		ID: 3, ModelKey: "gpu-xid-h100-7d", Version: "gpu-logistic-baseline-v9.build-7",
		SourceBaselineBuildID: 7, FeatureContractVersion: "atlas-prediction-features-v1", ScopeModelName: "NVIDIA H100 80GB HBM3",
	}
	run := api.PredictionShadowScoringRun{
		ID: 9, RunKey: "gpu-shadow-scoring-v3-1", ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version,
		ScoredGPUCount: 3, NoAlertEmitted: true, NoActionExecuted: true,
	}
	snapshots, err := service.liveShadowFeatureDistributionSnapshots(spec, run, []string{"gpu_temp_mean_24h"}, map[string][]float64{"gpu_temp_mean_24h": []float64{-5, 5, 25}}, now, "run-sha")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshots=%d", len(snapshots))
	}
	snapshot := snapshots[0]
	if snapshot.DistributionRole != "live_shadow" || snapshot.Status != "completed" || snapshot.SourceBaselineBuildID != 7 || snapshot.ReportSHA256 == "" {
		t.Fatalf("unexpected live snapshot metadata: %+v", snapshot)
	}
	if fmt.Sprint([]float64(snapshot.BinEdges)) != fmt.Sprint([]float64{0, 10, 20}) {
		t.Fatalf("live snapshot did not reuse training bins: %+v", snapshot.BinEdges)
	}
	if len(snapshot.BinProportions) != 2 || math.Abs(snapshot.BinProportions[0]-(2.0/3.0)) > 1e-12 || math.Abs(snapshot.BinProportions[1]-(1.0/3.0)) > 1e-12 {
		t.Fatalf("unexpected live proportions: %+v", snapshot.BinProportions)
	}
	if snapshot.SampleCount != 3 || snapshot.MissingCount != 0 {
		t.Fatalf("unexpected sample accounting: %+v", snapshot)
	}
}

func TestHistoryHandlerIsReadOnlyExceptExplicitAudit(t *testing.T) {
	prometheus := newTestPrometheus(t)
	defer prometheus.Close()
	db, err := storage.InitDB(fmt.Sprintf("file:history-handler-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{
		DatasetDir: "/mnt/public/atlas/training",
		Sources: []config.HistorySourceConfig{{
			ID: "primary", Name: "Primary", Type: "prometheus", BaseURL: prometheus.URL, Enabled: true,
		}},
	}, time.Second)
	handler := NewHandler(service)

	response := httptest.NewRecorder()
	handler.HandleSources(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/history/sources", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"execution":"atlas_deployment_node"`) {
		t.Fatalf("GET sources status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleAudits(response, httptest.NewRequest(http.MethodPost, "/api/v1/prediction/history/audits", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"source_version":"2.54.0"`) {
		t.Fatalf("POST audit status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleCandidates(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/history/candidates", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":"gpu-training-cohort-v2"`) {
		t.Fatalf("GET candidates status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHistoryCandidateReviewHandler(t *testing.T) {
	db, err := storage.InitDB(fmt.Sprintf("file:history-review-handler-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	candidate := api.HistoricalFaultCandidate{
		CandidateKey: "handler-review-test", SourceKey: "primary", BackfillRunID: 1,
		EntityType: "gpu", GPUUUID: "GPU-1", EventType: "xid_79_gpu_fallen_off_bus", EventCode: "79",
		QualityTier: "strong_proxy", OperationalPriority: "high",
		HardwareCertainty: "investigation_required", TrainingDisposition: "proxy_positive_after_review",
		ReviewStatus: "pending_review", SourceMetric: "ALERTS",
		OnsetAt: time.Now(), DetectionWindowEndAt: time.Now(),
		Labels: api.StringMap{"alert_template": "XID故障-高优先级", "err_code": "79"},
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(NewService(db, config.HistoryConfig{}, time.Second))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch,
		fmt.Sprintf("/api/v1/prediction/history/candidates/%d", candidate.ID),
		bytes.NewBufferString(`{"status":"accepted_proxy","note":"matched incident evidence","reviewed_by":"tester"}`))
	handler.HandleCandidate(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"review_status":"accepted_proxy"`) ||
		!strings.Contains(response.Body.String(), `"reviewed_by":"tester"`) {
		t.Fatalf("PATCH candidate status=%d body=%s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.HandleCandidate(response, httptest.NewRequest(http.MethodPatch,
		"/api/v1/prediction/history/candidates/not-a-number", bytes.NewBufferString(`{}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid candidate id status=%d body=%s", response.Code, response.Body.String())
	}
}

func newTestPrometheus(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/status/buildinfo":
			_, _ = w.Write([]byte(`{"status":"success","data":{"version":"2.54.0","revision":"abc"}}`))
		case "/api/v1/status/flags":
			_, _ = w.Write([]byte(`{"status":"success","data":{"storage.tsdb.retention.time":"5y","query.max-samples":"50000000","query.max-concurrency":"20"}}`))
		case "/api/v1/targets":
			_, _ = w.Write([]byte(`{"status":"success","data":{"activeTargets":[{"labels":{"job":"dcgm_exporter"},"health":"up"},{"labels":{"job":"gpu_exporter"},"health":"up"}]}}`))
		case "/api/v1/label/__name__/values":
			_, _ = w.Write([]byte(`{"status":"success","data":["DCGM_FI_DEV_GPU_TEMP","DCGM_FI_DEV_GPU_UTIL","DCGM_FI_DEV_XID_ERRORS"]}`))
		case "/api/v1/query":
			query := r.URL.Query().Get("query")
			value := "0"
			timestamp := "1785398400"
			switch query {
			case "count(count by(UUID) (DCGM_FI_DEV_GPU_UTIL))":
				value = "720"
			case "quantile(0.5, count_over_time(DCGM_FI_DEV_GPU_UTIL[5m]))":
				value = "20"
			case "max(timestamp(DCGM_FI_DEV_GPU_UTIL))":
				value = timestamp
			default:
				t.Fatalf("unexpected query %q", query)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[%s,"%s"]}]}}`, timestamp, value)))
		case "/api/v1/query_range":
			if strings.Contains(r.URL.Query().Get("query"), "ALERTS") {
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
					{"metric":{"__name__":"ALERTS","UUID":"GPU-1","instance":"10.0.0.1:9400","modelName":"NVIDIA H100","pci_bus_id":"0000:01:00.0","alertname":"hash-critical","alert_template":"XID故障-高优先级","alertstate":"firing","err_code":"79","err_msg":"GPU has fallen off the bus","severity":"紧急"},"values":[[1782864000,"1"],[1782864060,"1"],[1782864120,"1"],[1782871200,"1"]]},
					{"metric":{"__name__":"ALERTS","UUID":"GPU-2","instance":"10.0.0.2:9400","device_type":"H100","alertname":"hash-ecc","alert_template":"XID故障-高优先级","alertstate":"firing","err_code":"94","err_msg":"Contained ECC error","severity":"紧急"},"values":[[1782864300,"1"],[1782864360,"1"]]}
				]}}`))
			} else if strings.Contains(r.URL.Query().Get("query"), "DCGM_FI_DEV_GPU_UTIL") {
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
					{"metric":{"__name__":"DCGM_FI_DEV_GPU_UTIL","UUID":"GPU-OLD","host_ip":"10.0.0.9","Hostname":"gpu-9","host_id":"HOST-9","sn":"SN-9","gpu":"0","modelName":"NVIDIA H100","pci_bus_id":"0000:01:00.0","DCGM_FI_DRIVER_VERSION":"560.35.03","job":"dcgm_exporter"},"values":[[1782864000,"0"],[1783123200,"10"]]},
					{"metric":{"__name__":"DCGM_FI_DEV_GPU_UTIL","UUID":"GPU-OLD","host_ip":"10.0.0.9","Hostname":"gpu-9","host_id":"HOST-9","gpu":"0","modelName":"NVIDIA H100","pci_bus_id":"0000:01:00.0","DCGM_FI_DRIVER_VERSION":"560.35.03","job":"gpu_exporter"},"values":[[1782864000,"0"],[1783123200,"10"]]},
					{"metric":{"__name__":"DCGM_FI_DEV_GPU_UTIL","UUID":"GPU-NEW","host_ip":"10.0.0.9","Hostname":"gpu-9","host_id":"HOST-9","sn":"SN-9","gpu":"0","modelName":"NVIDIA H100","pci_bus_id":"0000:01:00.0","DCGM_FI_DRIVER_VERSION":"560.35.03","job":"dcgm_exporter"},"values":[[1783209600,"0"],[1783382400,"20"]]}
				]}}`))
			} else if strings.Contains(r.URL.Query().Get("query"), "UNCORRECTABLE_REMAPPED_ROWS") {
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
					{"metric":{"__name__":"DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS","UUID":"GPU-3","instance":"10.0.0.3:9400","modelName":"NVIDIA H100","pci_bus_id":"0000:03:00.0"},"values":[[1782864500,"8"]]}
				]}}`))
			} else {
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1750000000,"0"],[1756425600,"472"]]}]}}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

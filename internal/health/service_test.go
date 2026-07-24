package health

import (
	"context"
	"strings"
	"testing"
	"time"

	"atlas/internal/prometheus"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

type fakePrometheus struct {
	values       map[string]float64
	sourceValues map[string]map[string]float64
}

func (f *fakePrometheus) BaseURL() string { return "http://prometheus.test" }
func (f *fakePrometheus) Query(_ context.Context, query string) ([]prometheus.Sample, error) {
	for _, spec := range metricSpecs {
		if spec.query == query {
			values := f.values
			if f.sourceValues != nil {
				values = f.sourceValues[spec.source]
			}
			value, ok := values[spec.key]
			if !ok {
				return nil, nil
			}
			labels := map[string]string{"UUID": "GPU-A"}
			if spec.source == "gpu_exporter" {
				labels = map[string]string{"uuid": strings.TrimPrefix("GPU-A", "GPU-")}
			}
			return []prometheus.Sample{{Metric: labels, Value: value, Timestamp: time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)}}, nil
		}
	}
	return nil, nil
}

func TestEvaluatePersistsScoredAndUnknownAssets(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	node := api.GPUNode{NodeIP: "10.114.4.21", Lifecycle: "active"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	assets := []api.GPUAsset{
		{AssetKey: "10.114.4.21:0", NodeID: node.ID, NodeIP: node.NodeIP, GPUIndex: 0, CurrentUUID: "GPU-A", ModelName: "NVIDIA H100 80GB HBM3", State: "active"},
		{AssetKey: "10.114.4.21:1", NodeID: node.ID, NodeIP: node.NodeIP, GPUIndex: 1, CurrentUUID: "GPU-B", ModelName: "NVIDIA H100 80GB HBM3", State: "history_only"},
	}
	if err := db.Create(&assets).Error; err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, spec := range metricSpecs {
		values[spec.key] = 0
	}
	values["gpu_temp"] = 60
	values["gpu_temp_max_15m"] = 65
	values["memory_temp"] = 65
	values["memory_temp_max_15m"] = 70
	values["xid_current"] = 43
	values["xid_changes_24h"] = 1
	service := NewService(db, &fakePrometheus{values: values}, config.HealthConfig{RuleVersion: "test-v1"})
	now := time.Date(2026, 7, 20, 8, 1, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	run, err := service.Evaluate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.AssetCount != 2 || run.ScoredCount != 1 || run.UnknownCount != 1 || run.RuleHitCount != 1 {
		t.Fatalf("unexpected run: %+v", run)
	}
	var scores []api.GPUHealthScore
	db.Where("current = ?", true).Order("gpu_index").Find(&scores)
	if len(scores) != 2 || scores[0].Score == nil || *scores[0].Score != 65 || scores[1].Score != nil || scores[1].Level != "unknown" {
		t.Fatalf("unexpected scores: %+v", scores)
	}
	var snapshot api.GPUFeatureSnapshot
	if err := db.Where("gpu_uuid = ?", "GPU-A").First(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	if snapshot.FeatureCatalogVersion != "1.5.0" || snapshot.FeatureVersions["gpu_temp"] != "1.5.0" || snapshot.MetricSources["gpu_temp"] != "dcgm_exporter" {
		t.Fatalf("snapshot is missing feature lineage: %+v", snapshot)
	}
}

func TestEvaluateUsesGPUExporterFallbackAndDegradesConfidence(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	node := api.GPUNode{NodeIP: "10.114.4.21", Lifecycle: "active"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	asset := api.GPUAsset{AssetKey: "10.114.4.21:0", NodeID: node.ID, NodeIP: node.NodeIP, GPUIndex: 0, CurrentUUID: "GPU-A", ModelName: "NVIDIA H100 80GB HBM3", State: "active"}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	sourceValues := map[string]map[string]float64{"dcgm_exporter": {}, "gpu_exporter": {}}
	for _, spec := range metricSpecs {
		sourceValues[spec.source][spec.key] = 0
	}
	delete(sourceValues["dcgm_exporter"], "gpu_temp")
	sourceValues["gpu_exporter"]["gpu_temp"] = 61
	service := NewService(db, &fakePrometheus{sourceValues: sourceValues}, config.HealthConfig{RuleVersion: "test-v1"})
	service.now = func() time.Time { return time.Date(2026, 7, 20, 8, 1, 0, 0, time.UTC) }
	if _, err := service.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var snapshot api.GPUFeatureSnapshot
	if err := db.Where("gpu_asset_id = ?", asset.ID).First(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	if snapshot.Metrics["gpu_temp"] != 61 || snapshot.MetricSources["gpu_temp"] != "gpu_exporter" || snapshot.FallbackMetricCount != 1 || snapshot.DataConfidence != "B" {
		t.Fatalf("unexpected fallback snapshot: %+v", snapshot)
	}
}

func TestMergeFeatureCandidatesPrefersDCGMAndDetectsMismatch(t *testing.T) {
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	value := mergeFeatureCandidates(map[string]map[string]metricObservation{
		"gpu_temp_max_15m": {
			"dcgm_exporter": {value: 60, observedAt: now},
			"gpu_exporter":  {value: 70, observedAt: now},
		},
	})
	if value.metrics["gpu_temp_max_15m"] != 60 || value.sources["gpu_temp_max_15m"] != "dcgm_exporter" || value.fallbackCount != 0 || len(value.sourceDifferences) != 1 {
		t.Fatalf("unexpected merged value: %+v", value)
	}
	if confidence := degradeConfidence("A", value.fallbackCount); confidence != "A" {
		t.Fatalf("source difference must not degrade confidence, got %s", confidence)
	}
}

func TestSourceDifferenceIsAuditOnly(t *testing.T) {
	if confidence := degradeConfidence("A", 0); confidence != "A" {
		t.Fatalf("audit-only source difference changed confidence: %s", confidence)
	}
}

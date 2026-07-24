package degradation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/internal/features"
	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestEvaluateFindsLowClockCandidateWithoutChangingHealth(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	for index, clock := range []float64{1500, 1510, 1490, 900} {
		snapshot := api.GPUFeatureSnapshot{
			GPUAssetID: uint(index + 1), GPUUUID: "GPU-TEST", NodeIP: "10.114.4.21", GPUIndex: index,
			ModelName: "NVIDIA H100 80GB HBM3", Metrics: api.FloatMap{"gpu_util_avg_15m": 90, "sm_clock_avg_15m": clock},
			FeatureVersions: api.StringMap{"sm_clock_avg_15m": "1.1.0"}, DataConfidence: "A", ObservedAt: now,
		}
		if err := db.Create(&snapshot).Error; err != nil {
			t.Fatal(err)
		}
		scoreValue := 100
		score := api.GPUHealthScore{FeatureSnapshotID: snapshot.ID, GPUAssetID: snapshot.GPUAssetID, NodeIP: snapshot.NodeIP, GPUIndex: index, ModelName: snapshot.ModelName, Score: &scoreValue, Level: "healthy", DataConfidence: "A", Current: true}
		if err := db.Create(&score).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db)
	service.now = func() time.Time { return now.Add(5 * time.Minute) }
	summary, candidates, err := service.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	if summary.EligibleGPUs != 4 || summary.CandidateGPUs != 1 || len(candidates) != 1 {
		t.Fatalf("unexpected result summary=%+v candidates=%+v", summary, candidates)
	}
	candidate := candidates[0]
	if candidate.GPUIndex != 3 || candidate.BaselineScope != "same_node_model" || candidate.PerformanceRatio >= ratioThreshold || candidate.DataConfidence != "C" {
		t.Fatalf("unexpected candidate: %+v", candidate)
	}
	var original api.GPUHealthScore
	if err := db.Where("gpu_index = ?", 3).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	if original.Score == nil || *original.Score != 100 || original.Level != "healthy" {
		t.Fatalf("shadow evaluation changed health: %+v", original)
	}
}

func TestHandlersExposeShadowContract(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	response := httptest.NewRecorder()
	handler.HandleSummary(response, httptest.NewRequest(http.MethodGet, "/api/v1/degradation/summary", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data api.GPUDegradationSummary `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Version != Version || payload.Data.Mode != "shadow" {
		t.Fatalf("unexpected contract: %+v", payload.Data)
	}
	if payload.Data.FreshnessSLASeconds != int64(freshnessSLA.Seconds()) {
		t.Fatalf("freshness contract missing: %+v", payload.Data)
	}

	response = httptest.NewRecorder()
	handler.HandleCandidates(response, httptest.NewRequest(http.MethodPost, "/api/v1/degradation/candidates", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("write-like request should be rejected, status=%d", response.Code)
	}
}

func TestEvaluateExcludesStaleSnapshots(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	for index := 0; index < 4; index++ {
		snapshot := api.GPUFeatureSnapshot{
			GPUAssetID: uint(index + 1), NodeIP: "10.114.4.21", GPUIndex: index, ModelName: "H100",
			Metrics: api.FloatMap{"gpu_util_avg_15m": 90, "sm_clock_avg_15m": 1000}, DataConfidence: "A",
			ObservedAt: now.Add(-freshnessSLA - time.Second),
		}
		if err := db.Create(&snapshot).Error; err != nil {
			t.Fatal(err)
		}
		scoreValue := 100
		score := api.GPUHealthScore{
			FeatureSnapshotID: snapshot.ID, GPUAssetID: snapshot.GPUAssetID, NodeIP: snapshot.NodeIP,
			GPUIndex: index, ModelName: snapshot.ModelName, Score: &scoreValue, DataConfidence: "A", Current: true,
		}
		if err := db.Create(&score).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	summary, candidates, err := service.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	if summary.EligibleGPUs != 0 || summary.InsufficientGPUs != 4 || len(candidates) != 0 {
		t.Fatalf("stale snapshots should be excluded: summary=%+v candidates=%+v", summary, candidates)
	}
}

func TestEvaluateRequiresLoadAndPeers(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := api.GPUFeatureSnapshot{GPUAssetID: 1, NodeIP: "10.114.4.21", GPUIndex: 0, ModelName: "H100", Metrics: api.FloatMap{"gpu_util_avg_15m": 20, "sm_clock_avg_15m": 500}, DataConfidence: "A", ObservedAt: time.Now()}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	scoreValue := 100
	if err := db.Create(&api.GPUHealthScore{FeatureSnapshotID: snapshot.ID, GPUAssetID: 1, Score: &scoreValue, DataConfidence: "A", Current: true}).Error; err != nil {
		t.Fatal(err)
	}
	summary, candidates, err := NewService(db).Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	if summary.EligibleGPUs != 0 || len(candidates) != 0 {
		t.Fatalf("low-load GPU became candidate: summary=%+v candidates=%+v", summary, candidates)
	}
}

func TestEvaluatePrefersMatureHistoricalBaselineWithoutLivePeers(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	snapshot := api.GPUFeatureSnapshot{
		GPUAssetID: 1, GPUUUID: "GPU-A", NodeIP: "10.114.4.21", GPUIndex: 0,
		ModelName:       "NVIDIA H100 80GB HBM3",
		Metrics:         api.FloatMap{"gpu_util_avg_15m": 90, "sm_clock_avg_15m": 900},
		FeatureVersions: api.StringMap{"sm_clock_avg_15m": features.CatalogVersion},
		DataConfidence:  "A", ObservedAt: now,
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	scoreValue := 100
	if err := db.Create(&api.GPUHealthScore{
		FeatureSnapshotID: snapshot.ID, GPUAssetID: 1, GPUUUID: "GPU-A",
		NodeIP: snapshot.NodeIP, GPUIndex: 0, ModelName: snapshot.ModelName,
		Score: &scoreValue, DataConfidence: "A", Current: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	baseline := api.GPUFeatureBaseline{
		ContractVersion: features.BaselineContractVersion,
		FeatureName:     "sm_clock_avg_15m", FeatureVersion: features.CatalogVersion,
		ModelName: snapshot.ModelName, LoadBucket: "high", WindowDays: 7,
		SampleCount: 400, GPUCount: 8, P05: 1400, P50: 1500, P95: 1550, MAD: 20,
		Maturity: "mature", WindowStartedAt: now.Add(-7 * 24 * time.Hour), WindowEndedAt: now,
		ComputedAt: now, Owner: "atlas-ml",
	}
	if err := db.Create(&baseline).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return now.Add(5 * time.Minute) }
	summary, candidates, err := service.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	if summary.BaselineReadyGPUs != 1 || summary.HistoricalBaselineGPUs != 1 || len(candidates) != 1 {
		t.Fatalf("unexpected historical baseline result summary=%+v candidates=%+v", summary, candidates)
	}
	candidate := candidates[0]
	if candidate.BaselineID != baseline.ID || candidate.BaselineScope != "same_model_high_load_7d" ||
		candidate.BaselineMaturity != "mature" || candidate.BaselineValue != 1500 || candidate.DataConfidence != "A" {
		t.Fatalf("unexpected historical candidate: %+v", candidate)
	}
}

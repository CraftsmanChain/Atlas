package features

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestRefreshHistoricalBaselinesBuildsRobustModelLoadContract(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	for gpu := 1; gpu <= 4; gpu++ {
		for sample := 0; sample < 6; sample++ {
			snapshot := api.GPUFeatureSnapshot{
				GPUAssetID: uint(gpu), GPUUUID: "GPU-TEST", ModelName: "NVIDIA H100 80GB HBM3",
				Metrics:               api.FloatMap{"gpu_util_avg_15m": 90, "sm_clock_avg_15m": float64(1400 + gpu*10 + sample)},
				FeatureCatalogVersion: CatalogVersion,
				FeatureVersions:       api.StringMap{"sm_clock_avg_15m": CatalogVersion},
				DataConfidence:        "A", ObservedAt: now.Add(-time.Duration(sample) * 6 * time.Hour),
			}
			if err := db.Create(&snapshot).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := RefreshHistoricalBaselines(db, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Refreshed || result.Run.Status != "success" || result.Run.SnapshotCount != 24 || result.Run.BaselineCount != 1 {
		t.Fatalf("unexpected refresh result: %+v", result)
	}
	if result.Run.DurationMillis < 0 || result.Run.FinishedAt == nil || result.Run.FinishedAt.Before(result.Run.StartedAt) {
		t.Fatalf("refresh duration provenance is invalid: %+v", result.Run)
	}
	baselines, err := ListBaselines(db, BaselineListOptions{FeatureName: "sm_clock_avg_15m", LoadBucket: "high"})
	if err != nil || len(baselines) != 1 {
		t.Fatalf("unexpected baselines count=%d err=%v", len(baselines), err)
	}
	baseline := baselines[0]
	if baseline.Maturity != "mature" || baseline.GPUCount != 4 || baseline.SampleCount != 24 ||
		baseline.P05 <= 0 || baseline.P50 <= baseline.P05 || baseline.P95 <= baseline.P50 || baseline.MAD <= 0 {
		t.Fatalf("unexpected robust baseline: %+v", baseline)
	}
}

func TestRefreshHistoricalBaselinesSkipsUntilRefreshInterval(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	first, err := RefreshHistoricalBaselines(db, now, true)
	if err != nil || !first.Refreshed {
		t.Fatalf("first refresh=%+v err=%v", first, err)
	}
	second, err := RefreshHistoricalBaselines(db, now.Add(time.Hour), false)
	if err != nil || second.Refreshed || second.Run.ID != first.Run.ID {
		t.Fatalf("refresh should be rate-limited: first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestBaselineHandlerFiltersAndExposesRefreshProvenance(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	finished := now
	if err := db.Create(&api.FeatureBaselineRefreshRun{
		Status: "success", ContractVersion: BaselineContractVersion, WindowDays: baselineWindowDays,
		BaselineCount: 1, StartedAt: now, FinishedAt: &finished,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.GPUFeatureBaseline{
		ContractVersion: BaselineContractVersion, FeatureName: "sm_clock_avg_15m", FeatureVersion: CatalogVersion,
		ModelName: "H100", LoadBucket: "high", WindowDays: baselineWindowDays,
		SampleCount: 24, GPUCount: 4, P05: 1400, P50: 1450, P95: 1500, MAD: 20,
		Maturity: "mature", WindowStartedAt: now.Add(-24 * time.Hour), WindowEndedAt: now,
		ComputedAt: now, Owner: "atlas-ml",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.GPUFeatureBaseline{
		ContractVersion: BaselineContractVersion, FeatureName: "sm_clock_avg_15m", FeatureVersion: "experimental-reused-version",
		ModelName: "H100", LoadBucket: "high", WindowDays: baselineWindowDays,
		SampleCount: 400, GPUCount: 8, P05: 1300, P50: 1400, P95: 1500, MAD: 30,
		Maturity: "mature", WindowStartedAt: now.Add(-7 * 24 * time.Hour), WindowEndedAt: now,
		ComputedAt: now, Owner: "atlas-ml",
	}).Error; err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewBaselineHandler(db).HandleCollection(response, httptest.NewRequest(http.MethodGet, "/api/v1/features/baselines?maturity=mature&load_bucket=high", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []api.GPUFeatureBaseline `json:"data"`
		Meta struct {
			ContractVersion string                         `json:"contract_version"`
			LatestRefresh   *api.FeatureBaselineRefreshRun `json:"latest_refresh"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Meta.ContractVersion != BaselineContractVersion || payload.Meta.LatestRefresh == nil {
		t.Fatalf("unexpected response: %+v", payload)
	}

	response = httptest.NewRecorder()
	NewBaselineHandler(db).HandleCollection(response, httptest.NewRequest(http.MethodGet, "/api/v1/features/baselines?version=all", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("all-version status=%d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 2 {
		t.Fatalf("version=all must explicitly include historical baselines: %+v", payload.Data)
	}
}

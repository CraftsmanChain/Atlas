package prediction

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

func TestFrameworkSeedsHorizonContractsWithoutPretendingToScore(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := SeedBuiltins(db); err != nil {
		t.Fatal(err)
	}
	if err := SeedBuiltins(db); err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC) }
	overview, err := service.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Models) != 3 || overview.ScoringEnabled || overview.ProbabilityEmitted || !overview.NoActionExecuted {
		t.Fatalf("unsafe or incomplete framework overview: %+v", overview)
	}
	if overview.Results.Total != 0 || overview.Results.ProbabilityEmitted {
		t.Fatalf("empty framework emitted prediction results: %+v", overview.Results)
	}
	for _, model := range overview.Models {
		if model.Status != "data_readiness" || model.Algorithm != "unselected" || model.ArtifactURI != "" || model.DecisionThreshold != nil {
			t.Fatalf("untrained model was represented as released: %+v", model)
		}
	}
}

func TestReadinessUsesPointInTimeFeatureQualityGates(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	readySnapshot := api.GPUFeatureSnapshot{
		GPUAssetID: 1, GPUUUID: "GPU-READY", NodeIP: "10.114.4.21", GPUIndex: 0, ModelName: "H100",
		FeatureCatalogVersion: features.CatalogVersion, AvailableMetricCount: 32, ExpectedMetricCount: 35,
		DataConfidence: "A", ObservedAt: now.Add(-5 * time.Minute),
	}
	blockedSnapshot := api.GPUFeatureSnapshot{
		GPUAssetID: 2, NodeIP: "10.114.4.22", GPUIndex: 1, ModelName: "H100",
		FeatureCatalogVersion: features.CatalogVersion, AvailableMetricCount: 10, ExpectedMetricCount: 35,
		ConsistencyIssueCount: 1, DataConfidence: "C", ObservedAt: now.Add(-time.Hour),
	}
	if err := db.Create(&readySnapshot).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&blockedSnapshot).Error; err != nil {
		t.Fatal(err)
	}
	score := 100
	scores := []api.GPUHealthScore{
		{FeatureSnapshotID: readySnapshot.ID, GPUAssetID: 1, GPUUUID: "GPU-READY", NodeIP: "10.114.4.21", GPUIndex: 0, ModelName: "H100", Score: &score, DataConfidence: "A", Current: true},
		{FeatureSnapshotID: blockedSnapshot.ID, GPUAssetID: 2, NodeIP: "10.114.4.22", GPUIndex: 1, ModelName: "H100", Score: &score, DataConfidence: "C", Current: true},
	}
	if err := db.Create(&scores).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	summary, rows, err := service.Readiness()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 2 || summary.ReadyForDataset != 1 || summary.Blocked != 1 || summary.ProbabilityEmitted {
		t.Fatalf("unexpected readiness summary: %+v", summary)
	}
	if rows[0].Status != "blocked" || len(rows[0].BlockingReasons) < 4 || rows[0].ProbabilityEmitted || !rows[0].NoActionExecuted {
		t.Fatalf("blocked GPU lost gate evidence: %+v", rows[0])
	}
	if rows[1].Status != "ready_for_dataset" || rows[1].FeatureCoverage < minimumFeatureCoverage {
		t.Fatalf("ready GPU was not recognized: %+v", rows[1])
	}
}

func TestPredictionHandlersExposeReadOnlyFramework(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := SeedBuiltins(db); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	response := httptest.NewRecorder()
	handler.HandleOverview(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/overview", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("overview status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data Overview `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.FrameworkVersion != FrameworkVersion || payload.Data.Mode != "shadow" {
		t.Fatalf("unexpected framework contract: %+v", payload.Data)
	}
	response = httptest.NewRecorder()
	handler.HandleModels(response, httptest.NewRequest(http.MethodPost, "/api/v1/prediction/models", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("write-like model request should be rejected: %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.HandleResults(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/results", nil))
	if response.Code != http.StatusOK || response.Body.String() == "" {
		t.Fatalf("results contract unavailable: %d %s", response.Code, response.Body.String())
	}
}

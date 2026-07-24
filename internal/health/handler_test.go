package health

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestHealthAPIExposesFallbackAndSourceDifferenceProvenance(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := api.GPUFeatureSnapshot{MetricSources: api.StringMap{"gpu_temp": "gpu_exporter"}, SourcesAvailable: api.StringList{"dcgm_exporter", "gpu_exporter"}, FallbackMetricCount: 1, ConsistencyCandidates: api.StringList{"gpu_temp_max_15m: dcgm=60 gpu_exporter=70"}, ConsistencyCandidateCount: 1, ObservedAt: time.Now()}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	scoreValue := 100
	score := api.GPUHealthScore{FeatureSnapshotID: snapshot.ID, GPUAssetID: 1, GPUUUID: "GPU-A", Score: &scoreValue, Level: "healthy", DataConfidence: "C", Current: true, EvaluatedAt: time.Now()}
	if err := db.Create(&score).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	response := httptest.NewRecorder()
	handler.HandleScores(response, httptest.NewRequest("GET", "/api/v1/health/gpus", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"fallback_metric_count":1`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"gpu_temp":"gpu_exporter"`)) {
		t.Fatalf("unexpected scores response: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleSummary(response, httptest.NewRequest("GET", "/api/v1/health/summary", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"fallback_gpus":1`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"source_difference_gpus":1`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"inconsistent_gpus":0`)) {
		t.Fatalf("unexpected summary response: %d %s", response.Code, response.Body.String())
	}
}

func TestFaultEventCursorPaginationIsStableAcrossUpdatesAndInserts(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 7; i++ {
		event := api.GPUFaultEvent{EpisodeKey: fmt.Sprintf("episode-%d", i), State: "open", Severity: "warning", RuleCode: "TEST", FirstObservedAt: now, LastObservedAt: now}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}
	handler := NewHandler(db)
	type responsePayload struct {
		Data []api.GPUFaultEvent `json:"data"`
		Meta struct {
			Total        int64 `json:"total"`
			HasMore      bool  `json:"has_more"`
			NextBeforeID uint  `json:"next_before_id"`
		} `json:"meta"`
	}
	fetch := func(path string) responsePayload {
		t.Helper()
		response := httptest.NewRecorder()
		handler.HandleEvents(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var payload responsePayload
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	first := fetch("/api/v1/fault-events?limit=3")
	if len(first.Data) != 3 || first.Data[0].ID != 7 || first.Meta.NextBeforeID != 5 || !first.Meta.HasMore {
		t.Fatalf("unexpected first page: %+v", first)
	}
	// Mutable fields must not affect ordering, and a new row must stay ahead of
	// the traversal cursor rather than reappearing on a later page.
	if err := db.Model(&api.GPUFaultEvent{}).Where("id = ?", 4).Updates(map[string]any{"severity": "critical", "last_observed_at": now.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.GPUFaultEvent{EpisodeKey: "new", State: "open", Severity: "critical", RuleCode: "NEW", FirstObservedAt: now, LastObservedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	second := fetch(fmt.Sprintf("/api/v1/fault-events?limit=3&before_id=%d", first.Meta.NextBeforeID))
	third := fetch(fmt.Sprintf("/api/v1/fault-events?limit=3&before_id=%d", second.Meta.NextBeforeID))
	got := []uint{}
	for _, item := range append(append(first.Data, second.Data...), third.Data...) {
		got = append(got, item.ID)
	}
	if fmt.Sprint(got) != fmt.Sprint([]uint{7, 6, 5, 4, 3, 2, 1}) {
		t.Fatalf("got IDs %v", got)
	}
}

func TestFaultEventPaginationRejectsInvalidCursor(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	for _, path := range []string{"/api/v1/fault-events?limit=0", "/api/v1/fault-events?limit=x", "/api/v1/fault-events?before_id=-1"} {
		response := httptest.NewRecorder()
		handler.HandleEvents(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 400 {
			t.Fatalf("%s status=%d", path, response.Code)
		}
	}
}

func TestFaultEventsExposeIssueWorkflowAssociation(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	event := api.GPUFaultEvent{EpisodeKey: "GPU-A:xid", State: "open", Severity: "critical", RuleCode: "xid_critical", FirstObservedAt: now, LastObservedAt: now}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	issue := api.PlatformIssue{IssueKey: fmt.Sprintf("fault_event:%d", event.ID), Category: "hardware_fault", IssueType: event.RuleCode, Title: "XID on GPU-A", Status: "in_progress", DetectionState: "active", DetectionSource: "health_rule", SourceRecordID: event.ID, FirstDetectedAt: now, LastDetectedAt: now}
	if err := db.Create(&issue).Error; err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewHandler(db).HandleEvents(response, httptest.NewRequest("GET", "/api/v1/fault-events", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(fmt.Sprintf(`"issue_id":%d`, issue.ID))) || !bytes.Contains(response.Body.Bytes(), []byte(`"workflow_status":"in_progress"`)) {
		t.Fatalf("unexpected event workflow response: %d %s", response.Code, response.Body.String())
	}
}

func TestTelemetryQualitySummaryAndClassification(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)
	finished := now
	run := api.HealthEvaluationRun{Status: "success", RuleVersion: "gpu-health-v1.4.1", StartedAt: now.Add(-time.Minute), FinishedAt: &finished}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	snapshots := []api.GPUFeatureSnapshot{
		{EvaluationRunID: run.ID, GPUAssetID: 1, GPUUUID: "GPU-A", NodeIP: "10.114.4.21", GPUIndex: 0, Metrics: api.FloatMap{"gpu_metric_samples_1h": 240, "gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15, "gpu_metric_gap_max_seconds_1h": 15, "gpu_uuid_presence_flap_count_1h": 0, "target_scrape_success_ratio_5m": 100, "target_scrape_samples_ratio_5m": 100, "target_scrape_duration_ratio_5m": 110}, FeatureCatalogVersion: "1.4.0", ObservedAt: now},
		{EvaluationRunID: run.ID, GPUAssetID: 2, GPUUUID: "GPU-B", NodeIP: "10.114.4.21", GPUIndex: 1, Metrics: api.FloatMap{"gpu_metric_samples_1h": 180, "gpu_metric_presence_ratio_1h": 75, "gpu_metric_sample_age_seconds": 20, "gpu_metric_gap_max_seconds_1h": 150, "gpu_uuid_presence_flap_count_1h": 2}, FeatureCatalogVersion: "1.4.0", ObservedAt: now},
		{EvaluationRunID: run.ID, GPUAssetID: 3, GPUUUID: "GPU-C", NodeIP: "10.114.4.21", GPUIndex: 2, Metrics: api.FloatMap{}, FeatureCatalogVersion: "1.4.0", ObservedAt: now},
	}
	if err := db.Create(&snapshots).Error; err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewHandler(db).HandleTelemetryQuality(response, httptest.NewRequest("GET", "/api/v1/health/telemetry-quality?status=stale", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"fresh":1`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"stale":1`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"unknown":1`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"total":1`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"max_metric_gap_seconds_1h":150`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"min_target_scrape_success_ratio_5m":100`)) {
		t.Fatalf("unexpected telemetry quality response: %d %s", response.Code, response.Body.String())
	}
}

func TestClassifyTelemetryQualityThresholdBoundaries(t *testing.T) {
	number := func(value float64) api.FloatMap {
		return api.FloatMap{"gpu_metric_presence_ratio_1h": value, "gpu_metric_sample_age_seconds": 60}
	}
	tests := []struct {
		name     string
		metrics  api.FloatMap
		expected string
	}{
		{name: "fresh at inclusive boundaries", metrics: api.FloatMap{"gpu_metric_presence_ratio_1h": 95, "gpu_metric_sample_age_seconds": 60, "gpu_metric_gap_max_seconds_1h": 45, "gpu_uuid_presence_flap_count_1h": 0, "target_scrape_success_ratio_5m": 95, "target_scrape_samples_ratio_5m": 80, "target_scrape_duration_ratio_5m": 200}, expected: "fresh"},
		{name: "degraded below presence target", metrics: number(94.99), expected: "degraded"},
		{name: "degraded at stale presence boundary", metrics: api.FloatMap{"gpu_metric_presence_ratio_1h": 80, "gpu_metric_sample_age_seconds": 300}, expected: "degraded"},
		{name: "degraded on UUID flap", metrics: api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15, "gpu_uuid_presence_flap_count_1h": 1}, expected: "degraded"},
		{name: "degraded on maximum gap", metrics: api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15, "gpu_metric_gap_max_seconds_1h": 45.01}, expected: "degraded"},
		{name: "degraded on scrape samples", metrics: api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15, "target_scrape_samples_ratio_5m": 79.99}, expected: "degraded"},
		{name: "duration ratio is audit only", metrics: api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15, "target_scrape_duration_ratio_5m": 500}, expected: "fresh"},
		{name: "stale below presence boundary", metrics: number(79.99), expected: "stale"},
		{name: "stale above age boundary", metrics: api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 300.01}, expected: "stale"},
		{name: "stale on repeated UUID flaps", metrics: api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15, "gpu_uuid_presence_flap_count_1h": 2}, expected: "stale"},
		{name: "stale on scrape failure ratio", metrics: api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15, "target_scrape_success_ratio_5m": 79.99}, expected: "stale"},
		{name: "unknown when structural metric missing", metrics: api.FloatMap{"gpu_metric_presence_ratio_1h": 100}, expected: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := classifyTelemetryQuality(api.GPUFeatureSnapshot{Metrics: test.metrics}).Status; actual != test.expected {
				t.Fatalf("expected %s, got %s", test.expected, actual)
			}
		})
	}
}

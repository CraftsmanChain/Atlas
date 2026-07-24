package issues

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func seedIssueSources(t *testing.T, db *storage.DB, now time.Time) api.GPUNode {
	t.Helper()
	node := api.GPUNode{NodeIP: "10.114.4.21", State: "degraded", Lifecycle: "active", ExpectedGPUCount: 8, ObservedGPUCount: 7, LastSyncedAt: now}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	asset := api.GPUAsset{AssetKey: "10.114.4.21:7", NodeID: node.ID, NodeIP: node.NodeIP, GPUIndex: 7, State: "uuid_unknown", SampleState: "missing", LastSyncedAt: now}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	target := api.CollectorTarget{TargetKey: "dcgm:10.114.4.21", Job: "dcgm_exporter", NodeIP: node.NodeIP, Health: "down", ReasonCode: "scrape_failed", LastSyncedAt: now}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	score := api.GPUHealthScore{GPUAssetID: asset.ID, NodeIP: node.NodeIP, GPUIndex: 7, Level: "unknown", DataConfidence: "D", Current: true, EvaluatedAt: now}
	if err := db.Create(&score).Error; err != nil {
		t.Fatal(err)
	}
	fault := api.GPUFaultEvent{EpisodeKey: "GPU-A:xid", State: "open", GPUUUID: "GPU-A", NodeIP: node.NodeIP, GPUIndex: 0, RuleCode: "xid_critical", Domain: "stability", Severity: "critical", Evidence: "XID 79", FirstObservedAt: now.Add(-time.Hour), LastObservedAt: now}
	if err := db.Create(&fault).Error; err != nil {
		t.Fatal(err)
	}
	recoveredAt := now.Add(-time.Minute)
	recovered := api.GPUFaultEvent{EpisodeKey: "GPU-B:temp", State: "recovered", GPUUUID: "GPU-B", NodeIP: node.NodeIP, GPUIndex: 1, RuleCode: "gpu_temp_high", Domain: "thermal", Severity: "warning", Evidence: "temperature recovered", FirstObservedAt: now.Add(-2 * time.Hour), LastObservedAt: now.Add(-time.Hour), RecoveredAt: &recoveredAt}
	if err := db.Create(&recovered).Error; err != nil {
		t.Fatal(err)
	}
	return node
}

func TestSyncDetectedIssuesAndAutomaticRecovery(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	node := seedIssueSources(t, db, now)
	service := NewService(db)
	service.now = func() time.Time { return now }
	if err := service.SyncDetectedIssues(); err != nil {
		t.Fatal(err)
	}
	var total, open, resolved int64
	db.Model(&api.PlatformIssue{}).Count(&total)
	db.Model(&api.PlatformIssue{}).Where("status = ?", "open").Count(&open)
	db.Model(&api.PlatformIssue{}).Where("status = ?", "resolved").Count(&resolved)
	if total != 6 || open != 5 || resolved != 1 {
		t.Fatalf("unexpected counts total=%d open=%d resolved=%d", total, open, resolved)
	}
	if err := db.Model(&node).Update("state", "up").Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SyncDetectedIssues(); err != nil {
		t.Fatal(err)
	}
	var nodeIssue api.PlatformIssue
	if err := db.Where("issue_key = ?", "node_state:"+node.NodeIP).First(&nodeIssue).Error; err != nil {
		t.Fatal(err)
	}
	if nodeIssue.Status != "resolved" || nodeIssue.DetectionState != "cleared" || nodeIssue.SourceRecoveredAt == nil {
		t.Fatalf("node issue was not auto-cleared: %+v", nodeIssue)
	}
	var asset api.GPUAsset
	if err := db.Where("node_ip = ?", node.NodeIP).First(&asset).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&asset).Updates(map[string]any{"state": "active", "sample_state": "current"}).Error; err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(time.Minute) }
	if err := service.SyncDetectedIssues(); err != nil {
		t.Fatal(err)
	}
	var assetIssue api.PlatformIssue
	if err := db.Where("issue_key = ?", "gpu_state:"+asset.AssetKey).First(&assetIssue).Error; err != nil {
		t.Fatal(err)
	}
	if assetIssue.Status != "resolved" || assetIssue.DetectionState != "cleared" || assetIssue.SourceRecoveredAt == nil {
		t.Fatalf("restored GPU issue was not auto-cleared: %+v", assetIssue)
	}
}

func TestSyncDetectedIssuesClearsLegacySourceConsistency(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	node := api.GPUNode{NodeIP: "10.114.4.21", State: "up", Lifecycle: "active", ExpectedGPUCount: 8, ObservedGPUCount: 8, LastSyncedAt: now}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	asset := api.GPUAsset{AssetKey: "10.114.4.21:0", NodeID: node.ID, NodeIP: node.NodeIP, GPUIndex: 0, CurrentUUID: "GPU-A", State: "active", SampleState: "current", LastSyncedAt: now}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := api.GPUFeatureSnapshot{GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, NodeIP: asset.NodeIP, GPUIndex: 0, ConsistencyCandidates: api.StringList{"gpu_temp: dcgm=60 gpu_exporter=70"}, ConsistencyCandidateCount: 1, ObservedAt: now}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	scoreValue := 100
	score := api.GPUHealthScore{FeatureSnapshotID: snapshot.ID, GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, NodeIP: asset.NodeIP, GPUIndex: 0, Score: &scoreValue, Level: "healthy", DataConfidence: "A", Current: true, EvaluatedAt: now}
	if err := db.Create(&score).Error; err != nil {
		t.Fatal(err)
	}
	issue := api.PlatformIssue{IssueKey: "source_consistency:legacy", Category: "data_quality", IssueType: "gpu_source_inconsistency", Title: "legacy source mismatch", EntityType: "gpu", EntityKey: "legacy", Severity: "attention", Status: "open", DetectionState: "active", DetectionSource: "source_consistency", FirstDetectedAt: now.Add(-time.Hour), LastDetectedAt: now.Add(-time.Hour)}
	if err := db.Create(&issue).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	if err := service.SyncDetectedIssues(); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&issue, issue.ID).Error; err != nil {
		t.Fatal(err)
	}
	if issue.Status != "resolved" || issue.DetectionState != "cleared" {
		t.Fatalf("legacy consistency issue was not cleared: %+v", issue)
	}
	var count int64
	db.Model(&api.PlatformIssue{}).Where("issue_type = ?", "gpu_source_inconsistency").Count(&count)
	if count != 1 {
		t.Fatalf("source difference created a new issue, count=%d", count)
	}
	handler := NewHandlerWithService(db, service)
	response := httptest.NewRecorder()
	handler.HandleCollection(response, httptest.NewRequest("GET", "/api/v1/issues?limit=10", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"total":0`)) {
		t.Fatalf("legacy consistency issue remained visible: %d %s", response.Code, response.Body.String())
	}
}

func TestSyncDetectedIssuesExcludesTargetsOnRetiredNodes(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	node := api.GPUNode{NodeIP: "10.114.4.37", State: "offline", Lifecycle: "retired", LastSyncedAt: now.Add(-24 * time.Hour)}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	target := api.CollectorTarget{TargetKey: "dcgm_exporter|10.114.4.37", Job: "dcgm_exporter", NodeIP: node.NodeIP, Health: "missing", LastSyncedAt: now.Add(-24 * time.Hour)}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	legacy := api.PlatformIssue{IssueKey: "target_health:" + target.TargetKey, Category: "data_quality", IssueType: "target_health", Title: "legacy missing target", EntityType: "target", EntityKey: target.TargetKey, NodeIP: node.NodeIP, Severity: "attention", Status: "open", DetectionState: "active", DetectionSource: "collector_target", FirstDetectedAt: now.Add(-24 * time.Hour), LastDetectedAt: now.Add(-24 * time.Hour)}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	if err := service.SyncDetectedIssues(); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&legacy, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.Status != "resolved" || legacy.DetectionState != "cleared" || legacy.SourceRecoveredAt == nil {
		t.Fatalf("retired-node target issue was not cleared: %+v", legacy)
	}
	var generated int64
	db.Model(&api.PlatformIssue{}).Where("node_ip = ? AND detection_state = ?", node.NodeIP, "active").Count(&generated)
	if generated != 0 {
		t.Fatalf("retired node generated %d active issues", generated)
	}
}

func TestIssueSummaryResolutionAndTrainingDataAPI(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	seedIssueSources(t, db, now)
	handler := NewHandler(db)
	handler.service.now = func() time.Time { return now }

	response := httptest.NewRecorder()
	handler.HandleSummary(response, httptest.NewRequest("GET", "/api/v1/issues/summary", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"discovered":4`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"remaining":4`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"remaining_by_category"`)) || bytes.Contains(response.Body.Bytes(), []byte(`"hardware_fault"`)) {
		t.Fatalf("unexpected summary: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleCollection(response, httptest.NewRequest("GET", "/api/v1/issues", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"total":4`)) || bytes.Contains(response.Body.Bytes(), []byte(`"hardware_fault"`)) {
		t.Fatalf("unexpected data statistics collection: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleCollection(response, httptest.NewRequest("GET", "/api/v1/issues?category=hardware_fault", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"total":2`)) {
		t.Fatalf("hardware event workflow records unavailable: %d %s", response.Code, response.Body.String())
	}

	var issue api.PlatformIssue
	if err := db.Where("issue_key = ?", "node_state:10.114.4.21").First(&issue).Error; err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"status": "resolved", "root_cause": "loose PCIe power cable on 10.114.4.21", "solution": "reseated and replaced cable", "resolution_process": "operator ops@example.com drained the node, powered off, reseated cable, and ran DCGM diagnostics", "result": "GPU-01234567-89ab-cdef-0123-456789abcdef visible and diagnostics passed", "operator": "ops-user", "training_eligible": true, "evidence": []string{"10.114.4.21 dcgmi diag passed"}}
	body, _ := json.Marshal(payload)
	response = httptest.NewRecorder()
	handler.HandleSubresource(response, httptest.NewRequest("POST", "/api/v1/issues/"+itoa(issue.ID)+"/resolution", bytes.NewReader(body)))
	if response.Code != 201 {
		t.Fatalf("resolution status=%d body=%s", response.Code, response.Body.String())
	}
	if err := db.First(&issue, issue.ID).Error; err != nil {
		t.Fatal(err)
	}
	if issue.Status != "resolved" || issue.LatestResolutionID == 0 {
		t.Fatalf("issue not resolved: %+v", issue)
	}

	response = httptest.NewRecorder()
	handler.HandleSummary(response, httptest.NewRequest("GET", "/api/v1/issues/summary", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"training_eligible":1`)) {
		t.Fatalf("unexpected training summary: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.HandleTrainingData(response, httptest.NewRequest("GET", "/api/v1/issues/training-data?download=1", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"schema_version":"atlas-issue-training-v1"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"contract_version":"1.1.0"`)) || !bytes.Contains(response.Body.Bytes(), []byte("loose PCIe power cable")) {
		t.Fatalf("unexpected training export: %d %s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(`[NODE_IP]`)) || !bytes.Contains(response.Body.Bytes(), []byte(`[GPU_UUID]`)) || !bytes.Contains(response.Body.Bytes(), []byte(`[EMAIL]`)) {
		t.Fatalf("training export did not redact identifiers: %s", response.Body.String())
	}
	for _, forbidden := range []string{"10.114.4.21", "GPU-01234567-89ab-cdef-0123-456789abcdef", "ops@example.com", `"operator"`, `"issue_key"`, `"entity_key"`} {
		if bytes.Contains(response.Body.Bytes(), []byte(forbidden)) {
			t.Fatalf("training export leaked %q: %s", forbidden, response.Body.String())
		}
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Content-Disposition") == "" || response.Header().Get("ETag") == "" {
		t.Fatalf("missing safe export headers: %+v", response.Header())
	}
	firstDataset, err := buildTrainingDataset(db, now)
	if err != nil {
		t.Fatal(err)
	}
	secondDataset, err := buildTrainingDataset(db, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if firstDataset.DatasetID != secondDataset.DatasetID || firstDataset.Summary.Total != 1 || firstDataset.Summary.Train+firstDataset.Summary.Evaluation != 1 {
		t.Fatalf("training snapshot or split is unstable: first=%+v second=%+v", firstDataset, secondDataset)
	}

	payload["root_cause"] = "review correction pending"
	payload["training_eligible"] = false
	body, _ = json.Marshal(payload)
	response = httptest.NewRecorder()
	handler.HandleSubresource(response, httptest.NewRequest("POST", "/api/v1/issues/"+itoa(issue.ID)+"/resolution", bytes.NewReader(body)))
	if response.Code != 201 {
		t.Fatalf("follow-up resolution status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleTrainingData(response, httptest.NewRequest("GET", "/api/v1/issues/training-data", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"total":0`)) || bytes.Contains(response.Body.Bytes(), []byte(`"sample-`)) {
		t.Fatalf("superseded training resolution remained exportable: %d %s", response.Code, response.Body.String())
	}
}

func TestSyncDetectedIssuesTracksTelemetryContinuityAndRecovery(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)
	snapshot := api.GPUFeatureSnapshot{GPUAssetID: 12, GPUUUID: "GPU-A", NodeIP: "10.114.4.21", GPUIndex: 2, Metrics: api.FloatMap{"gpu_metric_presence_ratio_1h": 70, "gpu_metric_sample_age_seconds": 30}, ObservedAt: now}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	scoreValue := 100
	score := api.GPUHealthScore{FeatureSnapshotID: snapshot.ID, GPUAssetID: snapshot.GPUAssetID, GPUUUID: snapshot.GPUUUID, NodeIP: snapshot.NodeIP, GPUIndex: snapshot.GPUIndex, Score: &scoreValue, Level: "healthy", Current: true, EvaluatedAt: now}
	if err := db.Create(&score).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return now }
	if err := service.SyncDetectedIssues(); err != nil {
		t.Fatal(err)
	}
	var issue api.PlatformIssue
	if err := db.Where("issue_key = ?", "telemetry_continuity:12").First(&issue).Error; err != nil {
		t.Fatal(err)
	}
	if issue.Status != "open" || issue.Severity != "warning" {
		t.Fatalf("unexpected telemetry issue: %+v", issue)
	}
	snapshot.Metrics = api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15}
	if err := db.Save(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(time.Minute) }
	if err := service.SyncDetectedIssues(); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&issue, issue.ID).Error; err != nil {
		t.Fatal(err)
	}
	if issue.Status != "resolved" || issue.DetectionState != "cleared" {
		t.Fatalf("telemetry issue was not auto-cleared: %+v", issue)
	}
}

func TestTelemetryContinuitySeverityUsesGapFlapAndScrapeBoundaries(t *testing.T) {
	base := api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15}
	if severity, active := telemetryContinuitySeverity(base); active || severity != "" {
		t.Fatalf("healthy telemetry must not be active: severity=%s active=%v", severity, active)
	}
	durationAudit := api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15, "target_scrape_duration_ratio_5m": 600}
	if severity, active := telemetryContinuitySeverity(durationAudit); active || severity != "" {
		t.Fatalf("relative scrape duration without an absolute gate must remain audit-only: severity=%s active=%v", severity, active)
	}
	attention := api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15, "gpu_metric_gap_max_seconds_1h": 46}
	if severity, active := telemetryContinuitySeverity(attention); !active || severity != "attention" {
		t.Fatalf("maximum gap must create attention: severity=%s active=%v", severity, active)
	}
	warning := api.FloatMap{"gpu_metric_presence_ratio_1h": 100, "gpu_metric_sample_age_seconds": 15, "gpu_uuid_presence_flap_count_1h": 2}
	if severity, active := telemetryContinuitySeverity(warning); !active || severity != "warning" {
		t.Fatalf("repeated UUID flaps must create warning: severity=%s active=%v", severity, active)
	}
}

func itoa(value uint) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	buffer := make([]byte, 0, 10)
	for value > 0 {
		buffer = append([]byte{digits[value%10]}, buffer...)
		value /= 10
	}
	return string(buffer)
}

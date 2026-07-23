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
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"discovered":6`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"remaining":5`)) {
		t.Fatalf("unexpected summary: %d %s", response.Code, response.Body.String())
	}

	var issue api.PlatformIssue
	if err := db.Where("issue_key = ?", "node_state:10.114.4.21").First(&issue).Error; err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"status": "resolved", "root_cause": "loose PCIe power cable", "solution": "reseated and replaced cable", "resolution_process": "drain node, power off, reseat cable, run DCGM diagnostics", "result": "8 GPUs visible and diagnostics passed", "operator": "ops-user", "training_eligible": true, "evidence": []string{"dcgmi diag passed"}}
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
	handler.HandleTrainingData(response, httptest.NewRequest("GET", "/api/v1/issues/training-data", nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte("atlas-issue-training-v1")) || !bytes.Contains(response.Body.Bytes(), []byte("loose PCIe power cable")) {
		t.Fatalf("unexpected training export: %d %s", response.Code, response.Body.String())
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

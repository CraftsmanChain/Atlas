package prediction

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestSyncLabelsKeepsProxyProvenanceAndRequiresHardwareConfirmation(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	events := []api.GPUFaultEvent{
		{EpisodeKey: "GPU-A:temp:1", GPUAssetID: 1, GPUUUID: "GPU-A", NodeIP: "10.114.4.21", ModelName: "H100", RuleCode: "gpu_temp_critical", RuleVersion: "gpu-health-v1.4.1", Severity: "critical", State: "recovered", FirstObservedAt: now.Add(-2 * time.Hour), LastObservedAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour)},
		{EpisodeKey: "GPU-B:ue:1", GPUAssetID: 2, GPUUUID: "GPU-B", NodeIP: "10.114.4.22", ModelName: "RTX 4090", RuleCode: "uncorrectable_remapped_rows", RuleVersion: "gpu-health-v1.4.1", Severity: "critical", State: "open", FirstObservedAt: now.Add(-time.Hour), LastObservedAt: now, CreatedAt: now.Add(-time.Hour)},
	}
	for index := range events {
		if err := db.Create(&events[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	hardwareIssue := api.PlatformIssue{
		IssueKey: "fault_event:1", Category: "hardware_fault", IssueType: events[0].RuleCode,
		Title: "confirmed GPU fault", EntityType: "gpu", EntityKey: events[0].EpisodeKey,
		GPUUUID: events[0].GPUUUID, Status: "resolved", DetectionState: "cleared",
		DetectionSource: "health_rule", SourceRecordID: events[0].ID,
		FirstDetectedAt: events[0].FirstObservedAt, LastDetectedAt: events[0].LastObservedAt,
	}
	dataIssue := api.PlatformIssue{
		IssueKey: "target:test", Category: "data_quality", IssueType: "target_health",
		Title: "target down", EntityType: "target", EntityKey: "test", Status: "resolved",
		DetectionState: "cleared", DetectionSource: "collector_target", SourceRecordID: events[1].ID,
		FirstDetectedAt: now.Add(-time.Hour), LastDetectedAt: now,
	}
	if err := db.Create(&hardwareIssue).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dataIssue).Error; err != nil {
		t.Fatal(err)
	}
	resolutions := []api.IssueResolution{
		{IssueID: hardwareIssue.ID, Status: "resolved", RootCause: "confirmed board failure", Solution: "replaced GPU", ResolutionProcess: "drained, replaced, validated", Result: "diagnostics passed", Operator: "ops", TrainingEligible: true, CreatedAt: now},
		{IssueID: dataIssue.ID, Status: "resolved", RootCause: "exporter stopped", Solution: "restored exporter", ResolutionProcess: "checked service", Result: "target up", Operator: "ops", TrainingEligible: true, CreatedAt: now},
	}
	for index := range resolutions {
		if err := db.Create(&resolutions[index]).Error; err != nil {
			t.Fatal(err)
		}
	}

	service := NewService(db)
	if err := service.SyncLabels(); err != nil {
		t.Fatal(err)
	}
	if err := service.SyncLabels(); err != nil {
		t.Fatal(err)
	}
	summary, labels, err := service.Labels(100)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 2 || summary.Confirmed != 1 || summary.StrongProxy != 1 || summary.WeakProxy != 0 || summary.AffectedGPUs != 2 {
		t.Fatalf("unexpected label summary: %+v labels=%+v", summary, labels)
	}
	byKey := map[string]api.FailureLabel{}
	for _, label := range labels {
		byKey[label.LabelKey] = label
	}
	confirmed := byKey["gpu-fault-event:1"]
	if confirmed.QualityTier != "confirmed" || confirmed.SourceType != "gpu_fault_event" ||
		confirmed.SourceRecordID != events[0].ID || confirmed.ConfirmationResolutionID != resolutions[0].ID ||
		confirmed.ConfirmedAt == nil || confirmed.RuleVersion != "gpu-health-v1.4.1" {
		t.Fatalf("confirmation lost event provenance: %+v", confirmed)
	}
	proxy := byKey["gpu-fault-event:2"]
	if proxy.QualityTier != "strong_proxy" || proxy.ConfirmationResolutionID != 0 {
		t.Fatalf("data-quality resolution incorrectly confirmed hardware label: %+v", proxy)
	}

	reviewCorrection := api.IssueResolution{
		IssueID: hardwareIssue.ID, Status: "resolved", RootCause: "confirmation under review",
		Operator: "reviewer", TrainingEligible: false, CreatedAt: now.Add(time.Minute),
	}
	if err := db.Create(&reviewCorrection).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SyncLabels(); err != nil {
		t.Fatal(err)
	}
	_, labels, err = service.Labels(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range labels {
		if label.LabelKey == "gpu-fault-event:1" &&
			(label.QualityTier != "weak_proxy" || label.ConfirmationResolutionID != 0 || label.ConfirmedAt != nil) {
			t.Fatalf("superseding review did not revoke confirmed label: %+v", label)
		}
	}
}

func TestLabelsHandlerIsReadOnly(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	response := httptest.NewRecorder()
	handler.HandleLabels(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/labels", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("labels status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleLabels(response, httptest.NewRequest(http.MethodPost, "/api/v1/prediction/labels", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("write-like label request should be rejected: %d", response.Code)
	}
}

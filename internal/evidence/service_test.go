package evidence

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestBuildBundleAndReportPreserveEvidenceProvenance(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 16, 0, 0, 0, time.UTC)
	snapshot := api.GPUFeatureSnapshot{
		GPUAssetID: 7, GPUUUID: "GPU-TEST", NodeIP: "10.114.4.21", GPUIndex: 3, ModelName: "NVIDIA H100 80GB HBM3",
		Metrics: api.FloatMap{"uncorrectable_remapped_rows": 1, "gpu_temp": 72}, MetricSources: api.StringMap{"gpu_temp": "dcgm_exporter"},
		SourcesAvailable: api.StringList{"dcgm_exporter", "gpu_exporter"}, FeatureCatalogVersion: "1.8.0",
		AvailableMetricCount: 2, ExpectedMetricCount: 2, DataConfidence: "A", ObservedAt: now.Add(-time.Minute),
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	scoreValue := 70
	score := api.GPUHealthScore{
		FeatureSnapshotID: snapshot.ID, GPUAssetID: 7, GPUUUID: "GPU-TEST", NodeIP: "10.114.4.21", GPUIndex: 3,
		ModelName: snapshot.ModelName, Score: &scoreValue, Level: "critical", DataConfidence: "A",
		RuleVersion: "gpu-health-v1.4.1", EvaluatedAt: now,
	}
	if err := db.Create(&score).Error; err != nil {
		t.Fatal(err)
	}
	hit := api.GPUHealthRuleHit{
		HealthScoreID: score.ID, GPUAssetID: 7, GPUUUID: "GPU-TEST", RuleCode: "row_remap_failure",
		Domain: "memory", Severity: "critical", ObservedValue: 1, Threshold: "> 0",
		Evidence: "row remap failure is set", RuleVersion: score.RuleVersion, EvaluatedAt: now,
	}
	if err := db.Create(&hit).Error; err != nil {
		t.Fatal(err)
	}
	event := api.GPUFaultEvent{
		EpisodeKey: "GPU-TEST:row_remap_failure:1", Source: "health_rule", State: "open",
		GPUAssetID: 7, GPUUUID: "GPU-TEST", NodeIP: "10.114.4.21", GPUIndex: 3, ModelName: snapshot.ModelName,
		RuleCode: "row_remap_failure", Domain: "memory", Severity: "critical", Evidence: hit.Evidence,
		ObservedValue: 1, Threshold: "> 0", OccurrenceCount: 2, LatestScoreID: score.ID,
		RuleVersion: score.RuleVersion, FirstObservedAt: now.Add(-time.Hour), LastObservedAt: now,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	issue := api.PlatformIssue{
		IssueKey: fmt.Sprintf("fault_event:%d", event.ID), Category: "hardware_fault", IssueType: event.RuleCode,
		Title: "Row remap failure", EntityType: "gpu", EntityKey: event.EpisodeKey, NodeIP: event.NodeIP, GPUUUID: event.GPUUUID,
		Severity: "critical", Status: "resolved", DetectionState: "active", DetectionSource: "health_rule", SourceRecordID: event.ID,
		FirstDetectedAt: event.FirstObservedAt, LastDetectedAt: event.LastObservedAt,
	}
	if err := db.Create(&issue).Error; err != nil {
		t.Fatal(err)
	}
	resolution := api.IssueResolution{
		IssueID: issue.ID, Status: "resolved", RootCause: "Confirmed memory subsystem fault",
		Solution: "Replaced hardware", ResolutionProcess: "Reviewed evidence and replaced the GPU",
		Result: "Telemetry returned to normal", Operator: "tester", CreatedAt: now.Add(time.Hour),
	}
	if err := db.Create(&resolution).Error; err != nil {
		t.Fatal(err)
	}

	service := NewService(db)
	service.now = func() time.Time { return now.Add(2 * time.Hour) }
	bundle, err := service.BuildBundle(event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.SchemaVersion != EvidenceSchemaVersion || bundle.HealthScore == nil || bundle.FeatureSnapshot == nil ||
		len(bundle.RuleHits) != 1 || bundle.Issue == nil || len(bundle.Resolutions) != 1 {
		t.Fatalf("incomplete bundle: %+v", bundle)
	}
	if bundle.SourceStatus[1].Status != "not_collected" {
		t.Fatalf("node access must remain explicitly uncollected in v0.1: %+v", bundle.SourceStatus)
	}
	evidenceIDs := map[string]bool{}
	for _, item := range bundle.Evidence {
		evidenceIDs[item.ID] = true
	}
	for _, entry := range bundle.Timeline {
		if !evidenceIDs[entry.EvidenceID] {
			t.Fatalf("timeline references missing evidence %q", entry.EvidenceID)
		}
	}

	report, err := service.BuildReport(event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != ReportSchemaVersion || report.ReportVersion != ReportVersion ||
		report.AnalysisMode != "deterministic" || !report.NoActionExecuted {
		t.Fatalf("unexpected report contract: %+v", report)
	}
	for _, finding := range report.Findings {
		for _, evidenceID := range finding.EvidenceIDs {
			if !evidenceIDs[evidenceID] {
				t.Fatalf("finding %s references missing evidence %q", finding.Code, evidenceID)
			}
		}
	}
	for _, hypothesis := range report.Hypotheses {
		for _, evidenceID := range hypothesis.EvidenceIDs {
			if !evidenceIDs[evidenceID] {
				t.Fatalf("hypothesis %s references missing evidence %q", hypothesis.Code, evidenceID)
			}
		}
	}
	if report.Hypotheses[0].Status != "supported" {
		t.Fatalf("operator root cause should be the supported hypothesis: %+v", report.Hypotheses)
	}
}

func TestBuildBundleReportsMissingReadOnlyEvidenceWithoutInventingData(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	event := api.GPUFaultEvent{
		EpisodeKey: "minimal", Source: "health_rule", State: "open", RuleCode: "gpu_unavailable",
		Domain: "availability", Severity: "critical", Evidence: "GPU is unavailable",
		FirstObservedAt: time.Now(), LastObservedAt: time.Now(),
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	bundle, err := NewService(db).BuildBundle(event.ID)
	if err != nil {
		t.Fatal(err)
	}
	missing := map[string]bool{}
	for _, gap := range bundle.MissingEvidence {
		missing[gap.Code] = true
	}
	for _, code := range []string{"health_snapshot", "issue_record", "node_logs", "bmc_evidence"} {
		if !missing[code] {
			t.Fatalf("expected missing evidence %q in %+v", code, bundle.MissingEvidence)
		}
	}
	if len(bundle.Evidence) != 1 || bundle.Evidence[0].Kind != "fault_event" {
		t.Fatalf("minimal event must not invent evidence: %+v", bundle.Evidence)
	}
}

func TestBuildBundleReturnsTypedNotFound(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(db).BuildBundle(999); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("expected ErrEventNotFound, got %v", err)
	}
}

func TestBuildBundleIncludesLatestNodeEvidence(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 4, 0, 0, 0, time.UTC)
	event := api.GPUFaultEvent{
		EpisodeKey: "node-evidence", Source: "health_rule", State: "open",
		NodeIP: "10.114.4.25", RuleCode: "xid_critical", Domain: "stability",
		Severity: "critical", Evidence: "XID 79",
		FirstObservedAt: now.Add(-time.Hour), LastObservedAt: now,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	collection := api.NodeEvidenceCollection{
		FaultEventID: event.ID, NodeIP: event.NodeIP, Status: "completed",
		CommandCount: 1, NoCredentialDisclosed: true, ReadOnly: true,
		StartedAt: now, FinishedAt: now.Add(time.Second),
	}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	record := api.NodeEvidenceRecord{
		CollectionID: collection.ID, CommandID: "logs.kernel_window", Kind: "node_log",
		Status: "completed", Output: "NVRM: Xid 79", OutputBytes: 12, ObservedAt: now,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	bundle, err := NewService(db).BuildBundle(event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.NodeEvidence) != 1 || bundle.SourceStatus[1].Status != "completed" {
		t.Fatalf("node evidence was not linked: %+v", bundle)
	}
	for _, missing := range bundle.MissingEvidence {
		if missing.Code == "node_logs" {
			t.Fatalf("usable node log must close node_logs gap: %+v", bundle.MissingEvidence)
		}
	}
	found := false
	for _, item := range bundle.Evidence {
		if item.ID == fmt.Sprintf("node-evidence:%d", record.ID) && item.Provenance == "node_evidence_records" {
			found = true
		}
	}
	if !found {
		t.Fatalf("node evidence item missing from bundle: %+v", bundle.Evidence)
	}
}

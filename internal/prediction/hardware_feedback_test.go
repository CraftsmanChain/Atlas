package prediction

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestHardwareFaultFeedbackCreatesOfflineHistoryPackRequest(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	asset := api.GPUAsset{
		AssetKey:    "node-10.114.4.21-gpu-2",
		NodeIP:      "10.114.4.21",
		GPUIndex:    2,
		CurrentUUID: "GPU-HW-FAULT",
		ModelName:   "H100",
		State:       "active",
	}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	handler.service.now = func() time.Time { return time.Date(2026, 8, 21, 9, 30, 0, 0, time.UTC) }
	body := bytes.NewBufferString(`{
		"gpu_asset_id": 1,
		"node_ip": "10.114.4.21",
		"reported_gpu_uuid": "GPU-HW-FAULT",
		"gpu_index": 2,
		"fault_type": "gpu_hardware_failure",
		"fault_occurred_at": "2026-08-21T08:00:00Z",
		"pre_window_hours": 72,
		"post_window_hours": 24,
		"operator": "ops-a",
		"description": "GPU dropped from nvidia-smi and recovered after board replacement",
		"repair_action": "replace_gpu",
		"hardware_replaced": true,
		"evidence_note": "ticket HW-100",
		"training_eligible": true
	}`)
	response := httptest.NewRecorder()
	handler.HandleHardwareFaultFeedback(response, httptest.NewRequest(http.MethodPost, "/api/v1/prediction/hardware-fault-feedback", body))
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created struct {
		Data api.HardwareFaultFeedbackRequest `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Status != "history_pack_requested" || created.Data.HistoryPackStatus != "queued_offline_collection" {
		t.Fatalf("feedback did not become offline pack request: %+v", created.Data)
	}
	if created.Data.NodeIP != "10.114.4.21" || created.Data.GPUIndex != 2 || created.Data.GPUUUID != "" || created.Data.ReportedGPUUUID != "GPU-HW-FAULT" || created.Data.ModelName != "H100" {
		t.Fatalf("feedback lost failed GPU identity: %+v", created.Data)
	}
	if created.Data.IdentityResolutionStatus != "requires_historical_identity_at_fault_time" || !strings.Contains(created.Data.IdentityResolutionNote, "historical identity intervals") {
		t.Fatalf("replacement feedback must require historical identity resolution: %+v", created.Data)
	}
	if !strings.Contains(created.Data.HistoryPackScope, "start=2026-08-18T08:00:00Z") || !strings.Contains(created.Data.HistoryPackScope, "end=2026-08-22T08:00:00Z") {
		t.Fatalf("feedback scope did not bind pre/post windows: %s", created.Data.HistoryPackScope)
	}
	if !strings.Contains(created.Data.HistoryPackScope, "identity_resolution=requires_historical_identity_at_fault_time") || !strings.Contains(created.Data.HistoryPackScope, "reported_gpu_uuid=GPU-HW-FAULT") {
		t.Fatalf("replacement feedback scope must keep reported UUID separate from resolved fault identity: %s", created.Data.HistoryPackScope)
	}
	if len(created.Data.BlockingReasons) < 2 || !created.Data.TrainingEligible || !created.Data.HardwareReplaced {
		t.Fatalf("feedback lost governance fields: %+v", created.Data)
	}

	response = httptest.NewRecorder()
	handler.HandleHardwareFaultFeedback(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/hardware-fault-feedback", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	var listed struct {
		Data []api.HardwareFaultFeedbackRequest `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data) != 1 || listed.Data[0].RequestKey != created.Data.RequestKey {
		t.Fatalf("feedback request was not listed: %+v", listed.Data)
	}
}

func TestHardwareFaultFeedbackRejectsMissingOperator(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	response := httptest.NewRecorder()
	handler.HandleHardwareFaultFeedback(response, httptest.NewRequest(http.MethodPost, "/api/v1/prediction/hardware-fault-feedback", bytes.NewBufferString(`{
		"node_ip": "10.114.4.21",
		"gpu_index": 0,
		"fault_type": "gpu_hardware_failure",
		"fault_occurred_at": "2026-08-21T08:00:00Z"
	}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "operator is required") {
		t.Fatalf("missing operator should be rejected: %d %s", response.Code, response.Body.String())
	}
}

func TestParseFeedbackTimeTreatsUnzonedInputAsAsiaShanghai(t *testing.T) {
	parsed, err := parseFeedbackTime("2026-08-11T23:29")
	if err != nil {
		t.Fatal(err)
	}
	_, offset := parsed.Zone()
	if offset != 8*60*60 || parsed.Hour() != 23 || parsed.UTC().Hour() != 15 {
		t.Fatalf("local feedback time shifted unexpectedly: local=%s utc=%s offset=%d", parsed, parsed.UTC(), offset)
	}
	date, precision, start, end, err := parseFeedbackTimeWindow("2026-08-11", "date", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if precision != "date" || !date.Equal(start) || end.Sub(start) != 24*time.Hour-time.Nanosecond {
		t.Fatalf("date window mismatch: date=%s start=%s end=%s precision=%s", date, start, end, precision)
	}
	_, dateOffset := date.Zone()
	if dateOffset != 8*60*60 {
		t.Fatalf("date-only feedback must use Asia/Shanghai: %s", date)
	}
}

func TestHardwareFaultFeedbackUsesCurrentIdentityWhenNoReplacementReported(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	asset := api.GPUAsset{
		AssetKey:    "node-10.114.4.22-gpu-3",
		NodeIP:      "10.114.4.22",
		GPUIndex:    3,
		CurrentUUID: "GPU-STILL-IN-SLOT",
		ModelName:   "H100",
		State:       "active",
	}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		GPUAssetID:       asset.ID,
		NodeIP:           asset.NodeIP,
		GPUUUID:          asset.CurrentUUID,
		GPUIndex:         asset.GPUIndex,
		FaultType:        "pcie_link_failure",
		FaultOccurredAt:  "2026-08-21T08:00:00Z",
		Operator:         "ops-b",
		TrainingEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.IdentityResolutionStatus != "current_identity_selected" || row.GPUUUID != "GPU-STILL-IN-SLOT" || row.ReportedGPUUUID != "GPU-STILL-IN-SLOT" {
		t.Fatalf("non-replacement feedback should keep current identity selected: %+v", row)
	}
	if len(row.BlockingReasons) != 1 || strings.Contains(row.HistoryPackScope, "requires_historical_identity_at_fault_time") {
		t.Fatalf("non-replacement feedback should not require replacement identity blocker: %+v", row)
	}
}

func TestPrepareHardwareFaultFeedbackPackResolvesReplacementIdentityAndSourceCoverage(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	last := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&api.HistoricalGPUIdentityInterval{
		IntervalKey: "identity-10.114.4.23-gpu3-old",
		SourceKey:   "current-prometheus", NodeIP: "10.114.4.23", GPUIndex: 3, GPUUUID: "GPU-OLD-FAILED", ModelName: "H100",
		FirstSeenAt: first, LastSeenAt: last, ObservationCount: 120, EvidenceStrength: "strong",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.MonitoringHistoryAudit{
		SourceKey: "current-prometheus", SourceName: "Current Prometheus", SourceType: "prometheus", BaseURL: "http://prometheus",
		Status: "success", EarliestSampleAt: &first, LatestSampleAt: &last, StartedAt: first, FinishedAt: last,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.23", ReportedGPUUUID: "GPU-NEW-AFTER-REPLACE", GPUIndex: 3,
		FaultType: "pcie_link_failure", FaultOccurredAt: "2026-08-21T08:00:00Z",
		PreWindowHours: 8, PostWindowHours: 4, Operator: "ops-c", RepairAction: "replace_gpu", TrainingEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.PrepareHardwareFaultFeedbackPack(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "history_pack_manifest_ready" || prepared.HistoryPackStatus != "manifest_ready_pending_metric_extraction" || prepared.HistoryPackSHA256 == "" {
		t.Fatalf("pack manifest was not prepared: %+v", prepared)
	}
	if prepared.GPUUUID != "GPU-OLD-FAILED" || prepared.ReportedGPUUUID != "GPU-NEW-AFTER-REPLACE" || prepared.IdentityResolutionStatus != "historical_identity_resolved" {
		t.Fatalf("replacement identity was not resolved safely: %+v", prepared)
	}
	if len(prepared.BlockingReasons) != 0 || !strings.Contains(prepared.HistoryPackScope, "source_key=current-prometheus") {
		t.Fatalf("prepared pack kept blockers or lost source scope: %+v", prepared)
	}
	again, err := service.PrepareHardwareFaultFeedbackPack(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.HistoryPackSHA256 != prepared.HistoryPackSHA256 {
		t.Fatalf("pack checksum must be stable: %s != %s", again.HistoryPackSHA256, prepared.HistoryPackSHA256)
	}
}

func TestPrepareHardwareFaultFeedbackPackBlocksWhenFaultTimeIdentityMissing(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.24", ReportedGPUUUID: "GPU-NEW", GPUIndex: 3,
		FaultType: "pcie_link_failure", FaultOccurredAt: "2026-08-21T08:00:00Z",
		Operator: "ops-d", RepairAction: "replace_gpu", TrainingEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.PrepareHardwareFaultFeedbackPack(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "blocked" || prepared.HistoryPackStatus != "blocked_identity_unresolved" || prepared.HistoryPackSHA256 != "" {
		t.Fatalf("missing identity should block pack preparation: %+v", prepared)
	}
	if prepared.IdentityResolutionStatus != "blocked_no_fault_time_identity" || len(prepared.BlockingReasons) == 0 {
		t.Fatalf("missing identity blocker not recorded: %+v", prepared)
	}
}

func TestReviewedFeedbackPackAndWarningUseImmutableCorrectedFacts(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	reported := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	confirmed := reported.Add(-45 * 24 * time.Hour)
	auditStart := confirmed.Add(-10 * 24 * time.Hour)
	auditEnd := reported.Add(2 * 24 * time.Hour)
	if err := db.Create(&api.MonitoringHistoryAudit{
		SourceKey: "current-prometheus", SourceName: "Current Prometheus", SourceType: "prometheus", BaseURL: "http://prometheus",
		Status: "success", EarliestSampleAt: &auditStart, LatestSampleAt: &auditEnd, StartedAt: auditStart, FinishedAt: auditEnd,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.HistoricalGPUIdentityInterval{
		IntervalKey: "reviewed-pack-identity", SourceKey: "current-prometheus", NodeIP: "10.114.4.48", GPUIndex: 2, GPUUUID: "GPU-FAULT-TIME",
		FirstSeenAt: confirmed.Add(-24 * time.Hour), LastSeenAt: confirmed.Add(24 * time.Hour), ObservationCount: 100, EvidenceStrength: "strong",
	}).Error; err != nil {
		t.Fatal(err)
	}
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.48", TargetScope: "multi_gpu", GPUIndex: -1, FaultType: "gpu_hardware_failure",
		FaultOccurredAt: reported.Format(time.RFC3339), PreWindowHours: 168, PostWindowHours: 24, Operator: "importer",
	})
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.ReviewHardwareFaultFeedback(row.ID, HardwareFaultFeedbackReviewInput{
		Decision: "confirmed_hardware", Reviewer: "reviewer", ReviewNote: "monitoring and repair evidence agree",
		ConfirmedOnsetAt: confirmed.Format(time.RFC3339), ConfirmedWindowStartAt: confirmed.Format(time.RFC3339), ConfirmedWindowEndAt: confirmed.Format(time.RFC3339),
		ConfirmedNodeIP: row.NodeIP, ConfirmedGPUUUID: "GPU-FAULT-TIME", ConfirmedGPUIndex: intPointer(2), ConfirmedFaultType: "gpu_memory_failure",
		TargetScope: "gpu", EpisodeKey: "reviewed-pack-episode", EvidenceKeys: []string{"candidate:reviewed-pack"},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.PrepareHardwareFaultFeedbackPack(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.HistoryPackStatus != "manifest_ready_pending_metric_extraction" || prepared.HistoryPackSHA256 == "" {
		t.Fatalf("reviewed pack was not prepared: %+v", prepared)
	}
	if !strings.Contains(prepared.HistoryPackScope, "target_scope=gpu") || !strings.Contains(prepared.HistoryPackScope, "gpu_uuid=GPU-FAULT-TIME") || !strings.Contains(prepared.HistoryPackScope, "fault="+confirmed.Format(time.RFC3339)) || !strings.Contains(prepared.HistoryPackScope, "review_sha256="+review.ReviewSHA256) {
		t.Fatalf("pack provenance did not use immutable reviewed facts: %s", prepared.HistoryPackScope)
	}
	var stored api.HardwareFaultFeedbackRequest
	if err := db.First(&stored, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TargetScope != "multi_gpu" || stored.GPUUUID != "" || !stored.FaultOccurredAt.Equal(reported) {
		t.Fatalf("source facts were overwritten by reviewed pack preparation: %+v", stored)
	}
	threshold, probability := .5, .9
	spec := api.PredictionModelSpec{ModelKey: "reviewed-pack", Version: "1", HardwareClass: "gpu", EntityType: "gpu", Task: "failure_probability", HorizonMinutes: 1440, Algorithm: "test", Runtime: "test", Mode: "shadow", Status: "shadow_candidate", FeatureContractVersion: FeatureContractVersion, LabelContractVersion: LabelContractVersion, DecisionThreshold: &threshold}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.HardwareRiskPrediction{ModelSpecID: spec.ID, ModelVersion: spec.Version, HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-FAULT-TIME", GPUUUID: "GPU-FAULT-TIME", NodeIP: row.NodeIP, HorizonMinutes: 1440, Probability: &probability, RiskLevel: "unvalidated", Status: "shadow_observation", EvaluatedAt: confirmed.Add(-time.Hour), ObservedAt: confirmed.Add(-time.Hour), ExpiresAt: confirmed.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	reviewed, err := service.ReviewHardwareFaultFeedbackWarning(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.WarningReviewStatus != "manual_feedback_prior_shadow_candidate_found" || reviewed.MatchedWarningCount != 1 || !strings.Contains(reviewed.WarningReviewNote, review.ReviewSHA256) {
		t.Fatalf("warning review did not use immutable reviewed GPU/onset: %+v", reviewed)
	}
}

func intPointer(value int) *int { return &value }

func TestBaseboardFaultFeedbackAllowsNodeScopeAndDatePrecision(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	last := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&api.MonitoringHistoryAudit{
		SourceKey: "current-prometheus", SourceName: "Current Prometheus", SourceType: "prometheus", BaseURL: "http://prometheus",
		Status: "success", EarliestSampleAt: &first, LatestSampleAt: &last, StartedAt: first, FinishedAt: last,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.36", TargetScope: "baseboard", AffectedGPUIndexes: []string{"0", "1", "2", "3"},
		FaultType: "gpu_baseboard_fault", FaultOccurredAt: "2026-08-15", FaultTimePrecision: "date",
		PreWindowHours: 72, PostWindowHours: 24, Operator: "admin", RepairAction: "replace_gpu_baseboard",
		HardwareReplaced: true, TrainingEligible: true,
		Description: "GPU baseboard fault; cross-swapping GPU did not recover; replacing GPU baseboard recovered the node",
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.TargetScope != "baseboard" || row.GPUUUID != "" || row.GPUIndex != 0 || row.IdentityResolutionStatus != "node_or_board_scope" {
		t.Fatalf("baseboard feedback should not be forced into single-GPU identity: %+v", row)
	}
	if row.FaultTimePrecision != "date" || !row.FaultOccurredAt.Equal(time.Date(2026, 8, 15, 0, 0, 0, 0, feedbackLocalLocation)) || row.FaultWindowStartAt == nil || row.FaultWindowEndAt == nil {
		t.Fatalf("date precision window not persisted: %+v", row)
	}
	if len(row.AffectedGPUIndexes) != 4 || !strings.Contains(row.HistoryPackScope, "target_scope=baseboard") || !strings.Contains(row.HistoryPackScope, "time_precision=date") {
		t.Fatalf("baseboard scope metadata missing: %+v", row)
	}
	prepared, err := service.PrepareHardwareFaultFeedbackPack(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "history_pack_manifest_ready" || prepared.HistoryPackStatus != "manifest_ready_pending_metric_extraction" || prepared.HistoryPackSHA256 == "" {
		t.Fatalf("baseboard feedback should prepare node-scoped history pack without GPU UUID: %+v", prepared)
	}
	reviewed, err := service.ReviewHardwareFaultFeedbackWarning(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.WarningReviewStatus != "manual_feedback_no_prior_shadow_warning" || reviewed.MatchedWarningCount != 0 {
		t.Fatalf("baseboard node-scope warning review failed: %+v", reviewed)
	}
}

func TestReviewHardwareFaultFeedbackWarningMarksNoPriorShadowWarning(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.25", GPUUUID: "GPU-NO-WARNING", GPUIndex: 1,
		FaultType: "gpu_hardware_failure", FaultOccurredAt: "2026-08-21T08:00:00Z",
		PreWindowHours: 12, Operator: "ops-e", TrainingEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := service.ReviewHardwareFaultFeedbackWarning(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.WarningReviewStatus != "manual_feedback_no_prior_shadow_warning" || reviewed.MatchedWarningCount != 0 || reviewed.WarningReviewWindowHours != 12 {
		t.Fatalf("no prior warning should become false-negative review evidence: %+v", reviewed)
	}
	if !strings.Contains(reviewed.WarningReviewNote, "false-negative") || len(reviewed.MatchedWarningKeys) != 0 {
		t.Fatalf("review note/keys not recorded correctly: %+v", reviewed)
	}
}

func TestReviewHardwareFaultFeedbackWarningFindsPriorShadowCandidate(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{
		NodeIP: "10.114.4.26", GPUUUID: "GPU-WARNED", GPUIndex: 2,
		FaultType: "pcie_link_failure", FaultOccurredAt: "2026-08-21T08:00:00Z",
		PreWindowHours: 24, Operator: "ops-f", TrainingEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	probability := 0.82
	threshold := 0.60
	spec := api.PredictionModelSpec{
		ModelKey: "gpu.failure.within_24h", Version: "baseline-gpu-failure-v1", HardwareClass: "gpu", EntityType: "gpu", Task: "failure_prediction",
		HorizonMinutes: 1440, Algorithm: "xgboost", Runtime: "offline", Mode: "shadow", Status: "shadow_candidate",
		FeatureContractVersion: "prediction-feature-contract-v1", LabelContractVersion: "prediction-label-contract-v1", DecisionThreshold: &threshold,
	}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.HardwareRiskPrediction{
		ModelSpecID: spec.ID, ModelVersion: "baseline-gpu-failure-v1", HardwareClass: "gpu", EntityType: "gpu",
		EntityKey: "GPU-WARNED", GPUUUID: "GPU-WARNED", NodeIP: "10.114.4.26", HorizonMinutes: 1440,
		Probability: &probability, RiskLevel: "unvalidated", Status: "shadow_observation",
		ObservedAt:  time.Date(2026, 8, 21, 6, 45, 0, 0, time.UTC),
		EvaluatedAt: time.Date(2026, 8, 21, 7, 0, 0, 0, time.UTC),
		ExpiresAt:   time.Date(2026, 8, 22, 7, 0, 0, 0, time.UTC),
	}).Error; err != nil {
		t.Fatal(err)
	}
	reviewed, err := service.ReviewHardwareFaultFeedbackWarning(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.WarningReviewStatus != "manual_feedback_prior_shadow_candidate_found" || reviewed.MatchedWarningCount != 1 {
		t.Fatalf("prior shadow candidate not linked: %+v", reviewed)
	}
	if len(reviewed.MatchedWarningKeys) != 1 || !strings.Contains(reviewed.MatchedWarningKeys[0], "prediction:") || !strings.Contains(reviewed.WarningReviewNote, "matched 1") {
		t.Fatalf("matched warning evidence not recorded: %+v", reviewed)
	}
}

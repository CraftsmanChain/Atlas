package prediction

import (
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestHardwareFaultFeedbackReviewIsHashChainedAndRawTimeImmutable(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	original := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{NodeIP: "10.114.4.30", GPUUUID: "GPU-REVIEW", GPUIndex: 2, FaultType: "gpu_hardware_failure", FaultOccurredAt: original.Format(time.RFC3339), Operator: "ops", TrainingEligible: true})
	if err != nil {
		t.Fatal(err)
	}
	corrected := original.Add(17 * time.Minute)
	if err := db.Create(&api.HistoricalGPUIdentityInterval{IntervalKey: "review-identity", SourceKey: "primary", NodeIP: row.NodeIP, GPUIndex: row.GPUIndex, GPUUUID: row.GPUUUID, FirstSeenAt: original.Add(-24 * time.Hour), LastSeenAt: original.Add(24 * time.Hour), ObservationCount: 20, EvidenceStrength: "strong"}).Error; err != nil {
		t.Fatal(err)
	}
	first, err := service.ReviewHardwareFaultFeedback(row.ID, HardwareFaultFeedbackReviewInput{Decision: "confirmed_hardware", Reviewer: "reviewer-a", ReviewNote: "XID and replacement record agree", ConfirmedOnsetAt: corrected.Format(time.RFC3339), ConfirmedWindowStartAt: corrected.Add(-time.Hour).Format(time.RFC3339), ConfirmedWindowEndAt: corrected.Add(time.Hour).Format(time.RFC3339), EvidenceKeys: []string{"monitor:candidate-1", "repair:ticket-9"}, EpisodeKey: "episode-9"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ReviewHardwareFaultFeedback(row.ID, HardwareFaultFeedbackReviewInput{Decision: "needs_evidence", Reviewer: "reviewer-b", ReviewNote: "vendor repair result is still missing", EvidenceKeys: []string{"monitor:candidate-1"}, EpisodeKey: "episode-9"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || second.Revision != 2 || second.PreviousReviewSHA256 != first.ReviewSHA256 || first.ReviewSHA256 == second.ReviewSHA256 {
		t.Fatalf("review chain is invalid: first=%+v second=%+v", first, second)
	}
	var stored api.HardwareFaultFeedbackRequest
	if err := db.First(&stored, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.FaultOccurredAt.Equal(original) || stored.TrainingEligible || stored.TriageStatus != "needs_evidence" {
		t.Fatalf("raw time was rewritten or latest review was not projected safely: %+v", stored)
	}
}

func TestHardwareFaultFeedbackReviewRequiresFaultTimeIdentity(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	onset := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{NodeIP: "10.114.4.99", GPUUUID: "GPU-CURRENT-ONLY", GPUIndex: 0, FaultType: "gpu_hardware_failure", FaultOccurredAt: onset.Format(time.RFC3339), Operator: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ReviewHardwareFaultFeedback(row.ID, HardwareFaultFeedbackReviewInput{Decision: "confirmed_hardware", Reviewer: "reviewer", ReviewNote: "repair evidence exists", EvidenceKeys: []string{"repair:ticket"}})
	if err == nil || !strings.Contains(err.Error(), "identity interval is missing") {
		t.Fatalf("current identity must not prove historical identity: %v", err)
	}
}

func TestHardwareFaultEvidenceEpisodeAndValueReports(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	onset := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	row, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{NodeIP: "10.114.4.31", GPUUUID: "GPU-VALUE", GPUIndex: 1, FaultType: "pcie_link_failure", FaultOccurredAt: onset.Format(time.RFC3339), PreWindowHours: 24, PostWindowHours: 2, Operator: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.HistoricalGPUIdentityInterval{IntervalKey: "value-identity", SourceKey: "primary", NodeIP: row.NodeIP, GPUIndex: row.GPUIndex, GPUUUID: row.GPUUUID, FirstSeenAt: onset.Add(-24 * time.Hour), LastSeenAt: onset.Add(24 * time.Hour), ObservationCount: 20, EvidenceStrength: "strong"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.HistoricalFaultCandidate{CandidateKey: "candidate-value", SourceKey: "primary", EntityType: "gpu", GPUUUID: "GPU-VALUE", NodeIP: "10.114.4.31", EventType: "xid_79", EventCode: "79", QualityTier: "weak_proxy", ReviewStatus: "pending", SourceMetric: "DCGM_FI_DEV_XID_ERRORS", OnsetAt: onset.Add(-5 * time.Minute), DetectionWindowEndAt: onset}).Error; err != nil {
		t.Fatal(err)
	}
	matches, err := service.HardwareFaultEvidenceMatches(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if matches.CandidateCount != 1 || matches.Candidates[0].EvidenceSHA256 == "" || !matches.ReadOnly || !matches.NoActionExecuted {
		t.Fatalf("unexpected evidence matches: %+v", matches)
	}
	review, err := service.ReviewHardwareFaultFeedback(row.ID, HardwareFaultFeedbackReviewInput{Decision: "confirmed_hardware", Reviewer: "reviewer", ReviewNote: "matched XID and repair evidence", EvidenceKeys: []string{"candidate:candidate-value"}, EpisodeKey: "episode-value"})
	if err != nil {
		t.Fatal(err)
	}
	threshold := .7
	spec := api.PredictionModelSpec{ModelKey: "test.1h", Version: "1", HardwareClass: "gpu", EntityType: "gpu", Task: "failure_probability", HorizonMinutes: 60, Algorithm: "test", Runtime: "test", Mode: "shadow", Status: "shadow_candidate", FeatureContractVersion: FeatureContractVersion, LabelContractVersion: LabelContractVersion, DecisionThreshold: &threshold, Current: true}
	if err := db.Create(&spec).Error; err != nil {
		t.Fatal(err)
	}
	probability := .8
	if err := db.Create(&api.HardwareRiskPrediction{ShadowRunID: 7, ModelSpecID: spec.ID, ModelVersion: "1", HardwareClass: "gpu", EntityType: "gpu", EntityKey: "GPU-VALUE", GPUUUID: "GPU-VALUE", NodeIP: "10.114.4.31", HorizonMinutes: 60, Probability: &probability, RiskLevel: "unvalidated", Status: "shadow_observation", EvaluatedAt: onset.Add(-30 * time.Minute), ExpiresAt: onset.Add(30 * time.Minute), ObservedAt: onset.Add(-30 * time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	episodes, err := service.HardwareFaultEpisodes()
	if err != nil {
		t.Fatal(err)
	}
	if episodes.EpisodeCount != 1 || episodes.TrainingEpisodes != 1 || episodes.Episodes[0].EpisodeKey != review.EpisodeKey {
		t.Fatalf("unexpected episodes: %+v", episodes)
	}
	value, err := service.HardwareFaultValueReport()
	if err != nil {
		t.Fatal(err)
	}
	if value.ConfirmedCount != 1 || value.TrainingEpisodeCount != 1 || len(value.Horizons) != 4 || value.Horizons[0].WarningHits != 1 || value.Horizons[0].Recall != 1 || value.Horizons[0].MedianLeadMinutes != 30 || len(value.RankingAtK) != 3 || value.RankingAtK[0].Hits != 1 || !value.NoActionExecuted {
		t.Fatalf("unexpected value report: %+v", value)
	}
	again, err := service.HardwareFaultValueReport()
	if err != nil {
		t.Fatal(err)
	}
	if again.ReportSHA256 != value.ReportSHA256 {
		t.Fatalf("unchanged evidence must keep a stable report SHA: %s != %s", again.ReportSHA256, value.ReportSHA256)
	}
}

func TestHardwareFaultEpisodeConflictsAreNotTrainingEligible(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db"); if err != nil { t.Fatal(err) }
	service := NewService(db); onset := time.Date(2026,8,21,8,0,0,0,time.UTC)
	first, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{NodeIP:"10.114.4.50",GPUUUID:"GPU-CONFLICT",GPUIndex:0,FaultType:"gpu_hardware_failure",FaultOccurredAt:onset.Format(time.RFC3339),Operator:"ops"}); if err != nil { t.Fatal(err) }
	second, err := service.CreateHardwareFaultFeedback(HardwareFaultFeedbackInput{NodeIP:"10.114.4.50",GPUUUID:"GPU-CONFLICT",GPUIndex:0,FaultType:"gpu_hardware_failure",FaultOccurredAt:onset.Add(time.Minute).Format(time.RFC3339),Operator:"ops"}); if err != nil { t.Fatal(err) }
	if err := db.Create(&api.HistoricalGPUIdentityInterval{IntervalKey:"conflict-identity",SourceKey:"primary",NodeIP:first.NodeIP,GPUIndex:0,GPUUUID:first.GPUUUID,FirstSeenAt:onset.Add(-time.Hour),LastSeenAt:onset.Add(time.Hour),ObservationCount:10,EvidenceStrength:"strong"}).Error; err != nil { t.Fatal(err) }
	if _, err := service.ReviewHardwareFaultFeedback(first.ID, HardwareFaultFeedbackReviewInput{Decision:"confirmed_hardware",Reviewer:"a",ReviewNote:"monitor evidence",EvidenceKeys:[]string{"candidate:1"},EpisodeKey:"shared-episode"}); err != nil { t.Fatal(err) }
	if _, err := service.ReviewHardwareFaultFeedback(second.ID, HardwareFaultFeedbackReviewInput{Decision:"excluded_no_hardware",Reviewer:"b",ReviewNote:"vendor found no hardware fault",EpisodeKey:"shared-episode"}); err != nil { t.Fatal(err) }
	report, err := service.HardwareFaultEpisodes(); if err != nil { t.Fatal(err) }
	if report.EpisodeCount != 1 || report.TrainingEpisodes != 0 || !report.Episodes[0].ReviewConflict || report.Episodes[0].Decision != "conflicting_reviews" { t.Fatalf("conflicting episode must be blocked: %+v", report) }
}

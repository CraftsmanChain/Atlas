package prediction

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestShadowRegistryPromotesOnlyIntegrityCheckedCandidate(t *testing.T) {
	db, err := storage.InitDB(filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := SeedBuiltins(db); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	artifactPath := filepath.Join(directory, "models.json")
	reportPath := filepath.Join(directory, "evaluation_report.json")
	artifact := `{"version":"gpu-logistic-baseline-v9","matrix_key":"matrix-v4","scope_event_type":"xid_94_contained_ecc","scope_model_name":"NVIDIA H100 80GB HBM3","models":[{"horizon_minutes":10080,"feature_columns":["gpu_temp_mean_24h","gpu_temp_slope_per_hour_24h"],"threshold":0.35,"calibration":{"version":"validation-platt-v1","status":"fitted","slope":0.4}}]}`
	report := `{"version":"gpu-logistic-baseline-v9","matrix_key":"matrix-v4","scope_event_type":"xid_94_contained_ecc","scope_model_name":"NVIDIA H100 80GB HBM3","feature_audit":{"status":"passed","prohibited_selected_count":0},"horizons":[{"horizon_minutes":10080,"cross_split_status":"robust_candidate","release_readiness":"shadow_candidate","threshold":0.35,"test_calibration":{"status":"passed"}}]}`
	if err := os.WriteFile(artifactPath, []byte(artifact), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	checksum, err := sha256File(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	finished := time.Date(2026, 8, 4, 10, 18, 43, 0, time.UTC)
	build := api.BaselineModelBuild{
		BaselineModelKey: "gpu-logistic-baseline-v9-test", Version: "gpu-logistic-baseline-v9", Status: "completed",
		Algorithm: "logistic_regression", SourceMatrixBuildID: 4, SourceTrainingMatrixKey: "matrix-v4",
		FeatureContractVersion: "1.9.0", ScopeEventType: "xid_94_contained_ecc", ScopeModelName: "NVIDIA H100 80GB HBM3",
		FeatureAuditStatus: "passed", ShadowCandidateCount: 1, ArtifactPath: artifactPath,
		ArtifactSHA256: checksum, ReportPath: reportPath, StartedAt: finished.Add(-time.Minute), FinishedAt: &finished,
	}
	if err := db.Create(&build).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	service.now = func() time.Time { return finished.Add(time.Hour) }
	summary, err := service.SyncShadowModelRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if summary.ModelsRegistered != 1 || summary.BuildsRejected != 0 {
		t.Fatalf("unexpected registry summary: %+v", summary)
	}
	models, err := service.Models()
	if err != nil {
		t.Fatal(err)
	}
	var candidate *api.PredictionModelSpec
	for index := range models {
		if models[index].Status == "shadow_candidate" {
			candidate = &models[index]
		}
	}
	if candidate == nil || candidate.SourceBaselineBuildID != build.ID || candidate.HorizonMinutes != 10080 || candidate.DecisionThreshold == nil || *candidate.DecisionThreshold != 0.35 || candidate.RegistryGateVersion != ShadowRegistryGateVersion {
		t.Fatalf("candidate provenance was not registered: %+v", candidate)
	}
	overview, err := service.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if overview.Phase != "gpu_shadow_candidate_registered" || overview.ScoringEnabled || overview.ProbabilityEmitted {
		t.Fatalf("candidate registration bypassed shadow safety: %+v", overview)
	}
	if err := service.SyncFeatureParityAudits(); err != nil {
		t.Fatal(err)
	}
	audits, err := service.FeatureParityAudits(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].Status != "replay_required" || audits[0].TrainingFeatureCount != 2 || audits[0].ContractMatchedCount != 2 || audits[0].SourceMetricCount != 1 || audits[0].ScoringAllowed {
		t.Fatalf("unexpected feature parity audit: %+v", audits)
	}
}

func TestShadowRegistryRejectsTamperedArtifact(t *testing.T) {
	db, err := storage.InitDB(filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	artifactPath := filepath.Join(directory, "models.json")
	reportPath := filepath.Join(directory, "evaluation_report.json")
	if err := os.WriteFile(artifactPath, []byte(`{"models":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	build := api.BaselineModelBuild{
		BaselineModelKey: "tampered", Version: "gpu-logistic-baseline-v9", Status: "completed", Algorithm: "logistic_regression",
		SourceMatrixBuildID: 4, SourceTrainingMatrixKey: "matrix-v4", FeatureContractVersion: "1.9.0",
		ScopeEventType: "xid_94_contained_ecc", ScopeModelName: "NVIDIA H100 80GB HBM3", FeatureAuditStatus: "passed",
		ShadowCandidateCount: 1, ArtifactPath: artifactPath, ArtifactSHA256: "not-the-file-checksum", ReportPath: reportPath, StartedAt: time.Now(),
	}
	if err := db.Create(&build).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	summary, err := service.SyncShadowModelRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if summary.BuildsRejected != 1 || summary.ModelsRegistered != 0 || len(summary.Errors) != 1 {
		t.Fatalf("tampered artifact was not rejected: %+v", summary)
	}
}

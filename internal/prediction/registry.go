package prediction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ShadowRegistryGateVersion = "gpu-shadow-registry-gate-v1"

type ShadowRegistrySummary struct {
	BuildsEvaluated  int       `json:"builds_evaluated"`
	ModelsRegistered int       `json:"models_registered"`
	BuildsRejected   int       `json:"builds_rejected"`
	Errors           []string  `json:"errors"`
	EvaluatedAt      time.Time `json:"evaluated_at"`
}

type registryReport struct {
	Version        string `json:"version"`
	MatrixKey      string `json:"matrix_key"`
	ScopeEventType string `json:"scope_event_type"`
	ScopeModelName string `json:"scope_model_name"`
	FeatureAudit   struct {
		Status                  string `json:"status"`
		ProhibitedSelectedCount int    `json:"prohibited_selected_count"`
	} `json:"feature_audit"`
	Horizons []struct {
		HorizonMinutes   int     `json:"horizon_minutes"`
		CrossSplitStatus string  `json:"cross_split_status"`
		ReleaseReadiness string  `json:"release_readiness"`
		Threshold        float64 `json:"threshold"`
		TestCalibration  struct {
			Status string `json:"status"`
		} `json:"test_calibration"`
	} `json:"horizons"`
}

type registryArtifact struct {
	Version        string `json:"version"`
	MatrixKey      string `json:"matrix_key"`
	ScopeEventType string `json:"scope_event_type"`
	ScopeModelName string `json:"scope_model_name"`
	Models         []struct {
		HorizonMinutes int      `json:"horizon_minutes"`
		FeatureColumns []string `json:"feature_columns"`
		Threshold      float64  `json:"threshold"`
		Calibration    struct {
			Version string  `json:"version"`
			Status  string  `json:"status"`
			Slope   float64 `json:"slope"`
		} `json:"calibration"`
	} `json:"models"`
}

func (s *Service) SyncShadowModelRegistry() (ShadowRegistrySummary, error) {
	summary := ShadowRegistrySummary{Errors: []string{}, EvaluatedAt: s.now()}
	var builds []api.BaselineModelBuild
	if err := s.db.Where("status = ? AND shadow_candidate_count > ?", "completed", 0).Order("id").Find(&builds).Error; err != nil {
		return summary, err
	}
	for _, build := range builds {
		summary.BuildsEvaluated++
		registered, err := s.registerShadowBuild(build)
		if err != nil {
			summary.BuildsRejected++
			summary.Errors = append(summary.Errors, fmt.Sprintf("build %d: %v", build.ID, err))
			continue
		}
		summary.ModelsRegistered += registered
	}
	return summary, nil
}

func (s *Service) RunShadowModelRegistrySync(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		summary, err := s.SyncShadowModelRegistry()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("prediction shadow registry sync failed: %v", err)
			}
			return
		}
		if summary.BuildsRejected > 0 {
			log.Printf("prediction shadow registry rejected builds: %s", strings.Join(summary.Errors, "; "))
		}
		if err := s.SyncFeatureParityAudits(); err != nil {
			log.Printf("prediction feature parity audit failed: %v", err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (s *Service) registerShadowBuild(build api.BaselineModelBuild) (int, error) {
	if build.FeatureAuditStatus != "passed" || build.ProhibitedFeatureCount != 0 {
		return 0, fmt.Errorf("feature leakage audit did not pass")
	}
	if build.ArtifactPath == "" || build.ReportPath == "" || build.ArtifactSHA256 == "" {
		return 0, fmt.Errorf("artifact, report, and checksum are required")
	}
	checksum, err := sha256File(build.ArtifactPath)
	if err != nil {
		return 0, fmt.Errorf("verify artifact: %w", err)
	}
	if !strings.EqualFold(checksum, build.ArtifactSHA256) {
		return 0, fmt.Errorf("artifact checksum mismatch")
	}
	var report registryReport
	if err := decodeRegistryJSON(build.ReportPath, &report); err != nil {
		return 0, fmt.Errorf("decode evaluation report: %w", err)
	}
	var artifact registryArtifact
	if err := decodeRegistryJSON(build.ArtifactPath, &artifact); err != nil {
		return 0, fmt.Errorf("decode model artifact: %w", err)
	}
	if report.Version != build.Version || artifact.Version != build.Version || report.MatrixKey != build.SourceTrainingMatrixKey || artifact.MatrixKey != build.SourceTrainingMatrixKey {
		return 0, fmt.Errorf("build provenance does not match report and artifact")
	}
	if report.ScopeEventType != build.ScopeEventType || artifact.ScopeEventType != build.ScopeEventType || report.ScopeModelName != build.ScopeModelName || artifact.ScopeModelName != build.ScopeModelName {
		return 0, fmt.Errorf("build scope does not match report and artifact")
	}
	if report.FeatureAudit.Status != "passed" || report.FeatureAudit.ProhibitedSelectedCount != 0 {
		return 0, fmt.Errorf("report feature leakage audit did not pass")
	}
	models := make(map[int]struct {
		Threshold float64
		Status    string
		Version   string
		Slope     float64
	}, len(artifact.Models))
	for _, model := range artifact.Models {
		models[model.HorizonMinutes] = struct {
			Threshold float64
			Status    string
			Version   string
			Slope     float64
		}{model.Threshold, model.Calibration.Status, model.Calibration.Version, model.Calibration.Slope}
	}
	candidates := make([]api.PredictionModelSpec, 0, len(report.Horizons))
	for _, horizon := range report.Horizons {
		if horizon.ReleaseReadiness != "shadow_candidate" {
			continue
		}
		if horizon.CrossSplitStatus != "robust_candidate" || horizon.TestCalibration.Status != "passed" {
			return 0, fmt.Errorf("horizon %d bypassed stability or calibration gate", horizon.HorizonMinutes)
		}
		model, exists := models[horizon.HorizonMinutes]
		if !exists || model.Status != "fitted" || model.Version == "" || model.Slope <= 0 || horizon.Threshold <= 0 || horizon.Threshold >= 1 || math.Abs(model.Threshold-horizon.Threshold) > 1e-12 {
			return 0, fmt.Errorf("horizon %d model calibration or threshold mismatch", horizon.HorizonMinutes)
		}
		threshold := horizon.Threshold
		trainedAt := build.FinishedAt
		if trainedAt == nil {
			value := build.UpdatedAt
			trainedAt = &value
		}
		key := shadowModelKey(build.ScopeEventType, build.ScopeModelName, horizon.HorizonMinutes)
		version := build.Version + ".build-" + strconv.FormatUint(uint64(build.ID), 10)
		candidates = append(candidates, api.PredictionModelSpec{
			ModelKey: key, Version: version, HardwareClass: "gpu", EntityType: "gpu",
			Task: "failure_probability:" + build.ScopeEventType, HorizonMinutes: horizon.HorizonMinutes,
			Algorithm: build.Algorithm, Runtime: "atlas_native_logistic_v1", Mode: "shadow",
			Status: "shadow_candidate", FeatureContractVersion: build.FeatureContractVersion,
			LabelContractVersion: LabelContractVersion, DatasetVersion: build.SourceTrainingMatrixKey,
			SourceBaselineBuildID: build.ID, ScopeEventType: build.ScopeEventType, ScopeModelName: build.ScopeModelName,
			ArtifactURI: build.ArtifactPath, ArtifactSHA256: build.ArtifactSHA256,
			RegistryGateVersion: ShadowRegistryGateVersion, DecisionThreshold: &threshold,
			Current: true, TrainedAt: trainedAt,
		})
	}
	if len(candidates) == 0 {
		return 0, fmt.Errorf("report contains no eligible shadow candidate horizon")
	}
	registered := 0
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, spec := range candidates {
			var current api.PredictionModelSpec
			err := tx.Where("model_key = ? AND current = ?", spec.ModelKey, true).Order("source_baseline_build_id DESC, id DESC").First(&current).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && current.SourceBaselineBuildID > build.ID {
				continue
			}
			if err := tx.Model(&api.PredictionModelSpec{}).Where("model_key = ? AND current = ? AND version <> ?", spec.ModelKey, true, spec.Version).Update("current", false).Error; err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "model_key"}, {Name: "version"}},
				DoUpdates: clause.AssignmentColumns([]string{"hardware_class", "entity_type", "task", "horizon_minutes", "algorithm", "runtime", "mode", "status", "feature_contract_version", "label_contract_version", "dataset_version", "source_baseline_build_id", "scope_event_type", "scope_model_name", "artifact_uri", "artifact_sha256", "registry_gate_version", "decision_threshold", "current", "trained_at", "updated_at"}),
			}).Create(&spec).Error; err != nil {
				return err
			}
			registered++
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return registered, nil
}

func decodeRegistryJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(io.LimitReader(file, 32<<20)).Decode(target)
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > 64<<20 {
		return "", fmt.Errorf("artifact exceeds 64 MiB registry limit")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

var nonModelKeyCharacter = regexp.MustCompile(`[^a-z0-9]+`)

func shadowModelKey(eventType, modelName string, horizon int) string {
	slug := func(value string) string {
		return strings.Trim(nonModelKeyCharacter.ReplaceAllString(strings.ToLower(value), "_"), "_")
	}
	return fmt.Sprintf("gpu.%s.%s.within_%dm", slug(eventType), slug(modelName), horizon)
}

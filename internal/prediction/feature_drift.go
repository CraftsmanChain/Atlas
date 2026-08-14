package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"time"

	"atlas/pkg/api"
)

const (
	FeatureDriftReportVersion = "prediction-feature-drift-report-v1"
	featureDriftSampleLimit   = 8
)

type FeatureDriftFeature struct {
	Name               string  `json:"name"`
	Mean               float64 `json:"mean"`
	Scale              float64 `json:"scale"`
	DistributionStatus string  `json:"distribution_status"`
}

type FeatureDriftReplaySnapshot struct {
	ReplayID                  uint       `json:"replay_id"`
	ReplayKey                 string     `json:"replay_key"`
	Version                   string     `json:"version"`
	Status                    string     `json:"status"`
	ReportSHA256              string     `json:"report_sha256,omitempty"`
	TrainingFeatureCount      int        `json:"training_feature_count"`
	VerifiedColumnCount       int        `json:"verified_column_count"`
	ComparedValueCount        int        `json:"compared_value_count"`
	MismatchCount             int        `json:"mismatch_count"`
	MissingTrainingValueCount int        `json:"missing_training_value_count"`
	MissingReplayValueCount   int        `json:"missing_replay_value_count"`
	FinishedAt                *time.Time `json:"finished_at,omitempty"`
}

type FeatureDriftReport struct {
	Version                  string                      `json:"version"`
	FrameworkVersion         string                      `json:"framework_version"`
	Mode                     string                      `json:"mode"`
	Status                   string                      `json:"status"`
	ReportSHA256             string                      `json:"report_sha256"`
	Method                   string                      `json:"method"`
	SourceBaselineBuildID    uint                        `json:"source_baseline_build_id"`
	BaselineModelKey         string                      `json:"baseline_model_key,omitempty"`
	BaselineVersion          string                      `json:"baseline_version,omitempty"`
	ArtifactPath             string                      `json:"artifact_path,omitempty"`
	ArtifactSHA256           string                      `json:"artifact_sha256,omitempty"`
	ArtifactLocalSHA256      string                      `json:"artifact_local_sha256,omitempty"`
	FeatureContractVersion   string                      `json:"feature_contract_version,omitempty"`
	FeatureColumnCount       int                         `json:"feature_column_count"`
	FeatureDistributionCount int                         `json:"feature_distribution_count"`
	PSIStatus                string                      `json:"psi_status"`
	KSStatus                 string                      `json:"ks_status"`
	LatestReplay             *FeatureDriftReplaySnapshot `json:"latest_replay,omitempty"`
	SampleFeatures           []FeatureDriftFeature       `json:"sample_features"`
	BlockingReasons          []string                    `json:"blocking_reasons"`
	RecommendedNextRun       []string                    `json:"recommended_next_run"`
	GeneratedAt              time.Time                   `json:"generated_at"`
}

type featureDriftArtifact struct {
	Version string `json:"version"`
	Models  []struct {
		HorizonMinutes int       `json:"horizon_minutes"`
		FeatureColumns []string  `json:"feature_columns"`
		Means          []float64 `json:"means"`
		Scales         []float64 `json:"scales"`
	} `json:"models"`
}

func (s *Service) FeatureDriftReport() (FeatureDriftReport, error) {
	report := FeatureDriftReport{
		Version: FeatureDriftReportVersion, FrameworkVersion: FrameworkVersion, Mode: "read_only_feature_distribution_readiness",
		Method:      "checks baseline artifact feature columns and latest feature replay parity; feature-level PSI/KS remains pending until per-column training and live distributions are persisted",
		PSIStatus:   "pending_distribution_store",
		KSStatus:    "pending_distribution_store",
		GeneratedAt: s.now(),
		RecommendedNextRun: []string{
			"persist training feature quantiles or histograms for each selected model feature",
			"persist live shadow feature quantiles or histograms from read-only extraction",
			"compute feature-level PSI and KS only after both training and live per-column distributions are available",
			"keep this gate read-only; do not tune thresholds or trigger actions from missing distribution evidence",
		},
	}
	var build api.BaselineModelBuild
	if err := s.db.Where("status = ? AND artifact_path <> ?", "completed", "").Order("finished_at DESC, id DESC").Limit(1).Find(&build).Error; err != nil {
		return FeatureDriftReport{}, err
	}
	if build.ID == 0 {
		report.Status = "blocked_no_baseline_artifact"
		report.BlockingReasons = append(report.BlockingReasons, "no completed baseline model artifact is available")
		report.ReportSHA256 = featureDriftChecksum(report)
		return report, nil
	}
	report.SourceBaselineBuildID = build.ID
	report.BaselineModelKey = build.BaselineModelKey
	report.BaselineVersion = build.Version
	report.ArtifactPath = build.ArtifactPath
	report.ArtifactSHA256 = build.ArtifactSHA256
	report.FeatureContractVersion = build.FeatureContractVersion
	if build.ArtifactPath != "" {
		if sha, err := localFileSHA256(build.ArtifactPath); err == nil {
			report.ArtifactLocalSHA256 = sha
			if build.ArtifactSHA256 != "" && build.ArtifactSHA256 != sha {
				report.BlockingReasons = append(report.BlockingReasons, "baseline artifact SHA256 does not match local artifact")
			}
		} else {
			report.Status = "blocked_artifact_unreadable"
			report.BlockingReasons = append(report.BlockingReasons, "baseline artifact file is not readable")
			report.ReportSHA256 = featureDriftChecksum(report)
			return report, nil
		}
	}
	artifact, err := readFeatureDriftArtifact(build.ArtifactPath)
	if err != nil {
		report.Status = "blocked_artifact_unreadable"
		report.BlockingReasons = append(report.BlockingReasons, "baseline artifact file cannot be decoded")
		report.ReportSHA256 = featureDriftChecksum(report)
		return report, nil
	}
	report.FeatureColumnCount = featureDriftColumnCount(artifact)
	report.SampleFeatures = featureDriftSampleFeatures(artifact, featureDriftSampleLimit)

	var replay api.PredictionFeatureReplayRun
	if err := s.db.Where("source_baseline_build_id = ?", build.ID).Order("finished_at DESC, id DESC").Limit(1).Find(&replay).Error; err != nil {
		return FeatureDriftReport{}, err
	}
	if replay.ID > 0 {
		snapshot := featureDriftReplaySnapshot(replay)
		report.LatestReplay = &snapshot
		if replay.Status != "passed" {
			report.BlockingReasons = append(report.BlockingReasons, "latest feature replay has not passed")
		}
		if replay.TrainingFeatureCount > 0 && replay.VerifiedColumnCount < replay.TrainingFeatureCount {
			report.BlockingReasons = append(report.BlockingReasons, "latest feature replay has unverified training columns")
		}
	} else {
		report.BlockingReasons = append(report.BlockingReasons, "no feature replay exists for the latest baseline artifact")
	}
	if report.FeatureColumnCount == 0 {
		report.BlockingReasons = append(report.BlockingReasons, "baseline artifact has no feature columns")
	}
	report.BlockingReasons = append(report.BlockingReasons, "per-column training/live feature distributions are not persisted")
	report.BlockingReasons = uniqueSorted(report.BlockingReasons)
	report.Status = "blocked_feature_distributions_unavailable"
	if len(report.BlockingReasons) == 1 && report.BlockingReasons[0] == "per-column training/live feature distributions are not persisted" && report.LatestReplay != nil && report.LatestReplay.Status == "passed" {
		report.Status = "blocked_feature_distribution_store_required"
	}
	report.ReportSHA256 = featureDriftChecksum(report)
	return report, nil
}

func readFeatureDriftArtifact(path string) (featureDriftArtifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return featureDriftArtifact{}, err
	}
	defer file.Close()
	var payload featureDriftArtifact
	if err := json.NewDecoder(io.LimitReader(file, 64<<20)).Decode(&payload); err != nil {
		return featureDriftArtifact{}, err
	}
	return payload, nil
}

func featureDriftColumnCount(artifact featureDriftArtifact) int {
	seen := map[string]struct{}{}
	for _, model := range artifact.Models {
		for _, column := range model.FeatureColumns {
			seen[column] = struct{}{}
		}
	}
	return len(seen)
}

func featureDriftSampleFeatures(artifact featureDriftArtifact, limit int) []FeatureDriftFeature {
	if limit <= 0 {
		return nil
	}
	seen := map[string]struct{}{}
	features := make([]FeatureDriftFeature, 0, limit)
	for _, model := range artifact.Models {
		for index, column := range model.FeatureColumns {
			if _, exists := seen[column]; exists {
				continue
			}
			seen[column] = struct{}{}
			item := FeatureDriftFeature{Name: column, DistributionStatus: "pending_distribution_store"}
			if index < len(model.Means) {
				item.Mean = model.Means[index]
			}
			if index < len(model.Scales) {
				item.Scale = model.Scales[index]
			}
			features = append(features, item)
			if len(features) >= limit {
				return features
			}
		}
	}
	return features
}

func featureDriftReplaySnapshot(replay api.PredictionFeatureReplayRun) FeatureDriftReplaySnapshot {
	return FeatureDriftReplaySnapshot{
		ReplayID: replay.ID, ReplayKey: replay.ReplayKey, Version: replay.Version, Status: replay.Status,
		ReportSHA256: replay.ReportSHA256, TrainingFeatureCount: replay.TrainingFeatureCount, VerifiedColumnCount: replay.VerifiedColumnCount,
		ComparedValueCount: replay.ComparedValueCount, MismatchCount: replay.MismatchCount,
		MissingTrainingValueCount: replay.MissingTrainingValueCount, MissingReplayValueCount: replay.MissingReplayValueCount,
		FinishedAt: replay.FinishedAt,
	}
}

func featureDriftChecksum(report FeatureDriftReport) string {
	fingerprint := struct {
		Version                  string                      `json:"version"`
		FrameworkVersion         string                      `json:"framework_version"`
		Mode                     string                      `json:"mode"`
		Status                   string                      `json:"status"`
		Method                   string                      `json:"method"`
		SourceBaselineBuildID    uint                        `json:"source_baseline_build_id"`
		BaselineModelKey         string                      `json:"baseline_model_key,omitempty"`
		BaselineVersion          string                      `json:"baseline_version,omitempty"`
		ArtifactSHA256           string                      `json:"artifact_sha256,omitempty"`
		ArtifactLocalSHA256      string                      `json:"artifact_local_sha256,omitempty"`
		FeatureContractVersion   string                      `json:"feature_contract_version,omitempty"`
		FeatureColumnCount       int                         `json:"feature_column_count"`
		FeatureDistributionCount int                         `json:"feature_distribution_count"`
		PSIStatus                string                      `json:"psi_status"`
		KSStatus                 string                      `json:"ks_status"`
		LatestReplay             *FeatureDriftReplaySnapshot `json:"latest_replay,omitempty"`
		SampleFeatures           []FeatureDriftFeature       `json:"sample_features"`
		BlockingReasons          []string                    `json:"blocking_reasons"`
		RecommendedNextRun       []string                    `json:"recommended_next_run"`
	}{
		Version: report.Version, FrameworkVersion: report.FrameworkVersion, Mode: report.Mode, Status: report.Status,
		Method: report.Method, SourceBaselineBuildID: report.SourceBaselineBuildID,
		BaselineModelKey: report.BaselineModelKey, BaselineVersion: report.BaselineVersion,
		ArtifactSHA256: report.ArtifactSHA256, ArtifactLocalSHA256: report.ArtifactLocalSHA256,
		FeatureContractVersion: report.FeatureContractVersion, FeatureColumnCount: report.FeatureColumnCount,
		FeatureDistributionCount: report.FeatureDistributionCount, PSIStatus: report.PSIStatus, KSStatus: report.KSStatus,
		LatestReplay: report.LatestReplay, SampleFeatures: report.SampleFeatures,
		BlockingReasons: report.BlockingReasons, RecommendedNextRun: report.RecommendedNextRun,
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

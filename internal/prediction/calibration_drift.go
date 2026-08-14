package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"time"

	"atlas/pkg/api"
)

const (
	CalibrationDriftReportVersion = "prediction-calibration-drift-report-v1"
	calibrationMaximumECEDelta    = 0.05
	calibrationMaximumBrierDelta  = 0.05
	calibrationMaximumBSSDrop     = 0.05
)

type CalibrationDriftSnapshot struct {
	BuildID              uint       `json:"build_id"`
	BaselineModelKey     string     `json:"baseline_model_key"`
	Version              string     `json:"version"`
	Status               string     `json:"status"`
	ReportSHA256         string     `json:"report_sha256"`
	HorizonCount         int        `json:"horizon_count"`
	CalibrationPassed    int        `json:"calibration_passed"`
	CalibrationRequired  int        `json:"calibration_required"`
	InsufficientLabels   int        `json:"insufficient_labels"`
	MeanECE              float64    `json:"mean_ece"`
	MeanModelBrier       float64    `json:"mean_model_brier"`
	MeanNullBrier        float64    `json:"mean_null_brier"`
	MeanBrierSkillScore  float64    `json:"mean_brier_skill_score"`
	ReleaseReadyHorizons int        `json:"release_ready_horizons"`
	FinishedAt           *time.Time `json:"finished_at,omitempty"`
}

type CalibrationDriftReport struct {
	Version                 string                    `json:"version"`
	FrameworkVersion        string                    `json:"framework_version"`
	Mode                    string                    `json:"mode"`
	Status                  string                    `json:"status"`
	ReportSHA256            string                    `json:"report_sha256"`
	Method                  string                    `json:"method"`
	MinimumReports          int                       `json:"minimum_reports"`
	ECEDeltaThreshold       float64                   `json:"ece_delta_threshold"`
	BrierDeltaThreshold     float64                   `json:"brier_delta_threshold"`
	BrierSkillDropThreshold float64                   `json:"brier_skill_drop_threshold"`
	Latest                  *CalibrationDriftSnapshot `json:"latest,omitempty"`
	Baseline                *CalibrationDriftSnapshot `json:"baseline,omitempty"`
	ECEDelta                float64                   `json:"ece_delta"`
	ModelBrierDelta         float64                   `json:"model_brier_delta"`
	BrierSkillScoreDelta    float64                   `json:"brier_skill_score_delta"`
	BlockingReasons         []string                  `json:"blocking_reasons"`
	RecommendedNextRun      []string                  `json:"recommended_next_run"`
	GeneratedAt             time.Time                 `json:"generated_at"`
}

type calibrationReportFile struct {
	Horizons []struct {
		ReleaseReadiness string `json:"release_readiness"`
		TestCalibration  struct {
			Status          string  `json:"status"`
			ECE             float64 `json:"ece"`
			ModelBrier      float64 `json:"model_brier"`
			NullBrier       float64 `json:"null_brier"`
			BrierSkillScore float64 `json:"brier_skill_score"`
		} `json:"test_calibration"`
	} `json:"horizons"`
}

func (s *Service) CalibrationDriftReport() (CalibrationDriftReport, error) {
	var builds []api.BaselineModelBuild
	if err := s.db.Where("status = ? AND report_path <> ?", "completed", "").Order("finished_at DESC, id DESC").Limit(2).Find(&builds).Error; err != nil {
		return CalibrationDriftReport{}, err
	}
	report := CalibrationDriftReport{
		Version: CalibrationDriftReportVersion, FrameworkVersion: FrameworkVersion, Mode: "read_only_calibration_drift",
		Method:                  "compares held-out test calibration summaries from the two latest completed baseline model reports",
		MinimumReports:          2,
		ECEDeltaThreshold:       calibrationMaximumECEDelta,
		BrierDeltaThreshold:     calibrationMaximumBrierDelta,
		BrierSkillDropThreshold: calibrationMaximumBSSDrop,
		GeneratedAt:             s.now(),
		RecommendedNextRun: []string{
			"archive the calibration drift SHA256 with offline validation evidence",
			"treat calibration drift as read-only governance; do not adjust thresholds from this report",
			"require fresh baseline calibration evidence before champion/challenger comparisons",
		},
	}
	if len(builds) > 0 {
		latest, err := calibrationSnapshotFromBuild(builds[0])
		if err != nil {
			return CalibrationDriftReport{}, err
		}
		report.Latest = &latest
	}
	if len(builds) < report.MinimumReports {
		report.Status = "exploratory_insufficient_calibration_reports"
		if len(builds) == 0 {
			report.Status = "blocked_no_calibration_reports"
			report.BlockingReasons = append(report.BlockingReasons, "no completed baseline calibration reports are available")
		} else {
			report.BlockingReasons = append(report.BlockingReasons, "only one completed baseline calibration report is available")
		}
		report.ReportSHA256 = calibrationDriftChecksum(report)
		return report, nil
	}
	baseline, err := calibrationSnapshotFromBuild(builds[1])
	if err != nil {
		return CalibrationDriftReport{}, err
	}
	report.Baseline = &baseline
	report.ECEDelta = report.Latest.MeanECE - baseline.MeanECE
	report.ModelBrierDelta = report.Latest.MeanModelBrier - baseline.MeanModelBrier
	report.BrierSkillScoreDelta = report.Latest.MeanBrierSkillScore - baseline.MeanBrierSkillScore
	report.Status = "passed"
	if math.Abs(report.ECEDelta) > report.ECEDeltaThreshold {
		report.Status = "review_required"
		report.BlockingReasons = append(report.BlockingReasons, "ece_delta_exceeds_threshold")
	}
	if report.ModelBrierDelta > report.BrierDeltaThreshold {
		report.Status = "review_required"
		report.BlockingReasons = append(report.BlockingReasons, "model_brier_delta_exceeds_threshold")
	}
	if -report.BrierSkillScoreDelta > report.BrierSkillDropThreshold {
		report.Status = "review_required"
		report.BlockingReasons = append(report.BlockingReasons, "brier_skill_score_drop_exceeds_threshold")
	}
	if report.Latest.CalibrationRequired > 0 || report.Latest.InsufficientLabels > 0 {
		report.Status = "review_required"
		report.BlockingReasons = append(report.BlockingReasons, "latest_calibration_report_has_unpassed_horizons")
	}
	report.BlockingReasons = uniqueSorted(report.BlockingReasons)
	report.ReportSHA256 = calibrationDriftChecksum(report)
	return report, nil
}

func calibrationSnapshotFromBuild(build api.BaselineModelBuild) (CalibrationDriftSnapshot, error) {
	file, err := os.Open(build.ReportPath)
	if err != nil {
		return CalibrationDriftSnapshot{}, err
	}
	defer file.Close()
	var payload calibrationReportFile
	if err := json.NewDecoder(io.LimitReader(file, 32<<20)).Decode(&payload); err != nil {
		return CalibrationDriftSnapshot{}, err
	}
	snapshot := CalibrationDriftSnapshot{
		BuildID: build.ID, BaselineModelKey: build.BaselineModelKey, Version: build.Version,
		Status: build.Status, HorizonCount: len(payload.Horizons), FinishedAt: build.FinishedAt,
	}
	if build.ReportPath != "" {
		if sha, err := localFileSHA256(build.ReportPath); err == nil {
			snapshot.ReportSHA256 = sha
		}
	}
	for _, horizon := range payload.Horizons {
		switch horizon.TestCalibration.Status {
		case "passed":
			snapshot.CalibrationPassed++
		case "calibration_required":
			snapshot.CalibrationRequired++
		case "insufficient_labels":
			snapshot.InsufficientLabels++
		}
		if horizon.ReleaseReadiness == "shadow_candidate" {
			snapshot.ReleaseReadyHorizons++
		}
		snapshot.MeanECE += horizon.TestCalibration.ECE
		snapshot.MeanModelBrier += horizon.TestCalibration.ModelBrier
		snapshot.MeanNullBrier += horizon.TestCalibration.NullBrier
		snapshot.MeanBrierSkillScore += horizon.TestCalibration.BrierSkillScore
	}
	if snapshot.HorizonCount > 0 {
		count := float64(snapshot.HorizonCount)
		snapshot.MeanECE /= count
		snapshot.MeanModelBrier /= count
		snapshot.MeanNullBrier /= count
		snapshot.MeanBrierSkillScore /= count
	}
	return snapshot, nil
}

func localFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func calibrationDriftChecksum(report CalibrationDriftReport) string {
	fingerprint := struct {
		Version                 string                    `json:"version"`
		FrameworkVersion        string                    `json:"framework_version"`
		Mode                    string                    `json:"mode"`
		Status                  string                    `json:"status"`
		Method                  string                    `json:"method"`
		MinimumReports          int                       `json:"minimum_reports"`
		ECEDeltaThreshold       float64                   `json:"ece_delta_threshold"`
		BrierDeltaThreshold     float64                   `json:"brier_delta_threshold"`
		BrierSkillDropThreshold float64                   `json:"brier_skill_drop_threshold"`
		Latest                  *CalibrationDriftSnapshot `json:"latest,omitempty"`
		Baseline                *CalibrationDriftSnapshot `json:"baseline,omitempty"`
		ECEDelta                float64                   `json:"ece_delta"`
		ModelBrierDelta         float64                   `json:"model_brier_delta"`
		BrierSkillScoreDelta    float64                   `json:"brier_skill_score_delta"`
		BlockingReasons         []string                  `json:"blocking_reasons"`
		RecommendedNextRun      []string                  `json:"recommended_next_run"`
	}{
		Version: report.Version, FrameworkVersion: report.FrameworkVersion, Mode: report.Mode, Status: report.Status,
		Method: report.Method, MinimumReports: report.MinimumReports, ECEDeltaThreshold: report.ECEDeltaThreshold,
		BrierDeltaThreshold: report.BrierDeltaThreshold, BrierSkillDropThreshold: report.BrierSkillDropThreshold,
		Latest: report.Latest, Baseline: report.Baseline, ECEDelta: report.ECEDelta,
		ModelBrierDelta: report.ModelBrierDelta, BrierSkillScoreDelta: report.BrierSkillScoreDelta,
		BlockingReasons: report.BlockingReasons, RecommendedNextRun: report.RecommendedNextRun,
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

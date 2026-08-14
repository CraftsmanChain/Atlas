package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"sort"
	"time"

	"atlas/pkg/api"
)

const (
	FeatureDriftReportVersion = "prediction-feature-drift-report-v2"
	featureDriftSampleLimit   = 8
	featureDriftMaximumPSI    = 0.20
	featureDriftMaximumKS     = 0.20
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

type FeatureDistributionSnapshot struct {
	SnapshotID       uint      `json:"snapshot_id"`
	SnapshotKey      string    `json:"snapshot_key"`
	Version          string    `json:"version"`
	Status           string    `json:"status"`
	DistributionRole string    `json:"distribution_role"`
	FeatureName      string    `json:"feature_name"`
	SampleCount      int       `json:"sample_count"`
	MissingRatio     float64   `json:"missing_ratio"`
	Mean             float64   `json:"mean"`
	Stddev           float64   `json:"stddev"`
	Minimum          float64   `json:"minimum"`
	P50              float64   `json:"p50"`
	P95              float64   `json:"p95"`
	Maximum          float64   `json:"maximum"`
	BinCount         int       `json:"bin_count"`
	ReportSHA256     string    `json:"report_sha256,omitempty"`
	ObservedAt       time.Time `json:"observed_at"`
}

type FeatureDriftComparison struct {
	FeatureName        string   `json:"feature_name"`
	Status             string   `json:"status"`
	TrainingSnapshotID uint     `json:"training_snapshot_id,omitempty"`
	LiveSnapshotID     uint     `json:"live_snapshot_id,omitempty"`
	TrainingSamples    int      `json:"training_samples"`
	LiveSamples        int      `json:"live_samples"`
	PSI                float64  `json:"psi"`
	KS                 float64  `json:"ks"`
	MeanDelta          float64  `json:"mean_delta"`
	P50Delta           float64  `json:"p50_delta"`
	P95Delta           float64  `json:"p95_delta"`
	BlockingReasons    []string `json:"blocking_reasons"`
}

type FeatureDriftReport struct {
	Version                    string                        `json:"version"`
	FrameworkVersion           string                        `json:"framework_version"`
	Mode                       string                        `json:"mode"`
	Status                     string                        `json:"status"`
	ReportSHA256               string                        `json:"report_sha256"`
	Method                     string                        `json:"method"`
	SourceBaselineBuildID      uint                          `json:"source_baseline_build_id"`
	BaselineModelKey           string                        `json:"baseline_model_key,omitempty"`
	BaselineVersion            string                        `json:"baseline_version,omitempty"`
	ArtifactPath               string                        `json:"artifact_path,omitempty"`
	ArtifactSHA256             string                        `json:"artifact_sha256,omitempty"`
	ArtifactLocalSHA256        string                        `json:"artifact_local_sha256,omitempty"`
	FeatureContractVersion     string                        `json:"feature_contract_version,omitempty"`
	FeatureColumnCount         int                           `json:"feature_column_count"`
	FeatureDistributionCount   int                           `json:"feature_distribution_count"`
	ComparedFeatureCount       int                           `json:"compared_feature_count"`
	PassedFeatureCount         int                           `json:"passed_feature_count"`
	ReviewRequiredFeatureCount int                           `json:"review_required_feature_count"`
	PSIThreshold               float64                       `json:"psi_threshold"`
	KSThreshold                float64                       `json:"ks_threshold"`
	MaximumPSI                 float64                       `json:"maximum_psi"`
	MaximumKS                  float64                       `json:"maximum_ks"`
	PSIStatus                  string                        `json:"psi_status"`
	KSStatus                   string                        `json:"ks_status"`
	LatestReplay               *FeatureDriftReplaySnapshot   `json:"latest_replay,omitempty"`
	SampleDistributions        []FeatureDistributionSnapshot `json:"sample_distributions"`
	FeatureComparisons         []FeatureDriftComparison      `json:"feature_comparisons"`
	SampleFeatures             []FeatureDriftFeature         `json:"sample_features"`
	BlockingReasons            []string                      `json:"blocking_reasons"`
	RecommendedNextRun         []string                      `json:"recommended_next_run"`
	GeneratedAt                time.Time                     `json:"generated_at"`
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
		Method:       "compares baseline training and live-shadow per-feature aggregate histograms when both are available; otherwise reports the exact missing read-only evidence",
		PSIStatus:    "pending_distribution_store",
		KSStatus:     "pending_distribution_store",
		PSIThreshold: featureDriftMaximumPSI,
		KSThreshold:  featureDriftMaximumKS,
		GeneratedAt:  s.now(),
	}
	var build api.BaselineModelBuild
	if err := s.db.Where("status = ? AND artifact_path <> ?", "completed", "").Order("finished_at DESC, id DESC").Limit(1).Find(&build).Error; err != nil {
		return FeatureDriftReport{}, err
	}
	if build.ID == 0 {
		report.Status = "blocked_no_baseline_artifact"
		report.BlockingReasons = append(report.BlockingReasons, "no completed baseline model artifact is available")
		finalizeFeatureDriftReport(&report)
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
			finalizeFeatureDriftReport(&report)
			return report, nil
		}
	}
	artifact, err := readFeatureDriftArtifact(build.ArtifactPath)
	if err != nil {
		report.Status = "blocked_artifact_unreadable"
		report.BlockingReasons = append(report.BlockingReasons, "baseline artifact file cannot be decoded")
		finalizeFeatureDriftReport(&report)
		return report, nil
	}
	report.FeatureColumnCount = featureDriftColumnCount(artifact)
	report.SampleFeatures = featureDriftSampleFeatures(artifact, featureDriftSampleLimit)
	distributions, err := s.featureDistributionSnapshots(build.ID)
	if err != nil {
		return FeatureDriftReport{}, err
	}
	report.FeatureDistributionCount = len(distributions)
	report.SampleDistributions = featureDriftSampleDistributions(distributions, featureDriftSampleLimit)

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
	report.FeatureComparisons = featureDriftComparisons(featureDriftFeatureNames(artifact), distributions)
	featureDriftApplySampleFeatureStatuses(report.SampleFeatures, report.FeatureComparisons)
	for _, comparison := range report.FeatureComparisons {
		if comparison.Status == "passed" {
			report.PassedFeatureCount++
		}
		if comparison.Status == "review_required" {
			report.ReviewRequiredFeatureCount++
		}
		if comparison.Status == "passed" || comparison.Status == "review_required" {
			report.ComparedFeatureCount++
		}
		report.MaximumPSI = math.Max(report.MaximumPSI, comparison.PSI)
		report.MaximumKS = math.Max(report.MaximumKS, comparison.KS)
	}
	if report.ComparedFeatureCount == 0 {
		report.BlockingReasons = append(report.BlockingReasons, "per-column training/live feature distributions are not persisted")
	} else if report.ComparedFeatureCount < report.FeatureColumnCount {
		report.BlockingReasons = append(report.BlockingReasons, "per-column training/live feature distributions are incomplete")
	}
	if report.ReviewRequiredFeatureCount > 0 {
		report.BlockingReasons = append(report.BlockingReasons, "feature_drift_metric_exceeds_threshold")
	}
	report.BlockingReasons = uniqueSorted(report.BlockingReasons)
	switch {
	case report.ReviewRequiredFeatureCount > 0:
		report.Status = "review_required"
	case report.ComparedFeatureCount > 0 && report.ComparedFeatureCount == report.FeatureColumnCount:
		report.Status = "passed"
		report.PSIStatus = "computed"
		report.KSStatus = "computed"
	case report.ComparedFeatureCount > 0:
		report.Status = "blocked_feature_distributions_incomplete"
		report.PSIStatus = "partial"
		report.KSStatus = "partial"
	default:
		report.Status = "blocked_feature_distributions_unavailable"
	}
	if report.Status == "passed" && len(report.BlockingReasons) > 0 {
		report.Status = "blocked_feature_drift_evidence"
	}
	if report.Status == "blocked_feature_distributions_unavailable" && len(report.BlockingReasons) == 1 && report.BlockingReasons[0] == "per-column training/live feature distributions are not persisted" && report.LatestReplay != nil && report.LatestReplay.Status == "passed" {
		report.Status = "blocked_feature_distribution_store_required"
	}
	if report.Status == "review_required" {
		report.PSIStatus = "computed_review_required"
		report.KSStatus = "computed_review_required"
	}
	finalizeFeatureDriftReport(&report)
	return report, nil
}

func featureDriftApplySampleFeatureStatuses(features []FeatureDriftFeature, comparisons []FeatureDriftComparison) {
	if len(features) == 0 || len(comparisons) == 0 {
		return
	}
	statusByFeature := map[string]string{}
	for _, comparison := range comparisons {
		statusByFeature[comparison.FeatureName] = comparison.Status
	}
	for index := range features {
		if status := statusByFeature[features[index].Name]; status != "" {
			features[index].DistributionStatus = status
		}
	}
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
	return len(featureDriftFeatureNames(artifact))
}

func featureDriftFeatureNames(artifact featureDriftArtifact) []string {
	seen := map[string]struct{}{}
	names := []string{}
	for _, model := range artifact.Models {
		for _, column := range model.FeatureColumns {
			if _, exists := seen[column]; exists {
				continue
			}
			seen[column] = struct{}{}
			names = append(names, column)
		}
	}
	sort.Strings(names)
	return names
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

func (s *Service) featureDistributionSnapshots(baselineBuildID uint) ([]api.PredictionFeatureDistributionSnapshot, error) {
	if baselineBuildID == 0 {
		return nil, nil
	}
	var rows []api.PredictionFeatureDistributionSnapshot
	if err := s.db.Where("source_baseline_build_id = ? AND status IN ?", baselineBuildID, []string{"completed", "passed"}).Order("observed_at DESC, id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	latest := map[string]api.PredictionFeatureDistributionSnapshot{}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		key := row.DistributionRole + "\x00" + row.FeatureName
		if _, exists := latest[key]; exists {
			continue
		}
		latest[key] = row
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]api.PredictionFeatureDistributionSnapshot, 0, len(keys))
	for _, key := range keys {
		result = append(result, latest[key])
	}
	return result, nil
}

func featureDriftSampleDistributions(rows []api.PredictionFeatureDistributionSnapshot, limit int) []FeatureDistributionSnapshot {
	if limit <= 0 {
		return nil
	}
	snapshots := make([]FeatureDistributionSnapshot, 0, minInt(limit, len(rows)))
	for _, row := range rows {
		snapshots = append(snapshots, featureDistributionSnapshot(row))
		if len(snapshots) >= limit {
			return snapshots
		}
	}
	return snapshots
}

func featureDistributionSnapshot(row api.PredictionFeatureDistributionSnapshot) FeatureDistributionSnapshot {
	return FeatureDistributionSnapshot{
		SnapshotID: row.ID, SnapshotKey: row.SnapshotKey, Version: row.Version, Status: row.Status,
		DistributionRole: row.DistributionRole, FeatureName: row.FeatureName, SampleCount: row.SampleCount,
		MissingRatio: row.MissingRatio, Mean: row.Mean, Stddev: row.Stddev, Minimum: row.Minimum,
		P50: row.P50, P95: row.P95, Maximum: row.Maximum, BinCount: len(row.BinProportions),
		ReportSHA256: row.ReportSHA256, ObservedAt: row.ObservedAt,
	}
}

func featureDriftComparisons(featureNames []string, distributions []api.PredictionFeatureDistributionSnapshot) []FeatureDriftComparison {
	byRole := map[string]map[string]api.PredictionFeatureDistributionSnapshot{
		"training":    {},
		"live_shadow": {},
	}
	for _, row := range distributions {
		if _, ok := byRole[row.DistributionRole]; !ok {
			continue
		}
		byRole[row.DistributionRole][row.FeatureName] = row
	}
	comparisons := make([]FeatureDriftComparison, 0, len(featureNames))
	for _, feature := range featureNames {
		training, hasTraining := byRole["training"][feature]
		live, hasLive := byRole["live_shadow"][feature]
		comparison := FeatureDriftComparison{FeatureName: feature, Status: "blocked_missing_distribution"}
		if !hasTraining {
			comparison.BlockingReasons = append(comparison.BlockingReasons, "missing_training_distribution")
		}
		if !hasLive {
			comparison.BlockingReasons = append(comparison.BlockingReasons, "missing_live_shadow_distribution")
		}
		if !hasTraining || !hasLive {
			comparisons = append(comparisons, comparison)
			continue
		}
		comparison.TrainingSnapshotID = training.ID
		comparison.LiveSnapshotID = live.ID
		comparison.TrainingSamples = training.SampleCount
		comparison.LiveSamples = live.SampleCount
		comparison.MeanDelta = math.Abs(live.Mean - training.Mean)
		comparison.P50Delta = math.Abs(live.P50 - training.P50)
		comparison.P95Delta = math.Abs(live.P95 - training.P95)
		comparison.PSI = featurePSI(training.BinProportions, live.BinProportions)
		comparison.KS = featureKS(training.BinProportions, live.BinProportions)
		comparison.Status = "passed"
		if len(training.BinProportions) == 0 || len(live.BinProportions) == 0 || len(training.BinProportions) != len(live.BinProportions) || !featureBinEdgesCompatible(training.BinEdges, live.BinEdges) {
			comparison.Status = "blocked_missing_bins"
			comparison.BlockingReasons = append(comparison.BlockingReasons, "training/live histogram bins are unavailable or incompatible")
		}
		if comparison.Status == "passed" && comparison.PSI > featureDriftMaximumPSI {
			comparison.Status = "review_required"
			comparison.BlockingReasons = append(comparison.BlockingReasons, "psi_exceeds_threshold")
		}
		if comparison.Status == "passed" && comparison.KS > featureDriftMaximumKS {
			comparison.Status = "review_required"
			comparison.BlockingReasons = append(comparison.BlockingReasons, "ks_exceeds_threshold")
		}
		comparison.BlockingReasons = uniqueSorted(comparison.BlockingReasons)
		comparisons = append(comparisons, comparison)
	}
	return comparisons
}

func featureBinEdgesCompatible(training, live api.FloatList) bool {
	if len(training) != len(live) || len(training) < 2 {
		return false
	}
	for index := range training {
		if math.Abs(training[index]-live[index]) > 1e-9 {
			return false
		}
	}
	return true
}

func featurePSI(training, live api.FloatList) float64 {
	if len(training) == 0 || len(training) != len(live) {
		return 0
	}
	result := 0.0
	for index := range training {
		expected := math.Max(training[index], 1e-9)
		actual := math.Max(live[index], 1e-9)
		result += (actual - expected) * math.Log(actual/expected)
	}
	return result
}

func featureKS(training, live api.FloatList) float64 {
	if len(training) == 0 || len(training) != len(live) {
		return 0
	}
	trainingCumulative, liveCumulative, result := 0.0, 0.0, 0.0
	for index := range training {
		trainingCumulative += training[index]
		liveCumulative += live[index]
		result = math.Max(result, math.Abs(liveCumulative-trainingCumulative))
	}
	return result
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func finalizeFeatureDriftReport(report *FeatureDriftReport) {
	report.RecommendedNextRun = featureDriftRecommendedNextRun(*report)
	report.ReportSHA256 = featureDriftChecksum(*report)
}

func featureDriftRecommendedNextRun(report FeatureDriftReport) []string {
	recommendations := []string{}
	add := func(value string) {
		if value == "" {
			return
		}
		for _, existing := range recommendations {
			if existing == value {
				return
			}
		}
		recommendations = append(recommendations, value)
	}
	switch report.Status {
	case "blocked_no_baseline_artifact":
		add("complete a scoped baseline model build before evaluating feature drift")
	case "blocked_artifact_unreadable":
		add("verify the baseline artifact path and SHA256 before evaluating feature drift")
	}
	if report.LatestReplay == nil {
		add("run feature replay for the latest baseline before interpreting feature drift")
	} else if report.LatestReplay.Status != "passed" || (report.LatestReplay.TrainingFeatureCount > 0 && report.LatestReplay.VerifiedColumnCount < report.LatestReplay.TrainingFeatureCount) {
		add("rerun feature replay until all selected training columns pass")
	}
	missingTraining, missingLive, incompatibleBins := 0, 0, 0
	for _, comparison := range report.FeatureComparisons {
		for _, reason := range comparison.BlockingReasons {
			switch reason {
			case "missing_training_distribution":
				missingTraining++
			case "missing_live_shadow_distribution":
				missingLive++
			case "training/live histogram bins are unavailable or incompatible":
				incompatibleBins++
			}
		}
	}
	if missingTraining > 0 {
		add("materialize training feature distributions from the completed baseline-bound training matrix")
	}
	if missingLive > 0 {
		add("run read-only shadow scoring after live coverage passes to materialize live-shadow feature distributions")
	}
	if incompatibleBins > 0 {
		add("regenerate live-shadow distributions with the matching training histogram bins before computing PSI/KS")
	}
	if report.ReviewRequiredFeatureCount > 0 {
		add("review the top per-feature PSI/KS rows before changing any threshold or model")
	}
	if report.Status == "passed" {
		add("archive the feature drift report SHA256 with validation readiness")
	}
	if report.ComparedFeatureCount > 0 && report.ComparedFeatureCount < report.FeatureColumnCount {
		add("complete missing per-feature distribution pairs before treating drift status as comparable")
	}
	add("keep this gate read-only; do not tune thresholds or trigger actions from feature drift evidence alone")
	return recommendations
}

func featureDriftChecksum(report FeatureDriftReport) string {
	fingerprint := struct {
		Version                    string                        `json:"version"`
		FrameworkVersion           string                        `json:"framework_version"`
		Mode                       string                        `json:"mode"`
		Status                     string                        `json:"status"`
		Method                     string                        `json:"method"`
		SourceBaselineBuildID      uint                          `json:"source_baseline_build_id"`
		BaselineModelKey           string                        `json:"baseline_model_key,omitempty"`
		BaselineVersion            string                        `json:"baseline_version,omitempty"`
		ArtifactSHA256             string                        `json:"artifact_sha256,omitempty"`
		ArtifactLocalSHA256        string                        `json:"artifact_local_sha256,omitempty"`
		FeatureContractVersion     string                        `json:"feature_contract_version,omitempty"`
		FeatureColumnCount         int                           `json:"feature_column_count"`
		FeatureDistributionCount   int                           `json:"feature_distribution_count"`
		ComparedFeatureCount       int                           `json:"compared_feature_count"`
		PassedFeatureCount         int                           `json:"passed_feature_count"`
		ReviewRequiredFeatureCount int                           `json:"review_required_feature_count"`
		PSIThreshold               float64                       `json:"psi_threshold"`
		KSThreshold                float64                       `json:"ks_threshold"`
		MaximumPSI                 float64                       `json:"maximum_psi"`
		MaximumKS                  float64                       `json:"maximum_ks"`
		PSIStatus                  string                        `json:"psi_status"`
		KSStatus                   string                        `json:"ks_status"`
		LatestReplay               *FeatureDriftReplaySnapshot   `json:"latest_replay,omitempty"`
		SampleDistributions        []FeatureDistributionSnapshot `json:"sample_distributions"`
		FeatureComparisons         []FeatureDriftComparison      `json:"feature_comparisons"`
		SampleFeatures             []FeatureDriftFeature         `json:"sample_features"`
		BlockingReasons            []string                      `json:"blocking_reasons"`
		RecommendedNextRun         []string                      `json:"recommended_next_run"`
	}{
		Version: report.Version, FrameworkVersion: report.FrameworkVersion, Mode: report.Mode, Status: report.Status,
		Method: report.Method, SourceBaselineBuildID: report.SourceBaselineBuildID,
		BaselineModelKey: report.BaselineModelKey, BaselineVersion: report.BaselineVersion,
		ArtifactSHA256: report.ArtifactSHA256, ArtifactLocalSHA256: report.ArtifactLocalSHA256,
		FeatureContractVersion: report.FeatureContractVersion, FeatureColumnCount: report.FeatureColumnCount,
		FeatureDistributionCount: report.FeatureDistributionCount, ComparedFeatureCount: report.ComparedFeatureCount,
		PassedFeatureCount: report.PassedFeatureCount, ReviewRequiredFeatureCount: report.ReviewRequiredFeatureCount,
		PSIThreshold: report.PSIThreshold, KSThreshold: report.KSThreshold, MaximumPSI: report.MaximumPSI,
		MaximumKS: report.MaximumKS, PSIStatus: report.PSIStatus, KSStatus: report.KSStatus,
		LatestReplay: report.LatestReplay, SampleDistributions: report.SampleDistributions,
		FeatureComparisons: report.FeatureComparisons, SampleFeatures: report.SampleFeatures,
		BlockingReasons: report.BlockingReasons, RecommendedNextRun: report.RecommendedNextRun,
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

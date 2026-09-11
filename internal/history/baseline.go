package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"atlas/internal/featurestats"
	"atlas/pkg/api"
)

const (
	baselineModelVersion             = "gpu-logistic-baseline-v13"
	shallowGBDTModelVersion          = "gpu-shallow-gbdt-challenger-v1"
	anomalyLogisticModelVersion      = "gpu-anomaly-logistic-cascade-v1"
	anomalyAugmentedModelVersion     = "gpu-anomaly-augmented-logistic-v1"
	logisticRegressionAlgorithm      = "logistic_regression"
	shallowGBDTAlgorithm             = "gradient_boosted_stumps"
	anomalyLogisticAlgorithm         = "anomaly_filtered_logistic"
	anomalyAugmentedAlgorithm        = "anomaly_augmented_logistic"
	cohortReadinessGateName          = "fault-model-horizon-readiness-v1"
	baselineFeatureAuditVersion      = "baseline-feature-leakage-audit-v3"
	baselineFeatureSelectionVersion  = "train-only-effect-selection-v1"
	baselineMinimumPrecision         = 0.70
	baselineMinimumRecall            = 0.50
	baselineMinimumClassCoverage     = 0.70
	baselineMinorityRowsPerFeature   = 5
	baselineMaximumSelectedFeatures  = 48
	baselineMaximumFeaturesPerSource = 4
	shallowGBDTMaximumFeatures       = 64
	shallowGBDTMaximumRounds         = 48
	shallowGBDTLearningRate          = 0.08
	shallowGBDTMinimumLeafRows       = 5
	anomalyControlQuantile           = 0.80
	anomalyMinimumScale              = 1e-9
	anomalySyntheticFeature          = "__train_control_robust_anomaly_score_v1"
	allAvailableWindowsPolicy        = "all_available_windows"
	maximum24HourWindowPolicy        = "max_24h"
	maximum3DayWindowPolicy          = "max_3d"
	maximum7DayWindowPolicy          = "max_7d"
)

type BaselineModelBuildRequest struct {
	SourceMatrixBuildID uint   `json:"source_matrix_build_id"`
	EventType           string `json:"event_type,omitempty"`
	ModelName           string `json:"model_name,omitempty"`
	Algorithm           string `json:"algorithm,omitempty"`
	FeatureWindowPolicy string `json:"feature_window_policy,omitempty"`
}

type logisticModel struct {
	HorizonMinutes   int                      `json:"horizon_minutes"`
	FeatureColumns   []string                 `json:"feature_columns"`
	Means            []float64                `json:"means"`
	Scales           []float64                `json:"scales"`
	Coefficients     []float64                `json:"coefficients"`
	Intercept        float64                  `json:"intercept"`
	Threshold        float64                  `json:"threshold"`
	Calibration      probabilityCalibration   `json:"calibration"`
	FeatureSelection baselineFeatureSelection `json:"feature_selection"`
}

type boostedStump struct {
	Feature      string  `json:"feature"`
	Threshold    float64 `json:"threshold"`
	LeftValue    float64 `json:"left_value"`
	RightValue   float64 `json:"right_value"`
	MissingValue float64 `json:"missing_value"`
}

type shallowGBDTModel struct {
	HorizonMinutes   int                      `json:"horizon_minutes"`
	FeatureColumns   []string                 `json:"feature_columns"`
	Intercept        float64                  `json:"intercept"`
	LearningRate     float64                  `json:"learning_rate"`
	Trees            []boostedStump           `json:"trees"`
	Threshold        float64                  `json:"threshold"`
	Calibration      probabilityCalibration   `json:"calibration"`
	FeatureSelection baselineFeatureSelection `json:"feature_selection"`
}

type anomalyFilterModel struct {
	Version         string    `json:"version"`
	FeatureColumns  []string  `json:"feature_columns"`
	Medians         []float64 `json:"medians"`
	Scales          []float64 `json:"scales"`
	Threshold       float64   `json:"threshold"`
	ControlQuantile float64   `json:"control_quantile"`
	TopK            int       `json:"top_k"`
}

type anomalyLogisticModel struct {
	HorizonMinutes   int                      `json:"horizon_minutes"`
	Filter           anomalyFilterModel       `json:"filter"`
	Classifier       logisticModel            `json:"classifier"`
	Threshold        float64                  `json:"threshold"`
	Calibration      probabilityCalibration   `json:"calibration"`
	FeatureSelection baselineFeatureSelection `json:"feature_selection"`
}

type anomalyAugmentedLogisticModel struct {
	HorizonMinutes   int                      `json:"horizon_minutes"`
	Anomaly          anomalyFilterModel       `json:"anomaly"`
	Classifier       logisticModel            `json:"classifier"`
	Threshold        float64                  `json:"threshold"`
	Calibration      probabilityCalibration   `json:"calibration"`
	FeatureSelection baselineFeatureSelection `json:"feature_selection"`
}

type anomalyFilterSplitReport struct {
	Count             int     `json:"count"`
	Positive          int     `json:"positive"`
	Control           int     `json:"control"`
	Retained          int     `json:"retained"`
	RetainedPositive  int     `json:"retained_positive"`
	RetainedControl   int     `json:"retained_control"`
	PositiveRetention float64 `json:"positive_retention"`
	ControlRetention  float64 `json:"control_retention"`
}

type anomalyFilterReport struct {
	Version         string                   `json:"version"`
	Threshold       float64                  `json:"threshold"`
	ControlQuantile float64                  `json:"control_quantile"`
	TopK            int                      `json:"top_k"`
	FeatureCount    int                      `json:"feature_count"`
	Train           anomalyFilterSplitReport `json:"train"`
	Validation      anomalyFilterSplitReport `json:"validation"`
	Test            anomalyFilterSplitReport `json:"test"`
}

type anomalyScoreClassReport struct {
	Count int     `json:"count"`
	Mean  float64 `json:"mean"`
	P50   float64 `json:"p50"`
	P90   float64 `json:"p90"`
}

type anomalyScoreSplitReport struct {
	Positive anomalyScoreClassReport `json:"positive"`
	Control  anomalyScoreClassReport `json:"control"`
}

type anomalyAugmentationReport struct {
	Version          string                  `json:"version"`
	SyntheticFeature string                  `json:"synthetic_feature"`
	FeatureCount     int                     `json:"feature_count"`
	TopK             int                     `json:"top_k"`
	Train            anomalyScoreSplitReport `json:"train"`
	Validation       anomalyScoreSplitReport `json:"validation"`
	Test             anomalyScoreSplitReport `json:"test"`
}

type baselineSelectedFeature struct {
	Feature          string  `json:"feature"`
	SourceMetric     string  `json:"source_metric"`
	Score            float64 `json:"score"`
	PositiveCoverage float64 `json:"positive_coverage"`
	ControlCoverage  float64 `json:"control_coverage"`
}

type baselineFeatureSelection struct {
	Version               string                    `json:"version"`
	Status                string                    `json:"status"`
	CandidateFeatureCount int                       `json:"candidate_feature_count"`
	EligibleFeatureCount  int                       `json:"eligible_feature_count"`
	SelectedFeatureCount  int                       `json:"selected_feature_count"`
	SelectionLimit        int                       `json:"selection_limit"`
	Selected              []baselineSelectedFeature `json:"selected"`
}

type probabilityCalibration struct {
	Version       string  `json:"version"`
	Status        string  `json:"status"`
	Algorithm     string  `json:"algorithm"`
	Slope         float64 `json:"slope"`
	Intercept     float64 `json:"intercept"`
	FittedCount   int     `json:"fitted_count"`
	PositiveCount int     `json:"positive_count"`
	ControlCount  int     `json:"control_count"`
}
type baselineMetrics struct {
	Count     int     `json:"count"`
	Positive  int     `json:"positive"`
	Control   int     `json:"control"`
	ROCAUC    float64 `json:"roc_auc"`
	PRAUC     float64 `json:"pr_auc"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	Brier     float64 `json:"brier"`
}
type baselineHorizonReport struct {
	HorizonMinutes          int                        `json:"horizon_minutes"`
	FeatureSelection        baselineFeatureSelection   `json:"feature_selection"`
	Train                   baselineMetrics            `json:"train"`
	Validation              baselineMetrics            `json:"validation"`
	ValidationUncertainty   baselineUncertainty        `json:"validation_uncertainty"`
	Test                    baselineMetrics            `json:"test"`
	TestUncertainty         baselineUncertainty        `json:"test_uncertainty"`
	CrossSplitStatus        string                     `json:"cross_split_status"`
	RawTestCalibration      baselineCalibration        `json:"raw_test_calibration"`
	TestCalibration         baselineCalibration        `json:"test_calibration"`
	ReleaseReadiness        string                     `json:"release_readiness"`
	TestByModel             map[string]baselineMetrics `json:"test_by_model"`
	TestByEventType         map[string]baselineMetrics `json:"test_by_event_type"`
	TestByDriverVersion     map[string]baselineMetrics `json:"test_by_driver_version"`
	TestByLabelSource       map[string]baselineMetrics `json:"test_by_label_source"`
	TestByHardwareCertainty map[string]baselineMetrics `json:"test_by_hardware_certainty"`
	TestByRuleVersion       map[string]baselineMetrics `json:"test_by_rule_version"`
	Threshold               float64                    `json:"threshold"`
	AnomalyFilter           *anomalyFilterReport       `json:"anomaly_filter,omitempty"`
	AnomalyAugmentation     *anomalyAugmentationReport `json:"anomaly_augmentation,omitempty"`
}

type baselineCalibrationBin struct {
	Index           int     `json:"index"`
	LowerBound      float64 `json:"lower_bound"`
	UpperBound      float64 `json:"upper_bound"`
	Count           int     `json:"count"`
	MeanProbability float64 `json:"mean_probability"`
	ObservedRate    float64 `json:"observed_rate"`
}

type baselineCalibration struct {
	Version         string                   `json:"version"`
	Status          string                   `json:"status"`
	BinCount        int                      `json:"bin_count"`
	ECE             float64                  `json:"ece"`
	ModelBrier      float64                  `json:"model_brier"`
	NullBrier       float64                  `json:"null_brier"`
	BrierSkillScore float64                  `json:"brier_skill_score"`
	Bins            []baselineCalibrationBin `json:"bins"`
}

type baselineUncertainty struct {
	Version         string  `json:"version"`
	Method          string  `json:"method"`
	Resamples       int     `json:"resamples"`
	ConfidenceLevel float64 `json:"confidence_level"`
	Seed            int64   `json:"seed"`
	EntityCount     int     `json:"entity_count"`
	ROCAUCLower     float64 `json:"roc_auc_lower"`
	ROCAUCUpper     float64 `json:"roc_auc_upper"`
	PRAUCLower      float64 `json:"pr_auc_lower"`
	PRAUCUpper      float64 `json:"pr_auc_upper"`
	NullPRAUC       float64 `json:"null_pr_auc"`
	Status          string  `json:"status"`
}
type baselineArtifact struct {
	Version             string                          `json:"version"`
	Algorithm           string                          `json:"algorithm"`
	FeatureWindowPolicy string                          `json:"feature_window_policy"`
	MatrixKey           string                          `json:"matrix_key"`
	ScopeEventType      string                          `json:"scope_event_type,omitempty"`
	ScopeModelName      string                          `json:"scope_model_name,omitempty"`
	ReadinessGate       string                          `json:"readiness_gate,omitempty"`
	PredictionTarget    string                          `json:"prediction_target"`
	FeaturePolicy       string                          `json:"feature_policy"`
	FeatureAudit        baselineFeatureAudit            `json:"feature_audit"`
	Models              []logisticModel                 `json:"models"`
	BoostedModels       []shallowGBDTModel              `json:"boosted_models,omitempty"`
	CascadeModels       []anomalyLogisticModel          `json:"cascade_models,omitempty"`
	AugmentedModels     []anomalyAugmentedLogisticModel `json:"augmented_models,omitempty"`
	CreatedAt           time.Time                       `json:"created_at"`
}
type baselineReport struct {
	Version                 string                     `json:"version"`
	Algorithm               string                     `json:"algorithm"`
	FeatureWindowPolicy     string                     `json:"feature_window_policy"`
	MatrixKey               string                     `json:"matrix_key"`
	ScopeEventType          string                     `json:"scope_event_type,omitempty"`
	ScopeModelName          string                     `json:"scope_model_name,omitempty"`
	ReadinessGate           string                     `json:"readiness_gate,omitempty"`
	PredictionTarget        string                     `json:"prediction_target"`
	Mode                    string                     `json:"mode"`
	FeaturePolicy           string                     `json:"feature_policy"`
	FeatureAudit            baselineFeatureAudit       `json:"feature_audit"`
	CalibrationPolicy       string                     `json:"calibration_policy"`
	OperatingPointPolicy    string                     `json:"operating_point_policy"`
	FeatureSelectionPolicy  string                     `json:"feature_selection_policy"`
	Horizons                []baselineHorizonReport    `json:"horizons"`
	MacroTest               baselineMetrics            `json:"macro_test"`
	ByTestModel             map[string]baselineMetrics `json:"by_test_model"`
	ByTestEventType         map[string]baselineMetrics `json:"by_test_event_type"`
	ByTestDriverVersion     map[string]baselineMetrics `json:"by_test_driver_version"`
	ByTestLabelSource       map[string]baselineMetrics `json:"by_test_label_source"`
	ByTestHardwareCertainty map[string]baselineMetrics `json:"by_test_hardware_certainty"`
	ByTestRuleVersion       map[string]baselineMetrics `json:"by_test_rule_version"`
	CreatedAt               time.Time                  `json:"created_at"`
}

type baselineFeatureExclusion struct {
	Feature string `json:"feature"`
	Reason  string `json:"reason"`
}

type baselineFeatureAudit struct {
	Version                 string                     `json:"version"`
	PredictionTarget        string                     `json:"prediction_target"`
	Status                  string                     `json:"status"`
	SourceFeatureCount      int                        `json:"source_feature_count"`
	SelectedFeatureCount    int                        `json:"selected_feature_count"`
	ExcludedFeatureCount    int                        `json:"excluded_feature_count"`
	ProhibitedSelectedCount int                        `json:"prohibited_selected_count"`
	SelectedColumns         []string                   `json:"selected_columns"`
	Exclusions              []baselineFeatureExclusion `json:"exclusions"`
}

func (s *Service) BaselineModelBuilds(limit int) ([]api.BaselineModelBuild, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.BaselineModelBuild
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) BaselineModelReport(id uint) (baselineReport, error) {
	var build api.BaselineModelBuild
	if err := s.db.First(&build, id).Error; err != nil {
		return baselineReport{}, err
	}
	if build.Status != "completed" || build.ReportPath == "" {
		return baselineReport{}, fmt.Errorf("completed baseline report is required")
	}
	base, err := filepath.Abs(s.config.DatasetDir)
	if err != nil {
		return baselineReport{}, err
	}
	path, err := filepath.Abs(build.ReportPath)
	if err != nil {
		return baselineReport{}, err
	}
	relative, err := filepath.Rel(base, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return baselineReport{}, fmt.Errorf("baseline report path is outside the configured dataset directory")
	}
	file, err := os.Open(path)
	if err != nil {
		return baselineReport{}, err
	}
	defer file.Close()
	var report baselineReport
	if err := json.NewDecoder(io.LimitReader(file, 32<<20)).Decode(&report); err != nil {
		return baselineReport{}, fmt.Errorf("decode baseline report: %w", err)
	}
	return report, nil
}

func (s *Service) StartBaselineModelBuild(request BaselineModelBuildRequest) (api.BaselineModelBuild, error) {
	s.baselineMu.Lock()
	defer s.baselineMu.Unlock()
	if s.baselineRunning {
		return api.BaselineModelBuild{}, fmt.Errorf("baseline training is already running")
	}
	request.EventType = strings.TrimSpace(request.EventType)
	request.ModelName = strings.TrimSpace(request.ModelName)
	algorithm, version, err := resolveBaselineAlgorithm(request.Algorithm)
	if err != nil {
		return api.BaselineModelBuild{}, err
	}
	request.Algorithm = algorithm
	request.FeatureWindowPolicy, err = resolveFeatureWindowPolicy(request.FeatureWindowPolicy)
	if err != nil {
		return api.BaselineModelBuild{}, err
	}
	if (request.EventType == "") != (request.ModelName == "") {
		return api.BaselineModelBuild{}, fmt.Errorf("event_type and model_name must be provided together")
	}
	var matrix api.TrainingMatrixBuild
	query := s.db.Where("status = ? AND version = ?", "completed", trainingMatrixVersion)
	if request.SourceMatrixBuildID > 0 {
		query = query.Where("id = ?", request.SourceMatrixBuildID)
	}
	result := query.Order("finished_at DESC, id DESC").Limit(1).Find(&matrix)
	if result.Error != nil {
		return api.BaselineModelBuild{}, result.Error
	}
	if result.RowsAffected == 0 {
		return api.BaselineModelBuild{}, fmt.Errorf("a completed supervised training matrix is required")
	}
	started := s.now()
	key := version + "-" + strconv.FormatInt(started.UTC().UnixNano(), 10)
	readinessGate := ""
	if request.EventType != "" {
		readinessGate = cohortReadinessGateName
	}
	build := api.BaselineModelBuild{BaselineModelKey: key, Version: version, Status: "queued", Algorithm: request.Algorithm, FeatureWindowPolicy: request.FeatureWindowPolicy,
		SourceMatrixBuildID: matrix.ID, SourceTrainingMatrixKey: matrix.TrainingMatrixKey, FeatureContractVersion: matrix.FeatureContractVersion,
		ScopeEventType: request.EventType, ScopeModelName: request.ModelName, ReadinessGateVersion: readinessGate,
		OutputDir: filepath.Join(s.config.DatasetDir, "baseline-models", key), StartedAt: started}
	if err := s.db.Create(&build).Error; err != nil {
		return build, err
	}
	s.baselineRunning = true
	go s.executeBaselineModelBuild(build.ID)
	return build, nil
}

func resolveFeatureWindowPolicy(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", allAvailableWindowsPolicy:
		return allAvailableWindowsPolicy, nil
	case maximum24HourWindowPolicy, maximum3DayWindowPolicy, maximum7DayWindowPolicy:
		return strings.TrimSpace(value), nil
	default:
		return "", fmt.Errorf("unsupported feature window policy %q", strings.TrimSpace(value))
	}
}

func maximumFeatureWindow(policy string) time.Duration {
	switch policy {
	case maximum24HourWindowPolicy:
		return 24 * time.Hour
	case maximum3DayWindowPolicy:
		return 3 * 24 * time.Hour
	case maximum7DayWindowPolicy:
		return 7 * 24 * time.Hour
	default:
		return 0
	}
}

func resolveBaselineAlgorithm(value string) (string, string, error) {
	switch strings.TrimSpace(value) {
	case "", logisticRegressionAlgorithm:
		return logisticRegressionAlgorithm, baselineModelVersion, nil
	case shallowGBDTAlgorithm:
		return shallowGBDTAlgorithm, shallowGBDTModelVersion, nil
	case anomalyLogisticAlgorithm:
		return anomalyLogisticAlgorithm, anomalyLogisticModelVersion, nil
	case anomalyAugmentedAlgorithm:
		return anomalyAugmentedAlgorithm, anomalyAugmentedModelVersion, nil
	default:
		return "", "", fmt.Errorf("unsupported baseline algorithm %q", strings.TrimSpace(value))
	}
}
func (s *Service) executeBaselineModelBuild(id uint) {
	defer func() { s.baselineMu.Lock(); s.baselineRunning = false; s.baselineMu.Unlock() }()
	var build api.BaselineModelBuild
	if s.db.First(&build, id).Error != nil {
		return
	}
	if s.db.Model(&build).Update("status", "running").Error != nil {
		return
	}
	if err := s.buildBaselineModels(&build); err != nil {
		finished := s.now()
		_ = s.db.Model(&build).Updates(map[string]any{"status": "failed", "error_message": err.Error(), "finished_at": &finished}).Error
	}
}

func (s *Service) buildBaselineModels(build *api.BaselineModelBuild) error {
	var matrix api.TrainingMatrixBuild
	if err := s.db.First(&matrix, build.SourceMatrixBuildID).Error; err != nil {
		return err
	}
	if err := verifyFileSHA256(matrix.MatrixPath, matrix.MatrixSHA256); err != nil {
		return fmt.Errorf("matrix checksum: %w", err)
	}
	rows, err := readJSONLines[trainingMatrixRow](matrix.MatrixPath)
	if err != nil {
		return err
	}
	if build.ScopeEventType != "" {
		rows, err = scopedReadyMatrixRows(matrix.TrainingMatrixKey, rows, build.ScopeEventType, build.ScopeModelName)
		if err != nil {
			return err
		}
	}
	predictionTarget, err := matrixPredictionTarget(rows)
	if err != nil {
		return err
	}
	windowPolicy, err := resolveFeatureWindowPolicy(build.FeatureWindowPolicy)
	if err != nil {
		return err
	}
	build.FeatureWindowPolicy = windowPolicy
	featureAudit := auditBaselineFeaturesForTargetAndWindowPolicy(rows, predictionTarget, windowPolicy)
	if featureAudit.Status != "passed" || featureAudit.ProhibitedSelectedCount != 0 {
		return fmt.Errorf("baseline feature leakage audit failed: %d prohibited columns selected", featureAudit.ProhibitedSelectedCount)
	}
	columns := featureAudit.SelectedColumns
	if len(columns) == 0 {
		return fmt.Errorf("no pre-failure-safe feature columns")
	}
	build.FeatureColumnCount = 0
	build.FeatureAuditStatus = featureAudit.Status
	build.ExcludedFeatureCount = featureAudit.ExcludedFeatureCount
	build.ProhibitedFeatureCount = featureAudit.ProhibitedSelectedCount
	byHorizon := map[int][]trainingMatrixRow{}
	for _, row := range rows {
		byHorizon[row.HorizonMinutes] = append(byHorizon[row.HorizonMinutes], row)
	}
	horizons := make([]int, 0, len(byHorizon))
	for h := range byHorizon {
		horizons = append(horizons, h)
	}
	sort.Ints(horizons)
	mode := "offline_evaluation_only"
	featureSelectionPolicy := "training-only standardized class separation; >=70% coverage in each class; <=1 feature per 5 minority-class rows, <=48 total and <=4 per source metric; validation and test labels never select features"
	if build.Algorithm == shallowGBDTAlgorithm {
		mode = "offline_challenger_evaluation_only"
		featureSelectionPolicy = "training-only coverage and variance filter; up to 64 safe features; 48 deterministic logistic-loss boosted stumps learn nonlinear thresholds; validation/test labels never select features; runtime registration is disabled"
	}
	if build.Algorithm == anomalyLogisticAlgorithm {
		mode = "offline_challenger_evaluation_only"
		featureSelectionPolicy = "stage 1 fits robust median/MAD anomaly statistics and a healthy-control quantile gate on training rows only; stage 2 selects and fits Logistic only on gate-retained training rows; validation/test labels never fit the gate, features, calibration, or operating threshold; runtime registration is disabled"
	}
	if build.Algorithm == anomalyAugmentedAlgorithm {
		mode = "offline_challenger_evaluation_only"
		featureSelectionPolicy = "a broad safe feature set fits a continuous robust anomaly score from training controls only; training-only effect selection supplies Logistic features and the anomaly score is appended without filtering rows; validation/test labels never fit anomaly statistics, features, calibration, or operating threshold; runtime registration is disabled"
	}
	artifact := baselineArtifact{Version: build.Version, Algorithm: build.Algorithm, FeatureWindowPolicy: windowPolicy, MatrixKey: matrix.TrainingMatrixKey, ScopeEventType: build.ScopeEventType, ScopeModelName: build.ScopeModelName, ReadinessGate: build.ReadinessGateVersion, PredictionTarget: predictionTarget, FeaturePolicy: baselineFeaturePolicy(predictionTarget), FeatureAudit: featureAudit, CreatedAt: s.now()}
	report := baselineReport{Version: build.Version, Algorithm: build.Algorithm, FeatureWindowPolicy: windowPolicy, MatrixKey: matrix.TrainingMatrixKey, ScopeEventType: build.ScopeEventType, ScopeModelName: build.ScopeModelName, ReadinessGate: build.ReadinessGateVersion, PredictionTarget: predictionTarget, Mode: mode, FeaturePolicy: baselineFeaturePolicy(predictionTarget), FeatureAudit: featureAudit, CalibrationPolicy: "validation-only Platt scaling fits slope/intercept; held-out test labels are audit-only; no online probability release", OperatingPointPolicy: "validation-only threshold prioritizes precision >= 0.70 and recall >= 0.50; held-out test must independently pass both gates", FeatureSelectionPolicy: featureSelectionPolicy, ByTestModel: map[string]baselineMetrics{}, ByTestEventType: map[string]baselineMetrics{}, ByTestDriverVersion: map[string]baselineMetrics{}, ByTestLabelSource: map[string]baselineMetrics{}, ByTestHardwareCertainty: map[string]baselineMetrics{}, ByTestRuleVersion: map[string]baselineMetrics{}, CreatedAt: s.now()}
	for _, h := range horizons {
		train, val, test := splitMatrixRows(byHorizon[h])
		if !hasBothLabels(train) || !hasBothLabels(val) || !hasBothLabels(test) {
			return fmt.Errorf("horizon %d requires both labels in every split", h)
		}
		selection := selectBaselineFeatures(train, columns)
		if build.Algorithm == shallowGBDTAlgorithm || build.Algorithm == anomalyLogisticAlgorithm {
			selection = selectShallowGBDTFeatures(train, columns)
		}
		if selection.Status != "passed" || selection.SelectedFeatureCount == 0 {
			return fmt.Errorf("horizon %d has no train-only selected features", h)
		}
		selectedColumns := selectedFeatureNames(selection)
		if len(selectedColumns) > build.FeatureColumnCount {
			build.FeatureColumnCount = len(selectedColumns)
		}
		var rawValidationScores, validationScores, rawTestScores, testScores []scoredLabel
		var anomalyReport *anomalyFilterReport
		var anomalyAugmentation *anomalyAugmentationReport
		threshold := 0.5
		if build.Algorithm == shallowGBDTAlgorithm {
			model := fitShallowGBDT(train, selectedColumns, h)
			model.FeatureSelection = selection
			rawValidationScores = scoreShallowGBDTRowsWithoutCalibration(model, val)
			model.Calibration = fitPlattCalibration(rawValidationScores)
			validationScores = applyCalibration(rawValidationScores, model.Calibration)
			model.Threshold = operationalThreshold(validationScores)
			threshold = model.Threshold
			rawTestScores = scoreShallowGBDTRowsWithoutCalibration(model, test)
			testScores = applyCalibration(rawTestScores, model.Calibration)
			artifact.BoostedModels = append(artifact.BoostedModels, model)
		} else if build.Algorithm == anomalyLogisticAlgorithm {
			model, _, err := fitAnomalyLogisticCascade(train, selectedColumns, h)
			if err != nil {
				return fmt.Errorf("horizon %d anomaly cascade: %w", h, err)
			}
			rawValidationScores = scoreAnomalyLogisticRowsWithoutCalibration(model, val)
			model.Calibration = fitPlattCalibration(rawValidationScores)
			validationScores = applyCalibration(rawValidationScores, model.Calibration)
			model.Threshold = operationalThreshold(validationScores)
			threshold = model.Threshold
			rawTestScores = scoreAnomalyLogisticRowsWithoutCalibration(model, test)
			testScores = applyCalibration(rawTestScores, model.Calibration)
			artifact.CascadeModels = append(artifact.CascadeModels, model)
			anomalyReport = &anomalyFilterReport{Version: model.Filter.Version, Threshold: model.Filter.Threshold, ControlQuantile: model.Filter.ControlQuantile, TopK: model.Filter.TopK, FeatureCount: len(model.Filter.FeatureColumns), Train: describeAnomalyFilterSplit(train, model.Filter), Validation: describeAnomalyFilterSplit(val, model.Filter), Test: describeAnomalyFilterSplit(test, model.Filter)}
		} else if build.Algorithm == anomalyAugmentedAlgorithm {
			anomalySelection := selectShallowGBDTFeatures(train, columns)
			if anomalySelection.Status != "passed" || anomalySelection.SelectedFeatureCount == 0 {
				return fmt.Errorf("horizon %d has no training-only anomaly features", h)
			}
			anomalyColumns := selectedFeatureNames(anomalySelection)
			if len(anomalyColumns) > build.FeatureColumnCount {
				build.FeatureColumnCount = len(anomalyColumns)
			}
			model, err := fitAnomalyAugmentedLogistic(train, anomalyColumns, selectedColumns, h)
			if err != nil {
				return fmt.Errorf("horizon %d anomaly augmentation: %w", h, err)
			}
			model.FeatureSelection = selection
			model.Classifier.FeatureSelection = selection
			rawValidationScores = scoreAnomalyAugmentedRowsWithoutCalibration(model, val)
			model.Calibration = fitPlattCalibration(rawValidationScores)
			validationScores = applyCalibration(rawValidationScores, model.Calibration)
			model.Threshold = operationalThreshold(validationScores)
			threshold = model.Threshold
			rawTestScores = scoreAnomalyAugmentedRowsWithoutCalibration(model, test)
			testScores = applyCalibration(rawTestScores, model.Calibration)
			artifact.AugmentedModels = append(artifact.AugmentedModels, model)
			anomalyAugmentation = &anomalyAugmentationReport{Version: model.Anomaly.Version, SyntheticFeature: anomalySyntheticFeature, FeatureCount: len(model.Anomaly.FeatureColumns), TopK: model.Anomaly.TopK, Train: describeAnomalyScoreSplit(train, model.Anomaly), Validation: describeAnomalyScoreSplit(val, model.Anomaly), Test: describeAnomalyScoreSplit(test, model.Anomaly)}
		} else {
			model := fitLogistic(train, selectedColumns, h)
			model.FeatureSelection = selection
			rawValidationScores = scoreRowsWithoutCalibration(model, val)
			model.Calibration = fitPlattCalibration(rawValidationScores)
			validationScores = applyCalibration(rawValidationScores, model.Calibration)
			model.Threshold = operationalThreshold(validationScores)
			threshold = model.Threshold
			rawTestScores = scoreRowsWithoutCalibration(model, test)
			testScores = applyCalibration(rawTestScores, model.Calibration)
			artifact.Models = append(artifact.Models, model)
		}
		for i := range testScores {
			testScores[i].threshold = threshold
		}
		validationUncertainty := bootstrapBaselineUncertainty(val, validationScores, h+1_000_000)
		testUncertainty := bootstrapBaselineUncertainty(test, testScores, h)
		crossSplitStatus := crossSplitStability(validationUncertainty, testUncertainty)
		rawTestCalibration := evaluateBaselineCalibration(rawTestScores)
		testCalibration := evaluateBaselineCalibration(testScores)
		testMetrics := evaluateScores(testScores, threshold)
		releaseReadiness := baselineReleaseReadiness(crossSplitStatus, testCalibration.Status, testMetrics)
		if build.Algorithm != logisticRegressionAlgorithm && releaseReadiness == "shadow_candidate" {
			releaseReadiness = "offline_challenger_candidate_runtime_required"
		}
		report.Horizons = append(report.Horizons, baselineHorizonReport{HorizonMinutes: h, FeatureSelection: selection, Train: describeLabels(train), Validation: evaluateScores(validationScores, threshold), ValidationUncertainty: validationUncertainty, Test: testMetrics, TestUncertainty: testUncertainty, CrossSplitStatus: crossSplitStatus, RawTestCalibration: rawTestCalibration, TestCalibration: testCalibration, ReleaseReadiness: releaseReadiness, TestByModel: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return []string{row.ModelName} }), TestByEventType: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return row.LabelMetadata.EventTypes }), TestByDriverVersion: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return row.LabelMetadata.DriverVersions }), TestByLabelSource: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return row.LabelMetadata.LabelSources }), TestByHardwareCertainty: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return row.LabelMetadata.HardwareCertainties }), TestByRuleVersion: stratifiedTestMetrics(test, testScores, func(row trainingMatrixRow) []string { return row.LabelMetadata.RuleDecisionVersions }), Threshold: threshold, AnomalyFilter: anomalyReport, AnomalyAugmentation: anomalyAugmentation})
		if crossSplitStatus == "robust_candidate" {
			build.StatisticallyStableCount++
		}
		if releaseReadiness == "shadow_candidate" {
			build.ShadowCandidateCount++
		}
		build.TrainCount += len(train)
		build.ValidationCount += len(val)
		build.TestCount += len(test)
	}
	report.ByTestModel = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByModel })
	report.ByTestEventType = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByEventType })
	report.ByTestDriverVersion = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByDriverVersion })
	report.ByTestLabelSource = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByLabelSource })
	report.ByTestHardwareCertainty = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByHardwareCertainty })
	report.ByTestRuleVersion = macroStratifiedMetrics(report.Horizons, func(row baselineHorizonReport) map[string]baselineMetrics { return row.TestByRuleVersion })
	report.MacroTest = macroTestMetrics(report.Horizons)
	build.HorizonCount = len(horizons)
	build.TrainedModelCount = len(artifact.Models) + len(artifact.BoostedModels) + len(artifact.CascadeModels) + len(artifact.AugmentedModels)
	build.TestMacroROCAUC = report.MacroTest.ROCAUC
	build.TestMacroPRAUC = report.MacroTest.PRAUC
	build.TestMacroPrecision = report.MacroTest.Precision
	build.TestMacroRecall = report.MacroTest.Recall
	if err := os.MkdirAll(build.OutputDir, 0o750); err != nil {
		return err
	}
	artifactPath := filepath.Join(build.OutputDir, "models.json")
	if err := writeJSONAtomic(artifactPath, artifact); err != nil {
		return err
	}
	checksum, err := fileSHA256(artifactPath)
	if err != nil {
		return err
	}
	reportPath := filepath.Join(build.OutputDir, "evaluation_report.json")
	if err := writeJSONAtomic(reportPath, report); err != nil {
		return err
	}
	finished := s.now()
	return s.db.Model(build).Updates(map[string]any{"status": "completed", "feature_column_count": build.FeatureColumnCount, "feature_audit_status": build.FeatureAuditStatus, "excluded_feature_count": build.ExcludedFeatureCount, "prohibited_feature_count": build.ProhibitedFeatureCount, "statistically_stable_count": build.StatisticallyStableCount, "shadow_candidate_count": build.ShadowCandidateCount, "horizon_count": build.HorizonCount, "trained_model_count": build.TrainedModelCount, "train_count": build.TrainCount, "validation_count": build.ValidationCount, "test_count": build.TestCount, "test_macro_roc_auc": build.TestMacroROCAUC, "test_macro_pr_auc": build.TestMacroPRAUC, "test_macro_precision": build.TestMacroPrecision, "test_macro_recall": build.TestMacroRecall, "artifact_path": artifactPath, "artifact_sha256": checksum, "report_path": reportPath, "finished_at": &finished}).Error
}

func baselineReleaseReadiness(crossSplitStatus, calibrationStatus string, test baselineMetrics) string {
	if crossSplitStatus != "robust_candidate" {
		return "blocked_stability"
	}
	if calibrationStatus != "passed" {
		return "blocked_calibration"
	}
	if test.Precision < baselineMinimumPrecision || test.Recall < baselineMinimumRecall {
		return "blocked_operating_point"
	}
	return "shadow_candidate"
}

func evaluateBaselineCalibration(scores []scoredLabel) baselineCalibration {
	const binCount = 10
	result := baselineCalibration{Version: "held-out-calibration-audit-v1", Status: "insufficient_labels", BinCount: binCount, Bins: make([]baselineCalibrationBin, binCount)}
	if len(scores) == 0 {
		return result
	}
	positiveCount := 0
	probabilitySums, positiveSums := make([]float64, binCount), make([]int, binCount)
	for _, score := range scores {
		probability := math.Max(0, math.Min(1, score.score))
		index := int(probability * binCount)
		if index >= binCount {
			index = binCount - 1
		}
		result.Bins[index].Count++
		probabilitySums[index] += probability
		if score.label == 1 {
			positiveCount++
			positiveSums[index]++
		}
		result.ModelBrier += (probability - float64(score.label)) * (probability - float64(score.label))
	}
	if positiveCount == 0 || positiveCount == len(scores) {
		return result
	}
	result.ModelBrier /= float64(len(scores))
	prevalence := float64(positiveCount) / float64(len(scores))
	result.NullBrier = prevalence * (1 - prevalence)
	if result.NullBrier > 0 {
		result.BrierSkillScore = 1 - result.ModelBrier/result.NullBrier
	}
	for index := range result.Bins {
		bin := &result.Bins[index]
		bin.Index, bin.LowerBound, bin.UpperBound = index, float64(index)/binCount, float64(index+1)/binCount
		if bin.Count == 0 {
			continue
		}
		bin.MeanProbability = probabilitySums[index] / float64(bin.Count)
		bin.ObservedRate = float64(positiveSums[index]) / float64(bin.Count)
		result.ECE += float64(bin.Count) / float64(len(scores)) * math.Abs(bin.MeanProbability-bin.ObservedRate)
	}
	if result.ECE <= 0.10 && result.BrierSkillScore > 0 {
		result.Status = "passed"
	} else {
		result.Status = "calibration_required"
	}
	return result
}

func fitPlattCalibration(scores []scoredLabel) probabilityCalibration {
	calibration := probabilityCalibration{Version: "validation-platt-v1", Status: "rejected", Algorithm: "platt_scaling", Slope: 1, FittedCount: len(scores)}
	for _, score := range scores {
		if score.label == 1 {
			calibration.PositiveCount++
		} else {
			calibration.ControlCount++
		}
	}
	if calibration.PositiveCount == 0 || calibration.ControlCount == 0 {
		return calibration
	}
	for epoch := 0; epoch < 1000; epoch++ {
		gradientSlope, gradientIntercept := 0.0, 0.0
		for _, score := range scores {
			x := logitProbability(score.score)
			probability := sigmoid(calibration.Slope*x + calibration.Intercept)
			errorValue := probability - float64(score.label)
			gradientSlope += errorValue * x
			gradientIntercept += errorValue
		}
		rate := 0.05 / math.Sqrt(1+float64(epoch)/100)
		count := float64(len(scores))
		calibration.Slope -= rate * (gradientSlope/count + 0.01*(calibration.Slope-1))
		calibration.Intercept -= rate * gradientIntercept / count
	}
	if calibration.Slope <= 0 || math.IsNaN(calibration.Slope) || math.IsInf(calibration.Slope, 0) || math.IsNaN(calibration.Intercept) || math.IsInf(calibration.Intercept, 0) {
		return calibration
	}
	calibration.Status = "fitted"
	return calibration
}

func logitProbability(probability float64) float64 {
	probability = math.Max(1e-6, math.Min(1-1e-6, probability))
	return math.Log(probability / (1 - probability))
}

func applyCalibration(scores []scoredLabel, calibration probabilityCalibration) []scoredLabel {
	result := append([]scoredLabel(nil), scores...)
	if calibration.Status != "fitted" {
		return result
	}
	for index := range result {
		result[index].score = sigmoid(calibration.Slope*logitProbability(result[index].score) + calibration.Intercept)
	}
	return result
}

func crossSplitStability(validation, test baselineUncertainty) string {
	if validation.Status == "candidate_signal" && test.Status == "candidate_signal" {
		return "robust_candidate"
	}
	if validation.Status == "inverse_signal" && test.Status == "inverse_signal" {
		return "consistent_inverse"
	}
	if (validation.Status == "candidate_signal" && test.Status == "inverse_signal") || (validation.Status == "inverse_signal" && test.Status == "candidate_signal") {
		return "temporal_instability"
	}
	return "inconclusive"
}

func bootstrapBaselineUncertainty(rows []trainingMatrixRow, scores []scoredLabel, horizonMinutes int) baselineUncertainty {
	const resamples = 1000
	seed := int64(202608030000 + horizonMinutes)
	result := baselineUncertainty{Version: "gpu-cluster-bootstrap-v1", Method: "GPU-UUID cluster bootstrap of held-out test scores", ConfidenceLevel: 0.95, Seed: seed, Status: "insufficient_labels"}
	if len(rows) != len(scores) {
		return result
	}
	clusters := map[string][]scoredLabel{}
	positiveCount := 0
	for index, score := range scores {
		gpu := normalizeHistoricalGPUUUID(rows[index].GPUUUID)
		if gpu == "" {
			gpu = "ROW:" + rows[index].RowKey
		}
		clusters[gpu] = append(clusters[gpu], score)
		if score.label == 1 {
			positiveCount++
		}
	}
	if positiveCount == 0 || positiveCount == len(scores) || len(clusters) < 2 {
		return result
	}
	keys := make([]string, 0, len(clusters))
	for key := range clusters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result.EntityCount = len(keys)
	result.NullPRAUC = float64(positiveCount) / float64(len(scores))
	rng := rand.New(rand.NewSource(seed))
	rocValues, prValues := make([]float64, 0, resamples), make([]float64, 0, resamples)
	resampled := make([]scoredLabel, 0, len(scores))
	for attempt := 0; len(rocValues) < resamples && attempt < resamples*20; attempt++ {
		resampled = resampled[:0]
		for range keys {
			resampled = append(resampled, clusters[keys[rng.Intn(len(keys))]]...)
		}
		metrics := evaluateScores(resampled, 0.5)
		if metrics.Positive == 0 || metrics.Control == 0 {
			continue
		}
		rocValues = append(rocValues, metrics.ROCAUC)
		prValues = append(prValues, metrics.PRAUC)
	}
	result.Resamples = len(rocValues)
	if result.Resamples < resamples {
		return result
	}
	result.ROCAUCLower, result.ROCAUCUpper = percentile(rocValues, 0.025), percentile(rocValues, 0.975)
	result.PRAUCLower, result.PRAUCUpper = percentile(prValues, 0.025), percentile(prValues, 0.975)
	switch {
	case result.ROCAUCLower > 0.5 && result.PRAUCLower > result.NullPRAUC:
		result.Status = "candidate_signal"
	case result.ROCAUCUpper < 0.5:
		result.Status = "inverse_signal"
	default:
		result.Status = "inconclusive"
	}
	return result
}

func percentile(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(math.Round(quantile * float64(len(sorted)-1)))
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func stratifiedTestMetrics(rows []trainingMatrixRow, scores []scoredLabel, labels func(trainingMatrixRow) []string) map[string]baselineMetrics {
	grouped := map[string][]scoredLabel{}
	for index, row := range rows {
		appendStratifiedScore(grouped, labels(row), scores[index])
	}
	return evaluateStratifiedScores(grouped)
}

func appendStratifiedScore(grouped map[string][]scoredLabel, labels []string, score scoredLabel) {
	if len(labels) == 0 {
		grouped["unknown"] = append(grouped["unknown"], score)
		return
	}
	seen := map[string]bool{}
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		grouped[label] = append(grouped[label], score)
	}
	if len(seen) == 0 {
		grouped["unknown"] = append(grouped["unknown"], score)
	}
}

func evaluateStratifiedScores(grouped map[string][]scoredLabel) map[string]baselineMetrics {
	result := make(map[string]baselineMetrics, len(grouped))
	for label, scores := range grouped {
		result[label] = evaluateScores(scores, -1)
	}
	return result
}

func macroStratifiedMetrics(horizons []baselineHorizonReport, selectMetrics func(baselineHorizonReport) map[string]baselineMetrics) map[string]baselineMetrics {
	totals := map[string]baselineMetrics{}
	counts := map[string]int{}
	for _, horizon := range horizons {
		for label, metrics := range selectMetrics(horizon) {
			total := totals[label]
			total.Count += metrics.Count
			total.Positive += metrics.Positive
			total.Control += metrics.Control
			total.ROCAUC += metrics.ROCAUC
			total.PRAUC += metrics.PRAUC
			total.Precision += metrics.Precision
			total.Recall += metrics.Recall
			total.F1 += metrics.F1
			total.Brier += metrics.Brier
			totals[label] = total
			counts[label]++
		}
	}
	for label, total := range totals {
		count := float64(counts[label])
		total.ROCAUC /= count
		total.PRAUC /= count
		total.Precision /= count
		total.Recall /= count
		total.F1 /= count
		total.Brier /= count
		totals[label] = total
	}
	return totals
}

func baselineFeaturePolicy(predictionTarget string) string {
	if predictionTarget == highPriorityXIDEventTarget {
		return "for future high-priority XID prediction, allow point-in-time-safe correctable row-remap history as a precursor; exclude sampling counts plus current XID, uncorrectable ECC/row-remap, remap failure and reset indicators"
	}
	return "for hardware-failure prediction, exclude sampling counts plus XID, ECC, row-remap and reset indicators that represent an occurred fault; use only pre-failure operational trends"
}
func safeBaselineColumns(rows []trainingMatrixRow) []string {
	return auditBaselineFeaturesForTarget(rows, hardwareFailureTarget).SelectedColumns
}

func selectBaselineFeatures(rows []trainingMatrixRow, columns []string) baselineFeatureSelection {
	type classMoments struct {
		positiveCount, controlCount int
		positiveSum, controlSum     float64
		sum, sumSquares             float64
	}
	result := baselineFeatureSelection{
		Version: baselineFeatureSelectionVersion, Status: "insufficient_features",
		CandidateFeatureCount: len(columns), Selected: []baselineSelectedFeature{},
	}
	positiveTotal, controlTotal := 0, 0
	for _, row := range rows {
		if row.LabelValue == 1 {
			positiveTotal++
		} else {
			controlTotal++
		}
	}
	minorityCount := positiveTotal
	if controlTotal < minorityCount {
		minorityCount = controlTotal
	}
	result.SelectionLimit = minorityCount / baselineMinorityRowsPerFeature
	if result.SelectionLimit > baselineMaximumSelectedFeatures {
		result.SelectionLimit = baselineMaximumSelectedFeatures
	}
	if result.SelectionLimit < 1 || positiveTotal == 0 || controlTotal == 0 {
		return result
	}

	ranked := make([]baselineSelectedFeature, 0, len(columns))
	for _, column := range columns {
		moments := classMoments{}
		for _, row := range rows {
			value, exists := row.Features[column]
			if !exists || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			moments.sum += value
			moments.sumSquares += value * value
			if row.LabelValue == 1 {
				moments.positiveCount++
				moments.positiveSum += value
			} else {
				moments.controlCount++
				moments.controlSum += value
			}
		}
		positiveCoverage := float64(moments.positiveCount) / float64(positiveTotal)
		controlCoverage := float64(moments.controlCount) / float64(controlTotal)
		if positiveCoverage < baselineMinimumClassCoverage || controlCoverage < baselineMinimumClassCoverage {
			continue
		}
		observed := moments.positiveCount + moments.controlCount
		mean := moments.sum / float64(observed)
		variance := moments.sumSquares/float64(observed) - mean*mean
		if variance <= 1e-18 {
			continue
		}
		positiveMean := moments.positiveSum / float64(moments.positiveCount)
		controlMean := moments.controlSum / float64(moments.controlCount)
		source, _, _, ok := featurestats.ParseTrailingRangeColumn(column)
		if !ok {
			source = column
		}
		ranked = append(ranked, baselineSelectedFeature{
			Feature: column, SourceMetric: source,
			Score:            math.Abs(positiveMean-controlMean) / math.Sqrt(variance),
			PositiveCoverage: positiveCoverage, ControlCoverage: controlCoverage,
		})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return ranked[i].Feature < ranked[j].Feature
		}
		return ranked[i].Score > ranked[j].Score
	})
	result.EligibleFeatureCount = len(ranked)
	bySource := map[string]int{}
	for _, candidate := range ranked {
		if len(result.Selected) >= result.SelectionLimit {
			break
		}
		if bySource[candidate.SourceMetric] >= baselineMaximumFeaturesPerSource {
			continue
		}
		result.Selected = append(result.Selected, candidate)
		bySource[candidate.SourceMetric]++
	}
	result.SelectedFeatureCount = len(result.Selected)
	if result.SelectedFeatureCount > 0 {
		result.Status = "passed"
	}
	return result
}

func selectedFeatureNames(selection baselineFeatureSelection) []string {
	columns := make([]string, 0, len(selection.Selected))
	for _, selected := range selection.Selected {
		columns = append(columns, selected.Feature)
	}
	return columns
}

func selectShallowGBDTFeatures(rows []trainingMatrixRow, columns []string) baselineFeatureSelection {
	type moments struct {
		positive, control int
		sum, sumSquares   float64
	}
	result := baselineFeatureSelection{Version: "train-only-diversity-selection-v1", Status: "insufficient_features", CandidateFeatureCount: len(columns), Selected: []baselineSelectedFeature{}}
	positiveTotal, controlTotal := 0, 0
	for _, row := range rows {
		if row.LabelValue == 1 {
			positiveTotal++
		} else {
			controlTotal++
		}
	}
	minority := positiveTotal
	if controlTotal < minority {
		minority = controlTotal
	}
	result.SelectionLimit = minority / 2
	if result.SelectionLimit > shallowGBDTMaximumFeatures {
		result.SelectionLimit = shallowGBDTMaximumFeatures
	}
	if result.SelectionLimit < 1 || positiveTotal == 0 || controlTotal == 0 {
		return result
	}

	bySource := map[string][]baselineSelectedFeature{}
	for _, column := range columns {
		m := moments{}
		for _, row := range rows {
			value, ok := row.Features[column]
			if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			m.sum += value
			m.sumSquares += value * value
			if row.LabelValue == 1 {
				m.positive++
			} else {
				m.control++
			}
		}
		positiveCoverage := float64(m.positive) / float64(positiveTotal)
		controlCoverage := float64(m.control) / float64(controlTotal)
		if positiveCoverage < baselineMinimumClassCoverage || controlCoverage < baselineMinimumClassCoverage {
			continue
		}
		observed := m.positive + m.control
		mean := m.sum / float64(observed)
		variance := m.sumSquares/float64(observed) - mean*mean
		if variance <= 1e-18 {
			continue
		}
		source, _, _, ok := featurestats.ParseTrailingRangeColumn(column)
		if !ok {
			source = column
		}
		bySource[source] = append(bySource[source], baselineSelectedFeature{Feature: column, SourceMetric: source, Score: variance, PositiveCoverage: positiveCoverage, ControlCoverage: controlCoverage})
	}
	for source := range bySource {
		sort.Slice(bySource[source], func(i, j int) bool { return bySource[source][i].Feature < bySource[source][j].Feature })
		result.EligibleFeatureCount += len(bySource[source])
	}
	sources := make([]string, 0, len(bySource))
	for source := range bySource {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	for depth := 0; len(result.Selected) < result.SelectionLimit; depth++ {
		added := false
		for _, source := range sources {
			if depth >= len(bySource[source]) || depth >= 8 || len(result.Selected) >= result.SelectionLimit {
				continue
			}
			result.Selected = append(result.Selected, bySource[source][depth])
			added = true
		}
		if !added {
			break
		}
	}
	result.SelectedFeatureCount = len(result.Selected)
	if result.SelectedFeatureCount > 0 {
		result.Status = "passed"
	}
	return result
}

func auditBaselineFeatures(rows []trainingMatrixRow) baselineFeatureAudit {
	return auditBaselineFeaturesForTarget(rows, hardwareFailureTarget)
}

func auditBaselineFeaturesForTarget(rows []trainingMatrixRow, predictionTarget string) baselineFeatureAudit {
	return auditBaselineFeaturesForTargetAndWindowPolicy(rows, predictionTarget, allAvailableWindowsPolicy)
}

func auditBaselineFeaturesForTargetAndWindowPolicy(rows []trainingMatrixRow, predictionTarget, windowPolicy string) baselineFeatureAudit {
	set := map[string]bool{}
	for _, r := range rows {
		for c := range r.Features {
			set[c] = true
		}
	}
	all := make([]string, 0, len(set))
	for c := range set {
		all = append(all, c)
	}
	sort.Strings(all)
	audit := baselineFeatureAudit{Version: baselineFeatureAuditVersion, PredictionTarget: predictionTarget, Status: "passed", SourceFeatureCount: len(all), SelectedColumns: []string{}, Exclusions: []baselineFeatureExclusion{}}
	for _, column := range all {
		if reason := prohibitedBaselineFeatureReasonForTarget(column, predictionTarget); reason != "" {
			audit.Exclusions = append(audit.Exclusions, baselineFeatureExclusion{Feature: column, Reason: reason})
			continue
		}
		if maximum := maximumFeatureWindow(windowPolicy); maximum > 0 {
			if _, _, duration, ok := featurestats.ParseTrailingRangeColumn(column); ok && duration > maximum {
				audit.Exclusions = append(audit.Exclusions, baselineFeatureExclusion{Feature: column, Reason: "temporal_ablation_above_" + windowPolicy})
				continue
			}
		}
		audit.SelectedColumns = append(audit.SelectedColumns, column)
	}
	for _, column := range audit.SelectedColumns {
		if prohibitedBaselineFeatureReasonForTarget(column, predictionTarget) != "" {
			audit.ProhibitedSelectedCount++
		}
	}
	audit.SelectedFeatureCount = len(audit.SelectedColumns)
	audit.ExcludedFeatureCount = len(audit.Exclusions)
	if audit.ProhibitedSelectedCount > 0 {
		audit.Status = "failed"
	}
	return audit
}

func prohibitedBaselineFeatureReason(column string) string {
	return prohibitedBaselineFeatureReasonForTarget(column, hardwareFailureTarget)
}

func prohibitedBaselineFeatureReasonForTarget(column, predictionTarget string) string {
	if strings.Contains(column, "_sample_count_") {
		return "quality_only_sampling_count"
	}
	if strings.HasPrefix(column, "correctable_remapped_rows_") {
		if predictionTarget == highPriorityXIDEventTarget {
			return ""
		}
		return "occurred_fault_row_remap"
	}
	prefixes := []struct {
		prefix string
		reason string
	}{
		{"xid_", "occurred_fault_xid"},
		{"uncorrectable_remapped_rows_", "occurred_fault_row_remap"},
		{"row_remap_failure_", "occurred_fault_row_remap"},
		{"uncorrected_ecc_", "occurred_fault_ecc"},
		{"gpu_reset_required_", "occurred_fault_reset"},
	}
	for _, item := range prefixes {
		if strings.HasPrefix(column, item.prefix) {
			return item.reason
		}
	}
	return ""
}

func scopedReadyMatrixRows(matrixKey string, rows []trainingMatrixRow, eventType, modelName string) ([]trainingMatrixRow, error) {
	readiness := evaluateCohortReadiness(matrixKey, rows)
	readyHorizons := map[int]bool{}
	for _, stratum := range readiness.Strata {
		if stratum.EventType == eventType && stratum.ModelName == modelName && stratum.Status == "exploratory_ready" {
			readyHorizons[stratum.HorizonMinutes] = true
		}
	}
	if len(readyHorizons) == 0 {
		return nil, fmt.Errorf("no exploratory-ready horizon for event_type=%q model_name=%q", eventType, modelName)
	}
	result := make([]trainingMatrixRow, 0, len(rows))
	for _, row := range rows {
		if row.ModelName == modelName && readyHorizons[row.HorizonMinutes] && containsString(row.LabelMetadata.EventTypes, eventType) {
			result = append(result, row)
		}
	}
	return result, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func splitMatrixRows(rows []trainingMatrixRow) (train, val, test []trainingMatrixRow) {
	for _, r := range rows {
		switch r.Split {
		case "train":
			train = append(train, r)
		case "validation":
			val = append(val, r)
		case "test":
			test = append(test, r)
		}
	}
	return
}
func hasBothLabels(rows []trainingMatrixRow) bool {
	a, b := false, false
	for _, r := range rows {
		if r.LabelValue == 1 {
			a = true
		} else {
			b = true
		}
	}
	return a && b
}
func describeLabels(rows []trainingMatrixRow) baselineMetrics {
	m := baselineMetrics{Count: len(rows)}
	for _, r := range rows {
		if r.LabelValue == 1 {
			m.Positive++
		} else {
			m.Control++
		}
	}
	return m
}

func fitLogistic(rows []trainingMatrixRow, cols []string, h int) logisticModel {
	n := len(cols)
	mean := make([]float64, n)
	scale := make([]float64, n)
	observed := make([]int, n)
	for _, r := range rows {
		for j, c := range cols {
			if value, exists := r.Features[c]; exists {
				mean[j] += value
				observed[j]++
			}
		}
	}
	for j := range mean {
		if observed[j] > 0 {
			mean[j] /= float64(observed[j])
		}
	}
	for _, r := range rows {
		for j, c := range cols {
			if value, exists := r.Features[c]; exists {
				d := value - mean[j]
				scale[j] += d * d
			}
		}
	}
	for j := range scale {
		if observed[j] > 0 {
			scale[j] = math.Sqrt(scale[j] / float64(observed[j]))
		}
		if scale[j] < 1e-9 {
			scale[j] = 1
		}
	}
	coef := make([]float64, n)
	intercept := 0.0
	for epoch := 0; epoch < 500; epoch++ {
		gradient := make([]float64, n)
		gb := 0.0
		weightSum := 0.0
		for _, r := range rows {
			z := intercept
			for j, c := range cols {
				z += coef[j] * standardizedValue(r.Features, c, mean[j], scale[j])
			}
			p := sigmoid(z)
			w := r.TrainingWeight
			if w <= 0 {
				w = 1
			}
			e := (p - float64(r.LabelValue)) * w
			gb += e
			weightSum += w
			for j, c := range cols {
				gradient[j] += e * standardizedValue(r.Features, c, mean[j], scale[j])
			}
		}
		rate := 0.08 / math.Sqrt(1+float64(epoch)/50)
		intercept -= rate * gb / weightSum
		for j := range coef {
			coef[j] -= rate * (gradient[j]/weightSum + 0.01*coef[j])
		}
	}
	return logisticModel{HorizonMinutes: h, FeatureColumns: cols, Means: mean, Scales: scale, Coefficients: coef, Intercept: intercept, Threshold: 0.5}
}

func fitAnomalyLogisticCascade(rows []trainingMatrixRow, columns []string, horizonMinutes int) (anomalyLogisticModel, []trainingMatrixRow, error) {
	filter, err := fitAnomalyFilter(rows, columns)
	if err != nil {
		return anomalyLogisticModel{}, nil, err
	}
	filtered := filterRowsByAnomaly(rows, filter)
	if !hasBothLabels(filtered) {
		return anomalyLogisticModel{}, nil, fmt.Errorf("anomaly gate retained no usable two-class training cohort")
	}
	selection := selectBaselineFeatures(filtered, columns)
	if selection.Status != "passed" || selection.SelectedFeatureCount == 0 {
		return anomalyLogisticModel{}, nil, fmt.Errorf("gate-retained cohort has no train-only classifier features")
	}
	classifier := fitLogistic(filtered, selectedFeatureNames(selection), horizonMinutes)
	classifier.FeatureSelection = selection
	return anomalyLogisticModel{
		HorizonMinutes:   horizonMinutes,
		Filter:           filter,
		Classifier:       classifier,
		Threshold:        0.5,
		FeatureSelection: selection,
	}, filtered, nil
}

func fitAnomalyFilter(rows []trainingMatrixRow, columns []string) (anomalyFilterModel, error) {
	model, err := fitRobustAnomalyStatistics(rows, columns, "train-control-robust-anomaly-filter-v1")
	if err != nil {
		return anomalyFilterModel{}, err
	}
	controlScores := make([]float64, 0, len(rows))
	for _, row := range rows {
		if row.LabelValue == 0 {
			controlScores = append(controlScores, anomalyScore(model, row))
		}
	}
	if len(controlScores) == 0 {
		return anomalyFilterModel{}, fmt.Errorf("training controls are required")
	}
	// Prefer a strict healthy-control filter, but relax it deterministically when
	// the gate would remove every positive training example. The gate statistics
	// and every candidate threshold still use training controls only.
	for _, quantile := range []float64{anomalyControlQuantile, 0.70, 0.60, 0.50, 0} {
		model.ControlQuantile = quantile
		model.Threshold = percentile(controlScores, quantile)
		if hasBothLabels(filterRowsByAnomaly(rows, model)) {
			return model, nil
		}
	}
	return anomalyFilterModel{}, fmt.Errorf("no training-only control quantile retained both labels")
}

func fitRobustAnomalyStatistics(rows []trainingMatrixRow, columns []string, version string) (anomalyFilterModel, error) {
	model := anomalyFilterModel{Version: version, FeatureColumns: append([]string(nil), columns...), TopK: 3}
	model.Medians = make([]float64, len(columns))
	model.Scales = make([]float64, len(columns))
	for columnIndex, column := range columns {
		values := make([]float64, 0, len(rows))
		for _, row := range rows {
			value, ok := row.Features[column]
			if row.LabelValue != 0 || !ok || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			values = append(values, value)
		}
		if len(values) == 0 {
			return anomalyFilterModel{}, fmt.Errorf("feature %q has no observed training controls", column)
		}
		model.Medians[columnIndex] = percentile(values, 0.5)
		deviations := make([]float64, len(values))
		for index, value := range values {
			deviations[index] = math.Abs(value - model.Medians[columnIndex])
		}
		model.Scales[columnIndex] = 1.4826 * percentile(deviations, 0.5)
		if model.Scales[columnIndex] < anomalyMinimumScale {
			model.Scales[columnIndex] = robustFallbackScale(values)
		}
	}
	return model, nil
}

func robustFallbackScale(values []float64) float64 {
	if len(values) < 2 {
		return 1
	}
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	variance := 0.0
	for _, value := range values {
		delta := value - mean
		variance += delta * delta
	}
	scale := math.Sqrt(variance / float64(len(values)))
	if scale < anomalyMinimumScale {
		return 1
	}
	return scale
}

func anomalyScore(model anomalyFilterModel, row trainingMatrixRow) float64 {
	values := make([]float64, 0, len(model.FeatureColumns))
	for index, column := range model.FeatureColumns {
		value, ok := row.Features[column]
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		scale := model.Scales[index]
		if scale < anomalyMinimumScale {
			scale = 1
		}
		values = append(values, math.Min(12, math.Abs(value-model.Medians[index])/scale))
	}
	if len(values) == 0 {
		return 0
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(values)))
	topK := model.TopK
	if topK <= 0 || topK > len(values) {
		topK = len(values)
	}
	sum := 0.0
	for _, value := range values[:topK] {
		sum += value
	}
	return sum / float64(topK)
}

func softAnomalyScore(model anomalyFilterModel, row trainingMatrixRow) float64 {
	values := make([]float64, 0, len(model.FeatureColumns))
	for index, column := range model.FeatureColumns {
		value, ok := row.Features[column]
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		scale := model.Scales[index]
		if scale < anomalyMinimumScale {
			scale = 1
		}
		values = append(values, math.Log1p(math.Abs(value-model.Medians[index])/scale))
	}
	if len(values) == 0 {
		return 0
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(values)))
	topK := model.TopK
	if topK <= 0 || topK > len(values) {
		topK = len(values)
	}
	sum := 0.0
	for _, value := range values[:topK] {
		sum += value
	}
	return sum / float64(topK)
}

func fitAnomalyAugmentedLogistic(rows []trainingMatrixRow, anomalyColumns, classifierColumns []string, horizonMinutes int) (anomalyAugmentedLogisticModel, error) {
	anomaly, err := fitRobustAnomalyStatistics(rows, anomalyColumns, "train-control-robust-anomaly-score-v1")
	if err != nil {
		return anomalyAugmentedLogisticModel{}, err
	}
	augmentedRows := augmentRowsWithAnomalyScore(rows, anomaly)
	columns := append(append([]string(nil), classifierColumns...), anomalySyntheticFeature)
	classifier := fitLogistic(augmentedRows, columns, horizonMinutes)
	return anomalyAugmentedLogisticModel{HorizonMinutes: horizonMinutes, Anomaly: anomaly, Classifier: classifier, Threshold: 0.5}, nil
}

func augmentRowsWithAnomalyScore(rows []trainingMatrixRow, anomaly anomalyFilterModel) []trainingMatrixRow {
	result := make([]trainingMatrixRow, len(rows))
	for index, row := range rows {
		result[index] = row
		result[index].Features = make(map[string]float64, len(row.Features)+1)
		for key, value := range row.Features {
			result[index].Features[key] = value
		}
		result[index].Features[anomalySyntheticFeature] = softAnomalyScore(anomaly, row)
	}
	return result
}

func scoreAnomalyAugmentedRowsWithoutCalibration(model anomalyAugmentedLogisticModel, rows []trainingMatrixRow) []scoredLabel {
	return scoreRowsWithoutCalibration(model.Classifier, augmentRowsWithAnomalyScore(rows, model.Anomaly))
}

func describeAnomalyScoreSplit(rows []trainingMatrixRow, anomaly anomalyFilterModel) anomalyScoreSplitReport {
	positive, control := make([]float64, 0), make([]float64, 0)
	for _, row := range rows {
		score := softAnomalyScore(anomaly, row)
		if row.LabelValue == 1 {
			positive = append(positive, score)
		} else {
			control = append(control, score)
		}
	}
	return anomalyScoreSplitReport{Positive: describeAnomalyScoreClass(positive), Control: describeAnomalyScoreClass(control)}
}

func describeAnomalyScoreClass(values []float64) anomalyScoreClassReport {
	report := anomalyScoreClassReport{Count: len(values)}
	for _, value := range values {
		report.Mean += value
	}
	if len(values) > 0 {
		report.Mean /= float64(len(values))
		report.P50 = percentile(values, 0.5)
		report.P90 = percentile(values, 0.9)
	}
	return report
}

func filterRowsByAnomaly(rows []trainingMatrixRow, model anomalyFilterModel) []trainingMatrixRow {
	filtered := make([]trainingMatrixRow, 0, len(rows))
	for _, row := range rows {
		if anomalyScore(model, row) >= model.Threshold {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func scoreAnomalyLogisticRowsWithoutCalibration(model anomalyLogisticModel, rows []trainingMatrixRow) []scoredLabel {
	result := make([]scoredLabel, len(rows))
	for index, row := range rows {
		if anomalyScore(model.Filter, row) < model.Filter.Threshold {
			result[index] = scoredLabel{score: 1e-6, label: row.LabelValue}
			continue
		}
		result[index] = scoreRowsWithoutCalibration(model.Classifier, []trainingMatrixRow{row})[0]
	}
	return result
}

func describeAnomalyFilterSplit(rows []trainingMatrixRow, model anomalyFilterModel) anomalyFilterSplitReport {
	report := anomalyFilterSplitReport{Count: len(rows)}
	for _, row := range rows {
		retained := anomalyScore(model, row) >= model.Threshold
		if row.LabelValue == 1 {
			report.Positive++
			if retained {
				report.RetainedPositive++
			}
		} else {
			report.Control++
			if retained {
				report.RetainedControl++
			}
		}
		if retained {
			report.Retained++
		}
	}
	if report.Positive > 0 {
		report.PositiveRetention = float64(report.RetainedPositive) / float64(report.Positive)
	}
	if report.Control > 0 {
		report.ControlRetention = float64(report.RetainedControl) / float64(report.Control)
	}
	return report
}

func fitShallowGBDT(rows []trainingMatrixRow, columns []string, horizonMinutes int) shallowGBDTModel {
	positiveWeight, totalWeight := 0.0, 0.0
	for _, row := range rows {
		weight := row.TrainingWeight
		if weight <= 0 {
			weight = 1
		}
		totalWeight += weight
		if row.LabelValue == 1 {
			positiveWeight += weight
		}
	}
	prevalence := positiveWeight / totalWeight
	if prevalence < 1e-6 {
		prevalence = 1e-6
	} else if prevalence > 1-1e-6 {
		prevalence = 1 - 1e-6
	}
	model := shallowGBDTModel{HorizonMinutes: horizonMinutes, FeatureColumns: append([]string(nil), columns...), Intercept: logitProbability(prevalence), LearningRate: shallowGBDTLearningRate, Threshold: 0.5, Trees: []boostedStump{}}
	logits := make([]float64, len(rows))
	for index := range logits {
		logits[index] = model.Intercept
	}
	for round := 0; round < shallowGBDTMaximumRounds; round++ {
		residuals := make([]float64, len(rows))
		for index, row := range rows {
			residuals[index] = float64(row.LabelValue) - sigmoid(logits[index])
		}
		stump, gain, ok := bestBoostedStump(rows, residuals, columns)
		if !ok || gain <= 1e-12 {
			break
		}
		model.Trees = append(model.Trees, stump)
		for index, row := range rows {
			logits[index] += model.LearningRate * boostedStumpValue(stump, row.Features)
		}
	}
	return model
}

func bestBoostedStump(rows []trainingMatrixRow, residuals []float64, columns []string) (boostedStump, float64, bool) {
	type observation struct {
		value, residual, weight float64
	}
	best, bestGain, found := boostedStump{}, 0.0, false
	for _, column := range columns {
		observed := make([]observation, 0, len(rows))
		missingSum, missingWeight := 0.0, 0.0
		for index, row := range rows {
			weight := row.TrainingWeight
			if weight <= 0 {
				weight = 1
			}
			value, ok := row.Features[column]
			if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
				missingSum += residuals[index] * weight
				missingWeight += weight
				continue
			}
			observed = append(observed, observation{value: value, residual: residuals[index], weight: weight})
		}
		if len(observed) < shallowGBDTMinimumLeafRows*2 {
			continue
		}
		sort.Slice(observed, func(i, j int) bool { return observed[i].value < observed[j].value })
		totalSum, totalWeight := 0.0, 0.0
		for _, item := range observed {
			totalSum += item.residual * item.weight
			totalWeight += item.weight
		}
		leftSum, leftWeight := 0.0, 0.0
		for index := 0; index < len(observed)-1; index++ {
			leftSum += observed[index].residual * observed[index].weight
			leftWeight += observed[index].weight
			leftRows, rightRows := index+1, len(observed)-index-1
			if leftRows < shallowGBDTMinimumLeafRows || rightRows < shallowGBDTMinimumLeafRows || observed[index].value == observed[index+1].value {
				continue
			}
			rightSum, rightWeight := totalSum-leftSum, totalWeight-leftWeight
			gain := leftSum*leftSum/(leftWeight+1) + rightSum*rightSum/(rightWeight+1)
			if missingWeight > 0 {
				gain += missingSum * missingSum / (missingWeight + 1)
			}
			if found && gain <= bestGain {
				continue
			}
			bestGain, found = gain, true
			best = boostedStump{Feature: column, Threshold: (observed[index].value + observed[index+1].value) / 2, LeftValue: leftSum / (leftWeight + 1), RightValue: rightSum / (rightWeight + 1)}
			if missingWeight > 0 {
				best.MissingValue = missingSum / (missingWeight + 1)
			}
		}
	}
	return best, bestGain, found
}

func boostedStumpValue(stump boostedStump, features map[string]float64) float64 {
	value, ok := features[stump.Feature]
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return stump.MissingValue
	}
	if value <= stump.Threshold {
		return stump.LeftValue
	}
	return stump.RightValue
}

func scoreShallowGBDTRowsWithoutCalibration(model shallowGBDTModel, rows []trainingMatrixRow) []scoredLabel {
	result := make([]scoredLabel, len(rows))
	for index, row := range rows {
		logit := model.Intercept
		for _, tree := range model.Trees {
			logit += model.LearningRate * boostedStumpValue(tree, row.Features)
		}
		result[index] = scoredLabel{score: sigmoid(logit), label: row.LabelValue}
	}
	return result
}

func sigmoid(x float64) float64 {
	if x >= 0 {
		return 1 / (1 + math.Exp(-x))
	}
	e := math.Exp(x)
	return e / (1 + e)
}

type scoredLabel struct {
	score     float64
	label     int
	threshold float64
}

func scoreRows(m logisticModel, rows []trainingMatrixRow) []scoredLabel {
	return applyCalibration(scoreRowsWithoutCalibration(m, rows), m.Calibration)
}

func scoreRowsWithoutCalibration(m logisticModel, rows []trainingMatrixRow) []scoredLabel {
	out := make([]scoredLabel, len(rows))
	for i, r := range rows {
		z := m.Intercept
		for j, c := range m.FeatureColumns {
			z += m.Coefficients[j] * standardizedValue(r.Features, c, m.Means[j], m.Scales[j])
		}
		out[i] = scoredLabel{score: sigmoid(z), label: r.LabelValue}
	}
	return out
}
func bestF1Threshold(scores []scoredLabel) float64 {
	bestT, best := 0.5, -1.0
	for i := 5; i <= 95; i++ {
		t := float64(i) / 100
		m := evaluateScores(scores, t)
		if m.F1 > best {
			best, bestT = m.F1, t
		}
	}
	return bestT
}

func operationalThreshold(scores []scoredLabel) float64 {
	bestThreshold, bestRecall, bestPrecision := 0.0, -1.0, -1.0
	for i := 5; i <= 95; i++ {
		threshold := float64(i) / 100
		metrics := evaluateScores(scores, threshold)
		if metrics.Precision < baselineMinimumPrecision || metrics.Recall < baselineMinimumRecall {
			continue
		}
		if metrics.Recall > bestRecall || (metrics.Recall == bestRecall && metrics.Precision > bestPrecision) {
			bestThreshold, bestRecall, bestPrecision = threshold, metrics.Recall, metrics.Precision
		}
	}
	if bestRecall >= 0 {
		return bestThreshold
	}
	return bestF1Threshold(scores)
}
func evaluateScores(scores []scoredLabel, t float64) baselineMetrics {
	m := baselineMetrics{Count: len(scores)}
	tp, fp, fn := 0, 0, 0
	for _, s := range scores {
		threshold := t
		if threshold < 0 {
			threshold = s.threshold
		}
		if s.label == 1 {
			m.Positive++
		} else {
			m.Control++
		}
		m.Brier += (s.score - float64(s.label)) * (s.score - float64(s.label))
		if s.score >= threshold && s.label == 1 {
			tp++
		}
		if s.score >= threshold && s.label == 0 {
			fp++
		}
		if s.score < threshold && s.label == 1 {
			fn++
		}
	}
	if len(scores) > 0 {
		m.Brier /= float64(len(scores))
	}
	if tp+fp > 0 {
		m.Precision = float64(tp) / float64(tp+fp)
	}
	if tp+fn > 0 {
		m.Recall = float64(tp) / float64(tp+fn)
	}
	if m.Precision+m.Recall > 0 {
		m.F1 = 2 * m.Precision * m.Recall / (m.Precision + m.Recall)
	}
	m.ROCAUC = rankAUC(scores)
	m.PRAUC = prAUC(scores)
	return m
}
func rankAUC(scores []scoredLabel) float64 {
	sorted := append([]scoredLabel(nil), scores...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].score < sorted[j].score })
	rankSum := 0.0
	pos := 0
	for first := 0; first < len(sorted); {
		last := first + 1
		for last < len(sorted) && sorted[last].score == sorted[first].score {
			last++
		}
		averageRank := float64(first+1+last) / 2
		for index := first; index < last; index++ {
			if sorted[index].label == 1 {
				rankSum += averageRank
				pos++
			}
		}
		first = last
	}
	neg := len(sorted) - pos
	if pos == 0 || neg == 0 {
		return 0
	}
	return (rankSum - float64(pos*(pos+1))/2) / float64(pos*neg)
}

func standardizedValue(features map[string]float64, column string, mean, scale float64) float64 {
	value, exists := features[column]
	if !exists {
		return 0
	}
	return (value - mean) / scale
}
func prAUC(scores []scoredLabel) float64 {
	sorted := append([]scoredLabel(nil), scores...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].score > sorted[j].score })
	total := 0
	for _, s := range sorted {
		if s.label == 1 {
			total++
		}
	}
	if total == 0 {
		return 0
	}
	tp, fp := 0, 0
	area, prevRecall := 0.0, 0.0
	for first := 0; first < len(sorted); {
		last := first + 1
		for last < len(sorted) && sorted[last].score == sorted[first].score {
			last++
		}
		for index := first; index < last; index++ {
			if sorted[index].label == 1 {
				tp++
			} else {
				fp++
			}
		}
		recall := float64(tp) / float64(total)
		precision := float64(tp) / float64(tp+fp)
		area += (recall - prevRecall) * precision
		prevRecall = recall
		first = last
	}
	return area
}
func macroTestMetrics(rows []baselineHorizonReport) baselineMetrics {
	m := baselineMetrics{}
	if len(rows) == 0 {
		return m
	}
	for _, r := range rows {
		m.Count += r.Test.Count
		m.Positive += r.Test.Positive
		m.Control += r.Test.Control
		m.ROCAUC += r.Test.ROCAUC
		m.PRAUC += r.Test.PRAUC
		m.Precision += r.Test.Precision
		m.Recall += r.Test.Recall
		m.F1 += r.Test.F1
		m.Brier += r.Test.Brier
	}
	n := float64(len(rows))
	m.ROCAUC /= n
	m.PRAUC /= n
	m.Precision /= n
	m.Recall /= n
	m.F1 /= n
	m.Brier /= n
	return m
}
func fileSHA256(path string) (string, error) {
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

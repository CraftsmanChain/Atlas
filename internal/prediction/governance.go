package prediction

import (
	"time"

	"atlas/pkg/api"
)

const ModelGovernanceReportVersion = "prediction-model-governance-v1"

type DatasetCard struct {
	Version                    string     `json:"version"`
	DatasetKey                 string     `json:"dataset_key,omitempty"`
	SourceKey                  string     `json:"source_key,omitempty"`
	Status                     string     `json:"status,omitempty"`
	Horizons                   []string   `json:"horizons,omitempty"`
	CandidateCount             int        `json:"candidate_count"`
	EligibleCandidateCount     int        `json:"eligible_candidate_count"`
	EpisodeCount               int        `json:"episode_count"`
	WindowCount                int        `json:"window_count"`
	PendingReviewCount         int        `json:"pending_review_count"`
	IdentityMissingCount       int        `json:"identity_missing_count"`
	ContextOnlyCount           int        `json:"context_only_count"`
	ExcludedCount              int        `json:"excluded_count"`
	FeatureDatasetKey          string     `json:"feature_dataset_key,omitempty"`
	FeatureContractVersion     string     `json:"feature_contract_version,omitempty"`
	FeatureColumnCount         int        `json:"feature_column_count"`
	AverageMetricCoverage      float64    `json:"average_metric_coverage,omitempty"`
	MinimumMetricCoverage      float64    `json:"minimum_metric_coverage,omitempty"`
	PreparedDatasetKey         string     `json:"prepared_dataset_key,omitempty"`
	EligiblePositiveCount      int        `json:"eligible_positive_count"`
	TelemetryCensoredCount     int        `json:"telemetry_censored_count"`
	LowCoverageCount           int        `json:"low_coverage_count"`
	ExtractionFailedCount      int        `json:"extraction_failed_count"`
	PositiveDiscontinuousCount int        `json:"positive_discontinuous_count"`
	LabelIneligibleCount       int        `json:"label_ineligible_count"`
	CorrelatedEventCount       int        `json:"correlated_event_count"`
	EntityTimeConflictCount    int        `json:"entity_time_conflict_count"`
	TrainCount                 int        `json:"train_count"`
	ValidationCount            int        `json:"validation_count"`
	TestCount                  int        `json:"test_count"`
	MatrixKey                  string     `json:"matrix_key,omitempty"`
	MatrixSHA256               string     `json:"matrix_sha256,omitempty"`
	MatrixSampleCount          int        `json:"matrix_sample_count"`
	MatrixPositiveCount        int        `json:"matrix_positive_count"`
	MatrixControlCount         int        `json:"matrix_control_count"`
	DuplicateCount             int        `json:"duplicate_count"`
	PointInTimeViolationCount  int        `json:"point_in_time_violation_count"`
	EntitySplitConflictCount   int        `json:"entity_split_conflict_count"`
	ContractViolationCount     int        `json:"contract_violation_count"`
	FinishedAt                 *time.Time `json:"finished_at,omitempty"`
}

type ModelCard struct {
	ModelKey                 string     `json:"model_key"`
	ModelVersion             string     `json:"model_version"`
	HardwareClass            string     `json:"hardware_class"`
	EntityType               string     `json:"entity_type"`
	Task                     string     `json:"task"`
	HorizonMinutes           int        `json:"horizon_minutes"`
	Algorithm                string     `json:"algorithm"`
	Runtime                  string     `json:"runtime"`
	Mode                     string     `json:"mode"`
	Status                   string     `json:"status"`
	FeatureContractVersion   string     `json:"feature_contract_version"`
	LabelContractVersion     string     `json:"label_contract_version"`
	DatasetVersion           string     `json:"dataset_version,omitempty"`
	SourceBaselineBuildID    uint       `json:"source_baseline_build_id,omitempty"`
	ScopeEventType           string     `json:"scope_event_type,omitempty"`
	ScopeModelName           string     `json:"scope_model_name,omitempty"`
	ArtifactSHA256           string     `json:"artifact_sha256,omitempty"`
	RegistryGateVersion      string     `json:"registry_gate_version,omitempty"`
	DecisionThreshold        *float64   `json:"decision_threshold,omitempty"`
	TrainedAt                *time.Time `json:"trained_at,omitempty"`
	BaselineVersion          string     `json:"baseline_version,omitempty"`
	BaselineStatus           string     `json:"baseline_status,omitempty"`
	FeatureAuditStatus       string     `json:"feature_audit_status,omitempty"`
	ProhibitedFeatureCount   int        `json:"prohibited_feature_count"`
	StatisticallyStableCount int        `json:"statistically_stable_count"`
	ShadowCandidateCount     int        `json:"shadow_candidate_count"`
	TrainCount               int        `json:"train_count"`
	ValidationCount          int        `json:"validation_count"`
	TestCount                int        `json:"test_count"`
	TestMacroROCAUC          float64    `json:"test_macro_roc_auc,omitempty"`
	TestMacroPRAUC           float64    `json:"test_macro_pr_auc,omitempty"`
	TestMacroPrecision       float64    `json:"test_macro_precision,omitempty"`
	TestMacroRecall          float64    `json:"test_macro_recall,omitempty"`
}

type ShadowGateCard struct {
	FeatureParityStatus       string     `json:"feature_parity_status,omitempty"`
	FeatureParityBlocking     []string   `json:"feature_parity_blocking,omitempty"`
	ReplayStatus              string     `json:"replay_status,omitempty"`
	ReplayBlocking            []string   `json:"replay_blocking,omitempty"`
	ReplayVerifiedColumns     int        `json:"replay_verified_columns"`
	ReplayComparedValues      int        `json:"replay_compared_values"`
	ReplayMismatchCount       int        `json:"replay_mismatch_count"`
	LiveCoverageStatus        string     `json:"live_coverage_status,omitempty"`
	LiveCoverageBlocking      []string   `json:"live_coverage_blocking,omitempty"`
	LiveCoverageEligibleRatio float64    `json:"live_coverage_eligible_ratio,omitempty"`
	ShadowRunStatus           string     `json:"shadow_run_status,omitempty"`
	ShadowDistributionStatus  string     `json:"shadow_distribution_status,omitempty"`
	ShadowBlocking            []string   `json:"shadow_blocking,omitempty"`
	ScoredGPUCount            int        `json:"scored_gpu_count"`
	PositiveGPUCount          int        `json:"positive_gpu_count"`
	PositiveRatio             float64    `json:"positive_ratio,omitempty"`
	NoAlertEmitted            bool       `json:"no_alert_emitted"`
	NoActionExecuted          bool       `json:"no_action_executed"`
	EvaluatedAt               *time.Time `json:"evaluated_at,omitempty"`
}

type ModelGovernanceReport struct {
	Version            string         `json:"version"`
	FrameworkVersion   string         `json:"framework_version"`
	Mode               string         `json:"mode"`
	Dataset            DatasetCard    `json:"dataset"`
	Models             []ModelCard    `json:"models"`
	ShadowGates        ShadowGateCard `json:"shadow_gates"`
	Outcome            OutcomeReport  `json:"outcome"`
	Limitations        []string       `json:"limitations"`
	RecommendedNextRun []string       `json:"recommended_next_run"`
	GeneratedAt        time.Time      `json:"generated_at"`
}

func (s *Service) ModelGovernanceReport() (ModelGovernanceReport, error) {
	generatedAt := s.now()
	outcome, err := s.OutcomeReport()
	if err != nil {
		return ModelGovernanceReport{}, err
	}
	report := ModelGovernanceReport{
		Version: ModelGovernanceReportVersion, FrameworkVersion: FrameworkVersion, Mode: "read_only_shadow",
		Outcome: outcome, GeneratedAt: generatedAt,
		Limitations: []string{
			"GPU-only governance snapshot; server, storage, and network predictors are not represented.",
			"Shadow probabilities remain unvalidated operational signals until mature outcome evidence is sufficient.",
			"Dataset and model cards summarize metadata already persisted by Atlas; raw telemetry is not copied into this report.",
		},
		RecommendedNextRun: []string{
			"Review dataset exclusions and low-coverage counts before training new candidates.",
			"Compare model gates with mature outcome ranking metrics before promoting any model.",
			"Keep all probability outputs read-only until calibration and prospective outcome gates pass.",
		},
	}
	if err := s.fillDatasetCard(&report.Dataset); err != nil {
		return ModelGovernanceReport{}, err
	}
	models, err := s.Models()
	if err != nil {
		return ModelGovernanceReport{}, err
	}
	report.Models = make([]ModelCard, 0, len(models))
	for _, model := range models {
		card := modelCardFromSpec(model)
		if model.SourceBaselineBuildID > 0 {
			var baseline api.BaselineModelBuild
			if err := s.db.First(&baseline, model.SourceBaselineBuildID).Error; err == nil {
				fillModelCardBaseline(&card, baseline)
			}
		}
		report.Models = append(report.Models, card)
	}
	if err := s.fillShadowGateCard(&report.ShadowGates); err != nil {
		return ModelGovernanceReport{}, err
	}
	return report, nil
}

func (s *Service) fillDatasetCard(card *DatasetCard) error {
	var dataset api.TrainingDatasetBuild
	if err := s.db.Where("status = ?", "completed").Order("finished_at DESC, id DESC").Limit(1).Find(&dataset).Error; err != nil {
		return err
	}
	if dataset.ID > 0 {
		card.Version, card.DatasetKey, card.SourceKey, card.Status = dataset.Version, dataset.DatasetKey, dataset.SourceKey, dataset.Status
		card.Horizons = append([]string(nil), dataset.Horizons...)
		card.CandidateCount, card.EligibleCandidateCount = dataset.CandidateCount, dataset.EligibleCandidateCount
		card.EpisodeCount, card.WindowCount = dataset.EpisodeCount, dataset.WindowCount
		card.PendingReviewCount, card.IdentityMissingCount = dataset.PendingReviewCount, dataset.IdentityMissingCount
		card.ContextOnlyCount, card.ExcludedCount = dataset.ContextOnlyCount, dataset.ExcludedCount
		card.FinishedAt = dataset.FinishedAt
	}
	var feature api.TrainingFeatureBuild
	if err := s.db.Where("status = ?", "completed").Order("finished_at DESC, id DESC").Limit(1).Find(&feature).Error; err != nil {
		return err
	}
	if feature.ID > 0 {
		card.FeatureDatasetKey, card.FeatureContractVersion = feature.FeatureDatasetKey, feature.FeatureContractVersion
		card.FeatureColumnCount, card.AverageMetricCoverage, card.MinimumMetricCoverage = feature.FeatureColumnCount, feature.AverageMetricCoverage, feature.MinimumMetricCoverage
	}
	var prep api.TrainingPreparationBuild
	if err := s.db.Where("status = ?", "completed").Order("finished_at DESC, id DESC").Limit(1).Find(&prep).Error; err != nil {
		return err
	}
	if prep.ID > 0 {
		card.PreparedDatasetKey, card.EligiblePositiveCount = prep.PreparedDatasetKey, prep.EligiblePositiveCount
		card.TelemetryCensoredCount, card.LowCoverageCount, card.ExtractionFailedCount = prep.TelemetryCensoredCount, prep.LowCoverageCount, prep.ExtractionFailedCount
		card.PositiveDiscontinuousCount, card.LabelIneligibleCount = prep.PositiveDiscontinuousCount, prep.LabelIneligibleCount
		card.CorrelatedEventCount, card.EntityTimeConflictCount = prep.CorrelatedEventCount, prep.EntityTimeConflictCount
		card.TrainCount, card.ValidationCount, card.TestCount = prep.TrainCount, prep.ValidationCount, prep.TestCount
	}
	var matrix api.TrainingMatrixBuild
	if err := s.db.Where("status = ?", "completed").Order("finished_at DESC, id DESC").Limit(1).Find(&matrix).Error; err != nil {
		return err
	}
	if matrix.ID > 0 {
		card.MatrixKey, card.MatrixSHA256 = matrix.TrainingMatrixKey, matrix.MatrixSHA256
		card.MatrixSampleCount, card.MatrixPositiveCount, card.MatrixControlCount = matrix.SampleCount, matrix.PositiveCount, matrix.ControlCount
		card.DuplicateCount, card.PointInTimeViolationCount = matrix.DuplicateCount, matrix.PointInTimeViolationCount
		card.EntitySplitConflictCount, card.ContractViolationCount = matrix.EntitySplitConflictCount, matrix.ContractViolationCount
	}
	return nil
}

func modelCardFromSpec(model api.PredictionModelSpec) ModelCard {
	return ModelCard{
		ModelKey: model.ModelKey, ModelVersion: model.Version, HardwareClass: model.HardwareClass, EntityType: model.EntityType,
		Task: model.Task, HorizonMinutes: model.HorizonMinutes, Algorithm: model.Algorithm, Runtime: model.Runtime,
		Mode: model.Mode, Status: model.Status, FeatureContractVersion: model.FeatureContractVersion, LabelContractVersion: model.LabelContractVersion,
		DatasetVersion: model.DatasetVersion, SourceBaselineBuildID: model.SourceBaselineBuildID, ScopeEventType: model.ScopeEventType,
		ScopeModelName: model.ScopeModelName, ArtifactSHA256: model.ArtifactSHA256, RegistryGateVersion: model.RegistryGateVersion,
		DecisionThreshold: model.DecisionThreshold, TrainedAt: model.TrainedAt,
	}
}

func fillModelCardBaseline(card *ModelCard, baseline api.BaselineModelBuild) {
	card.BaselineVersion, card.BaselineStatus = baseline.Version, baseline.Status
	card.FeatureAuditStatus, card.ProhibitedFeatureCount = baseline.FeatureAuditStatus, baseline.ProhibitedFeatureCount
	card.StatisticallyStableCount, card.ShadowCandidateCount = baseline.StatisticallyStableCount, baseline.ShadowCandidateCount
	card.TrainCount, card.ValidationCount, card.TestCount = baseline.TrainCount, baseline.ValidationCount, baseline.TestCount
	card.TestMacroROCAUC, card.TestMacroPRAUC = baseline.TestMacroROCAUC, baseline.TestMacroPRAUC
	card.TestMacroPrecision, card.TestMacroRecall = baseline.TestMacroPrecision, baseline.TestMacroRecall
}

func (s *Service) fillShadowGateCard(card *ShadowGateCard) error {
	var parity api.PredictionFeatureParityAudit
	if err := s.db.Order("audited_at DESC, id DESC").Limit(1).Find(&parity).Error; err != nil {
		return err
	}
	if parity.ID > 0 {
		card.FeatureParityStatus = parity.Status
		card.FeatureParityBlocking = append([]string(nil), parity.BlockingReasons...)
	}
	var replay api.PredictionFeatureReplayRun
	if err := s.db.Order("started_at DESC, id DESC").Limit(1).Find(&replay).Error; err != nil {
		return err
	}
	if replay.ID > 0 {
		card.ReplayStatus = replay.Status
		card.ReplayBlocking = append([]string(nil), replay.BlockingReasons...)
		card.ReplayVerifiedColumns, card.ReplayComparedValues, card.ReplayMismatchCount = replay.VerifiedColumnCount, replay.ComparedValueCount, replay.MismatchCount
	}
	var coverage api.PredictionLiveCoverageAudit
	if err := s.db.Order("started_at DESC, id DESC").Limit(1).Find(&coverage).Error; err != nil {
		return err
	}
	if coverage.ID > 0 {
		card.LiveCoverageStatus = coverage.Status
		card.LiveCoverageBlocking = append([]string(nil), coverage.BlockingReasons...)
		card.LiveCoverageEligibleRatio = coverage.EligibleRatio
	}
	var shadow api.PredictionShadowScoringRun
	if err := s.db.Order("started_at DESC, id DESC").Limit(1).Find(&shadow).Error; err != nil {
		return err
	}
	card.NoAlertEmitted, card.NoActionExecuted = true, true
	if shadow.ID > 0 {
		card.ShadowRunStatus, card.ShadowDistributionStatus = shadow.Status, shadow.DistributionStatus
		card.ShadowBlocking = append([]string(nil), shadow.BlockingReasons...)
		card.ScoredGPUCount, card.PositiveGPUCount, card.PositiveRatio = shadow.ScoredGPUCount, shadow.PositiveGPUCount, shadow.PositiveRatio
		card.NoAlertEmitted, card.NoActionExecuted = shadow.NoAlertEmitted, shadow.NoActionExecuted
		card.EvaluatedAt = shadow.FinishedAt
	}
	return nil
}

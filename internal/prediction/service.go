package prediction

import (
	"sort"
	"sync"
	"time"

	"atlas/internal/features"
	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm/clause"
)

const (
	FrameworkVersion       = "prediction-framework-v0.27.17"
	FeatureContractVersion = "atlas-prediction-features-v1"
	LabelContractVersion   = "atlas-failure-label-v1"
	readinessFreshnessSLA  = 30 * time.Minute
	minimumFeatureCoverage = 0.8
)

type EntityContract struct {
	HardwareClass string `json:"hardware_class"`
	EntityType    string `json:"entity_type"`
	Status        string `json:"status"`
	IdentityKey   string `json:"identity_key"`
	FeatureSource string `json:"feature_source"`
	LabelSource   string `json:"label_source"`
}

type LabelPolicy struct {
	Version               string         `json:"version"`
	ConfirmedPositive     []string       `json:"confirmed_positive"`
	WeakPositive          []string       `json:"weak_positive"`
	ExcludedAsPositive    []string       `json:"excluded_as_positive"`
	NegativePolicy        string         `json:"negative_policy"`
	PointInTimeRule       string         `json:"point_in_time_rule"`
	EntityIsolation       string         `json:"entity_isolation"`
	MinimumCensoringHours int            `json:"minimum_censoring_hours"`
	QualityTiers          map[string]int `json:"quality_tiers"`
}

type ReleaseGates struct {
	MinimumPrecision float64 `json:"minimum_precision"`
	MinimumRecall    float64 `json:"minimum_recall"`
	Calibration      string  `json:"calibration"`
	TimeSplit        bool    `json:"time_split"`
	EntitySplit      bool    `json:"entity_split"`
	ShadowRequired   bool    `json:"shadow_required"`
	AutoAction       bool    `json:"auto_action"`
}

type ReadinessItem struct {
	HardwareClass      string    `json:"hardware_class"`
	EntityType         string    `json:"entity_type"`
	EntityKey          string    `json:"entity_key"`
	GPUAssetID         uint      `json:"gpu_asset_id"`
	GPUUUID            string    `json:"gpu_uuid"`
	NodeIP             string    `json:"node_ip"`
	GPUIndex           int       `json:"gpu_index"`
	ModelName          string    `json:"model_name"`
	Status             string    `json:"status"`
	DataConfidence     string    `json:"data_confidence"`
	FeatureCoverage    float64   `json:"feature_coverage"`
	FeatureSnapshotID  uint      `json:"feature_snapshot_id"`
	FeatureCatalog     string    `json:"feature_catalog_version"`
	ObservedAt         time.Time `json:"observed_at"`
	BlockingReasons    []string  `json:"blocking_reasons"`
	ProbabilityEmitted bool      `json:"probability_emitted"`
	NoActionExecuted   bool      `json:"no_action_executed"`
}

type ReadinessSummary struct {
	Total              int            `json:"total"`
	ReadyForDataset    int            `json:"ready_for_dataset"`
	Blocked            int            `json:"blocked"`
	ByReason           map[string]int `json:"by_reason"`
	LatestObservedAt   *time.Time     `json:"latest_observed_at,omitempty"`
	ProbabilityEmitted bool           `json:"probability_emitted"`
	EvaluatedAt        time.Time      `json:"evaluated_at"`
}

type ResultSummary struct {
	Total              int64          `json:"total"`
	Current            int64          `json:"current"`
	ByRiskLevel        map[string]int `json:"by_risk_level"`
	ByStatus           map[string]int `json:"by_status"`
	ProbabilityEmitted bool           `json:"probability_emitted"`
}

type RetentionPolicy struct {
	OnlineRetentionDays int    `json:"online_retention_days"`
	ColdArchiveStatus   string `json:"cold_archive_status"`
	TrainingHistorySafe bool   `json:"training_history_safe"`
}

type Overview struct {
	FrameworkVersion       string                    `json:"framework_version"`
	Phase                  string                    `json:"phase"`
	Mode                   string                    `json:"mode"`
	ScoringEnabled         bool                      `json:"scoring_enabled"`
	ProbabilityEmitted     bool                      `json:"probability_emitted"`
	NoActionExecuted       bool                      `json:"no_action_executed"`
	FeatureContractVersion string                    `json:"feature_contract_version"`
	LabelContractVersion   string                    `json:"label_contract_version"`
	FeatureCatalogVersion  string                    `json:"feature_catalog_version"`
	HorizonsMinutes        []int                     `json:"horizons_minutes"`
	EntityContracts        []EntityContract          `json:"entity_contracts"`
	LabelPolicy            LabelPolicy               `json:"label_policy"`
	ReleaseGates           ReleaseGates              `json:"release_gates"`
	Models                 []api.PredictionModelSpec `json:"models"`
	Readiness              ReadinessSummary          `json:"readiness"`
	Results                ResultSummary             `json:"results"`
	Labels                 LabelSummary              `json:"labels"`
	Retention              RetentionPolicy           `json:"retention"`
	GeneratedAt            time.Time                 `json:"generated_at"`
}

type Service struct {
	db                  *storage.DB
	onlineRetentionDays int
	now                 func() time.Time
	labelMu             sync.Mutex
	outcomeMu           sync.Mutex
}

func NewService(db *storage.DB) *Service {
	return NewServiceWithRetention(db, 365*24*time.Hour)
}

func NewServiceWithRetention(db *storage.DB, retention time.Duration) *Service {
	days := int(retention / (24 * time.Hour))
	if days <= 0 {
		days = 365
	}
	// Early shadow records used a HIGH/LOW display label before prospective
	// fleet-distribution validation existed. Preserve their probabilities while
	// removing the unvalidated operational risk semantics.
	_ = db.Model(&api.HardwareRiskPrediction{}).Where("status = ?", "shadow_scored").Updates(map[string]any{
		"risk_level": "unvalidated", "status": "shadow_observation",
	}).Error
	return &Service{db: db, onlineRetentionDays: days, now: time.Now}
}

func BuiltinModels() []api.PredictionModelSpec {
	return []api.PredictionModelSpec{
		modelSpec("gpu.failure.within_1h", 60),
		modelSpec("gpu.failure.within_6h", 360),
		modelSpec("gpu.failure.within_24h", 1440),
	}
}

func modelSpec(key string, horizon int) api.PredictionModelSpec {
	return api.PredictionModelSpec{
		ModelKey: key, Version: "0.1.0", HardwareClass: "gpu", EntityType: "gpu",
		Task: "failure_probability", HorizonMinutes: horizon, Algorithm: "unselected",
		Runtime: "none", Mode: "shadow", Status: "data_readiness",
		FeatureContractVersion: FeatureContractVersion, LabelContractVersion: LabelContractVersion,
		Current: true,
	}
}

func SeedBuiltins(db *storage.DB) error {
	for _, model := range BuiltinModels() {
		if err := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "model_key"}, {Name: "version"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"hardware_class", "entity_type", "task", "horizon_minutes", "algorithm",
				"runtime", "mode", "status", "feature_contract_version",
				"label_contract_version", "current", "updated_at",
			}),
		}).Create(&model).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Models() ([]api.PredictionModelSpec, error) {
	var rows []api.PredictionModelSpec
	err := s.db.Where("current = ?", true).Order("hardware_class, entity_type, horizon_minutes").Find(&rows).Error
	return rows, err
}

func (s *Service) Readiness() (ReadinessSummary, []ReadinessItem, error) {
	evaluatedAt := s.now()
	var scores []api.GPUHealthScore
	if err := s.db.Where("current = ?", true).Order("node_ip, gpu_index").Find(&scores).Error; err != nil {
		return ReadinessSummary{}, nil, err
	}
	snapshotIDs := make([]uint, 0, len(scores))
	for _, score := range scores {
		if score.FeatureSnapshotID > 0 {
			snapshotIDs = append(snapshotIDs, score.FeatureSnapshotID)
		}
	}
	var snapshots []api.GPUFeatureSnapshot
	if len(snapshotIDs) > 0 {
		if err := s.db.Where("id IN ?", snapshotIDs).Find(&snapshots).Error; err != nil {
			return ReadinessSummary{}, nil, err
		}
	}
	byID := make(map[uint]api.GPUFeatureSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byID[snapshot.ID] = snapshot
	}

	summary := ReadinessSummary{Total: len(scores), ByReason: map[string]int{}, EvaluatedAt: evaluatedAt}
	items := make([]ReadinessItem, 0, len(scores))
	for _, score := range scores {
		snapshot, exists := byID[score.FeatureSnapshotID]
		item := ReadinessItem{
			HardwareClass: "gpu", EntityType: "gpu", EntityKey: score.GPUUUID,
			GPUAssetID: score.GPUAssetID, GPUUUID: score.GPUUUID, NodeIP: score.NodeIP,
			GPUIndex: score.GPUIndex, ModelName: score.ModelName, DataConfidence: score.DataConfidence,
			FeatureSnapshotID: score.FeatureSnapshotID, NoActionExecuted: true,
		}
		reasons := make([]string, 0, 5)
		if score.GPUUUID == "" {
			reasons = append(reasons, "identity_missing")
			item.EntityKey = score.NodeIP + ":gpu:" + stringIndex(score.GPUIndex)
		}
		if !exists {
			reasons = append(reasons, "feature_snapshot_missing")
		} else {
			item.ObservedAt = snapshot.ObservedAt
			item.FeatureCatalog = snapshot.FeatureCatalogVersion
			if summary.LatestObservedAt == nil || snapshot.ObservedAt.After(*summary.LatestObservedAt) {
				observedAt := snapshot.ObservedAt
				summary.LatestObservedAt = &observedAt
			}
			if snapshot.ExpectedMetricCount > 0 {
				item.FeatureCoverage = float64(snapshot.AvailableMetricCount) / float64(snapshot.ExpectedMetricCount)
			}
			if snapshot.ObservedAt.After(evaluatedAt) {
				reasons = append(reasons, "snapshot_in_future")
			} else if snapshot.ObservedAt.IsZero() || evaluatedAt.Sub(snapshot.ObservedAt) > readinessFreshnessSLA {
				reasons = append(reasons, "snapshot_stale")
			}
			if snapshot.ConsistencyIssueCount > 0 {
				reasons = append(reasons, "source_inconsistent")
			}
			if snapshot.ExpectedMetricCount == 0 || item.FeatureCoverage < minimumFeatureCoverage {
				reasons = append(reasons, "feature_coverage_low")
			}
			if snapshot.FeatureCatalogVersion == "" {
				reasons = append(reasons, "feature_version_missing")
			}
		}
		if score.DataConfidence != "A" && score.DataConfidence != "B" {
			reasons = append(reasons, "data_confidence_low")
		}
		item.BlockingReasons = reasons
		if len(reasons) == 0 {
			item.Status = "ready_for_dataset"
			summary.ReadyForDataset++
		} else {
			item.Status = "blocked"
			summary.Blocked++
			for _, reason := range reasons {
				summary.ByReason[reason]++
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Status != items[j].Status {
			return items[i].Status < items[j].Status
		}
		if items[i].NodeIP != items[j].NodeIP {
			return items[i].NodeIP < items[j].NodeIP
		}
		return items[i].GPUIndex < items[j].GPUIndex
	})
	return summary, items, nil
}

func (s *Service) Results(limit int) (ResultSummary, []api.HardwareRiskPrediction, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []api.HardwareRiskPrediction
	if err := s.db.Where("current = ?", true).Order("evaluated_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return ResultSummary{}, nil, err
	}
	summary := ResultSummary{ByRiskLevel: map[string]int{}, ByStatus: map[string]int{}}
	if err := s.db.Model(&api.HardwareRiskPrediction{}).Count(&summary.Total).Error; err != nil {
		return ResultSummary{}, nil, err
	}
	if err := s.db.Model(&api.HardwareRiskPrediction{}).Where("current = ?", true).Count(&summary.Current).Error; err != nil {
		return ResultSummary{}, nil, err
	}
	var probabilityCount int64
	if err := s.db.Model(&api.HardwareRiskPrediction{}).Where("current = ? AND probability IS NOT NULL", true).Count(&probabilityCount).Error; err != nil {
		return ResultSummary{}, nil, err
	}
	summary.ProbabilityEmitted = probabilityCount > 0
	for _, row := range rows {
		summary.ByRiskLevel[row.RiskLevel]++
		summary.ByStatus[row.Status]++
	}
	return summary, rows, nil
}

func (s *Service) Overview() (Overview, error) {
	models, err := s.Models()
	if err != nil {
		return Overview{}, err
	}
	results, _, err := s.Results(500)
	if err != nil {
		return Overview{}, err
	}
	labels, _, err := s.Labels(100)
	if err != nil {
		return Overview{}, err
	}
	readiness, _, err := s.Readiness()
	if err != nil {
		return Overview{}, err
	}
	phase := "gpu_data_readiness"
	horizons := []int{60, 360, 1440}
	for _, model := range models {
		if model.Status == "shadow_candidate" {
			phase = "gpu_shadow_candidate_registered"
		}
		horizons = append(horizons, model.HorizonMinutes)
	}
	if results.ProbabilityEmitted {
		phase = "gpu_read_only_shadow_scoring"
	}
	horizons = uniqueSortedHorizons(horizons)
	return Overview{
		FrameworkVersion: FrameworkVersion, Phase: phase, Mode: "shadow",
		ScoringEnabled: results.ProbabilityEmitted, ProbabilityEmitted: results.ProbabilityEmitted, NoActionExecuted: true,
		FeatureContractVersion: FeatureContractVersion, LabelContractVersion: LabelContractVersion,
		FeatureCatalogVersion: features.CatalogVersion, HorizonsMinutes: horizons,
		EntityContracts: []EntityContract{
			{HardwareClass: "gpu", EntityType: "gpu", Status: "active", IdentityKey: "gpu_uuid", FeatureSource: "gpu_feature_snapshots", LabelSource: "gpu_fault_events + confirmed resolutions"},
			{HardwareClass: "server", EntityType: "node", Status: "planned", IdentityKey: "asset_sn", FeatureSource: "node telemetry + BMC", LabelSource: "availability events + maintenance outcomes"},
			{HardwareClass: "storage", EntityType: "storage_device", Status: "planned", IdentityKey: "asset_sn", FeatureSource: "device telemetry + controller events", LabelSource: "device faults + replacement outcomes"},
			{HardwareClass: "network", EntityType: "network_device", Status: "planned", IdentityKey: "asset_sn", FeatureSource: "interface telemetry + device events", LabelSource: "link/device faults + replacement outcomes"},
		},
		LabelPolicy: LabelPolicy{
			Version:               LabelContractVersion,
			ConfirmedPositive:     []string{"operator-confirmed hardware failure", "component replacement with validated recovery"},
			WeakPositive:          []string{"deterministic GPU fault episode", "repeated critical XID or uncorrectable error"},
			ExcludedAsPositive:    []string{"stable lifetime aggregate without observed growth", "telemetry missing only", "credential failure", "planned shutdown", "retired asset"},
			NegativePolicy:        "Only create negatives after the full prediction horizon and censoring window pass without a positive event.",
			PointInTimeRule:       "Every feature must be observed before the label cutoff; post-failure evidence is forbidden.",
			EntityIsolation:       "Train, calibration, and evaluation splits isolate GPU UUIDs and preserve time order.",
			MinimumCensoringHours: 24,
			QualityTiers:          map[string]int{"confirmed": 3, "strong_proxy": 2, "weak_proxy": 1, "excluded": 0},
		},
		ReleaseGates: ReleaseGates{
			MinimumPrecision: 0.7, MinimumRecall: 0.5,
			Calibration: "Brier score and reliability curve required per model and horizon",
			TimeSplit:   true, EntitySplit: true, ShadowRequired: true, AutoAction: false,
		},
		Models: models, Readiness: readiness, Results: results, Labels: labels,
		Retention: RetentionPolicy{
			OnlineRetentionDays: s.onlineRetentionDays,
			ColdArchiveStatus:   "planned",
			TrainingHistorySafe: s.onlineRetentionDays >= 180,
		},
		GeneratedAt: s.now(),
	}, nil
}

func uniqueSortedHorizons(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}

func stringIndex(index int) string {
	if index == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for index > 0 {
		digits = append([]byte{byte('0' + index%10)}, digits...)
		index /= 10
	}
	return string(digits)
}

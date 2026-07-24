package features

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BaselineContractVersion = "feature-baseline-v1.0.0"
	baselineWindowDays      = 7
	baselineSamplesPerGPU   = 200
	baselineRefreshInterval = 6 * time.Hour
	baselineMinimumGPUs     = 4
	baselineMinimumSamples  = 20
	baselineMinimumCoverage = 24 * time.Hour
)

var baselineFeatureNames = []string{"sm_clock_avg_15m"}

type BaselineListOptions struct {
	FeatureName    string
	FeatureVersion string
	ModelName      string
	LoadBucket     string
	Maturity       string
}

type BaselineRefreshResult struct {
	Run       api.FeatureBaselineRefreshRun
	Refreshed bool
}

type baselineSnapshot struct {
	ID                    uint
	GPUAssetID            uint
	ModelName             string
	Metrics               api.FloatMap
	FeatureCatalogVersion string
	FeatureVersions       api.StringMap
	DataConfidence        string
	ObservedAt            time.Time
}

type baselineSnapshotRow struct {
	ID                    uint
	GPUAssetID            uint
	ModelName             string
	Metrics               string
	FeatureCatalogVersion string
	FeatureVersions       string
	DataConfidence        string
	ObservedAt            time.Time
}

type baselineGroupKey struct {
	featureName    string
	featureVersion string
	modelName      string
	loadBucket     string
}

type baselineGroup struct {
	values    []float64
	gpuIDs    map[uint]struct{}
	startedAt time.Time
	endedAt   time.Time
}

func RefreshHistoricalBaselines(db *storage.DB, now time.Time, force bool) (BaselineRefreshResult, error) {
	wallStartedAt := time.Now()
	var latest api.FeatureBaselineRefreshRun
	result := db.Where(
		"status = ? AND contract_version = ? AND feature_catalog_version = ?",
		"success", BaselineContractVersion, CatalogVersion,
	).Order("finished_at DESC").Order("id DESC").Limit(1).Find(&latest)
	if result.Error != nil {
		return BaselineRefreshResult{}, result.Error
	}
	if !force && result.RowsAffected > 0 && latest.FinishedAt != nil && now.Sub(*latest.FinishedAt) < baselineRefreshInterval {
		return BaselineRefreshResult{Run: latest, Refreshed: false}, nil
	}

	run := api.FeatureBaselineRefreshRun{
		Status: "running", ContractVersion: BaselineContractVersion,
		FeatureCatalogVersion: CatalogVersion, WindowDays: baselineWindowDays, StartedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		return BaselineRefreshResult{}, err
	}
	fail := func(err error) (BaselineRefreshResult, error) {
		elapsed := time.Since(wallStartedAt)
		finished := now.Add(elapsed)
		run.Status, run.FinishedAt, run.ErrorMessage = "failed", &finished, err.Error()
		run.DurationMillis = elapsed.Milliseconds()
		_ = db.Save(&run).Error
		return BaselineRefreshResult{Run: run, Refreshed: true}, err
	}

	var rows []baselineSnapshotRow
	cutoff := now.AddDate(0, 0, -baselineWindowDays)
	query := `
WITH bucketed AS (
	SELECT id, gpu_asset_id, model_name, metrics, feature_catalog_version, feature_versions, data_confidence, observed_at,
		NTILE(?) OVER (PARTITION BY gpu_asset_id ORDER BY observed_at ASC, id ASC) AS sample_bucket
	FROM gpu_feature_snapshots
	WHERE observed_at >= ? AND data_confidence IN ('A', 'B')
), sampled AS (
	SELECT id, gpu_asset_id, model_name, metrics, feature_catalog_version, feature_versions, data_confidence, observed_at,
		ROW_NUMBER() OVER (PARTITION BY gpu_asset_id, sample_bucket ORDER BY observed_at DESC, id DESC) AS bucket_rank
	FROM bucketed
)
SELECT id, gpu_asset_id, model_name, metrics, feature_catalog_version, feature_versions, data_confidence, observed_at
FROM sampled
WHERE bucket_rank = 1`
	if err := db.Raw(query, baselineSamplesPerGPU, cutoff).Scan(&rows).Error; err != nil {
		return fail(err)
	}
	snapshots := make([]baselineSnapshot, 0, len(rows))
	for _, row := range rows {
		snapshot := baselineSnapshot{
			ID: row.ID, GPUAssetID: row.GPUAssetID, ModelName: row.ModelName,
			FeatureCatalogVersion: row.FeatureCatalogVersion, DataConfidence: row.DataConfidence,
			ObservedAt: row.ObservedAt,
		}
		if err := json.Unmarshal([]byte(row.Metrics), &snapshot.Metrics); err != nil {
			return fail(fmt.Errorf("decode metrics for feature snapshot %d: %w", row.ID, err))
		}
		if strings.TrimSpace(row.FeatureVersions) != "" {
			if err := json.Unmarshal([]byte(row.FeatureVersions), &snapshot.FeatureVersions); err != nil {
				return fail(fmt.Errorf("decode feature versions for snapshot %d: %w", row.ID, err))
			}
		}
		snapshots = append(snapshots, snapshot)
	}
	run.SnapshotCount = len(snapshots)
	groups := make(map[baselineGroupKey]*baselineGroup)
	for _, snapshot := range snapshots {
		modelName := strings.TrimSpace(snapshot.ModelName)
		if modelName == "" || snapshot.ObservedAt.IsZero() {
			continue
		}
		loadBucket := featureLoadBucket(snapshot.Metrics["gpu_util_avg_15m"])
		for _, featureName := range baselineFeatureNames {
			value, ok := snapshot.Metrics[featureName]
			if !ok || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			featureVersion := strings.TrimSpace(snapshot.FeatureVersions[featureName])
			if featureVersion == "" {
				featureVersion = strings.TrimSpace(snapshot.FeatureCatalogVersion)
			}
			if featureVersion == "" {
				featureVersion = CatalogVersion
			}
			key := baselineGroupKey{featureName: featureName, featureVersion: featureVersion, modelName: modelName, loadBucket: loadBucket}
			group := groups[key]
			if group == nil {
				group = &baselineGroup{gpuIDs: map[uint]struct{}{}, startedAt: snapshot.ObservedAt, endedAt: snapshot.ObservedAt}
				groups[key] = group
			}
			group.values = append(group.values, value)
			group.gpuIDs[snapshot.GPUAssetID] = struct{}{}
			if snapshot.ObservedAt.Before(group.startedAt) {
				group.startedAt = snapshot.ObservedAt
			}
			if snapshot.ObservedAt.After(group.endedAt) {
				group.endedAt = snapshot.ObservedAt
			}
		}
	}

	baselines := make([]api.GPUFeatureBaseline, 0, len(groups))
	for key, group := range groups {
		sort.Float64s(group.values)
		p50 := quantile(group.values, 0.50)
		deviations := make([]float64, len(group.values))
		for index, value := range group.values {
			deviations[index] = math.Abs(value - p50)
		}
		sort.Float64s(deviations)
		baselines = append(baselines, api.GPUFeatureBaseline{
			ContractVersion: BaselineContractVersion,
			FeatureName:     key.featureName, FeatureVersion: key.featureVersion,
			ModelName: key.modelName, LoadBucket: key.loadBucket, WindowDays: baselineWindowDays,
			SampleCount: len(group.values), GPUCount: len(group.gpuIDs),
			P05: quantile(group.values, 0.05), P50: p50, P95: quantile(group.values, 0.95),
			MAD: quantile(deviations, 0.50), Maturity: baselineMaturity(group),
			WindowStartedAt: group.startedAt, WindowEndedAt: group.endedAt,
			ComputedAt: now, Owner: "atlas-ml",
		})
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		for index := range baselines {
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{
					{Name: "contract_version"}, {Name: "feature_name"}, {Name: "feature_version"},
					{Name: "model_name"}, {Name: "load_bucket"}, {Name: "window_days"},
				},
				DoUpdates: clause.AssignmentColumns([]string{
					"sample_count", "gpu_count", "p05", "p50", "p95", "mad", "maturity",
					"window_started_at", "window_ended_at", "computed_at", "owner", "updated_at",
				}),
			}).Create(&baselines[index]).Error; err != nil {
				return err
			}
		}
		return tx.Where("contract_version = ? AND window_days = ? AND computed_at < ?", BaselineContractVersion, baselineWindowDays, now).
			Delete(&api.GPUFeatureBaseline{}).Error
	})
	if err != nil {
		return fail(err)
	}
	elapsed := time.Since(wallStartedAt)
	finished := now.Add(elapsed)
	run.Status, run.FinishedAt, run.BaselineCount = "success", &finished, len(baselines)
	run.DurationMillis = elapsed.Milliseconds()
	if err := db.Save(&run).Error; err != nil {
		return BaselineRefreshResult{}, err
	}
	return BaselineRefreshResult{Run: run, Refreshed: true}, nil
}

func featureLoadBucket(utilization float64) string {
	switch {
	case utilization >= 80:
		return "high"
	case utilization >= 20:
		return "normal"
	default:
		return "idle"
	}
}

func baselineMaturity(group *baselineGroup) string {
	if len(group.gpuIDs) >= baselineMinimumGPUs && len(group.values) >= baselineMinimumSamples &&
		group.endedAt.Sub(group.startedAt) >= baselineMinimumCoverage {
		return "mature"
	}
	if len(group.gpuIDs) >= baselineMinimumGPUs {
		return "warming"
	}
	return "insufficient"
}

func quantile(sorted []float64, probability float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	position := probability * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func ListBaselines(db *storage.DB, options BaselineListOptions) ([]api.GPUFeatureBaseline, error) {
	query := db.Model(&api.GPUFeatureBaseline{}).Where("contract_version = ?", BaselineContractVersion)
	if value := strings.TrimSpace(options.FeatureName); value != "" {
		query = query.Where("feature_name = ?", value)
	}
	if value := strings.TrimSpace(options.FeatureVersion); value != "" {
		query = query.Where("feature_version = ?", value)
	}
	if value := strings.TrimSpace(options.ModelName); value != "" {
		query = query.Where("model_name = ?", value)
	}
	if value := strings.TrimSpace(options.LoadBucket); value != "" {
		query = query.Where("load_bucket = ?", value)
	}
	if value := strings.TrimSpace(options.Maturity); value != "" {
		query = query.Where("maturity = ?", value)
	}
	var baselines []api.GPUFeatureBaseline
	err := query.Order("feature_name ASC, model_name ASC, load_bucket ASC, feature_version DESC").Find(&baselines).Error
	return baselines, err
}

func LatestBaselineRefresh(db *storage.DB) (*api.FeatureBaselineRefreshRun, error) {
	var run api.FeatureBaselineRefreshRun
	if err := db.Where(
		"contract_version = ? AND feature_catalog_version = ?",
		BaselineContractVersion, CatalogVersion,
	).Order("id DESC").First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

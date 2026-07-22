package health

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"atlas/internal/features"
	"atlas/internal/prometheus"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

type prometheusReader interface {
	BaseURL() string
	Query(context.Context, string) ([]prometheus.Sample, error)
}

type Service struct {
	db     *storage.DB
	prom   prometheusReader
	config config.HealthConfig
	now    func() time.Time
}

type metricSpec struct {
	key   string
	query string
}

var metricSpecs = func() []metricSpec {
	catalogSpecs := features.HealthMetricSpecs()
	result := make([]metricSpec, 0, len(catalogSpecs))
	for _, spec := range catalogSpecs {
		result = append(result, metricSpec{key: spec.Key, query: spec.Query})
	}
	return result
}()

type featureValue struct {
	metrics    api.FloatMap
	observedAt time.Time
}

type ruleHit struct {
	code, domain, severity string
	deduction              int
	value                  float64
	threshold, evidence    string
}

type scoreResult struct {
	score                                                *int
	level                                                string
	stability, memory, thermal, power, link, performance int
	hits                                                 []ruleHit
	evidence                                             api.StringList
}

func NewService(db *storage.DB, prom prometheusReader, cfg config.HealthConfig) *Service {
	return &Service{db: db, prom: prom, config: cfg, now: time.Now}
}

func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	s.evaluateAndLog(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.evaluateAndLog(ctx)
		}
	}
}

func (s *Service) evaluateAndLog(ctx context.Context) {
	if _, err := s.Evaluate(ctx); err != nil && ctx.Err() == nil {
		log.Printf("GPU health evaluation failed: %v", err)
	}
}

func (s *Service) Evaluate(ctx context.Context) (*api.HealthEvaluationRun, error) {
	now := s.now()
	ruleVersion := strings.TrimSpace(s.config.RuleVersion)
	if ruleVersion == "" {
		ruleVersion = "gpu-health-v1.0.0"
	}
	run := &api.HealthEvaluationRun{Status: "running", RuleVersion: ruleVersion, Source: s.prom.BaseURL(), StartedAt: now}
	if err := s.db.Create(run).Error; err != nil {
		return nil, err
	}
	fail := func(err error) (*api.HealthEvaluationRun, error) {
		finished := s.now()
		run.Status, run.FinishedAt, run.ErrorMessage = "failed", &finished, err.Error()
		_ = s.db.Save(run).Error
		return run, err
	}

	featureValues := make(map[string]*featureValue)
	successfulQueries := 0
	for _, spec := range metricSpecs {
		rows, err := s.prom.Query(ctx, spec.query)
		if err != nil {
			log.Printf("health metric query skipped: key=%s error=%v", spec.key, err)
			continue
		}
		successfulQueries++
		for _, row := range rows {
			uuid := strings.TrimSpace(row.Metric["UUID"])
			if uuid == "" || math.IsNaN(row.Value) || math.IsInf(row.Value, 0) {
				continue
			}
			feature := featureValues[uuid]
			if feature == nil {
				feature = &featureValue{metrics: api.FloatMap{}}
				featureValues[uuid] = feature
			}
			feature.metrics[spec.key] = row.Value
			if row.Timestamp.After(feature.observedAt) {
				feature.observedAt = row.Timestamp
			}
		}
	}
	if successfulQueries == 0 {
		return fail(fmt.Errorf("all health metric queries failed"))
	}

	activeNodeIDs := s.db.Model(&api.GPUNode{}).Select("id").Where("lifecycle <> ?", "retired")
	var assets []api.GPUAsset
	if err := s.db.Where("node_id IN (?) AND current_uuid <> ''", activeNodeIDs).Order("node_ip ASC, gpu_index ASC").Find(&assets).Error; err != nil {
		return fail(err)
	}
	run.AssetCount = len(assets)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&api.GPUHealthScore{}).Where("current = ?", true).Update("current", false).Error; err != nil {
			return err
		}
		for _, asset := range assets {
			value := featureValues[asset.CurrentUUID]
			metrics := api.FloatMap{}
			observedAt := now
			if value != nil {
				metrics, observedAt = value.metrics, value.observedAt
			}
			expected := expectedMetricKeys(asset.ModelName)
			available := availableCount(metrics, expected)
			confidence := dataConfidence(asset.State, available, len(expected))
			snapshot := api.GPUFeatureSnapshot{EvaluationRunID: run.ID, GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, NodeIP: asset.NodeIP, GPUIndex: asset.GPUIndex, ModelName: asset.ModelName, Metrics: metrics, FeatureCatalogVersion: features.CatalogVersion, FeatureVersions: features.HealthFeatureVersions(), AvailableMetricCount: available, ExpectedMetricCount: len(expected), DataConfidence: confidence, ObservedAt: observedAt}
			if err := tx.Create(&snapshot).Error; err != nil {
				return err
			}
			result := evaluateRules(metrics, asset.ModelName, confidence)
			score := api.GPUHealthScore{EvaluationRunID: run.ID, FeatureSnapshotID: snapshot.ID, GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, NodeIP: asset.NodeIP, GPUIndex: asset.GPUIndex, ModelName: asset.ModelName, Score: result.score, Level: result.level, DataConfidence: confidence, StabilityScore: result.stability, MemoryScore: result.memory, ThermalScore: result.thermal, PowerScore: result.power, InterconnectScore: result.link, PerformanceScore: result.performance, Evidence: result.evidence, RuleVersion: ruleVersion, Current: true, EvaluatedAt: now}
			if err := tx.Create(&score).Error; err != nil {
				return err
			}
			if result.score == nil {
				run.UnknownCount++
			} else {
				run.ScoredCount++
			}
			for _, hit := range result.hits {
				record := api.GPUHealthRuleHit{HealthScoreID: score.ID, GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, RuleCode: hit.code, Domain: hit.domain, Severity: hit.severity, Deduction: hit.deduction, ObservedValue: hit.value, Threshold: hit.threshold, Evidence: hit.evidence, RuleVersion: ruleVersion, EvaluatedAt: now}
				if err := tx.Create(&record).Error; err != nil {
					return err
				}
				run.RuleHitCount++
			}
			if err := reconcileFaultEvents(tx, asset, score, result.hits, confidence, now, ruleVersion); err != nil {
				return err
			}
		}
		cutoff := now.Add(-35 * 24 * time.Hour)
		if err := tx.Where("evaluated_at < ?", cutoff).Delete(&api.GPUHealthRuleHit{}).Error; err != nil {
			return err
		}
		if err := tx.Where("evaluated_at < ?", cutoff).Delete(&api.GPUHealthScore{}).Error; err != nil {
			return err
		}
		if err := tx.Where("observed_at < ?", cutoff).Delete(&api.GPUFeatureSnapshot{}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	finished := s.now()
	run.Status, run.FinishedAt = "success", &finished
	if err := s.db.Save(run).Error; err != nil {
		return nil, err
	}
	return run, nil
}

func expectedMetricKeys(model string) []string {
	return features.ExpectedHealthKeys(model)
}

func availableCount(metrics api.FloatMap, keys []string) int {
	count := 0
	for _, key := range keys {
		if _, ok := metrics[key]; ok {
			count++
		}
	}
	return count
}

func dataConfidence(assetState string, available, expected int) string {
	if assetState != "active" || expected == 0 {
		return "D"
	}
	ratio := float64(available) / float64(expected)
	switch {
	case ratio >= .90:
		return "A"
	case ratio >= .75:
		return "B"
	case ratio >= .50:
		return "C"
	default:
		return "D"
	}
}

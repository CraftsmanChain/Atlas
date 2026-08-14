package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"atlas/internal/featurestats"
	promclient "atlas/internal/prometheus"
	"atlas/pkg/api"
	"gorm.io/gorm"
)

const (
	shadowScoringVersion           = "gpu-shadow-scoring-v3"
	maximumShadowPositiveRatio     = 0.20
	maximumShadowMedianToThreshold = 1.0
)

type shadowScoringRequest struct {
	ModelSpecID uint `json:"model_spec_id,omitempty"`
}

type shadowScoringGPUReport struct {
	GPUAssetID          uint     `json:"gpu_asset_id"`
	GPUUUID             string   `json:"gpu_uuid"`
	NodeIP              string   `json:"node_ip"`
	GPUIndex            int      `json:"gpu_index"`
	Status              string   `json:"status"`
	Probability         *float64 `json:"probability,omitempty"`
	PredictedPositive   bool     `json:"predicted_positive"`
	FeatureVectorSHA256 string   `json:"feature_vector_sha256,omitempty"`
	BlockingReasons     []string `json:"blocking_reasons"`
}

type shadowScoringReport struct {
	Version               string                   `json:"version"`
	RunKey                string                   `json:"run_key"`
	ModelKey              string                   `json:"model_key"`
	ModelVersion          string                   `json:"model_version"`
	ArtifactSHA256        string                   `json:"artifact_sha256"`
	TransformationVersion string                   `json:"transformation_contract_version"`
	DecisionThreshold     float64                  `json:"decision_threshold"`
	DistributionStatus    string                   `json:"distribution_status"`
	DistributionReasons   []string                 `json:"distribution_reasons"`
	FeatureColumns        []string                 `json:"feature_columns"`
	GPUs                  []shadowScoringGPUReport `json:"gpus"`
	NoAlertEmitted        bool                     `json:"no_alert_emitted"`
	NoActionExecuted      bool                     `json:"no_action_executed"`
	CreatedAt             time.Time                `json:"created_at"`
}

func (s *Service) ShadowScoringRuns(limit int) ([]api.PredictionShadowScoringRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.PredictionShadowScoringRun
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) StartShadowScoring(modelSpecID uint) (api.PredictionShadowScoringRun, error) {
	s.shadowMu.Lock()
	defer s.shadowMu.Unlock()
	if s.shadowRunning {
		return api.PredictionShadowScoringRun{}, fmt.Errorf("a shadow scoring run is already active")
	}
	var spec api.PredictionModelSpec
	query := s.db.Where("current = ? AND status = ?", true, "shadow_candidate")
	if modelSpecID > 0 {
		query = query.Where("id = ?", modelSpecID)
	}
	result := query.Order("trained_at DESC, id DESC").Limit(1).Find(&spec)
	if result.Error != nil {
		return api.PredictionShadowScoringRun{}, result.Error
	}
	if result.RowsAffected == 0 {
		return api.PredictionShadowScoringRun{}, fmt.Errorf("a current shadow model candidate is required")
	}
	var parity api.PredictionFeatureParityAudit
	result = s.db.Where("model_spec_id = ?", spec.ID).Limit(1).Find(&parity)
	if result.Error != nil || result.RowsAffected == 0 || parity.Status != "shadow_runtime_required" || parity.ReplayVerifiedCount != parity.TrainingFeatureCount {
		return api.PredictionShadowScoringRun{}, fmt.Errorf("feature replay and live coverage gates must pass before shadow scoring")
	}
	var coverage api.PredictionLiveCoverageAudit
	result = s.db.Where("model_spec_id = ? AND status = ?", spec.ID, "passed").Order("finished_at DESC, id DESC").Limit(1).Find(&coverage)
	if result.Error != nil || result.RowsAffected == 0 {
		return api.PredictionShadowScoringRun{}, fmt.Errorf("a passed live coverage audit is required")
	}
	started := s.now()
	key := shadowScoringVersion + "-" + strconv.FormatInt(started.UTC().UnixNano(), 10)
	run := api.PredictionShadowScoringRun{
		RunKey: key, Version: shadowScoringVersion, Status: "queued", Trigger: "manual",
		ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version,
		ArtifactSHA256: spec.ArtifactSHA256, SourceKey: coverage.SourceKey, ScopeModelName: spec.ScopeModelName,
		TransformationVersion: parity.TransformationContractVersion, WindowMinutes: int(featureLookback / time.Minute),
		QueryStepSeconds: int(featureQueryStep / time.Second), NoAlertEmitted: true, NoActionExecuted: true, StartedAt: started,
	}
	if err := s.db.Create(&run).Error; err != nil {
		return run, err
	}
	s.shadowRunning = true
	go s.executeShadowScoring(run.ID)
	return run, nil
}

func (s *Service) executeShadowScoring(id uint) {
	defer func() {
		s.shadowMu.Lock()
		s.shadowRunning = false
		s.shadowMu.Unlock()
	}()
	var run api.PredictionShadowScoringRun
	if err := s.db.First(&run, id).Error; err != nil {
		return
	}
	if err := s.db.Model(&run).Update("status", "running").Error; err != nil {
		return
	}
	if err := s.buildShadowScoring(&run); err != nil {
		finished := s.now()
		_ = s.db.Model(&run).Updates(map[string]any{"status": "failed", "error_message": err.Error(), "finished_at": &finished}).Error
	}
}

func (s *Service) buildShadowScoring(run *api.PredictionShadowScoringRun) error {
	var spec api.PredictionModelSpec
	var parity api.PredictionFeatureParityAudit
	if err := s.db.First(&spec, run.ModelSpecID).Error; err != nil {
		return err
	}
	if err := s.db.Where("model_spec_id = ?", spec.ID).First(&parity).Error; err != nil {
		return err
	}
	checksum, err := fileSHA256(spec.ArtifactURI)
	if err != nil {
		return fmt.Errorf("verify model artifact: %w", err)
	}
	if checksum != spec.ArtifactSHA256 || checksum != run.ArtifactSHA256 {
		return fmt.Errorf("model artifact checksum mismatch")
	}
	var artifact baselineArtifact
	file, err := os.Open(spec.ArtifactURI)
	if err != nil {
		return err
	}
	err = json.NewDecoder(io.LimitReader(file, 32<<20)).Decode(&artifact)
	_ = file.Close()
	if err != nil {
		return fmt.Errorf("decode model artifact: %w", err)
	}
	if artifact.ScopeEventType != spec.ScopeEventType || artifact.ScopeModelName != spec.ScopeModelName {
		return fmt.Errorf("model artifact scope mismatch")
	}
	var model *logisticModel
	for index := range artifact.Models {
		if artifact.Models[index].HorizonMinutes == spec.HorizonMinutes {
			model = &artifact.Models[index]
			break
		}
	}
	if model == nil || model.Calibration.Status != "fitted" || model.Calibration.Slope <= 0 || spec.DecisionThreshold == nil || math.Abs(model.Threshold-*spec.DecisionThreshold) > 1e-12 || len(model.FeatureColumns) != parity.TrainingFeatureCount || len(model.Means) != len(model.FeatureColumns) || len(model.Scales) != len(model.FeatureColumns) || len(model.Coefficients) != len(model.FeatureColumns) {
		return fmt.Errorf("model runtime contract is incomplete")
	}
	var assets []api.GPUAsset
	if err := s.db.Where("current_node_identity = ? AND state = ? AND model_name = ? AND current_uuid <> ?", true, "active", spec.ScopeModelName, "").Order("node_ip, gpu_index").Find(&assets).Error; err != nil {
		return err
	}
	if len(assets) == 0 {
		return fmt.Errorf("no active GPUs match model scope %q", spec.ScopeModelName)
	}
	source, err := s.resolveSource(run.SourceKey)
	if err != nil {
		return err
	}
	client, err := s.historyClient(source)
	if err != nil {
		return err
	}
	end := s.now()
	start := end.Add(-featureLookback)
	byGPU := map[string]map[string][]promclient.RangePoint{}
	for offset := 0; offset < len(assets); offset += liveCoverageUUIDChunkSize {
		limit := offset + liveCoverageUUIDChunkSize
		if limit > len(assets) {
			limit = len(assets)
		}
		uuids := make([]string, 0, limit-offset)
		for _, asset := range assets[offset:limit] {
			uuids = append(uuids, asset.CurrentUUID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		series, queryErr := client.QueryRange(ctx, historicalMetricQueryUUIDs(uuids), start, end, featureQueryStep)
		cancel()
		if queryErr != nil {
			return fmt.Errorf("shadow query chunk %d: %w", offset/liveCoverageUUIDChunkSize, queryErr)
		}
		for uuid, values := range canonicalSeriesByGPU(series) {
			byGPU[uuid] = values
		}
	}
	report := shadowScoringReport{Version: shadowScoringVersion, RunKey: run.RunKey, ModelKey: spec.ModelKey, ModelVersion: spec.Version, ArtifactSHA256: checksum, TransformationVersion: parity.TransformationContractVersion, DecisionThreshold: *spec.DecisionThreshold, FeatureColumns: append([]string(nil), model.FeatureColumns...), GPUs: make([]shadowScoringGPUReport, 0, len(assets)), NoAlertEmitted: true, NoActionExecuted: true, CreatedAt: s.now()}
	predictions := make([]api.HardwareRiskPrediction, 0, len(assets))
	probabilitySum := 0.0
	probabilities := make([]float64, 0, len(assets))
	liveFeatureValues := map[string][]float64{}
	nodeCounts, nodePositiveCounts, nodeProbabilitySums := map[string]int{}, map[string]int{}, map[string]float64{}
	for _, asset := range assets {
		item := shadowScoringGPUReport{GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, NodeIP: asset.NodeIP, GPUIndex: asset.GPUIndex, Status: "scored", BlockingReasons: []string{}}
		features := map[string]float64{}
		values := byGPU[normalizeHistoricalGPUUUID(asset.CurrentUUID)]
		for _, metric := range parity.SourceMetrics {
			points := pointsInWindow(values[metric], start, end)
			if len(points) < int(math.Ceil(float64(int(featureLookback/featureQueryStep)+1)*liveCoverageMinimumRatio)) {
				item.BlockingReasons = append(item.BlockingReasons, "metric_sparse:"+metric)
				continue
			}
			if end.Sub(points[len(points)-1].Timestamp) > liveCoverageFreshnessSLA {
				item.BlockingReasons = append(item.BlockingReasons, "metric_stale:"+metric)
				continue
			}
			featurestats.AddTrailing24hStatistics(features, metric, points)
		}
		for _, column := range model.FeatureColumns {
			if _, exists := features[column]; !exists {
				item.BlockingReasons = append(item.BlockingReasons, "feature_missing:"+column)
			}
		}
		prediction := api.HardwareRiskPrediction{ShadowRunID: run.ID, ModelSpecID: spec.ID, ModelVersion: spec.Version, HardwareClass: "gpu", EntityType: "gpu", EntityKey: asset.CurrentUUID, GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, NodeIP: asset.NodeIP, HorizonMinutes: spec.HorizonMinutes, RiskLevel: "unknown", Status: "shadow_blocked", Explanations: api.StringList{}, Current: true, ObservedAt: end, EvaluatedAt: s.now(), ExpiresAt: end.Add(time.Duration(spec.HorizonMinutes) * time.Minute), TransformVersion: parity.TransformationContractVersion}
		if len(item.BlockingReasons) > 0 {
			item.Status = "blocked"
			prediction.Explanations = append(prediction.Explanations, item.BlockingReasons...)
			run.BlockedGPUCount++
		} else {
			probability := scoreShadowModel(*model, features)
			if math.IsNaN(probability) || math.IsInf(probability, 0) {
				return fmt.Errorf("model emitted a non-finite probability for GPU %s", asset.CurrentUUID)
			}
			vectorSHA := shadowFeatureVectorSHA(model.FeatureColumns, features)
			item.Probability, item.FeatureVectorSHA256 = &probability, vectorSHA
			item.PredictedPositive = probability >= *spec.DecisionThreshold
			prediction.Probability, prediction.FeatureVectorSHA = &probability, vectorSHA
			prediction.Status, prediction.RiskLevel = "shadow_below_threshold", "unvalidated"
			prediction.Explanations = api.StringList{"read_only_shadow_prediction", "no_alert_emitted", "no_action_executed"}
			if item.PredictedPositive {
				prediction.Status = "shadow_above_threshold"
				run.PositiveGPUCount++
			}
			run.ScoredGPUCount++
			for _, column := range model.FeatureColumns {
				value := features[column]
				if !math.IsNaN(value) && !math.IsInf(value, 0) {
					liveFeatureValues[column] = append(liveFeatureValues[column], value)
				}
			}
			probabilitySum += probability
			probabilities = append(probabilities, probability)
			nodeCounts[asset.NodeIP]++
			nodeProbabilitySums[asset.NodeIP] += probability
			if item.PredictedPositive {
				nodePositiveCounts[asset.NodeIP]++
			}
			if run.MinimumProbability == nil || probability < *run.MinimumProbability {
				value := probability
				run.MinimumProbability = &value
			}
			if run.MaximumProbability == nil || probability > *run.MaximumProbability {
				value := probability
				run.MaximumProbability = &value
			}
		}
		report.GPUs = append(report.GPUs, item)
		predictions = append(predictions, prediction)
	}
	run.TargetGPUCount = len(assets)
	if run.ScoredGPUCount > 0 {
		value := probabilitySum / float64(run.ScoredGPUCount)
		run.MeanProbability = &value
		run.PositiveRatio = float64(run.PositiveGPUCount) / float64(run.ScoredGPUCount)
		sort.Float64s(probabilities)
		median, p90, p95, p99 := shadowQuantile(probabilities, 0.50), shadowQuantile(probabilities, 0.90), shadowQuantile(probabilities, 0.95), shadowQuantile(probabilities, 0.99)
		run.MedianProbability, run.P90Probability, run.P95Probability, run.P99Probability = &median, &p90, &p95, &p99
	}
	if run.ScoredGPUCount == 0 {
		return fmt.Errorf("all in-scope GPUs were blocked by live feature gates")
	}
	for nodeIP, count := range nodeCounts {
		mean := nodeProbabilitySums[nodeIP] / float64(count)
		if run.MaximumNodeMean == nil || mean > *run.MaximumNodeMean {
			value := mean
			run.MaximumNodeMean = &value
		}
		if count >= 4 && nodePositiveCounts[nodeIP] == count {
			run.AllAboveThresholdNodes++
		}
	}
	run.DistributionStatus = "passed"
	if run.PositiveRatio > maximumShadowPositiveRatio {
		run.DistributionStatus = "review_required"
		run.BlockingReasons = append(run.BlockingReasons, "above_threshold_ratio_exceeds_20_percent")
	}
	if run.MedianProbability != nil && *run.MedianProbability >= *spec.DecisionThreshold*maximumShadowMedianToThreshold {
		run.DistributionStatus = "review_required"
		run.BlockingReasons = append(run.BlockingReasons, "fleet_median_at_or_above_decision_threshold")
	}
	if run.AllAboveThresholdNodes >= 3 {
		run.DistributionStatus = "review_required"
		run.BlockingReasons = append(run.BlockingReasons, "threshold_hits_cluster_across_multiple_whole_nodes")
	}
	report.DistributionStatus = run.DistributionStatus
	report.DistributionReasons = append([]string(nil), run.BlockingReasons...)
	if err := os.MkdirAll(filepath.Join(s.config.DatasetDir, "shadow-scoring", run.RunKey), 0o750); err != nil {
		return err
	}
	reportPath := filepath.Join(s.config.DatasetDir, "shadow-scoring", run.RunKey, "shadow_report.json")
	if err := writeJSONAtomic(reportPath, report); err != nil {
		return err
	}
	reportSHA, err := fileSHA256(reportPath)
	if err != nil {
		return err
	}
	finished := s.now()
	liveSnapshots, err := s.liveShadowFeatureDistributionSnapshots(spec, *run, model.FeatureColumns, liveFeatureValues, finished, reportSHA)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&api.HardwareRiskPrediction{}).Where("model_spec_id = ? AND current = ?", spec.ID, true).Update("current", false).Error; err != nil {
			return err
		}
		if err := tx.CreateInBatches(&predictions, 100).Error; err != nil {
			return err
		}
		if len(liveSnapshots) > 0 {
			if err := tx.CreateInBatches(&liveSnapshots, 100).Error; err != nil {
				return err
			}
		}
		status := "completed"
		if run.DistributionStatus == "review_required" {
			status = "distribution_review_required"
		}
		return tx.Model(run).Updates(map[string]any{"status": status, "target_gpu_count": run.TargetGPUCount, "scored_gpu_count": run.ScoredGPUCount, "blocked_gpu_count": run.BlockedGPUCount, "positive_gpu_count": run.PositiveGPUCount, "positive_ratio": run.PositiveRatio, "minimum_probability": run.MinimumProbability, "maximum_probability": run.MaximumProbability, "mean_probability": run.MeanProbability, "median_probability": run.MedianProbability, "p90_probability": run.P90Probability, "p95_probability": run.P95Probability, "p99_probability": run.P99Probability, "maximum_node_mean": run.MaximumNodeMean, "all_above_threshold_nodes": run.AllAboveThresholdNodes, "distribution_status": run.DistributionStatus, "blocking_reasons": run.BlockingReasons, "no_alert_emitted": true, "no_action_executed": true, "report_path": reportPath, "report_sha256": reportSHA, "finished_at": &finished}).Error
	})
}

func shadowQuantile(sorted []float64, probability float64) float64 {
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

func scoreShadowModel(model logisticModel, features map[string]float64) float64 {
	z := model.Intercept
	for index, column := range model.FeatureColumns {
		value := features[column]
		scale := model.Scales[index]
		if scale < 1e-9 {
			scale = 1
		}
		z += model.Coefficients[index] * ((value - model.Means[index]) / scale)
	}
	raw := sigmoid(z)
	if model.Calibration.Status != "fitted" {
		return raw
	}
	return sigmoid(model.Calibration.Slope*logitProbability(raw) + model.Calibration.Intercept)
}

func shadowFeatureVectorSHA(columns []string, features map[string]float64) string {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for _, column := range columns {
		_ = encoder.Encode(struct {
			Column string  `json:"column"`
			Value  float64 `json:"value"`
		}{column, features[column]})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

package degradation

import (
	"fmt"
	"sort"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

const (
	Version            = "degradation-v0.1.0"
	minimumUtilization = 80.0
	ratioThreshold     = 0.85
	minimumNodePeers   = 3
	minimumModelPeers  = 4
	freshnessSLA       = time.Hour
)

type Service struct {
	db  *storage.DB
	now func() time.Time
}

type observation struct {
	score    api.GPUHealthScore
	snapshot api.GPUFeatureSnapshot
	util     float64
	clock    float64
}

func NewService(db *storage.DB) *Service { return &Service{db: db, now: time.Now} }

func (s *Service) Evaluate() (api.GPUDegradationSummary, []api.GPUDegradationAssessment, error) {
	evaluatedAt := s.now()
	var scores []api.GPUHealthScore
	if err := s.db.Where("current = ? AND feature_snapshot_id > 0", true).Find(&scores).Error; err != nil {
		return api.GPUDegradationSummary{}, nil, err
	}
	snapshotIDs := make([]uint, 0, len(scores))
	for _, score := range scores {
		snapshotIDs = append(snapshotIDs, score.FeatureSnapshotID)
	}
	var snapshots []api.GPUFeatureSnapshot
	if len(snapshotIDs) > 0 {
		if err := s.db.Where("id IN ?", snapshotIDs).Find(&snapshots).Error; err != nil {
			return api.GPUDegradationSummary{}, nil, err
		}
	}
	snapshotByID := make(map[uint]api.GPUFeatureSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		snapshotByID[snapshot.ID] = snapshot
	}

	summary := api.GPUDegradationSummary{
		Version: Version, Mode: "shadow", EvaluatedGPUs: len(scores),
		MinimumUtilization: minimumUtilization, RatioThreshold: ratioThreshold,
		FreshnessSLASeconds: int64(freshnessSLA.Seconds()), ByModel: map[string]int{},
		EvaluatedAt: evaluatedAt,
	}
	eligible := make([]observation, 0, len(scores))
	for _, score := range scores {
		snapshot, ok := snapshotByID[score.FeatureSnapshotID]
		if !ok {
			summary.InsufficientGPUs++
			continue
		}
		if summary.LatestObservedAt == nil || snapshot.ObservedAt.After(*summary.LatestObservedAt) {
			observedAt := snapshot.ObservedAt
			summary.LatestObservedAt = &observedAt
		}
		util, clock := snapshot.Metrics["gpu_util_avg_15m"], snapshot.Metrics["sm_clock_avg_15m"]
		if snapshot.ObservedAt.IsZero() || evaluatedAt.Sub(snapshot.ObservedAt) > freshnessSLA ||
			util < minimumUtilization || clock <= 0 || (score.DataConfidence != "A" && score.DataConfidence != "B") {
			summary.InsufficientGPUs++
			continue
		}
		eligible = append(eligible, observation{score: score, snapshot: snapshot, util: util, clock: clock})
	}
	summary.EligibleGPUs = len(eligible)

	candidates := make([]api.GPUDegradationAssessment, 0)
	for _, item := range eligible {
		peers, scope := peerClocks(item, eligible)
		if len(peers) == 0 {
			continue
		}
		summary.BaselineReadyGPUs++
		baseline := median(peers)
		if baseline <= 0 {
			continue
		}
		ratio := item.clock / baseline
		if ratio >= ratioThreshold {
			continue
		}
		confidence := assessmentConfidence(item.score.DataConfidence, len(peers), scope)
		assessment := api.GPUDegradationAssessment{
			GPUAssetID: item.score.GPUAssetID, GPUUUID: item.score.GPUUUID, NodeIP: item.score.NodeIP,
			GPUIndex: item.score.GPUIndex, ModelName: item.score.ModelName, Status: "candidate",
			Metric: "sm_clock_avg_15m", ObservedValue: item.clock, BaselineValue: baseline,
			PerformanceRatio: ratio, GPUUtilization: item.util, PeerCount: len(peers),
			BaselineScope: scope, DataConfidence: confidence,
			Evidence: api.StringList{
				fmt.Sprintf("GPU util avg 15m %.1f%%", item.util),
				fmt.Sprintf("SM clock avg 15m %.1fMHz vs %.1fMHz peer median", item.clock, baseline),
			},
			RecommendedAction: "Confirm workload comparability, then schedule maintenance-window DCGM/SuperBench validation; do not isolate automatically.",
			FeatureVersion:    item.snapshot.FeatureVersions["sm_clock_avg_15m"],
			ObservedAt:        item.snapshot.ObservedAt, EvaluatedAt: evaluatedAt,
		}
		candidates = append(candidates, assessment)
		summary.ByModel[item.score.ModelName]++
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].PerformanceRatio != candidates[j].PerformanceRatio {
			return candidates[i].PerformanceRatio < candidates[j].PerformanceRatio
		}
		if candidates[i].NodeIP != candidates[j].NodeIP {
			return candidates[i].NodeIP < candidates[j].NodeIP
		}
		return candidates[i].GPUIndex < candidates[j].GPUIndex
	})
	summary.CandidateGPUs = len(candidates)
	return summary, candidates, nil
}

func peerClocks(current observation, all []observation) ([]float64, string) {
	nodePeers := make([]float64, 0)
	for _, peer := range all {
		if peer.score.GPUAssetID != current.score.GPUAssetID && peer.score.NodeIP == current.score.NodeIP && peer.score.ModelName == current.score.ModelName {
			nodePeers = append(nodePeers, peer.clock)
		}
	}
	if len(nodePeers) >= minimumNodePeers {
		return nodePeers, "same_node_model"
	}
	modelPeers := make([]float64, 0)
	for _, peer := range all {
		if peer.score.GPUAssetID != current.score.GPUAssetID && peer.score.ModelName == current.score.ModelName {
			modelPeers = append(modelPeers, peer.clock)
		}
	}
	if len(modelPeers) >= minimumModelPeers {
		return modelPeers, "same_model_fleet"
	}
	return nil, "insufficient_peers"
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

func assessmentConfidence(sourceConfidence string, peerCount int, scope string) string {
	if sourceConfidence != "A" {
		return "C"
	}
	if scope == "same_node_model" && peerCount >= 7 {
		return "A"
	}
	if peerCount >= 4 {
		return "B"
	}
	return "C"
}

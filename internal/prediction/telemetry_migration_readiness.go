package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
)

const (
	TelemetryMigrationReadinessVersion = "prediction-telemetry-migration-readiness-v1"
	telemetryMigrationBurnIn           = 24 * time.Hour
	telemetryMigrationRunLookback      = 500
)

type TelemetryMigrationReadinessReport struct {
	Version              string                             `json:"version"`
	FrameworkVersion     string                             `json:"framework_version"`
	Mode                 string                             `json:"mode"`
	Status               string                             `json:"status"`
	ReportSHA256         string                             `json:"report_sha256"`
	MinimumNodeCoverage  float64                            `json:"minimum_node_coverage"`
	TargetHorizonMinutes int                                `json:"target_horizon_minutes"`
	NegativeCensorHours  int                                `json:"negative_censor_hours"`
	BurnInHours          int                                `json:"burn_in_hours"`
	Scopes               []TelemetryMigrationScopeReadiness `json:"scopes"`
	Safety               ObservabilityRankingSafety         `json:"safety"`
	BlockingReasons      []string                           `json:"blocking_reasons"`
	Interpretation       []string                           `json:"interpretation"`
	RecommendedNextRun   []string                           `json:"recommended_next_run"`
	GeneratedAt          time.Time                          `json:"generated_at"`
}

type TelemetryMigrationScopeReadiness struct {
	Scope                      string     `json:"scope"`
	DataCenterID               string     `json:"data_center_id,omitempty"`
	Status                     string     `json:"status"`
	LatestRunID                uint       `json:"latest_run_id,omitempty"`
	LatestFinishedAt           *time.Time `json:"latest_finished_at,omitempty"`
	LatestGPUCount             int        `json:"latest_gpu_count"`
	LatestNodeCount            int        `json:"latest_node_count"`
	LatestScoredGPUCount       int        `json:"latest_scored_gpu_count"`
	LatestScoredNodeCount      int        `json:"latest_scored_node_count"`
	LatestGPUScoreCoverage     float64    `json:"latest_gpu_score_coverage"`
	LatestNodeScoreCoverage    float64    `json:"latest_node_score_coverage"`
	FirstAdequateRunID         uint       `json:"first_adequate_run_id,omitempty"`
	FirstAdequateFinishedAt    *time.Time `json:"first_adequate_finished_at,omitempty"`
	LatestAdequateRunID        uint       `json:"latest_adequate_run_id,omitempty"`
	LatestAdequateFinishedAt   *time.Time `json:"latest_adequate_finished_at,omitempty"`
	BurnInReadyAt              *time.Time `json:"burn_in_ready_at,omitempty"`
	FirstOutcomeWindowEndAt    *time.Time `json:"first_outcome_window_end_at,omitempty"`
	FirstFixedReplayReadyAt    *time.Time `json:"first_fixed_replay_ready_at,omitempty"`
	HoursUntilBurnInReady      *float64   `json:"hours_until_burn_in_ready,omitempty"`
	HoursUntilFirstReplayReady *float64   `json:"hours_until_first_replay_ready,omitempty"`
	BlockingReasons            []string   `json:"blocking_reasons"`
	RecommendedNextRun         []string   `json:"recommended_next_run"`
}

type telemetryMigrationRunScopeStats struct {
	runID       uint
	finishedAt  *time.Time
	gpuCount    int
	nodeCount   int
	scoredGPU   int
	seenNodes   map[string]struct{}
	scoredNodes map[string]struct{}
}

func (s *Service) TelemetryMigrationReadinessReport() (TelemetryMigrationReadinessReport, error) {
	now := s.now()
	report := TelemetryMigrationReadinessReport{
		Version:              TelemetryMigrationReadinessVersion,
		FrameworkVersion:     FrameworkVersion,
		Mode:                 "read_only_new_fleet_prediction_readiness",
		Status:               "blocked_no_current_health_run",
		MinimumNodeCoverage:  observabilityRankingMinimumCoverage,
		TargetHorizonMinutes: int(observabilityRankingHorizon / time.Minute),
		NegativeCensorHours:  int(observabilityRankingNegativeCensor / time.Hour),
		BurnInHours:          int(telemetryMigrationBurnIn / time.Hour),
		Scopes:               []TelemetryMigrationScopeReadiness{},
		Safety:               ObservabilityRankingSafety{ReadOnlyShadow: true, NoAlertEmitted: true, NoActionExecuted: true, ModelIndependent: true},
		Interpretation: []string{
			"planned migration should be evaluated by the intended monitored scope, not by treating old-room telemetry loss as a hardware signal",
			"coverage readiness does not relax the existing 80% coverage, unique-entity, temporal-consistency or calibration gates",
			"new scopes can produce a burn-in health-quality result after 24 hours of adequate telemetry, but seven-day ranking outcomes still require a full horizon plus censoring",
			"data_center_id is inferred from current unambiguous LXOP infrastructure assets by node IP; ambiguous or missing mappings are reported as data_center_unknown",
		},
		RecommendedNextRun: []string{
			"deploy collectors in the new scope, wait for an adequate health run, then use this report to schedule the first fixed-time replay",
			"archive the first adequate run ID and its fixed replay readiness time before evaluating ranking policies",
		},
		GeneratedAt: now,
	}
	var runs []api.HealthEvaluationRun
	if err := s.db.Where("status = ? AND finished_at IS NOT NULL", "success").Order("finished_at DESC, id DESC").Limit(telemetryMigrationRunLookback).Find(&runs).Error; err != nil {
		return report, err
	}
	if len(runs) == 0 {
		report.BlockingReasons = []string{"no successful health evaluation run exists"}
		report.ReportSHA256 = telemetryMigrationReadinessChecksum(report)
		return report, nil
	}
	runIDs := make([]uint, 0, len(runs))
	runByID := make(map[uint]api.HealthEvaluationRun, len(runs))
	for _, run := range runs {
		runIDs = append(runIDs, run.ID)
		runByID[run.ID] = run
	}
	var snapshots []api.GPUFeatureSnapshot
	if err := s.db.Where("evaluation_run_id IN ?", runIDs).Order("evaluation_run_id DESC, node_ip ASC, gpu_index ASC, id ASC").Find(&snapshots).Error; err != nil {
		return report, err
	}
	nodeToDataCenter, err := s.currentDataCentersByNode(snapshots)
	if err != nil {
		return report, err
	}
	statsByScope := map[string][]telemetryMigrationRunScopeStats{}
	indexByScopeRun := map[string]map[uint]int{}
	ensureStats := func(scope string, run api.HealthEvaluationRun) *telemetryMigrationRunScopeStats {
		if indexByScopeRun[scope] == nil {
			indexByScopeRun[scope] = map[uint]int{}
		}
		if index, ok := indexByScopeRun[scope][run.ID]; ok {
			return &statsByScope[scope][index]
		}
		statsByScope[scope] = append(statsByScope[scope], telemetryMigrationRunScopeStats{runID: run.ID, finishedAt: run.FinishedAt, seenNodes: map[string]struct{}{}, scoredNodes: map[string]struct{}{}})
		index := len(statsByScope[scope]) - 1
		indexByScopeRun[scope][run.ID] = index
		return &statsByScope[scope][index]
	}
	for _, snapshot := range snapshots {
		run, ok := runByID[snapshot.EvaluationRunID]
		if !ok {
			continue
		}
		node := strings.TrimSpace(snapshot.NodeIP)
		if node == "" {
			continue
		}
		dataCenter := strings.TrimSpace(nodeToDataCenter[node])
		scope := "data_center_unknown"
		if dataCenter != "" {
			scope = "data_center:" + dataCenter
		}
		for _, key := range []string{"all", scope} {
			stats := ensureStats(key, run)
			stats.gpuCount++
			if _, exists := stats.seenNodes[node]; !exists {
				stats.seenNodes[node] = struct{}{}
				stats.nodeCount++
			}
			if _, valid := validNonnegativeMetric(snapshot.Metrics, observabilityMaxGapMetric); valid {
				stats.scoredGPU++
				stats.scoredNodes[node] = struct{}{}
			}
		}
	}
	for scope := range statsByScope {
		sort.SliceStable(statsByScope[scope], func(i, j int) bool {
			left, right := statsByScope[scope][i], statsByScope[scope][j]
			if left.finishedAt != nil && right.finishedAt != nil && !left.finishedAt.Equal(*right.finishedAt) {
				return left.finishedAt.After(*right.finishedAt)
			}
			return left.runID > right.runID
		})
	}
	for _, scope := range sortedTelemetryScopes(statsByScope) {
		report.Scopes = append(report.Scopes, telemetryMigrationScopeReadiness(scope, statsByScope[scope], now))
	}
	report.Status, report.BlockingReasons = telemetryMigrationReportStatus(report.Scopes)
	report.ReportSHA256 = telemetryMigrationReadinessChecksum(report)
	return report, nil
}

func (s *Service) currentDataCentersByNode(snapshots []api.GPUFeatureSnapshot) (map[string]string, error) {
	nodeSet := map[string]struct{}{}
	for _, snapshot := range snapshots {
		if node := strings.TrimSpace(snapshot.NodeIP); node != "" {
			nodeSet[node] = struct{}{}
		}
	}
	if len(nodeSet) == 0 {
		return map[string]string{}, nil
	}
	nodes := make([]string, 0, len(nodeSet))
	for node := range nodeSet {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	var assets []api.InfrastructureAsset
	if err := s.db.Where("ip_address IN ? AND present = ? AND in_use = ?", nodes, true, true).Order("ip_address ASC, source ASC, id ASC").Find(&assets).Error; err != nil {
		return nil, err
	}
	return unambiguousPredictionDataCentersByNode(assets), nil
}

func unambiguousPredictionDataCentersByNode(assets []api.InfrastructureAsset) map[string]string {
	type state struct {
		value     string
		ambiguous bool
	}
	states := map[string]state{}
	for _, asset := range assets {
		node := strings.TrimSpace(asset.IPAddress)
		dc := strings.TrimSpace(asset.DataCenterID)
		if node == "" || dc == "" {
			continue
		}
		item := states[node]
		if item.value == "" {
			item.value = dc
		} else if item.value != dc {
			item.ambiguous = true
		}
		states[node] = item
	}
	result := map[string]string{}
	for node, item := range states {
		if !item.ambiguous {
			result[node] = item.value
		}
	}
	return result
}

func sortedTelemetryScopes(statsByScope map[string][]telemetryMigrationRunScopeStats) []string {
	scopes := make([]string, 0, len(statsByScope))
	for scope, rows := range statsByScope {
		if len(rows) > 0 {
			scopes = append(scopes, scope)
		}
	}
	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i] == "all" {
			return true
		}
		if scopes[j] == "all" {
			return false
		}
		if scopes[i] == "data_center_unknown" {
			return false
		}
		if scopes[j] == "data_center_unknown" {
			return true
		}
		return scopes[i] < scopes[j]
	})
	return scopes
}

func telemetryMigrationScopeReadiness(scope string, rows []telemetryMigrationRunScopeStats, now time.Time) TelemetryMigrationScopeReadiness {
	result := TelemetryMigrationScopeReadiness{Scope: scope, BlockingReasons: []string{}, RecommendedNextRun: []string{}}
	if strings.HasPrefix(scope, "data_center:") {
		result.DataCenterID = strings.TrimPrefix(scope, "data_center:")
	}
	if len(rows) == 0 {
		result.Status = "blocked_no_scope_health_runs"
		result.BlockingReasons = []string{"no health-run snapshots exist for this scope"}
		return result
	}
	latest := rows[0]
	result.LatestRunID = latest.runID
	result.LatestFinishedAt = latest.finishedAt
	result.LatestGPUCount = latest.gpuCount
	result.LatestNodeCount = latest.nodeCount
	result.LatestScoredGPUCount = latest.scoredGPU
	result.LatestScoredNodeCount = len(latest.scoredNodes)
	if result.LatestGPUCount > 0 {
		result.LatestGPUScoreCoverage = float64(result.LatestScoredGPUCount) / float64(result.LatestGPUCount)
	}
	if result.LatestNodeCount > 0 {
		result.LatestNodeScoreCoverage = float64(result.LatestScoredNodeCount) / float64(result.LatestNodeCount)
	}
	for index := len(rows) - 1; index >= 0; index-- {
		row := rows[index]
		if !telemetryMigrationAdequate(row) {
			continue
		}
		if result.FirstAdequateRunID == 0 {
			result.FirstAdequateRunID = row.runID
			result.FirstAdequateFinishedAt = row.finishedAt
		}
	}
	for _, row := range rows {
		if telemetryMigrationAdequate(row) {
			result.LatestAdequateRunID = row.runID
			result.LatestAdequateFinishedAt = row.finishedAt
			break
		}
	}
	if result.FirstAdequateFinishedAt != nil {
		burnInReady := result.FirstAdequateFinishedAt.Add(telemetryMigrationBurnIn)
		outcomeEnd := result.FirstAdequateFinishedAt.Add(observabilityRankingHorizon)
		replayReady := outcomeEnd.Add(observabilityRankingNegativeCensor)
		result.BurnInReadyAt = &burnInReady
		result.FirstOutcomeWindowEndAt = &outcomeEnd
		result.FirstFixedReplayReadyAt = &replayReady
		if now.Before(burnInReady) {
			hours := burnInReady.Sub(now).Hours()
			result.HoursUntilBurnInReady = &hours
		}
		if now.Before(replayReady) {
			hours := replayReady.Sub(now).Hours()
			result.HoursUntilFirstReplayReady = &hours
		}
	}
	result.Status, result.BlockingReasons, result.RecommendedNextRun = telemetryMigrationScopeStatus(result, now)
	return result
}

func telemetryMigrationAdequate(row telemetryMigrationRunScopeStats) bool {
	if row.nodeCount == 0 {
		return false
	}
	return float64(len(row.scoredNodes))/float64(row.nodeCount) >= observabilityRankingMinimumCoverage
}

func telemetryMigrationScopeStatus(scope TelemetryMigrationScopeReadiness, now time.Time) (string, []string, []string) {
	if scope.LatestNodeCount == 0 {
		return "blocked_no_current_scope_assets", []string{"latest health run has no nodes in this scope"}, []string{"verify the intended monitored fleet scope and LXOP data_center_id mapping"}
	}
	if scope.LatestNodeScoreCoverage < observabilityRankingMinimumCoverage {
		return "blocked_low_current_coverage", []string{"latest node score coverage is below 80%"}, []string{"restore or deploy collectors for this scope until node score coverage is at least 80%"}
	}
	if scope.FirstAdequateRunID == 0 || scope.FirstAdequateFinishedAt == nil {
		return "collecting_no_adequate_run", []string{"no adequate health run was found in the recent lookback window"}, []string{"wait for the first successful health run with at least 80% node score coverage"}
	}
	if scope.BurnInReadyAt != nil && now.Before(*scope.BurnInReadyAt) {
		return "warming_24h_burn_in", []string{"the first adequate run exists, but the 24-hour burn-in window has not elapsed"}, []string{"continue read-only health runs until the burn-in window completes"}
	}
	if scope.FirstFixedReplayReadyAt != nil && now.Before(*scope.FirstFixedReplayReadyAt) {
		return "collecting_7d_outcome", []string{"the first adequate run has not completed the seven-day outcome horizon plus 24-hour censoring"}, []string{"schedule the first fixed-time observability replay at first_fixed_replay_ready_at"}
	}
	return "ready_for_first_fixed_replay", []string{}, []string{"run observability-ranking-validation with as_of at or after first_fixed_replay_ready_at and archive the SHA-bound report"}
}

func telemetryMigrationReportStatus(scopes []TelemetryMigrationScopeReadiness) (string, []string) {
	if len(scopes) == 0 {
		return "blocked_no_current_health_run", []string{"no scoped health-run snapshots are available"}
	}
	ready, collecting := 0, 0
	for _, scope := range scopes {
		if scope.Status == "ready_for_first_fixed_replay" {
			ready++
		}
		if strings.HasPrefix(scope.Status, "collecting") || strings.HasPrefix(scope.Status, "warming") {
			collecting++
		}
	}
	if ready > 0 {
		return "ready", []string{}
	}
	if collecting > 0 {
		return "collecting", []string{"at least one scope has adequate telemetry but is still waiting for burn-in or outcome maturity"}
	}
	return "blocked", []string{"all current scopes are blocked before outcome collection"}
}

func telemetryMigrationReadinessChecksum(report TelemetryMigrationReadinessReport) string {
	fingerprint := report
	fingerprint.ReportSHA256 = ""
	fingerprint.GeneratedAt = time.Time{}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

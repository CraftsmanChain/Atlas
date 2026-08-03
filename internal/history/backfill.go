package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	promclient "atlas/internal/prometheus"
	"atlas/pkg/api"
	"atlas/pkg/config"
)

const (
	alertBackfillQueryVersion = "gpu-fault-signal-onset-v2"
	alertOnsetQuery           = `(ALERTS{alert_template=~"XID故障(|-低优先级|-高优先级)|GPU掉卡",alertstate="firing"} == 1) unless (ALERTS{alert_template=~"XID故障(|-低优先级|-高优先级)|GPU掉卡",alertstate="firing"} offset 5m)`
	uncorrectableRowsQuery    = `(DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS > 0) and (DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS > DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS offset 5m)`
)

var historicalSignalScans = []struct {
	SourceMetric string
	Query        string
}{
	{SourceMetric: "ALERTS", Query: alertOnsetQuery},
	{SourceMetric: "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", Query: uncorrectableRowsQuery},
}

type BackfillRequest struct {
	SourceKey string     `json:"source_key"`
	Start     *time.Time `json:"start,omitempty"`
	End       *time.Time `json:"end,omitempty"`
}

type CandidateReviewRequest struct {
	Status     string `json:"status"`
	Note       string `json:"note"`
	ReviewedBy string `json:"reviewed_by"`
}

type CandidateSummary struct {
	Total          int            `json:"total"`
	Pending        int            `json:"pending_review"`
	ByReview       map[string]int `json:"by_review_status"`
	ByRuleDecision map[string]int `json:"by_rule_decision"`
	ByEventCode    map[string]int `json:"by_event_code"`
	ByQuality      map[string]int `json:"by_quality_tier"`
	ByPriority     map[string]int `json:"by_operational_priority"`
	ByCertainty    map[string]int `json:"by_hardware_certainty"`
	ByDisposition  map[string]int `json:"by_training_disposition"`
	ByModel        map[string]int `json:"by_model"`
	LatestOnset    *time.Time     `json:"latest_onset_at,omitempty"`
	EarliestOnset  *time.Time     `json:"earliest_onset_at,omitempty"`
}

type TrainingCohortPolicy struct {
	Version                   string   `json:"version"`
	PositiveHorizonsMinutes   []int    `json:"positive_horizons_minutes"`
	HealthyCensorBeforeHours  int      `json:"healthy_censor_before_hours"`
	HealthyCensorAfterHours   int      `json:"healthy_censor_after_hours"`
	ControlMatchDimensions    []string `json:"control_match_dimensions"`
	NormalRangeStatistics     []string `json:"normal_range_statistics"`
	PositiveCandidatePolicy   string   `json:"positive_candidate_policy"`
	HealthyWindowPolicy       string   `json:"healthy_window_policy"`
	ReplacementEvidencePolicy string   `json:"replacement_evidence_policy"`
}

type onsetSignal struct {
	Labels       map[string]string
	SourceMetric string
	At           time.Time
}

type signalClassification struct {
	EventType           string
	EventCode           string
	QualityTier         string
	OperationalPriority string
	HardwareCertainty   string
	TrainingDisposition string
	RecommendedAction   string
	RecoveryAware       bool
}

func CurrentTrainingCohortPolicy() TrainingCohortPolicy {
	return TrainingCohortPolicy{
		Version: "gpu-training-cohort-v2",
		PositiveHorizonsMinutes: []int{
			1, 10, 30, 60, 180, 360, 720, 1440, 2880, 4320, 10080,
		},
		HealthyCensorBeforeHours: 168,
		HealthyCensorAfterHours:  72,
		ControlMatchDimensions: []string{
			"model_name", "data_center_id", "load_bucket", "driver_version", "telemetry_coverage",
		},
		NormalRangeStatistics: []string{
			"median", "MAD", "p01", "p05", "p95", "p99", "empirical_CDF",
		},
		PositiveCandidatePolicy:   "deterministic hardware and contained ECC signals may become proxies after identity review; XID 109 remains operationally high priority but requires corroborating hardware or operator evidence for training",
		HealthyWindowPolicy:       "exclude windows near high-risk/deterministic faults, telemetry gaps, node restarts, maintenance, and GPU identity changes",
		ReplacementEvidencePolicy: "same stable node identity and PCI slot changing UUID is supporting evidence only; require non-overlap, persistence, and exclusion of whole-node identity/topology changes",
	}
}

func (s *Service) BackfillRuns(limit int) ([]api.HistoryBackfillRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.HistoryBackfillRun
	err := s.db.Where("job_type = ?", "gpu_fault_signal_onset").
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) Candidates(limit int) (CandidateSummary, []api.HistoricalFaultCandidate, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var all []api.HistoricalFaultCandidate
	if err := s.db.Order("onset_at DESC, id DESC").Find(&all).Error; err != nil {
		return CandidateSummary{}, nil, err
	}
	summary := CandidateSummary{
		Total: len(all), ByReview: map[string]int{}, ByRuleDecision: map[string]int{},
		ByEventCode: map[string]int{}, ByQuality: map[string]int{},
		ByPriority: map[string]int{}, ByCertainty: map[string]int{},
		ByDisposition: map[string]int{}, ByModel: map[string]int{},
	}
	for _, row := range all {
		if row.ReviewStatus == "pending_review" || row.ReviewStatus == "needs_evidence" ||
			row.ReviewStatus == "needs_human_review" {
			summary.Pending++
		}
		summary.ByReview[row.ReviewStatus]++
		summary.ByRuleDecision[row.RuleDecision]++
		summary.ByEventCode[row.EventCode]++
		summary.ByQuality[row.QualityTier]++
		summary.ByPriority[row.OperationalPriority]++
		summary.ByCertainty[row.HardwareCertainty]++
		summary.ByDisposition[row.TrainingDisposition]++
		summary.ByModel[row.ModelName]++
		if summary.EarliestOnset == nil || row.OnsetAt.Before(*summary.EarliestOnset) {
			value := row.OnsetAt
			summary.EarliestOnset = &value
		}
		if summary.LatestOnset == nil || row.OnsetAt.After(*summary.LatestOnset) {
			value := row.OnsetAt
			summary.LatestOnset = &value
		}
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return summary, all, nil
}

func (s *Service) ReviewCandidate(id uint, request CandidateReviewRequest) (api.HistoricalFaultCandidate, error) {
	var candidate api.HistoricalFaultCandidate
	if id == 0 {
		return candidate, fmt.Errorf("candidate id is required")
	}
	if err := s.db.First(&candidate, id).Error; err != nil {
		return candidate, err
	}
	status := strings.ToLower(strings.TrimSpace(request.Status))
	note := strings.TrimSpace(request.Note)
	reviewedBy := strings.TrimSpace(request.ReviewedBy)
	if reviewedBy == "" {
		reviewedBy = "operator"
	}
	if status != "needs_evidence" && note == "" {
		return candidate, fmt.Errorf("review note is required for status %q", status)
	}
	classification := classifySignal(candidate.Labels, candidate.SourceMetric)
	updates := map[string]any{
		"review_status": status, "review_note": note, "reviewed_by": reviewedBy,
		"reviewed_at": s.now(), "quality_tier": classification.QualityTier,
		"training_disposition": classification.TrainingDisposition,
	}
	switch status {
	case "accepted_proxy":
		// Preserve the rule-derived quality tier. Acceptance permits dataset
		// construction but does not upgrade monitoring evidence to confirmed.
	case "context_only":
		updates["quality_tier"] = "weak_proxy"
		updates["training_disposition"] = "context_only"
	case "needs_evidence":
		updates["training_disposition"] = "pending_review"
	case "excluded":
		updates["quality_tier"] = "excluded"
		updates["training_disposition"] = "excluded"
	default:
		return candidate, fmt.Errorf("unsupported review status %q", status)
	}
	if err := s.db.Model(&candidate).Updates(updates).Error; err != nil {
		return candidate, err
	}
	if err := s.db.First(&candidate, id).Error; err != nil {
		return candidate, err
	}
	return candidate, nil
}

func (s *Service) StartAlertBackfill(request BackfillRequest) (api.HistoryBackfillRun, error) {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	if s.backfillRunning {
		return api.HistoryBackfillRun{}, fmt.Errorf("a historical backfill is already running")
	}
	source, err := s.resolveSource(request.SourceKey)
	if err != nil {
		return api.HistoryBackfillRun{}, err
	}
	end := s.now()
	if request.End != nil && !request.End.IsZero() {
		end = *request.End
	}
	end = end.UTC().Truncate(time.Minute)
	start := end.Add(-30 * 24 * time.Hour)
	if request.Start != nil && !request.Start.IsZero() {
		start = request.Start.UTC().Truncate(time.Minute)
	} else {
		var audit api.MonitoringHistoryAudit
		result := s.db.Where("source_key = ? AND earliest_sample_at IS NOT NULL", source.ID).
			Order("finished_at DESC, id DESC").Limit(1).Find(&audit)
		if result.Error != nil {
			return api.HistoryBackfillRun{}, result.Error
		}
		if result.RowsAffected > 0 && audit.EarliestSampleAt != nil {
			value := audit.EarliestSampleAt.UTC()
			start = time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
		}
	}
	if !start.Before(end) {
		return api.HistoryBackfillRun{}, fmt.Errorf("backfill start must be before end")
	}
	const chunk = 7 * 24 * time.Hour
	chunks := int((end.Sub(start) + chunk - 1) / chunk)
	run := api.HistoryBackfillRun{
		SourceKey: source.ID, JobType: "gpu_fault_signal_onset", Status: "queued",
		QueryVersion: alertBackfillQueryVersion, RangeStart: start, RangeEnd: end,
		StepSeconds: 60, ChunkHours: 168, ChunksTotal: chunks, StartedAt: s.now(),
	}
	if err := s.db.Create(&run).Error; err != nil {
		return api.HistoryBackfillRun{}, err
	}
	s.backfillRunning = true
	go s.executeAlertBackfill(run.ID, source)
	return run, nil
}

func (s *Service) executeAlertBackfill(runID uint, source config.HistorySourceConfig) {
	defer func() {
		s.backfillMu.Lock()
		s.backfillRunning = false
		s.backfillMu.Unlock()
	}()
	var run api.HistoryBackfillRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return
	}
	if err := s.db.Model(&run).Update("status", "running").Error; err != nil {
		return
	}
	client, err := s.historyClient(source)
	if err != nil {
		s.failBackfill(&run, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	signals := []onsetSignal{}
	chunk := time.Duration(run.ChunkHours) * time.Hour
	for cursor := run.RangeStart; cursor.Before(run.RangeEnd); {
		chunkEnd := cursor.Add(chunk)
		if chunkEnd.After(run.RangeEnd) {
			chunkEnd = run.RangeEnd
		}
		for _, scan := range historicalSignalScans {
			series, queryErr := client.QueryRange(ctx, scan.Query, cursor, chunkEnd, time.Duration(run.StepSeconds)*time.Second)
			if queryErr != nil {
				s.failBackfill(&run, fmt.Errorf("%s chunk %s..%s: %w", scan.SourceMetric, cursor.Format(time.RFC3339), chunkEnd.Format(time.RFC3339), queryErr))
				return
			}
			for _, item := range series {
				run.SeriesScanned++
				for _, point := range item.Values {
					if point.Value <= 0 {
						continue
					}
					signals = append(signals, onsetSignal{
						Labels: item.Metric, SourceMetric: scan.SourceMetric, At: point.Timestamp,
					})
					run.SignalPoints++
				}
			}
		}
		run.ChunksCompleted++
		if err := s.db.Model(&run).Updates(map[string]any{
			"chunks_completed": run.ChunksCompleted, "series_scanned": run.SeriesScanned,
			"signal_points": run.SignalPoints,
		}).Error; err != nil {
			s.failBackfill(&run, err)
			return
		}
		cursor = chunkEnd
	}
	candidates := buildCandidates(source.ID, run.ID, signals)
	for _, candidate := range candidates {
		var existing api.HistoricalFaultCandidate
		result := s.db.Where("candidate_key = ?", candidate.CandidateKey).Limit(1).Find(&existing)
		if result.Error != nil {
			s.failBackfill(&run, result.Error)
			return
		}
		if result.RowsAffected == 0 {
			if err := s.db.Create(&candidate).Error; err != nil {
				s.failBackfill(&run, err)
				return
			}
			run.CandidatesCreated++
			continue
		}
		if err := s.db.Model(&existing).Updates(map[string]any{
			"backfill_run_id": run.ID, "signal_samples": candidate.SignalSamples,
			"detection_window_end_at": candidate.DetectionWindowEndAt, "labels": candidate.Labels,
			"gpu_uuid": candidate.GPUUUID, "node_ip": candidate.NodeIP,
			"hostname": candidate.Hostname, "model_name": candidate.ModelName,
			"pci_bus_id": candidate.PCIBusID,
			"event_type": candidate.EventType, "event_code": candidate.EventCode,
			"severity": candidate.Severity, "quality_tier": candidate.QualityTier,
			"operational_priority": candidate.OperationalPriority,
			"hardware_certainty":   candidate.HardwareCertainty,
			"training_disposition": candidate.TrainingDisposition,
			"recommended_action":   candidate.RecommendedAction,
			"recovery_aware":       candidate.RecoveryAware, "source_metric": candidate.SourceMetric,
		}).Error; err != nil {
			s.failBackfill(&run, err)
			return
		}
		run.CandidatesUpdated++
	}
	finished := s.now()
	_ = s.db.Model(&run).Updates(map[string]any{
		"status": "completed", "candidates_created": run.CandidatesCreated,
		"candidates_updated": run.CandidatesUpdated, "finished_at": &finished,
	}).Error
}

func (s *Service) failBackfill(run *api.HistoryBackfillRun, err error) {
	finished := s.now()
	_ = s.db.Model(run).Updates(map[string]any{
		"status": "failed", "error_message": err.Error(), "finished_at": &finished,
		"chunks_completed": run.ChunksCompleted, "series_scanned": run.SeriesScanned,
		"signal_points": run.SignalPoints, "records_created": run.RecordsCreated,
		"records_updated": run.RecordsUpdated, "records_annotated": run.RecordsAnnotated,
	}).Error
}

func (s *Service) resolveSource(sourceKey string) (config.HistorySourceConfig, error) {
	sourceKey = strings.TrimSpace(sourceKey)
	for _, source := range s.config.Sources {
		if !source.Enabled {
			continue
		}
		if sourceKey == "" || source.ID == sourceKey {
			return source, nil
		}
	}
	return config.HistorySourceConfig{}, fmt.Errorf("enabled history source %q not found", sourceKey)
}

func (s *Service) historyClient(source config.HistorySourceConfig) (*promclient.Client, error) {
	if source.AuthType != "" && source.AuthType != "none" {
		return nil, fmt.Errorf("authenticated history sources are not enabled in the first backfill adapter")
	}
	baseURL := strings.TrimRight(source.BaseURL, "/")
	if source.Type == "victoriametrics-cluster" {
		baseURL += "/select/" + source.TenantID + "/prometheus"
	}
	return promclient.NewClient(baseURL, s.timeout)
}

func buildCandidates(sourceKey string, runID uint, signals []onsetSignal) []api.HistoricalFaultCandidate {
	sort.Slice(signals, func(i, j int) bool {
		left, right := signalSignature(signals[i].Labels), signalSignature(signals[j].Labels)
		if left != right {
			return left < right
		}
		return signals[i].At.Before(signals[j].At)
	})
	result := []api.HistoricalFaultCandidate{}
	for index := 0; index < len(signals); {
		first := signals[index]
		signature := signalSignature(first.Labels)
		end := first.At
		samples := 1
		lastSampleAt := first.At
		index++
		for index < len(signals) && signalSignature(signals[index].Labels) == signature &&
			!signals[index].At.After(first.At.Add(time.Hour)) {
			if signals[index].At.After(end) {
				end = signals[index].At
			}
			if !signals[index].At.Equal(lastSampleAt) {
				samples++
				lastSampleAt = signals[index].At
			}
			index++
		}
		labels := api.StringMap{}
		for key, value := range first.Labels {
			if key != "__name__" {
				labels[key] = value
			}
		}
		classification := classifySignal(labels, first.SourceMetric)
		uuid := normalizeHistoricalGPUUUID(firstNonEmpty(labels["UUID"], labels["uuid"]))
		candidate := api.HistoricalFaultCandidate{
			SourceKey: sourceKey, BackfillRunID: runID, EntityType: "gpu",
			GPUUUID: uuid, NodeIP: normalizeInstance(firstNonEmpty(labels["host_ip"], labels["instance"])),
			Hostname:  firstNonEmpty(labels["Hostname"], labels["hostname"], labels["node"]),
			ModelName: firstNonEmpty(labels["modelName"], labels["device_type"], labels["model"]),
			PCIBusID:  labels["pci_bus_id"], EventType: classification.EventType,
			EventCode: classification.EventCode, EventMessage: labels["err_msg"],
			Severity: labels["severity"], QualityTier: classification.QualityTier,
			OperationalPriority: classification.OperationalPriority,
			HardwareCertainty:   classification.HardwareCertainty,
			TrainingDisposition: classification.TrainingDisposition,
			RecommendedAction:   classification.RecommendedAction,
			RecoveryAware:       classification.RecoveryAware,
			ReviewStatus:        "pending_review", SourceMetric: first.SourceMetric,
			SourceAlertName: labels["alertname"], SignalSamples: samples, Labels: labels,
			OnsetAt: first.At, DetectionWindowEndAt: end,
		}
		candidate.CandidateKey = candidateKey(sourceKey, signature, first.At)
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].OnsetAt.Before(result[j].OnsetAt) })
	return result
}

func signalSignature(labels map[string]string) string {
	entityKey := firstNonEmpty(labels["UUID"], labels["uuid"])
	if entityKey == "" {
		entityKey = firstNonEmpty(labels["host_id"], labels["sn"], labels["host_ip"],
			normalizeInstance(labels["instance"]), labels["Hostname"], labels["hostname"], labels["node"])
	}
	parts := []string{
		entityKey,
		firstNonEmpty(labels["err_code"], labels["Xid"]),
		labels["alertname"],
	}
	if metric := labels["__name__"]; metric != "" && metric != "ALERTS" {
		parts = append(parts, metric)
	}
	return strings.Join(parts, "|")
}

func candidateKey(sourceKey, signature string, onset time.Time) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		sourceKey, signature, strconv.FormatInt(onset.Unix(), 10),
	}, "|")))
	return hex.EncodeToString(sum[:])
}

func classifySignal(labels map[string]string, sourceMetric string) signalClassification {
	if sourceMetric == "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS" {
		return signalClassification{
			EventType: "uncorrectable_remapped_rows", EventCode: "uncorrectable_remapped_rows",
			QualityTier: "strong_proxy", OperationalPriority: "critical",
			HardwareCertainty:   "deterministic_hardware",
			TrainingDisposition: "positive_after_identity_review",
			RecommendedAction:   "stop scheduling, preserve evidence, and arrange offline GPU memory inspection or replacement",
		}
	}
	template := labels["alert_template"]
	code := firstNonEmpty(labels["err_code"], labels["Xid"])
	switch template {
	case "GPU掉卡":
		return signalClassification{
			EventType: "gpu_dropout", EventCode: "gpu_dropout", QualityTier: "strong_proxy",
			OperationalPriority: "critical", HardwareCertainty: "deterministic_hardware",
			TrainingDisposition: "positive_after_identity_review",
			RecommendedAction:   "stop scheduling, verify GPU identity and PCIe state, then arrange offline inspection",
		}
	case "XID故障":
		return signalClassification{
			EventType: "xid_120_154_recovery_latch", EventCode: firstNonEmpty(code, "120|154"),
			QualityTier: "strong_proxy", OperationalPriority: "critical",
			HardwareCertainty:   "high_risk_operational_signal",
			TrainingDisposition: "proxy_positive_after_review",
			RecommendedAction:   "reboot to recover; if the latch remains active, stop the node and inspect offline",
			RecoveryAware:       true,
		}
	case "XID故障-低优先级":
		return lowPriorityXID(code)
	case "XID故障-高优先级":
		return highPriorityXID(code)
	}
	if isLowPriorityXID(code) {
		return lowPriorityXID(code)
	}
	return highPriorityXID(code)
}

func lowPriorityXID(code string) signalClassification {
	return signalClassification{
		EventType: "xid_" + firstNonEmpty(code, "unknown"), EventCode: code,
		QualityTier: "weak_proxy", OperationalPriority: "low",
		HardwareCertainty:   "operational_signal",
		TrainingDisposition: "context_only",
		RecommendedAction:   "retry the workload; investigate only when accompanied by other abnormal evidence",
	}
}

func highPriorityXID(code string) signalClassification {
	eventType := "xid_" + firstNonEmpty(code, "unknown")
	switch code {
	case "79":
		eventType = "xid_79_gpu_fallen_off_bus"
	case "94":
		eventType = "xid_94_contained_ecc"
	case "109":
		eventType = "xid_109_context_switch_timeout"
	}
	return signalClassification{
		EventType: eventType, EventCode: code, QualityTier: "strong_proxy",
		OperationalPriority: "high", HardwareCertainty: "investigation_required",
		TrainingDisposition: "proxy_positive_after_review",
		RecommendedAction:   "confirm root cause and reboot to restore scheduling; repeated events require offline inspection",
	}
}

func isLowPriorityXID(code string) bool {
	switch code {
	case "0", "13", "31", "43", "45":
		return true
	default:
		return false
	}
}

func normalizeInstance(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

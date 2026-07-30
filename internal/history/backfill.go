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
	alertBackfillQueryVersion = "gpu-alert-onset-v1"
	alertOnsetQuery           = `(ALERTS{UUID!="",alertstate="firing"} == 1) unless (ALERTS{UUID!="",alertstate="firing"} offset 5m)`
)

type BackfillRequest struct {
	SourceKey string     `json:"source_key"`
	Start     *time.Time `json:"start,omitempty"`
	End       *time.Time `json:"end,omitempty"`
}

type CandidateSummary struct {
	Total         int            `json:"total"`
	Pending       int            `json:"pending_review"`
	ByEventCode   map[string]int `json:"by_event_code"`
	ByQuality     map[string]int `json:"by_quality_tier"`
	ByModel       map[string]int `json:"by_model"`
	LatestOnset   *time.Time     `json:"latest_onset_at,omitempty"`
	EarliestOnset *time.Time     `json:"earliest_onset_at,omitempty"`
}

type onsetSignal struct {
	Labels map[string]string
	At     time.Time
}

func (s *Service) BackfillRuns(limit int) ([]api.HistoryBackfillRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.HistoryBackfillRun
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
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
		Total: len(all), ByEventCode: map[string]int{}, ByQuality: map[string]int{}, ByModel: map[string]int{},
	}
	for _, row := range all {
		if row.ReviewStatus == "pending_review" {
			summary.Pending++
		}
		summary.ByEventCode[row.EventCode]++
		summary.ByQuality[row.QualityTier]++
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
		SourceKey: source.ID, JobType: "gpu_alert_onset", Status: "queued",
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
		series, queryErr := client.QueryRange(ctx, alertOnsetQuery, cursor, chunkEnd, time.Duration(run.StepSeconds)*time.Second)
		if queryErr != nil {
			s.failBackfill(&run, fmt.Errorf("chunk %s..%s: %w", cursor.Format(time.RFC3339), chunkEnd.Format(time.RFC3339), queryErr))
			return
		}
		for _, item := range series {
			run.SeriesScanned++
			for _, point := range item.Values {
				if point.Value <= 0 {
					continue
				}
				signals = append(signals, onsetSignal{Labels: item.Metric, At: point.Timestamp})
				run.SignalPoints++
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
		"signal_points": run.SignalPoints,
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
		index++
		for index < len(signals) && signalSignature(signals[index].Labels) == signature &&
			!signals[index].At.After(first.At.Add(time.Hour)) {
			if signals[index].At.After(end) {
				end = signals[index].At
			}
			samples++
			index++
		}
		labels := api.StringMap{}
		for key, value := range first.Labels {
			if key != "__name__" {
				labels[key] = value
			}
		}
		code := firstNonEmpty(labels["err_code"], labels["Xid"])
		eventType, quality := classifyXID(code)
		uuid := firstNonEmpty(labels["UUID"], labels["uuid"])
		candidate := api.HistoricalFaultCandidate{
			SourceKey: sourceKey, BackfillRunID: runID, EntityType: "gpu",
			GPUUUID: uuid, NodeIP: normalizeInstance(firstNonEmpty(labels["host_ip"], labels["instance"])),
			Hostname:  firstNonEmpty(labels["Hostname"], labels["hostname"], labels["node"]),
			ModelName: firstNonEmpty(labels["modelName"], labels["device_type"], labels["model"]),
			PCIBusID:  labels["pci_bus_id"], EventType: eventType, EventCode: code,
			EventMessage: labels["err_msg"], Severity: labels["severity"], QualityTier: quality,
			ReviewStatus: "pending_review", SourceMetric: "ALERTS",
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
	return strings.Join([]string{
		firstNonEmpty(labels["UUID"], labels["uuid"]),
		firstNonEmpty(labels["err_code"], labels["Xid"]),
		labels["alertname"],
	}, "|")
}

func candidateKey(sourceKey, signature string, onset time.Time) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		sourceKey, signature, strconv.FormatInt(onset.Unix(), 10),
	}, "|")))
	return hex.EncodeToString(sum[:])
}

func classifyXID(code string) (string, string) {
	switch code {
	case "79":
		return "xid_79_gpu_fallen_off_bus", "strong_proxy"
	case "94":
		return "xid_94_contained_ecc", "weak_proxy"
	case "109":
		return "xid_109_context_switch_timeout", "weak_proxy"
	case "":
		return "gpu_alert_unclassified", "weak_proxy"
	default:
		return "xid_" + code, "weak_proxy"
	}
}

func normalizeInstance(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

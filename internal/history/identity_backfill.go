package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
)

const (
	identityBackfillQueryVersion = "gpu-identity-interval-v1"
	identityInventoryQuery       = `max_over_time(DCGM_FI_DEV_GPU_UTIL[6h]) >= 0`
)

type IdentitySummary struct {
	Total             int            `json:"total"`
	ByTransition      map[string]int `json:"by_transition_type"`
	ByEvidence        map[string]int `json:"by_evidence_strength"`
	CandidateEvidence map[string]int `json:"candidate_evidence"`
	EarliestSeenAt    *time.Time     `json:"earliest_seen_at,omitempty"`
	LatestSeenAt      *time.Time     `json:"latest_seen_at,omitempty"`
}

type identityAggregate struct {
	row api.HistoricalGPUIdentityInterval
}

func (s *Service) IdentityBackfillRuns(limit int) ([]api.HistoryBackfillRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.HistoryBackfillRun
	err := s.db.Where("job_type = ?", "gpu_identity_interval").
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) IdentityIntervals(limit int) (IdentitySummary, []api.HistoricalGPUIdentityInterval, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var all []api.HistoricalGPUIdentityInterval
	if err := s.db.Order("first_seen_at DESC, id DESC").Find(&all).Error; err != nil {
		return IdentitySummary{}, nil, err
	}
	summary := IdentitySummary{
		Total: len(all), ByTransition: map[string]int{}, ByEvidence: map[string]int{},
		CandidateEvidence: map[string]int{},
	}
	for _, row := range all {
		summary.ByTransition[row.TransitionType]++
		summary.ByEvidence[row.EvidenceStrength]++
		if summary.EarliestSeenAt == nil || row.FirstSeenAt.Before(*summary.EarliestSeenAt) {
			value := row.FirstSeenAt
			summary.EarliestSeenAt = &value
		}
		if summary.LatestSeenAt == nil || row.LastSeenAt.After(*summary.LatestSeenAt) {
			value := row.LastSeenAt
			summary.LatestSeenAt = &value
		}
	}
	var evidenceRows []struct {
		Status string
		Count  int
	}
	if err := s.db.Model(&api.HistoricalFaultCandidate{}).
		Select("identity_evidence_status AS status, count(*) AS count").
		Group("identity_evidence_status").Scan(&evidenceRows).Error; err != nil {
		return IdentitySummary{}, nil, err
	}
	for _, row := range evidenceRows {
		summary.CandidateEvidence[row.Status] = row.Count
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return summary, all, nil
}

func (s *Service) StartIdentityBackfill(request BackfillRequest) (api.HistoryBackfillRun, error) {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	if s.backfillRunning {
		return api.HistoryBackfillRun{}, fmt.Errorf("a historical backfill is already running")
	}
	source, err := s.resolveSource(request.SourceKey)
	if err != nil {
		return api.HistoryBackfillRun{}, err
	}
	start, end, err := s.backfillRange(source, request)
	if err != nil {
		return api.HistoryBackfillRun{}, err
	}
	const chunk = 7 * 24 * time.Hour
	run := api.HistoryBackfillRun{
		SourceKey: source.ID, JobType: "gpu_identity_interval", Status: "queued",
		QueryVersion: identityBackfillQueryVersion, RangeStart: start, RangeEnd: end,
		StepSeconds: 6 * 60 * 60, ChunkHours: 168,
		ChunksTotal: int((end.Sub(start) + chunk - 1) / chunk), StartedAt: s.now(),
	}
	if err := s.db.Create(&run).Error; err != nil {
		return api.HistoryBackfillRun{}, err
	}
	s.backfillRunning = true
	go s.executeIdentityBackfill(run.ID, source)
	return run, nil
}

func (s *Service) backfillRange(source config.HistorySourceConfig, request BackfillRequest) (time.Time, time.Time, error) {
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
			return time.Time{}, time.Time{}, result.Error
		}
		if result.RowsAffected > 0 && audit.EarliestSampleAt != nil {
			value := audit.EarliestSampleAt.UTC()
			start = time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
		}
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("backfill start must be before end")
	}
	return start, end, nil
}

func (s *Service) executeIdentityBackfill(runID uint, source config.HistorySourceConfig) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	aggregates := map[string]*identityAggregate{}
	chunk := time.Duration(run.ChunkHours) * time.Hour
	for cursor := run.RangeStart; cursor.Before(run.RangeEnd); {
		chunkEnd := cursor.Add(chunk)
		if chunkEnd.After(run.RangeEnd) {
			chunkEnd = run.RangeEnd
		}
		series, queryErr := client.QueryRange(
			ctx, identityInventoryQuery, cursor, chunkEnd, time.Duration(run.StepSeconds)*time.Second,
		)
		if queryErr != nil {
			s.failBackfill(&run, fmt.Errorf("identity chunk %s..%s: %w",
				cursor.Format(time.RFC3339), chunkEnd.Format(time.RFC3339), queryErr))
			return
		}
		for _, item := range series {
			if len(item.Values) == 0 {
				continue
			}
			labels := item.Metric
			nodeIP := normalizeInstance(firstNonEmpty(labels["host_ip"], labels["instance"]))
			uuid := firstNonEmpty(labels["UUID"], labels["uuid"])
			if nodeIP == "" || uuid == "" {
				continue
			}
			key := historicalIdentityKey(source.ID, nodeIP, labels["pci_bus_id"], uuid,
				labels["host_id"], labels["sn"], firstNonEmpty(labels["gpu"], labels["device"]))
			aggregate := aggregates[key]
			if aggregate == nil {
				gpuIndex, _ := strconv.Atoi(firstNonEmpty(labels["gpu"], strings.TrimPrefix(labels["device"], "nvidia")))
				aggregate = &identityAggregate{row: api.HistoricalGPUIdentityInterval{
					IntervalKey: key, SourceKey: source.ID, BackfillRunID: run.ID,
					NodeIP: nodeIP, HostID: labels["host_id"], HostSerial: labels["sn"],
					Hostname:     firstNonEmpty(labels["Hostname"], labels["hostname"]),
					DataCenterID: labels["data_center_id"], GPUIndex: gpuIndex, GPUUUID: uuid,
					PCIBusID: labels["pci_bus_id"], ModelName: firstNonEmpty(labels["modelName"], labels["model"]),
					DriverVersion: labels["DCGM_FI_DRIVER_VERSION"],
					FirstSeenAt:   item.Values[0].Timestamp, LastSeenAt: item.Values[len(item.Values)-1].Timestamp,
					EvidenceStrength: "observed_identity",
				}}
				aggregates[key] = aggregate
			}
			if item.Values[0].Timestamp.Before(aggregate.row.FirstSeenAt) {
				aggregate.row.FirstSeenAt = item.Values[0].Timestamp
			}
			last := item.Values[len(item.Values)-1].Timestamp
			if last.After(aggregate.row.LastSeenAt) {
				aggregate.row.LastSeenAt = last
			}
			aggregate.row.ObservationCount += len(item.Values)
			run.SeriesScanned++
			run.SignalPoints += len(item.Values)
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
	for _, aggregate := range aggregates {
		var existing api.HistoricalGPUIdentityInterval
		result := s.db.Where("interval_key = ?", aggregate.row.IntervalKey).Limit(1).Find(&existing)
		if result.Error != nil {
			s.failBackfill(&run, result.Error)
			return
		}
		if result.RowsAffected == 0 {
			if err := s.db.Create(&aggregate.row).Error; err != nil {
				s.failBackfill(&run, err)
				return
			}
			run.RecordsCreated++
			continue
		}
		if err := s.db.Model(&existing).Updates(map[string]any{
			"backfill_run_id": run.ID, "first_seen_at": aggregate.row.FirstSeenAt,
			"last_seen_at": aggregate.row.LastSeenAt, "observation_count": aggregate.row.ObservationCount,
			"hostname": aggregate.row.Hostname, "model_name": aggregate.row.ModelName,
			"driver_version": aggregate.row.DriverVersion,
		}).Error; err != nil {
			s.failBackfill(&run, err)
			return
		}
		run.RecordsUpdated++
	}
	if err := s.deriveIdentityTransitions(source.ID); err != nil {
		s.failBackfill(&run, err)
		return
	}
	annotated, err := s.annotateCandidatesWithIdentity(source.ID)
	if err != nil {
		s.failBackfill(&run, err)
		return
	}
	run.RecordsAnnotated = annotated
	finished := s.now()
	_ = s.db.Model(&run).Updates(map[string]any{
		"status": "completed", "records_created": run.RecordsCreated,
		"records_updated": run.RecordsUpdated, "records_annotated": run.RecordsAnnotated,
		"finished_at": &finished,
	}).Error
}

func (s *Service) deriveIdentityTransitions(sourceKey string) error {
	var rows []api.HistoricalGPUIdentityInterval
	if err := s.db.Where("source_key = ?", sourceKey).
		Order("node_ip ASC, pci_bus_id ASC, gpu_index ASC, first_seen_at ASC").Find(&rows).Error; err != nil {
		return err
	}
	groups := map[string][]api.HistoricalGPUIdentityInterval{}
	for _, row := range rows {
		slot := row.PCIBusID
		if slot == "" {
			slot = strconv.Itoa(row.GPUIndex)
		}
		groups[row.NodeIP+"|"+slot] = append(groups[row.NodeIP+"|"+slot], row)
	}
	for _, intervals := range groups {
		sort.Slice(intervals, func(i, j int) bool { return intervals[i].FirstSeenAt.Before(intervals[j].FirstSeenAt) })
		for index := range intervals {
			updates := map[string]any{
				"transition_type": "initial_observation", "predecessor_uuid": "",
				"transition_at": nil, "evidence_strength": "observed_identity",
			}
			if index > 0 {
				previous, current := intervals[index-1], intervals[index]
				if previous.GPUUUID == current.GPUUUID {
					updates["transition_type"] = "stable_continuation"
				} else {
					transitionType := "gpu_uuid_changed"
					strength := "replacement_candidate"
					if identityValueChanged(previous.HostID, current.HostID) ||
						identityValueChanged(previous.HostSerial, current.HostSerial) {
						transitionType = "node_identity_changed"
						strength = "node_boundary"
					}
					at := current.FirstSeenAt
					updates["transition_type"] = transitionType
					updates["predecessor_uuid"] = previous.GPUUUID
					updates["transition_at"] = &at
					updates["evidence_strength"] = strength
				}
			}
			if err := s.db.Model(&api.HistoricalGPUIdentityInterval{}).
				Where("id = ?", intervals[index].ID).Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) annotateCandidatesWithIdentity(sourceKey string) (int, error) {
	var intervals []api.HistoricalGPUIdentityInterval
	if err := s.db.Where("source_key = ?", sourceKey).Order("first_seen_at ASC").Find(&intervals).Error; err != nil {
		return 0, err
	}
	var candidates []api.HistoricalFaultCandidate
	if err := s.db.Where("source_key = ?", sourceKey).Find(&candidates).Error; err != nil {
		return 0, err
	}
	annotated := 0
	for _, candidate := range candidates {
		status := "insufficient_identity_history"
		var matched *api.HistoricalGPUIdentityInterval
		for index := range intervals {
			interval := &intervals[index]
			if interval.NodeIP != candidate.NodeIP || interval.GPUUUID != candidate.GPUUUID ||
				!sameSlot(candidate.PCIBusID, interval.PCIBusID) {
				continue
			}
			if !candidate.OnsetAt.Before(interval.FirstSeenAt.Add(-6*time.Hour)) &&
				!candidate.OnsetAt.After(interval.LastSeenAt.Add(6*time.Hour)) {
				matched = interval
				break
			}
		}
		evidence := api.StringMap{}
		intervalID := uint(0)
		if matched != nil {
			intervalID = matched.ID
			status = "matched_at_event"
			evidence["interval_first_seen_at"] = matched.FirstSeenAt.Format(time.RFC3339)
			evidence["interval_last_seen_at"] = matched.LastSeenAt.Format(time.RFC3339)
			evidence["pci_bus_id"] = matched.PCIBusID
			if successor := replacementAfter(candidate, *matched, intervals); successor != nil {
				status = "replacement_after_event"
				if successor.TransitionType == "node_identity_changed" {
					status = "node_identity_boundary"
				}
				evidence["successor_uuid"] = successor.GPUUUID
				evidence["transition_at"] = successor.FirstSeenAt.Format(time.RFC3339)
				evidence["transition_type"] = successor.TransitionType
			} else if matched.LastSeenAt.After(candidate.OnsetAt.Add(24 * time.Hour)) {
				status = "same_gpu_observed_after_event"
			} else if !matched.LastSeenAt.Before(candidate.OnsetAt.Add(-6 * time.Hour)) {
				status = "telemetry_ended_near_event"
			}
		} else if slotObservedAt(candidate, intervals) {
			status = "uuid_mismatch_at_event"
		}
		if err := s.db.Model(&candidate).Updates(map[string]any{
			"identity_evidence_status": status, "identity_interval_id": intervalID,
			"identity_evidence": evidence,
		}).Error; err != nil {
			return annotated, err
		}
		annotated++
	}
	return annotated, nil
}

func replacementAfter(candidate api.HistoricalFaultCandidate, matched api.HistoricalGPUIdentityInterval, intervals []api.HistoricalGPUIdentityInterval) *api.HistoricalGPUIdentityInterval {
	for index := range intervals {
		row := &intervals[index]
		if row.NodeIP == candidate.NodeIP && row.GPUUUID != candidate.GPUUUID &&
			sameSlot(candidate.PCIBusID, row.PCIBusID) &&
			row.FirstSeenAt.After(candidate.OnsetAt) && !row.FirstSeenAt.After(candidate.OnsetAt.Add(7*24*time.Hour)) {
			return row
		}
	}
	return nil
}

func slotObservedAt(candidate api.HistoricalFaultCandidate, intervals []api.HistoricalGPUIdentityInterval) bool {
	for _, row := range intervals {
		if row.NodeIP == candidate.NodeIP && sameSlot(candidate.PCIBusID, row.PCIBusID) &&
			!candidate.OnsetAt.Before(row.FirstSeenAt.Add(-6*time.Hour)) &&
			!candidate.OnsetAt.After(row.LastSeenAt.Add(6*time.Hour)) {
			return true
		}
	}
	return false
}

func sameSlot(left, right string) bool {
	return left == "" || right == "" || strings.EqualFold(left, right)
}

func identityValueChanged(left, right string) bool {
	return left != "" && right != "" && !strings.EqualFold(left, right)
}

func historicalIdentityKey(sourceKey, nodeIP, pciBusID, uuid, hostID, serial, gpu string) string {
	sum := sha256.Sum256([]byte(strings.Join(
		[]string{sourceKey, nodeIP, pciBusID, uuid, hostID, serial, gpu}, "|",
	)))
	return hex.EncodeToString(sum[:])
}

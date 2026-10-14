package diskhealth

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

type Service struct {
	db *storage.DB
}

func NewService(db *storage.DB) *Service {
	return &Service{db: db}
}

type PersistResult struct {
	SnapshotsCreated   int `json:"snapshots_created"`
	SnapshotsDuplicate int `json:"snapshots_duplicate"`
	EpisodesUpserted   int `json:"episodes_upserted"`
	CurrentPromoted    int `json:"current_promoted"`
}

type ListOptions struct {
	Limit            int
	Offset           int
	Query            string
	Level            string
	AssessmentStatus string
	Protocol         string
	MediaType        string
	Order            string
}

type SnapshotView struct {
	SnapshotID uint `json:"snapshot_id"`
	Current    bool `json:"current"`
	Result
}

type CurrentSummary struct {
	Total       int            `json:"total"`
	Assessed    int            `json:"assessed"`
	Unknown     int            `json:"unknown"`
	RiskCount   int            `json:"risk_count"`
	StaleCount  int            `json:"stale_count"`
	ByLevel     map[string]int `json:"by_level"`
	GeneratedAt time.Time      `json:"generated_at"`
}

type TrendWindow struct {
	Window              string         `json:"window"`
	SampleCount         int            `json:"sample_count"`
	FirstObservedAt     *time.Time     `json:"first_observed_at,omitempty"`
	LastObservedAt      *time.Time     `json:"last_observed_at,omitempty"`
	MaxGapSeconds       *int64         `json:"max_gap_seconds,omitempty"`
	CoverageStatus      string         `json:"coverage_status"`
	LatestTrendStatus   string         `json:"latest_trend_status"`
	LatestCounterDeltas []CounterDelta `json:"latest_counter_deltas"`
	LatestMediaDelta    *string        `json:"latest_media_delta,omitempty"`
}

type HistoryResponse struct {
	EntityID string         `json:"entity_id"`
	Windows  []TrendWindow  `json:"windows"`
	Rows     []SnapshotView `json:"rows"`
	Total    int            `json:"total"`
}

type DetailResponse struct {
	SnapshotView
	Episodes []api.DiskHealthEpisode `json:"episodes"`
	Windows  []TrendWindow           `json:"windows"`
}

func (s *Service) PersistReport(report Report, reportSHA256 string) (PersistResult, error) {
	result := PersistResult{}
	if s == nil || s.db == nil {
		return result, fmt.Errorf("disk health service requires a database")
	}
	if report.Schema != SchemaVersion {
		return result, fmt.Errorf("unsupported report schema %q", report.Schema)
	}
	if strings.TrimSpace(reportSHA256) == "" {
		encoded, err := json.Marshal(report)
		if err != nil {
			return result, err
		}
		reportSHA256 = Digest(encoded)
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, row := range report.Rows {
			if row.EntityType == "memory_controller" {
				continue
			}
			snapshot, created, err := upsertSnapshot(tx, report, row, reportSHA256)
			if err != nil {
				return err
			}
			if created {
				result.SnapshotsCreated++
			} else {
				result.SnapshotsDuplicate++
			}
			promoted, err := promoteCurrentSnapshot(tx, snapshot)
			if err != nil {
				return err
			}
			if promoted {
				result.CurrentPromoted++
			}
			episodes, err := upsertEpisodes(tx, snapshot, row)
			if err != nil {
				return err
			}
			result.EpisodesUpserted += episodes
		}
		return nil
	})
	return result, err
}

func (s *Service) Summary() (CurrentSummary, error) {
	summary := CurrentSummary{ByLevel: map[string]int{}, GeneratedAt: time.Now().UTC()}
	var rows []api.PhysicalDiskSnapshot
	if err := s.db.Where("current = ? AND entity_type <> ?", true, "memory_controller").Find(&rows).Error; err != nil {
		return summary, err
	}
	for _, row := range rows {
		summary.Total++
		summary.ByLevel[row.Level]++
		if row.AssessmentStatus == "assessed" {
			summary.Assessed++
		} else {
			summary.Unknown++
		}
		if row.Level == "critical" || row.Level == "warning" || row.Level == "attention" {
			summary.RiskCount++
		}
		if row.Freshness == "stale" {
			summary.StaleCount++
		}
	}
	return summary, nil
}

func (s *Service) ListCurrentSnapshots(options ListOptions) ([]SnapshotView, int, error) {
	query := s.db.Where("current = ? AND entity_type <> ?", true, "memory_controller")
	query = applySnapshotFilters(query, options)
	var snapshots []api.PhysicalDiskSnapshot
	if err := query.Find(&snapshots).Error; err != nil {
		return nil, 0, err
	}
	sortSnapshots(snapshots, options.Order)
	total := len(snapshots)
	limit, offset := normalizePage(options.Limit, options.Offset, 100, 2000)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	views := make([]SnapshotView, 0, end-offset)
	for _, snapshot := range snapshots[offset:end] {
		view, err := snapshotView(snapshot)
		if err != nil {
			return nil, 0, err
		}
		views = append(views, view)
	}
	return views, total, nil
}

func (s *Service) Detail(entityID string) (DetailResponse, error) {
	var snapshot api.PhysicalDiskSnapshot
	err := s.db.Where("entity_id = ? AND current = ? AND entity_type <> ?", entityID, true, "memory_controller").First(&snapshot).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DetailResponse{}, fmt.Errorf("disk entity not found")
	}
	if err != nil {
		return DetailResponse{}, err
	}
	view, err := snapshotView(snapshot)
	if err != nil {
		return DetailResponse{}, err
	}
	var episodes []api.DiskHealthEpisode
	if err := s.db.Where("entity_id = ?", entityID).Order("state ASC, severity DESC, last_observed_at DESC").Find(&episodes).Error; err != nil {
		return DetailResponse{}, err
	}
	history, err := s.History(entityID, 100, 0)
	if err != nil {
		return DetailResponse{}, err
	}
	return DetailResponse{SnapshotView: view, Episodes: episodes, Windows: history.Windows}, nil
}

func (s *Service) History(entityID string, limit, offset int) (HistoryResponse, error) {
	var snapshots []api.PhysicalDiskSnapshot
	if err := s.db.Where("entity_id = ? AND entity_type <> ?", entityID, "memory_controller").Order("observed_at ASC, id ASC").Find(&snapshots).Error; err != nil {
		return HistoryResponse{}, err
	}
	if len(snapshots) == 0 {
		return HistoryResponse{}, fmt.Errorf("disk entity not found")
	}
	allViews := make([]SnapshotView, 0, len(snapshots))
	for _, snapshot := range snapshots {
		view, err := snapshotView(snapshot)
		if err != nil {
			return HistoryResponse{}, err
		}
		allViews = append(allViews, view)
	}
	windows := trendWindows(allViews)
	sort.SliceStable(allViews, func(i, j int) bool {
		if allViews[i].ObservedAt.Equal(allViews[j].ObservedAt) {
			return allViews[i].SnapshotID > allViews[j].SnapshotID
		}
		return allViews[i].ObservedAt.After(allViews[j].ObservedAt)
	})
	limit, offset = normalizePage(limit, offset, 100, 500)
	if offset > len(allViews) {
		offset = len(allViews)
	}
	end := offset + limit
	if end > len(allViews) {
		end = len(allViews)
	}
	return HistoryResponse{EntityID: entityID, Windows: windows, Rows: allViews[offset:end], Total: len(allViews)}, nil
}

func upsertSnapshot(tx *gorm.DB, report Report, row Result, reportSHA256 string) (api.PhysicalDiskSnapshot, bool, error) {
	snapshotKey := snapshotKey(row)
	var existing api.PhysicalDiskSnapshot
	err := tx.Where("snapshot_key = ?", snapshotKey).First(&existing).Error
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return existing, false, err
	}
	snapshot, err := snapshotFromResult(report, row, reportSHA256, snapshotKey)
	if err != nil {
		return snapshot, false, err
	}
	if err := tx.Create(&snapshot).Error; err != nil {
		return snapshot, false, err
	}
	return snapshot, true, nil
}

func promoteCurrentSnapshot(tx *gorm.DB, snapshot api.PhysicalDiskSnapshot) (bool, error) {
	if snapshot.EntityID == "" {
		return false, nil
	}
	var current api.PhysicalDiskSnapshot
	err := tx.Where("entity_id = ? AND current = ?", snapshot.EntityID, true).First(&current).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	if err == nil && current.ID == snapshot.ID {
		return false, nil
	}
	promote := errors.Is(err, gorm.ErrRecordNotFound) ||
		snapshot.ObservedAt.After(current.ObservedAt) ||
		(snapshot.ObservedAt.Equal(current.ObservedAt) && snapshot.AvailableAt.After(current.AvailableAt)) ||
		(snapshot.ObservedAt.Equal(current.ObservedAt) && snapshot.AvailableAt.Equal(current.AvailableAt) && snapshot.ID > current.ID)
	if !promote {
		return false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) && current.ID != snapshot.ID {
		if err := tx.Model(&api.PhysicalDiskSnapshot{}).Where("id = ?", current.ID).Update("current", false).Error; err != nil {
			return false, err
		}
	}
	if !snapshot.Current {
		if err := tx.Model(&api.PhysicalDiskSnapshot{}).Where("id = ?", snapshot.ID).Update("current", true).Error; err != nil {
			return false, err
		}
	}
	return true, nil
}

func upsertEpisodes(tx *gorm.DB, snapshot api.PhysicalDiskSnapshot, row Result) (int, error) {
	count := 0
	for _, hit := range row.Hits {
		keyRaw, _ := json.Marshal([]string{row.EntityID, hit.Rule})
		key := Digest(keyRaw)
		var episode api.DiskHealthEpisode
		err := tx.Where("episode_key = ?", key).First(&episode).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			episode = api.DiskHealthEpisode{
				EpisodeKey:       key,
				EntityID:         row.EntityID,
				EntityType:       row.EntityType,
				Node:             row.Node,
				Device:           row.Device,
				Serial:           row.Serial,
				Model:            row.Model,
				RuleCode:         hit.Rule,
				Severity:         hit.Level,
				State:            "active",
				EvidenceID:       hit.EvidenceID,
				OccurrenceCount:  1,
				LatestSnapshotID: snapshot.ID,
				RuleVersion:      row.RuleVersion,
				FirstObservedAt:  row.ObservedAt,
				LastObservedAt:   row.ObservedAt,
			}
			if err := tx.Create(&episode).Error; err != nil {
				return count, err
			}
			count++
			continue
		}
		if err != nil {
			return count, err
		}
		if episode.LatestSnapshotID == snapshot.ID {
			continue
		}
		updates := map[string]any{
			"state":              "active",
			"recovered_at":       nil,
			"severity":           strongerSeverity(episode.Severity, hit.Level),
			"evidence_id":        hit.EvidenceID,
			"latest_snapshot_id": snapshot.ID,
			"last_observed_at":   row.ObservedAt,
			"rule_version":       row.RuleVersion,
			"occurrence_count":   episode.OccurrenceCount + 1,
			"node":               row.Node,
			"device":             row.Device,
			"serial":             row.Serial,
			"model":              row.Model,
		}
		if row.ObservedAt.Before(episode.FirstObservedAt) {
			updates["first_observed_at"] = row.ObservedAt
		}
		if err := tx.Model(&api.DiskHealthEpisode{}).Where("id = ?", episode.ID).Updates(updates).Error; err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func snapshotFromResult(report Report, row Result, reportSHA256, key string) (api.PhysicalDiskSnapshot, error) {
	diskJSON, err := marshalOptional(row.Disk)
	if err != nil {
		return api.PhysicalDiskSnapshot{}, err
	}
	memoryJSON, err := marshalOptional(row.Memory)
	if err != nil {
		return api.PhysicalDiskSnapshot{}, err
	}
	hitsJSON, err := json.Marshal(row.Hits)
	if err != nil {
		return api.PhysicalDiskSnapshot{}, err
	}
	evidenceJSON, err := json.Marshal(row.Evidence)
	if err != nil {
		return api.PhysicalDiskSnapshot{}, err
	}
	mediaType, profile := "", ""
	if row.Disk != nil {
		mediaType, profile = row.Disk.MediaType, row.Disk.Profile
	}
	return api.PhysicalDiskSnapshot{
		SnapshotKey:         key,
		ReportSHA256:        reportSHA256,
		InputManifestSHA256: report.InputManifestSHA256,
		ReportRuleVersion:   report.RuleVersion,
		RuleVersion:         row.RuleVersion,
		Reference:           row.Reference,
		EntityID:            row.EntityID,
		EntityType:          row.EntityType,
		IdentityStatus:      row.IdentityStatus,
		Scope:               row.Scope,
		Node:                row.Node,
		Generation:          row.Generation,
		BootID:              row.BootID,
		Serial:              row.Serial,
		Model:               row.Model,
		Firmware:            row.Firmware,
		Protocol:            row.Protocol,
		Device:              row.Device,
		MediaType:           mediaType,
		DiskProfile:         profile,
		ToolVersion:         row.ToolVersion,
		Source:              row.Source,
		SourceKind:          row.SourceKind,
		InputSHA256:         row.InputSHA256,
		BaselineSHA256:      row.BaselineSHA256,
		Freshness:           row.Freshness,
		Support:             row.Support,
		AssessmentStatus:    row.AssessmentStatus,
		Level:               row.Level,
		ExitCode:            row.ExitCode,
		MediaDelta:          row.MediaDelta,
		TrendStatus:         row.TrendStatus,
		Blockers:            api.StringList(row.Blockers),
		DiskJSON:            diskJSON,
		MemoryJSON:          memoryJSON,
		HitsJSON:            string(hitsJSON),
		EvidenceJSON:        string(evidenceJSON),
		ObservedAt:          row.ObservedAt,
		AvailableAt:         row.AvailableAt,
	}, nil
}

func snapshotView(snapshot api.PhysicalDiskSnapshot) (SnapshotView, error) {
	row := Result{
		RuleVersion:      snapshot.RuleVersion,
		Reference:        snapshot.Reference,
		EntityID:         snapshot.EntityID,
		EntityType:       snapshot.EntityType,
		IdentityStatus:   snapshot.IdentityStatus,
		Scope:            snapshot.Scope,
		Node:             snapshot.Node,
		Generation:       snapshot.Generation,
		BootID:           snapshot.BootID,
		Serial:           snapshot.Serial,
		Model:            snapshot.Model,
		Firmware:         snapshot.Firmware,
		Protocol:         snapshot.Protocol,
		Device:           snapshot.Device,
		ToolVersion:      snapshot.ToolVersion,
		Source:           snapshot.Source,
		SourceKind:       snapshot.SourceKind,
		InputSHA256:      snapshot.InputSHA256,
		ObservedAt:       snapshot.ObservedAt,
		AvailableAt:      snapshot.AvailableAt,
		Freshness:        snapshot.Freshness,
		Support:          snapshot.Support,
		AssessmentStatus: snapshot.AssessmentStatus,
		Level:            snapshot.Level,
		ExitCode:         snapshot.ExitCode,
		MediaDelta:       snapshot.MediaDelta,
		BaselineSHA256:   snapshot.BaselineSHA256,
		TrendStatus:      snapshot.TrendStatus,
		Blockers:         []string(snapshot.Blockers),
		Hits:             []Hit{},
		Evidence:         []Evidence{},
	}
	if strings.TrimSpace(snapshot.DiskJSON) != "" {
		var disk DiskDetails
		if err := json.Unmarshal([]byte(snapshot.DiskJSON), &disk); err != nil {
			return SnapshotView{}, err
		}
		row.Disk = &disk
	}
	if strings.TrimSpace(snapshot.MemoryJSON) != "" {
		var memory MemoryDetails
		if err := json.Unmarshal([]byte(snapshot.MemoryJSON), &memory); err != nil {
			return SnapshotView{}, err
		}
		row.Memory = &memory
	}
	if strings.TrimSpace(snapshot.HitsJSON) != "" {
		if err := json.Unmarshal([]byte(snapshot.HitsJSON), &row.Hits); err != nil {
			return SnapshotView{}, err
		}
	}
	if strings.TrimSpace(snapshot.EvidenceJSON) != "" {
		if err := json.Unmarshal([]byte(snapshot.EvidenceJSON), &row.Evidence); err != nil {
			return SnapshotView{}, err
		}
	}
	return SnapshotView{SnapshotID: snapshot.ID, Current: snapshot.Current, Result: row}, nil
}

func applySnapshotFilters(query *gorm.DB, options ListOptions) *gorm.DB {
	if value := strings.TrimSpace(options.Level); value != "" {
		query = query.Where("level = ?", value)
	}
	if value := strings.TrimSpace(options.AssessmentStatus); value != "" {
		query = query.Where("assessment_status = ?", value)
	}
	if value := strings.TrimSpace(options.Protocol); value != "" {
		query = query.Where("protocol = ?", value)
	}
	if value := strings.TrimSpace(options.MediaType); value != "" {
		query = query.Where("media_type = ?", value)
	}
	if value := strings.TrimSpace(options.Query); value != "" {
		like := "%" + strings.ToLower(value) + "%"
		query = query.Where("LOWER(node) LIKE ? OR LOWER(serial) LIKE ? OR LOWER(model) LIKE ? OR LOWER(device) LIKE ? OR LOWER(scope) LIKE ? OR LOWER(reference) LIKE ?",
			like, like, like, like, like, like)
	}
	return query
}

func sortSnapshots(snapshots []api.PhysicalDiskSnapshot, order string) {
	sort.SliceStable(snapshots, func(i, j int) bool {
		a, b := snapshots[i], snapshots[j]
		if order == "observed" {
			if a.ObservedAt.Equal(b.ObservedAt) {
				return a.Reference < b.Reference
			}
			return a.ObservedAt.After(b.ObservedAt)
		}
		if riskPriority(a.Level) != riskPriority(b.Level) {
			return riskPriority(a.Level) > riskPriority(b.Level)
		}
		if a.AssessmentStatus != b.AssessmentStatus {
			return a.AssessmentStatus == "insufficient_data"
		}
		if a.ObservedAt.Equal(b.ObservedAt) {
			return a.Reference < b.Reference
		}
		return a.ObservedAt.After(b.ObservedAt)
	})
}

func trendWindows(rows []SnapshotView) []TrendWindow {
	if len(rows) == 0 {
		return []TrendWindow{}
	}
	latest := rows[len(rows)-1]
	defs := []struct {
		name     string
		duration time.Duration
	}{
		{"1h", time.Hour},
		{"24h", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
	}
	result := make([]TrendWindow, 0, len(defs))
	for _, def := range defs {
		cutoff := latest.ObservedAt.Add(-def.duration)
		var selected []SnapshotView
		for _, row := range rows {
			if !row.ObservedAt.Before(cutoff) && !row.ObservedAt.After(latest.ObservedAt) {
				selected = append(selected, row)
			}
		}
		window := TrendWindow{Window: def.name, CoverageStatus: "collecting", LatestTrendStatus: latest.TrendStatus, LatestMediaDelta: latest.MediaDelta}
		if latest.Disk != nil {
			window.LatestCounterDeltas = latest.Disk.Deltas
		} else {
			window.LatestCounterDeltas = []CounterDelta{}
		}
		if len(selected) > 0 {
			first, last := selected[0].ObservedAt, selected[len(selected)-1].ObservedAt
			window.FirstObservedAt, window.LastObservedAt = &first, &last
			window.SampleCount = len(selected)
			var maxGap int64
			for i := 1; i < len(selected); i++ {
				gap := int64(selected[i].ObservedAt.Sub(selected[i-1].ObservedAt).Seconds())
				if gap > maxGap {
					maxGap = gap
				}
			}
			if len(selected) > 1 {
				window.MaxGapSeconds = &maxGap
				window.CoverageStatus = "available"
			}
		}
		result = append(result, window)
	}
	return result
}

func snapshotKey(row Result) string {
	raw, _ := json.Marshal([]string{row.EntityID, row.InputSHA256, row.ObservedAt.UTC().Format(time.RFC3339Nano), row.AvailableAt.UTC().Format(time.RFC3339Nano), row.RuleVersion})
	return Digest(raw)
}

func marshalOptional(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func riskPriority(level string) int {
	switch level {
	case "critical":
		return 4
	case "warning":
		return 3
	case "attention":
		return 2
	case "unknown":
		return 1
	default:
		return 0
	}
}

func strongerSeverity(a, b string) string {
	if riskPriority(b) > riskPriority(a) {
		return b
	}
	return a
}

func normalizePage(limit, offset, defaultLimit, maxLimit int) (int, int) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

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
	db  *storage.DB
	now func() time.Time
}

func NewService(db *storage.DB) *Service {
	return &Service{db: db, now: time.Now}
}

type PersistResult struct {
	SnapshotsCreated   int `json:"snapshots_created"`
	SnapshotsDuplicate int `json:"snapshots_duplicate"`
	EpisodesUpserted   int `json:"episodes_upserted"`
	CurrentPromoted    int `json:"current_promoted"`
}

type ListOptions struct {
	View             string
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
	SnapshotKey  string                 `json:"snapshot_key"`
	Installation InstallationMembership `json:"installation"`
	SnapshotID   uint                   `json:"snapshot_id"`
	Current      bool                   `json:"current"`
	Observation  ObservationStatus      `json:"observation"`
	Result
}

// Live cache age is separate from the immutable report's cutoff assessment.
// The one-hour budget tolerates one missed 30-minute pilot observation.
const ObservationMaxAge = time.Hour

type ObservationStatus struct {
	State         string    `json:"state"`
	EvaluatedAt   time.Time `json:"evaluated_at"`
	MaxAgeSeconds int64     `json:"max_age_seconds"`
	AgeSeconds    *int64    `json:"age_seconds"`
}

func observationStatus(observed, available, now time.Time) ObservationStatus {
	status := ObservationStatus{State: "unknown", EvaluatedAt: now.UTC(), MaxAgeSeconds: int64(ObservationMaxAge.Seconds())}
	if observed.IsZero() || available.IsZero() || available.Before(observed) {
		return status
	}
	if observed.After(now) || available.After(now) {
		status.State = "future"
		return status
	}
	age := int64(now.Sub(observed).Seconds())
	status.AgeSeconds = &age
	status.State = "fresh"
	if now.Sub(observed) > ObservationMaxAge {
		status.State = "stale"
	}
	return status
}

type CurrentSummary struct {
	ContractVersion      string         `json:"contract_version"`
	View                 string         `json:"view"`
	ActiveCount          int            `json:"active_count"`
	HistoricalCount      int            `json:"historical_count"`
	UnregisteredCount    int            `json:"unregistered_count"`
	RegisteredScopeCount int            `json:"registered_scope_count"`
	Total                int            `json:"total"`
	Assessed             int            `json:"assessed"`
	Unknown              int            `json:"unknown"`
	RiskCount            int            `json:"risk_count"`
	StaleCount           int            `json:"stale_count"`
	ByLevel              map[string]int `json:"by_level"`
	GeneratedAt          time.Time      `json:"generated_at"`
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
		if err := lockInstallationIdentity(tx); err != nil {
			return err
		}
		affectedEntities := map[string]struct{}{}
		for _, row := range report.Rows {
			if row.EntityType == "memory_controller" {
				continue
			}
			if err := validateRegisteredSnapshotBinding(tx, row); err != nil {
				return err
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
			affectedEntities[snapshot.EntityID] = struct{}{}
		}
		entityIDs := make([]string, 0, len(affectedEntities))
		for entityID := range affectedEntities {
			entityIDs = append(entityIDs, entityID)
		}
		sort.Strings(entityIDs)
		for _, entityID := range entityIDs {
			episodes, err := upsertEpisodes(tx, entityID)
			if err != nil {
				return err
			}
			result.EpisodesUpserted += episodes
		}
		return nil
	})
	if err != nil {
		return PersistResult{}, err
	}
	return result, nil
}

func (s *Service) Summary() (CurrentSummary, error) { return s.SummaryForView("active") }

func (s *Service) ListCurrentSnapshots(options ListOptions) ([]SnapshotView, int, error) {
	result, err := s.ListDisks(options)
	return result.Data, result.Total, err
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
	view.Observation = observationStatus(view.ObservedAt, view.AvailableAt, s.now())
	view.Installation, err = s.installationMembership(view.EntityID)
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
	membership, err := s.installationMembership(entityID)
	if err != nil {
		return HistoryResponse{}, err
	}
	allViews := make([]SnapshotView, 0, len(snapshots))
	now := s.now()
	for _, snapshot := range snapshots {
		view, err := snapshotView(snapshot)
		if err != nil {
			return HistoryResponse{}, err
		}
		view.Observation = observationStatus(view.ObservedAt, view.AvailableAt, now)
		view.Installation = membership
		allViews = append(allViews, view)
	}
	windows := trendWindows(allViews, now)
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

func upsertEpisodes(tx *gorm.DB, entityID string) (int, error) {
	// Rebuild from saved evidence even for a duplicate/no-hit import. Incrementing
	// the last aggregate cannot distinguish an old replay from a new occurrence,
	// and cannot repair counts polluted by older versions of that algorithm.
	var snapshots []api.PhysicalDiskSnapshot
	if err := tx.Select("id", "snapshot_key", "entity_type", "node", "device", "serial", "model",
		"rule_version", "input_sha256", "observed_at", "available_at", "hits_json").
		Where("entity_id = ? AND entity_type <> ?", entityID, "memory_controller").Find(&snapshots).Error; err != nil {
		return 0, err
	}
	// Compare instants in Go: SQLite text ordering can otherwise order distinct
	// timezone offsets differently from PostgreSQL timestamps.
	sort.Slice(snapshots, func(i, j int) bool {
		a, b := snapshots[i], snapshots[j]
		if !a.ObservedAt.Equal(b.ObservedAt) {
			return a.ObservedAt.Before(b.ObservedAt)
		}
		if !a.AvailableAt.Equal(b.AvailableAt) {
			return a.AvailableAt.Before(b.AvailableAt)
		}
		return a.SnapshotKey < b.SnapshotKey
	})
	type aggregation struct {
		episode      api.DiskHealthEpisode
		observations map[string]struct{}
	}
	aggregates := map[string]*aggregation{}
	for _, snapshot := range snapshots {
		var hits []Hit
		if err := json.Unmarshal([]byte(snapshot.HitsJSON), &hits); err != nil {
			return 0, fmt.Errorf("decode disk snapshot %d rule hits: %w", snapshot.ID, err)
		}
		// Duplicate hits in one snapshot are still one rule occurrence. Choosing a
		// stable representative also makes its evidence independent of hit order.
		rules := map[string]Hit{}
		for _, hit := range hits {
			previous, exists := rules[hit.Rule]
			if !exists || riskPriority(hit.Level) > riskPriority(previous.Level) ||
				(hit.Level == previous.Level && hit.EvidenceID > previous.EvidenceID) {
				rules[hit.Rule] = hit
			}
		}
		observation, _ := json.Marshal([]string{entityID, snapshot.InputSHA256, snapshot.ObservedAt.UTC().Format(time.RFC3339Nano)})
		for rule, hit := range rules {
			aggregate := aggregates[rule]
			if aggregate == nil {
				keyRaw, _ := json.Marshal([]string{entityID, rule})
				aggregate = &aggregation{
					episode: api.DiskHealthEpisode{
						EpisodeKey: Digest(keyRaw), EntityID: entityID, RuleCode: rule,
						State: "active", FirstObservedAt: snapshot.ObservedAt,
					},
					observations: map[string]struct{}{},
				}
				aggregates[rule] = aggregate
			}
			aggregate.observations[string(observation)] = struct{}{}
			episode := &aggregate.episode
			episode.OccurrenceCount = len(aggregate.observations)
			episode.Severity = strongerSeverity(episode.Severity, hit.Level)
			// The sorted order selects the newest observed/available source with a
			// content-based final tie, never the last imported database ID.
			episode.LastObservedAt = snapshot.ObservedAt
			episode.EntityType, episode.Node, episode.Device = snapshot.EntityType, snapshot.Node, snapshot.Device
			episode.Serial, episode.Model = snapshot.Serial, snapshot.Model
			episode.LatestSnapshotID, episode.RuleVersion = snapshot.ID, snapshot.RuleVersion
			episode.EvidenceID = hit.EvidenceID
		}
	}
	rules := make([]string, 0, len(aggregates))
	for rule := range aggregates {
		rules = append(rules, rule)
	}
	sort.Strings(rules)
	count := 0
	for _, rule := range rules {
		desired := aggregates[rule].episode
		var existing api.DiskHealthEpisode
		err := tx.Where("episode_key = ?", desired.EpisodeKey).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&desired).Error; err != nil {
				return count, err
			}
			count++
			continue
		}
		if err != nil {
			return count, err
		}
		desired.Severity = strongerSeverity(existing.Severity, desired.Severity)
		if sameEpisodeAggregation(existing, desired) {
			continue
		}
		// Review state/recovery are deliberately not rebuilt from rule hits.
		updates := map[string]any{
			"entity_type": desired.EntityType, "node": desired.Node, "device": desired.Device,
			"serial": desired.Serial, "model": desired.Model, "severity": desired.Severity,
			"evidence_id": desired.EvidenceID, "latest_snapshot_id": desired.LatestSnapshotID,
			"rule_version": desired.RuleVersion, "occurrence_count": desired.OccurrenceCount,
			"first_observed_at": desired.FirstObservedAt, "last_observed_at": desired.LastObservedAt,
		}
		if err := tx.Model(&api.DiskHealthEpisode{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func sameEpisodeAggregation(a, b api.DiskHealthEpisode) bool {
	return a.EntityType == b.EntityType && a.Node == b.Node && a.Device == b.Device &&
		a.Serial == b.Serial && a.Model == b.Model && a.Severity == b.Severity &&
		a.EvidenceID == b.EvidenceID && a.LatestSnapshotID == b.LatestSnapshotID &&
		a.RuleVersion == b.RuleVersion && a.OccurrenceCount == b.OccurrenceCount &&
		a.FirstObservedAt.Equal(b.FirstObservedAt) && a.LastObservedAt.Equal(b.LastObservedAt)
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
	return SnapshotView{SnapshotID: snapshot.ID, SnapshotKey: snapshot.SnapshotKey, Current: snapshot.Current, Result: row}, nil
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

package diskhealth

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

const InstallationViewContract = "disk-installation-view-v1"

// This describes the registry now, not installation membership at a report's
// historical cutoff. Snapshot.Current continues to mean per-entity latest.
type InstallationMembership struct {
	State          string     `json:"state"`
	InstallationID string     `json:"installation_id,omitempty"`
	ScopeID        string     `json:"scope_id,omitempty"`
	RosterVersion  string     `json:"roster_version,omitempty"`
	OpenedAt       *time.Time `json:"opened_at,omitempty"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
}

type DiskListResponse struct {
	Data    []SnapshotView
	Summary CurrentSummary
	Total   int
}

func normalizeInstallationView(view string) (string, error) {
	if view == "" {
		return "active", nil
	}
	switch view {
	case "active", "historical", "unregistered", "all":
		return view, nil
	default:
		return "", fmt.Errorf("unsupported disk installation view")
	}
}

func installationMemberships(tx *gorm.DB) (map[string]InstallationMembership, int, error) {
	var scopes []api.DiskObservationScope
	if err := tx.Find(&scopes).Error; err != nil {
		return nil, 0, err
	}
	scopeByID := make(map[string]api.DiskObservationScope, len(scopes))
	for _, scope := range scopes {
		scopeByID[scope.ScopeID] = scope
	}
	var installations []api.DiskInstallation
	if err := tx.Order("opened_at ASC, installation_id ASC").Find(&installations).Error; err != nil {
		return nil, 0, err
	}
	result := make(map[string]InstallationMembership)
	for _, installation := range installations {
		scope, ok := scopeByID[installation.ScopeID]
		if !ok {
			return nil, 0, fmt.Errorf("installation references missing observation scope")
		}
		state := "historical"
		if installation.State == "active" {
			state = "active"
		} else if installation.State != "superseded" {
			return nil, 0, fmt.Errorf("invalid installation state")
		}
		previous, exists := result[installation.EntityID]
		if exists && previous.State == "active" {
			if state == "active" {
				return nil, 0, fmt.Errorf("multiple active installations for one entity")
			}
			continue
		}
		opened := installation.OpenedAt
		result[installation.EntityID] = InstallationMembership{State: state, InstallationID: installation.InstallationID, ScopeID: scope.ScopeID, RosterVersion: strconv.FormatUint(scope.RosterVersion, 10), OpenedAt: &opened, ClosedAt: installation.ClosedAt}
	}
	return result, len(scopes), nil
}

func membershipFor(entityID string, registered map[string]InstallationMembership) InstallationMembership {
	if value, ok := registered[entityID]; ok {
		return value
	}
	return InstallationMembership{State: "unregistered"}
}

// ListDisks binds rows and all denominators to one database read transaction.
// No registry means zero confirmed installations, never a legacy fallback.
func (s *Service) ListDisks(options ListOptions) (DiskListResponse, error) {
	view, err := normalizeInstallationView(options.View)
	if err != nil {
		return DiskListResponse{}, err
	}
	result := DiskListResponse{Data: []SnapshotView{}, Summary: CurrentSummary{ContractVersion: InstallationViewContract, View: view, ByLevel: map[string]int{}, GeneratedAt: s.now().UTC()}}
	err = s.readDiskTransaction(func(tx *gorm.DB) error {
		memberships, scopes, err := installationMemberships(tx)
		if err != nil {
			return err
		}
		result.Summary.RegisteredScopeCount = scopes
		var all []api.PhysicalDiskSnapshot
		if err := tx.Where("current = ? AND entity_type <> ?", true, "memory_controller").Find(&all).Error; err != nil {
			return err
		}
		seen := make(map[string]bool, len(all))
		for _, snapshot := range all {
			if seen[snapshot.EntityID] {
				return fmt.Errorf("multiple latest snapshots for one entity")
			}
			seen[snapshot.EntityID] = true
			membership := membershipFor(snapshot.EntityID, memberships)
			switch membership.State {
			case "active":
				result.Summary.ActiveCount++
			case "historical":
				result.Summary.HistoricalCount++
			default:
				result.Summary.UnregisteredCount++
			}
			if view != "all" && membership.State != view {
				continue
			}
			result.Summary.Total++
			result.Summary.ByLevel[snapshot.Level]++
			if snapshot.AssessmentStatus == "assessed" {
				result.Summary.Assessed++
			} else {
				result.Summary.Unknown++
			}
			if snapshot.Level == "attention" || snapshot.Level == "warning" || snapshot.Level == "critical" {
				result.Summary.RiskCount++
			}
			if observationStatus(snapshot.ObservedAt, snapshot.AvailableAt, result.Summary.GeneratedAt).State == "stale" {
				result.Summary.StaleCount++
			}
		}
		for entity, membership := range memberships {
			if membership.State == "active" && !seen[entity] {
				return fmt.Errorf("active installation has no latest evidence")
			}
		}
		var filtered []api.PhysicalDiskSnapshot
		if err := applySnapshotFilters(tx.Where("current = ? AND entity_type <> ?", true, "memory_controller"), options).Find(&filtered).Error; err != nil {
			return err
		}
		selected := make([]api.PhysicalDiskSnapshot, 0, len(filtered))
		for _, snapshot := range filtered {
			if view == "all" || membershipFor(snapshot.EntityID, memberships).State == view {
				selected = append(selected, snapshot)
			}
		}
		sortSnapshots(selected, options.Order)
		result.Total = len(selected)
		limit, offset := normalizePage(options.Limit, options.Offset, 100, 2000)
		if offset > len(selected) {
			offset = len(selected)
		}
		end := offset + limit
		if end > len(selected) {
			end = len(selected)
		}
		for _, snapshot := range selected[offset:end] {
			row, err := snapshotView(snapshot)
			if err != nil {
				return err
			}
			row.Observation = observationStatus(row.ObservedAt, row.AvailableAt, result.Summary.GeneratedAt)
			row.Installation = membershipFor(row.EntityID, memberships)
			result.Data = append(result.Data, row)
		}
		return nil
	})
	return result, err
}

func (s *Service) SummaryForView(view string) (CurrentSummary, error) {
	result, err := s.ListDisks(ListOptions{View: view, Limit: 1})
	return result.Summary, err
}

func (s *Service) installationMembership(entityID string) (InstallationMembership, error) {
	rows, _, err := installationMemberships(s.db.DB)
	return membershipFor(entityID, rows), err
}

// Enrolment binds an entity to an immutable observation scope. Report replay
// cannot move it to another host even after its installation has been closed.
func validateRegisteredSnapshotBinding(tx *gorm.DB, row Result) error {
	var installations []api.DiskInstallation
	if err := tx.Where("entity_id = ?", row.EntityID).Find(&installations).Error; err != nil {
		return err
	}
	for _, installation := range installations {
		var scope api.DiskObservationScope
		if err := tx.Where("scope_id = ?", installation.ScopeID).First(&scope).Error; err != nil {
			return err
		}
		if row.IdentityStatus != "bound" || (row.EntityType != "disk" && row.EntityType != "nvme_controller") || (strings.HasPrefix(scope.AssetKey, "fixture:") && row.SourceKind != "fixture") || (!strings.HasPrefix(scope.AssetKey, "fixture:") && row.SourceKind != "operator_snapshot") || row.Node != scope.Node || row.Scope != scope.SnapshotScope || row.Generation != scope.SnapshotGeneration {
			return fmt.Errorf("registered entity observation scope mismatch")
		}
	}
	return nil
}

func (s *Service) readDiskTransaction(fn func(*gorm.DB) error) error {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.db.Dialector.Name() == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	return s.db.Transaction(fn, opts)
}
func (s *Service) installationRegistry(scopeID string, limit, offset int) ([]api.DiskObservationScope, []api.DiskInstallation, int64, error) {
	scopes := []api.DiskObservationScope{}
	installations := []api.DiskInstallation{}
	var total int64
	err := s.readDiskTransaction(func(tx *gorm.DB) error {
		query := tx.Model(&api.DiskInstallation{})
		scopeQuery := tx.Model(&api.DiskObservationScope{})
		if scopeID != "" {
			query = query.Where("scope_id = ?", scopeID)
			scopeQuery = scopeQuery.Where("scope_id = ?", scopeID)
		}
		if err := query.Count(&total).Error; err != nil {
			return err
		}
		if err := query.Order("scope_id ASC, opened_at DESC, installation_id ASC").Limit(limit).Offset(offset).Find(&installations).Error; err != nil {
			return err
		}
		return scopeQuery.Order("scope_id ASC").Limit(2000).Find(&scopes).Error
	})
	return scopes, installations, total, err
}

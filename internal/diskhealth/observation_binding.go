package diskhealth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

const ObservationBindingSchema = "disk-observation-binding-v1"
const ObservationPersistenceSchema = "disk-observation-persistence-v1"

// ObservationBinding freezes an already reconciled installation roster. It is
// not a roster writer and cannot enroll or reactivate an installation.
type ObservationBinding struct {
	Schema                string   `json:"schema"`
	ScopeID               string   `json:"scope_id"`
	ExpectedRosterVersion string   `json:"expected_roster_version"`
	AssetIdentitySHA256   string   `json:"asset_identity_sha256"`
	Node                  string   `json:"node"`
	SnapshotScope         string   `json:"snapshot_scope"`
	SnapshotGeneration    string   `json:"snapshot_generation"`
	EntityIDs             []string `json:"entity_ids"`
	raw                   []byte
	canonicalSHA          string
}

type ObservationPersistedEntity struct {
	EntityID     string    `json:"entity_id"`
	SnapshotID   string    `json:"snapshot_id"`
	ObservedAt   time.Time `json:"observed_at"`
	AvailableAt  time.Time `json:"available_at"`
	InputSHA256  string    `json:"input_sha256"`
	ReportSHA256 string    `json:"report_sha256"`
}

type ObservationPersistenceReceipt struct {
	Schema           string                       `json:"schema"`
	Verified         bool                         `json:"verified"`
	BindingSHA256    string                       `json:"binding_sha256"`
	ReportSHA256     string                       `json:"report_sha256"`
	ScopeID          string                       `json:"scope_id"`
	RosterVersion    string                       `json:"roster_version"`
	CompletedAt      time.Time                    `json:"completed_at"`
	SnapshotsCreated int                          `json:"snapshots_created"`
	Duplicate        int                          `json:"duplicate"`
	Entities         []ObservationPersistedEntity `json:"entities"`
}

func ParseObservationBinding(raw []byte) (ObservationBinding, error) {
	var binding ObservationBinding
	if len(raw) == 0 || len(raw) > InstallationManifestMaxBytes || ValidateJSON(raw) != nil {
		return binding, fmt.Errorf("invalid observation binding JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&binding) != nil || d.Decode(new(any)) != io.EOF {
		return ObservationBinding{}, fmt.Errorf("invalid observation binding fields")
	}
	if err := validateObservationBinding(binding); err != nil {
		return ObservationBinding{}, err
	}
	canonical, _ := json.Marshal(binding)
	binding.raw = append([]byte(nil), raw...)
	binding.canonicalSHA = Digest(canonical)
	return binding, nil
}

func validateObservationBinding(b ObservationBinding) error {
	version, err := strconv.ParseUint(b.ExpectedRosterVersion, 10, 64)
	if b.Schema != ObservationBindingSchema || err != nil || version == 0 || strconv.FormatUint(version, 10) != b.ExpectedRosterVersion || !installationSHA(b.AssetIdentitySHA256) || len(b.EntityIDs) == 0 || len(b.EntityIDs) > MaxInputs {
		return fmt.Errorf("invalid observation binding contract")
	}
	for _, value := range []string{b.ScopeID, b.Node, b.SnapshotScope, b.SnapshotGeneration} {
		if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("invalid observation binding scope")
		}
	}
	for i, id := range b.EntityIDs {
		if !installationSHA(id) || (i > 0 && id <= b.EntityIDs[i-1]) {
			return fmt.Errorf("observation entity IDs must be sorted and unique")
		}
	}
	return nil
}

func (b ObservationBinding) SHA256() (string, error) {
	if err := validateObservationBinding(b); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	if len(b.raw) != 0 {
		if Digest(canonical) != b.canonicalSHA {
			return "", fmt.Errorf("observation binding changed after parsing")
		}
		return Digest(b.raw), nil
	}
	return Digest(canonical), nil
}

// CheckObservationBinding is a consistent read-only preflight, with no schema
// migration and no locking UPDATE. Persistence rechecks under the writer lock.
func (s *Service) CheckObservationBinding(ctx context.Context, b ObservationBinding) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("disk health service requires a database")
	}
	if _, err := b.SHA256(); err != nil {
		return err
	}
	options := &sql.TxOptions{ReadOnly: true}
	if s.db.Dialector.Name() == "postgres" {
		options.Isolation = sql.LevelRepeatableRead
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return checkObservationBinding(tx, b) }, options)
}

func checkObservationBinding(tx *gorm.DB, b ObservationBinding) error {
	var scope api.DiskObservationScope
	if err := tx.Where("scope_id = ?", b.ScopeID).First(&scope).Error; err != nil {
		return fmt.Errorf("observation scope unavailable")
	}
	if strconv.FormatUint(scope.RosterVersion, 10) != b.ExpectedRosterVersion || scope.AssetIdentitySHA256 != b.AssetIdentitySHA256 || scope.Node != b.Node || scope.SnapshotScope != b.SnapshotScope || scope.SnapshotGeneration != b.SnapshotGeneration {
		return fmt.Errorf("observation binding or expected roster version changed")
	}
	var identity []byte
	if strings.HasPrefix(scope.AssetKey, "fixture:") {
		identity, _ = json.Marshal([]string{"synthetic-fixture-v1", scope.AssetKey})
	} else {
		var asset api.InfrastructureAsset
		if err := tx.Where("asset_key = ?", scope.AssetKey).First(&asset).Error; err != nil {
			return fmt.Errorf("observation inventory asset unavailable")
		}
		if !asset.InUse || !asset.Present {
			return fmt.Errorf("observation inventory asset is not present and in use")
		}
		identity, _ = json.Marshal([]string{asset.AssetKey, asset.Source, asset.SourceID, asset.DataCenterID, asset.SerialNumber})
	}
	if Digest(identity) != b.AssetIdentitySHA256 {
		return fmt.Errorf("observation inventory asset identity changed")
	}
	var active []api.DiskInstallation
	if err := tx.Where("scope_id = ? AND state = ?", b.ScopeID, "active").Order("entity_id ASC").Find(&active).Error; err != nil {
		return err
	}
	if len(active) != len(b.EntityIDs) {
		return fmt.Errorf("observation active entity set changed")
	}
	for i, row := range active {
		if row.EntityID != b.EntityIDs[i] || row.ActiveEntityID == nil || *row.ActiveEntityID != row.EntityID {
			return fmt.Errorf("observation active entity set changed")
		}
	}
	return nil
}

// PersistReportWithBinding rechecks the frozen scope after acquiring the same
// identity lock used by reconciliation. The deadline covers SQL and Commit.
// A commit/receipt uncertainty must pause the caller, never trigger an automatic retry.
func (s *Service) PersistReportWithBinding(ctx context.Context, report Report, reportSHA string, b ObservationBinding) (ObservationPersistenceReceipt, error) {
	var receipt ObservationPersistenceReceipt
	if s == nil || s.db == nil {
		return receipt, fmt.Errorf("disk health service requires a database")
	}
	sha, err := b.SHA256()
	if err != nil {
		return receipt, err
	}
	if report.Schema != SchemaVersion || !installationSHA(reportSHA) || len(report.Rows) != len(b.EntityIDs) {
		return receipt, fmt.Errorf("bound observation report contract mismatch")
	}
	seen := map[string]bool{}
	for _, row := range report.Rows {
		if seen[row.EntityID] || !sortContains(b.EntityIDs, row.EntityID) || row.IdentityStatus != "bound" || (row.EntityType != "disk" && row.EntityType != "nvme_controller") || row.Node != b.Node || row.Scope != b.SnapshotScope || row.Generation != b.SnapshotGeneration || !installationSHA(row.InputSHA256) || row.ObservedAt.IsZero() || row.AvailableAt.Before(row.ObservedAt) || row.AvailableAt.After(report.AsOf) {
			return receipt, fmt.Errorf("bound observation report identity mismatch")
		}
		seen[row.EntityID] = true
	}
	var result PersistResult
	var snapshots []api.PhysicalDiskSnapshot
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockInstallationIdentity(tx); err != nil {
			return err
		}
		if err := checkObservationBinding(tx, b); err != nil {
			return err
		}
		var err error
		snapshots, err = persistReportTransaction(tx, report, reportSHA, &result)
		if err != nil {
			return err
		}
		for _, snapshot := range snapshots {
			if snapshot.ReportSHA256 != reportSHA {
				return fmt.Errorf("bound observation duplicate report SHA mismatch")
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return receipt, err
	}
	completed := time.Now().UTC()
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if deadline, ok := ctx.Deadline(); ok && completed.After(deadline) {
		return receipt, context.DeadlineExceeded
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].EntityID < snapshots[j].EntityID })
	receipt = ObservationPersistenceReceipt{Schema: ObservationPersistenceSchema, Verified: true, BindingSHA256: sha, ReportSHA256: reportSHA, ScopeID: b.ScopeID, RosterVersion: b.ExpectedRosterVersion, CompletedAt: completed, SnapshotsCreated: result.SnapshotsCreated, Duplicate: result.SnapshotsDuplicate, Entities: make([]ObservationPersistedEntity, 0, len(snapshots))}
	for _, snapshot := range snapshots {
		receipt.Entities = append(receipt.Entities, ObservationPersistedEntity{EntityID: snapshot.EntityID, SnapshotID: strconv.FormatUint(uint64(snapshot.ID), 10), ObservedAt: snapshot.ObservedAt.UTC(), AvailableAt: snapshot.AvailableAt.UTC(), InputSHA256: snapshot.InputSHA256, ReportSHA256: snapshot.ReportSHA256})
	}
	return receipt, nil
}

func sortContains(ids []string, id string) bool {
	index := sort.SearchStrings(ids, id)
	return index < len(ids) && ids[index] == id
}

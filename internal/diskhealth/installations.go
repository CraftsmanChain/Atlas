package diskhealth

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"atlas/pkg/api"
)

const InstallationReconcileSchema = "disk-installation-reconcile-v1"
const InstallationManifestMaxBytes = 256 * 1024
const installationMemberLimit = 200

type InstallationReconcileMember struct {
	EntityID    string `json:"entity_id"`
	SnapshotKey string `json:"snapshot_key"`
	InputSHA256 string `json:"input_sha256"`
}

type InstallationReconcileClosure struct {
	InstallationID string `json:"installation_id"`
	Reason         string `json:"reason"`
	EvidenceSHA256 string `json:"evidence_sha256"`
}

type InstallationReconcileFailure struct {
	Member string `json:"member"`
	Code   string `json:"code"`
}

// InstallationReconcileManifest explicitly describes the complete bounded
// observation and any closures. Omitting an installed member never closes it.
// Evidence fields contain digests only; source payloads and credentials do not
// belong in this local operator contract.
type InstallationReconcileManifest struct {
	Schema                     string                         `json:"schema"`
	ScopeID                    string                         `json:"scope_id"`
	AssetKey                   string                         `json:"asset_key"`
	HostGeneration             string                         `json:"host_generation"`
	Node                       string                         `json:"node"`
	SnapshotScope              string                         `json:"snapshot_scope"`
	SnapshotGeneration         string                         `json:"snapshot_generation"`
	Disposition                string                         `json:"disposition"`
	HostIdentityEvidenceSHA256 string                         `json:"host_identity_evidence_sha256"`
	ExpectedRosterVersion      uint64                         `json:"expected_roster_version"`
	Actor                      string                         `json:"actor"`
	ObservedAt                 time.Time                      `json:"observed_at"`
	AvailableAt                time.Time                      `json:"available_at"`
	Complete                   bool                           `json:"complete"`
	EnumerationBoundary        string                         `json:"enumeration_boundary"`
	ExpectedEntityIDs          []string                       `json:"expected_entity_ids"`
	KnownInstallationIDs       []string                       `json:"known_installation_ids"`
	Members                    []InstallationReconcileMember  `json:"members"`
	Closures                   []InstallationReconcileClosure `json:"closures"`
	Failures                   []InstallationReconcileFailure `json:"failures"`
	rawJSON                    []byte
}

type InstallationReconcileResult struct {
	Schema                string `json:"schema"`
	ScopeID               string `json:"scope_id"`
	ManifestSHA256        string `json:"manifest_sha256"`
	ExpectedRosterVersion uint64 `json:"expected_roster_version"`
	RosterVersion         uint64 `json:"roster_version"`
	OpenedCount           int    `json:"opened_count"`
	ClosedCount           int    `json:"closed_count"`
	Idempotent            bool   `json:"idempotent"`
}

// ParseInstallationReconcileManifest rejects unknown/duplicate fields, bounds
// the entire payload, and retains the exact bytes used by the audit SHA.
func ParseInstallationReconcileManifest(raw []byte) (InstallationReconcileManifest, error) {
	var m InstallationReconcileManifest
	if len(raw) == 0 || len(raw) > InstallationManifestMaxBytes {
		return m, fmt.Errorf("installation manifest must be 1..%d bytes", InstallationManifestMaxBytes)
	}
	if err := ValidateJSON(raw); err != nil {
		return m, fmt.Errorf("invalid installation manifest JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, fmt.Errorf("invalid or unknown installation manifest field")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, name := range []string{"expected_roster_version", "complete", "expected_entity_ids", "known_installation_ids", "members", "closures", "failures"} {
		value, present := fields[name]
		if !present || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return m, fmt.Errorf("missing explicit installation %s", name)
		}
	}
	if err := validateInstallationManifest(m); err != nil {
		return m, err
	}
	m.rawJSON = append([]byte(nil), raw...)
	return m, nil
}

var installationIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]*$`)

func installationText(value string, limit int) bool {
	return len(value) > 0 && len(value) <= limit && installationIdentifier.MatchString(value)
}

func installationBoundary(value string) bool {
	if len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune(" _.:/-,()[]", char)) {
			return false
		}
	}
	return !strings.Contains(value, "://")
}

func installationSHA(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) || value == strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateInstallationManifest(m InstallationReconcileManifest) error {
	if m.Schema != InstallationReconcileSchema {
		return fmt.Errorf("unsupported installation manifest schema")
	}
	for _, field := range []struct {
		name, value string
		limit       int
	}{
		{"scope_id", m.ScopeID, 128}, {"asset_key", m.AssetKey, 256},
		{"host_generation", m.HostGeneration, 128}, {"node", m.Node, 256},
		{"snapshot_scope", m.SnapshotScope, 256}, {"snapshot_generation", m.SnapshotGeneration, 128},
		{"actor", m.Actor, 128},
	} {
		if !installationText(field.value, field.limit) {
			return fmt.Errorf("invalid installation %s", field.name)
		}
	}
	if !installationBoundary(m.EnumerationBoundary) {
		return fmt.Errorf("invalid installation enumeration boundary")
	}
	if net.ParseIP(m.AssetKey) != nil || strings.HasPrefix(m.AssetKey, "/") || strings.HasPrefix(m.AssetKey, "serial:") {
		return fmt.Errorf("asset_key must be a stable inventory reference")
	}
	if m.Disposition != "test" && m.Disposition != "fleet" {
		return fmt.Errorf("installation disposition must be test or fleet")
	}
	if strings.HasPrefix(m.AssetKey, "fixture:") && m.Disposition != "test" {
		return fmt.Errorf("fixture scope requires test disposition")
	}
	if m.AssetKey == "fixture:" {
		return fmt.Errorf("fixture scope requires a named host namespace")
	}
	if !installationSHA(m.HostIdentityEvidenceSHA256) {
		return fmt.Errorf("invalid host identity evidence SHA256")
	}
	if m.ObservedAt.IsZero() || m.AvailableAt.IsZero() || m.AvailableAt.Before(m.ObservedAt) {
		return fmt.Errorf("invalid installation observation cutoff")
	}
	if m.ExpectedRosterVersion == ^uint64(0) || m.ExpectedRosterVersion >= uint64(1<<63-1) {
		return fmt.Errorf("installation roster version exhausted")
	}
	if len(m.Members) > installationMemberLimit || len(m.Closures) > installationMemberLimit || len(m.ExpectedEntityIDs) > installationMemberLimit || len(m.KnownInstallationIDs) > installationMemberLimit || len(m.Failures) > installationMemberLimit {
		return fmt.Errorf("installation manifest exceeds member limit")
	}
	if m.ExpectedEntityIDs == nil || m.KnownInstallationIDs == nil || m.Members == nil || m.Closures == nil || m.Failures == nil {
		return fmt.Errorf("installation scope arrays must be explicit, including empty arrays")
	}
	expected := make(map[string]bool)
	for _, id := range m.ExpectedEntityIDs {
		if !installationSHA(id) || expected[id] {
			return fmt.Errorf("invalid or duplicate expected entity")
		}
		expected[id] = true
	}
	members := make(map[string]bool)
	for _, member := range m.Members {
		if !installationSHA(member.EntityID) || !installationSHA(member.SnapshotKey) || !installationSHA(member.InputSHA256) || members[member.EntityID] || !expected[member.EntityID] {
			return fmt.Errorf("invalid, duplicate, or unexpected installation member")
		}
		members[member.EntityID] = true
	}
	if len(expected) != len(members) {
		return fmt.Errorf("expected entities and observed members differ")
	}
	known := make(map[string]bool)
	for _, id := range m.KnownInstallationIDs {
		if !installationSHA(id) || known[id] {
			return fmt.Errorf("invalid or duplicate known installation")
		}
		known[id] = true
	}
	closures := make(map[string]bool)
	for _, closure := range m.Closures {
		if !installationSHA(closure.InstallationID) || !known[closure.InstallationID] || closures[closure.InstallationID] || !installationText(closure.Reason, 128) || !installationSHA(closure.EvidenceSHA256) {
			return fmt.Errorf("invalid, duplicate, or unknown installation closure")
		}
		closures[closure.InstallationID] = true
	}
	for _, failure := range m.Failures {
		if !installationText(failure.Member, 256) || !installationText(failure.Code, 128) {
			return fmt.Errorf("invalid installation failure declaration")
		}
	}
	return nil
}

// installationManifestBytes prevents modification of a parsed manifest after
// its raw evidence was bound. Programmatic callers may use canonical JSON.
func installationManifestBytes(m InstallationReconcileManifest) ([]byte, error) {
	encoded, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(m.rawJSON) == 0 {
		return encoded, nil
	}
	parsed, err := ParseInstallationReconcileManifest(m.rawJSON)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(parsed)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return nil, fmt.Errorf("installation manifest changed after SHA binding")
	}
	return append([]byte(nil), m.rawJSON...), nil
}

// ReconcileInstallations is the sole roster writer. PostgreSQL's transaction
// advisory lock also covers first-scope creation; row locks alone do not. SQLite
// obtains a write reservation before reading any roster state. Unique active
// entity IDs protect against writers reconciling different scopes concurrently.
func (s *Service) ReconcileInstallations(m InstallationReconcileManifest, manifestSHA256 string) (InstallationReconcileResult, error) {
	result := InstallationReconcileResult{}
	if s == nil || s.db == nil {
		return result, fmt.Errorf("disk health service requires a database")
	}
	if err := validateInstallationManifest(m); err != nil {
		return result, err
	}
	raw, err := installationManifestBytes(m)
	if err != nil {
		return result, err
	}
	if len(raw) > InstallationManifestMaxBytes || !installationSHA(manifestSHA256) || Digest(raw) != manifestSHA256 {
		return result, fmt.Errorf("installation manifest SHA256 mismatch")
	}
	if !m.Complete || len(m.Failures) != 0 {
		return result, fmt.Errorf("incomplete installation enumeration cannot reconcile")
	}
	if m.AvailableAt.After(s.now().UTC()) {
		return result, fmt.Errorf("installation cutoff is in the future")
	}
	for attempt := 0; attempt < 4; attempt++ {
		result = InstallationReconcileResult{}
		err = s.db.Transaction(func(tx *gorm.DB) error {
			if err := lockInstallationIdentity(tx); err != nil {
				return err
			}
			if err := lockInstallationScope(tx, m.ScopeID); err != nil {
				return err
			}
			var prior api.DiskInstallationReconcile
			found := tx.Where("manifest_sha256 = ?", manifestSHA256).First(&prior).Error
			if found == nil {
				if prior.ScopeID != m.ScopeID || prior.ManifestJSON != string(raw) {
					return fmt.Errorf("installation manifest audit conflict")
				}
				result = installationAuditResult(prior, true)
				return nil
			}
			if !errors.Is(found, gorm.ErrRecordNotFound) {
				return found
			}
			return reconcileInstallationTransaction(tx, m, manifestSHA256, raw, s.now().UTC(), &result)
		})
		if err == nil || !installationRetryable(err) {
			return result, err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return InstallationReconcileResult{}, err
}

// All snapshot imports and roster writers share this lock. Otherwise the
// first registration and a concurrent import for the same entity on a different
// node could both pass their binding checks before either transaction commits.
func lockInstallationIdentity(tx *gorm.DB) error {
	switch tx.Dialector.Name() {
	case "postgres":
		return tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(0x41544c4153444953)).Error
	case "sqlite":
		return tx.Exec("UPDATE disk_observation_scopes SET roster_version = roster_version WHERE scope_id = ?", "").Error
	default:
		return fmt.Errorf("unsupported installation database driver")
	}
}

func lockInstallationScope(tx *gorm.DB, scopeID string) error {
	switch tx.Dialector.Name() {
	case "postgres":
		hash, _ := hex.DecodeString(Digest([]byte("disk-installation-scope:" + scopeID)))
		key := int64(binary.BigEndian.Uint64(hash[:8]))
		return tx.Exec("SELECT pg_advisory_xact_lock(?)", key).Error
	case "sqlite":
		return tx.Exec("UPDATE disk_observation_scopes SET roster_version = roster_version WHERE scope_id = ?", scopeID).Error
	default:
		return fmt.Errorf("unsupported installation database driver")
	}
}

func installationRetryable(err error) bool {
	// Retry the entire transaction only. Never retry a partially applied step.
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "database is locked") || strings.Contains(text, "database table is locked") || strings.Contains(text, "sqlstate 40001") || strings.Contains(text, "sqlstate 40p01")
}

func installationAuditResult(a api.DiskInstallationReconcile, duplicate bool) InstallationReconcileResult {
	return InstallationReconcileResult{Schema: InstallationReconcileSchema, ScopeID: a.ScopeID, ManifestSHA256: a.ManifestSHA256, ExpectedRosterVersion: a.ExpectedRosterVersion, RosterVersion: a.RosterVersion, OpenedCount: a.OpenedCount, ClosedCount: a.ClosedCount, Idempotent: duplicate}
}

func reconcileInstallationTransaction(tx *gorm.DB, m InstallationReconcileManifest, sha string, raw []byte, now time.Time, result *InstallationReconcileResult) error {
	var scope api.DiskObservationScope
	err := tx.Where("scope_id = ?", m.ScopeID).First(&scope).Error
	newScope := errors.Is(err, gorm.ErrRecordNotFound)
	if err != nil && !newScope {
		return err
	}
	if scope.RosterVersion != m.ExpectedRosterVersion {
		return fmt.Errorf("installation expected roster version conflict")
	}
	if !newScope {
		if scope.AssetKey != m.AssetKey || scope.HostGeneration != m.HostGeneration || scope.Node != m.Node || scope.SnapshotScope != m.SnapshotScope || scope.SnapshotGeneration != m.SnapshotGeneration || scope.Disposition != m.Disposition || scope.HostIdentityEvidenceSHA256 != m.HostIdentityEvidenceSHA256 {
			return fmt.Errorf("installation scope identity binding conflict")
		}
		if m.ObservedAt.Before(scope.ObservedAt) || m.AvailableAt.Before(scope.AvailableAt) {
			return fmt.Errorf("installation cutoff cannot move backward")
		}
	}
	fixture := strings.HasPrefix(m.AssetKey, "fixture:")
	var assetIdentitySHA string
	if !fixture {
		var asset api.InfrastructureAsset
		if err := tx.Where("asset_key = ?", m.AssetKey).First(&asset).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("installation inventory asset not found")
			}
			return err
		}
		if m.Disposition == "fleet" && (!asset.InUse || !asset.Present) {
			return fmt.Errorf("fleet installation asset is not present and in use")
		}
		identity, _ := json.Marshal([]string{asset.AssetKey, asset.Source, asset.SourceID, asset.DataCenterID, asset.SerialNumber})
		assetIdentitySHA = Digest(identity)
	} else {
		identity, _ := json.Marshal([]string{"synthetic-fixture-v1", m.AssetKey})
		assetIdentitySHA = Digest(identity)
	}
	if !newScope && scope.AssetIdentitySHA256 != assetIdentitySHA {
		return fmt.Errorf("installation inventory asset identity changed")
	}
	// A host has one active roster. A different scope/generation must explicitly
	// close its previous roster before registering another; changing a scope ID
	// or snapshot generation cannot create a parallel installed denominator.
	var otherActive int64
	if err := tx.Model(&api.DiskInstallation{}).Joins("JOIN disk_observation_scopes AS installation_scope ON installation_scope.scope_id = disk_installations.scope_id").Where("installation_scope.asset_key = ? AND disk_installations.scope_id <> ? AND disk_installations.state = ?", m.AssetKey, m.ScopeID, "active").Count(&otherActive).Error; err != nil {
		return err
	}
	if otherActive != 0 {
		return fmt.Errorf("installation asset already has an active observation scope")
	}
	var duplicateScope int64
	if err := tx.Model(&api.DiskObservationScope{}).Where("asset_key = ? AND host_generation = ? AND snapshot_scope = ? AND snapshot_generation = ? AND scope_id <> ?", m.AssetKey, m.HostGeneration, m.SnapshotScope, m.SnapshotGeneration, m.ScopeID).Count(&duplicateScope).Error; err != nil {
		return err
	}
	if duplicateScope != 0 {
		return fmt.Errorf("installation host binding already has an observation scope")
	}
	var active []api.DiskInstallation
	if err := tx.Where("scope_id = ? AND state = ?", m.ScopeID, "active").Find(&active).Error; err != nil {
		return err
	}
	activeByID, activeByEntity := map[string]api.DiskInstallation{}, map[string]api.DiskInstallation{}
	actualIDs := make([]string, 0, len(active))
	for _, installation := range active {
		if installation.ActiveEntityID == nil || *installation.ActiveEntityID != installation.EntityID || installation.ClosedAt != nil {
			return fmt.Errorf("installation registry integrity conflict")
		}
		activeByID[installation.InstallationID], activeByEntity[installation.EntityID] = installation, installation
		actualIDs = append(actualIDs, installation.InstallationID)
	}
	if !installationSameSet(actualIDs, m.KnownInstallationIDs) {
		return fmt.Errorf("known installations do not match complete active roster")
	}
	memberByEntity := map[string]InstallationReconcileMember{}
	for _, member := range m.Members {
		var snapshot api.PhysicalDiskSnapshot
		if err := tx.Where("snapshot_key = ? AND entity_id = ? AND input_sha256 = ?", member.SnapshotKey, member.EntityID, member.InputSHA256).First(&snapshot).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("installation member snapshot evidence not found")
			}
			return err
		}
		if snapshot.IdentityStatus != "bound" || snapshot.Serial == "" || (snapshot.EntityType != "disk" && snapshot.EntityType != "nvme_controller") || snapshot.Node != m.Node || snapshot.Scope != m.SnapshotScope || snapshot.Generation != m.SnapshotGeneration {
			return fmt.Errorf("installation member snapshot identity binding conflict")
		}
		if (fixture && snapshot.SourceKind != "fixture") || (!fixture && snapshot.SourceKind != "operator_snapshot") {
			return fmt.Errorf("installation member source kind conflict")
		}
		if snapshot.ObservedAt.IsZero() || snapshot.AvailableAt.IsZero() || snapshot.AvailableAt.Before(snapshot.ObservedAt) || snapshot.ObservedAt.After(m.ObservedAt) || snapshot.AvailableAt.After(m.AvailableAt) {
			return fmt.Errorf("installation member evidence crosses cutoff")
		}
		var conflicts int64
		// EntityID omits Node, so validating only the supplied/current snapshot
		// would allow already mixed historical identities to enter the registry.
		if err := tx.Model(&api.PhysicalDiskSnapshot{}).Where("entity_id = ? AND (COALESCE(node, '') <> ? OR COALESCE(scope, '') <> ? OR COALESCE(generation, '') <> ? OR COALESCE(identity_status, '') <> ? OR COALESCE(source_kind, '') <> ?)", member.EntityID, m.Node, m.SnapshotScope, m.SnapshotGeneration, "bound", snapshot.SourceKind).Count(&conflicts).Error; err != nil {
			return err
		}
		if conflicts != 0 {
			return fmt.Errorf("installation member history contains identity conflicts")
		}
		var installed []api.DiskInstallation
		if err := tx.Where("entity_id = ?", member.EntityID).Find(&installed).Error; err != nil {
			return err
		}
		for _, existing := range installed {
			if existing.ScopeID != m.ScopeID {
				return fmt.Errorf("installation member belongs to a different observation scope")
			}
		}
		memberByEntity[member.EntityID] = member
	}
	for _, closure := range m.Closures {
		old, ok := activeByID[closure.InstallationID]
		if !ok || m.ObservedAt.Before(old.OpenedAt) {
			return fmt.Errorf("installation closure is not active at cutoff")
		}
		if _, observed := memberByEntity[old.EntityID]; observed {
			return fmt.Errorf("installation closure contradicts observed active member")
		}
	}
	version := m.ExpectedRosterVersion + 1
	opened, closed := 0, 0
	for _, closure := range m.Closures {
		update := tx.Model(&api.DiskInstallation{}).Where("installation_id = ? AND scope_id = ? AND state = ?", closure.InstallationID, m.ScopeID, "active").Updates(map[string]any{
			"state": "superseded", "active_entity_id": nil, "closed_at": m.ObservedAt.UTC(), "close_evidence_sha256": closure.EvidenceSHA256, "close_reason": closure.Reason, "close_roster_version": version,
		})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("installation closure changed concurrently")
		}
		closed++
	}
	for _, member := range m.Members {
		if _, exists := activeByEntity[member.EntityID]; exists {
			continue
		}
		identity, _ := json.Marshal([]string{m.ScopeID, sha, member.EntityID})
		entity := member.EntityID
		installation := api.DiskInstallation{InstallationID: Digest(identity), ScopeID: m.ScopeID, EntityID: member.EntityID, State: "active", ActiveEntityID: &entity, OpenedAt: m.ObservedAt.UTC(), OpenEvidenceSHA256: member.InputSHA256, OpenRosterVersion: version}
		if err := tx.Create(&installation).Error; err != nil {
			return err
		}
		opened++
	}
	scope = api.DiskObservationScope{ScopeID: m.ScopeID, AssetKey: m.AssetKey, AssetIdentitySHA256: assetIdentitySHA, HostGeneration: m.HostGeneration, Node: m.Node, SnapshotScope: m.SnapshotScope, SnapshotGeneration: m.SnapshotGeneration, Disposition: m.Disposition, HostIdentityEvidenceSHA256: m.HostIdentityEvidenceSHA256, RosterVersion: version, ManifestSHA256: sha, ObservedAt: m.ObservedAt.UTC(), AvailableAt: m.AvailableAt.UTC(), ReconciledAt: now}
	if newScope {
		if err := tx.Create(&scope).Error; err != nil {
			return err
		}
	} else {
		update := tx.Model(&api.DiskObservationScope{}).Where("scope_id = ? AND roster_version = ?", m.ScopeID, m.ExpectedRosterVersion).Updates(map[string]any{"roster_version": version, "manifest_sha256": sha, "observed_at": scope.ObservedAt, "available_at": scope.AvailableAt, "reconciled_at": now})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("installation expected roster version conflict")
		}
	}
	audit := api.DiskInstallationReconcile{ScopeID: m.ScopeID, ManifestSHA256: sha, ExpectedRosterVersion: m.ExpectedRosterVersion, RosterVersion: version, Actor: m.Actor, ObservedAt: scope.ObservedAt, AvailableAt: scope.AvailableAt, AppliedAt: now, OpenedCount: opened, ClosedCount: closed, ManifestJSON: string(raw)}
	if err := tx.Create(&audit).Error; err != nil {
		return err
	}
	*result = installationAuditResult(audit, false)
	return nil
}

func installationSameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

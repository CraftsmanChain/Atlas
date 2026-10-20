package api

import "time"

// DiskObservationScope binds a roster to a stable inventory asset and an
// installation generation. Node is only an observation endpoint, never a host ID.
type DiskObservationScope struct {
	ScopeID                    string    `json:"scope_id" gorm:"primaryKey;size:128"`
	AssetKey                   string    `json:"asset_key" gorm:"index;uniqueIndex:idx_disk_scope_host_binding;size:256;not null"`
	AssetIdentitySHA256        string    `json:"asset_identity_sha256" gorm:"size:64;not null"`
	HostGeneration             string    `json:"host_generation" gorm:"index;uniqueIndex:idx_disk_scope_host_binding;size:128;not null"`
	Node                       string    `json:"node" gorm:"index;size:256;not null"`
	SnapshotScope              string    `json:"snapshot_scope" gorm:"uniqueIndex:idx_disk_scope_host_binding;size:256;not null"`
	SnapshotGeneration         string    `json:"snapshot_generation" gorm:"uniqueIndex:idx_disk_scope_host_binding;size:128;not null"`
	Disposition                string    `json:"disposition" gorm:"size:16;not null"`
	HostIdentityEvidenceSHA256 string    `json:"host_identity_evidence_sha256" gorm:"size:64;not null"`
	RosterVersion              uint64    `json:"roster_version" gorm:"not null"`
	ManifestSHA256             string    `json:"manifest_sha256" gorm:"size:64;not null"`
	ObservedAt                 time.Time `json:"observed_at"`
	AvailableAt                time.Time `json:"available_at"`
	ReconciledAt               time.Time `json:"reconciled_at"`
	CreatedAt                  time.Time `json:"created_at"`
	UpdatedAt                  time.Time `json:"updated_at"`
}

// DiskInstallation is an explicit installation segment. A missed observation
// cannot close it. ActiveEntityID is NULL on closed segments, making the active
// entity uniqueness constraint portable across PostgreSQL and SQLite.
type DiskInstallation struct {
	InstallationID      string     `json:"installation_id" gorm:"primaryKey;size:64"`
	ScopeID             string     `json:"scope_id" gorm:"index;size:128;not null"`
	EntityID            string     `json:"entity_id" gorm:"index;size:64;not null"`
	State               string     `json:"state" gorm:"index;size:32;not null"`
	ActiveEntityID      *string    `json:"-" gorm:"uniqueIndex;size:64"`
	OpenedAt            time.Time  `json:"opened_at" gorm:"index"`
	ClosedAt            *time.Time `json:"closed_at,omitempty" gorm:"index"`
	OpenEvidenceSHA256  string     `json:"open_evidence_sha256" gorm:"size:64;not null"`
	CloseEvidenceSHA256 string     `json:"close_evidence_sha256,omitempty" gorm:"size:64"`
	CloseReason         string     `json:"close_reason,omitempty" gorm:"size:128"`
	OpenRosterVersion   uint64     `json:"open_roster_version" gorm:"not null"`
	CloseRosterVersion  *uint64    `json:"close_roster_version,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// DiskInstallationReconcile preserves the exact SHA-bound, local operator
// manifest and the outcome of one atomic roster update. It is an audit record,
// not a disk failure label or an operational action.
type DiskInstallationReconcile struct {
	ID                    uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	ScopeID               string    `json:"scope_id" gorm:"index:idx_disk_reconcile_scope_version,unique;size:128;not null"`
	ManifestSHA256        string    `json:"manifest_sha256" gorm:"uniqueIndex;size:64;not null"`
	ExpectedRosterVersion uint64    `json:"expected_roster_version"`
	RosterVersion         uint64    `json:"roster_version" gorm:"index:idx_disk_reconcile_scope_version,unique;not null"`
	Actor                 string    `json:"actor" gorm:"size:128;not null"`
	ObservedAt            time.Time `json:"observed_at"`
	AvailableAt           time.Time `json:"available_at"`
	AppliedAt             time.Time `json:"applied_at"`
	OpenedCount           int       `json:"opened_count"`
	ClosedCount           int       `json:"closed_count"`
	ManifestJSON          string    `json:"-" gorm:"type:text;not null"`
	CreatedAt             time.Time `json:"created_at"`
}

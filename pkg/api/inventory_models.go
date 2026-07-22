package api

import "time"

// InventorySyncRun is the audit record for one Prometheus reconciliation.
type InventorySyncRun struct {
	ID             uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	TaskType       string     `json:"task_type" gorm:"index"` // target_status/identity_incremental/full_reconcile
	Status         string     `json:"status" gorm:"index"`    // running/success/failed
	Source         string     `json:"source"`
	StartedAt      time.Time  `json:"started_at" gorm:"index"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	NodeCount      int        `json:"node_count"`
	GPUCount       int        `json:"gpu_count"`
	KnownUUIDCount int        `json:"known_uuid_count"`
	TargetCount    int        `json:"target_count"`
	ChangeCount    int        `json:"change_count"`
	ErrorMessage   string     `json:"error_message,omitempty" gorm:"type:text"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// GPUNode is the durable node identity. Missing telemetry changes state but
// never deletes this record.
type GPUNode struct {
	ID               uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	NodeIP           string    `json:"node_ip" gorm:"uniqueIndex;not null"`
	Hostname         string    `json:"hostname" gorm:"index"`
	BMCIP            string    `json:"bmc_ip" gorm:"column:bmc_ip;index"`
	State            string    `json:"state" gorm:"index"`     // up/degraded/offline/unknown
	Lifecycle        string    `json:"lifecycle" gorm:"index"` // discovered/active/retiring/retired
	ExpectedGPUCount int       `json:"expected_gpu_count"`
	ObservedGPUCount int       `json:"observed_gpu_count"`
	FirstSeenAt      time.Time `json:"first_seen_at"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	TargetSyncedAt   time.Time `json:"target_synced_at" gorm:"index"`
	LastSyncedAt     time.Time `json:"last_synced_at" gorm:"index"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// GPUAsset represents one physical slot on a node. CurrentUUID may be empty
// when monitoring proves the slot should exist but cannot identify the card.
type GPUAsset struct {
	ID            uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	AssetKey      string    `json:"asset_key" gorm:"uniqueIndex;not null"`
	NodeID        uint      `json:"node_id" gorm:"index;not null"`
	NodeIP        string    `json:"node_ip" gorm:"index"`
	GPUIndex      int       `json:"gpu_index" gorm:"column:gpu_index;index"`
	CurrentUUID   string    `json:"gpu_uuid" gorm:"column:current_uuid;index"`
	Device        string    `json:"device"`
	Model         string    `json:"model" gorm:"index"`
	ModelName     string    `json:"model_name" gorm:"index"`
	PCIBusID      string    `json:"pci_bus_id" gorm:"column:pci_bus_id"`
	HostSerial    string    `json:"host_serial"`
	DriverVersion string    `json:"driver_version"`
	State         string    `json:"state" gorm:"index"` // active/uuid_unknown/history_only/conflict
	SampleState   string    `json:"sample_state"`       // current/missing
	FirstSeenAt   time.Time `json:"first_seen_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	LastSyncedAt  time.Time `json:"last_synced_at" gorm:"index"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type CollectorTarget struct {
	ID                uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	TargetKey         string     `json:"target_key" gorm:"uniqueIndex;not null"`
	Job               string     `json:"job" gorm:"index"`
	Instance          string     `json:"instance"`
	TargetIP          string     `json:"target_ip" gorm:"index"`
	NodeIP            string     `json:"node_ip" gorm:"index"`
	Health            string     `json:"health" gorm:"index"`
	ReasonCode        string     `json:"reason_code,omitempty" gorm:"index"`
	Suppressed        bool       `json:"suppressed" gorm:"index"`
	SuppressionReason string     `json:"suppression_reason,omitempty"`
	LastError         string     `json:"last_error,omitempty" gorm:"type:text"`
	ScrapeURL         string     `json:"scrape_url,omitempty"`
	LastScrapeAt      *time.Time `json:"last_scrape_at,omitempty"`
	LastSyncedAt      time.Time  `json:"last_synced_at" gorm:"index"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type AssetChangeEvent struct {
	ID        uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	SyncRunID uint      `json:"sync_run_id" gorm:"index"`
	EventType string    `json:"event_type" gorm:"index"`
	NodeIP    string    `json:"node_ip" gorm:"index"`
	AssetKey  string    `json:"asset_key" gorm:"index"`
	OldValue  string    `json:"old_value"`
	NewValue  string    `json:"new_value"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at" gorm:"index"`
}

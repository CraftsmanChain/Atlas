package api

import "time"

// GPUCoreEvent is one automatic critical GPU event onset in the core-event
// ledger (gpu-core-event-ledger-v1, plan v5 M1). Rows are append-only and
// idempotent on Key; ObservedAt is when the event was first seen at its source
// and ImportedAt is when Atlas learned of it (point-in-time audits use both).
type GPUCoreEvent struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	Key        string    `json:"key" gorm:"uniqueIndex;size:64;not null"`
	Source     string    `json:"source" gorm:"size:32;index;not null"` // vm_dcgm | kernel_log
	NodeIP     string    `json:"node_ip" gorm:"size:64;index;not null"`
	Hostname   string    `json:"hostname" gorm:"size:128"`
	GPUUUID    string    `json:"gpu_uuid" gorm:"size:96"`
	PCIBusID   string    `json:"pci_bus_id" gorm:"size:32"`
	Model      string    `json:"model" gorm:"size:96"`
	Code       string    `json:"code" gorm:"size:32;index;not null"` // xid79 | urr_growth | remap_fail ...
	Day        string    `json:"day" gorm:"size:10;index;not null"`  // Asia/Shanghai calendar day
	ObservedAt time.Time `json:"observed_at" gorm:"index;not null"`
	ImportedAt time.Time `json:"imported_at" gorm:"not null"`
	Value      float64   `json:"value"`
	RunID      uint      `json:"run_id" gorm:"index"`
}

func (GPUCoreEvent) TableName() string { return "gpu_core_events" }

// GPUCoreEventRun records one ledger import window per source.
type GPUCoreEventRun struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	Source      string    `json:"source" gorm:"size:32;index;not null"`
	WindowStart time.Time `json:"window_start" gorm:"not null"`
	WindowEnd   time.Time `json:"window_end" gorm:"not null"`
	StartedAt   time.Time `json:"started_at" gorm:"not null"`
	FinishedAt  time.Time `json:"finished_at"`
	Status      string    `json:"status" gorm:"size:16;not null"` // completed | failed
	ErrorCode   string    `json:"error_code" gorm:"size:64"`
	Error       string    `json:"error" gorm:"size:512"`
	Series      int       `json:"series"`
	NewEvents   int       `json:"new_events"`
	SHA         string    `json:"sha" gorm:"size:64"`
}

func (GPUCoreEventRun) TableName() string { return "gpu_core_event_runs" }

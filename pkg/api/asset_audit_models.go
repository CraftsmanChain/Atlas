package api

import "time"

// AssetAuditConfig is the singleton configuration for the delivery
// verification and live-IP discovery jobs that sit under GPU Assets. Secret
// material (Feishu application secret, notification webhook secret) is stored
// as AEAD ciphertext only and is never serialized to JSON.
type AssetAuditConfig struct {
	ID                uint    `json:"id" gorm:"primaryKey;autoIncrement"`
	Enabled           bool    `json:"enabled"`
	DeliveryEnabled   bool    `json:"delivery_check_enabled"`
	ScanEnabled       bool    `json:"scan_enabled"`
	DeliverySheetURL  string  `json:"delivery_sheet_url"`
	FeishuAppID       string  `json:"feishu_app_id"`
	FeishuAppSecretCT []byte  `json:"-" gorm:"column:feishu_app_secret_ciphertext"`
	NotifyEnabled     bool    `json:"notify_enabled"`
	NotifyAlways      bool    `json:"notify_always"`
	NotifyWebhookURL  string  `json:"notify_webhook_url"`
	NotifySecretCT    []byte  `json:"-" gorm:"column:notify_secret_ciphertext"`
	SSHPasswordsCT    []byte  `json:"-" gorm:"column:ssh_passwords_ciphertext"`
	KeyVersion        string  `json:"key_version"`
	ScheduleHour      int     `json:"schedule_hour"`
	Timezone          string  `json:"timezone"`
	ScanConcurrency   int     `json:"scan_concurrency"`
	ScanTimeoutMS     int     `json:"scan_timeout_ms"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// AssetAuditRun is one delivery check or live-IP scan execution. Summary and
// findings are stored as JSON so the result stays self-contained even after the
// referenced assets change.
type AssetAuditRun struct {
	ID           uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	Kind         string     `json:"kind" gorm:"index"`   // delivery_check / ip_scan
	Status       string     `json:"status" gorm:"index"` // running / success / failed
	Trigger      string     `json:"trigger"`             // scheduled / manual
	StartedAt    time.Time  `json:"started_at" gorm:"index"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	SummaryJSON  string     `json:"-" gorm:"column:summary_json;type:text"`
	FindingsJSON string     `json:"-" gorm:"column:findings_json;type:text"`
	Notified     bool       `json:"notified"`
	NotifyError  string     `json:"notify_error,omitempty" gorm:"type:text"`
	ErrorMessage string     `json:"error_message,omitempty" gorm:"type:text"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// AssetScanMark is one live-IP address the operator has deliberately marked.
// A marked address is reported as its own category and stays outside the
// ICMP-alive / port-22-closed / SSH-login-failed funnel, so a known but
// unmanaged host does not keep inflating the actionable counts. Marks persist
// across scans.
type AssetScanMark struct {
	ID        uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	IPAddress string    `json:"ip_address" gorm:"uniqueIndex;not null"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

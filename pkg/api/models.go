package api

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// StringMap 帮助 GORM 处理 JSON 字典
type StringMap map[string]string

// StringList 帮助 GORM 处理 JSON 数组
type StringList []string

// Value 将 StringMap 转换为 JSON 字符串存入数据库
func (m StringMap) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

// Scan 将数据库中的 JSON 字符串解析为 StringMap
func (m *StringMap) Scan(value interface{}) error {
	if value == nil {
		*m = make(StringMap)
		return nil
	}
	bytes, err := databaseJSONBytes(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes, m)
}

// Value 将 StringList 转换为 JSON 字符串存入数据库
func (l StringList) Value() (driver.Value, error) {
	if l == nil {
		return nil, nil
	}
	return json.Marshal(l)
}

// Scan 将数据库中的 JSON 字符串解析为 StringList
func (l *StringList) Scan(value interface{}) error {
	if value == nil {
		*l = StringList{}
		return nil
	}
	bytes, err := databaseJSONBytes(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes, l)
}

// databaseJSONBytes normalizes the values returned by SQLite and PostgreSQL
// text columns. SQLite commonly returns []byte while pgx returns string.
func databaseJSONBytes(value any) ([]byte, error) {
	switch typed := value.(type) {
	case []byte:
		return typed, nil
	case string:
		return []byte(typed), nil
	default:
		return nil, fmt.Errorf("unsupported JSON database value %T", value)
	}
}

// AlertEvent 表示系统接收到的原始或经过增强的告警事件
type AlertEvent struct {
	ID            string    `json:"id" gorm:"primaryKey"`
	Source        string    `json:"source" gorm:"index"`
	Level         string    `json:"level" gorm:"index"`
	Message       string    `json:"message"`
	Labels        StringMap `json:"labels" gorm:"type:text"`
	Host          string    `json:"host" gorm:"index"`
	Timestamp     time.Time `json:"timestamp" gorm:"index"`
	IsProcessed   bool      `json:"is_processed"`
	RepeatCount   int       `json:"repeat_count" gorm:"default:1"` // 记录重复次数
	LastSeenAt    time.Time `json:"last_seen_at"`                  // 最后一次出现的时间
	CallbackURL   string    `json:"callback_url" gorm:"-"`
	CallbackToken string    `json:"callback_token" gorm:"-"`
}

// LogEntry 表示 Agent 收集到的系统故障或异常日志条目
type LogEntry struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	Host      string    `json:"host" gorm:"index"`
	Service   string    `json:"service" gorm:"index"`
	Level     string    `json:"level"`
	Content   string    `json:"content"`
	TraceID   string    `json:"trace_id" gorm:"index"`
	Timestamp time.Time `json:"timestamp" gorm:"index"`
}

// SystemMetrics 表示 Agent 收集到的主机基础指标
type SystemMetrics struct {
	ID          uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	Host        string    `json:"host" gorm:"index"`
	CPUUsage    float64   `json:"cpu_usage"`
	MemoryUsage float64   `json:"memory_usage"`
	DiskUsage   float64   `json:"disk_usage"`
	Timestamp   time.Time `json:"timestamp" gorm:"index"`
}

// HealthScore 表示某个主机的整体健康度评估结果
type HealthScore struct {
	Host      string    `json:"host" gorm:"primaryKey"` // 这里简化为用主机名做主键或唯一索引
	Score     float64   `json:"score"`
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
}

// PlatformDisplayConfig is the singleton public-facing identity of an Atlas
// deployment. It intentionally contains display-only fields and no secrets or
// infrastructure connection details.
type PlatformDisplayConfig struct {
	ID             uint      `json:"-" gorm:"primaryKey"`
	InstanceName   string    `json:"instance_name"`
	ProductName    string    `json:"product_name"`
	ProductTagline string    `json:"product_tagline"`
	Environment    string    `json:"environment"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// LXOPAssetConfig stores the singleton connection configuration for the
// read-only LXOP asset OpenAPI. APIKeyCiphertext is never serialized.
type LXOPAssetConfig struct {
	ID                 uint       `json:"-" gorm:"primaryKey"`
	OpsHostURL         string     `json:"ops_host_url"`
	AssetMachineURL    string     `json:"asset_machine_url"`
	DataCenterID       string     `json:"data_center_id" gorm:"index"`
	APIKeyCiphertext   []byte     `json:"-" gorm:"not null"`
	KeyVersion         string     `json:"-" gorm:"size:24;not null"`
	InsecureSkipVerify bool       `json:"insecure_skip_verify"`
	Enabled            bool       `json:"enabled" gorm:"index"`
	LastSyncStatus     string     `json:"last_sync_status" gorm:"index"`
	LastSyncError      string     `json:"last_sync_error,omitempty" gorm:"type:text"`
	LastSyncAt         *time.Time `json:"last_sync_at,omitempty" gorm:"index"`
	LastOpsHostCount   int        `json:"last_ops_host_count"`
	LastMachineCount   int        `json:"last_machine_count"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// NodeCredentialProfile stores only encrypted node-access credential material.
// Ciphertext contains an AEAD-protected username/password payload and must
// never be serialized by an API.
type NodeCredentialProfile struct {
	ID             uint      `json:"-" gorm:"primaryKey"`
	ProfileID      string    `json:"profile_id" gorm:"uniqueIndex;size:64;not null"`
	Priority       int       `json:"priority" gorm:"index;not null"`
	AuthType       string    `json:"auth_type" gorm:"size:24;not null"`
	UsernameMasked string    `json:"username_masked" gorm:"size:24;not null"`
	Ciphertext     []byte    `json:"-" gorm:"not null"`
	KeyVersion     string    `json:"key_version" gorm:"size:24;not null"`
	Enabled        bool      `json:"enabled" gorm:"index;not null"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// NodeAccessCheck records only redacted SSH handshake/authentication outcomes.
// It never contains credentials, command output, or arbitrary remote errors.
type NodeAccessCheck struct {
	ID                    uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	NodeIP                string     `json:"node_ip" gorm:"index;not null"`
	Status                string     `json:"status" gorm:"index;not null"`
	CredentialProfileID   string     `json:"credential_profile_id,omitempty" gorm:"index"`
	Attempts              StringList `json:"attempts" gorm:"type:text"`
	AlertRequired         bool       `json:"alert_required" gorm:"index"`
	NoCredentialDisclosed bool       `json:"no_credential_disclosed"`
	NoCommandExecuted     bool       `json:"no_command_executed"`
	StartedAt             time.Time  `json:"started_at" gorm:"index"`
	FinishedAt            time.Time  `json:"finished_at" gorm:"index"`
	CreatedAt             time.Time  `json:"created_at"`
}

// NodeEvidenceCollection is the redacted audit envelope for one bounded,
// read-only evidence collection associated with a fault event.
type NodeEvidenceCollection struct {
	ID                    uint                 `json:"id" gorm:"primaryKey;autoIncrement"`
	FaultEventID          uint                 `json:"fault_event_id,omitempty" gorm:"index"`
	PlatformIssueID       uint                 `json:"platform_issue_id,omitempty" gorm:"index"`
	NodeIP                string               `json:"node_ip" gorm:"index;not null"`
	Trigger               string               `json:"trigger" gorm:"index;size:32"`
	Status                string               `json:"status" gorm:"index;not null"`
	CredentialProfileID   string               `json:"credential_profile_id,omitempty" gorm:"index"`
	CommandCount          int                  `json:"command_count"`
	OutputBytes           int                  `json:"output_bytes"`
	OutputTruncated       bool                 `json:"output_truncated"`
	FailureCode           string               `json:"failure_code,omitempty" gorm:"index"`
	NoCredentialDisclosed bool                 `json:"no_credential_disclosed"`
	ReadOnly              bool                 `json:"read_only"`
	StartedAt             time.Time            `json:"started_at" gorm:"index"`
	FinishedAt            time.Time            `json:"finished_at" gorm:"index"`
	CreatedAt             time.Time            `json:"created_at"`
	Records               []NodeEvidenceRecord `json:"records,omitempty" gorm:"foreignKey:CollectionID"`
}

// NodeEvidenceRecord contains output only from a registered, fixed command.
// Output is size-bounded and sanitized before persistence.
type NodeEvidenceRecord struct {
	ID           uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	CollectionID uint      `json:"collection_id" gorm:"index;not null"`
	CommandID    string    `json:"command_id" gorm:"index;size:64;not null"`
	Kind         string    `json:"kind" gorm:"index;size:32;not null"`
	Status       string    `json:"status" gorm:"index;size:32;not null"`
	Output       string    `json:"output" gorm:"type:text"`
	OutputBytes  int       `json:"output_bytes"`
	Truncated    bool      `json:"truncated"`
	ObservedAt   time.Time `json:"observed_at" gorm:"index"`
	CreatedAt    time.Time `json:"created_at"`
}

// AlertIngestionRecord 记录 webhook 告警异步处理与回调确认全链路状态。
type AlertIngestionRecord struct {
	ID                 uint      `json:"id" gorm:"primaryKey;autoIncrement;index:idx_ingestions_created_id,priority:2"`
	EventID            string    `json:"event_id" gorm:"index"`
	Source             string    `json:"source" gorm:"index"`
	Host               string    `json:"host" gorm:"index"`
	Level              string    `json:"level" gorm:"index"`
	Message            string    `json:"message"`
	RawPayload         string    `json:"raw_payload" gorm:"type:text"`
	ProcessStatus      string    `json:"process_status" gorm:"index"` // processing/success/failed
	ProcessAttempts    int       `json:"process_attempts"`
	ProcessLastError   string    `json:"process_last_error" gorm:"type:text"`
	CallbackURL        string    `json:"callback_url"`
	CallbackStatus     string    `json:"callback_status" gorm:"index"` // disabled/pending/success/failed
	CallbackAttempts   int       `json:"callback_attempts"`
	CallbackLastError  string    `json:"callback_last_error" gorm:"type:text"`
	CallbackHTTPStatus int       `json:"callback_http_status"`
	CallbackLastAt     time.Time `json:"callback_last_at"`
	CreatedAt          time.Time `json:"created_at" gorm:"index:idx_ingestions_created_id,priority:1"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// AIAnalysisReport 记录 AI 分析任务和结果，为后续告警 / 日志 / 健康分析链路预留统一模型。
type AIAnalysisReport struct {
	ID                 uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	IngestionRecordID  uint       `json:"ingestion_record_id" gorm:"index"`
	EventID            string     `json:"event_id" gorm:"index"`
	AnalysisType       string     `json:"analysis_type" gorm:"index"` // alert_rca/log_analysis/health_explanation
	Status             string     `json:"status" gorm:"index"`        // pending/completed/blocked/failed
	Model              string     `json:"model"`
	PromptVersion      string     `json:"prompt_version"`
	Severity           string     `json:"severity" gorm:"index"`
	Summary            string     `json:"summary" gorm:"type:text"`
	ProbableCauses     StringList `json:"probable_causes" gorm:"type:text"`
	RecommendedActions StringList `json:"recommended_actions" gorm:"type:text"`
	Evidence           StringList `json:"evidence" gorm:"type:text"`
	Confidence         float64    `json:"confidence"`
	ErrorMessage       string     `json:"error_message" gorm:"type:text"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

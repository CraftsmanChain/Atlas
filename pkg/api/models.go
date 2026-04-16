package api

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
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
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
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
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}
	return json.Unmarshal(bytes, l)
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

// AlertIngestionRecord 记录 webhook 告警异步处理与回调确认全链路状态。
type AlertIngestionRecord struct {
	ID                 uint      `json:"id" gorm:"primaryKey;autoIncrement"`
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
	CreatedAt          time.Time `json:"created_at"`
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

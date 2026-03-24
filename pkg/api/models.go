package api

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

// StringMap 帮助 GORM 处理 JSON 字典
type StringMap map[string]string

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

// AlertEvent 表示系统接收到的原始或经过增强的告警事件
type AlertEvent struct {
	ID          string    `json:"id" gorm:"primaryKey"`
	Source      string    `json:"source" gorm:"index"`
	Level       string    `json:"level" gorm:"index"`
	Message     string    `json:"message"`
	Labels      StringMap `json:"labels" gorm:"type:text"`
	Host        string    `json:"host" gorm:"index"`
	Timestamp   time.Time `json:"timestamp" gorm:"index"`
	IsProcessed bool      `json:"is_processed"`
	RepeatCount int       `json:"repeat_count" gorm:"default:1"` // 记录重复次数
	LastSeenAt  time.Time `json:"last_seen_at"`                  // 最后一次出现的时间
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

package storage

import (
	"log"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"atlas/pkg/api"
)

// DB 是全局的数据库实例封装
type DB struct {
	*gorm.DB
}

// InitDB 初始化 SQLite 数据库并自动迁移表结构
func InitDB(dsn string) (*DB, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	log.Println("Database connection established.")

	// 自动迁移模式，确保数据库表结构和代码模型一致
	err = db.AutoMigrate(
		&api.AlertEvent{},
		&api.AlertIngestionRecord{},
		&api.LogEntry{},
		&api.SystemMetrics{},
		&api.HealthScore{},
	)
	if err != nil {
		return nil, err
	}

	log.Println("Database migration completed.")

	return &DB{db}, nil
}

// SaveAlertEvent 存储告警事件
func (db *DB) SaveAlertEvent(event *api.AlertEvent) error {
	return db.Create(event).Error
}

// SaveSystemMetrics 存储系统指标
func (db *DB) SaveSystemMetrics(metrics *api.SystemMetrics) error {
	return db.Create(metrics).Error
}

// ListFailedIngestions 查询最近失败的告警处理记录（处理失败或回调失败）。
func (db *DB) ListFailedIngestions(limit int) ([]api.AlertIngestionRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var records []api.AlertIngestionRecord
	err := db.
		Where("process_status = ? OR callback_status = ?", "failed", "failed").
		Order("updated_at DESC").
		Limit(limit).
		Find(&records).Error
	return records, err
}

package storage

import (
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"atlas/pkg/api"
)

type IngestionListOptions struct {
	Limit    int
	BeforeID uint
	Level    string
	Query    string
}

// OpenReadOnlyDB 打开独立只读数据源，不执行 AutoMigrate。用于测试环境
// 查询生产接收记录，避免测试写入、回调或分析状态影响生产数据库。
func OpenReadOnlyDB(dsn string) (*DB, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping read-only database: %w", err)
	}
	log.Println("Read-only ingestion database connection established.")
	return &DB{db}, nil
}

type IngestionPage struct {
	Records      []api.AlertIngestionRecord
	Limit        int
	Total        int64
	AllTotal     int64
	HasMore      bool
	NextBeforeID uint
	LatestAt     *time.Time
	Count5m      int64
	Count1h      int64
}

const MaxIngestionPageSize = 200

// DB 是全局的数据库实例封装
type DB struct {
	*gorm.DB
}

// InitDB initializes a SQLite database. It remains available for unit tests,
// local development, and the one-time SQLite-to-PostgreSQL migration tool.
func InitDB(dsn string) (*DB, error) {
	return InitDBWithDriver("sqlite", dsn)
}

// InitDBWithDriver initializes the configured database and ensures the Atlas
// schema exists. Production uses PostgreSQL; SQLite is retained only for tests
// and migration input.
func InitDBWithDriver(driver, dsn string) (*DB, error) {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver == "" {
		driver = "postgres"
	}
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("database DSN is required for driver %q", driver)
	}

	var dialector gorm.Dialector
	switch driver {
	case "postgres", "postgresql":
		dialector = postgres.Open(dsn)
	case "sqlite", "sqlite3":
		dialector = sqlite.Open(dsn)
	default:
		return nil, fmt.Errorf("unsupported database driver %q", driver)
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping %s database: %w", driver, err)
	}
	if driver == "postgres" || driver == "postgresql" {
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
	}

	log.Printf("Database connection established. driver=%s", driver)

	if err := migrateSchema(db); err != nil {
		return nil, err
	}

	log.Println("Database migration completed.")

	return &DB{db}, nil
}

func migrateSchema(db *gorm.DB) error {
	// GORM splits the GPU initialism as "gp_u" unless the table name is
	// explicit. Preserve existing identity backfill data during the one-time
	// correction to the stable table name.
	if db.Migrator().HasTable("historical_gp_uidentity_intervals") &&
		!db.Migrator().HasTable("historical_gpu_identity_intervals") {
		if err := db.Migrator().RenameTable(
			"historical_gp_uidentity_intervals",
			"historical_gpu_identity_intervals",
		); err != nil {
			return err
		}
	}
	return db.AutoMigrate(
		&api.AlertEvent{},
		&api.AlertIngestionRecord{},
		&api.AIAnalysisReport{},
		&api.LogEntry{},
		&api.SystemMetrics{},
		&api.HealthScore{},
		&api.InventorySyncRun{},
		&api.GPUNode{},
		&api.GPUAsset{},
		&api.CollectorTarget{},
		&api.AssetChangeEvent{},
		&api.HealthEvaluationRun{},
		&api.GPUFeatureSnapshot{},
		&api.GPUFeatureBaseline{},
		&api.FeatureBaselineRefreshRun{},
		&api.GPUHealthScore{},
		&api.GPUHealthRuleHit{},
		&api.GPUFaultEvent{},
		&api.FeatureDefinition{},
		&api.PlatformIssue{},
		&api.IssueResolution{},
		&api.PlatformDisplayConfig{},
		&api.LXOPAssetConfig{},
		&api.InfrastructureAsset{},
		&api.NodeCredentialProfile{},
		&api.NodeAccessCheck{},
		&api.NodeEvidenceCollection{},
		&api.NodeEvidenceRecord{},
		&api.PredictionModelSpec{},
		&api.PredictionFeatureParityAudit{},
		&api.FailureLabel{},
		&api.HardwareRiskPrediction{},
		&api.PredictionOutcomeEvaluation{},
		&api.MonitoringHistoryAudit{},
		&api.HistoryBackfillRun{},
		&api.HistoricalFaultCandidate{},
		&api.HistoricalGPUIdentityInterval{},
		&api.TrainingDatasetBuild{},
		&api.TrainingFeatureBuild{},
		&api.TrainingPreparationBuild{},
		&api.TrainingControlFeatureBuild{},
		&api.TrainingMatrixBuild{},
		&api.BaselineModelBuild{},
	)
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

// ListIngestions 查询接收记录分页与链路统计。before_id 使用稳定的主键游标，
// total 表示当前筛选命中总数，all_total 表示全表记录数。
func (db *DB) ListIngestions(options IngestionListOptions, now time.Time) (*IngestionPage, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > MaxIngestionPageSize {
		limit = MaxIngestionPageSize
	}

	filtered := db.Model(&api.AlertIngestionRecord{})
	if level := strings.TrimSpace(options.Level); level != "" {
		filtered = filtered.Where("level = ?", level)
	}
	if query := strings.TrimSpace(options.Query); query != "" {
		pattern := "%" + strings.ToLower(query) + "%"
		filtered = filtered.Where(
			"LOWER(message) LIKE ? OR LOWER(host) LIKE ? OR LOWER(source) LIKE ? OR LOWER(raw_payload) LIKE ? OR LOWER(event_id) LIKE ?",
			pattern, pattern, pattern, pattern, pattern,
		)
	}

	page := &IngestionPage{Limit: limit}
	if err := filtered.Count(&page.Total).Error; err != nil {
		return nil, err
	}
	if err := db.Model(&api.AlertIngestionRecord{}).Count(&page.AllTotal).Error; err != nil {
		return nil, err
	}

	query := filtered
	if options.BeforeID > 0 {
		query = query.Where("id < ?", options.BeforeID)
	}
	var records []api.AlertIngestionRecord
	if err := query.Order("id DESC").Limit(limit + 1).Find(&records).Error; err != nil {
		return nil, err
	}
	if len(records) > limit {
		page.HasMore = true
		records = records[:limit]
	}
	page.Records = records
	if len(records) > 0 {
		page.NextBeforeID = records[len(records)-1].ID
	}

	var latest api.AlertIngestionRecord
	result := db.Select("created_at").Order("created_at DESC").Order("id DESC").Limit(1).Find(&latest)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected > 0 {
		latestAt := latest.CreatedAt
		page.LatestAt = &latestAt
	}
	if err := db.Model(&api.AlertIngestionRecord{}).Where("created_at >= ?", now.Add(-5*time.Minute)).Count(&page.Count5m).Error; err != nil {
		return nil, err
	}
	if err := db.Model(&api.AlertIngestionRecord{}).Where("created_at >= ?", now.Add(-time.Hour)).Count(&page.Count1h).Error; err != nil {
		return nil, err
	}
	return page, nil
}

// GetAlertEventByID 查询指定事件。
func (db *DB) GetAlertEventByID(eventID string) (*api.AlertEvent, error) {
	if eventID == "" {
		return nil, nil
	}
	var event api.AlertEvent
	if err := db.Where("id = ?", eventID).First(&event).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &event, nil
}

// GetLatestAIAnalysisReportForIngestion 查询某条接收记录最新的 AI 报告。
func (db *DB) GetLatestAIAnalysisReportForIngestion(ingestionRecordID uint) (*api.AIAnalysisReport, error) {
	if ingestionRecordID == 0 {
		return nil, nil
	}
	var report api.AIAnalysisReport
	if err := db.
		Where("ingestion_record_id = ?", ingestionRecordID).
		Order("updated_at DESC").
		First(&report).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &report, nil
}

// GetAIAnalysisReportByID 查询指定 AI 分析报告。
func (db *DB) GetAIAnalysisReportByID(reportID uint) (*api.AIAnalysisReport, error) {
	if reportID == 0 {
		return nil, nil
	}
	var report api.AIAnalysisReport
	if err := db.Where("id = ?", reportID).First(&report).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &report, nil
}

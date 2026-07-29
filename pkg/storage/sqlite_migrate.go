package storage

import (
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"atlas/pkg/api"
)

type MigrationResult struct {
	Table       string
	SourceCount int64
	TargetCount int64
}

type migrationModel struct {
	name          string
	value         any
	order         string
	autoIncrement bool
}

var migrationModels = []migrationModel{
	{name: "alert_events", value: &api.AlertEvent{}, order: "id"},
	{name: "alert_ingestion_records", value: &api.AlertIngestionRecord{}, order: "id", autoIncrement: true},
	{name: "ai_analysis_reports", value: &api.AIAnalysisReport{}, order: "id", autoIncrement: true},
	{name: "log_entries", value: &api.LogEntry{}, order: "id"},
	{name: "system_metrics", value: &api.SystemMetrics{}, order: "id", autoIncrement: true},
	{name: "health_scores", value: &api.HealthScore{}, order: "host"},
	{name: "inventory_sync_runs", value: &api.InventorySyncRun{}, order: "id", autoIncrement: true},
	{name: "gpu_nodes", value: &api.GPUNode{}, order: "id", autoIncrement: true},
	{name: "gpu_assets", value: &api.GPUAsset{}, order: "id", autoIncrement: true},
	{name: "collector_targets", value: &api.CollectorTarget{}, order: "id", autoIncrement: true},
	{name: "asset_change_events", value: &api.AssetChangeEvent{}, order: "id", autoIncrement: true},
	{name: "health_evaluation_runs", value: &api.HealthEvaluationRun{}, order: "id", autoIncrement: true},
	{name: "gpu_feature_snapshots", value: &api.GPUFeatureSnapshot{}, order: "id", autoIncrement: true},
	{name: "gpu_feature_baselines", value: &api.GPUFeatureBaseline{}, order: "id", autoIncrement: true},
	{name: "feature_baseline_refresh_runs", value: &api.FeatureBaselineRefreshRun{}, order: "id", autoIncrement: true},
	{name: "gpu_health_scores", value: &api.GPUHealthScore{}, order: "id", autoIncrement: true},
	{name: "gpu_health_rule_hits", value: &api.GPUHealthRuleHit{}, order: "id", autoIncrement: true},
	{name: "gpu_fault_events", value: &api.GPUFaultEvent{}, order: "id", autoIncrement: true},
	{name: "feature_definitions", value: &api.FeatureDefinition{}, order: "id", autoIncrement: true},
	{name: "platform_issues", value: &api.PlatformIssue{}, order: "id", autoIncrement: true},
	{name: "issue_resolutions", value: &api.IssueResolution{}, order: "id", autoIncrement: true},
	{name: "platform_display_configs", value: &api.PlatformDisplayConfig{}, order: "id", autoIncrement: true},
	{name: "node_credential_profiles", value: &api.NodeCredentialProfile{}, order: "id", autoIncrement: true},
	{name: "node_access_checks", value: &api.NodeAccessCheck{}, order: "id", autoIncrement: true},
	{name: "node_evidence_collections", value: &api.NodeEvidenceCollection{}, order: "id", autoIncrement: true},
	{name: "node_evidence_records", value: &api.NodeEvidenceRecord{}, order: "id", autoIncrement: true},
}

// MigrateSQLite copies a consistent SQLite backup into PostgreSQL. Existing
// rows are updated by primary key so the production alert database can be
// overlaid after the richer former test database has been imported.
func MigrateSQLite(sourceDSN, targetDSN string, selectedTables []string, batchSize int, resume bool) ([]MigrationResult, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	source, err := gorm.Open(sqlite.Open(sourceDSN), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open SQLite source: %w", err)
	}
	sourceSQL, err := source.DB()
	if err != nil {
		return nil, err
	}
	defer sourceSQL.Close()
	if err := sourceSQL.Ping(); err != nil {
		return nil, fmt.Errorf("ping SQLite source: %w", err)
	}
	source.Logger = logger.Default.LogMode(logger.Silent)

	target, err := InitDBWithDriver("postgres", targetDSN)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL target: %w", err)
	}
	target.DB.Logger = logger.Default.LogMode(logger.Silent)
	targetSQL, err := target.DB.DB()
	if err != nil {
		return nil, err
	}
	defer targetSQL.Close()

	selected, err := normalizeSelectedTables(selectedTables)
	if err != nil {
		return nil, err
	}
	results := make([]MigrationResult, 0, len(migrationModels))
	for _, model := range migrationModels {
		if len(selected) > 0 {
			if _, ok := selected[model.name]; !ok {
				continue
			}
		}
		if !source.Migrator().HasTable(model.name) {
			log.Printf("Skipping absent SQLite table %s", model.name)
			continue
		}
		result, err := migrateSQLiteTable(source, target.DB, model, batchSize, resume)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

func normalizeSelectedTables(names []string) (map[string]struct{}, error) {
	if len(names) == 0 {
		return nil, nil
	}
	known := make(map[string]struct{}, len(migrationModels))
	for _, model := range migrationModels {
		known[model.name] = struct{}{}
	}
	selected := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, ok := known[name]; !ok {
			available := make([]string, 0, len(known))
			for table := range known {
				available = append(available, table)
			}
			sort.Strings(available)
			return nil, fmt.Errorf("unknown table %q (available: %s)", name, strings.Join(available, ","))
		}
		selected[name] = struct{}{}
	}
	return selected, nil
}

func migrateSQLiteTable(source, target *gorm.DB, model migrationModel, batchSize int, resume bool) (MigrationResult, error) {
	result := MigrationResult{Table: model.name}
	if err := source.Table(model.name).Count(&result.SourceCount).Error; err != nil {
		return result, fmt.Errorf("count SQLite table %s: %w", model.name, err)
	}
	if err := target.Table(model.name).Count(&result.TargetCount).Error; err != nil {
		return result, fmt.Errorf("count PostgreSQL table %s: %w", model.name, err)
	}
	if resume && result.TargetCount >= result.SourceCount {
		log.Printf("Skipping completed table=%s source=%d target=%d", model.name, result.SourceCount, result.TargetCount)
		return result, nil
	}
	if result.SourceCount > 0 {
		modelType := reflect.TypeOf(model.value).Elem()
		batch := reflect.New(reflect.SliceOf(modelType))
		query := source.Table(model.name)
		if resume && model.autoIncrement && result.TargetCount > 0 {
			var maximum int64
			if err := target.Table(model.name).Select("COALESCE(MAX(id), 0)").Scan(&maximum).Error; err != nil {
				return result, fmt.Errorf("read resume id for %s: %w", model.name, err)
			}
			query = query.Where("id > ?", maximum)
		}
		err := query.Order(model.order).FindInBatches(batch.Interface(), batchSize, func(_ *gorm.DB, _ int) error {
			if reflect.ValueOf(batch.Interface()).Elem().Len() == 0 {
				return nil
			}
			return target.Table(model.name).
				Clauses(clause.OnConflict{UpdateAll: true}).
				CreateInBatches(batch.Interface(), batchSize).Error
		}).Error
		if err != nil {
			return result, fmt.Errorf("copy table %s: %w", model.name, err)
		}
	}
	if model.autoIncrement {
		if err := resetPostgresSequence(target, model.name); err != nil {
			return result, err
		}
	}
	if err := target.Table(model.name).Count(&result.TargetCount).Error; err != nil {
		return result, fmt.Errorf("count PostgreSQL table %s: %w", model.name, err)
	}
	if result.TargetCount < result.SourceCount {
		return result, fmt.Errorf("verification failed for %s: source=%d target=%d", model.name, result.SourceCount, result.TargetCount)
	}
	log.Printf("Migrated table=%s source=%d target=%d", model.name, result.SourceCount, result.TargetCount)
	return result, nil
}

func resetPostgresSequence(target *gorm.DB, table string) error {
	var sequence *string
	if err := target.Raw("SELECT pg_get_serial_sequence(?, 'id')", table).Scan(&sequence).Error; err != nil {
		return fmt.Errorf("resolve sequence for %s: %w", table, err)
	}
	if sequence == nil || strings.TrimSpace(*sequence) == "" {
		return nil
	}
	var maximum int64
	if err := target.Table(table).Select("COALESCE(MAX(id), 0)").Scan(&maximum).Error; err != nil {
		return fmt.Errorf("read maximum id for %s: %w", table, err)
	}
	called := maximum > 0
	if maximum == 0 {
		maximum = 1
	}
	if err := target.Exec("SELECT setval(?, ?, ?)", *sequence, maximum, called).Error; err != nil {
		return fmt.Errorf("reset sequence for %s: %w", table, err)
	}
	return nil
}

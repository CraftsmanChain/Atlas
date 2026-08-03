package storage

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestListIngestionsPaginatesFiltersAndReturnsStats(t *testing.T) {
	db, err := InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 14, 0, 0, 0, time.UTC)
	for index := 0; index < 5; index++ {
		record := api.AlertIngestionRecord{
			EventID:       fmt.Sprintf("event-%d", index+1),
			Source:        "alertmanager",
			Host:          fmt.Sprintf("gpu-node-%d", index+1),
			Level:         []string{"warning", "critical"}[index%2],
			Message:       fmt.Sprintf("GPU alert %d", index+1),
			ProcessStatus: "success",
			CreatedAt:     now.Add(-time.Duration(index) * 10 * time.Minute),
		}
		if err := db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}

	first, err := db.ListIngestions(IngestionListOptions{Limit: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 5 || first.AllTotal != 5 || first.Limit != 2 || len(first.Records) != 2 || !first.HasMore {
		t.Fatalf("unexpected first page: %+v", first)
	}
	if first.Records[0].ID != 5 || first.Records[1].ID != 4 || first.NextBeforeID != 4 {
		t.Fatalf("unexpected first page order/cursor: %+v", first.Records)
	}
	if first.LatestAt == nil || !first.LatestAt.Equal(now) || first.Count5m != 1 || first.Count1h != 5 {
		t.Fatalf("unexpected stream stats: latest=%v count5m=%d count1h=%d", first.LatestAt, first.Count5m, first.Count1h)
	}

	second, err := db.ListIngestions(IngestionListOptions{Limit: 2, BeforeID: first.NextBeforeID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Records) != 2 || second.Records[0].ID != 3 || second.Records[1].ID != 2 || !second.HasMore {
		t.Fatalf("unexpected second page: %+v", second)
	}

	filtered, err := db.ListIngestions(IngestionListOptions{Limit: 1000, Level: "critical", Query: "GPU-NODE"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Limit != 200 || filtered.Total != 2 || filtered.AllTotal != 5 || len(filtered.Records) != 2 {
		t.Fatalf("unexpected filtered page: %+v", filtered)
	}
}

func TestOpenReadOnlyDBReadsWithoutAllowingWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	source, err := InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	record := api.AlertIngestionRecord{EventID: "event-live", Source: "alertmanager", Message: "latest", ProcessStatus: "success", CreatedAt: time.Now()}
	if err := source.Create(&record).Error; err != nil {
		t.Fatal(err)
	}

	readOnly, err := OpenReadOnlyDB("file:" + path + "?mode=ro&_query_only=1")
	if err != nil {
		t.Fatal(err)
	}
	page, err := readOnly.ListIngestions(IngestionListOptions{Limit: 100}, time.Now())
	if err != nil || page.AllTotal != 1 || len(page.Records) != 1 || page.Records[0].EventID != "event-live" {
		t.Fatalf("unexpected read-only page: page=%+v err=%v", page, err)
	}
	if err := readOnly.Create(&api.AlertIngestionRecord{EventID: "must-fail", Message: "write"}).Error; err == nil {
		t.Fatal("expected write against read-only ingestion database to fail")
	}
}

func TestInitDBWithDriverRejectsUnknownDriverAndEmptyDSN(t *testing.T) {
	if _, err := InitDBWithDriver("mysql", "irrelevant"); err == nil {
		t.Fatal("expected unsupported driver to fail")
	}
	if _, err := InitDBWithDriver("postgres", ""); err == nil {
		t.Fatal("expected empty PostgreSQL DSN to fail")
	}
}

func TestMigrateSchemaRenamesLegacyGPUIdentityTable(t *testing.T) {
	raw, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Table("historical_gp_uidentity_intervals").
		AutoMigrate(&api.HistoricalGPUIdentityInterval{}); err != nil {
		t.Fatal(err)
	}
	legacy := api.HistoricalGPUIdentityInterval{
		IntervalKey: "legacy-row", SourceKey: "primary", BackfillRunID: 1,
		NodeIP: "10.0.0.1", GPUUUID: "GPU-old", FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	if err := raw.Table("historical_gp_uidentity_intervals").Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateSchema(raw); err != nil {
		t.Fatal(err)
	}
	if raw.Migrator().HasTable("historical_gp_uidentity_intervals") ||
		!raw.Migrator().HasTable("historical_gpu_identity_intervals") {
		t.Fatal("legacy identity table was not renamed")
	}
	var count int64
	if err := raw.Table("historical_gpu_identity_intervals").Where("interval_key = ?", "legacy-row").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("legacy identity row count=%d", count)
	}
}

func TestBaselineMetricColumnsUseStableAUCNames(t *testing.T) {
	raw, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "baseline.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSchema(raw); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"test_macro_roc_auc", "test_macro_pr_auc"} {
		if !raw.Migrator().HasColumn(&api.BaselineModelBuild{}, column) {
			t.Fatalf("missing stable baseline metric column %s", column)
		}
	}
}

func TestNormalizeSelectedTables(t *testing.T) {
	selected, err := normalizeSelectedTables([]string{" gpu_nodes ", "gpu_assets"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := selected["gpu_nodes"]; !ok {
		t.Fatal("gpu_nodes was not selected")
	}
	if _, err := normalizeSelectedTables([]string{"not_an_atlas_table"}); err == nil {
		t.Fatal("expected unknown migration table to fail")
	}
}

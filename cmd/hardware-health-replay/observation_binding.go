package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"atlas/internal/diskhealth"
	"atlas/pkg/storage"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This path opens only existing schema. It never runs Atlas AutoMigrate, and
// failed driver payloads are not logged because they may contain credentials.
func openExistingReplayDB(ctx context.Context, driver, dsn string) (*storage.DB, error) {
	var dialector gorm.Dialector
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "sqlite", "sqlite3":
		if strings.HasPrefix(dsn, "file:") {
			uri, err := url.Parse(dsn)
			if err != nil {
				return nil, fmt.Errorf("invalid existing database reference")
			}
			query := uri.Query()
			// URI DSNs must not create a missing file or an in-memory schema.
			query.Set("mode", "rw")
			uri.RawQuery = query.Encode()
			dsn = uri.String()
		} else {
			if info, err := os.Stat(dsn); err != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("existing database required")
			}
		}
		dialector = sqlite.Open(dsn)
	case "postgres", "postgresql":
		dialector = postgres.Open(dsn)
	default:
		return nil, fmt.Errorf("unsupported observation database driver")
	}
	db, err := gorm.Open(dialector, &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if db.Dialector.Name() == "sqlite" {
		if err := db.WithContext(ctx).Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
			_ = sqlDB.Close()
			return nil, err
		}
	}
	return &storage.DB{DB: db}, nil
}

// Exclusive creation prevents a retry from overwriting an earlier commit
// receipt. Failure after commit means unknown persistence and must pause.
func writePersistenceReceipt(path string, receipt diskhealth.ObservationPersistenceReceipt) error {
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// gpu-risk defaults to read-only plan. Only explicit record may migrate/write
// the database selected through ATLAS_GPU_RISK_DSN; DSNs are never printed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"atlas/internal/gpurisk"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func run(args []string) error {
	command := "plan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("gpu-risk", flag.ContinueOnError)
	driver := flags.String("driver", "postgres", "postgres or sqlite")
	input := flags.String("input", "", "frozen plan JSON (required for record)")
	if flags.Parse(args) != nil || flags.NArg() != 0 || (command != "plan" && command != "record") {
		return fmt.Errorf("usage: gpu-risk [plan|record] [-driver postgres|sqlite] [-input plan.json]")
	}
	dsn := os.Getenv("ATLAS_GPU_RISK_DSN")
	if dsn == "" {
		return fmt.Errorf("ATLAS_GPU_RISK_DSN is required")
	}
	var dialector gorm.Dialector
	switch *driver {
	case "postgres":
		dialector = postgres.Open(dsn)
	case "sqlite":
		if command == "plan" {
			if strings.Contains(dsn, "?") || strings.HasPrefix(dsn, "file:") || dsn == ":memory:" {
				return fmt.Errorf("plan requires a plain existing SQLite path")
			}
			if _, e := os.Stat(dsn); e != nil {
				return fmt.Errorf("SQLite plan database must exist")
			}
			uri := url.URL{Scheme: "file", Path: dsn}
			values := url.Values{}
			values.Set("mode", "ro")
			uri.RawQuery = values.Encode()
			dsn = uri.String()
		}
		dialector = sqlite.Open(dsn)
	default:
		return fmt.Errorf("unsupported driver")
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		return fmt.Errorf("database connection failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("database unavailable")
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if command == "plan" {
		// Enforce read-only at the database connection, not only by convention.
		if *driver == "sqlite" {
			if db.Exec("PRAGMA query_only = ON").Error != nil {
				return fmt.Errorf("read-only guard failed")
			}
		} else {
			if db.Exec("SET default_transaction_read_only = on").Error != nil {
				return fmt.Errorf("read-only guard failed")
			}
		}
		p, e := gpurisk.Capture(ctx, db)
		if e != nil {
			return fmt.Errorf("snapshot capture failed; check inventory schema and source timestamps")
		}
		return json.NewEncoder(os.Stdout).Encode(p)
	}
	if *input == "" {
		return fmt.Errorf("record requires -input frozen-plan.json")
	}
	f, e := os.Open(*input)
	if e != nil {
		return fmt.Errorf("cannot open frozen plan")
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 32*1024*1024))
	decoder.DisallowUnknownFields()
	var p gpurisk.Snapshot
	if decoder.Decode(&p) != nil {
		return fmt.Errorf("invalid frozen plan JSON")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fmt.Errorf("trailing or oversized plan JSON")
	}
	if _, _, e := gpurisk.Build(p); e != nil {
		return fmt.Errorf("frozen plan validation failed")
	}
	if e := gpurisk.Migrate(db.WithContext(ctx)); e != nil {
		return fmt.Errorf("ranking schema migration failed")
	}
	q, e := gpurisk.Record(ctx, db, p)
	if e != nil {
		return fmt.Errorf("record failed: snapshot, lease or database check did not pass")
	}
	return json.NewEncoder(os.Stdout).Encode(q)
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

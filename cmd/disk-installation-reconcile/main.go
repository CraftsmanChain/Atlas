// disk-installation-reconcile reads an already collected local manifest. It
// never connects to a node or executes a device command.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"atlas/internal/diskhealth"
	"atlas/pkg/storage"
)

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("disk-installation-reconcile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "Local complete-scope manifest JSON (no node access)")
	expectedSHA := flags.String("manifest-sha256", "", "Required exact file SHA256 for --apply")
	apply := flags.Bool("apply", false, "Apply one atomic registry reconcile; default validates file only")
	driver := flags.String("db-driver", "sqlite", "Database driver for --apply: sqlite or postgres")
	dsnEnv := flags.String("db-dsn-env", "", "Environment variable containing database DSN for --apply")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *manifestPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "manifest is required; positional arguments are not accepted")
		return 2
	}
	if *apply && (*expectedSHA == "" || *dsnEnv == "" || os.Getenv(*dsnEnv) == "" || (*driver != "sqlite" && *driver != "postgres" && *driver != "postgresql")) {
		fmt.Fprintln(stderr, "apply requires manifest-sha256, a populated db-dsn-env, and sqlite/postgres driver")
		return 2
	}
	input, err := os.Open(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot open local installation manifest")
		return 1
	}
	raw, readErr := io.ReadAll(io.LimitReader(input, diskhealth.InstallationManifestMaxBytes+1))
	closeErr := input.Close()
	if readErr != nil || closeErr != nil {
		fmt.Fprintln(stderr, "cannot read local installation manifest")
		return 1
	}
	manifest, err := diskhealth.ParseInstallationReconcileManifest(raw)
	if err != nil {
		fmt.Fprintf(stderr, "installation manifest rejected: %v\n", err)
		return 1
	}
	sha := diskhealth.Digest(raw)
	if *expectedSHA != "" && sha != *expectedSHA {
		fmt.Fprintln(stderr, "installation manifest SHA256 mismatch")
		return 1
	}
	if !*apply {
		// Opening a database would run migrations. Plan-only intentionally does
		// not inspect credentials, open the DB, or claim live roster validation.
		blockers := []string{"database_binding_and_expected_version_not_checked"}
		if !manifest.Complete || len(manifest.Failures) != 0 {
			blockers = append(blockers, "incomplete_enumeration")
		}
		plan := struct {
			Schema                string   `json:"schema"`
			Mode                  string   `json:"mode"`
			ManifestSHA256        string   `json:"manifest_sha256"`
			ScopeID               string   `json:"scope_id"`
			ExpectedRosterVersion uint64   `json:"expected_roster_version"`
			Members               int      `json:"members"`
			Closures              int      `json:"closures"`
			Blockers              []string `json:"blockers"`
		}{diskhealth.InstallationReconcileSchema, "plan_only", sha, manifest.ScopeID, manifest.ExpectedRosterVersion, len(manifest.Members), len(manifest.Closures), blockers}
		if err := json.NewEncoder(stdout).Encode(plan); err != nil {
			return 1
		}
		return 0
	}
	if !manifest.Complete || len(manifest.Failures) != 0 {
		fmt.Fprintln(stderr, "incomplete installation enumeration cannot reconcile")
		return 1
	}
	db, err := openReconcileDB(*driver, os.Getenv(*dsnEnv))
	if err != nil {
		// Driver errors may include credentials/connection strings. This CLI's
		// public diagnostics deliberately contain no database error payload.
		fmt.Fprintln(stderr, "cannot initialize installation database")
		return 1
	}
	sqlDB, err := db.DB.DB()
	if err != nil {
		fmt.Fprintln(stderr, "cannot access installation database")
		return 1
	}
	defer sqlDB.Close()
	result, err := diskhealth.NewService(db).ReconcileInstallations(manifest, sha)
	if err != nil {
		// Validation errors are fixed strings; unexpected driver errors are
		// redacted as well, rather than interpolating a private payload.
		fmt.Fprintln(stderr, "installation reconcile rejected; registry transaction rolled back")
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return 1
	}
	return 0
}

// Schema migration belongs to Atlas startup, not a reconcile command. Opening
// an existing DB with a silent logger keeps failed DSNs and private SQL values
// out of CLI diagnostics and makes all CLI writes part of the roster transaction.
func openReconcileDB(driver, dsn string) (*storage.DB, error) {
	var dialector gorm.Dialector = sqlite.Open(dsn)
	if driver == "postgres" || driver == "postgresql" {
		dialector = postgres.Open(dsn)
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
	if driver == "sqlite" {
		if err := db.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
			_ = sqlDB.Close()
			return nil, err
		}
	}
	return &storage.DB{DB: db}, nil
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

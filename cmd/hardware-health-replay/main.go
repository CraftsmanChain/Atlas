package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"atlas/internal/diskhealth"
	"atlas/pkg/storage"
)

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hardware-health-replay", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifest := flags.String("manifest", "", "Approved file-only input manifest")
	audit := flags.String("acceptance-audit", "", "Existing hardware-evidence result.json; file-only, no SSH")
	generation := flags.String("generation", "", "Explicit disk identity generation for audit replay")
	sourceKind := flags.String("source-kind", "operator_snapshot", "Audit source: operator_snapshot or fixture")
	asOf := flags.String("as-of", "", "Fixed RFC3339 evaluation time")
	output := flags.String("out-dir", "", "New output directory (must not exist)")
	dbDriver := flags.String("db-driver", "sqlite", "Optional database driver for report persistence")
	dbDSN := flags.String("db-dsn", "", "Optional database DSN; when set, persist disk snapshots and episodes after report generation")
	dbDSNEnv := flags.String("db-dsn-env", "", "Environment variable containing optional DB DSN (keeps secrets out of argv)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *dbDSNEnv != "" {
		if *dbDSN != "" || os.Getenv(*dbDSNEnv) == "" {
			fmt.Fprintln(stderr, "db-dsn-env must be set and cannot be combined with db-dsn")
			return 2
		}
		*dbDSN = os.Getenv(*dbDSNEnv)
	}
	cutoff, err := time.Parse(time.RFC3339, *asOf)
	if err != nil || (*manifest == "") == (*audit == "") || (*audit != "" && *generation == "") || (*manifest != "" && *generation != "") || *output == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "as-of and out-dir required; choose exactly one of manifest or acceptance-audit; audit requires generation")
		return 2
	}
	var report diskhealth.Report
	inputPath := *manifest
	if *audit != "" {
		inputPath = *audit
		report, err = buildAcceptanceReport(*audit, *generation, *sourceKind, cutoff)
	} else {
		report, err = diskhealth.Build(*manifest, cutoff)
	}
	if err == nil {
		err = diskhealth.WriteReport(report, inputPath, *output)
	}
	if err == nil && *dbDSN != "" {
		var db *storage.DB
		db, err = storage.InitDBWithDriver(*dbDriver, *dbDSN)
		if err == nil {
			var persisted diskhealth.PersistResult
			persisted, err = diskhealth.NewService(db).PersistReport(report, "")
			if err == nil {
				fmt.Fprintf(stdout, "Persisted report: snapshots_created=%d duplicate=%d episodes_upserted=%d current_promoted=%d\n", persisted.SnapshotsCreated, persisted.SnapshotsDuplicate, persisted.EpisodesUpserted, persisted.CurrentPromoted)
			}
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "health replay failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Offline report: %s; records=%d assessed=%d insufficient=%d; no alerts/actions\n", *output, report.Summary.Total, report.Summary.Assessed, report.Summary.Unknown)
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

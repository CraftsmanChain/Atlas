package main

import (
	"context"
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
	bindingPath := flags.String("installation-binding", "", "Frozen local installation binding for pilot persistence")
	checkBinding := flags.Bool("check-installation-binding", false, "Read-only existing-registry preflight; requires no report input")
	persistenceReceipt := flags.String("persistence-receipt", "", "New post-commit persistence receipt file for bound pilot replay")
	deadlineText := flags.String("deadline", "", "Optional absolute RFC3339 deadline including database commit")
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
	ctx := context.Background()
	if *deadlineText != "" {
		deadline, err := time.Parse(time.RFC3339Nano, *deadlineText)
		if err != nil || !deadline.After(time.Now()) {
			fmt.Fprintln(stderr, "deadline must be a future RFC3339 instant")
			return 2
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	var binding diskhealth.ObservationBinding
	bound := *bindingPath != ""
	if bound || *checkBinding || *persistenceReceipt != "" {
		if !bound || *dbDSNEnv == "" || *dbDSN == "" || flags.NArg() != 0 || (!*checkBinding && *persistenceReceipt == "") || (*checkBinding && (*manifest != "" || *audit != "" || *output != "" || *persistenceReceipt != "")) {
			fmt.Fprintln(stderr, "bound replay requires installation-binding and db-dsn-env; persistence requires a new persistence-receipt; check mode accepts no report input")
			return 2
		}
		raw, err := diskhealth.ReadLimited(*bindingPath, diskhealth.InstallationManifestMaxBytes)
		if err == nil {
			binding, err = diskhealth.ParseObservationBinding(raw)
		}
		if err != nil {
			fmt.Fprintln(stderr, "installation binding file rejected")
			return 1
		}
		if *checkBinding {
			db, err := openExistingReplayDB(ctx, *dbDriver, *dbDSN)
			if err != nil {
				fmt.Fprintln(stderr, "cannot open existing observation database")
				return 1
			}
			sqlDB, _ := db.DB.DB()
			defer sqlDB.Close()
			if err := diskhealth.NewService(db).CheckObservationBinding(ctx, binding); err != nil {
				fmt.Fprintln(stderr, "installation binding preflight rejected")
				return 1
			}
			sha, _ := binding.SHA256()
			fmt.Fprintf(stdout, "Installation binding verified: scope_id=%s roster_version=%s binding_sha256=%s entities=%d\n", binding.ScopeID, binding.ExpectedRosterVersion, sha, len(binding.EntityIDs))
			return 0
		}
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
		if bound {
			db, err = openExistingReplayDB(ctx, *dbDriver, *dbDSN)
		} else {
			db, err = storage.InitDBWithDriver(*dbDriver, *dbDSN)
		}
		if err == nil {
			sqlDB, _ := db.DB.DB()
			defer sqlDB.Close()
			if bound {
				var receipt diskhealth.ObservationPersistenceReceipt
				var reportRaw []byte
				reportRaw, err = diskhealth.ReadLimited(*output+"/report.json", diskhealth.MaxBatchBytes)
				if err == nil {
					receipt, err = diskhealth.NewService(db).PersistReportWithBinding(ctx, report, diskhealth.Digest(reportRaw), binding)
				}
				if err == nil {
					err = writePersistenceReceipt(*persistenceReceipt, receipt)
				}
				if err == nil {
					fmt.Fprintf(stdout, "Persisted bound report: snapshots_created=%d duplicate=%d completed_at=%s\n", receipt.SnapshotsCreated, receipt.Duplicate, receipt.CompletedAt.Format(time.RFC3339Nano))
				}
			} else {
				var persisted diskhealth.PersistResult
				persisted, err = diskhealth.NewService(db).PersistReport(report, "")
				if err == nil {
					fmt.Fprintf(stdout, "Persisted report: snapshots_created=%d duplicate=%d episodes_upserted=%d current_promoted=%d\n", persisted.SnapshotsCreated, persisted.SnapshotsDuplicate, persisted.EpisodesUpserted, persisted.CurrentPromoted)
				}
			}
		}
	}
	if err != nil {
		if bound {
			fmt.Fprintln(stderr, "bound health replay failed; persistence state may be unknown; pause required")
		} else {
			fmt.Fprintf(stderr, "health replay failed: %v\n", err)
		}
		return 1
	}
	fmt.Fprintf(stdout, "Offline report: %s; records=%d assessed=%d insufficient=%d; no alerts/actions\n", *output, report.Summary.Total, report.Summary.Assessed, report.Summary.Unknown)
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

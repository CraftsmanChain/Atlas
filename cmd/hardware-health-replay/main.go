package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"atlas/internal/diskhealth"
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
	if err := flags.Parse(args); err != nil {
		return 2
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
	if err != nil {
		fmt.Fprintf(stderr, "health replay failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Offline report: %s; records=%d assessed=%d insufficient=%d; no alerts/actions\n", *output, report.Summary.Total, report.Summary.Assessed, report.Summary.Unknown)
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

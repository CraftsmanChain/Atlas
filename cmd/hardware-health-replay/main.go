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
	asOf := flags.String("as-of", "", "Fixed RFC3339 evaluation time")
	output := flags.String("out-dir", "", "New output directory (must not exist)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	cutoff, err := time.Parse(time.RFC3339, *asOf)
	if err != nil || *manifest == "" || *output == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "manifest, as-of and out-dir are required; no positional arguments")
		return 2
	}
	report, err := diskhealth.Build(*manifest, cutoff)
	if err == nil {
		err = diskhealth.WriteReport(report, *manifest, *output)
	}
	if err != nil {
		fmt.Fprintf(stderr, "health replay failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Offline report: %s; records=%d assessed=%d insufficient=%d; no alerts/actions\n", *output, report.Summary.Total, report.Summary.Assessed, report.Summary.Unknown)
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

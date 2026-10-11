package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicFixturesEndToEnd(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "report")
	args := []string{"--manifest", "../../internal/diskhealth/testdata/input.json", "--as-of", "2021-11-16T04:10:00Z", "--out-dir", outDir}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d error=%s", code, stderr.String())
	}
	for _, file := range []string{"report.json", "report.md", "manifest.json", "bundle.json"} {
		if _, err := os.Stat(filepath.Join(outDir, file)); err != nil {
			t.Fatal(err)
		}
	}
	if code := run(args, &stdout, &stderr); code == 0 {
		t.Fatal("overwrote existing artifact directory")
	}
}

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"--unknown"}, {"--manifest", "missing", "--as-of", "invalid", "--out-dir", "out"}, {"--manifest", "missing", "--as-of", "2026-10-09T00:00:00Z", "--out-dir", "out"}} {
		var out, err bytes.Buffer
		if run(args, &out, &err) == 0 || err.Len() == 0 {
			t.Fatalf("invalid arguments succeeded: %v", args)
		}
	}
}

func TestMemoryFixturesEndToEnd(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "report")
	var stdout, stderr bytes.Buffer
	args := []string{"--manifest", "../../internal/diskhealth/testdata/input-memory.json", "--as-of", "2026-10-10T00:10:00Z", "--out-dir", outDir}
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d error=%s", code, stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		RuleVersion string                                 `json:"rule_version"`
		Summary     struct{ Total, Assessed, Unknown int } `json:"summary"`
	}
	if json.Unmarshal(raw, &report) != nil || report.RuleVersion != "hardware-health-v1.3.0" || report.Summary.Total != 3 || report.Summary.Assessed != 2 || report.Summary.Unknown != 1 {
		t.Fatalf("invalid memory CLI report: %s", raw)
	}
}

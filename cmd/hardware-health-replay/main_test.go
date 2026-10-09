package main

import (
	"bytes"
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

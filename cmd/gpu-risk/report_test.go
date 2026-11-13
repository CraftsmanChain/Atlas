package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/internal/gpurisk"
	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestReportDailyWritesDatedAndLatestReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.sqlite")
	store, e := storage.InitDB(path)
	if e != nil {
		t.Fatal(e)
	}
	old := time.Now().UTC().Add(-time.Hour)
	if e := store.Create(&api.GPUNode{NodeIP: "report-node", AssetSerial: "report-sn", Lifecycle: "active", CreatedAt: old, UpdatedAt: old}).Error; e != nil {
		t.Fatal(e)
	}
	snapshot, e := gpurisk.Capture(t.Context(), store.DB)
	if e != nil {
		t.Fatal(e)
	}
	q, e := gpurisk.Record(t.Context(), store.DB, snapshot)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := store.DB.DB()
	raw.Close()
	before, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(t.TempDir(), "reports", "gpu-risk")
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	if e := run([]string{"report", "-driver", "sqlite", "-kind", "daily", "-out", out}); e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(path)
	if e != nil || string(after) != string(before) {
		t.Fatal("report modified database", e)
	}
	if st, e := os.Stat(out); e != nil || st.Mode().Perm()&0o027 != 0 {
		t.Fatal("report dir", e)
	}
	// The date is the Asia/Shanghai day; tolerate a run straddling midnight.
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	days := map[string]bool{time.Now().In(zone).Format("2006-01-02"): true, time.Now().Add(-time.Minute).In(zone).Format("2006-01-02"): true}
	dated := ""
	for d := range days {
		if _, e := os.Stat(filepath.Join(out, "daily-"+d+".json")); e == nil {
			dated = "daily-" + d
		}
	}
	if dated == "" {
		t.Fatal("dated report missing")
	}
	for _, name := range []string{dated + ".json", dated + ".md", "daily-latest.json", "daily-latest.md"} {
		st, e := os.Stat(filepath.Join(out, name))
		if e != nil || st.Mode().Perm() != 0o640 {
			t.Fatal(name, e)
		}
	}
	a, _ := os.ReadFile(filepath.Join(out, dated+".json"))
	b, _ := os.ReadFile(filepath.Join(out, "daily-latest.json"))
	if !bytes.Equal(a, b) {
		t.Fatal("latest differs from dated report")
	}
	var c gpurisk.DailyCheck
	if e := json.Unmarshal(a, &c); e != nil || c.Version != gpurisk.DailyCheckVersion || c.LatestQuery == nil || c.LatestQuery.ID != q.ID || c.Shadow.Exists {
		t.Fatalf("%+v %v", c, e)
	}
	found := false
	for _, f := range c.Findings {
		found = found || strings.HasPrefix(f, "WARN ") && strings.Contains(f, "缺少影子批次")
	}
	if !found {
		t.Fatal(c.Findings)
	}
	md, _ := os.ReadFile(filepath.Join(out, "daily-latest.md"))
	if !strings.HasPrefix(string(md), "# GPU 风险日检") {
		t.Fatal(string(md))
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 4 {
		t.Fatal("unexpected files (temp leftovers?)", len(entries))
	}
	// Rerun replaces atomically; weekly writes its own pair.
	if e := run([]string{"report", "-driver", "sqlite", "-kind", "daily", "-out", out}); e != nil {
		t.Fatal(e)
	}
	if e := run([]string{"report", "-driver", "sqlite", "-kind", "weekly", "-out", out}); e != nil {
		t.Fatal(e)
	}
	w, e := os.ReadFile(filepath.Join(out, "weekly-latest.json"))
	var r gpurisk.E2Report
	if e != nil || json.Unmarshal(w, &r) != nil || r.Version != gpurisk.E2PrimeVersion {
		t.Fatal("weekly report", e)
	}
	if entries, _ = os.ReadDir(out); len(entries) != 8 {
		t.Fatal("unexpected files", len(entries))
	}
}

func TestReportRejectsInvalidArguments(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.sqlite")
	t.Setenv("ATLAS_GPU_RISK_DSN", missing)
	out := filepath.Join(t.TempDir(), "out")
	for _, args := range [][]string{
		{"report", "-driver", "sqlite", "-kind", "monthly", "-out", out},
		{"report", "-driver", "sqlite", "-kind", "daily"},
		{"report", "-driver", "sqlite", "-out", out},
		{"report", "-driver", "sqlite", "-kind", "daily", "-out", out, "-query", "1"},
		{"plan", "-driver", "sqlite", "-kind", "daily"},
		{"report", "-driver", "sqlite", "-kind", "daily", "-out", out}, // absent DB
	} {
		if e := run(args); e == nil {
			t.Fatal("accepted", args)
		}
	}
	if _, e := os.Stat(missing); !os.IsNotExist(e) {
		t.Fatal("created DB", e)
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("created output", e)
	}
}

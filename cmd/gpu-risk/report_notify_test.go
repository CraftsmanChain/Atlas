package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"atlas/internal/gpurisk"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

// notifyReportDB creates a SQLite DB with one completed query and returns its
// path (closed, so the report opens it read-only).
func notifyReportDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notify.sqlite")
	store, e := storage.InitDB(path)
	if e != nil {
		t.Fatal(e)
	}
	old := time.Now().UTC().Add(-time.Hour)
	if e := store.Create(&api.GPUNode{NodeIP: "notify-node", AssetSerial: "notify-sn", Lifecycle: "active", CreatedAt: old, UpdatedAt: old}).Error; e != nil {
		t.Fatal(e)
	}
	snapshot, e := gpurisk.Capture(t.Context(), store.DB)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := gpurisk.Record(t.Context(), store.DB, snapshot); e != nil {
		t.Fatal(e)
	}
	raw, _ := store.DB.DB()
	raw.Close()
	return path
}

func injectSender(t *testing.T, fn func(configPath, title, template, content string) error) {
	t.Helper()
	old := sendReportCard
	sendReportCard = fn
	t.Cleanup(func() { sendReportCard = old })
}

func TestReportNotifySendsOnceAfterFilesWritten(t *testing.T) {
	path := notifyReportDB(t)
	t.Setenv("ATLAS_GPU_RISK_DSN", path)
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	for _, tc := range []struct{ kind, prefix string }{{"daily", "GPU 风险日检 "}, {"weekly", "GPU 风险 E2′ 周报 "}} {
		out := filepath.Join(t.TempDir(), "out")
		cfgPath := filepath.Join(t.TempDir(), "config.yaml")
		calls := 0
		injectSender(t, func(configPath, title, template, content string) error {
			calls++
			if configPath != cfgPath {
				t.Errorf("config path %q", configPath)
			}
			day := time.Now().In(zone).Format("2006-01-02")
			if !strings.HasPrefix(title, tc.prefix) || (!strings.HasSuffix(title, day) && !strings.HasSuffix(title, time.Now().Add(-time.Minute).In(zone).Format("2006-01-02"))) {
				t.Errorf("title %q", title)
			}
			if template == "" || content == "" || strings.Contains(content, "|") {
				t.Errorf("card %q %q", template, content)
			}
			for _, name := range []string{tc.kind + "-latest.json", tc.kind + "-latest.md"} {
				if _, e := os.Stat(filepath.Join(out, name)); e != nil {
					t.Errorf("sender called before %s was written: %v", name, e)
				}
			}
			return nil
		})
		if e := run([]string{"report", "-driver", "sqlite", "-kind", tc.kind, "-out", out, "-notify", "-config", cfgPath}); e != nil {
			t.Fatal(tc.kind, e)
		}
		if calls != 1 {
			t.Fatal(tc.kind, "sender calls", calls)
		}
	}
	if !t.Failed() {
		// -notify defaults to false: a plain report run must not push.
		injectSender(t, func(string, string, string, string) error { t.Fatal("sender called without -notify"); return nil })
		if e := run([]string{"report", "-driver", "sqlite", "-kind", "daily", "-out", filepath.Join(t.TempDir(), "o")}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestReportNotifyDefaultConfigPath(t *testing.T) {
	t.Setenv("ATLAS_GPU_RISK_DSN", notifyReportDB(t))
	got := ""
	injectSender(t, func(configPath, _, _, _ string) error { got = configPath; return nil })
	if e := run([]string{"report", "-driver", "sqlite", "-kind", "weekly", "-out", t.TempDir(), "-notify"}); e != nil {
		t.Fatal(e)
	}
	if got != "/ops/atlas/configs/config.yaml" {
		t.Fatal(got)
	}
}

func TestReportNotifyFailureKeepsFilesAndFails(t *testing.T) {
	t.Setenv("ATLAS_GPU_RISK_DSN", notifyReportDB(t))
	out := filepath.Join(t.TempDir(), "out")
	calls := 0
	injectSender(t, func(string, string, string, string) error { calls++; return errors.New("injected send failure") })
	e := run([]string{"report", "-driver", "sqlite", "-kind", "daily", "-out", out, "-notify"})
	if e == nil || !strings.Contains(e.Error(), "injected send failure") || calls != 1 {
		t.Fatal(e, calls)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 4 {
		t.Fatal("report files must be kept on push failure", len(entries))
	}
	for _, name := range []string{"daily-latest.json", "daily-latest.md"} {
		if st, e := os.Stat(filepath.Join(out, name)); e != nil || st.Size() == 0 {
			t.Fatal(name, e)
		}
	}
}

func TestReportNotifyFlagsRejectedOutsideReport(t *testing.T) {
	t.Setenv("ATLAS_GPU_RISK_DSN", filepath.Join(t.TempDir(), "absent.sqlite"))
	injectSender(t, func(string, string, string, string) error { t.Fatal("sender called"); return nil })
	for _, args := range [][]string{
		{"plan", "-driver", "sqlite", "-notify"},
		{"plan", "-driver", "sqlite", "-config", "/tmp/c.yaml"},
		{"outcomes", "-driver", "sqlite", "-query", "1", "-notify"},
		{"shadow-score", "-driver", "sqlite", "-query", "1", "-config", "/tmp/c.yaml"},
		{"ledger-declare", "-driver", "sqlite", "-through", "2026-01-01T00:00:00Z", "-owner", "x", "-notify"},
		{"-driver", "sqlite", "-notify"},
		{"report", "-driver", "sqlite", "-kind", "daily", "-out", t.TempDir(), "-notify", "-config", " "},
	} {
		e := run(args)
		if e == nil {
			t.Fatal("accepted", args)
		}
		if args[0] != "report" && !strings.Contains(e.Error(), "only valid for report") && !strings.Contains(e.Error(), "only accepts") {
			t.Fatal(args, e)
		}
	}
}

// The real sender must never surface the webhook URL/token or the secret.
func TestSendReportCardRedactsWebhook(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL + "/open-apis/bot/v2/hook/TOKENclosedABCDEF123"
	closed.Close()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := "feishu:\n  bots:\n" +
		"    - enabled: true\n      webhook_url: " + server.URL + "/open-apis/bot/v2/hook/TOKENliveABCDEF123\n      enable_signature: true\n      secret: SECRETxyz987\n" +
		"    - enabled: true\n      webhook_url: " + closedURL + "\n"
	if e := os.WriteFile(cfgPath, []byte(yaml), 0o600); e != nil {
		t.Fatal(e)
	}
	e := sendReportCard(cfgPath, "t", "green", "c")
	if e == nil || hits.Load() != 1 {
		t.Fatal("expected failure after one live attempt", e, hits.Load())
	}
	for _, leak := range []string{"TOKENliveABCDEF123", "TOKENclosedABCDEF123", "SECRETxyz987", "/hook/"} {
		if strings.Contains(e.Error(), leak) {
			t.Fatalf("error leaks %q: %v", leak, e)
		}
	}
	if e := sendReportCard(filepath.Join(dir, "missing.yaml"), "t", "green", "c"); e == nil || !strings.Contains(e.Error(), "does not exist") {
		t.Fatal(e)
	}
	if got := redactFeishu("x https://h/open-apis/bot/v2/hook/abcdefgh123 y abcdefgh123 S3cr3tS3cr3t", config.FeishuConfig{Bots: []config.FeishuBotConfig{{WebhookURL: "https://h/open-apis/bot/v2/hook/abcdefgh123", Secret: "S3cr3tS3cr3t"}}}); strings.Contains(got, "abcdefgh123") || strings.Contains(got, "S3cr3t") {
		t.Fatal(got)
	}
}

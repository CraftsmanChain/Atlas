package eventauditcli

import (
	"atlas/internal/eventaudit"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOfflineReplayAndNoOverwrite(t *testing.T) {
	base := t.TempDir()
	s := eventaudit.Snapshot{Version: eventaudit.Version, Namespace: "cluster-a", AsOf: time.Now().UTC(), Complete: true, Scope: "all_gpu_fault_events", Entries: []eventaudit.Entry{}}
	s.Seal()
	b, _ := json.Marshal(s)
	input := filepath.Join(base, "input.json")
	if e := os.WriteFile(input, b, 0600); e != nil {
		t.Fatal(e)
	}
	var out, errout bytes.Buffer
	dir := filepath.Join(base, "audit")
	args := []string{"-snapshot", input, "-previous", input, "-out-dir", dir}
	if code := Run(args, &out, &errout); code != 0 {
		t.Fatal(code, errout.String())
	}
	before, e := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if e != nil {
		t.Fatal(e)
	}
	if code := Run(args, &out, &errout); code == 0 {
		t.Fatal("overwrite allowed")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("modified existing artifact")
	}
	if code := Run([]string{"-snapshot", input, "-out-dir", filepath.Join(base, "other"), "unexpected"}, &out, &errout); code != 2 {
		t.Fatal(code)
	}
}
func TestConnectionSecretsNotPrinted(t *testing.T) {
	t.Setenv("AUDIT_TEST_DSN", "PRIVATE-SECRET-DO-NOT-PRINT")
	var out, errout bytes.Buffer
	code := Run([]string{"-dsn-env", "AUDIT_TEST_DSN", "-source-namespace", "test", "-out-dir", filepath.Join(t.TempDir(), "audit")}, &out, &errout)
	if code == 0 || bytes.Contains(errout.Bytes(), []byte("PRIVATE")) || bytes.Contains(out.Bytes(), []byte("PRIVATE")) {
		t.Fatal("unsafe connection failure", code)
	}
}

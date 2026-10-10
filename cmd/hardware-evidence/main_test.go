package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"atlas/internal/nodeaccess"
)

func cliScope(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	scope := nodeaccess.AcceptanceScope{Schema: "hardware-evidence-scope-v1", ScopeID: "local-cli-test",
		Node: "127.0.0.1", Username: "fixture-user", CredentialProfileID: "local-env",
		IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		Commands: []nodeaccess.AcceptanceRequest{{ID: "node.kernel"}}}
	raw, _ := json.Marshal(scope)
	path := filepath.Join(t.TempDir(), "scope.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIDefaultOnlyPlansAndNeverReadsKnownHostsOrCredentials(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "new-audit")
	var out, stderr bytes.Buffer
	args := []string{"--scope", cliScope(t), "--out-dir", outDir, "--known-hosts", "/not-existing", "--password-env", "UNCONFIGURED_TEST_ENV"}
	if code := run(context.Background(), args, &out, &stderr); code != 0 {
		t.Fatalf("plan failed %d: %s", code, stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "plan.json"))
	if err != nil || bytes.Contains(raw, []byte("fixture-user")) {
		t.Fatal("private plan missing or contains username")
	}
	var plan struct {
		Execution bool `json:"execution_requested"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil || plan.Execution {
		t.Fatal("dry-run attempted execution")
	}
	if _, err := os.Stat(filepath.Join(outDir, "result.json")); !os.IsNotExist(err) {
		t.Fatal("dry-run created a fake collection result")
	}
	info, _ := os.Stat(outDir)
	file, _ := os.Stat(filepath.Join(outDir, "plan.json"))
	if info.Mode().Perm() != 0o700 || file.Mode().Perm() != 0o600 {
		t.Fatal("audit files are not private")
	}
	if code := run(context.Background(), args, &out, &stderr); code == 0 {
		t.Fatal("existing audit directory overwritten")
	}
}

func TestCLIExecutionGatesBeforeAuditOrNetwork(t *testing.T) {
	for _, args := range [][]string{
		{"--execute"},
		{"--execute", "--confirm-node", "10.0.0.1", "--known-hosts", "/missing", "--password-env", "TEST_PASSWORD"},
		{"--execute", "--confirm-node", "127.0.0.1", "--known-hosts", "/missing", "--password-env", "not-an-env;"},
		{"--ssh-port", "0"},
		{"extra-positional"},
	} {
		outDir := filepath.Join(t.TempDir(), "new-audit")
		base := []string{"--scope", cliScope(t), "--out-dir", outDir}
		var output bytes.Buffer
		if code := run(context.Background(), append(base, args...), &output, &output); code == 0 {
			t.Fatalf("unsafe invocation accepted: %v", args)
		}
		if _, err := os.Stat(outDir); !os.IsNotExist(err) {
			t.Fatal("invalid execution gate reached persistence")
		}
	}
}

func TestCLITransportFailureRetainsIntentWithoutSecret(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "new-audit")
	t.Setenv("LOCAL_FIXTURE_PASSWORD", "never-persist-this")
	args := []string{"--scope", cliScope(t), "--out-dir", outDir, "--execute", "--confirm-node", "127.0.0.1", "--known-hosts", "/not-existing", "--password-env", "LOCAL_FIXTURE_PASSWORD"}
	var output bytes.Buffer
	if code := run(context.Background(), args, &output, &output); code == 0 {
		t.Fatal("missing known_hosts accepted")
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "plan.json"))
	if err != nil || bytes.Contains(raw, []byte("never-persist-this")) || bytes.Contains(output.Bytes(), []byte("never-persist-this")) {
		t.Fatal("missing intent or credential disclosure")
	}
}

func TestArtifactWriteNeverOverwritesAndChecksSize(t *testing.T) {
	dir := t.TempDir()
	sha, err := writeArtifact(dir, "plan.json", map[string]int{"a": 1})
	if err != nil || len(sha) != 64 {
		t.Fatal("artifact hash missing")
	}
	if _, err := writeArtifact(dir, "plan.json", map[string]int{"a": 2}); err == nil {
		t.Fatal("existing file overwritten")
	}
	if _, err := writeArtifact(dir, "large.json", string(bytes.Repeat([]byte("x"), 2<<20))); err == nil {
		t.Fatal("oversized artifact accepted")
	}
}

func TestCLIExecutionFailureHasResultAndHashManifest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	defer func() { _ = listener.Close(); <-done }()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(known, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(t.TempDir(), "new-audit")
	t.Setenv("LOCAL_CLI_FIXTURE_PASSWORD", "must-not-persist")
	var output bytes.Buffer
	args := []string{"--scope", cliScope(t), "--out-dir", outDir, "--execute", "--confirm-node", "127.0.0.1", "--known-hosts", known, "--password-env", "LOCAL_CLI_FIXTURE_PASSWORD", "--ssh-port", port}
	if code := run(context.Background(), args, &output, &output); code != 1 {
		t.Fatal("closed test transport should produce failed audited collection")
	}
	var audit nodeaccess.AcceptanceAudit
	raw, err := os.ReadFile(filepath.Join(outDir, "result.json"))
	if err != nil || json.Unmarshal(raw, &audit) != nil || audit.Status != "network_failed" || len(audit.Evidence) != 0 || audit.SSHHostVerified {
		t.Fatal("failed result fabricated command success or host verification")
	}
	for _, name := range []string{"plan.json", "result.json", "manifest.json"} {
		raw, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil || bytes.Contains(raw, []byte("must-not-persist")) || bytes.Contains(raw, []byte("fixture-user")) {
			t.Fatal("artifact missing or discloses credential")
		}
	}
	var manifest map[string]any
	raw, _ = os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if json.Unmarshal(raw, &manifest) != nil || manifest["ssh_host_verified"] != false || len(manifest["result_sha256"].(string)) != 64 {
		t.Fatal("failure manifest claims source authenticity")
	}
}

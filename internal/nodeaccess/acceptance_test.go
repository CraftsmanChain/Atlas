package nodeaccess

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func acceptanceTestScope(now time.Time) AcceptanceScope {
	return AcceptanceScope{Schema: "hardware-evidence-scope-v1", ScopeID: "local-test",
		Node: "127.0.0.1", Username: "test-user", CredentialProfileID: "local-env",
		IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		Commands: []AcceptanceRequest{{ID: "node.kernel"}}}
}

func TestAcceptancePlanGuards(t *testing.T) {
	now := time.Now().UTC()
	zero, negative, high := 0, -1, 32
	tests := []struct {
		name string
		edit func(*AcceptanceScope)
	}{
		{"schema", func(s *AcceptanceScope) { s.Schema = "other" }},
		{"hostname", func(s *AcceptanceScope) { s.Node = "host.example" }},
		{"target injection", func(s *AcceptanceScope) { s.Node = "127.0.0.1; reboot" }},
		{"target whitespace", func(s *AcceptanceScope) { s.Node += " " }},
		{"unspecified", func(s *AcceptanceScope) { s.Node = "0.0.0.0" }},
		{"multicast", func(s *AcceptanceScope) { s.Node = "224.0.0.1" }},
		{"user injection", func(s *AcceptanceScope) { s.Username = "root; reboot" }},
		{"scope traversal", func(s *AcceptanceScope) { s.ScopeID = "../test" }},
		{"missing profile", func(s *AcceptanceScope) { s.CredentialProfileID = "" }},
		{"future issued", func(s *AcceptanceScope) { s.IssuedAt = now.Add(time.Second) }},
		{"expired", func(s *AcceptanceScope) { s.ExpiresAt = now }},
		{"long permit", func(s *AcceptanceScope) { s.ExpiresAt = now.Add(time.Hour) }},
		{"missing issue", func(s *AcceptanceScope) { s.IssuedAt = time.Time{} }},
		{"empty commands", func(s *AcceptanceScope) { s.Commands = nil }},
		{"duplicate", func(s *AcceptanceScope) { s.Commands = append(s.Commands, s.Commands[0]) }},
		{"over budget", func(s *AcceptanceScope) { s.Commands = make([]AcceptanceRequest, 7) }},
		{"reboot", func(s *AcceptanceScope) { s.Commands[0].ID = "maintenance.node_reboot" }},
		{"free shell", func(s *AcceptanceScope) { s.Commands[0].ID = "sh -c uptime" }},
		{"unexpected controller", func(s *AcceptanceScope) { s.Commands[0].Controller = &zero }},
		{"unexpected path", func(s *AcceptanceScope) { s.Commands[0].Device = "/etc/passwd" }},
		{"missing controller", func(s *AcceptanceScope) { s.Commands[0].ID = "memory.edac.counters" }},
		{"negative controller", func(s *AcceptanceScope) {
			s.Commands[0] = AcceptanceRequest{ID: "memory.edac.counters", Controller: &negative}
		}},
		{"large controller", func(s *AcceptanceScope) {
			s.Commands[0] = AcceptanceRequest{ID: "memory.edac.driver", Controller: &high}
		}},
		{"unknown EDAC", func(s *AcceptanceScope) {
			s.Commands[0] = AcceptanceRequest{ID: "memory.edac.reset", Controller: &zero}
		}},
		{"disk missing", func(s *AcceptanceScope) { s.Commands[0].ID = "disk.smart" }},
		{"disk injection", func(s *AcceptanceScope) {
			s.Commands[0] = AcceptanceRequest{ID: "disk.smart", Device: "/dev/sda;reboot"}
		}},
		{"raid blind scan", func(s *AcceptanceScope) { s.Commands[0] = AcceptanceRequest{ID: "disk.smart", Device: "/dev/bus/0"} }},
		{"disk type unknown", func(s *AcceptanceScope) { s.Commands[0] = AcceptanceRequest{ID: "disk.smart", Device: "/dev/sda"} }},
		{"disk type auto", func(s *AcceptanceScope) {
			s.Commands[0] = AcceptanceRequest{ID: "disk.smart", Device: "/dev/sda", Protocol: "auto"}
		}},
		{"disk type mismatch", func(s *AcceptanceScope) {
			s.Commands[0] = AcceptanceRequest{ID: "disk.smart", Device: "/dev/nvme0n1", Protocol: "ata"}
		}},
		{"disk type scsi", func(s *AcceptanceScope) {
			s.Commands[0] = AcceptanceRequest{ID: "disk.smart", Device: "/dev/sda", Protocol: "scsi"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope := acceptanceTestScope(now)
			test.edit(&scope)
			if _, err := PlanAcceptance(scope, now); err == nil {
				t.Fatal("unsafe scope accepted")
			}
		})
	}
	scope := acceptanceTestScope(now)
	scope.Commands = []AcceptanceRequest{{ID: "node.kernel"}, {ID: "node.boot_id"}, {ID: "node.storage"},
		{ID: "memory.edac.counters", Controller: &zero}, {ID: "memory.edac.driver", Controller: &zero},
		{ID: "disk.smart", Device: "/dev/nvme0n1", Protocol: "nvme"}}
	plan, err := PlanAcceptance(scope, now)
	if err != nil || len(plan.Commands) != 6 || !plan.NoChangeExecuted {
		t.Fatalf("safe plan failed: %v", err)
	}
	for _, command := range plan.Commands {
		wire := acceptanceExec(command.Argv)
		if strings.ContainsAny(wire, ";|`$\n") {
			t.Fatalf("not a single registered argv: %q", wire)
		}
	}
	raw, _ := json.Marshal(plan)
	if bytes.Contains(raw, []byte(scope.Username)) || bytes.Contains(raw, []byte("secret_ref")) {
		t.Fatal("plan exposes credentials or username")
	}
	ata, err := acceptanceTemplate(AcceptanceRequest{ID: "disk.smart", Device: "/dev/sda", Protocol: "ata"})
	if err != nil || acceptanceExec(ata.Argv) != "'smartctl' '-d' 'ata' '-j' '-a' '-n' 'standby,3,5' '/dev/sda'" {
		t.Fatal("ATA guard must prevent autodetection and unsupported power-check fallback")
	}
}

func TestAcceptanceScopeJSONGuards(t *testing.T) {
	for _, raw := range []string{
		`{"node":"127.0.0.1","node":"10.0.0.1"}`,
		`{"node":"127.0.0.1","Node":"10.0.0.1"}`,
		`{"commands":[{"id":"node.kernel","ID":"reboot"}]}`,
		`{"argv":["reboot"]}`, `{} {}`, `{"commands":[`,
		strings.Repeat(" ", 16<<10) + `{}`,
		`{"commands":` + strings.Repeat("[", 10) + `0` + strings.Repeat("]", 10) + `}`,
	} {
		if _, err := ReadAcceptanceScope(strings.NewReader(raw)); err == nil {
			t.Fatalf("invalid JSON accepted: %.80s", raw)
		}
	}
	raw, _ := json.Marshal(acceptanceTestScope(time.Now().UTC()))
	if _, err := ReadAcceptanceScope(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptanceCaptureSharesCommandAndBatchBudget(t *testing.T) {
	batch := &acceptanceCaptureBudget{remaining: 100}
	c := &acceptanceCapture{budget: batch, remaining: 80}
	var wg sync.WaitGroup
	for _, stderr := range []bool{true, false} {
		wg.Add(1)
		go func(stderr bool) {
			defer wg.Done()
			_, _ = (acceptanceStream{c, stderr}).Write(bytes.Repeat([]byte("x"), 100))
		}(stderr)
	}
	wg.Wait()
	out, errout, outCut, errCut := c.snapshot()
	if len(out)+len(errout) != 80 || !outCut || !errCut || batch.remaining != 20 {
		t.Fatal("independent streams inflated shared budget")
	}
	c2 := &acceptanceCapture{budget: batch, remaining: 80}
	_, _ = (acceptanceStream{c2, false}).Write(bytes.Repeat([]byte("y"), 30))
	out, _, cut, _ := c2.snapshot()
	if len(out) != 20 || !cut || batch.remaining != 0 {
		t.Fatal("batch budget not shared across commands")
	}
}

func TestAcceptanceRawBytesAndSensitiveStreams(t *testing.T) {
	raw := []byte{' ', '\r', '\n', 0xff, 0, '\n'}
	got, redacted := redactAcceptance(raw, "test-user", []byte("test-password"))
	if redacted || !bytes.Equal(raw, got) {
		t.Fatal("non-sensitive raw bytes changed")
	}
	for _, raw := range []string{"test-user", "test-password", "password=other-secret", `{"password":"other-secret"}`, `{"api_key":"other-secret"}`, "Bearer abc123", "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-body"} {
		got, redacted := redactAcceptance([]byte(raw), "test-user", []byte("test-password"))
		if !redacted || string(got) != "[WITHHELD SENSITIVE STREAM]" {
			t.Fatal("sensitive stream not entirely withheld")
		}
	}
}

func TestAcceptanceSSHStreamsAndActualExit(t *testing.T) {
	for _, code := range []uint32{0, 8} {
		t.Run(strconv.Itoa(int(code)), func(t *testing.T) {
			executor, scope := acceptanceSSHFixture(t, func(channel ssh.Channel, command string) {
				if command != "'uname' '-r'" {
					t.Errorf("unregistered command: %q", command)
				}
				_, _ = channel.Write([]byte("{\"counter\":18446744073709551615}\n"))
				_, _ = channel.Stderr().Write([]byte("query warning\n"))
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
			})
			audit, err := executor.Collect(context.Background(), scope, []byte("test-password"))
			if err != nil || len(audit.Evidence) != 1 || !audit.SSHHostVerified || !strings.HasPrefix(audit.SSHHostFingerprint, "SHA256:") || audit.SSHPort == 0 {
				t.Fatalf("collect failed: %v %s", err, audit.Status)
			}
			e := audit.Evidence[0]
			if !e.ExecutionStarted || e.ExitCode == nil || *e.ExitCode != int(code) || string(e.Stderr) != "query warning\n" ||
				string(e.Stdout) != "{\"counter\":18446744073709551615}\n" || e.RedactionApplied {
				t.Fatalf("stream/exit contract lost: %#v", e)
			}
			if code == 0 && audit.Status != "completed" || code != 0 && audit.Status != "partial" {
				t.Fatal("nonzero exit classification lost")
			}
			if e.StdoutSHA256 != acceptanceHash(e.Stdout) || e.StderrSHA256 != acceptanceHash(e.Stderr) ||
				audit.HealthAssessment != "not_assessed" || !audit.NoChangeExecuted {
				t.Fatal("audit fingerprints or safety semantics lost")
			}
		})
	}
}

func TestAcceptanceSSHMissingExitAndCancellationStopPacket(t *testing.T) {
	for _, test := range []struct {
		name, status string
		stall        bool
	}{
		{"missing exit", "exit_missing", false},
		{"cancelled", "cancelled", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor, scope := acceptanceSSHFixture(t, func(channel ssh.Channel, _ string) {
				if test.stall {
					time.Sleep(500 * time.Millisecond)
				}
			})
			scope.Commands = append(scope.Commands, AcceptanceRequest{ID: "node.boot_id"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.stall {
				time.AfterFunc(150*time.Millisecond, cancel)
			}
			start := time.Now()
			audit, err := executor.Collect(ctx, scope, []byte("test-password"))
			if err != nil || len(audit.Evidence) != 1 || audit.Evidence[0].Status != test.status ||
				audit.Evidence[0].ExitCode != nil || len(audit.MissingCommandIDs) != 1 ||
				audit.Evidence[0].TerminationVerified || time.Since(start) > 3*time.Second {
				t.Fatalf("failed stop/null-exit contract: %v %#v", err, audit)
			}
		})
	}
}

func TestAcceptanceSSHTruncationAndCredentialWithholding(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		cut  bool
	}{
		{"large stream", bytes.Repeat([]byte("x"), acceptanceCommandBytes+1), true},
		{"credential", []byte("test-password\n"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor, scope := acceptanceSSHFixture(t, func(channel ssh.Channel, _ string) {
				_, _ = channel.Write(test.data)
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
			})
			scope.Commands = append(scope.Commands, AcceptanceRequest{ID: "node.boot_id"})
			audit, err := executor.Collect(context.Background(), scope, []byte("test-password"))
			if err != nil || len(audit.Evidence) == 0 || !audit.Evidence[0].RedactionApplied || audit.Status != "partial" {
				t.Fatal("unsafe evidence not marked partial")
			}
			if test.cut && (!audit.Evidence[0].StdoutTruncated || len(audit.Evidence[0].Stdout) != 0 || len(audit.Evidence) != 1) {
				t.Fatal("truncated stream retained or packet continued")
			}
			raw, _ := json.Marshal(audit)
			for _, e := range audit.Evidence {
				if bytes.Contains(e.Stdout, []byte("test-password")) || bytes.Contains(e.Stderr, []byte("test-password")) {
					t.Fatal("credential persisted in base64 stream")
				}
			}
			if bytes.Contains(raw, []byte(scope.Username)) {
				t.Fatal("username exposed in audit")
			}
		})
	}
}

func TestAcceptanceSSHHostKeyAndAuthStop(t *testing.T) {
	executor, scope := acceptanceSSHFixture(t, func(ssh.Channel, string) { t.Error("must not execute") })
	for _, secret := range [][]byte{nil, []byte("wrong-password")} {
		audit, err := executor.Collect(context.Background(), scope, secret)
		if err != nil || len(audit.Evidence) != 0 || audit.SSHHostVerified || audit.Status == "completed" {
			t.Fatal("unavailable/rejected credential executed")
		}
	}
	empty := t.TempDir() + "/known_hosts"
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unknown, err := NewAcceptanceSSH(executor.transport.port, empty)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := unknown.Collect(context.Background(), scope, []byte("test-password"))
	if err != nil || audit.Status != "host_identity_failed" || len(audit.Evidence) != 0 {
		t.Fatal("unknown host identity not rejected")
	}
}

func TestAcceptanceSSHSignalIsNotAnActualExitCode(t *testing.T) {
	executor, scope := acceptanceSSHFixture(t, func(channel ssh.Channel, _ string) {
		payload := struct {
			Signal string
			Core   bool
			Error  string
			Lang   string
		}{Signal: "TERM"}
		_, _ = channel.SendRequest("exit-signal", false, ssh.Marshal(payload))
	})
	audit, err := executor.Collect(context.Background(), scope, []byte("test-password"))
	if err != nil || len(audit.Evidence) != 1 || audit.Evidence[0].Status != "exit_signal" || audit.Evidence[0].ExitCode != nil {
		t.Fatal("synthetic 128+signal incorrectly persisted as actual exit")
	}
}

func TestAcceptanceCommandDeadlineIncludesSessionCreation(t *testing.T) {
	for _, openStalls := range []bool{false, true} {
		t.Run(strconv.FormatBool(openStalls), func(t *testing.T) {
			pause := func() {
				if openStalls {
					time.Sleep(500 * time.Millisecond)
				}
			}
			executor, scope := acceptanceSSHFixture(t, func(ssh.Channel, string) {
				time.Sleep(500 * time.Millisecond)
			}, pause)
			client, err := executor.transport.connect(context.Background(), scope.Node, scope.Username, "password", []byte("test-password"))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			command, _ := acceptanceTemplate(scope.Commands[0])
			start := time.Now()
			record := runAcceptanceCommand(context.Background(), client, command, &acceptanceCaptureBudget{remaining: 100}, 30*time.Millisecond)
			if record.Status != "timed_out" || record.ExitCode != nil || record.TerminationVerified || time.Since(start) > 300*time.Millisecond {
				t.Fatal("timeout fabricated successful exit/remote termination or missed session open")
			}
		})
	}
}

func acceptanceSSHFixture(t *testing.T, execute func(ssh.Channel, string), beforeSession ...func()) (*AcceptanceSSH, AcceptanceScope) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if string(password) != "test-password" {
			return nil, errors.New("rejected")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if len(beforeSession) > 0 {
						beforeSession[0]()
					}
					if incoming.ChannelType() != "session" {
						_ = incoming.Reject(ssh.Prohibited, "not a session")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					for request := range requests {
						var payload struct{ Command string }
						if request.Type != "exec" || ssh.Unmarshal(request.Payload, &payload) != nil {
							_ = request.Reply(false, nil)
							continue
						}
						_ = request.Reply(true, nil)
						execute(channel, payload.Command)
						_ = channel.Close()
						break
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); wg.Wait() })
	address := listener.Addr().String()
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	path := t.TempDir() + "/known_hosts"
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{address}, signer.PublicKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor, err := NewAcceptanceSSH(port, path)
	if err != nil {
		t.Fatal(err)
	}
	return executor, acceptanceTestScope(time.Now().UTC())
}

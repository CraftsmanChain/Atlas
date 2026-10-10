package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"atlas/internal/nodeaccess"
)

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hardware-evidence", flag.ContinueOnError)
	flags.SetOutput(stderr)
	scopeFile := flags.String("scope", "", "Operator-scoped typed JSON (no secrets)")
	outDir := flags.String("out-dir", "", "New private local audit directory")
	execute := flags.Bool("execute", false, "Execute only after separately approved remote scope")
	confirmNode := flags.String("confirm-node", "", "Must exactly match scope node for execution")
	knownHosts := flags.String("known-hosts", "", "Existing verified known_hosts (never modified)")
	passwordEnv := flags.String("password-env", "", "Local environment reference; never a password value")
	port := flags.Int("ssh-port", 22, "SSH port bound into the audit plan")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *scopeFile == "" || *outDir == "" || flags.NArg() != 0 || *port < 1 || *port > 65535 {
		fmt.Fprintln(stderr, "scope and new out-dir required; no positional arguments; invalid port rejected")
		return 2
	}
	input, err := os.Open(*scopeFile)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read scope file")
		return 1
	}
	scope, err := nodeaccess.ReadAcceptanceScope(input)
	_ = input.Close()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	plan, err := nodeaccess.PlanAcceptance(scope, time.Now().UTC())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *execute && (*confirmNode != scope.Node || *knownHosts == "" || !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`).MatchString(*passwordEnv)) {
		fmt.Fprintln(stderr, "execution requires exact confirm-node, existing known-hosts and a valid password environment reference")
		return 2
	}
	// Reserve and persist the intent before resolving credentials or opening SSH.
	// Existing directories are rejected; failures retain their local audit files.
	if err := os.Mkdir(*outDir, 0o700); err != nil {
		fmt.Fprintln(stderr, "cannot create new audit directory; nothing overwritten")
		return 1
	}
	intent := struct {
		Plan      nodeaccess.AcceptancePlan `json:"plan"`
		SSHPort   int                       `json:"ssh_port"`
		Execution bool                      `json:"execution_requested"`
	}{plan, *port, *execute}
	planSHA, err := writeArtifact(*outDir, "plan.json", intent)
	if err != nil {
		fmt.Fprintln(stderr, "cannot persist audit intent; SSH not attempted")
		return 1
	}
	if !*execute {
		fmt.Fprintln(stdout, "Read-only plan created; no SSH, credential resolution or device command executed")
		return 0
	}
	transport, err := nodeaccess.NewAcceptanceSSH(*port, *knownHosts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	secret := []byte(os.Getenv(*passwordEnv))
	defer func() {
		for i := range secret {
			secret[i] = 0
		}
	}()
	if len(secret) > 4096 {
		fmt.Fprintln(stderr, "credential unavailable or invalid; SSH not attempted")
		return 1
	}
	audit, err := transport.Collect(ctx, scope, secret)
	if err != nil {
		fmt.Fprintln(stderr, "scope expired or invalid before execution; SSH not attempted")
		return 1
	}
	resultSHA, err := writeArtifact(*outDir, "result.json", audit)
	if err != nil {
		fmt.Fprintln(stderr, "cannot persist result; do not repeat collection without inspecting audit intent")
		return 1
	}
	_, err = writeArtifact(*outDir, "manifest.json", map[string]any{
		"schema": nodeaccess.AcceptanceVersion, "plan_sha256": planSHA,
		"result_sha256": resultSHA, "stream_encoding": "base64", "credential_provider": "local_environment",
		"scope_authority":   "operator_declaration_not_cryptographic_authorization",
		"ssh_host_verified": audit.SSHHostVerified, "component_identity_verified": false, "no_change_executed": true,
	})
	if err != nil {
		fmt.Fprintln(stderr, "cannot persist artifact manifest")
		return 1
	}
	fmt.Fprintf(stdout, "Read-only evidence status=%s; command_attempts=%d; health not assessed\n", audit.Status, len(audit.Evidence))
	if audit.Status != "completed" {
		return 1
	}
	return 0
}

func writeArtifact(dir, name string, value any) (string, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	if len(raw) > 2<<20 {
		return "", fmt.Errorf("artifact exceeds 2 MiB")
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

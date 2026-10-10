package nodeaccess

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const AcceptanceVersion = "hardware-evidence-acceptance-v1"

const (
	acceptanceCommandBytes = 256 << 10
	acceptanceBatchBytes   = 1 << 20
)

// A scope is an operator declaration, not a cryptographic authorization or an
// asset registration. The CLI requires a separate explicit execution switch.
type AcceptanceScope struct {
	Schema              string              `json:"schema"`
	ScopeID             string              `json:"scope_id"`
	Node                string              `json:"node"`
	Username            string              `json:"username"`
	CredentialProfileID string              `json:"credential_profile_id"`
	IssuedAt            time.Time           `json:"issued_at"`
	ExpiresAt           time.Time           `json:"expires_at"`
	Commands            []AcceptanceRequest `json:"commands"`
}

type AcceptanceRequest struct {
	ID         string `json:"id"`
	Controller *int   `json:"controller,omitempty"`
	Device     string `json:"device,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

type AcceptanceCommand struct {
	ID   string   `json:"id"`
	Argv []string `json:"argv"`
}

type AcceptancePlan struct {
	Schema              string              `json:"schema"`
	ScopeID             string              `json:"scope_id"`
	Node                string              `json:"node"`
	CredentialProfileID string              `json:"credential_profile_id"`
	IssuedAt            time.Time           `json:"issued_at"`
	ExpiresAt           time.Time           `json:"expires_at"`
	Commands            []AcceptanceCommand `json:"commands"`
	CommandTimeout      int                 `json:"command_timeout_seconds"`
	ConnectTimeout      int                 `json:"connect_timeout_seconds"`
	TotalTimeout        int                 `json:"total_timeout_seconds"`
	CommandOutputBytes  int                 `json:"command_output_bytes"`
	BatchOutputBytes    int                 `json:"batch_output_bytes"`
	NoChangeExecuted    bool                `json:"no_change_executed"`
}

type AcceptanceEvidence struct {
	ID                  string    `json:"id"`
	Argv                []string  `json:"argv"`
	Status              string    `json:"status"`
	ExecutionStarted    bool      `json:"execution_started"`
	StartedAt           time.Time `json:"started_at"`
	FinishedAt          time.Time `json:"finished_at"`
	ExitCode            *int      `json:"exit_code"`
	Stdout              []byte    `json:"stdout_base64"`
	Stderr              []byte    `json:"stderr_base64"`
	StdoutSHA256        string    `json:"stdout_sha256"`
	StderrSHA256        string    `json:"stderr_sha256"`
	StdoutTruncated     bool      `json:"stdout_truncated"`
	StderrTruncated     bool      `json:"stderr_truncated"`
	RedactionApplied    bool      `json:"redaction_applied"`
	TerminationVerified bool      `json:"remote_termination_verified"`
}

type AcceptanceAudit struct {
	Schema              string               `json:"schema"`
	Plan                AcceptancePlan       `json:"plan"`
	Status              string               `json:"status"`
	StartedAt           time.Time            `json:"started_at"`
	FinishedAt          time.Time            `json:"finished_at"`
	Evidence            []AcceptanceEvidence `json:"evidence"`
	MissingCommandIDs   []string             `json:"missing_command_ids"`
	NoChangeExecuted    bool                 `json:"no_change_executed"`
	NoCredentialExposed bool                 `json:"no_credential_exposed"`
	HealthAssessment    string               `json:"health_assessment"`
	SSHHostVerified     bool                 `json:"ssh_host_verified"`
	SSHHostFingerprint  string               `json:"ssh_host_fingerprint,omitempty"`
	SSHPort             int                  `json:"ssh_port"`
}

var acceptanceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var acceptanceDevice = regexp.MustCompile(`^/dev/(sd[a-z]{1,3}|nvme[0-9]{1,3}n[0-9]{1,3})$`)

func ReadAcceptanceScope(r io.Reader) (AcceptanceScope, error) {
	var scope AcceptanceScope
	raw, err := io.ReadAll(io.LimitReader(r, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return scope, errors.New("scope input unreadable or larger than 16 KiB")
	}
	if err := checkScopeJSON(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return scope, errors.New("invalid or ambiguous scope JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&scope); err != nil {
		return scope, errors.New("invalid scope fields")
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return scope, errors.New("scope must contain one JSON object")
	}
	return scope, nil
}

func checkScopeJSON(d *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("scope JSON too deep")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[strings.ToLower(name)] {
				return errors.New("duplicate scope key")
			}
			seen[strings.ToLower(name)] = true
		}
		if err := checkScopeJSON(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func PlanAcceptance(scope AcceptanceScope, now time.Time) (AcceptancePlan, error) {
	plan := AcceptancePlan{}
	ip := net.ParseIP(scope.Node)
	if scope.Schema != "hardware-evidence-scope-v1" || ip == nil || ip.String() != scope.Node ||
		ip.IsUnspecified() || ip.IsMulticast() || !acceptanceName.MatchString(scope.ScopeID) ||
		!acceptanceName.MatchString(scope.Username) || !acceptanceName.MatchString(scope.CredentialProfileID) {
		return plan, errors.New("invalid scope identity")
	}
	if scope.IssuedAt.IsZero() || scope.IssuedAt.After(now) || !scope.ExpiresAt.After(now) ||
		!scope.ExpiresAt.After(scope.IssuedAt) || scope.ExpiresAt.Sub(scope.IssuedAt) > 30*time.Minute {
		return plan, errors.New("scope must be current and expire within 30 minutes of issue")
	}
	if len(scope.Commands) < 1 || len(scope.Commands) > 6 {
		return plan, errors.New("scope must contain 1 to 6 commands")
	}
	plan = AcceptancePlan{
		Schema: AcceptanceVersion, ScopeID: scope.ScopeID, Node: scope.Node,
		CredentialProfileID: scope.CredentialProfileID, IssuedAt: scope.IssuedAt, ExpiresAt: scope.ExpiresAt,
		Commands: []AcceptanceCommand{}, ConnectTimeout: 5, CommandTimeout: 10, TotalTimeout: 120,
		CommandOutputBytes: acceptanceCommandBytes, BatchOutputBytes: acceptanceBatchBytes, NoChangeExecuted: true,
	}
	seen := map[string]bool{}
	var controller *int
	for _, request := range scope.Commands {
		if seen[request.ID] {
			return AcceptancePlan{}, errors.New("duplicate command ID; only one disk/controller per scope")
		}
		seen[request.ID] = true
		command, err := acceptanceTemplate(request)
		if err != nil {
			return AcceptancePlan{}, err
		}
		if request.Controller != nil {
			if controller != nil && *controller != *request.Controller {
				return AcceptancePlan{}, errors.New("one controller per scope; counters and driver must match")
			}
			controller = request.Controller
		}
		plan.Commands = append(plan.Commands, command)
	}
	return plan, nil
}

func acceptanceTemplate(r AcceptanceRequest) (AcceptanceCommand, error) {
	c := AcceptanceCommand{ID: r.ID}
	if strings.HasPrefix(r.ID, "memory.edac.") {
		if r.Controller == nil || *r.Controller < 0 || *r.Controller > 31 || r.Device != "" || r.Protocol != "" {
			return c, errors.New("EDAC requires one controller index between 0 and 31")
		}
		base := "/sys/devices/system/edac/mc/mc" + strconv.Itoa(*r.Controller)
		switch r.ID {
		case "memory.edac.counters":
			c.Argv = []string{"head", "-v", "-c", "4096"}
			for _, field := range []string{"mc_name", "ce_count", "ue_count", "ce_noinfo_count", "ue_noinfo_count", "seconds_since_reset"} {
				c.Argv = append(c.Argv, base+"/"+field)
			}
		case "memory.edac.driver":
			c.Argv = []string{"readlink", "-e", base + "/device/driver"}
		default:
			return c, errors.New("unregistered acceptance command")
		}
		return c, nil
	}
	if r.Controller != nil {
		return c, errors.New("controller parameter is not allowed for this command")
	}
	if r.ID == "disk.smart" {
		if !acceptanceDevice.MatchString(r.Device) {
			return c, errors.New("SMART requires one explicit sd or NVMe namespace path")
		}
		switch {
		case r.Protocol == "ata" && strings.HasPrefix(r.Device, "/dev/sd"):
			c.Argv = []string{"smartctl", "-d", "ata", "-j", "-a", "-n", "standby,3,5", r.Device}
		case r.Protocol == "nvme" && strings.HasPrefix(r.Device, "/dev/nvme"):
			c.Argv = []string{"smartctl", "-d", "nvme", "-j", "-a", r.Device}
		default:
			return c, errors.New("SMART requires an explicit matching ata/nvme protocol; no autodetection or RAID passthrough")
		}
		return c, nil
	}
	if r.Device != "" || r.Protocol != "" {
		return c, errors.New("device/protocol parameters are not allowed for this command")
	}
	switch r.ID {
	case "node.kernel":
		c.Argv = []string{"uname", "-r"}
	case "node.boot_id":
		c.Argv = []string{"cat", "/proc/sys/kernel/random/boot_id"}
	case "node.memory":
		c.Argv = []string{"cat", "/proc/meminfo"}
	case "node.storage":
		c.Argv = []string{"lsblk", "--json", "--bytes", "--output", "NAME,TYPE,SIZE,MODEL,TRAN"}
	default:
		return c, errors.New("unregistered acceptance command")
	}
	return c, nil
}

// Shell quoting is only serialization of registered argv for SSH exec. No
// caller can supply an executable, a shell program, an option or a file path.
func acceptanceExec(argv []string) string {
	quoted := make([]string, len(argv))
	for i, value := range argv {
		quoted[i] = "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

type AcceptanceSSH struct{ transport *SSHReadOnlyExecutor }

func NewAcceptanceSSH(port int, knownHostsFile string) (*AcceptanceSSH, error) {
	transport, err := NewSSHReadOnlyExecutor(port, 5*time.Second, knownHostsFile)
	if err != nil {
		return nil, errors.New("verified SSH transport unavailable; check port and existing known_hosts")
	}
	return &AcceptanceSSH{transport: transport}, nil
}

func (e *AcceptanceSSH) Collect(ctx context.Context, scope AcceptanceScope, secret []byte) (AcceptanceAudit, error) {
	started := time.Now().UTC()
	plan, err := PlanAcceptance(scope, started)
	audit := AcceptanceAudit{Schema: AcceptanceVersion, Plan: plan, StartedAt: started,
		Evidence: []AcceptanceEvidence{}, MissingCommandIDs: []string{}, NoChangeExecuted: true,
		NoCredentialExposed: true, HealthAssessment: "not_assessed", Status: "scope_rejected"}
	if err != nil {
		return audit, err
	}
	for _, c := range plan.Commands {
		audit.MissingCommandIDs = append(audit.MissingCommandIDs, c.ID)
	}
	finish := func() { audit.FinishedAt = time.Now().UTC() }
	if len(secret) == 0 || len(secret) > 4096 {
		audit.Status = "credential_unavailable"
		finish()
		return audit, nil
	}
	if e == nil || e.transport == nil {
		audit.Status = "transport_unavailable"
		finish()
		return audit, nil
	}
	deadline := started.Add(120 * time.Second)
	if scope.ExpiresAt.Before(deadline) {
		deadline = scope.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	transport := *e.transport
	var hostFingerprint string
	verifyHost := transport.hostKeyCallback
	transport.hostKeyCallback = func(host string, remote net.Addr, key ssh.PublicKey) error {
		if err := verifyHost(host, remote, key); err != nil {
			return err
		}
		hostFingerprint = ssh.FingerprintSHA256(key)
		return nil
	}
	audit.SSHPort = transport.port
	client, err := transport.connect(ctx, scope.Node, scope.Username, "password", secret)
	if err != nil {
		switch {
		case errors.Is(err, ErrHostIdentityFailed):
			audit.Status = "host_identity_failed"
		case errors.Is(err, ErrAuthenticationRejected):
			audit.Status = "credential_rejected"
		default:
			audit.Status = "network_failed"
		}
		finish()
		return audit, nil
	}
	defer client.Close()
	audit.SSHHostVerified = true
	audit.SSHHostFingerprint = hostFingerprint
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()
	batch := &acceptanceCaptureBudget{remaining: acceptanceBatchBytes}
	audit.Status = "completed"
	for i, command := range plan.Commands {
		if ctx.Err() != nil {
			audit.Status = "cancelled"
			break
		}
		record := runAcceptanceCommand(ctx, client, command, batch, 10*time.Second)
		var stdoutRedacted bool
		record.Stdout, stdoutRedacted = redactAcceptance(record.Stdout, scope.Username, secret)
		var stderrRedacted bool
		record.Stderr, stderrRedacted = redactAcceptance(record.Stderr, scope.Username, secret)
		record.RedactionApplied = record.RedactionApplied || stdoutRedacted || stderrRedacted
		record.StdoutSHA256, record.StderrSHA256 = acceptanceHash(record.Stdout), acceptanceHash(record.Stderr)
		audit.Evidence = append(audit.Evidence, record)
		audit.MissingCommandIDs = audit.MissingCommandIDs[1:]
		if record.Status != "completed" || record.RedactionApplied {
			audit.Status = "partial"
		}
		if record.Status == "timed_out" || record.Status == "cancelled" || record.Status == "exit_missing" || record.Status == "exit_signal" ||
			record.Status == "session_failed" || record.StdoutTruncated || record.StderrTruncated {
			if i+1 < len(plan.Commands) {
				audit.Status = "partial"
			}
			break
		}
	}
	finish()
	return audit, nil
}

func runAcceptanceCommand(ctx context.Context, client *ssh.Client, command AcceptanceCommand, batch *acceptanceCaptureBudget, timeout time.Duration) AcceptanceEvidence {
	record := AcceptanceEvidence{ID: command.ID, Argv: command.Argv, StartedAt: time.Now().UTC(), Status: "session_failed"}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-commandCtx.Done():
			_ = client.Close()
		case <-watchDone:
		}
	}()
	session, err := client.NewSession()
	if err != nil {
		if commandCtx.Err() != nil {
			record.Status = "timed_out"
			if errors.Is(ctx.Err(), context.Canceled) {
				record.Status = "cancelled"
			}
		}
		record.FinishedAt = time.Now().UTC()
		return record
	}
	defer session.Close()
	capture := &acceptanceCapture{budget: batch, remaining: acceptanceCommandBytes}
	session.Stdout = acceptanceStream{capture: capture, stderr: false}
	session.Stderr = acceptanceStream{capture: capture, stderr: true}
	// Include channel creation/start in the packet deadline; command waiting has
	// its own watchdog. Closing SSH is not proof of remote process termination.
	done := make(chan error, 1)
	go func() {
		if err := session.Start(acceptanceExec(command.Argv)); err != nil {
			done <- err
			return
		}
		record.ExecutionStarted = true
		done <- session.Wait()
	}()
	select {
	case err = <-done:
		switch {
		case err == nil:
			code := 0
			record.ExitCode, record.Status = &code, "completed"
		default:
			var exit *ssh.ExitError
			if errors.As(err, &exit) && exit.Signal() != "" {
				// x/crypto synthesizes 128+signal when no exit-status is sent.
				// Never mistake that synthetic value for a reported exit code.
				record.Status = "exit_signal"
			} else if errors.As(err, &exit) && exit.ExitStatus() >= 0 {
				code := exit.ExitStatus()
				record.ExitCode, record.Status = &code, "command_failed"
			} else {
				record.Status = "exit_missing"
			}
		}
	case <-commandCtx.Done():
		_ = client.Close()
		_ = session.Close()
		<-done
		record.Status = "timed_out"
		if errors.Is(ctx.Err(), context.Canceled) {
			record.Status = "cancelled"
		}
	}
	if commandCtx.Err() != nil {
		record.ExitCode, record.Status = nil, "timed_out"
		if errors.Is(ctx.Err(), context.Canceled) {
			record.Status = "cancelled"
		}
	}
	record.Stdout, record.Stderr, record.StdoutTruncated, record.StderrTruncated = capture.snapshot()
	// Withhold truncated streams: an echoed credential may have been cut in the
	// middle and cannot be reliably redacted by matching its complete value.
	if record.StdoutTruncated {
		record.Stdout = nil
		record.RedactionApplied = true
	}
	if record.StderrTruncated {
		record.Stderr = nil
		record.RedactionApplied = true
	}
	record.FinishedAt = time.Now().UTC()
	return record
}

type acceptanceCaptureBudget struct {
	mu        sync.Mutex
	remaining int
}

type acceptanceCapture struct {
	budget         *acceptanceCaptureBudget
	remaining      int
	stdout, stderr []byte
	outCut, errCut bool
}

type acceptanceStream struct {
	capture *acceptanceCapture
	stderr  bool
}

func (w acceptanceStream) Write(p []byte) (int, error) {
	c := w.capture
	c.budget.mu.Lock()
	defer c.budget.mu.Unlock()
	take := min(len(p), max(0, min(c.remaining, c.budget.remaining)))
	if w.stderr {
		c.stderr = append(c.stderr, p[:take]...)
		c.errCut = c.errCut || take < len(p)
	} else {
		c.stdout = append(c.stdout, p[:take]...)
		c.outCut = c.outCut || take < len(p)
	}
	c.remaining -= take
	c.budget.remaining -= take
	return len(p), nil
}

func (c *acceptanceCapture) snapshot() ([]byte, []byte, bool, bool) {
	c.budget.mu.Lock()
	defer c.budget.mu.Unlock()
	return bytes.Clone(c.stdout), bytes.Clone(c.stderr), c.outCut, c.errCut
}

func redactAcceptance(raw []byte, username string, secret []byte) ([]byte, bool) {
	if (len(secret) > 0 && bytes.Contains(raw, secret)) ||
		(username != "" && bytes.Contains(raw, []byte(username))) {
		return []byte("[WITHHELD SENSITIVE STREAM]"), true
	}
	if acceptanceSensitivePattern.Match(raw) {
		return []byte("[WITHHELD SENSITIVE STREAM]"), true
	}
	for _, pattern := range sensitiveEvidencePatterns {
		if pattern.Match(raw) {
			return []byte("[WITHHELD SENSITIVE STREAM]"), true
		}
	}
	return raw, false
}

var acceptanceSensitivePattern = regexp.MustCompile(`(?i)["']?(password|passwd|token|secret|authorization|api[_-]?key)["']?\s*[:=]\s*\S+`)

func acceptanceHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

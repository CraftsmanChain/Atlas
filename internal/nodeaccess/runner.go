package nodeaccess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHReadOnlyExecutor opens a verified SSH connection and executes only
// server-registered commands supplied by the node evidence collector.
type SSHReadOnlyExecutor struct {
	port            int
	connectTimeout  time.Duration
	hostKeyCallback ssh.HostKeyCallback
}

func NewSSHReadOnlyExecutor(port int, connectTimeout time.Duration, knownHostsFile string) (*SSHReadOnlyExecutor, error) {
	authenticator, err := NewSSHAuthenticator(port, connectTimeout, knownHostsFile)
	if err != nil {
		return nil, err
	}
	return &SSHReadOnlyExecutor{
		port: authenticator.port, connectTimeout: authenticator.connectTimeout,
		hostKeyCallback: authenticator.hostKeyCallback,
	}, nil
}

func (e *SSHReadOnlyExecutor) Execute(
	ctx context.Context,
	node, username, authType string,
	secret []byte,
	commands []ReadOnlyCommand,
	commandTimeout time.Duration,
) ([]CommandOutcome, error) {
	client, err := e.connect(ctx, node, username, authType, secret)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	outcomes := make([]CommandOutcome, 0, len(commands))
	for _, command := range commands {
		outcomes = append(outcomes, e.run(ctx, client, command, commandTimeout))
	}
	return outcomes, nil
}

func (e *SSHReadOnlyExecutor) connect(ctx context.Context, node, username, authType string, secret []byte) (*ssh.Client, error) {
	if net.ParseIP(strings.TrimSpace(node)) == nil {
		return nil, fmt.Errorf("%w: invalid managed node address", ErrNetworkUnavailable)
	}
	if authType != "password" {
		return nil, fmt.Errorf("%w: unsupported authentication type", ErrAuthenticationRejected)
	}
	address := net.JoinHostPort(node, strconv.Itoa(e.port))
	connection, err := (&net.Dialer{Timeout: e.connectTimeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("%w: SSH transport unavailable", ErrNetworkUnavailable)
	}
	deadline := time.Now().Add(e.connectTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	config := &ssh.ClientConfig{
		User: username, Auth: []ssh.AuthMethod{ssh.Password(string(secret))},
		HostKeyCallback: e.hostKeyCallback, Timeout: e.connectTimeout,
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		_ = connection.Close()
		var hostKeyError *knownhosts.KeyError
		if errors.As(err, &hostKeyError) || strings.Contains(strings.ToLower(err.Error()), "knownhosts:") {
			return nil, fmt.Errorf("%w: SSH host identity verification failed", ErrHostIdentityFailed)
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "unable to authenticate") || strings.Contains(lower, "no supported methods remain") {
			return nil, fmt.Errorf("%w: SSH authentication rejected", ErrAuthenticationRejected)
		}
		return nil, fmt.Errorf("%w: SSH handshake unavailable", ErrNetworkUnavailable)
	}
	_ = connection.SetDeadline(time.Time{})
	return ssh.NewClient(clientConnection, channels, requests), nil
}

func (e *SSHReadOnlyExecutor) run(ctx context.Context, client *ssh.Client, command ReadOnlyCommand, timeout time.Duration) CommandOutcome {
	outcome := CommandOutcome{CommandID: command.ID, Kind: command.Kind, Status: "failed"}
	session, err := client.NewSession()
	if err != nil {
		outcome.Status = "session_failed"
		return outcome
	}
	defer session.Close()
	writer := &boundedWriter{limit: command.MaxOutputBytes}
	session.Stdout, session.Stderr = writer, writer
	done := make(chan error, 1)
	go func() { done <- session.Run(command.Command) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err == nil {
			outcome.Status = "completed"
		} else {
			outcome.Status = "command_failed"
		}
	case <-timer.C:
		outcome.Status = "timed_out"
		_ = session.Close()
		<-done
	case <-ctx.Done():
		outcome.Status = "cancelled"
		_ = session.Close()
		<-done
	}
	outcome.Output = writer.String()
	outcome.Truncated = writer.Truncated()
	return outcome
}

type boundedWriter struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.limit <= 0 {
		w.truncated = w.truncated || len(p) > 0
		return len(p), nil
	}
	remaining := w.limit - len(w.data)
	if remaining > 0 {
		take := len(p)
		if take > remaining {
			take = remaining
		}
		w.data = append(w.data, p[:take]...)
	}
	if len(p) > remaining {
		w.truncated = true
	}
	return len(p), nil
}

func (w *boundedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(append([]byte(nil), w.data...))
}

func (w *boundedWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

var _ io.Writer = (*boundedWriter)(nil)

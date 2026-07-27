package nodeaccess

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHAuthenticator performs only a verified SSH transport handshake and user
// authentication. It never opens a session or executes a command.
type SSHAuthenticator struct {
	port            int
	connectTimeout  time.Duration
	hostKeyCallback ssh.HostKeyCallback
}

func NewSSHAuthenticator(port int, connectTimeout time.Duration, knownHostsFile string) (*SSHAuthenticator, error) {
	if port <= 0 || port > 65535 {
		return nil, errors.New("invalid SSH port")
	}
	if connectTimeout <= 0 {
		return nil, errors.New("invalid SSH connect timeout")
	}
	knownHostsFile = strings.TrimSpace(knownHostsFile)
	if knownHostsFile == "" {
		return nil, errors.New("known_hosts file is not configured")
	}
	callback, err := knownhosts.New(knownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts: %w", err)
	}
	return &SSHAuthenticator{port: port, connectTimeout: connectTimeout, hostKeyCallback: callback}, nil
}

func (a *SSHAuthenticator) Authenticate(ctx context.Context, node, username, authType string, secret []byte) error {
	if net.ParseIP(strings.TrimSpace(node)) == nil {
		return fmt.Errorf("%w: invalid managed node address", ErrNetworkUnavailable)
	}
	if authType != "password" {
		return fmt.Errorf("%w: unsupported authentication type", ErrAuthenticationRejected)
	}
	address := net.JoinHostPort(node, strconv.Itoa(a.port))
	dialer := net.Dialer{Timeout: a.connectTimeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("%w: SSH transport unavailable", ErrNetworkUnavailable)
	}
	defer connection.Close()
	deadline := time.Now().Add(a.connectTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	config := &ssh.ClientConfig{
		User:            username,
		Auth:            []ssh.AuthMethod{ssh.Password(string(secret))},
		HostKeyCallback: a.hostKeyCallback,
		Timeout:         a.connectTimeout,
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		var hostKeyError *knownhosts.KeyError
		if errors.As(err, &hostKeyError) || strings.Contains(strings.ToLower(err.Error()), "knownhosts:") {
			return fmt.Errorf("%w: SSH host identity verification failed", ErrHostIdentityFailed)
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "unable to authenticate") || strings.Contains(lower, "no supported methods remain") {
			return fmt.Errorf("%w: SSH authentication rejected", ErrAuthenticationRejected)
		}
		return fmt.Errorf("%w: SSH handshake unavailable", ErrNetworkUnavailable)
	}
	client := ssh.NewClient(clientConnection, channels, requests)
	return client.Close()
}

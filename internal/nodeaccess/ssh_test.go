package nodeaccess

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestSSHAuthenticatorVerifiesKnownHostAndOnlyAuthenticates(t *testing.T) {
	address, signer, stop := startSSHTestServer(t, "correct-password")
	defer stop()
	host, portText, _ := net.SplitHostPort(address)
	knownHostsFile := t.TempDir() + "/known_hosts"
	if err := os.WriteFile(knownHostsFile, []byte(knownhosts.Line([]string{address}, signer.PublicKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	authenticator, err := NewSSHAuthenticator(port, 2*time.Second, knownHostsFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := authenticator.Authenticate(context.Background(), host, "atlas", "password", []byte("correct-password")); err != nil {
		t.Fatalf("known-host authentication failed: %v", err)
	}
	if err := authenticator.Authenticate(context.Background(), host, "atlas", "password", []byte("wrong-password")); !errors.Is(err, ErrAuthenticationRejected) {
		t.Fatalf("expected authentication rejection, got %v", err)
	}
}

func TestSSHAuthenticatorRejectsUnknownHostIdentity(t *testing.T) {
	address, _, stop := startSSHTestServer(t, "correct-password")
	defer stop()
	host, portText, _ := net.SplitHostPort(address)
	knownHostsFile := t.TempDir() + "/known_hosts"
	if err := os.WriteFile(knownHostsFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	authenticator, err := NewSSHAuthenticator(port, 2*time.Second, knownHostsFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := authenticator.Authenticate(context.Background(), host, "atlas", "password", []byte("correct-password")); !errors.Is(err, ErrHostIdentityFailed) {
		t.Fatalf("expected host identity failure, got %v", err)
	}
}

func startSSHTestServer(t *testing.T, password string) (string, ssh.Signer, func()) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(metadata ssh.ConnMetadata, provided []byte) (*ssh.Permissions, error) {
			if string(provided) != password {
				return nil, errors.New("password rejected")
			}
			return nil, nil
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				serverConnection, channels, requests, handshakeErr := ssh.NewServerConn(connection, config)
				if handshakeErr != nil {
					_ = connection.Close()
					return
				}
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					_ = channel.Reject(ssh.Prohibited, "sessions are not supported")
				}
				_ = serverConnection.Close()
			}()
		}
	}()
	stop := func() {
		_ = listener.Close()
		<-stopped
	}
	return listener.Addr().String(), signer, stop
}

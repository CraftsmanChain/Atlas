package nodeaccess

import (
	"context"
	"errors"
	"testing"

	"atlas/pkg/config"
)

type mapResolver map[string]string

func (r mapResolver) Available(ref string) bool { return r[ref] != "" }
func (r mapResolver) Resolve(ref string) ([]byte, error) {
	if r[ref] == "" {
		return nil, errors.New("missing")
	}
	return []byte(r[ref]), nil
}

type authFunc func(ctx context.Context, node, username, authType string, secret []byte) error

func (f authFunc) Authenticate(ctx context.Context, node, username, authType string, secret []byte) error {
	return f(ctx, node, username, authType, secret)
}

func TestAuthenticateTriesCredentialProfilesInPriorityOrder(t *testing.T) {
	service := NewService(config.NodeAccessConfig{CredentialProfiles: []config.NodeCredentialProfile{
		{ID: "b", Priority: 20, Username: "user-b", AuthType: "password", SecretRef: "env:B", Enabled: true},
		{ID: "a", Priority: 10, Username: "user-a", AuthType: "password", SecretRef: "env:A", Enabled: true},
	}}, mapResolver{"env:A": "wrong", "env:B": "right"})
	var attempts []string
	result := service.Authenticate(context.Background(), "10.114.4.1", authFunc(func(_ context.Context, _, username, _ string, secret []byte) error {
		attempts = append(attempts, username+":"+string(secret))
		if username == "user-a" {
			return ErrAuthenticationRejected
		}
		return nil
	}))
	if result.Status != "authenticated" || result.CredentialProfileID != "b" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(attempts) != 2 || attempts[0] != "user-a:wrong" || attempts[1] != "user-b:right" {
		t.Fatalf("unexpected authentication order: %#v", attempts)
	}
	if !result.NoCredentialDisclosed {
		t.Fatal("result must state that credentials were not disclosed")
	}
}

func TestAuthenticateMarksCredentialExhausted(t *testing.T) {
	service := NewService(config.NodeAccessConfig{CredentialProfiles: []config.NodeCredentialProfile{
		{ID: "a", Priority: 10, Username: "user-a", AuthType: "password", SecretRef: "env:A", Enabled: true},
		{ID: "b", Priority: 20, Username: "user-b", AuthType: "password", SecretRef: "env:B", Enabled: true},
	}}, mapResolver{"env:A": "wrong", "env:B": "wrong-too"})
	result := service.Authenticate(context.Background(), "10.114.4.2", authFunc(func(context.Context, string, string, string, []byte) error {
		return ErrAuthenticationRejected
	}))
	if result.Status != "credential_exhausted" || !result.AlertRequired || len(result.Attempts) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestAuthenticateStopsOnHostIdentityFailure(t *testing.T) {
	service := NewService(config.NodeAccessConfig{CredentialProfiles: []config.NodeCredentialProfile{
		{ID: "a", Priority: 10, Username: "user-a", AuthType: "password", SecretRef: "env:A", Enabled: true},
		{ID: "b", Priority: 20, Username: "user-b", AuthType: "password", SecretRef: "env:B", Enabled: true},
	}}, mapResolver{"env:A": "secret-a", "env:B": "secret-b"})
	result := service.Authenticate(context.Background(), "10.114.4.3", authFunc(func(context.Context, string, string, string, []byte) error {
		return ErrHostIdentityFailed
	}))
	if result.Status != "host_identity_failed" || len(result.Attempts) != 1 {
		t.Fatalf("host identity failure must stop credential rotation: %#v", result)
	}
}

func TestOverviewDoesNotExposeSecretReferences(t *testing.T) {
	service := NewService(config.NodeAccessConfig{
		Enabled: true, SkillID: SkillID, SkillVersion: SkillVersion,
		ConnectTimeout: "5s", CommandTimeout: "10s", MaxOutputBytes: 1024,
		MaxConcurrentNodes: 2, MaxCommandsPerNode: 6,
		CredentialProfiles: []config.NodeCredentialProfile{
			{ID: "a", Priority: 10, Username: "atlas", AuthType: "password", SecretRef: "env:VERY_SECRET_NAME", Enabled: true},
		},
	}, mapResolver{"env:VERY_SECRET_NAME": "value"})
	overview := service.Overview()
	if overview.Status != "ready_no_transport" || overview.ExecutionEnabled {
		t.Fatalf("unexpected overview: %#v", overview)
	}
	if len(overview.CredentialProfiles) != 1 || overview.CredentialProfiles[0].SecretProvider != "env" {
		t.Fatalf("unexpected profile status: %#v", overview.CredentialProfiles)
	}
	if len(overview.Commands) == 0 {
		t.Fatal("expected registered command catalog")
	}
	if len(overview.Skills) != 3 ||
		overview.Skills[0].ID != "atlas-node-evidence" ||
		overview.Skills[1].ID != "atlas-fault-analysis" ||
		overview.Skills[2].ID != "atlas-case-learning" {
		t.Fatalf("unexpected foundational skill catalog: %#v", overview.Skills)
	}
}

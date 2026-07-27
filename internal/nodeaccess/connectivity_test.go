package nodeaccess

import (
	"context"
	"errors"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func connectivityTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&api.GPUNode{NodeIP: "10.114.4.25", Lifecycle: "active", State: "up"}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestConnectivityCheckRejectsUnmanagedNodeBeforeAuthentication(t *testing.T) {
	db := connectivityTestDB(t)
	called := false
	access := NewService(config.NodeAccessConfig{}, mapResolver{})
	checker := NewConnectivityService(db, access, authFunc(func(context.Context, string, string, string, []byte) error {
		called = true
		return nil
	}), true)
	if _, err := checker.Check(context.Background(), "10.114.4.99"); !errors.Is(err, ErrNodeNotManaged) {
		t.Fatalf("expected unmanaged node rejection, got %v", err)
	}
	if called {
		t.Fatal("authentication must not run for unmanaged nodes")
	}
}

func TestConnectivityCheckPersistsRedactedSuccess(t *testing.T) {
	db := connectivityTestDB(t)
	access := NewService(config.NodeAccessConfig{CredentialProfiles: []config.NodeCredentialProfile{
		{ID: "node-a", Priority: 10, Username: "atlas", AuthType: "password", SecretRef: "env:A", Enabled: true},
	}}, mapResolver{"env:A": "secret"})
	checker := NewConnectivityService(db, access, authFunc(func(_ context.Context, node, username, _ string, secret []byte) error {
		if node != "10.114.4.25" || username != "atlas" || string(secret) != "secret" {
			t.Fatal("unexpected authentication input")
		}
		return nil
	}), true)
	now := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	checker.now = func() time.Time { now = now.Add(time.Second); return now }
	record, err := checker.Check(context.Background(), "10.114.4.25")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "authenticated" || record.CredentialProfileID != "node-a" ||
		!record.NoCredentialDisclosed || !record.NoCommandExecuted {
		t.Fatalf("unexpected connectivity record: %#v", record)
	}
	var stored api.NodeAccessCheck
	if err := db.First(&stored, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "authenticated" || len(stored.Attempts) != 1 || stored.Attempts[0] != "node-a:success" {
		t.Fatalf("unexpected stored record: %#v", stored)
	}
}

func TestCredentialExhaustionCreatesAndSuccessClearsAccessIssue(t *testing.T) {
	db := connectivityTestDB(t)
	access := NewService(config.NodeAccessConfig{CredentialProfiles: []config.NodeCredentialProfile{
		{ID: "node-a", Priority: 10, Username: "atlas", AuthType: "password", SecretRef: "env:A", Enabled: true},
	}}, mapResolver{"env:A": "secret"})
	authError := ErrAuthenticationRejected
	checker := NewConnectivityService(db, access, authFunc(func(context.Context, string, string, string, []byte) error {
		return authError
	}), true)
	if record, err := checker.Check(context.Background(), "10.114.4.25"); err != nil || record.Status != "credential_exhausted" {
		t.Fatalf("unexpected exhausted result record=%#v err=%v", record, err)
	}
	var issue api.PlatformIssue
	if err := db.Where("issue_key = ?", "node_access_auth:10.114.4.25").First(&issue).Error; err != nil {
		t.Fatal(err)
	}
	if issue.Status != "open" || issue.DetectionState != "active" || issue.Category != "access" {
		t.Fatalf("unexpected active issue: %#v", issue)
	}
	authError = nil
	if record, err := checker.Check(context.Background(), "10.114.4.25"); err != nil || record.Status != "authenticated" {
		t.Fatalf("unexpected recovered result record=%#v err=%v", record, err)
	}
	if err := db.First(&issue, issue.ID).Error; err != nil {
		t.Fatal(err)
	}
	if issue.Status != "resolved" || issue.DetectionState != "cleared" || issue.ResolvedAt == nil {
		t.Fatalf("access issue was not cleared: %#v", issue)
	}
}

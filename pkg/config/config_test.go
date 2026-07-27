package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRejectsInlineNodePassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atlas.yaml")
	if err := os.WriteFile(path, []byte(`
node_access:
  credential_profiles:
    - id: a
      password: plaintext
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "inline secret material") {
		t.Fatalf("expected inline secret rejection, got %v", err)
	}
}

func TestLoadConfigAcceptsOrderedEnvironmentReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atlas.yaml")
	if err := os.WriteFile(path, []byte(`
node_access:
  enabled: true
  credential_profiles:
    - id: a
      priority: 10
      username: atlas-a
      auth_type: password
      secret_ref: env:ATLAS_NODE_PASSWORD_A
      enabled: true
    - id: b
      priority: 20
      username: atlas-b
      auth_type: password
      secret_ref: env:ATLAS_NODE_PASSWORD_B
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeAccess.SkillID != "atlas-node-evidence" || len(cfg.NodeAccess.CredentialProfiles) != 2 {
		t.Fatalf("unexpected node access config: %#v", cfg.NodeAccess)
	}
}

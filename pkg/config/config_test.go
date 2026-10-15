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

func TestXiaozhenDeploymentConfigUsesReadOnlyVMMonitoring(t *testing.T) {
	cfg, err := LoadConfig("../../configs/clusters/xiaozhen-zhiyuan.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Branding.InstanceName != "算力小镇-智元" || cfg.Gateway.Port != ":7077" {
		t.Fatal("new cluster branding or port is incorrect")
	}
	if cfg.Storage.Driver != "postgres" || cfg.Storage.DSN != "" {
		t.Fatal("new cluster must inject its own PostgreSQL credentials")
	}
	if cfg.Prometheus.BaseURL != "http://10.80.192.236:28481/select/0/prometheus" {
		t.Fatal("VM base URL must not include /api/v1/query")
	}
	if !cfg.Inventory.Enabled || cfg.Inventory.AssetFile != "" || !cfg.Health.Enabled || !cfg.History.Enabled {
		t.Fatal("new cluster must use VM-backed inventory, health, and history without a local asset file")
	}
	if cfg.Inventory.ExpectedGPUCount != 8 || cfg.Inventory.NodePrefix != "10.80." || !cfg.Inventory.BMCFromHostLabel || len(cfg.Inventory.TargetJobs) != 4 {
		t.Fatalf("unexpected VM inventory defaults: %#v", cfg.Inventory)
	}
	if cfg.NodeAccess.Enabled || len(cfg.Feishu.Bots) != 0 || len(cfg.NodeAccess.CredentialProfiles) != 0 {
		t.Fatal("new cluster must not inherit active SSH credentials or notification loops")
	}
	if len(cfg.History.Sources) != 1 || !cfg.History.Sources[0].Enabled || cfg.History.Sources[0].Type != "victoriametrics-cluster" || cfg.History.Sources[0].TenantID != "0" || cfg.History.Sources[0].BaseURL != "http://10.80.192.236:28481" {
		t.Fatal("VM history source must retain its tenant without duplicating the select prefix")
	}
	legacy, err := LoadConfig("../../configs/config.yaml")
	if err != nil || legacy.Prometheus.BaseURL != "http://10.111.201.1:9090" {
		t.Fatal("legacy cluster configuration must be preserved")
	}
}

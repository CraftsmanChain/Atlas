package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Gateway    GatewayConfig    `yaml:"gateway"`
	Storage    StorageConfig    `yaml:"storage"`
	Feishu     FeishuConfig     `yaml:"feishu"`
	Logging    LoggingConfig    `yaml:"logging"`
	Web        WebConfig        `yaml:"web"`
	Branding   BrandingConfig   `yaml:"branding"`
	Prometheus PrometheusConfig `yaml:"prometheus"`
	Inventory  InventoryConfig  `yaml:"inventory"`
	Health     HealthConfig     `yaml:"health"`
	NodeAccess NodeAccessConfig `yaml:"node_access"`
}

type GatewayConfig struct {
	Port                string `yaml:"port"`
	WebhookToken        string `yaml:"webhook_token"`
	FeishuWebhookToken  string `yaml:"feishu_webhook_token"`
	IngestionSourceMode string `yaml:"ingestion_source_mode"`
	IngestionStaleAfter string `yaml:"ingestion_stale_after"`
}

type StorageConfig struct {
	Driver           string `yaml:"driver"`
	DSN              string `yaml:"dsn"`
	IngestionReadDSN string `yaml:"ingestion_read_dsn"`
}

type FeishuConfig struct {
	Bots []FeishuBotConfig `yaml:"bots"`
}

type LoggingConfig struct {
	Dir string `yaml:"dir"`
}

type WebConfig struct {
	StaticDir string `yaml:"static_dir"`
}

// BrandingConfig provides the initial public display settings. Once Atlas has
// persisted settings in its database, operators can update them from the
// Platform Overview without rebuilding the frontend.
type BrandingConfig struct {
	InstanceName   string `yaml:"instance_name"`
	ProductName    string `yaml:"product_name"`
	ProductTagline string `yaml:"product_tagline"`
	Environment    string `yaml:"environment"`
}

type PrometheusConfig struct {
	BaseURL        string `yaml:"base_url"`
	RequestTimeout string `yaml:"request_timeout"`
}

// InventoryConfig controls the read-only discovery loop. It never changes a
// node or exporter; it only reconciles what Prometheus currently exposes.
type InventoryConfig struct {
	Enabled              bool     `yaml:"enabled"`
	AssetFile            string   `yaml:"asset_file"`
	SyncInterval         string   `yaml:"sync_interval"` // legacy fallback for target status
	TargetSyncInterval   string   `yaml:"target_sync_interval"`
	IdentitySyncInterval string   `yaml:"identity_sync_interval"`
	FullSyncInterval     string   `yaml:"full_sync_interval"`
	HistoryWindow        string   `yaml:"history_window"`
	ExpectedGPUCount     int      `yaml:"expected_gpu_count"`
	NodePrefix           string   `yaml:"node_prefix"`
	BMCPrefix            string   `yaml:"bmc_prefix"`
	BMCLastOctetMin      int      `yaml:"bmc_last_octet_min"`
	TargetJobs           []string `yaml:"target_jobs"`
}

type HealthConfig struct {
	Enabled       bool   `yaml:"enabled"`
	ScoreInterval string `yaml:"score_interval"`
	RuleVersion   string `yaml:"rule_version"`
}

type NodeAccessConfig struct {
	Enabled            bool                    `yaml:"enabled"`
	SkillID            string                  `yaml:"skill_id"`
	SkillVersion       string                  `yaml:"skill_version"`
	SSHPort            int                     `yaml:"ssh_port"`
	KnownHostsFile     string                  `yaml:"known_hosts_file"`
	ConnectTimeout     string                  `yaml:"connect_timeout"`
	CommandTimeout     string                  `yaml:"command_timeout"`
	MaxOutputBytes     int                     `yaml:"max_output_bytes"`
	MaxConcurrentNodes int                     `yaml:"max_concurrent_nodes"`
	MaxCommandsPerNode int                     `yaml:"max_commands_per_node"`
	CredentialProfiles []NodeCredentialProfile `yaml:"credential_profiles"`
}

type NodeCredentialProfile struct {
	ID         string `yaml:"id"`
	Priority   int    `yaml:"priority"`
	Username   string `yaml:"username"`
	AuthType   string `yaml:"auth_type"`
	SecretRef  string `yaml:"secret_ref"`
	Enabled    bool   `yaml:"enabled"`
	Password   string `yaml:"password"`
	PrivateKey string `yaml:"private_key"`
}

type FeishuBotConfig struct {
	Enabled         bool   `yaml:"enabled"`
	WebhookURL      string `yaml:"webhook_url"`
	EnableSignature bool   `yaml:"enable_signature"`
	Secret          string `yaml:"secret"`
}

// LoadConfig 从指定路径加载 YAML 配置文件
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// 设置默认值
	if cfg.Gateway.Port == "" {
		cfg.Gateway.Port = ":8080"
	}
	if cfg.Storage.Driver == "" {
		cfg.Storage.Driver = "sqlite"
	}
	if cfg.Storage.DSN == "" && cfg.Storage.Driver == "sqlite" {
		cfg.Storage.DSN = "atlas.db"
	}
	if cfg.Gateway.IngestionSourceMode == "" {
		cfg.Gateway.IngestionSourceMode = "local-live"
	}
	if cfg.Gateway.IngestionStaleAfter == "" {
		cfg.Gateway.IngestionStaleAfter = "15m"
	}
	if cfg.Logging.Dir == "" {
		cfg.Logging.Dir = "logs"
	}
	if cfg.Web.StaticDir == "" {
		cfg.Web.StaticDir = "web/dist"
	}
	if cfg.Branding.InstanceName == "" {
		cfg.Branding.InstanceName = "Atlas Cluster"
	}
	if cfg.Branding.ProductName == "" {
		cfg.Branding.ProductName = "ATLAS"
	}
	if cfg.Branding.ProductTagline == "" {
		cfg.Branding.ProductTagline = "GPU RELIABILITY"
	}
	if cfg.Branding.Environment == "" {
		cfg.Branding.Environment = "LOCAL"
	}
	if cfg.Prometheus.RequestTimeout == "" {
		cfg.Prometheus.RequestTimeout = "15s"
	}
	if cfg.Inventory.TargetSyncInterval == "" {
		if cfg.Inventory.SyncInterval != "" {
			cfg.Inventory.TargetSyncInterval = cfg.Inventory.SyncInterval
		} else {
			cfg.Inventory.TargetSyncInterval = "10m"
		}
	}
	if cfg.Inventory.IdentitySyncInterval == "" {
		cfg.Inventory.IdentitySyncInterval = "10m"
	}
	if cfg.Inventory.FullSyncInterval == "" {
		cfg.Inventory.FullSyncInterval = "24h"
	}
	if cfg.Inventory.HistoryWindow == "" {
		cfg.Inventory.HistoryWindow = "365d"
	}
	if cfg.Inventory.ExpectedGPUCount <= 0 {
		cfg.Inventory.ExpectedGPUCount = 8
	}
	if cfg.Inventory.NodePrefix == "" {
		cfg.Inventory.NodePrefix = "10.114.4."
	}
	if cfg.Inventory.BMCPrefix == "" {
		cfg.Inventory.BMCPrefix = "10.114.1."
	}
	if cfg.Inventory.BMCLastOctetMin <= 0 {
		cfg.Inventory.BMCLastOctetMin = 20
	}
	if len(cfg.Inventory.TargetJobs) == 0 {
		cfg.Inventory.TargetJobs = []string{"dcgm_exporter", "gpu_exporter", "node_exporter", "ipmi_exporter"}
	}
	if cfg.Health.ScoreInterval == "" {
		cfg.Health.ScoreInterval = "10m"
	}
	if cfg.Health.RuleVersion == "" {
		cfg.Health.RuleVersion = "gpu-health-v1.4.1"
	}
	if cfg.NodeAccess.SkillID == "" {
		cfg.NodeAccess.SkillID = "atlas-node-evidence"
	}
	if cfg.NodeAccess.SkillVersion == "" {
		cfg.NodeAccess.SkillVersion = "v0.4.1"
	}
	if cfg.NodeAccess.ConnectTimeout == "" {
		cfg.NodeAccess.ConnectTimeout = "5s"
	}
	if cfg.NodeAccess.SSHPort <= 0 {
		cfg.NodeAccess.SSHPort = 22
	}
	if cfg.NodeAccess.CommandTimeout == "" {
		cfg.NodeAccess.CommandTimeout = "10s"
	}
	if cfg.NodeAccess.MaxOutputBytes <= 0 {
		cfg.NodeAccess.MaxOutputBytes = 1024 * 1024
	}
	if cfg.NodeAccess.MaxConcurrentNodes <= 0 {
		cfg.NodeAccess.MaxConcurrentNodes = 2
	}
	if cfg.NodeAccess.MaxCommandsPerNode <= 0 {
		cfg.NodeAccess.MaxCommandsPerNode = 6
	}
	profileIDs := make(map[string]struct{}, len(cfg.NodeAccess.CredentialProfiles))
	for _, profile := range cfg.NodeAccess.CredentialProfiles {
		if profile.Password != "" || profile.PrivateKey != "" {
			return nil, fmt.Errorf("node_access credential %q contains inline secret material; use secret_ref", profile.ID)
		}
		if _, exists := profileIDs[profile.ID]; profile.ID != "" && exists {
			return nil, fmt.Errorf("duplicate node_access credential id %q", profile.ID)
		}
		profileIDs[profile.ID] = struct{}{}
		if profile.Enabled {
			if profile.ID == "" || profile.Username == "" || profile.AuthType == "" || profile.SecretRef == "" {
				return nil, fmt.Errorf("enabled node_access credential profiles require id, username, auth_type, and secret_ref")
			}
			if profile.AuthType != "password" {
				return nil, fmt.Errorf("node_access credential %q uses unsupported auth_type %q", profile.ID, profile.AuthType)
			}
			if !strings.HasPrefix(profile.SecretRef, "env:") || strings.TrimPrefix(profile.SecretRef, "env:") == "" {
				return nil, fmt.Errorf("node_access credential %q must use a non-empty env: secret_ref", profile.ID)
			}
		}
	}

	return &cfg, nil
}

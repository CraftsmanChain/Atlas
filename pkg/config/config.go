package config

import (
	"os"

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
}

type GatewayConfig struct {
	Port                string `yaml:"port"`
	WebhookToken        string `yaml:"webhook_token"`
	FeishuWebhookToken  string `yaml:"feishu_webhook_token"`
	IngestionSourceMode string `yaml:"ingestion_source_mode"`
	IngestionStaleAfter string `yaml:"ingestion_stale_after"`
}

type StorageConfig struct {
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
	if cfg.Storage.DSN == "" {
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

	return &cfg, nil
}

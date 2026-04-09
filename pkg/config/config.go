package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Gateway GatewayConfig `yaml:"gateway"`
	Storage StorageConfig `yaml:"storage"`
	Feishu  FeishuConfig  `yaml:"feishu"`
	Logging LoggingConfig `yaml:"logging"`
}

type GatewayConfig struct {
	Port string `yaml:"port"`
}

type StorageConfig struct {
	DSN string `yaml:"dsn"`
}

type FeishuConfig struct {
	Bots []FeishuBotConfig `yaml:"bots"`
}

type LoggingConfig struct {
	Dir string `yaml:"dir"`
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
	if cfg.Logging.Dir == "" {
		cfg.Logging.Dir = "logs"
	}

	return &cfg, nil
}

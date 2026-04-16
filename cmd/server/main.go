package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"atlas/internal/analyzer"
	ig "atlas/internal/gateway"
	"atlas/pkg/config"
	"atlas/pkg/logging"
	"atlas/pkg/notifier"
	"atlas/pkg/storage"
)

func main() {
	fmt.Println("Starting Atlas Server...")

	configPath := resolveConfigPath()

	// 1. 加载配置文件
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Printf("Failed to load config, using default settings. Error: %v", err)
		cfg = &config.Config{
			Gateway: config.GatewayConfig{Port: ":8080", WebhookToken: "", FeishuWebhookToken: ""},
			Storage: config.StorageConfig{DSN: "atlas.db"},
			Feishu:  config.FeishuConfig{Bots: []config.FeishuBotConfig{}},
			Logging: config.LoggingConfig{Dir: "logs"},
		}
	}
	applyRuntimeOverrides(cfg)

	logWriter, err := logging.InitGlobalLogger(cfg.Logging.Dir)
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logWriter.Close()
	log.Printf("Logger initialized. dir=%s", cfg.Logging.Dir)

	// 2. 初始化 SQLite 数据库
	db, err := storage.InitDB(cfg.Storage.DSN)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// 3. 初始化飞书通知模块
	feishuNotifier := notifier.NewFeishuNotifier(&cfg.Feishu)

	// 4. 初始化告警分析器
	alertAnalyzer := analyzer.NewAlertAnalyzer(db, feishuNotifier)

	// 5. 初始化网关 Handler
	handler := ig.NewHandler(
		db,
		alertAnalyzer,
		cfg.Gateway.WebhookToken,
		cfg.Gateway.FeishuWebhookToken,
	)

	// 6. 注册路由
	mux := http.NewServeMux()

	// 6.1 基础健康检查
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Atlas Server is healthy\n"))
	})

	// 6.2 API 路由 (原 API 服务的功能)
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "ok", "message": "Atlas API is running"}`))
	})

	// 6.3 Gateway 路由 (原 Gateway 服务的功能，用于接收外部推送)
	mux.HandleFunc("/api/v1/webhook/alert", handler.HandleAlertWebhook)
	mux.HandleFunc("/open-apis/bot/v2/hook/", handler.HandleFeishuBotWebhook)
	mux.HandleFunc("/api/v1/alerts/ingestions", handler.HandleRecentIngestions)
	mux.HandleFunc("/api/v1/alerts/ingestions/", handler.HandleIngestionSubresources)
	mux.HandleFunc("/api/v1/alerts/failures", handler.HandleFailedIngestions)
	mux.HandleFunc("/api/v1/push/metrics", handler.HandleMetricsPush)

	// 7. 启动服务
	port := cfg.Gateway.Port
	fmt.Printf("Atlas Server listening on port %s\n", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalf("Atlas Server failed to start: %v", err)
	}
}

func resolveConfigPath() string {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to Atlas config file")
	flag.Parse()

	if strings.TrimSpace(configPath) != "" {
		return strings.TrimSpace(configPath)
	}
	if envPath := strings.TrimSpace(os.Getenv("ATLAS_CONFIG")); envPath != "" {
		return envPath
	}
	return "configs/config.yaml"
}

func applyRuntimeOverrides(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if port := strings.TrimSpace(os.Getenv("ATLAS_PORT")); port != "" {
		cfg.Gateway.Port = normalizeListenPort(port)
	}
}

func normalizeListenPort(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if strings.HasPrefix(value, ":") {
		return value
	}
	return ":" + value
}

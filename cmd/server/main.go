package main

import (
	"fmt"
	"log"
	"net/http"

	"atlas/internal/analyzer"
	ig "atlas/internal/gateway"
	"atlas/pkg/config"
	"atlas/pkg/notifier"
	"atlas/pkg/storage"
)

func main() {
	fmt.Println("Starting Atlas Server...")

	// 1. 加载配置文件
	cfg, err := config.LoadConfig("configs/config.yaml")
	if err != nil {
		log.Printf("Failed to load config, using default settings. Error: %v", err)
		cfg = &config.Config{
			Gateway: config.GatewayConfig{Port: ":8080"},
			Storage: config.StorageConfig{DSN: "atlas.db"},
			Feishu:  config.FeishuConfig{Bots: []config.FeishuBotConfig{}},
		}
	}

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
	handler := ig.NewHandler(db, alertAnalyzer)

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
	mux.HandleFunc("/api/v1/push/metrics", handler.HandleMetricsPush)

	// 7. 启动服务
	port := cfg.Gateway.Port
	fmt.Printf("Atlas Server listening on port %s\n", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalf("Atlas Server failed to start: %v", err)
	}
}
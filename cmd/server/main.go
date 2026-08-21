package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"atlas/internal/analyzer"
	"atlas/internal/degradation"
	"atlas/internal/evidence"
	"atlas/internal/features"
	"atlas/internal/freshness"
	ig "atlas/internal/gateway"
	"atlas/internal/health"
	"atlas/internal/history"
	"atlas/internal/inventory"
	"atlas/internal/issues"
	"atlas/internal/nodeaccess"
	"atlas/internal/platformconfig"
	"atlas/internal/prediction"
	promclient "atlas/internal/prometheus"
	"atlas/pkg/config"
	"atlas/pkg/logging"
	"atlas/pkg/notifier"
	"atlas/pkg/storage"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = ""
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
			Web:     config.WebConfig{StaticDir: "web/dist"},
			NodeAccess: config.NodeAccessConfig{
				SkillID: "atlas-node-evidence", SkillVersion: "v0.6.3", SSHPort: 22,
				ConnectTimeout: "5s", CommandTimeout: "10s", MaxOutputBytes: 1024 * 1024,
				MaxConcurrentNodes: 2, MaxCommandsPerNode: 6,
			},
		}
	}
	applyRuntimeOverrides(cfg)

	logWriter, err := logging.InitGlobalLogger(cfg.Logging.Dir)
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logWriter.Close()
	log.Printf("Logger initialized. dir=%s", cfg.Logging.Dir)

	// 2. 初始化数据库
	db, err := storage.InitDBWithDriver(cfg.Storage.Driver, cfg.Storage.DSN)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	if err := features.SeedBuiltins(db); err != nil {
		log.Fatalf("Failed to seed feature catalog: %v", err)
	}
	if err := prediction.SeedBuiltins(db); err != nil {
		log.Fatalf("Failed to seed prediction model contracts: %v", err)
	}
	ingestionDB := db
	if readDSN := strings.TrimSpace(cfg.Storage.IngestionReadDSN); readDSN != "" {
		ingestionDB, err = storage.OpenReadOnlyDB(readDSN)
		if err != nil {
			log.Fatalf("Failed to initialize read-only ingestion database: %v", err)
		}
	}

	// 3. 初始化飞书通知模块
	feishuNotifier := notifier.NewFeishuNotifier(&cfg.Feishu)

	// 4. 初始化告警分析器
	alertAnalyzer := analyzer.NewAlertAnalyzer(db, feishuNotifier)

	// 5. 初始化网关 Handler
	ingestionStaleAfter := parseDurationOrDefault("ingestion stale", cfg.Gateway.IngestionStaleAfter, 15*time.Minute)
	handler := ig.NewHandler(
		db,
		ingestionDB,
		alertAnalyzer,
		cfg.Gateway.WebhookToken,
		cfg.Gateway.FeishuWebhookToken,
		cfg.Gateway.IngestionSourceMode,
		ingestionStaleAfter,
	)
	inventoryHandler := inventory.NewHandler(db)
	healthHandler := health.NewHandler(db)
	evidenceHandler := evidence.NewHandler(evidence.NewService(db))
	inventoryFreshAfter := 2 * parseDurationOrDefault("target status", cfg.Inventory.TargetSyncInterval, 10*time.Minute)
	healthFreshAfter := 2 * parseDurationOrDefault("GPU health score", cfg.Health.ScoreInterval, 30*time.Minute)
	freshnessHandler := freshness.NewHandler(db, ingestionDB, cfg.Gateway.IngestionSourceMode, ingestionStaleAfter, inventoryFreshAfter, healthFreshAfter)
	featureHandler := features.NewHandler(db)
	baselineHandler := features.NewBaselineHandler(db)
	degradationHandler := degradation.NewHandler(db)
	predictionService := prediction.NewServiceWithRetention(db, health.ParseHistoryRetention(cfg.Health.HistoryRetention))
	predictionHandler := prediction.NewHandlerWithService(predictionService)
	historyTimeout := parseDurationOrDefault("history source request", cfg.History.RequestTimeout, 60*time.Second)
	historyService := history.NewService(db, cfg.History, historyTimeout)
	historyHandler := history.NewHandler(historyService)
	issueService := issues.NewService(db)
	issueHandler := issues.NewHandlerWithService(db, issueService)
	platformConfigHandler := platformconfig.NewHandler(db, cfg.Branding)
	secretMasterKey := os.Getenv("ATLAS_PLATFORM_SECRET_MASTER_KEY")
	if strings.TrimSpace(secretMasterKey) == "" {
		secretMasterKey = os.Getenv("ATLAS_NODE_CREDENTIAL_MASTER_KEY")
	}
	assetSource, assetSourceErr := platformconfig.NewAssetSource(db, secretMasterKey)
	if assetSourceErr != nil {
		log.Printf("LXOP asset configuration encryption is unavailable: %v", assetSourceErr)
		assetSource = nil
	}
	var credentialVault *nodeaccess.CredentialVault
	credentialVault, err = nodeaccess.NewCredentialVault(db, os.Getenv("ATLAS_NODE_CREDENTIAL_MASTER_KEY"))
	if err != nil {
		log.Printf("Node credential encryption is unavailable: %v", err)
		credentialVault = nil
	}
	nodeAccessService := nodeaccess.NewServiceWithVault(cfg.NodeAccess, nil, credentialVault)
	allowCredentialInsecureHTTP := strings.EqualFold(strings.TrimSpace(os.Getenv("ATLAS_NODE_CREDENTIAL_ALLOW_INSECURE_HTTP")), "true")
	if allowCredentialInsecureHTTP {
		log.Printf("WARNING: asset configuration writes are allowed over plaintext HTTP")
	}
	assetConfigHandler := platformconfig.NewAssetConfigHandler(assetSource, os.Getenv("ATLAS_NODE_CREDENTIAL_ADMIN_TOKEN"), allowCredentialInsecureHTTP)
	nodeAccessHandler := nodeaccess.NewHandlerWithVault(nodeAccessService, credentialVault)
	connectTimeout := parseDurationOrDefault("node SSH connect", cfg.NodeAccess.ConnectTimeout, 5*time.Second)
	sshAuthenticator, sshErr := nodeaccess.NewSSHAuthenticator(cfg.NodeAccess.SSHPort, connectTimeout, cfg.NodeAccess.KnownHostsFile)
	if sshErr != nil {
		log.Printf("Known-host SSH connectivity checks are unavailable: %v", sshErr)
	}
	nodeAccessHandler.SetConnectivity(nodeaccess.NewConnectivityService(db, nodeAccessService, sshAuthenticator, cfg.NodeAccess.Enabled))
	sshReadOnlyExecutor, runnerErr := nodeaccess.NewSSHReadOnlyExecutor(cfg.NodeAccess.SSHPort, connectTimeout, cfg.NodeAccess.KnownHostsFile)
	if runnerErr != nil {
		log.Printf("Known-host SSH read-only collection is unavailable: %v", runnerErr)
	}
	nodeEvidenceCollections := nodeaccess.NewCollectionService(db, nodeAccessService, sshReadOnlyExecutor, cfg.NodeAccess.MaxConcurrentNodes)
	nodeAccessHandler.SetCollections(nodeEvidenceCollections)
	go nodeEvidenceCollections.Run(context.Background(), time.Minute)
	go issueService.Run(context.Background(), time.Minute)
	go predictionService.RunLabelSync(context.Background(), time.Minute)
	go predictionService.RunOutcomeSync(context.Background(), time.Minute)
	go predictionService.RunShadowModelRegistrySync(context.Background(), time.Minute)
	if cfg.History.Enabled {
		historyAuditInterval := parseDurationOrDefault("monitoring history audit", cfg.History.AuditInterval, 6*time.Hour)
		go historyService.Run(context.Background(), historyAuditInterval)
	}

	// Inventory discovery is read-only and best-effort. Prometheus outages are
	// recorded as failed sync runs and never prevent Atlas from serving APIs.
	if cfg.Inventory.Enabled && strings.TrimSpace(cfg.Prometheus.BaseURL) != "" {
		requestTimeout, parseErr := time.ParseDuration(cfg.Prometheus.RequestTimeout)
		if parseErr != nil {
			log.Printf("Invalid Prometheus request timeout %q; using 15s", cfg.Prometheus.RequestTimeout)
			requestTimeout = 15 * time.Second
		}
		prometheusClient, clientErr := promclient.NewClient(cfg.Prometheus.BaseURL, requestTimeout)
		if clientErr != nil {
			log.Printf("Inventory sync disabled: %v", clientErr)
		} else {
			monitoringInterval := parseDurationOrDefault("monitoring reconciliation", cfg.Inventory.TargetSyncInterval, 10*time.Minute)
			fullInterval := parseDurationOrDefault("full reconciliation", cfg.Inventory.FullSyncInterval, 24*time.Hour)
			inventoryService := inventory.NewService(db, prometheusClient, cfg.Inventory)
			if assetSource != nil {
				inventoryService = inventory.NewServiceWithAssets(db, prometheusClient, cfg.Inventory, assetSource)
			}
			go inventoryService.Run(context.Background(), monitoringInterval, fullInterval)
			if cfg.Health.Enabled {
				healthInterval := parseDurationOrDefault("GPU health score", cfg.Health.ScoreInterval, 10*time.Minute)
				healthService := health.NewServiceWithAlerts(db, prometheusClient, cfg.Health, alertAnalyzer)
				go func() {
					// Let the startup full reconciliation finish before the first
					// health transaction writes snapshots and scores to SQLite.
					time.Sleep(10 * time.Second)
					healthService.Run(context.Background(), healthInterval)
				}()
			}
		}
	}

	// 6. 注册路由
	mux := http.NewServeMux()

	// 6.1 基础健康检查
	mux.HandleFunc("/", newWebHandler(cfg.Web.StaticDir))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Atlas Server is healthy\n"))
	})

	// 6.2 API 路由 (原 API 服务的功能)
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		payload := map[string]interface{}{
			"status":      "ok",
			"message":     "Atlas API is running",
			"version":     Version,
			"commit":      Commit,
			"build_time":  BuildTime,
			"go_version":  runtime.Version(),
			"server_time": time.Now().Format(time.RFC3339),
		}
		_ = json.NewEncoder(w).Encode(payload)
	})

	// 6.3 Gateway 路由 (原 Gateway 服务的功能，用于接收外部推送)
	mux.HandleFunc("/api/v1/webhook/alert", handler.HandleAlertWebhook)
	mux.HandleFunc("/open-apis/bot/v2/hook/", handler.HandleFeishuBotWebhook)
	mux.HandleFunc("/api/v1/alerts/ingestions", handler.HandleRecentIngestions)
	mux.HandleFunc("/api/v1/alerts/ingestions/", handler.HandleIngestionSubresources)
	mux.HandleFunc("/api/v1/alerts/failures", handler.HandleFailedIngestions)
	mux.HandleFunc("/api/v1/push/metrics", handler.HandleMetricsPush)
	mux.HandleFunc("/api/v1/data-freshness", freshnessHandler.Handle)
	mux.HandleFunc("/api/v1/features", featureHandler.HandleCollection)
	mux.HandleFunc("/api/v1/features/baselines", baselineHandler.HandleCollection)
	mux.HandleFunc("/api/v1/features/", featureHandler.HandleItem)
	mux.HandleFunc("/api/v1/issues/summary", issueHandler.HandleSummary)
	mux.HandleFunc("/api/v1/issues/training-data", issueHandler.HandleTrainingData)
	mux.HandleFunc("/api/v1/issues", issueHandler.HandleCollection)
	mux.HandleFunc("/api/v1/issues/", issueHandler.HandleSubresource)
	mux.HandleFunc("/api/v1/platform-config", platformConfigHandler.Handle)
	mux.HandleFunc("/api/v1/platform-config/assets", assetConfigHandler.Handle)
	mux.HandleFunc("/api/v1/node-access/overview", nodeAccessHandler.HandleOverview)
	mux.HandleFunc("/api/v1/node-access/credentials", nodeAccessHandler.HandleCredentials)
	mux.HandleFunc("/api/v1/node-access/credentials/", nodeAccessHandler.HandleCredential)
	mux.HandleFunc("/api/v1/node-access/checks", nodeAccessHandler.HandleChecks)
	mux.HandleFunc("/api/v1/node-access/collections", nodeAccessHandler.HandleCollections)

	// 6.4 GPU hardware inventory and collection coverage (read-only).
	mux.HandleFunc("/api/v1/fleet/summary", inventoryHandler.HandleFleetSummary)
	mux.HandleFunc("/api/v1/nodes", inventoryHandler.HandleNodes)
	mux.HandleFunc("/api/v1/nodes/", inventoryHandler.HandleNodeDetail)
	mux.HandleFunc("/api/v1/gpus", inventoryHandler.HandleGPUs)
	mux.HandleFunc("/api/v1/targets", inventoryHandler.HandleTargets)
	mux.HandleFunc("/api/v1/sync-runs", inventoryHandler.HandleSyncRuns)
	mux.HandleFunc("/api/v1/inventory/changes", inventoryHandler.HandleChanges)
	mux.HandleFunc("/api/v1/inventory/source-assets", inventoryHandler.HandleSourceAssets)
	mux.HandleFunc("/api/v1/inventory/reconciliation", inventoryHandler.HandleReconciliation)
	mux.HandleFunc("/api/v1/health/summary", healthHandler.HandleSummary)
	mux.HandleFunc("/api/v1/health/gpus", healthHandler.HandleScores)
	mux.HandleFunc("/api/v1/health/runs", healthHandler.HandleRuns)
	mux.HandleFunc("/api/v1/health/telemetry-quality", healthHandler.HandleTelemetryQuality)
	mux.HandleFunc("/api/v1/fault-events", healthHandler.HandleEvents)
	mux.HandleFunc("/api/v1/fault-events/summary", healthHandler.HandleEventSummary)
	mux.HandleFunc("/api/v1/fault-events/", evidenceHandler.HandleEventSubresource)
	mux.HandleFunc("/api/v1/degradation/summary", degradationHandler.HandleSummary)
	mux.HandleFunc("/api/v1/degradation/candidates", degradationHandler.HandleCandidates)
	mux.HandleFunc("/api/v1/prediction/overview", predictionHandler.HandleOverview)
	mux.HandleFunc("/api/v1/prediction/models", predictionHandler.HandleModels)
	mux.HandleFunc("/api/v1/prediction/readiness", predictionHandler.HandleReadiness)
	mux.HandleFunc("/api/v1/prediction/results", predictionHandler.HandleResults)
	mux.HandleFunc("/api/v1/prediction/feature-parity", predictionHandler.HandleFeatureParity)
	mux.HandleFunc("/api/v1/prediction/labels", predictionHandler.HandleLabels)
	mux.HandleFunc("/api/v1/prediction/accuracy", predictionHandler.HandleAccuracy)
	mux.HandleFunc("/api/v1/prediction/outcome-report", predictionHandler.HandleOutcomeReport)
	mux.HandleFunc("/api/v1/prediction/model-governance", predictionHandler.HandleModelGovernance)
	mux.HandleFunc("/api/v1/prediction/hearank-challenger", predictionHandler.HandleHeaRankChallenger)
	mux.HandleFunc("/api/v1/prediction/risk-ranking-snapshot", predictionHandler.HandleRiskRankingSnapshot)
	mux.HandleFunc("/api/v1/prediction/dual-track-validation", predictionHandler.HandleDualTrackValidation)
	mux.HandleFunc("/api/v1/prediction/label-manifest", predictionHandler.HandleLabelManifest)
	mux.HandleFunc("/api/v1/prediction/validation-readiness", predictionHandler.HandleValidationReadiness)
	mux.HandleFunc("/api/v1/prediction/promotion-decision", predictionHandler.HandlePromotionDecision)
	mux.HandleFunc("/api/v1/prediction/evidence-bundle", predictionHandler.HandleEvidenceBundle)
	mux.HandleFunc("/api/v1/prediction/human-feedback-manifest", predictionHandler.HandleHumanFeedbackManifest)
	mux.HandleFunc("/api/v1/prediction/data-drift-report", predictionHandler.HandleDataDriftReport)
	mux.HandleFunc("/api/v1/prediction/calibration-drift-report", predictionHandler.HandleCalibrationDriftReport)
	mux.HandleFunc("/api/v1/prediction/feature-drift-report", predictionHandler.HandleFeatureDriftReport)
	mux.HandleFunc("/api/v1/prediction/outcomes", predictionHandler.HandleOutcomes)
	mux.HandleFunc("/api/v1/prediction/outcomes/", predictionHandler.HandleOutcome)
	mux.HandleFunc("/api/v1/prediction/history/sources", historyHandler.HandleSources)
	mux.HandleFunc("/api/v1/prediction/history/audits", historyHandler.HandleAudits)
	mux.HandleFunc("/api/v1/prediction/history/backfills", historyHandler.HandleBackfills)
	mux.HandleFunc("/api/v1/prediction/history/feature-replays", historyHandler.HandleFeatureReplays)
	mux.HandleFunc("/api/v1/prediction/history/live-coverage", historyHandler.HandleLiveCoverageAudits)
	mux.HandleFunc("/api/v1/prediction/history/feature-distributions", historyHandler.HandleFeatureDistributions)
	mux.HandleFunc("/api/v1/prediction/history/shadow-scoring", historyHandler.HandleShadowScoringRuns)
	mux.HandleFunc("/api/v1/prediction/history/identity-backfills", historyHandler.HandleIdentityBackfills)
	mux.HandleFunc("/api/v1/prediction/history/identities", historyHandler.HandleIdentities)
	mux.HandleFunc("/api/v1/prediction/history/datasets", historyHandler.HandleDatasets)
	mux.HandleFunc("/api/v1/prediction/history/feature-datasets", historyHandler.HandleFeatureDatasets)
	mux.HandleFunc("/api/v1/prediction/history/training-preparations", historyHandler.HandleTrainingPreparations)
	mux.HandleFunc("/api/v1/prediction/history/control-feature-datasets", historyHandler.HandleControlFeatureDatasets)
	mux.HandleFunc("/api/v1/prediction/history/training-matrices", historyHandler.HandleTrainingMatrices)
	mux.HandleFunc("/api/v1/prediction/history/training-matrices/", historyHandler.HandleTrainingMatrix)
	mux.HandleFunc("/api/v1/prediction/history/baseline-models", historyHandler.HandleBaselineModels)
	mux.HandleFunc("/api/v1/prediction/history/baseline-models/", historyHandler.HandleBaselineModel)
	mux.HandleFunc("/api/v1/prediction/history/candidates", historyHandler.HandleCandidates)
	mux.HandleFunc("/api/v1/prediction/history/candidate-rules", historyHandler.HandleCandidateRules)
	mux.HandleFunc("/api/v1/prediction/history/candidates/", historyHandler.HandleCandidate)

	// 7. 启动服务
	port := cfg.Gateway.Port
	fmt.Printf("Atlas Server listening on port %s\n", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalf("Atlas Server failed to start: %v", err)
	}
}

func parseDurationOrDefault(name, value string, fallback time.Duration) time.Duration {
	interval, err := time.ParseDuration(value)
	if err != nil || interval <= 0 {
		log.Printf("Invalid %s interval %q; using %s", name, value, fallback)
		return fallback
	}
	return interval
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
	if webDir := strings.TrimSpace(os.Getenv("ATLAS_WEB_DIR")); webDir != "" {
		cfg.Web.StaticDir = webDir
	}
	if prometheusURL := strings.TrimSpace(os.Getenv("ATLAS_PROMETHEUS_URL")); prometheusURL != "" {
		cfg.Prometheus.BaseURL = prometheusURL
		cfg.Inventory.Enabled = true
	}
	if driver := strings.TrimSpace(os.Getenv("ATLAS_DATABASE_DRIVER")); driver != "" {
		cfg.Storage.Driver = driver
	}
	if dsn := strings.TrimSpace(os.Getenv("ATLAS_DATABASE_DSN")); dsn != "" {
		cfg.Storage.DSN = dsn
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

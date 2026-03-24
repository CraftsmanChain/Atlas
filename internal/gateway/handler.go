package gateway

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"atlas/internal/analyzer"
	"atlas/pkg/api"
	"atlas/pkg/storage"
)

// Handler 处理网关级别的 HTTP 请求
type Handler struct {
	db       *storage.DB
	analyzer *analyzer.AlertAnalyzer
}

// NewHandler 创建一个新的 Handler 实例
func NewHandler(db *storage.DB, analyzer *analyzer.AlertAnalyzer) *Handler {
	return &Handler{
		db:       db,
		analyzer: analyzer,
	}
}

// HandleAlertWebhook 接收并处理来自外部监控系统的告警回调
func (h *Handler) HandleAlertWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading request body: %v", err)
		http.Error(w, "Error reading request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	var event api.AlertEvent
	if err := json.Unmarshal(body, &event); err != nil {
		log.Printf("Error parsing JSON: %v. Body: %s", err, string(body))
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	// 基础的数据校验
	if event.Source == "" || event.Message == "" {
		http.Error(w, "Missing required fields: source or message", http.StatusBadRequest)
		return
	}

	// 异步交给 Analyzer 处理，不阻塞网关返回
	go h.analyzer.Process(&event)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"accepted", "message":"Alert event received successfully"}`))
}

// HandleMetricsPush 接收 Agent 主动推送上来的系统指标数据
func (h *Handler) HandleMetricsPush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var metrics api.SystemMetrics
	if err := json.NewDecoder(r.Body).Decode(&metrics); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	// 存储到数据库中
	if err := h.db.SaveSystemMetrics(&metrics); err != nil {
		log.Printf("Failed to save metrics to DB: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

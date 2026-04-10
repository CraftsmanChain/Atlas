package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"atlas/internal/analyzer"
	"atlas/pkg/api"
	"atlas/pkg/storage"
)

const (
	maxProcessRetries  = 3
	maxCallbackRetries = 3
)

// Handler 处理网关级别的 HTTP 请求
type Handler struct {
	db           *storage.DB
	analyzer     *analyzer.AlertAnalyzer
	webhookToken string
}

// NewHandler 创建一个新的 Handler 实例
func NewHandler(db *storage.DB, analyzer *analyzer.AlertAnalyzer, webhookToken string) *Handler {
	return &Handler{
		db:           db,
		analyzer:     analyzer,
		webhookToken: webhookToken,
	}
}

// HandleAlertWebhook 接收并处理来自外部监控系统的告警回调
func (h *Handler) HandleAlertWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.webhookToken != "" && r.Header.Get("X-Webhook-Token") != h.webhookToken {
		http.Error(w, "Unauthorized webhook request", http.StatusUnauthorized)
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

	log.Printf(
		"[Gateway] Alert received: source=%s level=%s host=%s message=%q labels=%v timestamp=%s",
		event.Source,
		event.Level,
		event.Host,
		event.Message,
		event.Labels,
		event.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
	)
	log.Printf("[Gateway] Alert raw payload: %s", string(body))

	record := &api.AlertIngestionRecord{
		Source:         event.Source,
		Host:           event.Host,
		Level:          event.Level,
		Message:        event.Message,
		RawPayload:     string(body),
		ProcessStatus:  "processing",
		CallbackURL:    event.CallbackURL,
		CallbackStatus: "disabled",
	}
	if event.CallbackURL != "" {
		record.CallbackStatus = "pending"
	}
	if err := h.db.Create(record).Error; err != nil {
		log.Printf("[Gateway] Failed to create ingestion record: %v", err)
		record = nil
	}

	// 异步处理，确保 webhook 快速返回
	go h.processAlertAsync(record, event)

	requestID := uint(0)
	if record != nil {
		requestID = record.ID
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "accepted",
		"message":    "Alert event received successfully",
		"request_id": requestID,
	})
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

// HandleFailedIngestions 返回最近失败的异步告警处理记录，供页面展示与排障。
func (h *Handler) HandleFailedIngestions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			limit = parsed
		}
	}
	records, err := h.db.ListFailedIngestions(limit)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"items": records,
		"total": len(records),
	})
}

func (h *Handler) processAlertAsync(record *api.AlertIngestionRecord, event api.AlertEvent) {
	processAttempts, processErr := h.runProcessWithRetry(&event)

	processStatus := "success"
	processLastError := ""
	if processErr != nil {
		processStatus = "failed"
		processLastError = processErr.Error()
	}
	h.updateIngestionRecord(record, map[string]interface{}{
		"event_id":           event.ID,
		"process_status":     processStatus,
		"process_attempts":   processAttempts,
		"process_last_error": processLastError,
	})

	if event.CallbackURL == "" {
		return
	}

	callbackPayload := map[string]interface{}{
		"request_id":       0,
		"event_id":         event.ID,
		"source":           event.Source,
		"status":           processStatus,
		"process_attempts": processAttempts,
		"error":            processLastError,
		"timestamp":        time.Now().UTC().Format(time.RFC3339),
	}
	if record != nil {
		callbackPayload["request_id"] = record.ID
	}

	callbackAttempts, callbackHTTPStatus, callbackErr := h.runCallbackWithRetry(event.CallbackURL, event.CallbackToken, callbackPayload)
	callbackStatus := "success"
	callbackLastError := ""
	if callbackErr != nil {
		callbackStatus = "failed"
		callbackLastError = callbackErr.Error()
	}

	h.updateIngestionRecord(record, map[string]interface{}{
		"callback_status":      callbackStatus,
		"callback_attempts":    callbackAttempts,
		"callback_http_status": callbackHTTPStatus,
		"callback_last_error":  callbackLastError,
		"callback_last_at":     time.Now(),
	})
}

func (h *Handler) runProcessWithRetry(event *api.AlertEvent) (int, error) {
	if h.analyzer == nil {
		return 1, fmt.Errorf("alert analyzer is not initialized")
	}
	var lastErr error
	for attempt := 1; attempt <= maxProcessRetries; attempt++ {
		if err := h.analyzer.Process(event); err == nil {
			return attempt, nil
		} else {
			lastErr = err
			log.Printf("[Gateway] Alert process failed (attempt %d/%d): %v", attempt, maxProcessRetries, err)
		}
		if attempt < maxProcessRetries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	return maxProcessRetries, lastErr
}

func (h *Handler) runCallbackWithRetry(callbackURL, callbackToken string, payload map[string]interface{}) (int, int, error) {
	var lastErr error
	lastStatus := 0
	for attempt := 1; attempt <= maxCallbackRetries; attempt++ {
		status, err := sendCallback(callbackURL, callbackToken, payload)
		lastStatus = status
		if err == nil {
			return attempt, status, nil
		}
		lastErr = err
		log.Printf("[Gateway] Callback failed (attempt %d/%d): %v", attempt, maxCallbackRetries, err)
		if attempt < maxCallbackRetries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	return maxCallbackRetries, lastStatus, lastErr
}

func sendCallback(callbackURL, callbackToken string, payload map[string]interface{}) (int, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest(http.MethodPost, callbackURL, bytes.NewBuffer(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if callbackToken != "" {
		req.Header.Set("X-Callback-Token", callbackToken)
	}

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("callback returned status %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func (h *Handler) updateIngestionRecord(record *api.AlertIngestionRecord, updates map[string]interface{}) {
	if record == nil || record.ID == 0 {
		return
	}
	if err := h.db.Model(&api.AlertIngestionRecord{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
		log.Printf("[Gateway] Failed to update ingestion record id=%d: %v", record.ID, err)
	}
}

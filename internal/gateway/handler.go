package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
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
	db                  *storage.DB
	ingestionDB         *storage.DB
	analyzer            *analyzer.AlertAnalyzer
	webhookToken        string
	feishuWebhookToken  string
	ingestionSourceMode string
	ingestionStaleAfter time.Duration
	now                 func() time.Time
}

type alertIngestionView struct {
	api.AlertIngestionRecord
	Labels             api.StringMap `json:"labels,omitempty"`
	EventTimestamp     *time.Time    `json:"event_timestamp,omitempty"`
	AIReportID         uint          `json:"ai_report_id,omitempty"`
	AIReportStatus     string        `json:"ai_report_status,omitempty"`
	AIReportSummary    string        `json:"ai_report_summary,omitempty"`
	AIReportUpdated    time.Time     `json:"ai_report_updated_at,omitempty"`
	AIReportConfidence float64       `json:"ai_report_confidence,omitempty"`
}

// NewHandler 创建一个新的 Handler 实例
func NewHandler(db, ingestionDB *storage.DB, analyzer *analyzer.AlertAnalyzer, webhookToken, feishuWebhookToken, ingestionSourceMode string, ingestionStaleAfter time.Duration) *Handler {
	if ingestionDB == nil {
		ingestionDB = db
	}
	if strings.TrimSpace(ingestionSourceMode) == "" {
		ingestionSourceMode = "local-live"
	}
	if ingestionStaleAfter <= 0 {
		ingestionStaleAfter = 15 * time.Minute
	}
	return &Handler{
		db:                  db,
		ingestionDB:         ingestionDB,
		analyzer:            analyzer,
		webhookToken:        webhookToken,
		feishuWebhookToken:  feishuWebhookToken,
		ingestionSourceMode: ingestionSourceMode,
		ingestionStaleAfter: ingestionStaleAfter,
		now:                 time.Now,
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

	if err := validateAlertEvent(&event); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.acceptAlert(w, event, string(body))
}

// HandleFeishuBotWebhook 兼容飞书机器人 Webhook 发送格式，便于外部告警平台直接投递到 Atlas。
func (h *Handler) HandleFeishuBotWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := extractFeishuHookToken(r.URL.Path)
	if token == "" {
		http.Error(w, "Missing webhook token in path", http.StatusBadRequest)
		return
	}
	if h.feishuWebhookToken != "" && token != h.feishuWebhookToken {
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

	events, err := parseFeishuWebhookAlerts(body)
	if err != nil {
		log.Printf("Error parsing feishu webhook: %v. Body: %s", err, string(body))
		http.Error(w, "Invalid Feishu webhook payload", http.StatusBadRequest)
		return
	}

	if len(events) == 0 {
		http.Error(w, "Invalid Feishu webhook payload", http.StatusBadRequest)
		return
	}

	for i := range events {
		if err := validateAlertEvent(&events[i]); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	if len(events) == 1 {
		h.acceptAlert(w, events[0], string(body))
		return
	}
	h.acceptAlerts(w, events, string(body))
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
	records, err := h.ingestionDB.ListFailedIngestions(limit)
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

// HandleRecentIngestions 返回最近接收的告警记录，便于校验 Atlas 的接入和解析效果。
func (h *Handler) HandleRecentIngestions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed <= 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	var beforeID uint
	if v := r.URL.Query().Get("before_id"); v != "" {
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil || parsed == 0 || uint64(uint(parsed)) != parsed {
			http.Error(w, "invalid before_id", http.StatusBadRequest)
			return
		}
		beforeID = uint(parsed)
	}
	now := h.now()
	page, err := h.ingestionDB.ListIngestions(storage.IngestionListOptions{
		Limit:    limit,
		BeforeID: beforeID,
		Level:    r.URL.Query().Get("level"),
		Query:    r.URL.Query().Get("query"),
	}, now)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]alertIngestionView, 0, len(page.Records))
	for _, record := range page.Records {
		view := alertIngestionView{AlertIngestionRecord: record}
		if event, err := h.ingestionDB.GetAlertEventByID(record.EventID); err == nil && event != nil {
			view.Labels = event.Labels
			if !event.Timestamp.IsZero() {
				timestamp := event.Timestamp
				view.EventTimestamp = &timestamp
			}
		}
		enrichIngestionViewFromRawPayload(&view)
		if report, err := h.ingestionDB.GetLatestAIAnalysisReportForIngestion(record.ID); err == nil && report != nil {
			view.AIReportID = report.ID
			view.AIReportStatus = report.Status
			view.AIReportSummary = report.Summary
			view.AIReportUpdated = report.UpdatedAt
			view.AIReportConfidence = report.Confidence
		}
		items = append(items, view)
	}
	streamStatus := "empty"
	if page.LatestAt != nil {
		switch strings.ToLower(strings.TrimSpace(h.ingestionSourceMode)) {
		case "snapshot":
			streamStatus = "snapshot"
		case "upstream-readonly":
			streamStatus = "live"
		default:
			streamStatus = "live"
			if now.Sub(*page.LatestAt) > h.ingestionStaleAfter {
				streamStatus = "stale"
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"items":              items,
		"total":              page.Total,
		"all_total":          page.AllTotal,
		"limit":              page.Limit,
		"has_more":           page.HasMore,
		"next_before_id":     page.NextBeforeID,
		"latest_received_at": page.LatestAt,
		"received_5m":        page.Count5m,
		"received_1h":        page.Count1h,
		"source_mode":        h.ingestionSourceMode,
		"stream_status":      streamStatus,
		"server_time":        now,
	})
}

// enrichIngestionViewFromRawPayload repairs presentation fields for historical
// records that were accepted before a newer Feishu card format was understood.
// It does not mutate the ingestion or event tables.
func enrichIngestionViewFromRawPayload(view *alertIngestionView) {
	if view == nil || strings.TrimSpace(view.RawPayload) == "" {
		return
	}
	needsFallback := strings.TrimSpace(view.Host) == "" || len(view.Labels) == 0 || view.EventTimestamp == nil
	if !needsFallback {
		return
	}
	events, err := parseFeishuWebhookAlerts([]byte(view.RawPayload))
	if err != nil || len(events) == 0 {
		return
	}
	candidate := events[0]
	for _, event := range events {
		if strings.TrimSpace(view.Message) != "" && event.Message == view.Message {
			candidate = event
			if strings.TrimSpace(view.Host) == "" || event.Host == view.Host {
				break
			}
		}
	}
	if strings.TrimSpace(view.Host) == "" {
		view.Host = candidate.Host
	}
	if strings.TrimSpace(view.Message) == "" {
		view.Message = candidate.Message
	}
	if strings.TrimSpace(view.Level) == "" || view.Level == "info" {
		view.Level = candidate.Level
	}
	if len(view.Labels) == 0 {
		view.Labels = candidate.Labels
	} else {
		for key, value := range candidate.Labels {
			if _, exists := view.Labels[key]; !exists {
				view.Labels[key] = value
			}
		}
	}
	if view.EventTimestamp == nil && !candidate.Timestamp.IsZero() {
		timestamp := candidate.Timestamp
		view.EventTimestamp = &timestamp
	}
}

// HandleIngestionSubresources 返回接收记录的下级资源，当前支持查询 AI 分析报告。
func (h *Handler) HandleIngestionSubresources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/alerts/ingestions/")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "analysis" {
		http.NotFound(w, r)
		return
	}

	ingestionID, err := strconv.Atoi(parts[0])
	if err != nil || ingestionID <= 0 {
		http.Error(w, "Invalid ingestion id", http.StatusBadRequest)
		return
	}

	report, err := h.ingestionDB.GetLatestAIAnalysisReportForIngestion(uint(ingestionID))
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if report == nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(report)
}

func (h *Handler) acceptAlert(w http.ResponseWriter, event api.AlertEvent, rawPayload string) {
	log.Printf(
		"[Gateway] Alert received: source=%s level=%s host=%s message=%q labels=%v timestamp=%s",
		event.Source,
		event.Level,
		event.Host,
		event.Message,
		event.Labels,
		event.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
	)
	log.Printf("[Gateway] Alert raw payload: %s", rawPayload)

	record := &api.AlertIngestionRecord{
		Source:         event.Source,
		Host:           event.Host,
		Level:          event.Level,
		Message:        event.Message,
		RawPayload:     rawPayload,
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
	h.createPendingAIReport(record, event)

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

func (h *Handler) acceptAlerts(w http.ResponseWriter, events []api.AlertEvent, rawPayload string) {
	requestIDs := make([]uint, 0, len(events))
	for _, event := range events {
		record := &api.AlertIngestionRecord{
			Source:         event.Source,
			Host:           event.Host,
			Level:          event.Level,
			Message:        event.Message,
			RawPayload:     rawPayload,
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
		h.createPendingAIReport(record, event)
		go h.processAlertAsync(record, event)
		if record != nil {
			requestIDs = append(requestIDs, record.ID)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "accepted",
		"message":     "Alert events received successfully",
		"count":       len(events),
		"request_ids": requestIDs,
	})
}

func validateAlertEvent(event *api.AlertEvent) error {
	if event.Source == "" || event.Message == "" {
		return fmt.Errorf("missing required fields: source or message")
	}
	if event.Level == "" {
		event.Level = "info"
	}
	if event.Labels == nil {
		event.Labels = api.StringMap{}
	}
	return nil
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
	h.updatePendingAIReport(record, event, processStatus, processLastError)

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

func (h *Handler) createPendingAIReport(record *api.AlertIngestionRecord, event api.AlertEvent) {
	if record == nil || record.ID == 0 {
		return
	}
	report := &api.AIAnalysisReport{
		IngestionRecordID: record.ID,
		EventID:           event.ID,
		AnalysisType:      "alert_rca",
		Status:            "pending",
		Model:             "pending",
		PromptVersion:     "v1",
		Severity:          event.Level,
		Summary:           "",
	}
	if err := h.db.Create(report).Error; err != nil {
		log.Printf("[Gateway] Failed to create pending AI report for ingestion id=%d: %v", record.ID, err)
	}
}

func (h *Handler) updatePendingAIReport(record *api.AlertIngestionRecord, event api.AlertEvent, processStatus, processLastError string) {
	if record == nil || record.ID == 0 {
		return
	}
	updates := map[string]interface{}{
		"event_id":   event.ID,
		"severity":   event.Level,
		"updated_at": time.Now(),
	}
	if processStatus == "failed" {
		updates["status"] = "blocked"
		updates["error_message"] = processLastError
		updates["summary"] = "Alert ingestion failed before AI analysis could start"
	} else {
		for key, value := range buildPlaceholderAIReport(event) {
			updates[key] = value
		}
	}
	if err := h.db.Model(&api.AIAnalysisReport{}).
		Where("ingestion_record_id = ? AND analysis_type = ?", record.ID, "alert_rca").
		Updates(updates).Error; err != nil {
		log.Printf("[Gateway] Failed to update pending AI report for ingestion id=%d: %v", record.ID, err)
	}
}

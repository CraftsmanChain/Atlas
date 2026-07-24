package health

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

type Handler struct{ db *storage.DB }

type countRow struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

func NewHandler(db *storage.DB) *Handler { return &Handler{db: db} }

func (h *Handler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var total, scored, unknown int64
	var fallbackGPUs, consistencyCandidateGPUs int64
	var average float64
	var levels, confidence []countRow
	query := h.db.Model(&api.GPUHealthScore{}).Where("current = ?", true)
	query.Count(&total)
	query.Where("score IS NOT NULL").Count(&scored)
	unknown = total - scored
	h.db.Model(&api.GPUHealthScore{}).Where("current = ? AND score IS NOT NULL", true).Select("coalesce(avg(score), 0)").Scan(&average)
	h.db.Model(&api.GPUHealthScore{}).Where("current = ?", true).Select("level AS name, count(*) AS count").Group("level").Scan(&levels)
	h.db.Model(&api.GPUHealthScore{}).Where("current = ?", true).Select("data_confidence AS name, count(*) AS count").Group("data_confidence").Scan(&confidence)
	h.db.Model(&api.GPUHealthScore{}).Joins("JOIN gpu_feature_snapshots ON gpu_feature_snapshots.id = gpu_health_scores.feature_snapshot_id").Where("gpu_health_scores.current = ? AND gpu_feature_snapshots.fallback_metric_count > 0", true).Count(&fallbackGPUs)
	h.db.Model(&api.GPUHealthScore{}).Joins("JOIN gpu_feature_snapshots ON gpu_feature_snapshots.id = gpu_health_scores.feature_snapshot_id").Where("gpu_health_scores.current = ? AND gpu_feature_snapshots.consistency_candidate_count > 0", true).Count(&consistencyCandidateGPUs)
	var latest api.HealthEvaluationRun
	var latestValue any
	if h.db.Where("status = ?", "success").Order("finished_at DESC").First(&latest).Error == nil {
		latestValue = latest
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"total": total, "scored": scored, "unknown": unknown, "fallback_gpus": fallbackGPUs, "source_difference_gpus": consistencyCandidateGPUs, "consistency_candidate_gpus": consistencyCandidateGPUs, "inconsistent_gpus": 0, "average_score": average, "by_level": rowsToMap(levels), "by_confidence": rowsToMap(confidence), "latest_run": latestValue}})
}

func (h *Handler) HandleScores(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query := h.db.Model(&api.GPUHealthScore{}).Where("current = ?", true)
	for field, column := range map[string]string{"node_ip": "node_ip", "level": "level", "data_confidence": "data_confidence", "model": "model_name"} {
		if value := strings.TrimSpace(r.URL.Query().Get(field)); value != "" {
			query = query.Where(column+" = ?", value)
		}
	}
	if value := strings.TrimSpace(r.URL.Query().Get("q")); value != "" {
		like := "%" + value + "%"
		query = query.Where("node_ip LIKE ? OR gpu_uuid LIKE ? OR model_name LIKE ?", like, like, like)
	}
	var total int64
	query.Count(&total)
	limit, offset := pagination(r, 1000, 2000)
	var rows []api.GPUHealthScore
	if err := query.Order("score IS NULL ASC, score ASC, node_ip ASC, gpu_index ASC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type scoreResponse struct {
		api.GPUHealthScore
		MetricSources             api.StringMap  `json:"metric_sources"`
		SourcesAvailable          api.StringList `json:"sources_available"`
		FallbackMetricCount       int            `json:"fallback_metric_count"`
		ConsistencyCandidates     api.StringList `json:"consistency_candidates"`
		ConsistencyCandidateCount int            `json:"consistency_candidate_count"`
		ConsistencyIssues         api.StringList `json:"consistency_issues"`
		ConsistencyIssueCount     int            `json:"consistency_issue_count"`
	}
	snapshotIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		if row.FeatureSnapshotID > 0 {
			snapshotIDs = append(snapshotIDs, row.FeatureSnapshotID)
		}
	}
	snapshotByID := make(map[uint]api.GPUFeatureSnapshot, len(snapshotIDs))
	if len(snapshotIDs) > 0 {
		var snapshots []api.GPUFeatureSnapshot
		if err := h.db.Where("id IN ?", snapshotIDs).Find(&snapshots).Error; err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, snapshot := range snapshots {
			snapshotByID[snapshot.ID] = snapshot
		}
	}
	responseRows := make([]scoreResponse, 0, len(rows))
	for _, row := range rows {
		snapshot := snapshotByID[row.FeatureSnapshotID]
		responseRows = append(responseRows, scoreResponse{GPUHealthScore: row, MetricSources: snapshot.MetricSources, SourcesAvailable: snapshot.SourcesAvailable, FallbackMetricCount: snapshot.FallbackMetricCount, ConsistencyCandidates: snapshot.ConsistencyCandidates, ConsistencyCandidateCount: snapshot.ConsistencyCandidateCount, ConsistencyIssues: snapshot.ConsistencyIssues, ConsistencyIssueCount: snapshot.ConsistencyIssueCount})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": responseRows, "meta": map[string]any{"total": total, "limit": limit, "offset": offset, "generated_at": time.Now().Format(time.RFC3339)}})
}

func (h *Handler) HandleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit, offset := pagination(r, 20, 200)
	var total int64
	h.db.Model(&api.HealthEvaluationRun{}).Count(&total)
	var rows []api.HealthEvaluationRun
	if err := h.db.Order("started_at DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": total, "limit": limit, "offset": offset}})
}

type telemetryQualityItem struct {
	GPUAssetID   uint      `json:"gpu_asset_id"`
	GPUUUID      string    `json:"gpu_uuid"`
	NodeIP       string    `json:"node_ip"`
	GPUIndex     int       `json:"gpu_index"`
	ModelName    string    `json:"model_name"`
	Status       string    `json:"status"`
	SampleCount  *float64  `json:"sample_count_1h,omitempty"`
	PresenceRate *float64  `json:"presence_ratio_1h,omitempty"`
	SampleAge    *float64  `json:"sample_age_seconds,omitempty"`
	ObservedAt   time.Time `json:"observed_at"`
}

func (h *Handler) HandleTelemetryQuality(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var latest api.HealthEvaluationRun
	if err := h.db.Where("status = ?", "success").Order("finished_at DESC, id DESC").First(&latest).Error; err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"data": []telemetryQualityItem{}, "summary": map[string]any{"total": 0, "by_status": map[string]int64{}, "feature_catalog_version": ""}, "meta": map[string]any{"total": 0}})
		return
	}
	var snapshots []api.GPUFeatureSnapshot
	if err := h.db.Where("evaluation_run_id = ?", latest.ID).Order("node_ip ASC, gpu_index ASC").Find(&snapshots).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	allRows := make([]telemetryQualityItem, 0, len(snapshots))
	byStatus := map[string]int64{}
	var presenceTotal, maxAge float64
	var presenceCount int
	for _, snapshot := range snapshots {
		item := classifyTelemetryQuality(snapshot)
		allRows = append(allRows, item)
		byStatus[item.Status]++
		if item.PresenceRate != nil {
			presenceTotal += *item.PresenceRate
			presenceCount++
		}
		if item.SampleAge != nil && *item.SampleAge > maxAge {
			maxAge = *item.SampleAge
		}
	}
	filtered := make([]telemetryQualityItem, 0, len(allRows))
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	for _, item := range allRows {
		if statusFilter != "" && item.Status != statusFilter {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(item.NodeIP+" "+item.GPUUUID+" "+item.ModelName), search) {
			continue
		}
		filtered = append(filtered, item)
	}
	total := len(filtered)
	limit, offset := pagination(r, 2000, 2000)
	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	averagePresence := float64(0)
	if presenceCount > 0 {
		averagePresence = presenceTotal / float64(presenceCount)
	}
	catalogVersion := ""
	if len(snapshots) > 0 {
		catalogVersion = snapshots[0].FeatureCatalogVersion
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": filtered[offset:end],
		"summary": map[string]any{
			"total": len(allRows), "by_status": byStatus, "average_presence_ratio_1h": averagePresence,
			"max_sample_age_seconds": maxAge, "feature_catalog_version": catalogVersion,
			"evaluation_run_id": latest.ID, "evaluated_at": latest.FinishedAt,
		},
		"meta": map[string]any{"total": total, "limit": limit, "offset": offset},
	})
}

func classifyTelemetryQuality(snapshot api.GPUFeatureSnapshot) telemetryQualityItem {
	item := telemetryQualityItem{GPUAssetID: snapshot.GPUAssetID, GPUUUID: snapshot.GPUUUID, NodeIP: snapshot.NodeIP, GPUIndex: snapshot.GPUIndex, ModelName: snapshot.ModelName, Status: "unknown", ObservedAt: snapshot.ObservedAt}
	if value, ok := snapshot.Metrics["gpu_metric_samples_1h"]; ok {
		item.SampleCount = &value
	}
	if value, ok := snapshot.Metrics["gpu_metric_presence_ratio_1h"]; ok {
		item.PresenceRate = &value
	}
	if value, ok := snapshot.Metrics["gpu_metric_sample_age_seconds"]; ok {
		item.SampleAge = &value
	}
	if item.PresenceRate == nil || item.SampleAge == nil {
		return item
	}
	switch {
	case *item.SampleAge > 300 || *item.PresenceRate < 80:
		item.Status = "stale"
	case *item.SampleAge > 60 || *item.PresenceRate < 95:
		item.Status = "degraded"
	default:
		item.Status = "fresh"
	}
	return item
}

func (h *Handler) HandleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query := h.db.Model(&api.GPUFaultEvent{})
	for field, column := range map[string]string{"node_ip": "node_ip", "state": "state", "severity": "severity", "domain": "domain", "rule_code": "rule_code", "gpu_uuid": "gpu_uuid"} {
		if value := strings.TrimSpace(r.URL.Query().Get(field)); value != "" {
			query = query.Where(column+" = ?", value)
		}
	}
	if value := strings.TrimSpace(r.URL.Query().Get("q")); value != "" {
		like := "%" + value + "%"
		query = query.Where("node_ip LIKE ? OR gpu_uuid LIKE ? OR model_name LIKE ? OR rule_code LIKE ? OR evidence LIKE ?", like, like, like, like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	limit, err := positiveQueryInt(r, "limit", 100, 1000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	beforeID, err := nonNegativeQueryUint(r, "before_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	var rows []api.GPUFaultEvent
	if err := query.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	var nextBeforeID uint
	if len(rows) > 0 {
		nextBeforeID = rows[len(rows)-1].ID
	}
	type eventWithWorkflow struct {
		api.GPUFaultEvent
		IssueID            uint   `json:"issue_id,omitempty"`
		WorkflowStatus     string `json:"workflow_status,omitempty"`
		LatestResolutionID uint   `json:"latest_resolution_id,omitempty"`
	}
	eventIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		eventIDs = append(eventIDs, row.ID)
	}
	issuesByEventID := make(map[uint]api.PlatformIssue, len(rows))
	if len(eventIDs) > 0 {
		var issues []api.PlatformIssue
		if err := h.db.Where("detection_source = ? AND source_record_id IN ?", "health_rule", eventIDs).Find(&issues).Error; err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, issue := range issues {
			issuesByEventID[issue.SourceRecordID] = issue
		}
	}
	result := make([]eventWithWorkflow, 0, len(rows))
	for _, row := range rows {
		item := eventWithWorkflow{GPUFaultEvent: row}
		if issue, ok := issuesByEventID[row.ID]; ok {
			item.IssueID = issue.ID
			item.WorkflowStatus = issue.Status
			item.LatestResolutionID = issue.LatestResolutionID
		}
		result = append(result, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result, "meta": map[string]any{"total": total, "limit": limit, "has_more": hasMore, "next_before_id": nextBeforeID, "generated_at": time.Now().Format(time.RFC3339)}})
}

func (h *Handler) HandleEventSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var total, open int64
	var states, severities []countRow
	h.db.Model(&api.GPUFaultEvent{}).Count(&total)
	h.db.Model(&api.GPUFaultEvent{}).Where("state = ?", "open").Count(&open)
	h.db.Model(&api.GPUFaultEvent{}).Select("state AS name, count(*) AS count").Group("state").Scan(&states)
	h.db.Model(&api.GPUFaultEvent{}).Where("state = ?", "open").Select("severity AS name, count(*) AS count").Group("severity").Scan(&severities)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"total": total, "open": open, "by_state": rowsToMap(states), "open_by_severity": rowsToMap(severities)}})
}

func rowsToMap(rows []countRow) map[string]int64 {
	result := make(map[string]int64, len(rows))
	for _, row := range rows {
		result[row.Name] = row.Count
	}
	return result
}

func pagination(r *http.Request, defaultLimit, maxLimit int) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func positiveQueryInt(r *http.Request, name string, defaultValue, maxValue int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid %s", name)
	}
	if value > maxValue {
		value = maxValue
	}
	return value, nil
}

func nonNegativeQueryUint(r *http.Request, name string) (uint, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 || uint64(uint(value)) != value {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return uint(value), nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"status": status, "message": message}})
}

package inventory

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

type Handler struct {
	db *storage.DB
}

type countRow struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

func NewHandler(db *storage.DB) *Handler { return &Handler{db: db} }

func (h *Handler) HandleFleetSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	var nodeTotal, gpuTotal, knownUUIDs int64
	var nodeStates, gpuStates, targetHealth []countRow
	activeNodeIDs := h.db.Model(&api.GPUNode{}).Select("id").Where("lifecycle <> ?", "retired")
	activeNodeIPs := h.db.Model(&api.GPUNode{}).Select("node_ip").Where("lifecycle <> ?", "retired")
	h.db.Model(&api.GPUNode{}).Where("lifecycle <> ?", "retired").Count(&nodeTotal)
	h.db.Model(&api.GPUAsset{}).Where("node_id IN (?)", activeNodeIDs).Count(&gpuTotal)
	h.db.Model(&api.GPUAsset{}).Where("node_id IN (?) AND current_uuid <> ''", activeNodeIDs).Count(&knownUUIDs)
	h.db.Model(&api.GPUNode{}).Where("lifecycle <> ?", "retired").Select("state AS name, count(*) AS count").Group("state").Scan(&nodeStates)
	h.db.Model(&api.GPUAsset{}).Where("node_id IN (?)", activeNodeIDs).Select("state AS name, count(*) AS count").Group("state").Scan(&gpuStates)
	h.db.Model(&api.CollectorTarget{}).Where("node_ip IN (?)", activeNodeIPs).Select("health AS name, count(*) AS count").Group("health").Scan(&targetHealth)
	var latest api.InventorySyncRun
	latestErr := h.db.Where("status = ?", "success").Order("finished_at DESC").First(&latest).Error
	var latestValue any
	if latestErr == nil {
		latestValue = latest
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"nodes":       map[string]any{"total": nodeTotal, "by_state": rowsToMap(nodeStates)},
		"gpus":        map[string]any{"total": gpuTotal, "known_uuid": knownUUIDs, "unknown_uuid": gpuTotal - knownUUIDs, "by_state": rowsToMap(gpuStates)},
		"targets":     map[string]any{"by_health": rowsToMap(targetHealth)},
		"latest_sync": latestValue,
	}})
}

func (h *Handler) HandleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	query := h.db.Model(&api.GPUNode{})
	if value := strings.TrimSpace(r.URL.Query().Get("state")); value != "" {
		query = query.Where("state = ?", value)
	}
	if value := strings.TrimSpace(r.URL.Query().Get("lifecycle")); value != "" {
		query = query.Where("lifecycle = ?", value)
	}
	if value := strings.TrimSpace(r.URL.Query().Get("q")); value != "" {
		like := "%" + value + "%"
		query = query.Where("node_ip LIKE ? OR hostname LIKE ? OR bmc_ip LIKE ?", like, like, like)
	}
	var total int64
	query.Count(&total)
	limit, offset := pagination(r, 200, 1000)
	var rows []api.GPUNode
	if err := query.Order("node_ip ASC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeList(w, rows, total, limit, offset)
}

func (h *Handler) HandleNodeDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	identity := strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/")
	if identity == "" || strings.Contains(identity, "/") {
		http.NotFound(w, r)
		return
	}
	var node api.GPUNode
	query := h.db.DB
	if id, err := strconv.ParseUint(identity, 10, 64); err == nil {
		query = query.Where("id = ?", id)
	} else {
		query = query.Where("node_ip = ?", identity)
	}
	if err := query.First(&node).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			writeError(w, http.StatusNotFound, "node not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	var gpus []api.GPUAsset
	var targets []api.CollectorTarget
	h.db.Where("node_id = ?", node.ID).Order("gpu_index ASC").Find(&gpus)
	h.db.Where("node_ip = ?", node.NodeIP).Order("job ASC").Find(&targets)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"node": node, "gpus": gpus, "targets": targets}})
}

func (h *Handler) HandleGPUs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	activeNodeIDs := h.db.Model(&api.GPUNode{}).Select("id").Where("lifecycle <> ?", "retired")
	query := h.db.Model(&api.GPUAsset{}).Where("node_id IN (?)", activeNodeIDs)
	for field, column := range map[string]string{"node_ip": "node_ip", "state": "state", "model": "model_name"} {
		if value := strings.TrimSpace(r.URL.Query().Get(field)); value != "" {
			query = query.Where(column+" = ?", value)
		}
	}
	if value := strings.TrimSpace(r.URL.Query().Get("q")); value != "" {
		like := "%" + value + "%"
		query = query.Where("node_ip LIKE ? OR current_uuid LIKE ? OR model_name LIKE ? OR pci_bus_id LIKE ?", like, like, like, like)
	}
	var total int64
	query.Count(&total)
	limit, offset := pagination(r, 200, 2000)
	var rows []api.GPUAsset
	if err := query.Order("node_ip ASC, gpu_index ASC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeList(w, rows, total, limit, offset)
}

func (h *Handler) HandleTargets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	activeNodeIPs := h.db.Model(&api.GPUNode{}).Select("node_ip").Where("lifecycle <> ?", "retired")
	query := h.db.Model(&api.CollectorTarget{}).Where("node_ip IN (?)", activeNodeIPs)
	for field, column := range map[string]string{"node_ip": "node_ip", "job": "job", "health": "health", "reason_code": "reason_code"} {
		if value := strings.TrimSpace(r.URL.Query().Get(field)); value != "" {
			query = query.Where(column+" = ?", value)
		}
	}
	if value := strings.TrimSpace(r.URL.Query().Get("suppressed")); value != "" {
		if suppressed, err := strconv.ParseBool(value); err == nil {
			query = query.Where("suppressed = ?", suppressed)
		}
	}
	var total int64
	query.Count(&total)
	limit, offset := pagination(r, 400, 2000)
	var rows []api.CollectorTarget
	if err := query.Order("node_ip ASC, job ASC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeList(w, rows, total, limit, offset)
}

func (h *Handler) HandleSyncRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit, offset := pagination(r, 20, 200)
	query := h.db.Model(&api.InventorySyncRun{})
	if taskType := strings.TrimSpace(r.URL.Query().Get("task_type")); taskType != "" {
		query = query.Where("task_type = ?", taskType)
	}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	query.Count(&total)
	var rows []api.InventorySyncRun
	if err := query.Order("started_at DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeList(w, rows, total, limit, offset)
}

func (h *Handler) HandleChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit, offset := pagination(r, 50, 500)
	query := h.db.Model(&api.AssetChangeEvent{})
	if nodeIP := strings.TrimSpace(r.URL.Query().Get("node_ip")); nodeIP != "" {
		query = query.Where("node_ip = ?", nodeIP)
	}
	if eventType := strings.TrimSpace(r.URL.Query().Get("event_type")); eventType != "" {
		query = query.Where("event_type = ?", eventType)
	}
	if syncRunID := strings.TrimSpace(r.URL.Query().Get("sync_run_id")); syncRunID != "" {
		query = query.Where("sync_run_id = ?", syncRunID)
	}
	var total int64
	query.Count(&total)
	var rows []api.AssetChangeEvent
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeList(w, rows, total, limit, offset)
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

func writeList(w http.ResponseWriter, data any, total int64, limit, offset int) {
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "meta": map[string]any{"total": total, "limit": limit, "offset": offset, "generated_at": time.Now().Format(time.RFC3339)}})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"status": status, "message": message}})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func rowsToMap(rows []countRow) map[string]int64 {
	result := make(map[string]int64, len(rows))
	for _, row := range rows {
		result[row.Name] = row.Count
	}
	return result
}

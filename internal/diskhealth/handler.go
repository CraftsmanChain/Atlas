package diskhealth

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) HandleDisks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit, offset := pagination(r, 100, 2000)
	options := ListOptions{
		Limit:            limit,
		Offset:           offset,
		Query:            r.URL.Query().Get("q"),
		Level:            r.URL.Query().Get("level"),
		AssessmentStatus: r.URL.Query().Get("assessment_status"),
		Protocol:         r.URL.Query().Get("protocol"),
		MediaType:        r.URL.Query().Get("media_type"),
		Order:            r.URL.Query().Get("order"),
	}
	rows, total, err := h.service.ListCurrentSnapshots(options)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	summary, err := h.service.Summary()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":    rows,
		"summary": summary,
		"meta": map[string]any{
			"total":        total,
			"limit":        limit,
			"offset":       offset,
			"generated_at": time.Now().UTC().Format(time.RFC3339),
		},
	})
}

func (h *Handler) HandleDiskSubresource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/hardware/disks/"), "/")
	if path == "" {
		writeError(w, http.StatusNotFound, "disk entity not found")
		return
	}
	if strings.HasSuffix(path, "/history") {
		entityID := strings.TrimSuffix(path, "/history")
		entityID = strings.Trim(entityID, "/")
		limit, offset := pagination(r, 100, 500)
		history, err := h.service.History(entityID, limit, offset)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": history.Rows, "windows": history.Windows, "meta": map[string]any{"total": history.Total, "limit": limit, "offset": offset}})
		return
	}
	detail, err := h.service.Detail(path)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": detail})
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

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"status": status, "message": message}})
}

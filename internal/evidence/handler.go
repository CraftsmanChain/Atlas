package evidence

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) HandleEventSubresource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/fault-events/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "fault event resource not found")
		return
	}
	eventID, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || eventID == 0 {
		writeError(w, http.StatusBadRequest, "invalid fault event id")
		return
	}
	var payload any
	switch parts[1] {
	case "evidence":
		payload, err = h.service.BuildBundle(uint(eventID))
	case "report":
		payload, err = h.service.BuildReport(uint(eventID))
	default:
		writeError(w, http.StatusNotFound, "fault event resource not found")
		return
	}
	if err != nil {
		if errors.Is(err, ErrEventNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

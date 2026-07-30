package history

import (
	"encoding/json"
	"net/http"
	"strconv"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) HandleSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	rows, err := h.service.Sources()
	if err != nil {
		historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	historyJSON(w, http.StatusOK, map[string]any{
		"data": rows,
		"meta": map[string]any{
			"total": len(rows), "execution": "atlas_deployment_node", "read_only": true,
			"research_metric_families": ResearchMetricFamilies(),
		},
	})
}

func (h *Handler) HandleAudits(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.Audits(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows)}})
	case http.MethodPost:
		rows, err := h.service.AuditAll(r.Context())
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows)}})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleBackfills(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.BackfillRuns(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows)}})
	case http.MethodPost:
		var request BackfillRequest
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		run, err := h.service.StartAlertBackfill(request)
		if err != nil {
			historyJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusAccepted, map[string]any{"data": run})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleCandidates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	summary, rows, err := h.service.Candidates(limit)
	if err != nil {
		historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	historyJSON(w, http.StatusOK, map[string]any{
		"data": rows, "summary": summary, "training_policy": CurrentTrainingCohortPolicy(),
	})
}

func historyJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

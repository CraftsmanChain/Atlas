package prediction

import (
	"encoding/json"
	"net/http"

	"atlas/pkg/storage"
)

type Handler struct {
	service *Service
}

func NewHandler(db *storage.DB) *Handler              { return &Handler{service: NewService(db)} }
func NewHandlerWithService(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) HandleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	overview, err := h.service.Overview()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": overview})
}

func (h *Handler) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	models, err := h.service.Models()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": models, "meta": map[string]any{"total": len(models)}})
}

func (h *Handler) HandleReadiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	summary, rows, err := h.service.Readiness()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": rows, "summary": summary})
}

func (h *Handler) HandleResults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	summary, rows, err := h.service.Results(100)
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": rows, "summary": summary})
}

func (h *Handler) HandleLabels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	summary, rows, err := h.service.Labels(100)
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": rows, "summary": summary})
}

func predictionJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

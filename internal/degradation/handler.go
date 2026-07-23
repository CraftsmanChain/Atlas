package degradation

import (
	"encoding/json"
	"net/http"

	"atlas/pkg/storage"
)

type Handler struct {
	service *Service
}

func NewHandler(db *storage.DB) *Handler { return &Handler{service: NewService(db)} }

func (h *Handler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		write(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	summary, _, err := h.service.Evaluate()
	if err != nil {
		write(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	write(w, http.StatusOK, map[string]any{"data": summary})
}

func (h *Handler) HandleCandidates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		write(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	summary, candidates, err := h.service.Evaluate()
	if err != nil {
		write(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	write(w, http.StatusOK, map[string]any{"data": candidates, "meta": map[string]any{"total": len(candidates), "version": summary.Version, "mode": summary.Mode}})
}

func write(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

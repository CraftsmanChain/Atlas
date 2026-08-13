package prediction

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"atlas/pkg/storage"
	"gorm.io/gorm"
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
	summary, rows, err := h.service.Results(500)
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": rows, "summary": summary})
}

func (h *Handler) HandleFeatureParity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	rows, err := h.service.FeatureParityAudits(100)
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows), "scoring_allowed": false}})
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

func (h *Handler) HandleAccuracy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	summary, err := h.service.Accuracy()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": summary})
}

func (h *Handler) HandleOutcomes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := h.service.Outcomes(100)
		if err != nil {
			predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		predictionJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows)}})
	case http.MethodPost:
		if err := h.service.SyncOutcomes(); err != nil {
			predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		summary, err := h.service.Accuracy()
		if err != nil {
			predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		predictionJSON(w, http.StatusOK, map[string]any{"data": summary})
	default:
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleOutcome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	idText := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/prediction/outcomes/"), "/")
	id64, err := strconv.ParseUint(idText, 10, 64)
	if err != nil || id64 == 0 {
		predictionJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid outcome id"})
		return
	}
	var input OutcomeOverride
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		predictionJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	row, err := h.service.OverrideOutcome(uint(id64), input)
	if err != nil {
		status := http.StatusBadRequest
		if err == gorm.ErrRecordNotFound {
			status = http.StatusNotFound
		}
		predictionJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": row})
}

func predictionJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

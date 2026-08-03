package history

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
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

func (h *Handler) HandleIdentityBackfills(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.IdentityBackfillRuns(limit)
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
		run, err := h.service.StartIdentityBackfill(request)
		if err != nil {
			historyJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusAccepted, map[string]any{"data": run})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleIdentities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	summary, rows, err := h.service.IdentityIntervals(limit)
	if err != nil {
		historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	historyJSON(w, http.StatusOK, map[string]any{"data": rows, "summary": summary})
}

func (h *Handler) HandleDatasets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.DatasetBuilds(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows)}})
	case http.MethodPost:
		var request DatasetBuildRequest
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		build, err := h.service.BuildDatasetManifest(request)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "data": build})
			return
		}
		historyJSON(w, http.StatusCreated, map[string]any{"data": build})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleFeatureDatasets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.FeatureBuilds(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{
			"total": len(rows), "execution": "atlas_deployment_node", "read_only_source": true,
		}})
	case http.MethodPost:
		var request FeatureBuildRequest
		if r.Body != nil && r.ContentLength != 0 {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		build, err := h.service.StartFeatureBuild(request)
		if err != nil {
			historyJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "data": build})
			return
		}
		historyJSON(w, http.StatusAccepted, map[string]any{"data": build})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleTrainingPreparations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.PreparationBuilds(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{
			"total": len(rows), "entity_isolated": true, "time_ordered": true,
		}})
	case http.MethodPost:
		var request PreparationBuildRequest
		if r.Body != nil && r.ContentLength != 0 {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		build, err := h.service.BuildTrainingPreparation(request)
		if err != nil {
			historyJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "data": build})
			return
		}
		historyJSON(w, http.StatusCreated, map[string]any{"data": build})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleControlFeatureDatasets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.ControlFeatureBuilds(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{
			"total": len(rows), "execution": "atlas_deployment_node", "read_only_source": true,
		}})
	case http.MethodPost:
		var request ControlFeatureBuildRequest
		if r.Body != nil && r.ContentLength != 0 {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		build, err := h.service.StartControlFeatureBuild(request)
		if err != nil {
			historyJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "data": build})
			return
		}
		historyJSON(w, http.StatusAccepted, map[string]any{"data": build})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleTrainingMatrices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.TrainingMatrixBuilds(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{
			"total": len(rows), "missing_values": "preserved", "entity_isolated": true,
		}})
	case http.MethodPost:
		var request TrainingMatrixBuildRequest
		if r.Body != nil && r.ContentLength != 0 {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		build, err := h.service.StartTrainingMatrixBuild(request)
		if err != nil {
			historyJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "data": build})
			return
		}
		historyJSON(w, http.StatusAccepted, map[string]any{"data": build})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleCandidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	idText := strings.TrimPrefix(r.URL.Path, "/api/v1/prediction/history/candidates/")
	id64, err := strconv.ParseUint(strings.Trim(idText, "/"), 10, 64)
	if err != nil || id64 == 0 {
		historyJSON(w, http.StatusBadRequest, map[string]any{"error": "valid candidate id is required"})
		return
	}
	var request CandidateReviewRequest
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&request) != nil {
		historyJSON(w, http.StatusBadRequest, map[string]any{"error": "valid JSON body is required"})
		return
	}
	candidate, err := h.service.ReviewCandidate(uint(id64), request)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "record not found") {
			status = http.StatusNotFound
		}
		historyJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	historyJSON(w, http.StatusOK, map[string]any{"data": candidate})
}

func historyJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

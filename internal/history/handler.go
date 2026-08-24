package history

import (
	"encoding/json"
	"fmt"
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

func (h *Handler) HandleFeatureReplays(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.FeatureReplayRuns(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows), "scoring_allowed": false}})
	case http.MethodPost:
		var request FeatureReplayRequest
		if r.Body != nil && r.ContentLength != 0 {
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		run, err := h.service.StartFeatureReplay(request)
		if err != nil {
			historyJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusAccepted, map[string]any{"data": run})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleLiveCoverageAudits(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.LiveCoverageAudits(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows), "scoring_allowed": false}})
	case http.MethodPost:
		var request struct {
			ModelSpecID uint `json:"model_spec_id,omitempty"`
		}
		if r.Body != nil && r.ContentLength != 0 {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		audit, err := h.service.StartLiveCoverageAudit(request.ModelSpecID)
		if err != nil {
			historyJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusAccepted, map[string]any{"data": audit})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleFeatureDistributions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		sourceBaselineBuildID, _ := strconv.ParseUint(r.URL.Query().Get("source_baseline_build_id"), 10, 64)
		modelSpecID, _ := strconv.ParseUint(r.URL.Query().Get("model_spec_id"), 10, 64)
		archive, err := h.service.FeatureDistributionArchiveForQuery(FeatureDistributionSnapshotQuery{
			Limit:                  limit,
			Scope:                  r.URL.Query().Get("scope"),
			SourceBaselineBuildID:  uint(sourceBaselineBuildID),
			ModelSpecID:            uint(modelSpecID),
			DistributionRole:       r.URL.Query().Get("distribution_role"),
			FeatureContractVersion: r.URL.Query().Get("feature_contract_version"),
		})
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("ETag", `"`+archive.ArchiveSHA256+`"`)
		w.Header().Set("X-Atlas-Feature-Distribution-Archive-Version", archive.Version)
		w.Header().Set("X-Atlas-Feature-Distribution-Archive-SHA256", archive.ArchiveSHA256)
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.json"`, archive.Version, archive.ArchiveSHA256[:12]))
			historyJSON(w, http.StatusOK, map[string]any{"data": archive})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{
			"data": archive.Snapshots,
			"meta": map[string]any{
				"total": archive.SnapshotCount, "read_only": true, "raw_samples_stored": false,
				"roles":           []string{"training", "live_shadow"},
				"archive_version": archive.Version, "archive_sha256": archive.ArchiveSHA256,
				"scope": archive.Scope, "blocking_reasons": archive.BlockingReasons,
				"training_snapshot_count": archive.TrainingSnapshotCount, "live_shadow_snapshot_count": archive.LiveShadowSnapshotCount,
				"baseline_count": archive.BaselineCount, "feature_count": archive.FeatureCount, "latest_observed_at": archive.LatestObservedAt,
				"scoring_allowed": archive.ScoringAllowed, "alerts_emitted": archive.AlertsEmitted, "actions_executed": archive.ActionsExecuted,
			},
		})
	case http.MethodPost:
		var request FeatureDistributionSnapshotRequest
		if r.Body != nil && r.ContentLength != 0 {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		rows, err := h.service.MaterializeTrainingFeatureDistributions(request)
		if err != nil {
			historyJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusAccepted, map[string]any{
			"data": rows,
			"meta": map[string]any{
				"total": len(rows), "distribution_role": "training", "raw_samples_stored": false,
				"scoring_allowed": false, "alerts_emitted": false, "actions_executed": false,
			},
		})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleShadowScoringRuns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.ShadowScoringRuns(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows), "mode": "read_only_shadow", "alerts_emitted": false, "actions_executed": false}})
	case http.MethodPost:
		var request shadowScoringRequest
		if r.Body != nil && r.ContentLength != 0 {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		run, err := h.service.StartShadowScoring(request.ModelSpecID)
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

func (h *Handler) HandleCandidateRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	updated, err := h.service.ReapplyHistoricalCandidateRules()
	if err != nil {
		historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	historyJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"rule_decision_version": historicalRuleDecisionVersion, "updated": updated}})
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

func (h *Handler) HandleManualFeedbackFeatureRequests(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.ManualFeedbackFeatureRequestBuilds(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{
			"total": len(rows), "execution": "atlas_deployment_node", "read_only_source": true, "raw_telemetry_stored": false,
		}})
	case http.MethodPost:
		var request ManualFeedbackFeatureRequestBuildRequest
		if r.Body != nil && r.ContentLength != 0 {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		build, err := h.service.BuildManualFeedbackFeatureRequestManifest(request)
		if err != nil {
			historyJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "data": build})
			return
		}
		historyJSON(w, http.StatusCreated, map[string]any{"data": build})
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

func (h *Handler) HandleTrainingMatrix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/prediction/history/training-matrices/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "readiness" {
		historyJSON(w, http.StatusNotFound, map[string]any{"error": "training matrix readiness not found"})
		return
	}
	id, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || id == 0 {
		historyJSON(w, http.StatusBadRequest, map[string]any{"error": "valid training matrix id is required"})
		return
	}
	report, err := h.service.TrainingMatrixReadiness(uint(id))
	if err != nil {
		historyJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	historyJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) HandleBaselineModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := h.service.BaselineModelBuilds(limit)
		if err != nil {
			historyJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		historyJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows), "mode": "offline_evaluation_only", "probability_emitted": false}})
	case http.MethodPost:
		var request BaselineModelBuildRequest
		if r.Body != nil && r.ContentLength != 0 {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&request) != nil {
				historyJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		build, err := h.service.StartBaselineModelBuild(request)
		if err != nil {
			historyJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "data": build})
			return
		}
		historyJSON(w, http.StatusAccepted, map[string]any{"data": build})
	default:
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleBaselineModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		historyJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/prediction/history/baseline-models/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "report" {
		historyJSON(w, http.StatusNotFound, map[string]any{"error": "baseline report not found"})
		return
	}
	id, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || id == 0 {
		historyJSON(w, http.StatusBadRequest, map[string]any{"error": "valid baseline model id is required"})
		return
	}
	report, err := h.service.BaselineModelReport(uint(id))
	if err != nil {
		historyJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	historyJSON(w, http.StatusOK, map[string]any{"data": report})
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

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

// This audit view exposes installation segments separately from the latest
// snapshot view. It never reconciles a roster or queries a device.
func (h *Handler) HandleInstallationRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if values, ok := r.URL.Query()["scope_id"]; ok && (len(values) != 1 || values[0] == "" || len(values[0]) > 128) {
		writeError(w, http.StatusBadRequest, "one scope_id required")
		return
	}
	scopeID := r.URL.Query().Get("scope_id")
	limit, offset := pagination(r, 100, 2000)
	scopes, installations, total, err := h.service.installationRegistry(scopeID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// uint64 versions use decimal strings in read contracts, like evidence counters.
	scopeViews := make([]map[string]any, 0, len(scopes))
	for _, scope := range scopes {
		scopeViews = append(scopeViews, map[string]any{"scope_id": scope.ScopeID, "asset_key": scope.AssetKey, "asset_identity_sha256": scope.AssetIdentitySHA256, "snapshot_scope": scope.SnapshotScope, "snapshot_generation": scope.SnapshotGeneration, "observed_at": scope.ObservedAt, "available_at": scope.AvailableAt, "host_generation": scope.HostGeneration, "node": scope.Node, "disposition": scope.Disposition, "roster_version": strconv.FormatUint(scope.RosterVersion, 10), "host_identity_evidence_sha256": scope.HostIdentityEvidenceSHA256, "manifest_sha256": scope.ManifestSHA256, "reconciled_at": scope.ReconciledAt})
	}
	segmentViews := make([]map[string]any, 0, len(installations))
	for _, installation := range installations {
		item := map[string]any{"installation_id": installation.InstallationID, "scope_id": installation.ScopeID, "entity_id": installation.EntityID, "state": installation.State, "opened_at": installation.OpenedAt, "closed_at": installation.ClosedAt, "open_evidence_sha256": installation.OpenEvidenceSHA256, "close_evidence_sha256": installation.CloseEvidenceSHA256, "close_reason": installation.CloseReason, "open_roster_version": strconv.FormatUint(installation.OpenRosterVersion, 10)}
		if installation.CloseRosterVersion != nil {
			item["close_roster_version"] = strconv.FormatUint(*installation.CloseRosterVersion, 10)
		}
		segmentViews = append(segmentViews, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"contract_version": InstallationViewContract, "data": segmentViews, "scopes": scopeViews, "meta": map[string]any{"total": total, "limit": limit, "offset": offset}})
}

func (h *Handler) HandleDisks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	values, hasView := r.URL.Query()["view"]
	if hasView && (len(values) != 1 || values[0] == "") {
		writeError(w, http.StatusBadRequest, "one nonempty view required")
		return
	}
	view, err := normalizeInstallationView(r.URL.Query().Get("view"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, offset := pagination(r, 100, 2000)
	options := ListOptions{
		View:             view,
		Limit:            limit,
		Offset:           offset,
		Query:            r.URL.Query().Get("q"),
		Level:            r.URL.Query().Get("level"),
		AssessmentStatus: r.URL.Query().Get("assessment_status"),
		Protocol:         r.URL.Query().Get("protocol"),
		MediaType:        r.URL.Query().Get("media_type"),
		Order:            r.URL.Query().Get("order"),
	}
	result, err := h.service.ListDisks(options)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":    result.Data,
		"summary": result.Summary,
		"meta": map[string]any{
			"total":        result.Total,
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

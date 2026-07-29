package nodeaccess

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

type Handler struct {
	service      *Service
	vault        *CredentialVault
	connectivity *ConnectivityService
	collections  *CollectionService
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) SetConnectivity(service *ConnectivityService) { h.connectivity = service }
func (h *Handler) SetCollections(service *CollectionService)    { h.collections = service }

func NewHandlerWithVault(service *Service, vault *CredentialVault) *Handler {
	return &Handler{service: service, vault: vault}
}

func (h *Handler) HandleCollections(w http.ResponseWriter, r *http.Request) {
	if h.collections == nil {
		writeNodeAccessError(w, http.StatusServiceUnavailable, "node evidence collection is unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		eventID, err := positiveUintQuery(r, "fault_event_id")
		if err != nil {
			writeNodeAccessError(w, http.StatusBadRequest, "invalid fault event id")
			return
		}
		limit := 5
		if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed < 1 || parsed > 100 {
				writeNodeAccessError(w, http.StatusBadRequest, "limit must be between 1 and 100")
				return
			}
			limit = parsed
		}
		includeHistory := false
		switch value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("history"))); value {
		case "", "0", "false":
		case "1", "true":
			includeHistory = true
		default:
			writeNodeAccessError(w, http.StatusBadRequest, "history must be true or false")
			return
		}
		var rows []api.NodeEvidenceCollection
		if includeHistory {
			rows, err = h.collections.List(eventID, limit+1)
		} else {
			rows, err = h.collections.ListCurrent(eventID, limit+1)
		}
		if err != nil {
			writeNodeAccessError(w, http.StatusInternalServerError, "failed to list node evidence collections")
			return
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		writeNodeAccessJSON(w, http.StatusOK, map[string]any{
			"data": rows,
			"meta": map[string]any{"limit": limit, "has_more": hasMore, "history": includeHistory},
		})
	case http.MethodPost:
		var request struct {
			FaultEventID      uint `json:"fault_event_id"`
			RetryCollectionID uint `json:"retry_collection_id"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil ||
			(request.FaultEventID == 0) == (request.RetryCollectionID == 0) {
			writeNodeAccessError(w, http.StatusBadRequest, "invalid node evidence collection")
			return
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeNodeAccessError(w, http.StatusBadRequest, "invalid node evidence collection")
			return
		}
		var record any
		var err error
		if request.RetryCollectionID > 0 {
			record, err = h.collections.Retry(r.Context(), request.RetryCollectionID)
		} else {
			record, err = h.collections.Collect(r.Context(), request.FaultEventID)
		}
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, ErrFaultEventNotFound), errors.Is(err, ErrEvidenceCollectionNotFound), errors.Is(err, ErrNodeNotManaged):
				status = http.StatusNotFound
			case errors.Is(err, ErrEvidenceCollectionNotRetryable):
				status = http.StatusConflict
			case errors.Is(err, ErrConnectivityUnavailable):
				status = http.StatusServiceUnavailable
			}
			writeNodeAccessError(w, status, err.Error())
			return
		}
		writeNodeAccessJSON(w, http.StatusCreated, map[string]any{"data": record})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeNodeAccessError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) HandleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeNodeAccessError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	overview := h.service.Overview()
	overview.ManagementReady = h.vault != nil
	overview.SecureWriteOnly = false
	overview.InsecureHTTPAllowed = true
	overview.ConnectivityEnabled = h.connectivity != nil && h.connectivity.Enabled()
	overview.KnownHostsReady = h.connectivity != nil && h.connectivity.KnownHostsReady()
	overview.ExecutionEnabled = h.collections != nil && h.collections.Enabled()
	if h.collections != nil {
		if summary, err := h.collections.CurrentSummary(); err == nil {
			overview.CollectionSummary = summary
		}
	}
	if overview.Enabled && !overview.KnownHostsReady {
		overview.Status = "known_hosts_missing"
	} else if overview.ExecutionEnabled {
		overview.Status = "readonly_collection_ready"
	} else if overview.Enabled && overview.KnownHostsReady && overview.Status == "ready_no_transport" {
		overview.Status = "connectivity_ready"
	}
	writeNodeAccessJSON(w, http.StatusOK, map[string]any{"data": overview})
}

func (h *Handler) HandleChecks(w http.ResponseWriter, r *http.Request) {
	if h.connectivity == nil {
		writeNodeAccessError(w, http.StatusServiceUnavailable, "node connectivity checks are unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit := 5
		if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 100 {
				writeNodeAccessError(w, http.StatusBadRequest, "limit must be between 1 and 100")
				return
			}
			limit = parsed
		}
		rows, err := h.connectivity.List(limit + 1)
		if err != nil {
			writeNodeAccessError(w, http.StatusInternalServerError, "failed to list node connectivity checks")
			return
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		writeNodeAccessJSON(w, http.StatusOK, map[string]any{
			"data": rows,
			"meta": map[string]any{"limit": limit, "has_more": hasMore},
		})
	case http.MethodPost:
		var request struct {
			NodeIP string `json:"node_ip"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeNodeAccessError(w, http.StatusBadRequest, "invalid node connectivity check")
			return
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeNodeAccessError(w, http.StatusBadRequest, "invalid node connectivity check")
			return
		}
		record, err := h.connectivity.Check(r.Context(), request.NodeIP)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, ErrNodeNotManaged):
				status = http.StatusNotFound
			case errors.Is(err, ErrNodeAccessDisabled), errors.Is(err, ErrConnectivityUnavailable):
				status = http.StatusServiceUnavailable
			}
			writeNodeAccessError(w, status, err.Error())
			return
		}
		writeNodeAccessJSON(w, http.StatusOK, map[string]any{"data": record})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeNodeAccessError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) HandleCredentials(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listCredentials(w)
	case http.MethodPost:
		h.createCredential(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeNodeAccessError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) HandleCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		writeNodeAccessError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	profileID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/node-access/credentials/"), "/")
	if profileID == "" || strings.Contains(profileID, "/") {
		writeNodeAccessError(w, http.StatusBadRequest, "invalid credential profile id")
		return
	}
	if h.vault == nil {
		writeNodeAccessError(w, http.StatusServiceUnavailable, "credential encryption is not configured")
		return
	}
	if err := h.vault.Delete(profileID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeNodeAccessError(w, http.StatusNotFound, "credential profile not found")
			return
		}
		writeNodeAccessError(w, http.StatusInternalServerError, "failed to delete credential profile")
		return
	}
	writeNodeAccessJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"profile_id": profileID, "deleted": true}})
}

func (h *Handler) listCredentials(w http.ResponseWriter) {
	if h.vault == nil {
		writeNodeAccessJSON(w, http.StatusOK, map[string]any{"data": []any{}, "encryption_ready": false})
		return
	}
	rows, err := h.vault.List()
	if err != nil {
		writeNodeAccessError(w, http.StatusInternalServerError, "failed to list credential profiles")
		return
	}
	writeNodeAccessJSON(w, http.StatusOK, map[string]any{"data": rows, "encryption_ready": true})
}

type createCredentialRequest struct {
	ProfileID string `json:"profile_id"`
	Priority  int    `json:"priority"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Enabled   bool   `json:"enabled"`
}

func (h *Handler) createCredential(w http.ResponseWriter, r *http.Request) {
	if h.vault == nil {
		writeNodeAccessError(w, http.StatusServiceUnavailable, "credential encryption is not configured")
		return
	}
	var request createCredentialRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeNodeAccessError(w, http.StatusBadRequest, "invalid credential profile")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeNodeAccessError(w, http.StatusBadRequest, "invalid credential profile")
		return
	}
	record, err := h.vault.Create(CredentialInput{
		ProfileID: request.ProfileID, Priority: request.Priority, Username: request.Username,
		Password: request.Password, Enabled: request.Enabled,
	})
	request.Username, request.Password = "", ""
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(strings.ToLower(err.Error()), "unique") {
			status = http.StatusConflict
		}
		writeNodeAccessError(w, status, err.Error())
		return
	}
	writeNodeAccessJSON(w, http.StatusCreated, map[string]any{"data": record})
}

func writeNodeAccessJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeNodeAccessError(w http.ResponseWriter, status int, message string) {
	writeNodeAccessJSON(w, status, map[string]any{"error": map[string]any{"status": status, "message": message}})
}

func positiveUintQuery(r *http.Request, name string) (uint, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return 0, errors.New("invalid positive integer")
	}
	return uint(parsed), nil
}

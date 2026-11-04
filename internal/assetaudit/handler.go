package assetaudit

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// Handler exposes the asset audit configuration, recent runs and manual
// triggers. Reads are open like the rest of the Atlas read-only APIs; writes
// and triggers require HTTPS (or the approved HTTP compatibility mode) and the
// management token.
type Handler struct {
	service           *Service
	adminToken        string
	allowInsecureHTTP bool
}

func NewHandler(service *Service, adminToken string, allowInsecureHTTP bool) *Handler {
	return &Handler{service: service, adminToken: strings.TrimSpace(adminToken), allowInsecureHTTP: allowInsecureHTTP}
}

// runView is the API projection of a run, with summary and findings embedded as
// JSON so the UI can render them without a second round trip.
type runView struct {
	ID           uint            `json:"id"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	Trigger      string          `json:"trigger"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
	Summary      json.RawMessage `json:"summary,omitempty"`
	Findings     json.RawMessage `json:"findings,omitempty"`
	Notified     bool            `json:"notified"`
	NotifyError  string          `json:"notify_error,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
}

func toRunView(run api.AssetAuditRun) runView {
	view := runView{
		ID: run.ID, Kind: run.Kind, Status: run.Status, Trigger: run.Trigger,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Notified: run.Notified,
		NotifyError: run.NotifyError, ErrorMessage: run.ErrorMessage,
	}
	if strings.TrimSpace(run.SummaryJSON) != "" {
		view.Summary = json.RawMessage(run.SummaryJSON)
	}
	if strings.TrimSpace(run.FindingsJSON) != "" {
		view.Findings = json.RawMessage(run.FindingsJSON)
	}
	return view
}

// HandleConfig serves GET/PUT of the singleton configuration.
func (h *Handler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "asset audit is unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		row, err := h.service.GetConfig()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"data": ConfigView{ScheduleHour: 5, Timezone: "Asia/Shanghai", ScanConcurrency: 1, ScanTimeoutMS: 1000}})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read asset audit configuration")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": configView(row)})
	case http.MethodPut:
		if !h.authorize(w, r) {
			return
		}
		var input ConfigInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid asset audit configuration")
			return
		}
		row, err := h.service.SaveConfig(input)
		input.FeishuAppSecret, input.NotifySecret, input.SSHPasswords = "", "", ""
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": configView(row)})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleRuns lists recent runs, optionally filtered by kind.
func (h *Handler) HandleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "asset audit is unavailable")
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && kind != KindDelivery && kind != KindScan {
		writeError(w, http.StatusBadRequest, "unsupported kind")
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}
	query := h.service.db.Model(&api.AssetAuditRun{}).Order("started_at DESC").Limit(limit)
	if kind != "" {
		query = query.Where("kind = ?", kind)
	}
	var runs []api.AssetAuditRun
	if err := query.Find(&runs).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read asset audit runs")
		return
	}
	views := make([]runView, 0, len(runs))
	for _, run := range runs {
		views = append(views, toRunView(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": views})
}

type triggerRequest struct {
	Kind string `json:"kind"`
}

// HandleTrigger starts one run immediately.
func (h *Handler) HandleTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "asset audit is unavailable")
		return
	}
	if !h.authorize(w, r) {
		return
	}
	var request triggerRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid trigger request")
		return
	}
	run, err := h.service.Trigger(r.Context(), strings.TrimSpace(request.Kind), triggerManual)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"data": toRunView(run)})
}

// marksRequest is the body of a mark or unmark call.
type marksRequest struct {
	IPAddresses []string `json:"ip_addresses"`
	Reason      string   `json:"reason"`
}

// HandleMarks serves the marked live-IP list: GET lists every marked address,
// POST adds addresses, DELETE removes them. Writes require the management
// token. A marked address is reported as its own scan category and stays
// outside the ICMP-alive / port-22 / SSH-login-failed counts.
func (h *Handler) HandleMarks(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "asset audit is unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		marks, err := h.service.ListMarks()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read marked IPs")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": marks})
	case http.MethodPost, http.MethodDelete:
		if !h.authorize(w, r) {
			return
		}
		var input marksRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid mark request")
			return
		}
		if r.Method == http.MethodPost {
			count, err := h.service.MarkAddresses(input.IPAddresses, input.Reason)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to mark IPs")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"marked": count}})
			return
		}
		count, err := h.service.UnmarkAddresses(input.IPAddresses)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to unmark IPs")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"unmarked": count}})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if r.TLS == nil && !h.allowInsecureHTTP {
		writeError(w, http.StatusUpgradeRequired, "asset audit writes require HTTPS or approved HTTP compatibility mode")
		return false
	}
	provided := r.Header.Get("X-Atlas-Admin-Token")
	if h.adminToken == "" || len(provided) != len(h.adminToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(h.adminToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid management credential")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"status": status, "message": message}})
}

package nodeaccess

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"gorm.io/gorm"
)

type Handler struct {
	service           *Service
	vault             *CredentialVault
	adminToken        string
	allowLoopbackHTTP bool
	allowInsecureHTTP bool
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func NewHandlerWithVault(service *Service, vault *CredentialVault, adminToken string, allowLoopbackHTTP ...bool) *Handler {
	handler := &Handler{service: service, vault: vault, adminToken: strings.TrimSpace(adminToken)}
	if len(allowLoopbackHTTP) > 0 {
		handler.allowLoopbackHTTP = allowLoopbackHTTP[0]
	}
	if len(allowLoopbackHTTP) > 1 {
		handler.allowInsecureHTTP = allowLoopbackHTTP[1]
	}
	return handler
}

func (h *Handler) HandleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeNodeAccessError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	overview := h.service.Overview()
	overview.ManagementReady = h.vault != nil && h.adminToken != ""
	overview.SecureWriteOnly = !h.allowInsecureHTTP
	overview.InsecureHTTPAllowed = h.allowInsecureHTTP
	writeNodeAccessJSON(w, http.StatusOK, map[string]any{"data": overview})
}

func (h *Handler) HandleCredentials(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listCredentials(w)
	case http.MethodPost:
		if !h.secureWriteTransport(r) {
			writeNodeAccessError(w, http.StatusUpgradeRequired, "credential writes require HTTPS or an approved loopback connection")
			return
		}
		if !h.authorized(r) {
			writeNodeAccessError(w, http.StatusUnauthorized, "invalid management credential")
			return
		}
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
	if !h.secureWriteTransport(r) {
		writeNodeAccessError(w, http.StatusUpgradeRequired, "credential writes require HTTPS or an approved loopback connection")
		return
	}
	if !h.authorized(r) {
		writeNodeAccessError(w, http.StatusUnauthorized, "invalid management credential")
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

func (h *Handler) authorized(r *http.Request) bool {
	if h.vault == nil || h.adminToken == "" {
		return false
	}
	provided := r.Header.Get("X-Atlas-Admin-Token")
	if len(provided) != len(h.adminToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(h.adminToken)) == 1
}

func (h *Handler) secureWriteTransport(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if h.allowInsecureHTTP {
		return true
	}
	if !h.allowLoopbackHTTP {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
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

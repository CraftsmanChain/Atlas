package platformconfig

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	promclient "atlas/internal/prometheus"
	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

// MonitoringSource persists the singleton monitoring data-source address in the
// database and applies it to the live client, so an operator can repoint Atlas
// at a moved Prometheus/VictoriaMetrics endpoint from the UI without a
// redeploy. The configuration-file address remains the first-run fallback.
type MonitoringSource struct {
	db              *storage.DB
	fallbackURL     string
	fallbackTimeout string
	apply           func(baseURL string, timeout time.Duration) error
}

func NewMonitoringSource(db *storage.DB, fallbackURL, fallbackTimeout string) *MonitoringSource {
	return &MonitoringSource{
		db:              db,
		fallbackURL:     strings.TrimSpace(fallbackURL),
		fallbackTimeout: strings.TrimSpace(fallbackTimeout),
	}
}

// SetApplier wires the live client so a saved address takes effect immediately.
func (s *MonitoringSource) SetApplier(apply func(baseURL string, timeout time.Duration) error) {
	s.apply = apply
}

// MonitoringConfigView is the credential-free projection returned to the UI.
type MonitoringConfigView struct {
	BaseURL        string    `json:"base_url"`
	RequestTimeout string    `json:"request_timeout"`
	Configured     bool      `json:"configured"`
	EffectiveURL   string    `json:"effective_url"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// MonitoringConfigInput is the write payload.
type MonitoringConfigInput struct {
	BaseURL        string `json:"base_url"`
	RequestTimeout string `json:"request_timeout"`
}

func monitoringView(row api.MonitoringConfig) MonitoringConfigView {
	return MonitoringConfigView{
		BaseURL:        row.BaseURL,
		RequestTimeout: row.RequestTimeout,
		Configured:     strings.TrimSpace(row.BaseURL) != "",
		EffectiveURL:   strings.TrimSpace(row.BaseURL),
		UpdatedAt:      row.UpdatedAt,
	}
}

// ensureConfig returns the persisted singleton row, creating it from the
// deployment fallback the first time Atlas starts so a fresh cluster is
// preconfigured. It never overwrites an existing row.
func (s *MonitoringSource) ensureConfig() (api.MonitoringConfig, error) {
	var row api.MonitoringConfig
	err := s.db.First(&row, singletonID).Error
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return row, err
	}
	row = api.MonitoringConfig{ID: singletonID, BaseURL: s.fallbackURL, RequestTimeout: normaliseTimeout(s.fallbackTimeout)}
	if err := s.db.Create(&row).Error; err != nil {
		// A concurrent first start may have inserted the row already.
		var existing api.MonitoringConfig
		if retryErr := s.db.First(&existing, singletonID).Error; retryErr == nil {
			return existing, nil
		}
		return row, err
	}
	return row, nil
}

// GetConfig returns the persisted singleton row, seeding it when absent.
func (s *MonitoringSource) GetConfig() (api.MonitoringConfig, error) {
	if s == nil || s.db == nil {
		return api.MonitoringConfig{}, errors.New("monitoring configuration database is unavailable")
	}
	return s.ensureConfig()
}

// Resolve returns the address and timeout Atlas should currently use.
func (s *MonitoringSource) Resolve() (string, time.Duration, error) {
	row, err := s.GetConfig()
	if err != nil {
		return "", 0, err
	}
	baseURL := strings.TrimSpace(row.BaseURL)
	if baseURL == "" {
		baseURL = s.fallbackURL
	}
	timeout, err := time.ParseDuration(normaliseTimeout(row.RequestTimeout))
	if err != nil || timeout <= 0 {
		timeout = 15 * time.Second
	}
	return baseURL, timeout, nil
}

// SaveConfig validates the operator-entered address, persists it, and applies it
// to the live client immediately.
func (s *MonitoringSource) SaveConfig(input MonitoringConfigInput) (api.MonitoringConfig, error) {
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	if input.BaseURL == "" {
		return api.MonitoringConfig{}, errors.New("base_url is required")
	}
	if err := validateMonitoringURL(input.BaseURL); err != nil {
		return api.MonitoringConfig{}, err
	}
	timeout := normaliseTimeout(input.RequestTimeout)
	parsedTimeout, err := time.ParseDuration(timeout)
	if err != nil || parsedTimeout <= 0 {
		return api.MonitoringConfig{}, errors.New("request_timeout must be a positive duration such as 15s")
	}
	row, err := s.ensureConfig()
	if err != nil {
		return row, err
	}
	row.BaseURL = input.BaseURL
	row.RequestTimeout = timeout
	if err := s.db.Save(&row).Error; err != nil {
		return row, err
	}
	if s.apply != nil {
		if err := s.apply(row.BaseURL, parsedTimeout); err != nil {
			return row, fmt.Errorf("monitoring address saved but could not be applied: %w", err)
		}
	}
	return row, nil
}

// Apply points the live client at the stored address; used at startup.
func (s *MonitoringSource) Apply() error {
	if s == nil || s.apply == nil {
		return nil
	}
	baseURL, timeout, err := s.Resolve()
	if err != nil {
		return err
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil
	}
	return s.apply(baseURL, timeout)
}

func validateMonitoringURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("base_url must be an absolute HTTP(S) URL")
	}
	return nil
}

func normaliseTimeout(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "15s"
	}
	return value
}

// MonitoringConfigHandler serves GET/PUT of the monitoring data source
// configuration and a POST connection probe. Reads are open like the rest of
// the Atlas read-only APIs; writes and probes require HTTPS (or the approved
// HTTP compatibility mode) and the management token.
type MonitoringConfigHandler struct {
	source            *MonitoringSource
	adminToken        string
	allowInsecureHTTP bool
}

func NewMonitoringConfigHandler(source *MonitoringSource, adminToken string, allowInsecureHTTP bool) *MonitoringConfigHandler {
	return &MonitoringConfigHandler{source: source, adminToken: strings.TrimSpace(adminToken), allowInsecureHTTP: allowInsecureHTTP}
}

func (h *MonitoringConfigHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if h.source == nil {
		writeError(w, http.StatusServiceUnavailable, "monitoring configuration is unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		row, err := h.source.GetConfig()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read monitoring configuration")
			return
		}
		view := monitoringView(row)
		if effective, _, resolveErr := h.source.Resolve(); resolveErr == nil {
			view.EffectiveURL = effective
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": view})
	case http.MethodPut:
		if !h.authorize(w, r) {
			return
		}
		var input MonitoringConfigInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid monitoring configuration")
			return
		}
		row, err := h.source.SaveConfig(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		view := monitoringView(row)
		view.EffectiveURL = strings.TrimSpace(row.BaseURL)
		writeJSON(w, http.StatusOK, map[string]any{"data": view})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleTest performs a read-only probe against the supplied address (or the
// stored one when the body is empty) so an operator can confirm a new address
// before saving it.
func (h *MonitoringConfigHandler) HandleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.source == nil {
		writeError(w, http.StatusServiceUnavailable, "monitoring configuration is unavailable")
		return
	}
	if !h.authorize(w, r) {
		return
	}
	var input MonitoringConfigInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid monitoring probe request")
		return
	}
	baseURL := strings.TrimSpace(input.BaseURL)
	timeout := normaliseTimeout(input.RequestTimeout)
	if baseURL == "" {
		stored, _, resolveErr := h.source.Resolve()
		if resolveErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to read monitoring configuration")
			return
		}
		baseURL = stored
	}
	if err := validateMonitoringURL(baseURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	parsedTimeout, err := time.ParseDuration(timeout)
	if err != nil || parsedTimeout <= 0 {
		parsedTimeout = 15 * time.Second
	}
	client, err := promclient.NewClient(baseURL, parsedTimeout)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), parsedTimeout+5*time.Second)
	defer cancel()
	targets, err := client.ActiveTargets(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	up := 0
	for _, target := range targets {
		if strings.EqualFold(target.Health, "up") {
			up++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"ok": true, "base_url": baseURL, "targets": len(targets), "up": up}})
}

func (h *MonitoringConfigHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if r.TLS == nil && !h.allowInsecureHTTP {
		writeError(w, http.StatusUpgradeRequired, "monitoring configuration writes require HTTPS or approved HTTP compatibility mode")
		return false
	}
	provided := r.Header.Get("X-Atlas-Admin-Token")
	if h.adminToken == "" || len(provided) != len(h.adminToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(h.adminToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid management credential")
		return false
	}
	return true
}

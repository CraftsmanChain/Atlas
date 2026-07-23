package platformconfig

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

const singletonID uint = 1

type Handler struct {
	db       *storage.DB
	defaults config.BrandingConfig
}

func NewHandler(db *storage.DB, defaults config.BrandingConfig) *Handler {
	return &Handler{db: db, defaults: defaults}
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := h.getOrCreate()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": settings})
	case http.MethodPut:
		h.update(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) getOrCreate() (*api.PlatformDisplayConfig, error) {
	var settings api.PlatformDisplayConfig
	err := h.db.First(&settings, singletonID).Error
	if err == nil {
		return &settings, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	settings = api.PlatformDisplayConfig{
		ID:             singletonID,
		InstanceName:   defaultValue(h.defaults.InstanceName, "Atlas Cluster"),
		ProductName:    defaultValue(h.defaults.ProductName, "ATLAS"),
		ProductTagline: defaultValue(h.defaults.ProductTagline, "GPU RELIABILITY"),
		Environment:    defaultValue(h.defaults.Environment, "LOCAL"),
	}
	if err := h.db.Create(&settings).Error; err != nil {
		// A concurrent first request may have inserted the singleton.
		if retryErr := h.db.First(&settings, singletonID).Error; retryErr == nil {
			return &settings, nil
		}
		return nil, err
	}
	return &settings, nil
}

type updateRequest struct {
	InstanceName   string `json:"instance_name"`
	ProductName    string `json:"product_name"`
	ProductTagline string `json:"product_tagline"`
	Environment    string `json:"environment"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var request updateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid platform config: "+err.Error())
		return
	}
	if err := ensureEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "invalid platform config: "+err.Error())
		return
	}
	request.InstanceName = strings.TrimSpace(request.InstanceName)
	request.ProductName = strings.TrimSpace(request.ProductName)
	request.ProductTagline = strings.TrimSpace(request.ProductTagline)
	request.Environment = strings.TrimSpace(request.Environment)
	if message := validate(request); message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}
	settings, err := h.getOrCreate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings.InstanceName = request.InstanceName
	settings.ProductName = request.ProductName
	settings.ProductTagline = request.ProductTagline
	settings.Environment = request.Environment
	if err := h.db.Save(settings).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": settings})
}

func validate(request updateRequest) string {
	fields := []struct {
		label string
		value string
		max   int
	}{
		{"instance_name", request.InstanceName, 80},
		{"product_name", request.ProductName, 40},
		{"product_tagline", request.ProductTagline, 80},
		{"environment", request.Environment, 40},
	}
	for _, field := range fields {
		if field.value == "" {
			return field.label + " is required"
		}
		if len([]rune(field.value)) > field.max {
			return field.label + " is too long"
		}
	}
	return ""
}

func defaultValue(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"status": status, "message": message}})
}

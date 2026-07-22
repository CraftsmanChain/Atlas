package features

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

type Handler struct{ db *storage.DB }

func NewHandler(db *storage.DB) *Handler { return &Handler{db: db} }

func (h *Handler) HandleCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		definitions, err := List(h.db, ListOptions{Domain: r.URL.Query().Get("domain"), Status: r.URL.Query().Get("status"), Purpose: r.URL.Query().Get("purpose")})
		if err != nil {
			writeFeatureError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeFeatureJSON(w, http.StatusOK, map[string]any{"data": definitions, "meta": map[string]any{"total": len(definitions), "catalog_version": CatalogVersion}})
	case http.MethodPost:
		var definition api.FeatureDefinition
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&definition); err != nil {
			writeFeatureError(w, http.StatusBadRequest, "invalid feature definition: "+err.Error())
			return
		}
		definition.ID = 0
		if err := Register(h.db, &definition); err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(strings.ToLower(err.Error()), "unique") {
				writeFeatureError(w, http.StatusConflict, "feature name and version already exist")
				return
			}
			writeFeatureError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeFeatureJSON(w, http.StatusCreated, map[string]any{"data": definition})
	default:
		writeFeatureError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) HandleItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeFeatureError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/features/"), "/")
	if name == "" || strings.Contains(name, "/") {
		writeFeatureError(w, http.StatusBadRequest, "feature name is required")
		return
	}
	definition, err := Get(h.db, name, strings.TrimSpace(r.URL.Query().Get("version")))
	if err != nil {
		writeFeatureError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if definition == nil {
		writeFeatureError(w, http.StatusNotFound, "feature not found")
		return
	}
	writeFeatureJSON(w, http.StatusOK, map[string]any{"data": definition})
}

func writeFeatureJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeFeatureError(w http.ResponseWriter, status int, message string) {
	writeFeatureJSON(w, status, map[string]any{"error": map[string]any{"status": status, "message": message}})
}

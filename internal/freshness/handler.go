package freshness

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

type SourceStatus struct {
	Status            string     `json:"status"`
	ObservedAt        *time.Time `json:"observed_at,omitempty"`
	AgeSeconds        *int64     `json:"age_seconds,omitempty"`
	StaleAfterSeconds int64      `json:"stale_after_seconds"`
	SourceMode        string     `json:"source_mode,omitempty"`
	Message           string     `json:"message,omitempty"`
}

type Response struct {
	OverallStatus string                  `json:"overall_status"`
	ServerTime    time.Time               `json:"server_time"`
	Sources       map[string]SourceStatus `json:"sources"`
}

type Handler struct {
	db                  *storage.DB
	ingestionDB         *storage.DB
	ingestionMode       string
	ingestionStaleAfter time.Duration
	inventoryStaleAfter time.Duration
	healthStaleAfter    time.Duration
	now                 func() time.Time
}

func NewHandler(db, ingestionDB *storage.DB, ingestionMode string, ingestionStaleAfter, inventoryStaleAfter, healthStaleAfter time.Duration) *Handler {
	if ingestionDB == nil {
		ingestionDB = db
	}
	return &Handler{
		db: db, ingestionDB: ingestionDB, ingestionMode: strings.TrimSpace(ingestionMode),
		ingestionStaleAfter: positiveDuration(ingestionStaleAfter, 15*time.Minute),
		inventoryStaleAfter: positiveDuration(inventoryStaleAfter, 20*time.Minute),
		healthStaleAfter:    positiveDuration(healthStaleAfter, time.Hour), now: time.Now,
	}
}

func positiveDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func Classify(now time.Time, observedAt *time.Time, staleAfter time.Duration) SourceStatus {
	result := SourceStatus{Status: "empty", ObservedAt: observedAt, StaleAfterSeconds: int64(staleAfter.Seconds())}
	if observedAt == nil {
		return result
	}
	age := now.Sub(*observedAt)
	if age < 0 {
		age = 0
	}
	ageSeconds := int64(age.Seconds())
	result.AgeSeconds = &ageSeconds
	result.Status = "fresh"
	if age > staleAfter {
		result.Status = "stale"
	}
	return result
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	now := h.now()
	sources := map[string]SourceStatus{}

	var ingestion api.AlertIngestionRecord
	result := h.ingestionDB.Select("created_at").Order("created_at DESC").Order("id DESC").Limit(1).Find(&ingestion)
	if result.Error != nil {
		sources["ingestion"] = errorStatus(h.ingestionStaleAfter, result.Error.Error())
	} else {
		var observed *time.Time
		if result.RowsAffected > 0 {
			observed = &ingestion.CreatedAt
		}
		status := Classify(now, observed, h.ingestionStaleAfter)
		status.SourceMode = h.ingestionMode
		if strings.EqualFold(h.ingestionMode, "snapshot") && observed != nil {
			status.Status = "snapshot"
		}
		sources["ingestion"] = status
	}

	sources["inventory"] = h.latestInventory(now)
	sources["health"] = h.latestHealth(now)
	response := Response{OverallStatus: overallStatus(sources), ServerTime: now, Sources: sources}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) latestInventory(now time.Time) SourceStatus {
	var run api.InventorySyncRun
	result := h.db.Where("status = ?", "success").Order("finished_at DESC").Order("id DESC").Limit(1).Find(&run)
	if result.Error != nil {
		return errorStatus(h.inventoryStaleAfter, result.Error.Error())
	}
	if result.RowsAffected == 0 {
		return Classify(now, nil, h.inventoryStaleAfter)
	}
	return Classify(now, run.FinishedAt, h.inventoryStaleAfter)
}

func (h *Handler) latestHealth(now time.Time) SourceStatus {
	var run api.HealthEvaluationRun
	result := h.db.Where("status = ?", "success").Order("finished_at DESC").Order("id DESC").Limit(1).Find(&run)
	if result.Error != nil {
		return errorStatus(h.healthStaleAfter, result.Error.Error())
	}
	if result.RowsAffected == 0 {
		return Classify(now, nil, h.healthStaleAfter)
	}
	return Classify(now, run.FinishedAt, h.healthStaleAfter)
}

func errorStatus(staleAfter time.Duration, message string) SourceStatus {
	return SourceStatus{Status: "error", StaleAfterSeconds: int64(staleAfter.Seconds()), Message: message}
}

func overallStatus(sources map[string]SourceStatus) string {
	nonFresh := false
	nonEmpty := false
	for _, source := range sources {
		switch source.Status {
		case "error":
			return "error"
		case "stale":
			return "stale"
		case "fresh", "snapshot":
			nonEmpty = true
		default:
			nonFresh = true
		}
	}
	if !nonEmpty {
		return "empty"
	}
	if nonFresh {
		return "partial"
	}
	return "fresh"
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

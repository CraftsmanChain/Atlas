package prediction

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"atlas/pkg/storage"
	"gorm.io/gorm"
)

type Handler struct {
	service            *Service
	validationCacheMu  sync.Mutex
	dualTrackCache     *dualTrackValidationCache
	validationCacheTTL time.Duration
	now                func() time.Time
}

type dualTrackValidationCache struct {
	report    DualTrackValidationReport
	cachedAt  time.Time
	expiresAt time.Time
}

const dualTrackValidationCacheVersion = "prediction-validation-report-cache-v2"
const dualTrackValidationCacheTTL = 30 * time.Second

func NewHandler(db *storage.DB) *Handler              { return newHandler(NewService(db)) }
func NewHandlerWithService(service *Service) *Handler { return newHandler(service) }

func newHandler(service *Service) *Handler {
	return &Handler{service: service, validationCacheTTL: dualTrackValidationCacheTTL, now: time.Now}
}

func (h *Handler) HandleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	overview, err := h.service.Overview()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": overview})
}

func (h *Handler) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	models, err := h.service.Models()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": models, "meta": map[string]any{"total": len(models)}})
}

func (h *Handler) HandleReadiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	summary, rows, err := h.service.Readiness()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": rows, "summary": summary})
}

func (h *Handler) HandleResults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	summary, rows, err := h.service.Results(500)
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": rows, "summary": summary})
}

func (h *Handler) HandleFeatureParity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	rows, err := h.service.FeatureParityAudits(100)
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": len(rows), "scoring_allowed": false}})
}

func (h *Handler) HandleLabels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	summary, rows, err := h.service.Labels(100)
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": rows, "summary": summary})
}

func (h *Handler) HandleAccuracy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	summary, err := h.service.Accuracy()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": summary})
}

func (h *Handler) HandleOutcomeReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	report, err := h.service.OutcomeReport()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) HandleModelGovernance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	report, err := h.service.ModelGovernanceReport()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) HandleHeaRankChallenger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	report, err := h.service.HeaRankChallengerReport()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+report.ReportSHA256+`"`)
	w.Header().Set("X-Atlas-HeaRank-Challenger-Version", report.Version)
	w.Header().Set("X-Atlas-HeaRank-Challenger-SHA256", report.ReportSHA256)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.json"`, report.Version, report.ReportSHA256[:12]))
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) HandleRiskRankingSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	report, err := h.service.RiskRankingSnapshotReport()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+report.ReportSHA256+`"`)
	w.Header().Set("X-Atlas-Risk-Ranking-Version", report.Version)
	w.Header().Set("X-Atlas-Risk-Ranking-SHA256", report.ReportSHA256)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.json"`, report.Version, report.ReportSHA256[:12]))
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) HandleDualTrackValidation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	report, cacheState, cachedAt, err := h.validationReport(r.URL.Query().Get("refresh") == "1")
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	h.setValidationCacheHeaders(w, cacheState, cachedAt)
	w.Header().Set("ETag", `"`+report.ReportSHA256+`"`)
	w.Header().Set("X-Atlas-Dual-Track-Validation-Version", report.Version)
	w.Header().Set("X-Atlas-Dual-Track-Validation-SHA256", report.ReportSHA256)
	if etagMatches(r.Header.Get("If-None-Match"), report.ReportSHA256) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.json"`, report.Version, report.ReportSHA256[:12]))
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) validationReport(forceRefresh bool) (DualTrackValidationReport, string, time.Time, error) {
	if forceRefresh {
		h.invalidateValidationCache()
	}
	report, cacheHit, cachedAt, err := h.cachedDualTrackValidationReport()
	if err != nil {
		return DualTrackValidationReport{}, "", time.Time{}, err
	}
	cacheState := "MISS"
	if forceRefresh {
		cacheState = "REFRESH"
	} else if cacheHit {
		cacheState = "HIT"
	}
	return report, cacheState, cachedAt, nil
}

func (h *Handler) setValidationCacheHeaders(w http.ResponseWriter, cacheState string, cachedAt time.Time) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Atlas-Report-Cache", cacheState)
	w.Header().Set("X-Atlas-Report-Cache-Version", dualTrackValidationCacheVersion)
	w.Header().Set("X-Atlas-Report-Cached-At", cachedAt.UTC().Format(time.RFC3339Nano))
	w.Header().Set("X-Atlas-Report-Cache-TTL-Seconds", strconv.FormatInt(int64(h.validationCacheTTL/time.Second), 10))
}

func (h *Handler) cachedDualTrackValidationReport() (DualTrackValidationReport, bool, time.Time, error) {
	h.validationCacheMu.Lock()
	defer h.validationCacheMu.Unlock()
	now := h.now()
	if h.dualTrackCache != nil && now.Before(h.dualTrackCache.expiresAt) {
		return h.dualTrackCache.report, true, h.dualTrackCache.cachedAt, nil
	}
	report, err := h.service.DualTrackValidationReport()
	if err != nil {
		return DualTrackValidationReport{}, false, time.Time{}, err
	}
	h.dualTrackCache = &dualTrackValidationCache{
		report: report, cachedAt: now, expiresAt: now.Add(h.validationCacheTTL),
	}
	return report, false, now, nil
}

func (h *Handler) invalidateValidationCache() {
	h.validationCacheMu.Lock()
	h.dualTrackCache = nil
	h.validationCacheMu.Unlock()
}

func etagMatches(header, sha string) bool {
	for _, value := range strings.Split(header, ",") {
		value = strings.TrimSpace(value)
		value = strings.TrimSpace(strings.TrimPrefix(value, "W/"))
		if value == "*" || value == `"`+sha+`"` {
			return true
		}
	}
	return false
}

func (h *Handler) HandleLabelManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	manifest, err := h.service.LabelManifest()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+manifest.ManifestSHA256+`"`)
	w.Header().Set("X-Atlas-Label-Manifest-Version", manifest.Version)
	w.Header().Set("X-Atlas-Label-Manifest-SHA256", manifest.ManifestSHA256)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.json"`, manifest.Version, manifest.ManifestSHA256[:12]))
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": manifest})
}

func (h *Handler) HandleValidationReadiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	dualTrackReport, cacheState, cachedAt, err := h.validationReport(r.URL.Query().Get("refresh") == "1")
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	report := dualTrackReport.Readiness
	h.setValidationCacheHeaders(w, cacheState, cachedAt)
	w.Header().Set("ETag", `"`+report.ReadinessSHA256+`"`)
	w.Header().Set("X-Atlas-Validation-Readiness-Version", report.Version)
	w.Header().Set("X-Atlas-Validation-Readiness-SHA256", report.ReadinessSHA256)
	if etagMatches(r.Header.Get("If-None-Match"), report.ReadinessSHA256) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.json"`, report.Version, report.ReadinessSHA256[:12]))
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) HandlePromotionDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	dualTrackReport, cacheState, cachedAt, err := h.validationReport(r.URL.Query().Get("refresh") == "1")
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	report := promotionDecisionReport(dualTrackReport)
	h.setValidationCacheHeaders(w, cacheState, cachedAt)
	w.Header().Set("ETag", `"`+report.DecisionSHA256+`"`)
	w.Header().Set("X-Atlas-Promotion-Decision-Version", report.Version)
	w.Header().Set("X-Atlas-Promotion-Decision-SHA256", report.DecisionSHA256)
	if etagMatches(r.Header.Get("If-None-Match"), report.DecisionSHA256) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.json"`, report.Version, report.DecisionSHA256[:12]))
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) HandleEvidenceBundle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	report, err := h.service.EvidenceBundleReport()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+report.BundleSHA256+`"`)
	w.Header().Set("X-Atlas-Evidence-Bundle-Version", report.Version)
	w.Header().Set("X-Atlas-Evidence-Bundle-SHA256", report.BundleSHA256)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.json"`, report.Version, report.BundleSHA256[:12]))
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) HandleDataDriftReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	report, err := h.service.DataDriftReport()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+report.ReportSHA256+`"`)
	w.Header().Set("X-Atlas-Data-Drift-Version", report.Version)
	w.Header().Set("X-Atlas-Data-Drift-SHA256", report.ReportSHA256)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.json"`, report.Version, report.ReportSHA256[:12]))
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) HandleCalibrationDriftReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	report, err := h.service.CalibrationDriftReport()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+report.ReportSHA256+`"`)
	w.Header().Set("X-Atlas-Calibration-Drift-Version", report.Version)
	w.Header().Set("X-Atlas-Calibration-Drift-SHA256", report.ReportSHA256)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.json"`, report.Version, report.ReportSHA256[:12]))
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) HandleFeatureDriftReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	report, err := h.service.FeatureDriftReport()
	if err != nil {
		predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+report.ReportSHA256+`"`)
	w.Header().Set("X-Atlas-Feature-Drift-Version", report.Version)
	w.Header().Set("X-Atlas-Feature-Drift-SHA256", report.ReportSHA256)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.json"`, report.Version, report.ReportSHA256[:12]))
	}
	predictionJSON(w, http.StatusOK, map[string]any{"data": report})
}

func (h *Handler) HandleOutcomes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		rows, total, err := h.service.OutcomesPage(limit, offset)
		if err != nil {
			predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if limit <= 0 || limit > 500 {
			limit = 100
		}
		if offset < 0 {
			offset = 0
		}
		predictionJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"total": total, "limit": limit, "offset": offset, "returned": len(rows)}})
	case http.MethodPost:
		if err := h.service.SyncOutcomes(); err != nil {
			predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		h.invalidateValidationCache()
		summary, err := h.service.Accuracy()
		if err != nil {
			predictionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		predictionJSON(w, http.StatusOK, map[string]any{"data": summary})
	default:
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (h *Handler) HandleOutcome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		predictionJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	idText := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/prediction/outcomes/"), "/")
	id64, err := strconv.ParseUint(idText, 10, 64)
	if err != nil || id64 == 0 {
		predictionJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid outcome id"})
		return
	}
	var input OutcomeOverride
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		predictionJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	row, err := h.service.OverrideOutcome(uint(id64), input)
	if err != nil {
		status := http.StatusBadRequest
		if err == gorm.ErrRecordNotFound {
			status = http.StatusNotFound
		}
		predictionJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	h.invalidateValidationCache()
	predictionJSON(w, http.StatusOK, map[string]any{"data": row})
}

func predictionJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

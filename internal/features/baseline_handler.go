package features

import (
	"net/http"

	"atlas/pkg/storage"
)

type BaselineHandler struct{ db *storage.DB }

func NewBaselineHandler(db *storage.DB) *BaselineHandler { return &BaselineHandler{db: db} }

func (h *BaselineHandler) HandleCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeFeatureError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	version := r.URL.Query().Get("version")
	if version == "" {
		version = CatalogVersion
	} else if version == "all" {
		version = ""
	}
	baselines, err := ListBaselines(h.db, BaselineListOptions{
		FeatureName:    r.URL.Query().Get("feature"),
		FeatureVersion: version,
		ModelName:      r.URL.Query().Get("model"),
		LoadBucket:     r.URL.Query().Get("load_bucket"),
		Maturity:       r.URL.Query().Get("maturity"),
	})
	if err != nil {
		writeFeatureError(w, http.StatusInternalServerError, err.Error())
		return
	}
	latestRefresh, err := LatestBaselineRefresh(h.db)
	if err != nil {
		writeFeatureError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeFeatureJSON(w, http.StatusOK, map[string]any{
		"data": baselines,
		"meta": map[string]any{
			"total": len(baselines), "contract_version": BaselineContractVersion,
			"feature_version": version, "window_days": baselineWindowDays, "latest_refresh": latestRefresh,
		},
	})
}

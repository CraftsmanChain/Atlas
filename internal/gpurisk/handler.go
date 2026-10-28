package gpurisk

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

type Handler struct {
	DB                   *gorm.DB
	AutomaticJobsEnabled bool
}

func (h Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/gpu-risk/overview", h.ServeHTTP)
	mux.HandleFunc("/api/v1/gpu-risk/queries", h.ServeHTTP)
	mux.HandleFunc("/api/v1/gpu-risk/queries/", h.ServeHTTP)
}
func number(v string, fallback int) (int, error) {
	if v == "" {
		return fallback, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < 1 {
		return 0, ErrInput
	}
	return n, nil
}
func params(v url.Values, allowed ...string) bool {
	for k, values := range v {
		ok := false
		for _, a := range allowed {
			if k == a {
				ok = true
			}
		}
		if !ok || len(values) != 1 || values[0] == "" {
			return false
		}
	}
	return true
}
func respond(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	v, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "invalid parameters", 400)
		return
	}
	db := h.DB.WithContext(r.Context())
	base := "/api/v1/gpu-risk/"
	path := strings.TrimPrefix(r.URL.Path, base)
	if path == "overview" {
		if !params(v) {
			http.Error(w, "invalid parameters", 400)
			return
		}
		var latest api.GPURiskQuery
		err := db.Where("status = ?", "completed").Order("cutoff DESC, id DESC").First(&latest).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "ranking unavailable", 500)
			return
		}
		jobs := []api.GPURiskJob{}
		if e := db.Order("updated_at DESC, id DESC").Limit(5).Find(&jobs).Error; e != nil {
			http.Error(w, "ranking unavailable", 500)
			return
		}
		status := "collecting"
		var query *api.GPURiskQuery
		if err == nil {
			query = &latest
			status = "ready"
			if time.Since(latest.Cutoff) > 25*time.Hour {
				status = "stale"
			}
		}
		respond(w, 200, map[string]any{"data": map[string]any{"status": status, "latest": query, "jobs": jobs, "no_action": true, "probability_emitted": false, "automatic_jobs_enabled": h.AutomaticJobsEnabled, "outcome_status": "not_implemented", "blockers": []string{"ranking_value_unverified", "history_features_pending", "ranking_outcomes_pending"}}})
		return
	}
	if path == "queries" {
		if !params(v, "limit", "before_id") {
			http.Error(w, "invalid parameters", 400)
			return
		}
		limit, e := number(v.Get("limit"), 25)
		before, e2 := number(v.Get("before_id"), 0)
		if e != nil || e2 != nil || limit > 200 {
			http.Error(w, "invalid pagination", 400)
			return
		}
		rows := []api.GPURiskQuery{}
		q := db.Order("id DESC").Limit(limit + 1)
		if before > 0 {
			q = q.Where("id < ?", before)
		}
		if q.Find(&rows).Error != nil {
			http.Error(w, "ranking unavailable", 500)
			return
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		next := uint(0)
		if more {
			next = rows[len(rows)-1].ID
		}
		respond(w, 200, map[string]any{"data": rows, "meta": map[string]any{"has_more": more, "next_before_id": next}})
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[0] != "queries" || parts[2] != "nodes" {
		http.NotFound(w, r)
		return
	}
	id, e := number(parts[1], 0)
	if e != nil || id == 0 || !params(v, "limit", "after_node_id", "policy", "status") {
		http.Error(w, "invalid query", 400)
		return
	}
	limit, e := number(v.Get("limit"), 100)
	after, e2 := number(v.Get("after_node_id"), 0)
	policy := v.Get("policy")
	if policy == "" {
		policy = HealthInverse
	}
	status := v.Get("status")
	allowedStatus := status == "" || status == "included" || status == "complete" || status == "partial" || status == "no_signal" || status == "blocked" || status == "excluded"
	if e != nil || e2 != nil || limit > 200 || (policy != Uniform && policy != HealthInverse) || !allowedStatus {
		http.Error(w, "invalid parameters", 400)
		return
	}
	var query api.GPURiskQuery
	if e := db.Where("id = ? AND status = ?", id, "completed").First(&query).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "ranking unavailable", 500)
		}
		return
	}
	// Validator binds both immutable result and the selected representation.
	tag, _ := digest([]any{query.ResultSHA, policy, status, limit, after})
	etag := "\"" + tag + "\""
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	rows := []api.GPURiskScore{}
	q := db.Where("query_id = ? AND policy = ? AND node_id > ?", id, policy, after).Order("node_id").Limit(limit + 1)
	if status != "" {
		q = q.Where("signal_status = ?", status)
	}
	if q.Find(&rows).Error != nil {
		http.Error(w, "ranking unavailable", 500)
		return
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	next := uint(0)
	if more {
		next = rows[len(rows)-1].NodeID
	}
	members := []api.GPURiskMember{}
	ids := []uint{}
	for _, s := range rows {
		ids = append(ids, s.NodeID)
	}
	if len(ids) > 0 {
		if db.Where("query_id = ? AND node_id IN ?", id, ids).Order("node_id").Find(&members).Error != nil {
			http.Error(w, "ranking unavailable", 500)
			return
		}
	}
	respond(w, 200, map[string]any{"data": rows, "members": members, "query": query, "meta": map[string]any{"total_nodes": query.MemberCount, "has_more": more, "next_after_node_id": next, "order": "node_id", "no_action": true}})
}

package gpurisk

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

func (h Handler) outcomeHTTP(w http.ResponseWriter, r *http.Request, queryID int, v url.Values) {
	if !params(v, "batch_id", "limit", "after_node_id") {
		http.Error(w, "invalid parameters", 400)
		return
	}
	id, e := number(v.Get("batch_id"), 0)
	limit, e2 := number(v.Get("limit"), 100)
	after, e3 := number(v.Get("after_node_id"), 0)
	if e != nil || e2 != nil || e3 != nil || limit > 200 {
		http.Error(w, "invalid pagination", 400)
		return
	}
	db := h.DB.WithContext(r.Context())
	var q api.GPURiskQuery
	if err := db.Where("id = ? AND status = ?", queryID, "completed").First(&q).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "outcome unavailable", 500)
		}
		return
	}
	var batch api.GPURiskOutcomeBatch
	lookup := db.Where("query_id = ?", queryID)
	if id > 0 {
		lookup = lookup.Where("id = ?", id)
	}
	err := lookup.Order("as_of DESC,id DESC").First(&batch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if id > 0 {
			http.NotFound(w, r)
			return
		}
		respond(w, 200, map[string]any{"data": []api.GPURiskNodeOutcome{}, "batch": nil, "query": q, "status": "collecting", "meta": map[string]any{"has_more": false, "next_after_node_id": 0}})
		return
	}
	if err != nil {
		http.Error(w, "outcome unavailable", 500)
		return
	}
	tag, _ := digest([]any{batch.SHA, limit, after})
	etag := "\"" + tag + "\""
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	rows := []api.GPURiskNodeOutcome{}
	if db.Where("batch_id = ? AND node_id > ?", batch.ID, after).Order("node_id,generation").Limit(limit+1).Find(&rows).Error != nil {
		http.Error(w, "outcome unavailable", 500)
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
	respond(w, 200, map[string]any{"data": rows, "batch": batch, "query": q, "status": "recorded", "meta": map[string]any{"total_nodes": batch.MemberCount, "has_more": more, "next_after_node_id": next, "no_action": true}})
}
func (h Handler) evidenceHTTP(w http.ResponseWriter, r *http.Request, v url.Values) {
	if !params(v, "kind", "before_id", "limit") {
		http.Error(w, "invalid parameters", 400)
		return
	}
	var e error
	before, e2 := number(v.Get("before_id"), 0)
	limit, e3 := number(v.Get("limit"), 25)
	kind := v.Get("kind")
	if e != nil || e2 != nil || e3 != nil || limit > 200 || (kind != "" && kind != "identity" && kind != "repair" && kind != "intervention" && kind != "coverage" && kind != "host_context" && kind != "episode_state") {
		http.Error(w, "invalid parameters", 400)
		return
	}
	q := h.DB.WithContext(r.Context()).Order("id DESC").Limit(limit + 1)
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}

	rows := []api.GPURiskEvidenceRevision{}
	if q.Find(&rows).Error != nil {
		http.Error(w, "evidence unavailable", 500)
		return
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	type view struct {
		api.GPURiskEvidenceRevision
		Input EvidenceInput `json:"input"`
	}
	data := []view{}
	for _, row := range rows {
		var input EvidenceInput
		if json.Unmarshal([]byte(row.Data), &input) != nil {
			http.Error(w, "evidence unavailable", 500)
			return
		}
		data = append(data, view{row, input})
	}
	next := uint(0)
	if more {
		next = rows[len(rows)-1].ID
	}
	respond(w, 200, map[string]any{"data": data, "meta": map[string]any{"has_more": more, "next_before_id": next}})
}

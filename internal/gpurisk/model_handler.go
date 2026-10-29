package gpurisk

import (
	"errors"
	"net/http"
	"net/url"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

func (h Handler) modelsHTTP(w http.ResponseWriter, r *http.Request, v url.Values) {
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
	rows := []api.GPURiskModel{}
	q := h.DB.WithContext(r.Context()).Omit("Artifact", "TrainingIdentities").Order("id DESC").Limit(limit + 1)
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	if q.Find(&rows).Error != nil {
		http.Error(w, "models unavailable", 500)
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
	respond(w, 200, map[string]any{"data": rows, "meta": map[string]any{"has_more": more, "next_before_id": next, "no_action": true, "probability_emitted": false, "promotion": "none"}})
}
func (h Handler) modelQueryHTTP(w http.ResponseWriter, r *http.Request, id int, path string, v url.Values) {
	db := h.DB.WithContext(r.Context())
	var q api.GPURiskQuery
	if e := db.Where("id = ? AND status = ?", id, "completed").First(&q).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "query unavailable", 500)
		}
		return
	}
	if path == "model-batches" {
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
		rows := []api.GPURiskModelBatch{}
		lookup := db.Omit("Input").Where("query_id = ?", q.ID).Order("id DESC").Limit(limit + 1)
		if before > 0 {
			lookup = lookup.Where("id < ?", before)
		}
		if lookup.Find(&rows).Error != nil {
			http.Error(w, "batches unavailable", 500)
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
		respond(w, 200, map[string]any{"data": rows, "query": q, "meta": map[string]any{"has_more": more, "next_before_id": next, "no_action": true}})
		return
	}
	if !params(v, "batch_id", "limit", "after_node_id") {
		http.Error(w, "invalid parameters", 400)
		return
	}
	batchID, e := number(v.Get("batch_id"), 0)
	limit, e2 := number(v.Get("limit"), 100)
	after, e3 := number(v.Get("after_node_id"), 0)
	if e != nil || e2 != nil || e3 != nil || batchID == 0 || limit > 200 {
		http.Error(w, "explicit batch required", 400)
		return
	}
	var batch api.GPURiskModelBatch
	if e := db.Omit("Input").Where("id = ? AND query_id = ?", batchID, q.ID).First(&batch).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "batch unavailable", 500)
		}
		return
	}
	rows := []api.GPURiskModelScore{}
	if db.Where("batch_id = ?", batch.ID).Order("node_id").Limit(10001).Find(&rows).Error != nil || len(rows) != batch.MemberCount || len(rows) > 10000 {
		http.Error(w, "batch integrity failed", 500)
		return
	}
	normalized := append([]api.GPURiskModelScore{}, rows...)
	for i := range normalized {
		normalized[i].ID = 0
		normalized[i].BatchID = 0
	}
	sha, e := digest(normalized)
	if e != nil || sha != batch.ResultSHA {
		http.Error(w, "batch integrity failed", 500)
		return
	}
	tag, _ := digest([]any{batch.ResultSHA, batch.RequestSHA, limit, after})
	etag := "\"" + tag + "\""
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	selected := []api.GPURiskModelScore{}
	for _, row := range rows {
		if row.NodeID > uint(after) {
			selected = append(selected, row)
		}
	}
	more := len(selected) > limit
	if more {
		selected = selected[:limit]
	}
	next := uint(0)
	if more {
		next = selected[len(selected)-1].NodeID
	}
	members := []api.GPURiskMember{}
	ids := []uint{}
	for _, row := range selected {
		ids = append(ids, row.NodeID)
	}
	if len(ids) > 0 && db.Where("query_id = ? AND node_id IN ?", q.ID, ids).Order("node_id").Find(&members).Error != nil {
		http.Error(w, "members unavailable", 500)
		return
	}
	respond(w, 200, map[string]any{"data": selected, "members": members, "batch": batch, "query": q, "meta": map[string]any{"total_nodes": batch.MemberCount, "has_more": more, "next_after_node_id": next, "no_action": true, "probability_emitted": false, "promotion": "none", "status": "candidate_value_unverified"}})
}

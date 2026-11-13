package gpurisk

import (
	"atlas/pkg/api"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"sort"
	"strings"
	"time"
)

type ContextCandidate struct {
	Value   string              `json:"value"`
	Sources []ContextProvenance `json:"sources"`
}
type ContextProvenance struct {
	Field      string    `json:"field"`
	ID         uint      `json:"id"`
	ObservedAt time.Time `json:"observed_at"`
	SHA        string    `json:"sha"`
}
type ContextPreparationMember struct {
	NodeID         uint                         `json:"node_id"`
	Generation     int                          `json:"generation"`
	EntityRef      string                       `json:"entity_ref"`
	Candidates     map[string]*ContextCandidate `json:"candidates"`
	FieldBlockers  map[string][]string          `json:"field_blockers"`
	Blockers       []string                     `json:"blockers"`
	ReviewTemplate map[string]any               `json:"review_template"`
}
type ContextPreparation struct {
	Version            string                     `json:"version"`
	Query              api.GPURiskQuery           `json:"query"`
	AsOf               time.Time                  `json:"as_of"`
	Members            []ContextPreparationMember `json:"members"`
	Counts             map[string]int             `json:"counts"`
	InputSHA           string                     `json:"input_sha"`
	SHA                string                     `json:"sha"`
	NoAction           bool                       `json:"no_action"`
	ProbabilityEmitted bool                       `json:"probability_emitted"`
	SigningExecuted    bool                       `json:"signing_executed"`
	Limitations        []string                   `json:"limitations"`
}

func sourceTimestamp(times ...time.Time) time.Time {
	var out time.Time
	for _, t := range times {
		if t.After(out) {
			out = t
		}
	}
	return out.UTC()
}
func contextValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "unknown", "n/a", "null", "none", "未知", "未提供":
		return false
	}
	return strings.TrimSpace(v) == v
}
func BuildContextPreparation(ctx context.Context, db *gorm.DB, queryID uint, asOf time.Time) (ContextPreparation, error) {
	out := ContextPreparation{Version: "gpu-system-context-prepare-v1", AsOf: asOf.UTC().Truncate(time.Microsecond), Members: []ContextPreparationMember{}, Counts: map[string]int{"N": 0, "candidate_members": 0, "blocked": 0}, NoAction: true, Limitations: []string{"current_durable_rows_not_historical_inventory_replay", "candidates_for_future_cutoffs_only", "timestamps_bound_current_rows_not_prior_versions", "templates_are_unsigned_review_worksheets_not_apply_payloads", "data_center_is_source_id_not_resolved_business_category", "missing_fields_remain_null_no_schema_or_gate_change"}}
	if queryID == 0 || asOf.IsZero() || asOf.After(evidenceNow()) {
		return out, ErrInput
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.First(&out.Query, queryID).Error; e != nil {
			return e
		}
		q := out.Query
		if q.HorizonMinutes != 10080 || q.Status != "completed" || q.Contract != Contract || q.Target != Target || q.CompletedAt == nil || q.CompletedAt.After(out.AsOf) || q.Cutoff.After(out.AsOf) {
			return ErrInput
		}
		var scope api.GPURiskScope
		if e := tx.Where("sha = ?", q.ScopeSHA).First(&scope).Error; e != nil {
			return e
		}
		var snapshot Snapshot
		if json.Unmarshal([]byte(scope.Input), &snapshot) != nil || snapshot.SHA != q.ScopeSHA || !snapshot.Cutoff.Equal(q.Cutoff) {
			return ErrInput
		}
		expected, scores, e := Build(snapshot)
		if e != nil {
			return e
		}
		result, _ := digest([]any{expected, scores})
		if result != q.ResultSHA {
			return ErrInput
		}
		var members []api.GPURiskMember
		if e := tx.Where("query_id = ?", q.ID).Order("node_id,generation").Find(&members).Error; e != nil {
			return e
		}
		if len(members) != q.MemberCount || len(members) > MaxNodes {
			return ErrInput
		}
		normalized := append([]api.GPURiskMember{}, members...)
		for i := range normalized {
			normalized[i].ID = 0
			normalized[i].QueryID = 0
		}
		a, _ := digest(expected)
		b, _ := digest(normalized)
		if a != b {
			return ErrInput
		}
		frozen := map[uint]Node{}
		for _, n := range snapshot.Nodes {
			frozen[n.ID] = n
		}
		inputs := []any{q, members}
		for _, m := range members {
			row := ContextPreparationMember{NodeID: m.NodeID, Generation: m.Generation, EntityRef: m.EntityRef, Candidates: map[string]*ContextCandidate{}, Blockers: []string{}}
			for _, name := range []string{"host_type", "gpu_model", "manufacturer", "main_board", "quota_group", "data_center", "rack"} {
				row.Candidates[name] = nil
			}
			row.ReviewTemplate = sourceTemplate("host_context")
			row.ReviewTemplate["node_id"] = m.NodeID
			row.ReviewTemplate["generation"] = m.Generation
			row.ReviewTemplate["entity_ref"] = m.EntityRef
			row.ReviewTemplate["context"] = HostContext{}
			n := frozen[m.NodeID]
			var node api.GPUNode
			if e := tx.First(&node, m.NodeID).Error; e != nil {
				if e != gorm.ErrRecordNotFound {
					return e
				}
				row.Blockers = append(row.Blockers, "current_node_missing")
			}
			inputs = append(inputs, node)
			nt := sourceTimestamp(node.CreatedAt, node.UpdatedAt, node.LastSyncedAt, node.LastSeenAt, node.IdentityChangedAt)
			if m.Status != "included" || node.IdentityGeneration != m.Generation || node.AssetSerial == "" || "serial:"+node.AssetSerial != m.EntityRef || node.AssetSerial != n.Serial || node.Lifecycle != "active" {
				row.Blockers = append(row.Blockers, "current_identity_mismatch")
			}
			if nt.IsZero() || nt.After(out.AsOf) {
				row.Blockers = append(row.Blockers, "current_node_unavailable_at_as_of")
			}
			var cards []api.GPUAsset
			if e := tx.Where("node_id = ? AND current_node_identity = ?", m.NodeID, true).Order("id").Find(&cards).Error; e != nil {
				return e
			}
			inputs = append(inputs, cards)
			want := map[string]bool{}
			for _, c := range n.Cards {
				want[c.UUID] = true
			}
			seen := map[string]bool{}
			gpu := map[string][]ContextProvenance{}
			for _, c := range cards {
				if !want[c.CurrentUUID] || seen[c.CurrentUUID] || c.NodeIdentityGeneration != m.Generation || c.HostSerial != n.Serial || c.State != "active" || c.CurrentUUID == "" {
					row.Blockers = append(row.Blockers, "current_gpu_identity_mismatch")
				}
				seen[c.CurrentUUID] = true
				at := sourceTimestamp(c.CreatedAt, c.UpdatedAt, c.FirstSeenAt, c.LastSeenAt, c.LastSyncedAt)
				if at.IsZero() || at.After(out.AsOf) {
					row.Blockers = append(row.Blockers, "current_gpu_unavailable_at_as_of")
				}
				value, field := c.ModelName, "gpu_assets.model_name"
				if !contextValue(value) {
					value, field = c.Model, "gpu_assets.model"
				}
				sha, _ := digest(c)
				if contextValue(value) {
					gpu[value] = append(gpu[value], ContextProvenance{field, c.ID, at, sha})
				}
			}
			if len(seen) != len(want) || len(cards) == 0 || len(cards) != n.Expected {
				row.Blockers = append(row.Blockers, "current_gpu_set_incomplete")
			}
			dc := map[string][]ContextProvenance{}
			if len(n.SourceAssetIDs) > 0 {
				var assets []api.InfrastructureAsset
				if e := tx.Where("id IN ?", n.SourceAssetIDs).Order("id").Find(&assets).Error; e != nil {
					return e
				}
				inputs = append(inputs, assets)
				if len(assets) != len(n.SourceAssetIDs) {
					row.Blockers = append(row.Blockers, "source_asset_missing")
				}
				for _, v := range assets {
					if v.SerialNumber != n.Serial || !v.Present || !v.InUse {
						row.Blockers = append(row.Blockers, "source_asset_identity_mismatch")
					}
					at := sourceTimestamp(v.CreatedAt, v.UpdatedAt, v.FirstSeenAt, v.LastSeenAt, v.LastSyncedAt)
					if at.IsZero() || at.After(out.AsOf) {
						row.Blockers = append(row.Blockers, "source_asset_unavailable_at_as_of")
					}
					sha, _ := digest(v)
					if contextValue(v.DataCenterID) {
						dc[v.DataCenterID] = append(dc[v.DataCenterID], ContextProvenance{"infrastructure_assets.data_center_id", v.ID, at, sha})
					}
				}
			}
			for name, values := range map[string]map[string][]ContextProvenance{"gpu_model": gpu, "data_center": dc} {
				if len(values) > 1 {
					row.Blockers = append(row.Blockers, name+"_conflict")
				}
				if len(values) == 1 {
					for value, provenance := range values {
						row.Candidates[name] = &ContextCandidate{value, provenance}
					}
				}
			}
			sort.Strings(row.Blockers)
			row.FieldBlockers = map[string][]string{"gpu_model": {}, "data_center": {}}
			nodeBlocked := false
			for _, b := range row.Blockers {
				if strings.HasPrefix(b, "current_gpu_") || b == "gpu_model_conflict" {
					row.FieldBlockers["gpu_model"] = append(row.FieldBlockers["gpu_model"], b)
				} else if strings.HasPrefix(b, "source_asset_") || b == "data_center_conflict" {
					row.FieldBlockers["data_center"] = append(row.FieldBlockers["data_center"], b)
				} else {
					nodeBlocked = true
				}
			}
			if nodeBlocked {
				for name := range row.Candidates {
					row.Candidates[name] = nil
				}
			}
			for name, bs := range row.FieldBlockers {
				if len(bs) > 0 {
					row.Candidates[name] = nil
				}
			}
			if len(row.Blockers) > 0 {
				out.Counts["blocked"]++
			}
			any := false
			for name, c := range row.Candidates {
				if c != nil {
					out.Counts["candidate:"+name]++
					any = true
				} else {
					out.Counts["missing:"+name]++
				}
			}
			if any {
				out.Counts["candidate_members"]++
			}

			out.Counts["N"]++
			out.Members = append(out.Members, row)
		}
		out.InputSHA, _ = digest(inputs)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, fmt.Errorf("context preparation: %w", err)
	}
	out.SHA, err = digest(out)
	return out, err
}

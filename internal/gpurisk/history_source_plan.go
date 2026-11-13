package gpurisk

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// CoreSourcePlan inventories the historical source view. Templates are deliberately
// incomplete review worksheets, never approved EvidenceInput payloads.
type CoreSourcePlan struct {
	Version            string              `json:"version"`
	Query              api.GPURiskQuery    `json:"query"`
	AsOf               time.Time           `json:"as_of"`
	MembersSHA         string              `json:"members_sha"`
	InputSHA           string              `json:"input_sha"`
	Counts             map[string]int      `json:"counts"`
	Members            []CoreSourceMember  `json:"members"`
	Episodes           []CoreSourceEpisode `json:"episodes"`
	NoAction           bool                `json:"no_action"`
	ProbabilityEmitted bool                `json:"probability_emitted"`
	SigningExecuted    bool                `json:"signing_executed"`
	Limitations        []string            `json:"limitations"`
	SHA                string              `json:"sha"`
}
type CoreSourceMember struct {
	NodeID              uint           `json:"node_id"`
	Generation          int            `json:"generation"`
	EntityRef           string         `json:"entity_ref"`
	Status              string         `json:"status"`
	Blockers            []string       `json:"blockers"`
	MissingContext      []string       `json:"missing_context"`
	HistoryUnknown      bool           `json:"history_unknown"`
	EpisodeStateUnknown bool           `json:"episode_state_unknown"`
	NoKnownEpisodes     bool           `json:"no_known_episodes"`
	Template            map[string]any `json:"review_template,omitempty"`
}
type CoreSourceIdentity struct {
	NodeID     uint   `json:"node_id"`
	Generation int    `json:"generation"`
	EntityRef  string `json:"entity_ref"`
}
type CoreSourceEpisode struct {
	Candidates []CoreSourceIdentity `json:"candidate_members"`
	EpisodeKey string               `json:"episode_key"`
	FeedbackID uint                 `json:"feedback_id"`
	ReviewSHA  string               `json:"review_sha"`
	EntityRefs []string             `json:"entity_refs"`
	Template   map[string]any       `json:"review_template"`
}

func sourceTemplate(kind string) map[string]any {
	return map[string]any{"kind": kind, "source_version": HistorySourceVersion, "owner": "", "evidence_sha": "", "from": "", "through": "", "key": "", "previous_sha": "", "state": ""}
}
func BuildCoreSourcePlan(ctx context.Context, db *gorm.DB, queryID uint, asOf time.Time) (CoreSourcePlan, error) {
	out := CoreSourcePlan{Version: "gpu-core-source-plan-v1", Counts: map[string]int{"N": 0, "included": 0, "excluded": 0, "no_signal": 0, "unknown": 0, "eligible": 0}, Members: []CoreSourceMember{}, Episodes: []CoreSourceEpisode{}, NoAction: true, Limitations: []string{"historical_core_inventory_not_dataset_or_training_readiness", "eligible_means_included_without_core_blockers_only", "templates_incomplete_require_responsible_review_and_real_source_sha", "new_evidence_only_available_for_future_cutoffs", "current_inventory_not_a_historical_source", "no_known_episodes_does_not_mean_no_failures", "episode_review_items_include_known_states_not_individual_missing_state_diagnoses"}}
	if queryID == 0 || asOf.IsZero() || asOf.After(evidenceNow()) {
		return out, ErrInput
	}
	out.AsOf = asOf.UTC().Truncate(time.Microsecond)
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.First(&out.Query, queryID).Error; e != nil {
			return e
		}
		q := out.Query
		if q.Status != "completed" || q.Contract != Contract || q.Target != Target || q.HorizonMinutes != 10080 || q.CompletedAt == nil || q.CompletedAt.After(out.AsOf) || q.Cutoff.After(out.AsOf) || !validSHA(q.ScopeSHA) || !validSHA(q.ResultSHA) {
			return ErrInput
		}
		var members []api.GPURiskMember
		if e := tx.Where("query_id = ?", q.ID).Order("node_id,generation").Find(&members).Error; e != nil {
			return e
		}
		if len(members) != q.MemberCount || len(members) > MaxNodes {
			return ErrInput
		}
		seen := map[string]bool{}
		for _, m := range members {
			key := fmt.Sprintf("%d:%d", m.NodeID, m.Generation)
			if seen[key] || m.EntityRef == "" {
				return ErrInput
			}
			seen[key] = true
		}
		out.MembersSHA, _ = digest(members)
		var evidence []api.GPURiskEvidenceRevision
		var reviews []api.HardwareFaultFeedbackReview
		if e := tx.Order("id").Limit(100001).Find(&evidence).Error; e != nil {
			return e
		}
		if e := tx.Order("id").Limit(100001).Find(&reviews).Error; e != nil {
			return e
		}
		if len(evidence) > 100000 || len(reviews) > 100000 {
			return ErrInput
		}
		ev := []api.GPURiskEvidenceRevision{}
		rv := []api.HardwareFaultFeedbackReview{}
		for _, v := range evidence {
			if v.AvailableAt.Before(q.Cutoff) {
				ev = append(ev, v)
			}
		}
		for _, v := range reviews {
			if v.CreatedAt.Before(q.Cutoff) && v.ReviewedAt.Before(q.Cutoff) {
				rv = append(rv, v)
			}
		}
		out.InputSHA, _ = digest([]any{ev, rv})
		var scope api.GPURiskScope
		if e := tx.Where("sha = ?", q.ScopeSHA).First(&scope).Error; e != nil {
			return e
		}
		var snapshot Snapshot
		if json.Unmarshal([]byte(scope.Input), &snapshot) != nil || snapshot.SHA != q.ScopeSHA || !snapshot.Cutoff.Equal(q.Cutoff) {
			return ErrInput
		}
		expectedMembers, scores, e := Build(snapshot)
		if e != nil {
			return e
		}
		resultSHA, _ := digest([]any{expectedMembers, scores})
		if resultSHA != q.ResultSHA {
			return ErrInput
		}
		normalized := append([]api.GPURiskMember{}, members...)
		for i := range normalized {
			normalized[i].ID = 0
			normalized[i].QueryID = 0
		}
		expectedSHA, _ := digest(expectedMembers)
		persistedSHA, _ := digest(normalized)
		if expectedSHA != persistedSHA {
			return ErrInput
		}
		for _, s := range scores {
			if s.Policy == HealthInverse && s.SignalStatus == "no_signal" {
				out.Counts["no_signal"]++
			}
		}
		byEntity := map[string][]api.GPURiskEvidenceRevision{}
		latest := map[string]api.GPURiskEvidenceRevision{}
		for _, r := range ev {
			key := r.Kind + ":" + r.Key
			if old, ok := latest[key]; !ok || r.Revision > old.Revision {
				latest[key] = r
			}
		}
		for _, r := range latest {
			var value EvidenceInput
			if json.Unmarshal([]byte(r.Data), &value) != nil {
				return ErrInput
			}
			byEntity[value.EntityRef] = append(byEntity[value.EntityRef], r)
		}
		episodeMap := map[string]*CoreSourceEpisode{}
		for _, m := range members {
			f, e := historyAt(ctx, m, q, byEntity[m.EntityRef], rv)
			if e != nil {
				return e
			}
			row := CoreSourceMember{NodeID: m.NodeID, Generation: m.Generation, EntityRef: m.EntityRef, Status: m.Status, Blockers: f.Blockers, MissingContext: []string{}, HistoryUnknown: f.HistoryUnknown, EpisodeStateUnknown: contains(f.Blockers, "episode_state_unknown"), NoKnownEpisodes: f.NoHistory == nil || *f.NoHistory}
			for _, name := range []string{"host_type", "gpu_model", "manufacturer", "main_board", "quota_group", "data_center", "rack"} {
				if f.Categorical[name] == nil {
					row.MissingContext = append(row.MissingContext, name)
					out.Counts["missing_context:"+name]++
				}
			}
			if len(row.MissingContext) > 0 {
				row.Template = sourceTemplate("host_context")
				row.Template["node_id"] = m.NodeID
				row.Template["generation"] = m.Generation
				row.Template["entity_ref"] = m.EntityRef
				row.Template["context"] = HostContext{}
			}
			out.Counts["N"]++
			if m.Status == "included" {
				out.Counts["included"]++
				if len(f.Blockers) == 0 {
					out.Counts["eligible"]++
				}
			} else {
				out.Counts["excluded"]++
			}
			if len(f.Blockers) > 0 {
				out.Counts["unknown"]++
			}
			if row.HistoryUnknown {
				out.Counts["history_unknown"]++
			}
			if row.EpisodeStateUnknown {
				out.Counts["episode_state_unknown"]++
				if row.NoKnownEpisodes {
					out.Counts["episode_state_unknown_without_known_episode"]++
				}
			}
			// historyAt's fingerprint contains only reviews of actual accepted events.
			shas := map[string]bool{}
			for _, sha := range f.EvidenceSHAs {
				shas[sha] = true
			}
			identityReviews := map[string]bool{}
			for _, record := range byEntity[m.EntityRef] {
				var v EvidenceInput
				if json.Unmarshal([]byte(record.Data), &v) == nil && v.Kind == "identity" && shas[record.SHA] {
					identityReviews[fmt.Sprintf("%d:%s", v.FeedbackID, v.ReviewSHA)] = true
				}
			}
			for _, r := range rv {
				if !identityReviews[fmt.Sprintf("%d:%s", r.FeedbackRequestID, r.ReviewSHA256)] || !shas[r.ReviewSHA256] || r.Decision != "confirmed_hardware" || r.EpisodeKey == "" {
					continue
				}
				key := r.EpisodeKey + ":" + r.ReviewSHA256
				item := episodeMap[key]
				if item == nil {
					template := sourceTemplate("episode_state")
					template["feedback_id"] = r.FeedbackRequestID
					template["review_sha"] = r.ReviewSHA256
					template["severity"] = ""
					item = &CoreSourceEpisode{EpisodeKey: r.EpisodeKey, FeedbackID: r.FeedbackRequestID, ReviewSHA: r.ReviewSHA256, EntityRefs: []string{}, Template: template}
					episodeMap[key] = item
				}
				item.Candidates = append(item.Candidates, CoreSourceIdentity{m.NodeID, m.Generation, m.EntityRef})
				if !contains(item.EntityRefs, m.EntityRef) {
					item.EntityRefs = append(item.EntityRefs, m.EntityRef)
				}
			}
			out.Members = append(out.Members, row)
		}
		keys := []string{}
		unique := map[string]bool{}
		for key := range episodeMap {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			v := episodeMap[key]
			sort.Strings(v.EntityRefs)
			if len(v.Candidates) == 1 {
				v.Template["node_id"] = v.Candidates[0].NodeID
				v.Template["generation"] = v.Candidates[0].Generation
			} else {
				v.Template["node_id"] = nil
				v.Template["generation"] = nil
			}
			if len(v.EntityRefs) == 1 {
				v.Template["entity_ref"] = v.EntityRefs[0]
			}
			unique[v.EpisodeKey] = true
			out.Episodes = append(out.Episodes, *v)
		}
		out.Counts["independent_episodes"] = len(unique)
		out.Counts["episode_review_items"] = len(out.Episodes)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	out.SHA, err = digest(out)
	return out, err
}

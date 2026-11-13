package eventaudit

import (
	"atlas/pkg/api"
	"context"
	"database/sql"
	"errors"
	"gorm.io/gorm"
	"sort"
)

const ReviewVersion = "hardware-event-review-plan-v1"

type ReviewItem struct {
	NodeFeedbackIDs      []uint   `json:"same_node_feedback_ids_not_identity_confirmation"`
	CandidateKey         string   `json:"candidate_key"`
	EventID              uint     `json:"event_id"`
	SourceSHA            string   `json:"source_sha256"`
	RelatedFeedbackIDs   []uint   `json:"related_feedback_ids_not_episode_confirmation"`
	LatestReviewIDs      []uint   `json:"latest_review_ids_not_adopted_decisions"`
	IdentityCandidateIDs []uint   `json:"identity_candidate_ids_not_certified"`
	RelatedSourceSHA     string   `json:"related_source_sha256"`
	Stage                string   `json:"stage"`
	Blockers             []string `json:"blockers"`
	TrainingEligible     bool     `json:"training_eligible"`
}
type ReviewPlan struct {
	Version          string       `json:"version"`
	Namespace        string       `json:"source_namespace"`
	Items            []ReviewItem `json:"items"`
	NoActionExecuted bool         `json:"no_action_executed"`
	SHA              string       `json:"sha256"`
}

// PrepareReviews never creates feedback, approves a review, or infers episode identity.
func PrepareReviews(ctx context.Context, db *gorm.DB, namespace string, ids []uint) (ReviewPlan, error) {
	p := ReviewPlan{Version: ReviewVersion, Namespace: namespace, Items: []ReviewItem{}, NoActionExecuted: true}
	if !namespacePattern.MatchString(namespace) || len(ids) == 0 || len(ids) > 100 {
		return p, errors.New("namespace and 1..100 unique event IDs required")
	}
	sorted := append([]uint(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for i, id := range sorted {
		if id == 0 || i > 0 && id == sorted[i-1] {
			return p, errors.New("invalid or duplicate event IDs")
		}
	}
	opt := &sql.TxOptions{ReadOnly: true}
	if db.Dialector.Name() == "postgres" {
		opt.Isolation = sql.LevelRepeatableRead
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if e := tx.Exec("SET LOCAL statement_timeout = '60s'").Error; e != nil {
				return errors.New("cannot bound review transaction")
			}
		}
		var events []api.GPUFaultEvent
		if e := tx.Where("id IN ?", sorted).Order("id").Find(&events).Error; e != nil {
			return errors.New("event source unavailable")
		}
		if len(events) != len(sorted) {
			return errors.New("requested event absent; no partial plan")
		}
		feedback, e := load[api.HardwareFaultFeedbackRequest](tx)
		if e != nil {
			return e
		}
		reviews, e := load[api.HardwareFaultFeedbackReview](tx)
		if e != nil {
			return e
		}
		identities, e := load[api.HistoricalGPUIdentityInterval](tx)
		if e != nil {
			return e
		}
		for _, v := range events {
			if ctx.Err() != nil {
				return errors.New("review deadline exceeded")
			}
			item := ReviewItem{CandidateKey: "event-review-" + Hash(struct {
				Namespace string
				ID        uint
			}{namespace, v.ID}), EventID: v.ID, SourceSHA: Hash(v), RelatedFeedbackIDs: []uint{}, LatestReviewIDs: []uint{}, IdentityCandidateIDs: []uint{}, Stage: "review_pending", Blockers: []string{"onset_and_episode_boundary_require_review", "historical_identity_window_requires_certification", "hardware_cause_or_repair_confirmation_required", "query_bound_prepare_required"}}
			links := []string{}
			for _, f := range feedback {
				if f.NodeIP == v.NodeIP && (f.GPUUUID == "" || f.TargetScope == "node") {
					item.NodeFeedbackIDs = append(item.NodeFeedbackIDs, f.ID)
					links = append(links, Hash(f))
				}
				if f.NodeIP != v.NodeIP || v.GPUUUID == "" || f.GPUUUID != v.GPUUUID {
					continue
				}
				item.RelatedFeedbackIDs = append(item.RelatedFeedbackIDs, f.ID)
				links = append(links, Hash(f))
				var latest *api.HardwareFaultFeedbackReview
				for i := range reviews {
					r := &reviews[i]
					if r.FeedbackRequestID == f.ID && (latest == nil || r.Revision > latest.Revision) {
						latest = r
					}
				}
				if latest != nil {
					item.LatestReviewIDs = append(item.LatestReviewIDs, latest.ID)
					links = append(links, Hash(*latest))
				}
			}
			for _, h := range identities {
				if v.GPUUUID != "" && h.GPUUUID == v.GPUUUID && h.NodeIP == v.NodeIP && h.GPUIndex == v.GPUIndex && !v.FirstObservedAt.Before(h.FirstSeenAt) && !v.FirstObservedAt.After(h.LastSeenAt) {
					item.IdentityCandidateIDs = append(item.IdentityCandidateIDs, h.ID)
					links = append(links, Hash(h))
				}
			}
			if len(item.RelatedFeedbackIDs) > 0 {
				item.Blockers = append(item.Blockers, "same_uuid_feedback_requires_episode_match")
			} else {
				item.Blockers = append(item.Blockers, "feedback_not_linked")
			}
			if len(item.NodeFeedbackIDs) > 0 {
				item.Blockers = append(item.Blockers, "node_feedback_requires_identity_and_episode_review")
			}
			if len(item.IdentityCandidateIDs) == 0 {
				item.Blockers = append(item.Blockers, "no_identity_covering_first_observed")
			}
			item.RelatedSourceSHA = Hash(links)
			p.Items = append(p.Items, item)
		}
		return nil
	}, opt)
	if err != nil {
		return ReviewPlan{}, err
	}
	p.SHA = Hash(p)
	return p, nil
}

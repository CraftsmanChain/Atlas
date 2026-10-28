package gpurisk

import (
	"encoding/json"
	"strings"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

func historyEvidenceValid(db *gorm.DB, in EvidenceInput) error {
	if in.SourceVersion != HistorySourceVersion || strings.TrimSpace(in.EntityRef) == "" || in.QueryID != 0 || in.Source != "" || in.Complete || in.GPUUUID != "" || in.GPUIndex != nil {
		return ErrInput
	}
	var node api.GPUNode
	if db.First(&node, in.NodeID).Error != nil {
		return ErrInput
	}
	if in.Kind == "host_context" {
		if in.FeedbackID != 0 || in.ReviewSHA != "" || in.Severity != "" || (in.State != "active" && in.State != "withdrawn") {
			return ErrInput
		}
		if in.State == "withdrawn" {
			if in.Context != nil {
				return ErrInput
			}
			return nil
		}
		if in.Context == nil {
			return ErrInput
		}
		for _, v := range contextFields(*in.Context) {
			if strings.TrimSpace(v) != v || len(v) > 256 || strings.ContainsAny(v, "\r\n\x00") {
				return ErrInput
			}
		}
		return nil
	}
	if in.Context != nil || in.FeedbackID == 0 || !validSHA(in.ReviewSHA) || (in.State != "unresolved" && in.State != "resolved" && in.State != "withdrawn") {
		return ErrInput
	}
	// This binary severity taxonomy is an explicit source assertion, not a mapping
	// inferred from XID names or a claim of author-equivalent classification.
	if in.State == "withdrawn" {
		if in.Severity != "" {
			return ErrInput
		}
	} else if in.Severity != "critical" && in.Severity != "noncritical" {
		return ErrInput
	}
	var review api.HardwareFaultFeedbackReview
	if db.Where("feedback_request_id = ? AND review_sha256 = ?", in.FeedbackID, in.ReviewSHA).First(&review).Error != nil || review.Decision != "confirmed_hardware" || review.EpisodeKey == "" {
		return ErrInput
	}
	_, end := reviewWindow(review)
	if end.IsZero() || in.From.Before(end) {
		return ErrInput
	}
	return nil
}

func contextFields(c HostContext) map[string]string {
	return map[string]string{"host_type": c.HostType, "gpu_model": c.GPUModel, "manufacturer": c.Manufacturer, "main_board": c.MainBoard, "quota_group": c.QuotaGroup, "data_center": c.DataCenter, "rack": c.Rack}
}

// Assertions are already reduced to the latest revision known strictly before q.
// From is the effective state time; Through is the last verification time. A
// reviewed state persists as-known until explicitly revised or withdrawn. This
// never infers unresolved from absence of a repair row.
func applyHistorySources(f *HistoryFeatures, m api.GPURiskMember, q api.GPURiskQuery, assertions []api.GPURiskEvidenceRevision, events []historyEvent) {
	contexts := []HostContext{}
	type state struct{ severity, state, sha string }
	states := map[string][]state{}
	byReview := map[string]map[string]bool{}
	for _, event := range events {
		for _, sha := range event.sha {
			if byReview[sha] == nil {
				byReview[sha] = map[string]bool{}
			}
			byReview[sha][event.key] = true
		}
	}
	for _, e := range assertions {
		var v EvidenceInput
		if json.Unmarshal([]byte(e.Data), &v) != nil {
			continue
		}
		if v.SourceVersion != HistorySourceVersion || v.EntityRef != m.EntityRef || v.From.After(q.Cutoff) || v.Through.After(q.Cutoff) {
			continue
		}
		if v.Kind == "host_context" && v.NodeID == m.NodeID && v.Generation == m.Generation {
			f.EvidenceSHAs = append(f.EvidenceSHAs, e.SHA)
			if v.State != "withdrawn" && v.Context != nil {
				contexts = append(contexts, *v.Context)
			}
		}
		if v.Kind == "episode_state" {
			for key := range byReview[v.ReviewSHA] {
				f.EvidenceSHAs = append(f.EvidenceSHAs, e.SHA)
				if v.State != "withdrawn" {
					states[key] = append(states[key], state{v.Severity, v.State, e.SHA})
				}
			}
		}
	}
	for _, name := range []string{"host_type", "gpu_model", "manufacturer", "main_board", "quota_group", "data_center", "rack"} {
		value, conflict := "", false
		for _, c := range contexts {
			v := contextFields(c)[name]
			if v == "" {
				continue
			}
			if value != "" && value != v {
				conflict = true
			}
			value = v
		}
		if conflict {
			f.Blockers = append(f.Blockers, "host_context_conflict:"+name)
		} else if value != "" {
			v := value
			f.Categorical[name] = &v
		}
		if f.Categorical[name] == nil {
			f.Blockers = append(f.Blockers, "host_context_unknown:"+name)
		}
	}
	severityCounts := map[string]int{}
	unresolved := 0
	known := f.Numeric["episode_count"] != nil
	for _, event := range events {
		values := states[event.key]
		if len(values) == 0 {
			known = false
			continue
		}
		first := values[0]
		for _, v := range values {
			f.EvidenceSHAs = append(f.EvidenceSHAs, v.sha)
			if v.severity != first.severity || v.state != first.state {
				known = false
				f.Blockers = append(f.Blockers, "episode_state_conflict")
			}
		}
		severityCounts[first.severity]++
		if first.state == "unresolved" {
			unresolved++
		}
	}
	if known {
		f.BySeverity = severityCounts
		f.Numeric["unresolved_count"] = numberPtr(float64(unresolved))
	} else {
		f.Blockers = append(f.Blockers, "episode_state_unknown")
	}
}

package gpurisk

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// CoveragePlan is an inventory of persisted assertions, not a source audit,
// outcome evaluation, dataset readiness decision or authorization to sign.
type CoveragePlan struct {
	Version            string               `json:"version"`
	QueryID            uint                 `json:"query_id"`
	ScopeSHA           string               `json:"scope_sha"`
	QueryKey           string               `json:"query_key"`
	ResultSHA          string               `json:"result_sha"`
	MembersSHA         string               `json:"members_sha"`
	Cutoff             time.Time            `json:"cutoff"`
	AsOf               time.Time            `json:"as_of"`
	Members            int                  `json:"members"`
	Included           int                  `json:"included"`
	Excluded           int                  `json:"excluded"`
	Sources            []CoveragePlanSource `json:"sources"`
	EvidenceSHAs       []string             `json:"evidence_shas"`
	NoAction           bool                 `json:"no_action"`
	ProbabilityEmitted bool                 `json:"probability_emitted"`
	SigningExecuted    bool                 `json:"signing_executed"`
	Limitations        []string             `json:"limitations"`
	SHA                string               `json:"sha"`
}
type CoveragePlanSource struct {
	Source                      string                          `json:"source"`
	Check                       *api.GPURiskSourceCoverageCheck `json:"check"`
	CheckStatus                 string                          `json:"check_status"`
	ActionReasons               []string                        `json:"action_reasons"`
	ConflictingMembers          int                             `json:"conflicting_members"`
	CheckStaleAtAsOf            bool                            `json:"check_stale_at_as_of"`
	OutcomeWindowAssertions     int                             `json:"outcome_window_assertions"`
	OutcomeWindowMissing        int                             `json:"outcome_window_missing"`
	MemberAssertions            int                             `json:"member_assertions"`
	MemberAssertionsMissing     int                             `json:"member_assertions_missing"`
	CutoffSafeHistoryCandidates int                             `json:"cutoff_safe_history_candidates"`
	CutoffSafeHistoryMissing    int                             `json:"cutoff_safe_history_missing"`
	Commands                    []string                        `json:"operator_command_templates"`
}

func BuildCoveragePlan(ctx context.Context, db *gorm.DB, queryID uint, asOf time.Time) (CoveragePlan, error) {
	out := CoveragePlan{Version: "gpu-coverage-plan-v1", NoAction: true, EvidenceSHAs: []string{}, Sources: []CoveragePlanSource{}, Limitations: []string{"persisted_assertions_only_not_live_source_audit", "history_candidates_are_not_dataset_coverage_or_paper_core_completion", "commands_require_responsible_owner_and_separate_write_authorization", "outcome_assertions_do_not_prove_label_maturity"}}
	if queryID == 0 || asOf.IsZero() || asOf.After(evidenceNow()) {
		return out, ErrInput
	}
	asOf = asOf.UTC().Truncate(time.Microsecond)
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var q api.GPURiskQuery
		if e := tx.First(&q, queryID).Error; e != nil {
			return e
		}
		if q.Status != "completed" || q.Contract != Contract || q.Target != Target || q.HorizonMinutes != 10080 || q.CompletedAt == nil || q.CompletedAt.After(asOf) || q.Cutoff.After(asOf) {
			return ErrInput
		}
		out.QueryID = q.ID
		out.QueryKey = q.Key
		out.ResultSHA = q.ResultSHA
		out.ScopeSHA = q.ScopeSHA
		out.Cutoff = q.Cutoff.UTC()
		out.AsOf = asOf
		var members []api.GPURiskMember
		if e := tx.Where("query_id = ?", q.ID).Order("node_id,generation").Find(&members).Error; e != nil {
			return e
		}
		if len(members) != q.MemberCount || len(members) > MaxNodes || !validSHA(q.ScopeSHA) || !validSHA(q.ResultSHA) {
			return ErrInput
		}
		out.Members = len(members)
		seen := map[string]bool{}
		for _, m := range members {
			key := fmt.Sprintf("%d:%d", m.NodeID, m.Generation)
			if seen[key] || m.EntityRef == "" {
				return ErrInput
			}
			seen[key] = true
		}
		out.MembersSHA, _ = digest(members)
		for _, m := range members {
			if m.Status == "included" {
				out.Included++
			} else {
				out.Excluded++
			}
		}
		var rows []api.GPURiskEvidenceRevision
		if e := tx.Where("kind = ? AND "+timeSQL(tx, "available_at", "<="), "coverage", asOf).Order("id").Limit(100001).Find(&rows).Error; e != nil {
			return e
		}
		if len(rows) > 100000 {
			return ErrInput
		}
		heads, historical := map[string]api.GPURiskEvidenceRevision{}, map[string]api.GPURiskEvidenceRevision{}
		for _, r := range rows {
			if r.AvailableAt.After(asOf) {
				continue
			}
			if !validSHA(r.SHA) {
				return ErrInput
			}
			if old, ok := heads[r.Key]; !ok || r.Revision > old.Revision {
				heads[r.Key] = r
			}
			if r.AvailableAt.Before(q.Cutoff) {
				if old, ok := historical[r.Key]; !ok || r.Revision > old.Revision {
					historical[r.Key] = r
				}
			}
		}
		type indexed struct {
			record api.GPURiskEvidenceRevision
			value  EvidenceInput
		}
		decoded := map[uint]EvidenceInput{}
		index := func(heads map[string]api.GPURiskEvidenceRevision) (map[string][]indexed, error) {
			result := map[string][]indexed{}
			for _, r := range heads {
				v, parsed := decoded[r.ID]
				if !parsed {
					decoder := json.NewDecoder(bytes.NewBufferString(r.Data))
					decoder.DisallowUnknownFields()
					if decoder.Decode(&v) != nil {
						return nil, ErrInput
					}
					var extra any
					if decoder.Decode(&extra) != io.EOF {
						return nil, ErrInput
					}
					if v.Kind != "coverage" || v.Key != r.Key || v.Through.Before(v.From) {
						return nil, ErrInput
					}
					decoded[r.ID] = v
				}
				key := fmt.Sprintf("%s:%d:%d:%s", v.Source, v.NodeID, v.Generation, v.EntityRef)
				result[key] = append(result[key], indexed{r, v})
			}
			return result, nil
		}
		current, e := index(heads)
		if e != nil {
			return e
		}
		past, e := index(historical)
		if e != nil {
			return e
		}
		selected := map[string]bool{}
		for _, source := range []string{"hardware_fault_ledger", "identity", "monitoring"} {
			s := CoveragePlanSource{Source: source, CheckStatus: "missing", Commands: []string{}}
			var checks []api.GPURiskSourceCoverageCheck
			if e := tx.Where("source = ? AND "+timeSQL(tx, "observed_at", "<="), source, asOf).Order("id").Limit(100001).Find(&checks).Error; e != nil {
				return e
			}
			if len(checks) > 100000 {
				return ErrInput
			}
			visible := []api.GPURiskSourceCoverageCheck{}
			for _, c := range checks {
				if !c.ObservedAt.After(asOf) && !c.CreatedAt.After(asOf) {
					visible = append(visible, c)
				}
			}
			sort.Slice(visible, func(i, j int) bool {
				if visible[i].ObservedAt.Equal(visible[j].ObservedAt) {
					return visible[i].ID > visible[j].ID
				}
				return visible[i].ObservedAt.After(visible[j].ObservedAt)
			})
			if len(visible) > 0 {
				c := visible[0]
				s.Check = &c
				s.CheckStaleAtAsOf = c.VerifiedThrough.Before(asOf.Add(-MaxSourceLag))
				s.CheckStatus = "within_query_tail"
				if c.ObservedAt.IsZero() || c.Contract != CoverageWatermarkContract || c.VerifiedThrough.IsZero() || c.VerifiedThrough.After(c.ObservedAt) || !validSHA(c.CheckSHA256) {
					s.CheckStatus = "invalid"
				} else if c.VerifiedThrough.Before(q.Cutoff.Add(-MaxSourceLag)) {
					s.CheckStatus = "stale_for_query"
				}
			}
			for _, m := range members {
				present, outcome, history := false, false, false
				key := fmt.Sprintf("%s:%d:%d:%s", source, m.NodeID, m.Generation, m.EntityRef)
				conflict := false
				for _, item := range current[key] {
					r, v := item.record, item.value
					selected[r.SHA] = true
					if v.QueryID == q.ID {
						if !v.Complete {
							conflict = true
						}
						if v.Complete {
							present = true
							if !v.From.After(q.Cutoff) && !v.Through.Before(q.Cutoff.Add(7*24*time.Hour)) {
								outcome = true
							}
						}
					}
				}
				for _, item := range past[key] {
					r, v := item.record, item.value
					selected[r.SHA] = true
					end := v.Through
					if end.After(q.Cutoff) {
						end = q.Cutoff
					}
					if !v.Complete {
						conflict = true
					}
					if v.Complete && end.After(v.From) && !end.Before(q.Cutoff.Add(-MaxSourceLag)) {
						history = true
					}
				}
				if conflict {
					s.ConflictingMembers++
				}
				if present {
					s.MemberAssertions++
				}
				if outcome {
					s.OutcomeWindowAssertions++
				}
				if history {
					s.CutoffSafeHistoryCandidates++
				}
			}
			s.ActionReasons = []string{"review_source_and_assertion_gaps_before_any_write", "excluded_members_are_not_written_by_coverage_members", "new_assertions_cannot_repair_old_cutoff_features", "commands_do_not_guarantee_full_horizon_or_gate_completion"}
			if s.CheckStatus != "within_query_tail" || s.CheckStaleAtAsOf {
				s.ActionReasons = append(s.ActionReasons, "source_check_missing_invalid_or_stale")
			}
			if s.MemberAssertions < out.Included {
				s.ActionReasons = append(s.ActionReasons, "member_assertions_missing")
			}
			if s.CheckStatus != "within_query_tail" || s.CheckStaleAtAsOf {
				s.Commands = append(s.Commands, fmt.Sprintf("gpu-risk coverage-check -source %s -owner '<RESPONSIBLE_OWNER>'", source))
			}
			if s.MemberAssertions < out.Included || s.OutcomeWindowAssertions < out.Included {
				s.Commands = append(s.Commands, fmt.Sprintf("gpu-risk coverage-members -query %d -source %s -owner '<RESPONSIBLE_OWNER>'", q.ID, source))
				s.ActionReasons = append(s.ActionReasons, "member_command_requires_successful_fresh_check_and_review_first")
			}
			s.MemberAssertionsMissing = out.Members - s.MemberAssertions
			s.OutcomeWindowMissing = out.Members - s.OutcomeWindowAssertions
			s.CutoffSafeHistoryMissing = out.Members - s.CutoffSafeHistoryCandidates
			out.Sources = append(out.Sources, s)
		}
		for sha := range selected {
			out.EvidenceSHAs = append(out.EvidenceSHAs, sha)
		}
		sort.Strings(out.EvidenceSHAs)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	out.SHA, err = digest(out)
	return out, err
}

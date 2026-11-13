package eventaudit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// Rows are selected in a single read-only snapshot. No migrations or sync jobs run.
func load[T any](tx *gorm.DB) ([]T, error) {
	var rows []T
	err := tx.Order("id").Limit(MaxRows + 1).Find(&rows).Error
	if err != nil {
		return nil, errors.New("required audit source unavailable")
	}
	if len(rows) > MaxRows {
		return nil, errors.New("audit source exceeds row bound; no partial checkpoint")
	}
	return rows, nil
}

type dependency struct {
	Source string `json:"source"`
	ID     uint   `json:"id"`
	SHA    string `json:"sha256"`
}

func Capture(ctx context.Context, db *gorm.DB, namespace string, asof time.Time) (Snapshot, error) {
	s := Snapshot{Version: Version, Namespace: namespace, AsOf: asof.UTC(), Scope: "all_gpu_fault_events", Entries: []Entry{}}
	if !namespacePattern.MatchString(namespace) {
		return s, errors.New("invalid source namespace")
	}
	if err := checkTime(asof); err != nil {
		return s, err
	}
	opt := &sql.TxOptions{ReadOnly: true}
	if db.Dialector.Name() == "postgres" {
		opt.Isolation = sql.LevelRepeatableRead
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SET LOCAL statement_timeout = '60s'").Error; err != nil {
				return errors.New("cannot bound audit transaction")
			}
		}
		events, err := load[api.GPUFaultEvent](tx)
		if err != nil {
			return err
		}
		labels, err := load[api.FailureLabel](tx)
		if err != nil {
			return err
		}
		identities, err := load[api.HistoricalGPUIdentityInterval](tx)
		if err != nil {
			return err
		}
		feedback, err := load[api.HardwareFaultFeedbackRequest](tx)
		if err != nil {
			return err
		}
		reviews, err := load[api.HardwareFaultFeedbackReview](tx)
		if err != nil {
			return err
		}
		issues, err := load[api.PlatformIssue](tx)
		if err != nil {
			return err
		}
		resolutions, err := load[api.IssueResolution](tx)
		if err != nil {
			return err
		}
		assets, err := load[api.GPUAsset](tx)
		if err != nil {
			return err
		}
		nodes, err := load[api.GPUNode](tx)
		if err != nil {
			return err
		}
		members, err := load[api.GPURiskMember](tx)
		if err != nil {
			return err
		}
		queries, err := load[api.GPURiskQuery](tx)
		if err != nil {
			return err
		}
		evidence, err := load[api.GPURiskEvidenceRevision](tx)
		if err != nil {
			return err
		}
		checks, err := load[api.GPURiskSourceCoverageCheck](tx)
		if err != nil {
			return err
		}
		// Coverage revisions can affect multiple cases. Conservatively invalidate all
		// dependencies when they change until per-query dependency scope is certified.
		global := Hash(struct {
			Evidence []api.GPURiskEvidenceRevision
			Checks   []api.GPURiskSourceCoverageCheck
		}{evidence, checks})
		privateEvidence := []string{}
		for _, v := range evidence {
			privateEvidence = append(privateEvidence, Hash(v.Data))
		}
		global = Hash([]string{global, Hash(privateEvidence)})
		queryByID := map[uint]api.GPURiskQuery{}
		for _, q := range queries {
			queryByID[q.ID] = q
		}
		for _, e := range events {
			if err := ctx.Err(); err != nil {
				return errors.New("audit deadline exceeded; no partial checkpoint")
			}
			if e.FirstObservedAt.After(asof) || e.LastObservedAt.After(asof) || e.CreatedAt.After(asof) || e.UpdatedAt.After(asof) {
				return errors.New("current source rows exceed as-of; historical reconstruction not supported")
			}
			deps := []dependency{{"coverage_revision_set", 0, global}}
			add := func(kind string, id uint, value any) { deps = append(deps, dependency{kind, id, Hash(value)}) }
			for _, v := range labels {
				if v.SourceType == "gpu_fault_event" && v.SourceRecordID == e.ID {
					add("label", v.ID, fields(v, "updated_at"))
				}
			}
			for _, v := range identities {
				if v.GPUUUID == e.GPUUUID && v.NodeIP == e.NodeIP {
					add("identity", v.ID, v)
				}
			}
			fb := map[uint]bool{}
			for _, v := range feedback {
				if v.NodeIP == e.NodeIP && (v.GPUUUID == e.GPUUUID || v.GPUUUID == "" || v.TargetScope == "node") {
					fb[v.ID] = true
					add("feedback", v.ID, v)
				}
			}
			for _, v := range reviews {
				if fb[v.FeedbackRequestID] || v.ConfirmedGPUUUID == e.GPUUUID {
					add("review", v.ID, v)
				}
			}
			is := map[uint]bool{}
			for _, v := range issues {
				if v.Category == "hardware_fault" && v.SourceRecordID == e.ID {
					is[v.ID] = true
					add("issue", v.ID, fields(v, "last_detected_at", "updated_at"))
				}
			}
			for _, v := range resolutions {
				if is[v.IssueID] {
					add("resolution", v.ID, v)
				}
			}
			for _, v := range assets {
				if v.ID == e.GPUAssetID || v.CurrentUUID == e.GPUUUID {
					add("asset", v.ID, fields(v, "last_seen_at", "last_synced_at", "updated_at"))
				}
			}
			for _, v := range nodes {
				if v.NodeIP == e.NodeIP {
					add("node", v.ID, fields(v, "last_seen_at", "last_synced_at", "target_synced_at", "updated_at"))
				}
			}
			for _, v := range members {
				q, ok := queryByID[v.QueryID]
				if ok && v.NodeIP == e.NodeIP && q.Cutoff.Before(e.FirstObservedAt) && !q.Cutoff.Add(time.Duration(q.HorizonMinutes)*time.Minute).Before(e.FirstObservedAt) {
					add("member", v.ID, v)
					add("query", q.ID, q)
				}
			}
			fresh := "current"
			if e.State == "recovered" {
				fresh = "recovered"
			} else if asof.Sub(e.LastObservedAt) > 24*time.Hour {
				fresh = "stale_open"
			}
			s.Entries = append(s.Entries, Entry{ID: e.ID, Semantic: Hash(fields(e, "occurrence_count", "last_observed_at", "updated_at", "latest_score_id")), Observation: Hash(struct {
				Last    time.Time
				Count   int
				Updated time.Time
			}{e.LastObservedAt, e.OccurrenceCount, e.UpdatedAt}), Dependency: Hash(deps), Freshness: fresh})
		}
		return nil
	}, opt)
	if err != nil {
		return s, fmt.Errorf("read-only audit failed: %w", err)
	}
	s.Complete = true
	s.Seal()
	return s, s.Validate()
}

package gpurisk

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// DailyCheckVersion names the read-only daily operator check contract. It only
// reads frozen rows and existing projections; it never writes, alerts or acts.
const DailyCheckVersion = "gpu-risk-daily-check-v1"

// dailyLedgerMaxAgeDays: a ledger declaration whose verified_through is older
// than this is reported as WARN (the v4 ledger lag budget is 8 days).
const dailyLedgerMaxAgeDays = 7.0

// dailyHistoryUnknownWarn: v4 history_unknown above this share of rows is WARN.
const dailyHistoryUnknownWarn = 0.10

var dailyZone = time.FixedZone("Asia/Shanghai", 8*3600)

type DailyQuery struct {
	ID          uint       `json:"id"`
	Cutoff      time.Time  `json:"cutoff"`
	MemberCount int        `json:"member_count"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type DailyOperation struct {
	Stage      string    `json:"stage"`
	Status     string    `json:"status"`
	ErrorCode  string    `json:"error_code"`
	ObservedAt time.Time `json:"observed_at"`
}

type DailyShadow struct {
	Exists        bool       `json:"exists"`
	BatchID       uint       `json:"batch_id,omitempty"`
	Timing        string     `json:"timing,omitempty"`
	SHA           string     `json:"sha,omitempty"`
	ComputedAt    *time.Time `json:"computed_at,omitempty"`
	ScoredCount   int        `json:"scored_count"`
	NonZeroSignal int        `json:"non_zero_signal_members"`
	LargestTie    int        `json:"largest_tie_group"`
}

type DailyDatasetCounts struct {
	Version        string `json:"version,omitempty"`
	SHA            string `json:"sha,omitempty"`
	Rows           int    `json:"rows"`
	HistoryUnknown int    `json:"history_unknown"`
	Eligible       int    `json:"eligible"`
	Error          string `json:"error,omitempty"`
}

// DailyR1 is the history_unknown probe of the newest v1 outcome batch of the
// latest query, under history contract v4 and the frozen v2 contract.
type DailyR1 struct {
	OutcomeBatchID uint               `json:"outcome_batch_id,omitempty"`
	OutcomeAsOf    *time.Time         `json:"outcome_as_of,omitempty"`
	Spec           *DatasetSpec       `json:"spec,omitempty"`
	V4             DailyDatasetCounts `json:"v4"`
	V2             DailyDatasetCounts `json:"v2"`
	Error          string             `json:"error,omitempty"`
}

type DailySourceCheck struct {
	Source          string    `json:"source"`
	VerifiedThrough time.Time `json:"verified_through"`
	ObservedAt      time.Time `json:"observed_at"`
	Contract        string    `json:"contract"`
	Owner           string    `json:"owner"`
}

type DailyLedger struct {
	VerifiedThrough time.Time `json:"verified_through"`
	ObservedAt      time.Time `json:"observed_at"`
	Owner           string    `json:"owner"`
	AgeDays         float64   `json:"age_days"` // now - verified_through
}

type DailyCoverage struct {
	MaxCoverageAvailableAt *time.Time         `json:"max_coverage_available_at,omitempty"`
	Sources                []DailySourceCheck `json:"sources"`
	Ledger                 *DailyLedger       `json:"ledger_declaration,omitempty"`
}

type DailyIncident struct {
	QueryID uint           `json:"query_id"`
	Cutoff  time.Time      `json:"cutoff"`
	BatchID uint           `json:"batch_id,omitempty"`
	AsOf    *time.Time     `json:"as_of,omitempty"`
	Counts  map[string]int `json:"counts"`
}

type DailyFeedback struct {
	ID        uint      `json:"id"`
	NodeIP    string    `json:"node_ip"`
	FaultType string    `json:"fault_type"`
	Onset     time.Time `json:"onset"`
	Decision  string    `json:"latest_decision"` // "" = no review
}

type DailyE2 struct {
	ProspectiveBatches int      `json:"prospective_batches"`
	Evaluated          int      `json:"evaluated"`
	M2Hits             int      `json:"m2_hits"`
	M1Hits             int      `json:"m1_hits"`
	ReadPoint          string   `json:"read_point"`
	PValue             *float64 `json:"p_value,omitempty"`
	Verdict            string   `json:"verdict"`
	Error              string   `json:"error,omitempty"`
}

type DailyCheck struct {
	Version            string           `json:"version"`
	GeneratedAt        time.Time        `json:"generated_at"`
	LocalDay           string           `json:"local_day"`
	LatestQuery        *DailyQuery      `json:"latest_query,omitempty"`
	Operations         []DailyOperation `json:"operations"`
	Shadow             DailyShadow      `json:"shadow"`
	R1                 DailyR1          `json:"r1_history_unknown"`
	Coverage           DailyCoverage    `json:"coverage"`
	IncidentOutcomes   []DailyIncident  `json:"incident_outcomes"`
	UnreviewedFeedback []DailyFeedback  `json:"unreviewed_feedback"`
	E2                 DailyE2          `json:"e2_prime"`
	Findings           []string         `json:"findings"`
}

// dailyR1Spec derives the R1 dataset spec from the latest cutoff and now.
func dailyR1Spec(batchID uint, cutoff, now time.Time) (DatasetSpec, error) {
	c := cutoff.UTC()
	anchor := time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, time.UTC)
	spec := DatasetSpec{BatchIDs: []uint{batchID}, Anchor: anchor, TrainEnd: anchor.Add(8 * time.Hour), ValidationEnd: anchor.Add(16 * time.Hour), TestEnd: now.UTC().Add(-time.Second), Profile: "paper_core", HistoryContract: HistoryContractV4}
	if !spec.TestEnd.After(spec.ValidationEnd) {
		spec.TestEnd = anchor.Add(20 * time.Hour)
		if spec.TestEnd.After(now.UTC()) {
			return spec, fmt.Errorf("r1 test_end %s not reached at %s", spec.TestEnd.Format(time.RFC3339), now.UTC().Format(time.RFC3339))
		}
	}
	return spec, nil
}

func dailyDataset(ctx context.Context, db *gorm.DB, spec DatasetSpec) DailyDatasetCounts {
	d, err := BuildDataset(ctx, db, spec)
	if err != nil {
		return DailyDatasetCounts{Error: err.Error()}
	}
	return DailyDatasetCounts{Version: d.Version, SHA: d.SHA, Rows: d.Counts["rows"], HistoryUnknown: d.Counts["history_unknown"], Eligible: d.Counts["eligible"]}
}

// BuildDailyCheck assembles the read-only daily operator check as of now.
// Failures of the R1 dataset probe and the E2' report are recorded in the
// result; only failures of the base read transaction are returned.
func BuildDailyCheck(ctx context.Context, db *gorm.DB, now time.Time) (DailyCheck, error) {
	now = now.UTC().Truncate(time.Microsecond)
	local := now.In(dailyZone)
	out := DailyCheck{Version: DailyCheckVersion, GeneratedAt: now, LocalDay: local.Format("2006-01-02"), Operations: []DailyOperation{}, Coverage: DailyCoverage{Sources: []DailySourceCheck{}}, IncidentOutcomes: []DailyIncident{}, UnreviewedFeedback: []DailyFeedback{}, Findings: []string{}}
	var r1Batch api.GPURiskOutcomeBatch
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var qs []api.GPURiskQuery
		if e := tx.Where("status = ? AND contract = ?", "completed", Contract).Order("cutoff DESC,id DESC").Limit(1).Find(&qs).Error; e != nil {
			return e
		}
		if len(qs) == 1 {
			q := qs[0]
			out.LatestQuery = &DailyQuery{ID: q.ID, Cutoff: q.Cutoff.UTC(), MemberCount: q.MemberCount, CompletedAt: q.CompletedAt}

			var batches []api.GPURiskShadowBatch
			if e := tx.Where("query_id = ? AND strategy = ?", q.ID, ShadowStrategy).Limit(1).Find(&batches).Error; e != nil {
				return e
			}
			if len(batches) == 1 {
				b := batches[0]
				at := b.ComputedAt.UTC()
				out.Shadow = DailyShadow{Exists: true, BatchID: b.ID, Timing: b.Timing, SHA: b.SHA, ComputedAt: &at, ScoredCount: b.ScoredCount}
				var rows []api.GPURiskShadowScore
				if e := tx.Where("batch_id = ?", b.ID).Find(&rows).Error; e != nil {
					return e
				}
				for _, r := range rows {
					if r.History > 0 || r.Precursor > 0 || r.DaysSince != nil {
						out.Shadow.NonZeroSignal++
					}
					if r.RawScore != nil && r.TieCount > out.Shadow.LargestTie {
						out.Shadow.LargestTie = r.TieCount
					}
				}
			}

			var obs []api.GPURiskOutcomeBatch
			if e := tx.Where("query_id = ? AND version = ?", q.ID, OutcomeVersion).Order("as_of DESC,id DESC").Limit(1).Find(&obs).Error; e != nil {
				return e
			}
			if len(obs) == 1 {
				r1Batch = obs[0]
			}
		}

		var ops []api.GPURiskOperation
		if e := tx.Where("day = ?", out.LocalDay).Order("stage").Find(&ops).Error; e != nil {
			return e
		}
		for _, o := range ops {
			out.Operations = append(out.Operations, DailyOperation{Stage: o.Stage, Status: o.Status, ErrorCode: o.ErrorCode, ObservedAt: o.ObservedAt.UTC()})
		}

		var cov []api.GPURiskEvidenceRevision
		if e := tx.Select("id", "available_at").Where("kind = ?", "coverage").Order(timeOrder(tx, "available_at")).Limit(1).Find(&cov).Error; e != nil {
			return e
		}
		if len(cov) == 1 && !cov[0].AvailableAt.IsZero() {
			at := cov[0].AvailableAt.UTC()
			out.Coverage.MaxCoverageAvailableAt = &at
		}
		for _, source := range coverageSources {
			var checks []api.GPURiskSourceCoverageCheck
			if e := tx.Where("source = ?", source).Order(timeOrder(tx, "observed_at")).Limit(1).Find(&checks).Error; e != nil {
				return e
			}
			if len(checks) == 1 {
				c := checks[0]
				out.Coverage.Sources = append(out.Coverage.Sources, DailySourceCheck{Source: c.Source, VerifiedThrough: c.VerifiedThrough.UTC(), ObservedAt: c.ObservedAt.UTC(), Contract: c.Contract, Owner: c.Owner})
			}
		}
		var decl []api.GPURiskSourceCoverageCheck
		if e := tx.Where("source = ? AND contract = ?", "hardware_fault_ledger", LedgerDeclaredContract).Order(timeOrder(tx, "observed_at")).Limit(1).Find(&decl).Error; e != nil {
			return e
		}
		if len(decl) == 1 {
			d := decl[0]
			out.Coverage.Ledger = &DailyLedger{VerifiedThrough: d.VerifiedThrough.UTC(), ObservedAt: d.ObservedAt.UTC(), Owner: d.Owner, AgeDays: now.Sub(d.VerifiedThrough.UTC()).Hours() / 24}
		}

		// T-A: newest incident outcome batch per query with cutoff in the last 31 days.
		since := now.Add(-31 * 24 * time.Hour)
		var recent []api.GPURiskQuery
		if e := tx.Where("status = ? AND "+timeSQL(tx, "cutoff", ">="), "completed", since).Order("cutoff DESC,id DESC").Limit(201).Find(&recent).Error; e != nil {
			return e
		}
		if len(recent) > 200 {
			return ErrInput
		}
		for _, q := range recent {
			if q.Cutoff.Before(since) || q.Cutoff.After(now) {
				continue
			}
			item := DailyIncident{QueryID: q.ID, Cutoff: q.Cutoff.UTC(), Counts: map[string]int{}}
			var ib []api.GPURiskOutcomeBatch
			if e := tx.Where("query_id = ? AND version = ?", q.ID, IncidentOutcomeVersion).Order("as_of DESC,id DESC").Limit(1).Find(&ib).Error; e != nil {
				return e
			}
			if len(ib) == 1 {
				at := ib[0].AsOf.UTC()
				item.BatchID, item.AsOf = ib[0].ID, &at
				var rows []api.GPURiskNodeOutcome
				if e := tx.Select("id", "status").Where("batch_id = ?", ib[0].ID).Find(&rows).Error; e != nil {
					return e
				}
				for _, r := range rows {
					item.Counts[r.Status]++
				}
			}
			out.IncidentOutcomes = append(out.IncidentOutcomes, item)
		}

		// Unreviewed (or needs_evidence) feedback with onset in the last 30 days.
		fbSince := now.Add(-30 * 24 * time.Hour)
		pre := fbSince.Add(-24 * time.Hour) // coarse SQL prefilter; exact bound below
		var feedback []api.HardwareFaultFeedbackRequest
		if e := tx.Where("("+timeSQL(tx, "fault_occurred_at", ">=")+" OR "+timeSQL(tx, "fault_window_start_at", ">=")+")", pre, pre).Order("id").Limit(10001).Find(&feedback).Error; e != nil {
			return e
		}
		if len(feedback) > 10000 {
			return ErrInput
		}
		ids := []uint{}
		for _, f := range feedback {
			ids = append(ids, f.ID)
		}
		latest := map[uint]string{}
		if len(ids) > 0 {
			var reviews []api.HardwareFaultFeedbackReview
			if e := tx.Where("feedback_request_id IN ?", ids).Order("feedback_request_id, revision DESC, id DESC").Find(&reviews).Error; e != nil {
				return e
			}
			for _, r := range reviews {
				if _, ok := latest[r.FeedbackRequestID]; !ok {
					latest[r.FeedbackRequestID] = r.Decision
				}
			}
		}
		for _, f := range feedback {
			onset := f.FaultOccurredAt
			if f.FaultWindowStartAt != nil {
				onset = *f.FaultWindowStartAt
			}
			if onset.Before(fbSince) {
				continue
			}
			d, ok := latest[f.ID]
			if ok && d != "needs_evidence" {
				continue
			}
			out.UnreviewedFeedback = append(out.UnreviewedFeedback, DailyFeedback{ID: f.ID, NodeIP: strings.TrimSpace(f.NodeIP), FaultType: f.FaultType, Onset: onset.UTC(), Decision: d})
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}

	// R1 probe: BuildDataset owns its transaction; failures are recorded only.
	switch {
	case out.LatestQuery == nil:
		out.R1.Error = "no completed query"
	case r1Batch.ID == 0:
		out.R1.Error = "no " + OutcomeVersion + " batch for latest query"
	default:
		at := r1Batch.AsOf.UTC()
		out.R1.OutcomeBatchID, out.R1.OutcomeAsOf = r1Batch.ID, &at
		spec, e := dailyR1Spec(r1Batch.ID, out.LatestQuery.Cutoff, now)
		if e != nil {
			out.R1.Error = e.Error()
			break
		}
		s := spec
		out.R1.Spec = &s
		out.R1.V4 = dailyDataset(ctx, db, spec)
		spec.HistoryContract = ""
		out.R1.V2 = dailyDataset(ctx, db, spec)
	}

	e2, e := BuildE2Report(ctx, db, now)
	if e != nil {
		out.E2 = DailyE2{Error: e.Error()}
	} else {
		out.E2 = DailyE2{ProspectiveBatches: e2.ProspectiveBatches, Evaluated: e2.Evaluated, M2Hits: e2.M2Hits, M1Hits: e2.M1Hits, ReadPoint: e2.ReadPoint, PValue: e2.PValue, Verdict: e2.Verdict}
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	out.Findings = dailyFindings(out)
	return out, nil
}

func dailyFindings(c DailyCheck) []string {
	f := []string{}
	if c.LatestQuery == nil {
		f = append(f, "WARN 无已完成的冻结查询（gpu_risk_queries status=completed）")
	} else if !c.Shadow.Exists {
		f = append(f, fmt.Sprintf("WARN 最新查询 %d 缺少影子批次（策略 %s）", c.LatestQuery.ID, ShadowStrategy))
	} else if c.Shadow.Timing != "prospective" {
		f = append(f, fmt.Sprintf("WARN 最新查询 %d 的影子批次 %d 为 %s，非前瞻，不计入 E2′", c.LatestQuery.ID, c.Shadow.BatchID, c.Shadow.Timing))
	}
	ledgerStale := c.Coverage.Ledger == nil || (c.LatestQuery != nil && c.Coverage.Ledger.VerifiedThrough.Before(c.LatestQuery.Cutoff.Add(-24*time.Hour)))
	for _, o := range c.Operations {
		switch {
		case o.Status == "failed" || o.Status == "manual_required":
			f = append(f, fmt.Sprintf("WARN 运行阶段 %s 状态 %s（%s）", o.Stage, o.Status, o.ErrorCode))
		case o.Stage == "coverage" && o.Status == "blocked":
			if o.ErrorCode == "coverage_hardware_fault_ledger_incomplete" && ledgerStale {
				f = append(f, fmt.Sprintf("INFO coverage 阶段 blocked（%s）：ledger 声明早于最新 cutoff-24h，需要新的 ledger 声明", o.ErrorCode))
			} else {
				f = append(f, fmt.Sprintf("WARN coverage 阶段 blocked（%s）", o.ErrorCode))
			}
		}
	}
	if l := c.Coverage.Ledger; l == nil {
		f = append(f, "WARN 无 ledger 声明（hardware_fault_ledger / "+LedgerDeclaredContract+"）")
	} else if l.AgeDays > dailyLedgerMaxAgeDays {
		f = append(f, fmt.Sprintf("WARN ledger 声明 verified_through %s 已 %.1f 天（> %.0f 天）", l.VerifiedThrough.Format(time.RFC3339), l.AgeDays, dailyLedgerMaxAgeDays))
	}
	if v := c.R1.V4; v.Error == "" && v.Rows > 0 && float64(v.HistoryUnknown) > dailyHistoryUnknownWarn*float64(v.Rows) {
		f = append(f, fmt.Sprintf("WARN v4 数据集 history_unknown %d/%d 行（> %.0f%%）", v.HistoryUnknown, v.Rows, dailyHistoryUnknownWarn*100))
	}
	if c.R1.Error != "" || c.R1.V4.Error != "" || c.R1.V2.Error != "" {
		f = append(f, "INFO R1 数据集探针未完整："+strings.Trim(strings.Join([]string{c.R1.Error, c.R1.V4.Error, c.R1.V2.Error}, "; "), "; "))
	}
	if c.E2.Error != "" {
		f = append(f, "WARN E2′ 报告生成失败："+c.E2.Error)
	}
	if n := len(c.UnreviewedFeedback); n > 0 {
		ids := make([]string, 0, n)
		for _, x := range c.UnreviewedFeedback {
			ids = append(ids, fmt.Sprint(x.ID))
		}
		f = append(f, fmt.Sprintf("INFO 近 30 天有 %d 条未审核反馈（%s），待 Commander 核对", n, strings.Join(ids, ",")))
	}
	return f
}

func fmtTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.In(dailyZone).Format("2006-01-02 15:04:05")
}

// RenderDailyMarkdown renders the daily check (Chinese, Asia/Shanghai times).
func RenderDailyMarkdown(c DailyCheck) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# GPU 风险日检（%s）\n\n", c.LocalDay)
	fmt.Fprintf(&b, "生成时间 %s（UTC+8）；契约 %s。只读检查，不产生告警或处置动作。\n\n", fmtTime(&c.GeneratedAt), c.Version)
	b.WriteString("## 结论\n\n")
	if len(c.Findings) == 0 {
		b.WriteString("- 无异常发现\n")
	}
	for _, x := range c.Findings {
		fmt.Fprintf(&b, "- %s\n", x)
	}
	b.WriteString("\n## 最新查询\n\n")
	if q := c.LatestQuery; q == nil {
		b.WriteString("无已完成查询。\n")
	} else {
		cut := q.Cutoff
		fmt.Fprintf(&b, "| id | cutoff | 成员数 |\n| --- | --- | --- |\n| %d | %s | %d |\n", q.ID, fmtTime(&cut), q.MemberCount)
	}
	fmt.Fprintf(&b, "\n## 今日运行阶段（%s）\n\n", c.LocalDay)
	if len(c.Operations) == 0 {
		b.WriteString("无记录。\n")
	} else {
		b.WriteString("| 阶段 | 状态 | 错误码 |\n| --- | --- | --- |\n")
		for _, o := range c.Operations {
			code := o.ErrorCode
			if code == "" {
				code = "-"
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", o.Stage, o.Status, code)
		}
	}
	b.WriteString("\n## 影子批次\n\n")
	if s := c.Shadow; !s.Exists {
		b.WriteString("不存在。\n")
	} else {
		fmt.Fprintf(&b, "| 批次 | 时序 | 计算时间 | 打分数 | 非零信号成员 | 最大并列组 | SHA |\n| --- | --- | --- | --- | --- | --- | --- |\n| %d | %s | %s | %d | %d | %d | %s |\n", s.BatchID, s.Timing, fmtTime(s.ComputedAt), s.ScoredCount, s.NonZeroSignal, s.LargestTie, s.SHA)
	}
	b.WriteString("\n## R1：history_unknown（最新查询的 v1 结果批次）\n\n")
	if c.R1.Error != "" {
		fmt.Fprintf(&b, "未计算：%s\n", c.R1.Error)
	} else {
		fmt.Fprintf(&b, "结果批次 %d（as_of %s）。\n\n| 契约 | 行数 | history_unknown | eligible | 错误 |\n| --- | --- | --- | --- | --- |\n", c.R1.OutcomeBatchID, fmtTime(c.R1.OutcomeAsOf))
		for _, v := range []struct {
			name string
			d    DailyDatasetCounts
		}{{"v4", c.R1.V4}, {"v2", c.R1.V2}} {
			e := v.d.Error
			if e == "" {
				e = "-"
			}
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %s |\n", v.name, v.d.Rows, v.d.HistoryUnknown, v.d.Eligible, e)
		}
	}
	b.WriteString("\n## 覆盖\n\n")
	fmt.Fprintf(&b, "coverage 证据最新 available_at：%s\n\n", fmtTime(c.Coverage.MaxCoverageAvailableAt))
	if len(c.Coverage.Sources) > 0 {
		b.WriteString("| 来源 | verified_through | 契约 | owner |\n| --- | --- | --- | --- |\n")
		for _, s := range c.Coverage.Sources {
			vt := s.VerifiedThrough
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", s.Source, fmtTime(&vt), s.Contract, s.Owner)
		}
		b.WriteString("\n")
	}
	if l := c.Coverage.Ledger; l == nil {
		b.WriteString("ledger 声明：无。\n")
	} else {
		vt := l.VerifiedThrough
		fmt.Fprintf(&b, "ledger 声明：verified_through %s，距今 %.1f 天（owner %s）。\n", fmtTime(&vt), l.AgeDays, l.Owner)
	}
	b.WriteString("\n## T-A 节点事件结果（近 31 天查询）\n\n")
	if len(c.IncidentOutcomes) == 0 {
		b.WriteString("无。\n")
	} else {
		b.WriteString("| query | cutoff | 批次 | 状态计数 |\n| --- | --- | --- | --- |\n")
		for _, x := range c.IncidentOutcomes {
			keys := make([]string, 0, len(x.Counts))
			for k := range x.Counts {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := []string{}
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s=%d", k, x.Counts[k]))
			}
			batch := "-"
			if x.BatchID != 0 {
				batch = fmt.Sprint(x.BatchID)
			}
			cut := x.Cutoff
			fmt.Fprintf(&b, "| %d | %s | %s | %s |\n", x.QueryID, fmtTime(&cut), batch, strings.Join(parts, " "))
		}
	}
	b.WriteString("\n## 未审核反馈（近 30 天发生）\n\n")
	if len(c.UnreviewedFeedback) == 0 {
		b.WriteString("无。\n")
	} else {
		b.WriteString("| id | 节点 | 故障类型 | 发生时间 | 最新审核 |\n| --- | --- | --- | --- | --- |\n")
		for _, x := range c.UnreviewedFeedback {
			d := x.Decision
			if d == "" {
				d = "未审核"
			}
			on := x.Onset
			fmt.Fprintf(&b, "| %d | %s | %s | %s | %s |\n", x.ID, x.NodeIP, x.FaultType, fmtTime(&on), d)
		}
	}
	b.WriteString("\n## E2′ 摘要\n\n")
	if c.E2.Error != "" {
		fmt.Fprintf(&b, "生成失败：%s\n", c.E2.Error)
	} else {
		p := "-"
		if c.E2.PValue != nil {
			p = fmt.Sprintf("%.4g", *c.E2.PValue)
		}
		fmt.Fprintf(&b, "| 前瞻批次 | 已评价 | M2′ 命中 | M1 命中 | 读数点 | p 值 | 判定 |\n| --- | --- | --- | --- | --- | --- | --- |\n| %d | %d | %d | %d | %s | %s | %s |\n", c.E2.ProspectiveBatches, c.E2.Evaluated, c.E2.M2Hits, c.E2.M1Hits, c.E2.ReadPoint, p, c.E2.Verdict)
	}
	return b.String()
}

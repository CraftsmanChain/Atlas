package gpurisk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// E2 prime implements the frozen pre-registration
// docs/gpu-e2-prime-preregistration-2026-11-13.md (Atlas-Docs). Any change to
// the decision rules below requires a new pre-registration version.
const E2PrimeVersion = "e2-prime-prereg-2026-11-13"

// E2PrimeStart: only incidents with onset at or after the first prospective
// shadow cutoff are evaluated (2026-11-14 02:00+08).
var E2PrimeStart = time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)

const (
	e2FirstRead = 12
	e2FinalRead = 20
)

type E2Model struct {
	N        int     `json:"n"`
	K        int     `json:"k"`
	Hit      bool    `json:"hit"`      // decisive (deterministic hash tie-break)
	Expected float64 `json:"expected"` // expected-value tie handling (reference)
	Rank     int     `json:"rank"`
	Scored   bool    `json:"scored"`
}

type E2Episode struct {
	Key         string    `json:"key"`
	NodeIP      string    `json:"node_ip"`
	Onset       time.Time `json:"onset"`
	OnsetEnd    time.Time `json:"onset_end"`
	FeedbackIDs []uint    `json:"feedback_ids"`
	Status      string    `json:"status"` // evaluated | out_of_scope | no_prospective_ranking | ambiguous
	QueryID     uint      `json:"query_id,omitempty"`
	Cutoff      time.Time `json:"cutoff,omitempty"`
	M2          *E2Model  `json:"m2_prime,omitempty"`
	M1          *E2Model  `json:"m1_health_inverse,omitempty"`
	M0          float64   `json:"m0_uniform_probability,omitempty"`
}

type E2Report struct {
	Version            string      `json:"version"`
	Strategy           string      `json:"strategy"`
	GeneratedAt        time.Time   `json:"generated_at"`
	Start              time.Time   `json:"start"`
	ProspectiveBatches int         `json:"prospective_batches"`
	FirstProspective   *time.Time  `json:"first_prospective_cutoff,omitempty"`
	Episodes           []E2Episode `json:"episodes"`
	Evaluated          int         `json:"evaluated"`
	OutOfScope         int         `json:"out_of_scope"`
	NoRanking          int         `json:"no_prospective_ranking"`
	Ambiguous          int         `json:"ambiguous"`
	M2Hits             int         `json:"m2_hits"`
	M2Expected         float64     `json:"m2_expected_hits"`
	M1Hits             int         `json:"m1_hits"`
	M0Expected         float64     `json:"m0_expected_hits"`
	ReadPoint          string      `json:"read_point"` // none | first | final
	PValue             *float64    `json:"p_value,omitempty"`
	Verdict            string      `json:"verdict"`
	UnreviewedFeedback []uint      `json:"unreviewed_feedback_since_start"`
}

func e2TieKey(queryKey string, nodeID uint, gen int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", queryKey, nodeID, gen)))
	return hex.EncodeToString(h[:])
}

type e2Ranked struct {
	NodeID uint
	Gen    int
	Score  float64
	Tie    string
}

// e2Evaluate places one target member in a ranking: K = ceil(0.05 N) over
// scored members; decisive membership uses the score order then the
// pre-registered hash; Expected uses proportional tie inclusion.
func e2Evaluate(rows []e2Ranked, nodeID uint, gen int) *E2Model {
	n := len(rows)
	m := &E2Model{N: n}
	if n == 0 {
		return m
	}
	m.K = int(math.Ceil(0.05 * float64(n)))
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Score != rows[j].Score {
			return rows[i].Score > rows[j].Score
		}
		return rows[i].Tie < rows[j].Tie
	})
	var target *e2Ranked
	for i := range rows {
		if rows[i].NodeID == nodeID && rows[i].Gen == gen {
			target = &rows[i]
			m.Rank = i + 1
		}
	}
	if target == nil {
		return m
	}
	m.Scored = true
	m.Hit = m.Rank <= m.K
	higher, tie := 0, 0
	for _, r := range rows {
		if r.Score > target.Score {
			higher++
		} else if r.Score == target.Score {
			tie++
		}
	}
	switch {
	case higher >= m.K:
		m.Expected = 0
	case higher+tie <= m.K:
		m.Expected = 1
	default:
		m.Expected = float64(m.K-higher) / float64(tie)
	}
	return m
}

// PoissonBinomialUpper returns P(X >= x) for independent Bernoulli(p_i).
func PoissonBinomialUpper(p []float64, x int) float64 {
	dist := make([]float64, len(p)+1)
	dist[0] = 1
	for i, pi := range p {
		for k := i + 1; k >= 1; k-- {
			dist[k] = dist[k]*(1-pi) + dist[k-1]*pi
		}
		dist[0] *= 1 - pi
	}
	s := 0.0
	for k := x; k < len(dist); k++ {
		if k >= 0 {
			s += dist[k]
		}
	}
	if s > 1 {
		s = 1
	}
	return s
}

// BuildE2Report evaluates the frozen pre-registration read-only, as of now.
func BuildE2Report(ctx context.Context, db *gorm.DB, now time.Time) (E2Report, error) {
	now = now.UTC().Truncate(time.Microsecond)
	rep := E2Report{Version: E2PrimeVersion, Strategy: ShadowStrategy, GeneratedAt: now, Start: E2PrimeStart, Episodes: []E2Episode{}, UnreviewedFeedback: []uint{}, ReadPoint: "none", Verdict: "accumulating"}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batches []api.GPURiskShadowBatch
		if e := tx.Where("strategy = ? AND timing = ?", ShadowStrategy, "prospective").Order("cutoff").Find(&batches).Error; e != nil {
			return e
		}
		rep.ProspectiveBatches = len(batches)
		if len(batches) > 0 {
			c := batches[0].Cutoff.UTC()
			rep.FirstProspective = &c
		}
		type qinfo struct {
			batch   api.GPURiskShadowBatch
			query   api.GPURiskQuery
			members map[string][]api.GPURiskMember
		}
		qs := []qinfo{}
		for _, b := range batches {
			var q api.GPURiskQuery
			if e := tx.First(&q, b.QueryID).Error; e != nil {
				return e
			}
			var ms []api.GPURiskMember
			if e := tx.Where("query_id = ? AND status = ?", q.ID, "included").Find(&ms).Error; e != nil {
				return e
			}
			byIP := map[string][]api.GPURiskMember{}
			for _, m := range ms {
				byIP[strings.TrimSpace(m.NodeIP)] = append(byIP[strings.TrimSpace(m.NodeIP)], m)
			}
			qs = append(qs, qinfo{b, q, byIP})
		}

		// T-A episodes from current reviews (labels mature after the fact).
		var feedback []api.HardwareFaultFeedbackRequest
		if e := tx.Order("id").Find(&feedback).Error; e != nil {
			return e
		}
		var reviews []api.HardwareFaultFeedbackReview
		if e := tx.Order("feedback_request_id, revision DESC, id DESC").Find(&reviews).Error; e != nil {
			return e
		}
		latest := map[uint]api.HardwareFaultFeedbackReview{}
		for _, r := range reviews {
			if _, ok := latest[r.FeedbackRequestID]; !ok {
				latest[r.FeedbackRequestID] = r
			}
		}
		type inc struct {
			ip   string
			a, b time.Time
			id   uint
		}
		byNode := map[string][]inc{}
		for _, f := range feedback {
			r, ok := latest[f.ID]
			fa := f.FaultOccurredAt
			if f.FaultWindowStartAt != nil {
				fa = *f.FaultWindowStartAt
			}
			if !ok || r.Decision == "needs_evidence" {
				if !fa.Before(E2PrimeStart) {
					rep.UnreviewedFeedback = append(rep.UnreviewedFeedback, f.ID)
				}
				continue
			}
			if r.Decision != "confirmed_hardware" {
				continue
			}
			a, b := reviewWindow(r)
			if a.IsZero() || b.IsZero() || b.Before(a) || a.Before(E2PrimeStart) {
				continue
			}
			ip := strings.TrimSpace(r.ConfirmedNodeIP)
			if ip == "" {
				ip = strings.TrimSpace(f.NodeIP)
			}
			if ip == "" {
				continue
			}
			byNode[ip] = append(byNode[ip], inc{ip, a.UTC(), b.UTC(), f.ID})
		}
		ips := []string{}
		for ip := range byNode {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		for _, ip := range ips {
			list := byNode[ip]
			sort.Slice(list, func(i, j int) bool { return list[i].a.Before(list[j].a) })
			var cur *E2Episode
			for _, x := range list {
				if cur != nil && x.a.Sub(cur.Onset) <= shadowEpisodeMerge {
					cur.FeedbackIDs = append(cur.FeedbackIDs, x.id)
					if x.b.After(cur.OnsetEnd) {
						cur.OnsetEnd = x.b
					}
					continue
				}
				if cur != nil {
					rep.Episodes = append(rep.Episodes, *cur)
				}
				cur = &E2Episode{Key: fmt.Sprintf("%s@%s", ip, x.a.Format(time.RFC3339)), NodeIP: ip, Onset: x.a, OnsetEnd: x.b, FeedbackIDs: []uint{x.id}}
			}
			if cur != nil {
				rep.Episodes = append(rep.Episodes, *cur)
			}
		}

		pvals := []float64{}
		for i := range rep.Episodes {
			e := &rep.Episodes[i]
			// Last prospective query with cutoff < onset where the node is included.
			var chosen *qinfo
			anyBefore := false
			for j := range qs {
				if !qs[j].batch.Cutoff.Before(e.Onset) {
					continue
				}
				anyBefore = true
				if len(qs[j].members[e.NodeIP]) > 0 {
					chosen = &qs[j]
				}
			}
			switch {
			case chosen == nil && anyBefore:
				e.Status = "out_of_scope"
				rep.OutOfScope++
				continue
			case chosen == nil:
				e.Status = "no_prospective_ranking"
				rep.NoRanking++
				continue
			}
			end := chosen.batch.Cutoff.Add(7 * 24 * time.Hour)
			e.QueryID, e.Cutoff = chosen.query.ID, chosen.batch.Cutoff.UTC()
			if e.Onset.After(end) {
				e.Status = "no_prospective_ranking"
				rep.NoRanking++
				continue
			}
			if e.OnsetEnd.After(end) {
				e.Status = "ambiguous"
				rep.Ambiguous++
				continue
			}
			m := chosen.members[e.NodeIP][0]
			var rows []api.GPURiskShadowScore
			if err := tx.Where("batch_id = ? AND raw_score IS NOT NULL", chosen.batch.ID).Find(&rows).Error; err != nil {
				return err
			}
			r2 := make([]e2Ranked, 0, len(rows))
			for _, r := range rows {
				r2 = append(r2, e2Ranked{r.NodeID, r.Generation, *r.RawScore, e2TieKey(chosen.query.Key, r.NodeID, r.Generation)})
			}
			e.M2 = e2Evaluate(r2, m.NodeID, m.Generation)
			var scores []api.GPURiskScore
			if err := tx.Where("query_id = ? AND policy = ? AND raw_score IS NOT NULL", chosen.query.ID, HealthInverse).Find(&scores).Error; err != nil {
				return err
			}
			r1 := make([]e2Ranked, 0, len(scores))
			for _, s := range scores {
				r1 = append(r1, e2Ranked{s.NodeID, s.Generation, *s.RawScore, e2TieKey(chosen.query.Key, s.NodeID, s.Generation)})
			}
			e.M1 = e2Evaluate(r1, m.NodeID, m.Generation)
			e.Status = "evaluated"
			rep.Evaluated++
			if e.M2.N > 0 {
				e.M0 = float64(e.M2.K) / float64(e.M2.N)
			}
			pvals = append(pvals, e.M0)
			rep.M0Expected += e.M0
			rep.M2Expected += e.M2.Expected
			if e.M2.Hit {
				rep.M2Hits++
			}
			if e.M1.Hit {
				rep.M1Hits++
			}
		}
		if rep.Evaluated >= e2FirstRead {
			rep.ReadPoint = "first"
			if rep.Evaluated >= e2FinalRead {
				rep.ReadPoint = "final"
			}
			p := PoissonBinomialUpper(pvals, rep.M2Hits)
			rep.PValue = &p
			switch {
			case p < 0.01 && rep.M2Hits >= rep.M1Hits:
				rep.Verdict = "pass"
			case p < 0.01:
				rep.Verdict = "significant_but_not_better_than_m1"
			case rep.ReadPoint == "final" && p >= 0.05:
				rep.Verdict = "stop_condition_triggered"
			case rep.ReadPoint == "final":
				rep.Verdict = "weak_evidence_human_decision"
			default:
				rep.Verdict = "not_yet_significant"
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return rep, err
}

// RenderE2Markdown renders a human report (Chinese) from an E2Report.
func RenderE2Markdown(r E2Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# E2′ 前瞻进度（%s）\n\n", r.GeneratedAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "预登记 %s；策略 %s；前瞻批次 %d。\n\n", r.Version, r.Strategy, r.ProspectiveBatches)
	fmt.Fprintf(&b, "| 指标 | 值 |\n| --- | --- |\n| 已评价独立 episode | %d |\n| 范围外 / 无前瞻排名 / 跨边界 | %d / %d / %d |\n| M2′ 命中（判定口径） | %d（期望值口径 %.2f） |\n| M1 健康反分命中 | %d |\n| M0 均匀期望 | %.2f |\n| 读数点 | %s |\n", r.Evaluated, r.OutOfScope, r.NoRanking, r.Ambiguous, r.M2Hits, r.M2Expected, r.M1Hits, r.M0Expected, r.ReadPoint)
	if r.PValue != nil {
		fmt.Fprintf(&b, "| p 值（单侧 Poisson-binomial） | %.4g |\n", *r.PValue)
	}
	fmt.Fprintf(&b, "| 判定 | %s |\n\n", r.Verdict)
	if len(r.Episodes) > 0 {
		b.WriteString("| episode | 状态 | query | M2′ 名次/K | M1 名次/K |\n| --- | --- | --- | --- | --- |\n")
		for _, e := range r.Episodes {
			m2, m1 := "-", "-"
			if e.M2 != nil {
				m2 = fmt.Sprintf("%d/%d", e.M2.Rank, e.M2.K)
			}
			if e.M1 != nil {
				m1 = fmt.Sprintf("%d/%d", e.M1.Rank, e.M1.K)
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %s |\n", e.Key, e.Status, e.QueryID, m2, m1)
		}
		b.WriteString("\n")
	}
	if len(r.UnreviewedFeedback) > 0 {
		fmt.Fprintf(&b, "待审核反馈（预登记期内）：%v\n", r.UnreviewedFeedback)
	}
	return b.String()
}

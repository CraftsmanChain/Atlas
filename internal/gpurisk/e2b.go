package gpurisk

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// E2-B (docs/gpu-e2b-preregistration-2026-11-16.md): pre-registered prospective
// test of the plan-v5 shadow routes on automatic core-event episodes.
//
//   - Episode: CoreEpisodes over ledger events Atlas knew at the readout
//     instant (imported_at <= asOf), start in [E2BEpisodeFrom, asOf-E2BLag).
//   - Ranked: for route r, the prospective batches with cutoff in
//     [start-7d, start) that scored the node. The analysis set is the episodes
//     ranked by every family route (same episodes for all tests).
//   - Statistic: mean over episodes of the node's mid-rank percentile averaged
//     over its ranked batches. Null: the failing node is a uniformly random
//     node among those scored in all of that episode's batches (node-label
//     permutation; keeps each route's day-to-day persistence). One-sided Monte
//     Carlo p = (1+#{T* >= T})/(B+1), fixed seed, Holm over the family.
//   - Looks: the first Monday 09:00 readout with n >= 12 (interim, pass needs
//     Holm p < 0.001) and the first with n >= 20 (final, pass needs < 0.01).
//     No other p-values are reported.
const E2BVersion = "gpu-e2b-v1"

var (
	e2bDraws       = E2BDraws
	E2BEpisodeFrom = time.Date(2026, 11, 16, 2, 0, 0, 0, shanghai)
	E2BFamily      = []string{CoreRecurrenceStrategy, CoreCohortStrategy, ShadowStrategy}
	e2bFirstLook   = time.Date(2026, 11, 23, 9, 0, 0, 0, shanghai) // first Monday readout
)

const (
	E2BLag          = 48 * time.Hour
	E2BInterimN     = 12
	E2BFinalN       = 20
	E2BInterimAlpha = 0.001
	E2BFinalAlpha   = 0.01
	E2BWeakAlpha    = 0.05
	E2BDraws        = 100000 // pre-registered; tests lower e2bDraws for speed
	e2bSeed         = 20261116
)

type E2BEpisode struct {
	NodeIP    string    `json:"node_ip"`
	Start     time.Time `json:"start"`
	Codes     []string  `json:"codes"`
	FirstTime bool      `json:"first_time"`
	Ranked    bool      `json:"ranked_by_all_routes"`
}

type E2BRoute struct {
	Strategy     string  `json:"strategy"`
	Episodes     int     `json:"episodes"`
	MeanPct      float64 `json:"mean_percentile"`
	Capture      float64 `json:"top5_capture"`
	CaptureLow   float64 `json:"top5_capture_low"`
	CaptureHigh  float64 `json:"top5_capture_high"`
	FirstCapture float64 `json:"first_time_top5_capture"`
	RecurCapture float64 `json:"recurrent_top5_capture"`
	P            float64 `json:"p_one_sided"`
	PHolm        float64 `json:"p_holm"`
	Pass         bool    `json:"pass"`
}

type E2BLook struct {
	Kind     string     `json:"kind"` // interim | final
	AsOf     time.Time  `json:"as_of"`
	N        int        `json:"n"`
	Alpha    float64    `json:"alpha"`
	Routes   []E2BRoute `json:"routes"`
	PairedD  float64    `json:"paired_r4_minus_r1_mean_pct"`
	PairedP  float64    `json:"paired_r4_gt_r1_p"`
	Decision string     `json:"decision"`
	Winner   string     `json:"winner,omitempty"`
}

type E2BReport struct {
	Version   string       `json:"version"`
	AsOf      time.Time    `json:"as_of"`
	N         int          `json:"n"` // ranked episodes as of AsOf (descriptive)
	NoRanking int          `json:"no_ranking"`
	Episodes  []E2BEpisode `json:"episodes"`
	Looks     []E2BLook    `json:"looks"`
	State     string       `json:"state"` // accumulating | interim_done | final_done
}

// e2bScored is one route's per-episode result: x = observed averaged
// percentile, null = averaged percentile of every pool node, credit = expected
// top-5% credit of the failing node.
type e2bScored struct {
	x, credit float64
	null      []float64
}

type e2bBatch struct {
	cutoff time.Time
	pct    map[string]float64
	credit map[string]float64
}

func e2bBatchStats(scores map[string]float64) e2bBatch {
	all := make([]float64, 0, len(scores))
	for _, s := range scores {
		all = append(all, s)
	}
	b := e2bBatch{pct: map[string]float64{}, credit: map[string]float64{}}
	for ip, s := range scores {
		b.pct[ip], b.credit[ip] = routeCredit(all, s)
	}
	return b
}

// e2bData loads episodes (as of asOf) and every family route's prospective batches.
func e2bData(tx *gorm.DB, asOf time.Time) ([]E2BEpisode, map[string][]e2bBatch, error) {
	var events []api.GPUCoreEvent
	if e := tx.Where(timeSQL(tx, "imported_at", "<="), asOf.UTC()).Order("id").Find(&events).Error; e != nil {
		return nil, nil, e
	}
	eps := []E2BEpisode{}
	for ip, list := range CoreEpisodes(events) {
		for i, e := range list {
			if e.Start.Before(E2BEpisodeFrom) || !e.Start.Before(asOf.Add(-E2BLag)) {
				continue
			}
			eps = append(eps, E2BEpisode{NodeIP: ip, Start: e.Start.UTC(), Codes: e.Codes, FirstTime: i == 0})
		}
	}
	sort.Slice(eps, func(i, j int) bool {
		if !eps[i].Start.Equal(eps[j].Start) {
			return eps[i].Start.Before(eps[j].Start)
		}
		return eps[i].NodeIP < eps[j].NodeIP
	})
	routes := map[string][]e2bBatch{}
	for _, st := range E2BFamily {
		var batches []api.GPURiskShadowBatch
		if e := tx.Where("strategy = ? AND timing = ? AND "+timeSQL(tx, "cutoff", ">=")+" AND "+timeSQL(tx, "cutoff", "<"), st, "prospective", RouteScoreStart.UTC(), asOf.UTC()).Order("cutoff").Find(&batches).Error; e != nil {
			return nil, nil, e
		}
		for _, b := range batches {
			var rows []api.GPURiskShadowScore
			if e := tx.Where("batch_id = ?", b.ID).Find(&rows).Error; e != nil {
				return nil, nil, e
			}
			var members []api.GPURiskMember
			if e := tx.Where("query_id = ?", b.QueryID).Find(&members).Error; e != nil {
				return nil, nil, e
			}
			ipOf := map[string]string{}
			for _, m := range members {
				ipOf[fmt.Sprint(m.NodeID, "/", m.Generation)] = strings.TrimSpace(m.NodeIP)
			}
			scores := map[string]float64{}
			for _, x := range rows {
				if x.RawScore == nil {
					continue
				}
				if ip := ipOf[fmt.Sprint(x.NodeID, "/", x.Generation)]; ip != "" {
					scores[ip] = *x.RawScore
				}
			}
			eb := e2bBatchStats(scores)
			eb.cutoff = b.Cutoff
			routes[st] = append(routes[st], eb)
		}
	}
	return eps, routes, nil
}

// e2bScore scores one episode under one route; ok=false if no batch ranked the node.
func e2bScore(e E2BEpisode, batches []e2bBatch) (e2bScored, bool) {
	var use []e2bBatch
	for _, b := range batches {
		if !b.cutoff.Before(e.Start) || b.cutoff.Before(e.Start.Add(-routeHorizon)) {
			continue
		}
		if _, ok := b.pct[e.NodeIP]; ok {
			use = append(use, b)
		}
	}
	if len(use) == 0 {
		return e2bScored{}, false
	}
	var out e2bScored
	for _, b := range use {
		out.x += b.pct[e.NodeIP]
		out.credit += b.credit[e.NodeIP]
	}
	n := float64(len(use))
	out.x, out.credit = out.x/n, out.credit/n
	pool := []string{}
	for ip := range use[0].pct {
		in := true
		for _, b := range use[1:] {
			if _, ok := b.pct[ip]; !ok {
				in = false
				break
			}
		}
		if in {
			pool = append(pool, ip)
		}
	}
	sort.Strings(pool)
	for _, ip := range pool {
		v := 0.0
		for _, b := range use {
			v += b.pct[ip]
		}
		out.null = append(out.null, v/n)
	}
	return out, true
}

// e2bPermP: one-sided Monte Carlo p for mean(x) under independent per-episode null draws.
func e2bPermP(sc []e2bScored, seed uint64) float64 {
	if len(sc) == 0 {
		return 1
	}
	obs := 0.0
	for _, s := range sc {
		obs += s.x
	}
	r := rand.New(rand.NewPCG(e2bSeed, seed))
	ge := 0
	for d := 0; d < e2bDraws; d++ {
		t := 0.0
		for _, s := range sc {
			t += s.null[r.IntN(len(s.null))]
		}
		if t >= obs-1e-9 {
			ge++
		}
	}
	return float64(1+ge) / float64(e2bDraws+1)
}

// e2bSignFlipP: one-sided paired sign-flip p that mean(d) > 0.
func e2bSignFlipP(d []float64, seed uint64) float64 {
	if len(d) == 0 {
		return 1
	}
	obs := 0.0
	for _, v := range d {
		obs += v
	}
	r := rand.New(rand.NewPCG(e2bSeed, seed))
	ge := 0
	for k := 0; k < e2bDraws; k++ {
		t := 0.0
		for _, v := range d {
			if r.IntN(2) == 0 {
				t += v
			} else {
				t -= v
			}
		}
		if t >= obs-1e-9 {
			ge++
		}
	}
	return float64(1+ge) / float64(e2bDraws+1)
}

// holm returns Holm step-down adjusted p-values.
func holm(p []float64) []float64 {
	idx := make([]int, len(p))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return p[idx[a]] < p[idx[b]] })
	out := make([]float64, len(p))
	run := 0.0
	for rank, i := range idx {
		v := math.Min(1, float64(len(p)-rank)*p[i])
		run = math.Max(run, v)
		out[i] = run
	}
	return out
}

// e2bAnalyse computes the analysis set and per-route results. withP=false
// skips the permutation tests (non-look weeks report descriptives only).
func e2bAnalyse(eps []E2BEpisode, routes map[string][]e2bBatch, withP bool) ([]E2BEpisode, []E2BRoute, map[string][]e2bScored) {
	scored := map[string][]e2bScored{}
	for i := range eps {
		all := true
		per := map[string]e2bScored{}
		for _, st := range E2BFamily {
			s, ok := e2bScore(eps[i], routes[st])
			if !ok {
				all = false
				break
			}
			per[st] = s
		}
		eps[i].Ranked = all
		if all {
			for _, st := range E2BFamily {
				scored[st] = append(scored[st], per[st])
			}
		}
	}
	var res []E2BRoute
	var ps []float64
	for k, st := range E2BFamily {
		sc := scored[st]
		r := E2BRoute{Strategy: st, Episodes: len(sc), P: 1}
		nf, nr := 0, 0
		j := 0
		for i := range eps {
			if !eps[i].Ranked {
				continue
			}
			s := sc[j]
			j++
			r.MeanPct += s.x
			r.Capture += s.credit
			if eps[i].FirstTime {
				nf++
				r.FirstCapture += s.credit
			} else {
				nr++
				r.RecurCapture += s.credit
			}
		}
		if n := len(sc); n > 0 {
			r.MeanPct /= float64(n)
			r.Capture /= float64(n)
			r.CaptureLow, r.CaptureHigh = wilson(r.Capture, n)
			if withP {
				r.P = e2bPermP(sc, uint64(k+1))
			}
		}
		if nf > 0 {
			r.FirstCapture /= float64(nf)
		}
		if nr > 0 {
			r.RecurCapture /= float64(nr)
		}
		res = append(res, r)
		ps = append(ps, r.P)
	}
	for i, v := range holm(ps) {
		res[i].PHolm = v
	}
	return eps, res, scored
}

func e2bLook(kind string, asOf time.Time, eps []E2BEpisode, routes map[string][]e2bBatch) E2BLook {
	eps, res, scored := e2bAnalyse(eps, routes, true)
	alpha := E2BFinalAlpha
	if kind == "interim" {
		alpha = E2BInterimAlpha
	}
	l := E2BLook{Kind: kind, AsOf: asOf.UTC(), N: len(scored[E2BFamily[0]]), Alpha: alpha, Routes: res, PairedP: 1}
	r1, r4 := scored[CoreRecurrenceStrategy], scored[CoreCohortStrategy]
	d := make([]float64, len(r1))
	for i := range r1 {
		d[i] = r4[i].x - r1[i].x
		l.PairedD += d[i]
	}
	if len(d) > 0 {
		l.PairedD /= float64(len(d))
		l.PairedP = e2bSignFlipP(d, 99)
	}
	best, bestPct, minHolm := "", -1.0, 1.0
	for i := range l.Routes {
		r := &l.Routes[i]
		r.Pass = r.PHolm < alpha
		minHolm = math.Min(minHolm, r.PHolm)
		if r.Pass && r.MeanPct > bestPct {
			best, bestPct = r.Strategy, r.MeanPct
		}
	}
	// Parsimony: R4 only replaces a passing R1 if it is better on the same episodes (paired p < 0.05).
	if best == CoreCohortStrategy && l.Routes[0].Pass && l.PairedP >= 0.05 {
		best = CoreRecurrenceStrategy
	}
	switch {
	case best != "":
		l.Decision, l.Winner = "pass", best
	case kind == "interim":
		l.Decision = "continue"
	case minHolm < E2BWeakAlpha:
		l.Decision = "weak_evidence"
	default:
		l.Decision = "stop"
	}
	return l
}

// BuildE2B is read-only. Looks are found statelessly: each Monday 09:00 from
// the first readout up to asOf is checked for n (episodes ranked by all routes,
// as Atlas knew them at that Monday); the first with n >= 12 is the interim
// look, the first with n >= 20 the final. An interim pass ends the test.
func BuildE2B(ctx context.Context, db *gorm.DB, asOf time.Time) (E2BReport, error) {
	rep := E2BReport{Version: E2BVersion, AsOf: asOf.UTC(), Episodes: []E2BEpisode{}, Looks: []E2BLook{}, State: "accumulating"}
	if !db.Migrator().HasTable(&api.GPUCoreEvent{}) || !db.Migrator().HasTable(&api.GPURiskShadowBatch{}) {
		return rep, nil
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		interim, final := false, false
		for m := e2bFirstLook; !m.After(asOf) && !final; m = m.Add(7 * 24 * time.Hour) {
			eps, routes, e := e2bData(tx, m)
			if e != nil {
				return e
			}
			_, res, _ := e2bAnalyse(eps, routes, false)
			n := res[0].Episodes
			if !interim && n >= E2BInterimN {
				interim = true
				l := e2bLook("interim", m, eps, routes)
				rep.Looks = append(rep.Looks, l)
				rep.State = "interim_done"
				if l.Decision == "pass" {
					final = true
					rep.State = "final_done"
					break
				}
			}
			if n >= E2BFinalN {
				final = true
				rep.Looks = append(rep.Looks, e2bLook("final", m, eps, routes))
				rep.State = "final_done"
			}
		}
		eps, routes, e := e2bData(tx, asOf)
		if e != nil {
			return e
		}
		eps, res, _ := e2bAnalyse(eps, routes, false)
		rep.Episodes, rep.N = eps, res[0].Episodes
		rep.NoRanking = len(eps) - rep.N
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return rep, err
}

// RenderE2BMarkdown renders the E2-B section of the weekly report.
func RenderE2BMarkdown(r E2BReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## E2-B 前瞻检验（%s，预登记 gpu-e2b-preregistration-2026-11-16）\n\n", r.Version)
	fmt.Fprintf(&b, "截至 %s（故障需早于此刻 48 小时）：三条路线都有前瞻排名的故障 %d 次，无前瞻排名 %d 次；首读需 %d 次、终读需 %d 次。状态：%s。读数点以外不计算 p 值。\n", r.AsOf.In(shanghai).Format("2006-01-02 15:04"), r.N, r.NoRanking, E2BInterimN, E2BFinalN, r.State)
	for _, l := range r.Looks {
		name := "首读"
		if l.Kind == "final" {
			name = "终读"
		}
		fmt.Fprintf(&b, "\n### %s（截至 %s，n=%d，Holm 校正后 p < %g 通过）\n\n| 路线 | 平均百分位 | p（单侧） | Holm p | Top5%% 捕获 | 95%% 区间 | 首次 | 复发 | 通过 |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n", name, l.AsOf.In(shanghai).Format("2006-01-02 15:04"), l.N, l.Alpha)
		for _, x := range l.Routes {
			fmt.Fprintf(&b, "| %s | %.3f | %.4f | %.4f | %.3f | %.2f–%.2f | %.3f | %.3f | %v |\n", x.Strategy, x.MeanPct, x.P, x.PHolm, x.Capture, x.CaptureLow, x.CaptureHigh, x.FirstCapture, x.RecurCapture, x.Pass)
		}
		fmt.Fprintf(&b, "\nR4−R1 平均百分位差 %.3f（配对符号翻转单侧 p=%.4f）。判定：%s", l.PairedD, l.PairedP, l.Decision)
		if l.Winner != "" {
			fmt.Fprintf(&b, "，选定路线 %s", l.Winner)
		}
		b.WriteString("。\n")
	}
	return b.String()
}

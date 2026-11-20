package gpurisk

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// Route scorecard (plan v5 §4): every shadow route is scored on the same
// prospective core episodes. An episode on node n counts for each prospective
// batch whose cutoff lies in [start-7d, start) and that scored n. Per episode
// the route's percentile and expected top-5% capture (random tie breaking) are
// averaged over those batches, so each failure counts once.
const RouteScorecardVersion = "gpu-route-scorecard-v1"

// RouteScoreStart: first day all plan-v5 routes freeze prospective batches.
var RouteScoreStart = time.Date(2026, 11, 16, 0, 0, 0, 0, shanghai)

const routeHorizon = 7 * 24 * time.Hour

type RouteScore struct {
	Strategy     string  `json:"strategy"`
	Batches      int     `json:"prospective_batches"`
	Episodes     int     `json:"episodes"`
	FirstTime    int     `json:"first_time_episodes"`
	MeanPct      float64 `json:"mean_percentile"`
	Capture      float64 `json:"top5_capture"`
	CaptureLow   float64 `json:"top5_capture_low"`
	CaptureHigh  float64 `json:"top5_capture_high"`
	FirstCapture float64 `json:"first_time_top5_capture"`
	RecurCapture float64 `json:"recurrent_top5_capture"`
	// Split statistics (2026-11-20): first-time and recurrent episodes are
	// reported separately because history-based routes cannot rank a node
	// before its first core episode.
	Recurrent    int     `json:"recurrent_episodes"`
	FirstMeanPct float64 `json:"first_time_mean_percentile"`
	RecurMeanPct float64 `json:"recurrent_mean_percentile"`
	FirstLow     float64 `json:"first_time_top5_capture_low"`
	FirstHigh    float64 `json:"first_time_top5_capture_high"`
	RecurLow     float64 `json:"recurrent_top5_capture_low"`
	RecurHigh    float64 `json:"recurrent_top5_capture_high"`
}

type RouteEpisode struct {
	NodeIP    string    `json:"node_ip"`
	Start     time.Time `json:"start"`
	Codes     []string  `json:"codes"`
	FirstTime bool      `json:"first_time"`
	Batches   int       `json:"batches"`
}

type RouteScorecard struct {
	Version  string         `json:"version"`
	Start    time.Time      `json:"start"`
	AsOf     time.Time      `json:"as_of"`
	Routes   []RouteScore   `json:"routes"`
	Episodes []RouteEpisode `json:"episodes"`
}

// routeCredit returns the percentile and expected top-5% credit of s in scores.
func routeCredit(scores []float64, s float64) (float64, float64) {
	n := len(scores)
	if n <= 1 {
		return 0, 0
	}
	lower, higher, tie := 0, 0, 0
	for _, o := range scores {
		switch {
		case o < s:
			lower++
		case o > s:
			higher++
		default:
			tie++
		}
	}
	pct := (float64(lower) + float64(tie-1)/2) / float64(n-1)
	k := int(math.Ceil(0.05 * float64(n)))
	credit := 0.0
	if higher+tie <= k {
		credit = 1
	} else if higher < k {
		credit = float64(k-higher) / float64(tie)
	}
	return pct, credit
}

// wilson gives a 95% interval for a mean of per-episode credits in [0,1].
func wilson(mean float64, n int) (float64, float64) {
	if n == 0 {
		return 0, 0
	}
	z, fn := 1.96, float64(n)
	d := 1 + z*z/fn
	c := (mean + z*z/(2*fn)) / d
	h := z * math.Sqrt(mean*(1-mean)/fn+z*z/(4*fn*fn)) / d
	return math.Max(0, c-h), math.Min(1, c+h)
}

// BuildRouteScorecard is read-only.
func BuildRouteScorecard(ctx context.Context, db *gorm.DB, now time.Time) (RouteScorecard, error) {
	sc := RouteScorecard{Version: RouteScorecardVersion, Start: RouteScoreStart.UTC(), AsOf: now.UTC(), Routes: []RouteScore{}, Episodes: []RouteEpisode{}}
	if !db.Migrator().HasTable(&api.GPUCoreEvent{}) {
		return sc, nil
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []api.GPUCoreEvent
		if e := tx.Where(timeSQL(tx, "observed_at", "<"), now.UTC()).Order("id").Find(&events).Error; e != nil {
			return e
		}
		all := CoreEpisodes(events)
		eps := []RouteEpisode{}
		for ip, list := range all {
			for i, e := range list {
				if e.Start.Before(RouteScoreStart) {
					continue
				}
				eps = append(eps, RouteEpisode{NodeIP: ip, Start: e.Start.UTC(), Codes: e.Codes, FirstTime: i == 0})
			}
		}
		sort.Slice(eps, func(i, j int) bool { return eps[i].Start.Before(eps[j].Start) })
		for _, st := range ShadowStrategies {
			var batches []api.GPURiskShadowBatch
			if e := tx.Where("strategy = ? AND timing = ? AND "+timeSQL(tx, "cutoff", ">="), st, "prospective", RouteScoreStart.UTC()).Order("cutoff").Find(&batches).Error; e != nil {
				return e
			}
			r := RouteScore{Strategy: st, Batches: len(batches)}
			type nodeScores struct {
				byIP   map[string]float64
				scores []float64
			}
			cache := map[uint]*nodeScores{}
			load := func(b api.GPURiskShadowBatch) (*nodeScores, error) {
				if c, ok := cache[b.ID]; ok {
					return c, nil
				}
				var rows []api.GPURiskShadowScore
				if e := tx.Where("batch_id = ?", b.ID).Find(&rows).Error; e != nil {
					return nil, e
				}
				var members []api.GPURiskMember
				if e := tx.Where("query_id = ?", b.QueryID).Find(&members).Error; e != nil {
					return nil, e
				}
				ipOf := map[string]string{}
				for _, m := range members {
					ipOf[fmt.Sprint(m.NodeID, "/", m.Generation)] = strings.TrimSpace(m.NodeIP)
				}
				c := &nodeScores{byIP: map[string]float64{}}
				for _, x := range rows {
					if x.RawScore == nil {
						continue
					}
					c.scores = append(c.scores, *x.RawScore)
					c.byIP[ipOf[fmt.Sprint(x.NodeID, "/", x.Generation)]] = *x.RawScore
				}
				cache[b.ID] = c
				return c, nil
			}
			var sumPct, sumCap, sumFirst, sumRecur, pctFirst, pctRecur float64
			nFirst, nRecur := 0, 0
			for i := range eps {
				e := &eps[i]
				pct, cap, n := 0.0, 0.0, 0
				for _, b := range batches {
					if !b.Cutoff.Before(e.Start) || b.Cutoff.Before(e.Start.Add(-routeHorizon)) {
						continue
					}
					c, err := load(b)
					if err != nil {
						return err
					}
					s, ok := c.byIP[e.NodeIP]
					if !ok {
						continue
					}
					p, k := routeCredit(c.scores, s)
					pct, cap, n = pct+p, cap+k, n+1
				}
				if st == ShadowStrategies[0] {
					e.Batches = n
				}
				if n == 0 {
					continue
				}
				r.Episodes++
				sumPct += pct / float64(n)
				sumCap += cap / float64(n)
				if e.FirstTime {
					nFirst++
					sumFirst += cap / float64(n)
					pctFirst += pct / float64(n)
				} else {
					nRecur++
					sumRecur += cap / float64(n)
					pctRecur += pct / float64(n)
				}
			}
			if r.Episodes > 0 {
				r.MeanPct, r.Capture = sumPct/float64(r.Episodes), sumCap/float64(r.Episodes)
				r.CaptureLow, r.CaptureHigh = wilson(r.Capture, r.Episodes)
			}
			r.FirstTime, r.Recurrent = nFirst, nRecur
			if nFirst > 0 {
				r.FirstCapture, r.FirstMeanPct = sumFirst/float64(nFirst), pctFirst/float64(nFirst)
				r.FirstLow, r.FirstHigh = wilson(r.FirstCapture, nFirst)
			}
			if nRecur > 0 {
				r.RecurCapture, r.RecurMeanPct = sumRecur/float64(nRecur), pctRecur/float64(nRecur)
				r.RecurLow, r.RecurHigh = wilson(r.RecurCapture, nRecur)
			}
			sc.Routes = append(sc.Routes, r)
		}
		sc.Episodes = eps
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return sc, err
}

// RenderRouteScorecardMarkdown renders the scorecard section.
func RenderRouteScorecardMarkdown(sc RouteScorecard) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## 路线记分卡（计划 v5，%s）\n\n自 %s 起的前瞻批次；关键故障来自双来源账本，每次故障只计一次（在其前 7 天内各批次上取平均）。R0 均匀随机的期望为平均百分位 0.50、Top5%% 捕获 0.05。\n\n", sc.Version, sc.Start.In(shanghai).Format("2006-01-02"))
	b.WriteString("| 路线 | 前瞻批次 | 故障数 | 其中首次 | 平均百分位 | Top5% 捕获 | 95% 区间 | 首次故障捕获 | 复发故障捕获 |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range sc.Routes {
		if r.Episodes == 0 {
			fmt.Fprintf(&b, "| %s | %d | 0 | 0 | - | - | - | - | - |\n", r.Strategy, r.Batches)
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %.3f | %.3f | %.2f–%.2f | %s | %s |\n", r.Strategy, r.Batches, r.Episodes, r.FirstTime, r.MeanPct, r.Capture, r.CaptureLow, r.CaptureHigh, splitCell(r.FirstTime, r.FirstCapture), splitCell(r.Recurrent, r.RecurCapture))
	}
	if len(sc.Episodes) > 0 {
		b.WriteString("\n前瞻期关键故障：\n\n")
		for _, e := range sc.Episodes {
			kind := "复发"
			if e.FirstTime {
				kind = "首次"
			}
			fmt.Fprintf(&b, "- %s %s %s（%s，覆盖批次 %d）\n", e.Start.In(shanghai).Format("01-02 15:04"), e.NodeIP, strings.Join(e.Codes, ","), kind, e.Batches)
		}
	}
	return b.String()
}

// splitCell renders a split capture, "-" when the split has no episodes.
func splitCell(n int, v float64) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%.3f", v)
}

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

// Route backtest (plan v5: accumulate, verify, backtest). Read-only replay of
// the frozen R1/R4 scoring rule (coreRouteScore, shared with live shadow) over
// every past day, so the capture-vs-history curve updates itself as nodes and
// failures accumulate. It is retrospective by construction and never feeds a
// pre-registered decision (E2-B uses only frozen prospective batches).
//
// Replay day D, cutoff D 02:00 Asia/Shanghai:
//   - members: nodes of the latest completed query's scope whose DCGM
//     observation window covers D-1 (first_seen <= D-1 <= last_seen); exporter
//     gaps inside the window are not visible here (approximation).
//   - features: ledger events observed before cutoff. imported_at is ignored
//     because most of the ledger was backfilled; kernel-log events exist only
//     from the collector's first window, so early months are VM-only.
//   - cohort age: first_seen_day from gpu_node_observations (as live R4).
//   - outcome: episodes (full ledger) starting in [cutoff, cutoff+7d) on a
//     member; each episode is averaged over its up-to-7 replay days, so it
//     counts once. First-time = no earlier episode on the node.
//   - only days whose 7-day horizon ended at least 48h ago are replayed.
const RouteBacktestVersion = "gpu-route-backtest-v1"

var routeBacktestStart = time.Date(2026, 7, 9, 0, 0, 0, 0, shanghai)

type BacktestRoute struct {
	MeanPct      float64 `json:"mean_percentile"`
	Capture      float64 `json:"top5_capture"`
	FirstCapture float64 `json:"first_time_top5_capture"`
	RecurCapture float64 `json:"recurrent_top5_capture"`
}

type BacktestRow struct {
	Period        string        `json:"period"` // YYYY-MM by episode start, or "all" / "last90d"
	Episodes      int           `json:"episodes"`
	FirstTime     int           `json:"first_time"`
	MedianMembers int           `json:"median_members"`
	R1            BacktestRoute `json:"r1"`
	R4            BacktestRoute `json:"r4"`
	Ceiling       float64       `json:"r1_style_ceiling"` // recurrent share * R1 recurrent capture + first share * 0.05
}

type RouteBacktest struct {
	Version  string        `json:"version"`
	AsOf     time.Time     `json:"as_of"`
	Days     int           `json:"replay_days"`
	FirstDay string        `json:"first_day"`
	LastDay  string        `json:"last_day"`
	Scope    int           `json:"scope_nodes"`
	Rows     []BacktestRow `json:"rows"`
	NoMember int           `json:"episodes_not_member"` // in-scope episodes whose node was not a replay member on any day
}

type btEpisode struct {
	ip         string
	start      time.Time
	first      bool
	pct1, cap1 float64
	pct4, cap4 float64
	n          int
}

// BuildRouteBacktest is read-only.
func BuildRouteBacktest(ctx context.Context, db *gorm.DB, now time.Time) (RouteBacktest, error) {
	bt := RouteBacktest{Version: RouteBacktestVersion, AsOf: now.UTC(), Rows: []BacktestRow{}}
	m := db.Migrator()
	if !m.HasTable(&api.GPUCoreEvent{}) || !m.HasTable(&api.GPUNodeObservation{}) || !m.HasTable(&api.GPURiskQuery{}) {
		return bt, nil
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var q api.GPURiskQuery
		if e := tx.Where("status = ?", "completed").Order("cutoff desc").First(&q).Error; e != nil {
			if e == gorm.ErrRecordNotFound {
				return nil
			}
			return e
		}
		var members []api.GPURiskMember
		if e := tx.Where("query_id = ? AND status = ?", q.ID, "included").Find(&members).Error; e != nil {
			return e
		}
		scope := map[string]bool{}
		for _, mb := range members {
			scope[strings.TrimSpace(mb.NodeIP)] = true
		}
		bt.Scope = len(scope)
		var obsRows []api.GPUNodeObservation
		if e := tx.Order("node_ip").Find(&obsRows).Error; e != nil {
			return e
		}
		obs := map[string]api.GPUNodeObservation{}
		for _, o := range obsRows {
			obs[o.NodeIP] = o
		}
		var events []api.GPUCoreEvent
		if e := tx.Where(timeSQL(tx, "observed_at", "<"), now.UTC()).Order("id").Find(&events).Error; e != nil {
			return e
		}
		all := CoreEpisodes(events)
		// episode list (in scope) and per-node time-sorted episodes for feature replay
		var eps []*btEpisode
		byIP := map[string][]*btEpisode{}
		for ip, list := range all {
			if !scope[ip] {
				continue
			}
			for i, e := range list {
				x := &btEpisode{ip: ip, start: e.Start, first: i == 0}
				eps = append(eps, x)
				byIP[ip] = append(byIP[ip], x)
			}
		}
		lastCut := now.Add(-E2BLag - routeHorizon)
		var memberCounts []int
		monthMembers := map[string][]int{}
		for d := routeBacktestStart; ; d = d.AddDate(0, 0, 1) {
			cutoff := d.Add(2 * time.Hour)
			if cutoff.After(lastCut) {
				break
			}
			prev := d.AddDate(0, 0, -1).Format("2006-01-02")
			var ips []string
			for ip := range scope {
				o, ok := obs[ip]
				if ok && o.FirstSeenDay <= prev && prev <= o.LastSeenDay {
					ips = append(ips, ip)
				}
			}
			if len(ips) < 2 {
				continue
			}
			sort.Strings(ips)
			// features: episodes from events observed before cutoff
			var known []api.GPUCoreEvent
			for _, e := range events {
				if e.ObservedAt.Before(cutoff) {
					known = append(known, e)
				}
			}
			past := CoreEpisodes(known)
			s1 := make([]float64, len(ips))
			s4 := make([]float64, len(ips))
			for i, ip := range ips {
				s1[i], _, _ = coreRouteScore(past[ip], ip, cutoff, false, obs)
				s4[i], _, _ = coreRouteScore(past[ip], ip, cutoff, true, obs)
			}
			bt.Days++
			if bt.FirstDay == "" {
				bt.FirstDay = d.Format("2006-01-02")
			}
			bt.LastDay = d.Format("2006-01-02")
			memberCounts = append(memberCounts, len(ips))
			monthMembers[d.Format("2006-01")] = append(monthMembers[d.Format("2006-01")], len(ips))
			idx := map[string]int{}
			for i, ip := range ips {
				idx[ip] = i
			}
			for ip, list := range byIP {
				i, ok := idx[ip]
				if !ok {
					continue
				}
				for _, x := range list {
					if x.start.Before(cutoff) || !x.start.Before(cutoff.Add(routeHorizon)) {
						continue
					}
					p1, c1 := routeCredit(s1, s1[i])
					p4, c4 := routeCredit(s4, s4[i])
					x.pct1, x.cap1, x.pct4, x.cap4, x.n = x.pct1+p1, x.cap1+c1, x.pct4+p4, x.cap4+c4, x.n+1
				}
			}
		}
		groups := map[string][]*btEpisode{}
		var order []string
		add := func(k string, x *btEpisode) {
			if _, ok := groups[k]; !ok {
				order = append(order, k)
			}
			groups[k] = append(groups[k], x)
		}
		sort.Slice(eps, func(i, j int) bool { return eps[i].start.Before(eps[j].start) })
		for _, x := range eps {
			if x.start.Before(routeBacktestStart.Add(2*time.Hour)) || !x.start.Before(lastCut.Add(routeHorizon)) {
				continue
			}
			if x.n == 0 {
				bt.NoMember++
				continue
			}
			add(x.start.In(shanghai).Format("2006-01"), x)
		}
		sort.Strings(order)
		var allEps, recent []*btEpisode
		for _, k := range order {
			allEps = append(allEps, groups[k]...)
		}
		for _, x := range allEps {
			if !x.start.Before(now.AddDate(0, 0, -90)) {
				recent = append(recent, x)
			}
		}
		row := func(period string, list []*btEpisode, mm []int) BacktestRow {
			r := BacktestRow{Period: period, Episodes: len(list), MedianMembers: median(mm)}
			nf, nr := 0, 0
			for _, x := range list {
				n := float64(x.n)
				r.R1.MeanPct += x.pct1 / n
				r.R1.Capture += x.cap1 / n
				r.R4.MeanPct += x.pct4 / n
				r.R4.Capture += x.cap4 / n
				if x.first {
					nf++
					r.R1.FirstCapture += x.cap1 / n
					r.R4.FirstCapture += x.cap4 / n
				} else {
					nr++
					r.R1.RecurCapture += x.cap1 / n
					r.R4.RecurCapture += x.cap4 / n
				}
			}
			r.FirstTime = nf
			if len(list) > 0 {
				k := float64(len(list))
				r.R1.MeanPct, r.R1.Capture, r.R4.MeanPct, r.R4.Capture = r.R1.MeanPct/k, r.R1.Capture/k, r.R4.MeanPct/k, r.R4.Capture/k
			}
			if nf > 0 {
				r.R1.FirstCapture /= float64(nf)
				r.R4.FirstCapture /= float64(nf)
			}
			if nr > 0 {
				r.R1.RecurCapture /= float64(nr)
				r.R4.RecurCapture /= float64(nr)
			}
			if len(list) > 0 {
				rs := float64(nr) / float64(len(list))
				r.Ceiling = rs*r.R1.RecurCapture + (1-rs)*0.05
			}
			return r
		}
		for _, k := range order {
			bt.Rows = append(bt.Rows, row(k, groups[k], monthMembers[k]))
		}
		bt.Rows = append(bt.Rows, row("all", allEps, memberCounts))
		bt.Rows = append(bt.Rows, row("last90d", recent, nil))
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return bt, err
}

func median(v []int) int {
	if len(v) == 0 {
		return 0
	}
	c := append([]int(nil), v...)
	sort.Ints(c)
	return c[len(c)/2]
}

// RenderRouteBacktestMarkdown renders the backtest section of the weekly report.
func RenderRouteBacktestMarkdown(bt RouteBacktest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## 路线回测（%s，回溯，不参与判定）\n\n", bt.Version)
	if bt.Days == 0 {
		b.WriteString("暂无可回放的日期。\n")
		return b.String()
	}
	fmt.Fprintf(&b, "用线上 R1/R4 同一套打分规则回放 %s 至 %s 共 %d 天（每天 02:00，范围为最新查询的 %d 个节点，当天有 DCGM 观测的节点参与排名）；按故障起始月份分组，每次故障只计一次。随机基线：平均百分位 0.50、Top5%% 捕获 0.05。上限 = 复发占比 × R1 复发捕获 + 首次占比 × 0.05。\n\n", bt.FirstDay, bt.LastDay, bt.Days, bt.Scope)
	b.WriteString("| 时段 | 故障 | 首次 | 每日排名节点 | R1 平均百分位 | R1 Top5% | R4 平均百分位 | R4 Top5% | R1 首次 / 复发 | 上限 |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range bt.Rows {
		name := r.Period
		switch name {
		case "all":
			name = "合计"
		case "last90d":
			name = "近 90 天"
		}
		if r.Episodes == 0 {
			fmt.Fprintf(&b, "| %s | 0 | 0 | %d | - | - | - | - | - | - |\n", name, r.MedianMembers)
			continue
		}
		mm := fmt.Sprint(r.MedianMembers)
		if r.Period == "last90d" {
			mm = "-"
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %.2f | %.2f | %.2f | %.2f | %.2f / %.2f | %.2f |\n", name, r.Episodes, r.FirstTime, mm, r.R1.MeanPct, r.R1.Capture, r.R4.MeanPct, r.R4.Capture, r.R1.FirstCapture, r.R1.RecurCapture, r.Ceiling)
	}
	if bt.NoMember > 0 {
		fmt.Fprintf(&b, "\n另有 %d 次范围内故障发生时节点不在当日排名中（DCGM 观测窗口未覆盖），未计入。\n", bt.NoMember)
	}
	return b.String()
}

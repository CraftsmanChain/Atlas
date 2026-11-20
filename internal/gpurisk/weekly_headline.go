package gpurisk

import (
	"fmt"
	"strings"
)

// Weekly headline (2026-11-20, operator decision 3 of the G0 review): the
// weekly report leads with capture split into recurrent and first-time
// episodes. About three quarters of in-scope core episodes are a node's first
// one, which history-based routes can only rank at random level, so a single
// Top5% number mixes "ranking works" with "fleet composition". The headline is
// descriptive only; the frozen E2-B decision is unchanged.
const WeeklyHeadlineVersion = "gpu-weekly-headline-v1"

// PaperTop5Capture is the paper's six-month online Top5% capture, kept as a
// long-term reference rather than a weekly target.
const PaperTop5Capture = 0.64

// randomTop5 is the expected Top5% capture of a uniform random ranking.
const randomTop5 = 0.05

// RequiredRecurrentShare is the smallest recurrent share at which the paper
// number is reachable when every recurrent episode is captured and first-time
// episodes stay at random level: s*1 + (1-s)*0.05 = 0.64.
func RequiredRecurrentShare() float64 {
	return (PaperTop5Capture - randomTop5) / (1 - randomTop5)
}

var routeLabel = map[string]string{
	ShadowStrategy:         "旧前兆规则",
	CoreRecurrenceStrategy: "R1",
	CoreCohortStrategy:     "R4",
}

func routeName(st string) string {
	if l, ok := routeLabel[st]; ok {
		return l + " " + st
	}
	return st
}

// headlineBacktestRows picks the latest month with episodes and the last-90d row.
func headlineBacktestRows(bt *RouteBacktest) []BacktestRow {
	if bt == nil {
		return nil
	}
	var month *BacktestRow
	var recent *BacktestRow
	for i := range bt.Rows {
		r := &bt.Rows[i]
		switch r.Period {
		case "all":
		case "last90d":
			recent = r
		default:
			if r.Episodes > 0 {
				month = r
			}
		}
	}
	out := []BacktestRow{}
	if month != nil {
		out = append(out, *month)
	}
	if recent != nil && recent.Episodes > 0 {
		out = append(out, *recent)
	}
	return out
}

func splitText(n int, capture, low, high, pct float64) string {
	if n == 0 {
		return "0 次"
	}
	return fmt.Sprintf("%d 次 · %.2f（%.2f–%.2f）· 百分位 %.2f", n, capture, low, high, pct)
}

func periodName(p string) string {
	if p == "last90d" {
		return "近 90 天"
	}
	return p
}

// RenderWeeklyHeadlineMarkdown renders the headline section; sc and bt may be nil.
func RenderWeeklyHeadlineMarkdown(sc *RouteScorecard, bt *RouteBacktest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## 本周要点：复发故障与首次故障分开看（%s）\n\n", WeeklyHeadlineVersion)
	fmt.Fprintf(&b, "首次故障指节点在账本中的第一次关键故障。只靠故障历史的路线（R1）在首次故障发生前没有任何依据，对首次故障只能做到随机水平（Top5%% 捕获 %.2f）。总捕获 ≈ 复发占比 × 复发捕获 + 首次占比 × 首次捕获。本节只做描述，不是 E2-B 判定；论文的 %.0f%% 作为长期参照，不作每周考核。\n\n", randomTop5, PaperTop5Capture*100)
	if sc != nil {
		n, first := 0, 0
		for _, e := range sc.Episodes {
			n++
			if e.FirstTime {
				first++
			}
		}
		fmt.Fprintf(&b, "**前瞻（自 %s 起）**：关键故障 %d 次，其中首次 %d、复发 %d。\n\n", sc.Start.In(shanghai).Format("2006-01-02"), n, first, n-first)
		b.WriteString("| 路线 | 首次故障：次数 · Top5% 捕获（95% 区间）· 平均百分位 | 复发故障：次数 · Top5% 捕获（95% 区间）· 平均百分位 |\n| --- | --- | --- |\n")
		for _, r := range sc.Routes {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", routeName(r.Strategy), splitText(r.FirstTime, r.FirstCapture, r.FirstLow, r.FirstHigh, r.FirstMeanPct), splitText(r.Recurrent, r.RecurCapture, r.RecurLow, r.RecurHigh, r.RecurMeanPct))
		}
		fmt.Fprintf(&b, "\n随机基线：Top5%% 捕获 %.2f、平均百分位 0.50。故障少时区间很宽，单周数字不作结论。\n\n", randomTop5)
	}
	if rows := headlineBacktestRows(bt); len(rows) > 0 {
		b.WriteString("**故障构成（回溯，R1 同一打分规则）**\n\n")
		b.WriteString("| 时段 | 故障 | 复发占比 | R1 复发捕获 | R1 首次捕获 | 按此构成的 R1 上限 |\n| --- | --- | --- | --- | --- | --- |\n")
		for _, r := range rows {
			share := float64(r.Episodes-r.FirstTime) / float64(r.Episodes)
			fmt.Fprintf(&b, "| %s | %d | %.2f | %.2f | %.2f | %.2f |\n", periodName(r.Period), r.Episodes, share, r.R1.RecurCapture, r.R1.FirstCapture, r.Ceiling)
		}
		fmt.Fprintf(&b, "\n首次故障停留在随机水平时，即使复发故障全部排进前 5%%，复发占比也要达到 %.2f 才能到 %.0f%%。复发占比随着机队变老而上升、随新节点上架而下降；提高首次故障捕获只能靠复线新信号或 cohort 先验。\n", RequiredRecurrentShare(), PaperTop5Capture*100)
	}
	return b.String()
}

// RenderWeeklyHeadlineFeishu renders the same headline as compact lark_md lines.
func RenderWeeklyHeadlineFeishu(sc *RouteScorecard, bt *RouteBacktest) string {
	var b strings.Builder
	if sc != nil {
		n, first := len(sc.Episodes), 0
		for _, e := range sc.Episodes {
			if e.FirstTime {
				first++
			}
		}
		fmt.Fprintf(&b, "**复发 / 首次分开看**（前瞻自 %s）：故障 %d 次，首次 %d、复发 %d\n", sc.Start.In(shanghai).Format("01-02"), n, first, n-first)
		for _, r := range sc.Routes {
			if l, ok := routeLabel[r.Strategy]; ok && r.Strategy != ShadowStrategy {
				fmt.Fprintf(&b, "- %s 首次：%s；复发：%s\n", l, splitText(r.FirstTime, r.FirstCapture, r.FirstLow, r.FirstHigh, r.FirstMeanPct), splitText(r.Recurrent, r.RecurCapture, r.RecurLow, r.RecurHigh, r.RecurMeanPct))
			}
		}
	}
	if rows := headlineBacktestRows(bt); len(rows) > 0 {
		r := rows[0]
		share := float64(r.Episodes-r.FirstTime) / float64(r.Episodes)
		fmt.Fprintf(&b, "- 回溯 %s：复发占比 %.2f，R1 上限 %.2f（到 %.0f%% 需复发占比 ≥ %.2f）\n", periodName(r.Period), share, r.Ceiling, PaperTop5Capture*100, RequiredRecurrentShare())
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	return b.String()
}

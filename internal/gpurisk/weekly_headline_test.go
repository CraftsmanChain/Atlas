package gpurisk

import (
	"math"
	"strings"
	"testing"
	"time"
)

// The required recurrent share solves s + (1-s)*0.05 = 0.64.
func TestRequiredRecurrentShare(t *testing.T) {
	s := RequiredRecurrentShare()
	if math.Abs(s+(1-s)*0.05-0.64) > 1e-12 || math.Abs(s-0.6210526315789474) > 1e-12 {
		t.Fatal(s)
	}
}

// The headline splits first-time and recurrent episodes per route, renders
// empty splits as "0 次" (never 0.00), and takes the composition from the
// latest month with episodes plus the last-90d row.
func TestWeeklyHeadlineSplit(t *testing.T) {
	sc := &RouteScorecard{Start: RouteScoreStart.UTC(), Episodes: []RouteEpisode{
		{NodeIP: "10.0.0.1", Start: time.Now(), FirstTime: true},
		{NodeIP: "10.0.0.2", Start: time.Now(), FirstTime: true},
	}, Routes: []RouteScore{
		{Strategy: CoreRecurrenceStrategy, Episodes: 2, FirstTime: 2, FirstCapture: 0.0, FirstLow: 0, FirstHigh: 0.66, FirstMeanPct: 0.47},
		{Strategy: CoreCohortStrategy, Episodes: 2, FirstTime: 2, FirstCapture: 0.5, FirstLow: 0.09, FirstHigh: 0.91, FirstMeanPct: 0.6},
	}}
	bt := &RouteBacktest{Rows: []BacktestRow{
		{Period: "2026-10", Episodes: 20, FirstTime: 13, R1: BacktestRoute{RecurCapture: 0.6, FirstCapture: 0.01}, Ceiling: 0.24},
		{Period: "2026-11", Episodes: 0},
		{Period: "all", Episodes: 40, FirstTime: 30},
		{Period: "last90d", Episodes: 30, FirstTime: 22, R1: BacktestRoute{RecurCapture: 0.5}, Ceiling: 0.17},
	}}
	md := RenderWeeklyHeadlineMarkdown(sc, bt)
	for _, want := range []string{
		"关键故障 2 次，其中首次 2、复发 0",
		"| R1 core-recurrence-v1 | 2 次 · 0.00（0.00–0.66）· 百分位 0.47 | 0 次 |",
		"| R4 core-recurrence-cohort-v1 | 2 次 · 0.50（0.09–0.91）· 百分位 0.60 | 0 次 |",
		"| 2026-10 | 20 | 0.35 | 0.60 | 0.01 | 0.24 |",
		"| 近 90 天 | 30 | 0.27 | 0.50 | 0.00 | 0.17 |",
		"复发占比也要达到 0.62",
		"不是 E2-B 判定",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q in\n%s", want, md)
		}
	}
	if strings.Contains(md, "合计") || strings.Contains(md, "| 2026-11 |") {
		t.Fatal(md)
	}
	fs := RenderWeeklyHeadlineFeishu(sc, bt)
	if !strings.Contains(fs, "- R1 首次：2 次") || strings.Contains(fs, "旧前兆规则") || !strings.Contains(fs, "回溯 2026-10：复发占比 0.35") {
		t.Fatal(fs)
	}
	if RenderWeeklyHeadlineFeishu(nil, nil) != "" {
		t.Fatal("empty inputs must render nothing")
	}
}

// Scorecard split cells render "-" when a split has no episodes.
func TestScorecardSplitCellEmpty(t *testing.T) {
	md := RenderRouteScorecardMarkdown(RouteScorecard{Start: RouteScoreStart.UTC(), Routes: []RouteScore{{Strategy: CoreRecurrenceStrategy, Batches: 3, Episodes: 1, FirstTime: 1, Capture: 0.2, FirstCapture: 0.2}}})
	if !strings.Contains(md, "| 0.200 | - |") {
		t.Fatal(md)
	}
}

package gpurisk

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func checkFeishuContent(t *testing.T, content string, want ...string) {
	t.Helper()
	if strings.Contains(content, "|") {
		t.Fatalf("content has '|' (table rows are not rendered by lark_md):\n%s", content)
	}
	if n := utf8.RuneCountInString(content); n > 2500 {
		t.Fatalf("content too long: %d runes", n)
	}
	for _, w := range want {
		if !strings.Contains(content, w) {
			t.Fatalf("content missing %q:\n%s", w, content)
		}
	}
}

func feishuDailyFixture() DailyCheck {
	now := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	p := 0.0123
	return DailyCheck{
		Version: DailyCheckVersion, GeneratedAt: now, LocalDay: "2026-11-13",
		LatestQuery: &DailyQuery{ID: 41, Cutoff: cutoff, MemberCount: 777},
		Operations:  []DailyOperation{{Stage: "query", Status: "completed"}, {Stage: "shadow", Status: "completed"}},
		Shadow:      DailyShadow{Exists: true, BatchID: 9, Timing: "prospective", ComputedAt: &at, ScoredCount: 770, NonZeroSignal: 33},
		R1:          DailyR1{OutcomeBatchID: 5, V4: DailyDatasetCounts{Rows: 700, HistoryUnknown: 12}, V2: DailyDatasetCounts{Rows: 700, HistoryUnknown: 345}},
		Coverage:    DailyCoverage{Ledger: &DailyLedger{VerifiedThrough: cutoff.Add(-48 * time.Hour), AgeDays: 2.3}},
		IncidentOutcomes: []DailyIncident{
			{QueryID: 41, BatchID: 3, Counts: map[string]int{"matured_positive": 2, "matured_negative": 100, "pending_horizon": 5}},
			{QueryID: 40, BatchID: 2, Counts: map[string]int{"matured_positive": 1, "matured_negative": 50}},
			{QueryID: 39, Counts: map[string]int{}},
		},
		UnreviewedFeedback: []DailyFeedback{{ID: 501}, {ID: 502}},
		E2:                 DailyE2{ProspectiveBatches: 1, Evaluated: 0, M2Hits: 0, M1Hits: 0, ReadPoint: "none", PValue: &p, Verdict: "accumulating"},
		Findings:           []string{"INFO 近 30 天有 2 条未审核反馈（501,502），待 Commander 核对"},
	}
}

func TestRenderDailyFeishuGreen(t *testing.T) {
	title, template, content := RenderDailyFeishu(feishuDailyFixture())
	if title != "GPU 风险日检 2026-11-13" || template != "green" {
		t.Fatal(title, template)
	}
	checkFeishuContent(t, content,
		"#41", "成员 777", "query：completed", "shadow：completed",
		"#9 · prospective · 打分 770 · 非零信号 33",
		"v4 12/700 行 · v2 345/700 行", "距今 2.3 天",
		"正例 3 · 负例 150", "**未审核反馈**：2（501,502）",
		"前瞻批次 1", "累积中", "p=0.0123",
		"完整报告：/ops/atlas/reports/gpu-risk/daily-latest.md · 只读检查，不含处置动作")
}

func TestRenderDailyFeishuOrangeWarnFirst(t *testing.T) {
	c := feishuDailyFixture()
	c.Findings = []string{"INFO 信息一", "WARN 警告一", "INFO 信息二 | 带竖线", "WARN 警告二"}
	_, template, content := RenderDailyFeishu(c)
	if template != "orange" {
		t.Fatal(template)
	}
	checkFeishuContent(t, content, "WARN 2 · INFO 2", "信息二 / 带竖线")
	w1, w2, i1 := strings.Index(content, "WARN 警告一"), strings.Index(content, "WARN 警告二"), strings.Index(content, "INFO 信息一")
	if w1 < 0 || w2 < 0 || i1 < 0 || w1 > i1 || w2 > i1 {
		t.Fatalf("WARN findings must come first:\n%s", content)
	}
}

func TestRenderDailyFeishuRedOnStageFailure(t *testing.T) {
	for _, status := range []string{"failed", "manual_required"} {
		c := feishuDailyFixture()
		c.Operations = append(c.Operations, DailyOperation{Stage: "coverage", Status: status, ErrorCode: "boom"})
		c.Findings = []string{"WARN 运行阶段 coverage 状态 " + status + "（boom）"}
		_, template, content := RenderDailyFeishu(c)
		if template != "red" {
			t.Fatal(status, template)
		}
		checkFeishuContent(t, content, "coverage："+status+"（boom）")
	}
}

func TestRenderDailyFeishuEmptyAndLengthBound(t *testing.T) {
	_, template, content := RenderDailyFeishu(DailyCheck{LocalDay: "2026-11-13", E2: DailyE2{Error: "x"}, R1: DailyR1{Error: "no completed query"}})
	if template != "green" {
		t.Fatal(template)
	}
	checkFeishuContent(t, content, "无已完成查询", "影子批次**：不存在", "ledger 声明**：无", "生成失败")

	c := feishuDailyFixture()
	for i := 0; i < 200; i++ {
		c.Findings = append(c.Findings, "WARN "+strings.Repeat("长", 300))
		c.Operations = append(c.Operations, DailyOperation{Stage: fmt.Sprintf("stage-%d-%s", i, strings.Repeat("x", 50)), Status: "completed"})
		c.UnreviewedFeedback = append(c.UnreviewedFeedback, DailyFeedback{ID: uint(1000 + i)})
	}
	_, template, content = RenderDailyFeishu(c)
	if template != "orange" {
		t.Fatal(template)
	}
	checkFeishuContent(t, content, "已截断", "daily-latest.md")
}

func TestRenderE2Feishu(t *testing.T) {
	now := time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)
	onset := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)
	p := 0.0042
	r := E2Report{Version: E2PrimeVersion, GeneratedAt: now, ProspectiveBatches: 7, Evaluated: 12, OutOfScope: 3, NoRanking: 2, Ambiguous: 1,
		M2Hits: 4, M2Expected: 3.75, M1Hits: 2, M0Expected: 0.61, ReadPoint: "first", PValue: &p, Verdict: "pass",
		Episodes: []E2Episode{
			{NodeIP: "10.114.4.21", Onset: onset, Status: "evaluated", M2: &E2Model{Rank: 3, K: 39}, M1: &E2Model{Rank: 120, K: 39}},
			{NodeIP: "10.114.4.22", Onset: onset, Status: "out_of_scope"},
		},
		UnreviewedFeedback: []uint{77, 78}}
	title, template, content := RenderE2Feishu(r)
	if title != "GPU 风险 E2′ 周报 2026-11-20" || template != "green" {
		t.Fatal(title, template)
	}
	checkFeishuContent(t, content, "**前瞻批次**：7", "已评价 12 · 范围外 3 · 无前瞻排名 2 · 跨边界 1",
		"**M2′ 命中**：4（期望值口径 3.75）", "**M1 命中**：2", "**M0 期望**：0.61", "first", "p=0.0042", "通过",
		"10.114.4.21 · 2026-11-15 11:00:00 · 已评价 · M2′ 3/39 · M1 120/39", "10.114.4.22 · 2026-11-15 11:00:00 · 范围外 · M2′ - · M1 -",
		"：2（77,78）", "/ops/atlas/reports/gpu-risk/weekly-latest.md")

	r.Verdict = "stop_condition_triggered"
	if _, template, content = RenderE2Feishu(r); template != "red" || !strings.Contains(content, "触发停止条件") {
		t.Fatal(template)
	}
	for _, v := range []string{"accumulating", "not_yet_significant", "significant_but_not_better_than_m1", "weak_evidence_human_decision"} {
		r.Verdict = v
		if _, template, _ = RenderE2Feishu(r); template != "blue" {
			t.Fatal(v, template)
		}
	}

	for i := 0; i < 60; i++ {
		r.Episodes = append(r.Episodes, E2Episode{NodeIP: fmt.Sprintf("10.114.4.%d", 100+i), Onset: onset, Status: "evaluated", M2: &E2Model{Rank: i, K: 39}})
	}
	_, _, content = RenderE2Feishu(r)
	checkFeishuContent(t, content, "…另 42 条见完整报告")
	if n := strings.Count(content, "\n- "); n != 21 {
		t.Fatalf("want 20 episode lines + 1 overflow line, got %d", n)
	}
}

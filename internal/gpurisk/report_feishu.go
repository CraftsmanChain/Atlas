package gpurisk

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// feishuMaxBody bounds the card body (runes, footer excluded) so a whole card
// stays around 2500 characters.
const feishuMaxBody = 2300

const (
	feishuDailyFooter  = "完整报告：/ops/atlas/reports/gpu-risk/daily-latest.md · 只读检查，不含处置动作"
	feishuWeeklyFooter = "完整报告：/ops/atlas/reports/gpu-risk/weekly-latest.md · 只读检查，不含处置动作"
)

var e2VerdictZH = map[string]string{
	"accumulating":                       "累积中（未到读数点）",
	"not_yet_significant":                "首读未显著，继续累积",
	"pass":                               "通过",
	"significant_but_not_better_than_m1": "显著但未优于 M1",
	"stop_condition_triggered":           "触发停止条件",
	"weak_evidence_human_decision":       "证据偏弱，需人工决策",
}

var e2StatusZH = map[string]string{
	"evaluated":              "已评价",
	"out_of_scope":           "范围外",
	"no_prospective_ranking": "无前瞻排名",
	"ambiguous":              "跨边界",
}

func verdictZH(v string) string {
	if s, ok := e2VerdictZH[v]; ok {
		return s + "（" + v + "）"
	}
	return v
}

// feishuText makes free text safe for one lark_md line: no '|' (no table
// rows), no line breaks, bounded length.
func feishuText(s string, max int) string {
	s = strings.NewReplacer("|", "/", "\r", " ", "\n", " ").Replace(s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}

// feishuFinish caps the body, removes any '|' and appends the footer.
func feishuFinish(body, footer string) string {
	body = strings.ReplaceAll(body, "|", "/")
	if utf8.RuneCountInString(body) > feishuMaxBody {
		body = string([]rune(body)[:feishuMaxBody]) + "\n…（已截断，见完整报告）\n"
	}
	return body + "\n" + footer
}

func joinIDs(ids []uint, max int) string {
	parts := []string{}
	for i, id := range ids {
		if i == max {
			parts = append(parts, fmt.Sprintf("…共 %d 条", len(ids)))
			break
		}
		parts = append(parts, fmt.Sprint(id))
	}
	return strings.Join(parts, ",")
}

// RenderDailyFeishu renders a compact lark_md card (no tables) for the daily
// check. It is sent on every run; the template only reflects severity:
// red = a stage failed/manual_required, orange = any WARN finding, else green.
func RenderDailyFeishu(c DailyCheck) (title, template, content string) {
	title = "GPU 风险日检 " + c.LocalDay
	template = "green"
	var warn, other []string
	for _, f := range c.Findings {
		if strings.HasPrefix(f, "WARN") {
			warn = append(warn, f)
		} else {
			other = append(other, f)
		}
	}
	if len(warn) > 0 {
		template = "orange"
	}
	for _, o := range c.Operations {
		if o.Status == "failed" || o.Status == "manual_required" {
			template = "red"
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**结论**（WARN %d · INFO %d）\n", len(warn), len(other))
	if len(c.Findings) == 0 {
		b.WriteString("- 无异常发现\n")
	}
	ordered := append(append([]string{}, warn...), other...)
	for i, f := range ordered {
		if i == 8 {
			fmt.Fprintf(&b, "- …另 %d 条见完整报告\n", len(ordered)-i)
			break
		}
		fmt.Fprintf(&b, "- %s\n", feishuText(f, 160))
	}

	b.WriteString("\n**最新查询**：")
	if q := c.LatestQuery; q == nil {
		b.WriteString("无已完成查询\n")
	} else {
		cut := q.Cutoff
		fmt.Fprintf(&b, "#%d · cutoff %s · 成员 %d\n", q.ID, fmtTime(&cut), q.MemberCount)
	}

	b.WriteString("**今日运行阶段**\n")
	if len(c.Operations) == 0 {
		b.WriteString("- 无记录\n")
	}
	for _, o := range c.Operations {
		line := o.Stage + "：" + o.Status
		if o.ErrorCode != "" {
			line += "（" + o.ErrorCode + "）"
		}
		fmt.Fprintf(&b, "- %s\n", feishuText(line, 120))
	}

	b.WriteString("**影子批次**：")
	if s := c.Shadow; !s.Exists {
		b.WriteString("不存在\n")
	} else {
		fmt.Fprintf(&b, "#%d · %s · 打分 %d · 非零信号 %d\n", s.BatchID, s.Timing, s.ScoredCount, s.NonZeroSignal)
	}

	b.WriteString("**R1 history_unknown**：")
	if c.R1.Error != "" {
		fmt.Fprintf(&b, "未计算（%s）\n", feishuText(c.R1.Error, 100))
	} else {
		r1 := func(d DailyDatasetCounts) string {
			if d.Error != "" {
				return "错误（" + feishuText(d.Error, 80) + "）"
			}
			return fmt.Sprintf("%d/%d 行", d.HistoryUnknown, d.Rows)
		}
		fmt.Fprintf(&b, "v4 %s · v2 %s\n", r1(c.R1.V4), r1(c.R1.V2))
	}

	b.WriteString("**ledger 声明**：")
	if l := c.Coverage.Ledger; l == nil {
		b.WriteString("无\n")
	} else {
		vt := l.VerifiedThrough
		fmt.Fprintf(&b, "through %s · 距今 %.1f 天\n", fmtTime(&vt), l.AgeDays)
	}

	pos, neg, withBatch := 0, 0, 0
	for _, x := range c.IncidentOutcomes {
		if x.BatchID != 0 {
			withBatch++
		}
		pos += x.Counts["matured_positive"]
		neg += x.Counts["matured_negative"]
	}
	fmt.Fprintf(&b, "**T-A 已成熟**（近 31 天 %d 个查询，%d 个有结果批次）：正例 %d · 负例 %d\n", len(c.IncidentOutcomes), withBatch, pos, neg)

	ids := make([]uint, 0, len(c.UnreviewedFeedback))
	for _, f := range c.UnreviewedFeedback {
		ids = append(ids, f.ID)
	}
	if len(ids) == 0 {
		b.WriteString("**未审核反馈**：0\n")
	} else {
		fmt.Fprintf(&b, "**未审核反馈**：%d（%s）\n", len(ids), joinIDs(ids, 20))
	}

	b.WriteString("**E2′**：")
	if c.E2.Error != "" {
		fmt.Fprintf(&b, "生成失败（%s）\n", feishuText(c.E2.Error, 100))
	} else {
		p := ""
		if c.E2.PValue != nil {
			p = fmt.Sprintf(" · p=%.4g", *c.E2.PValue)
		}
		fmt.Fprintf(&b, "前瞻批次 %d · 已评价 %d · M2′ 命中 %d · M1 命中 %d · 读数点 %s%s · %s\n", c.E2.ProspectiveBatches, c.E2.Evaluated, c.E2.M2Hits, c.E2.M1Hits, c.E2.ReadPoint, p, verdictZH(c.E2.Verdict))
	}
	return title, template, feishuFinish(b.String(), feishuDailyFooter)
}

// RenderE2Feishu renders a compact lark_md card (no tables) for the weekly
// E2′ report: green = pass, red = stop_condition_triggered, else blue.
func RenderE2Feishu(r E2Report) (title, template, content string) {
	title = "GPU 风险 E2′ 周报 " + r.GeneratedAt.In(dailyZone).Format("2006-01-02")
	switch r.Verdict {
	case "pass":
		template = "green"
	case "stop_condition_triggered":
		template = "red"
	default:
		template = "blue"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**判定**：%s\n", verdictZH(r.Verdict))
	fmt.Fprintf(&b, "**前瞻批次**：%d\n", r.ProspectiveBatches)
	fmt.Fprintf(&b, "**episode**：已评价 %d · 范围外 %d · 无前瞻排名 %d · 跨边界 %d\n", r.Evaluated, r.OutOfScope, r.NoRanking, r.Ambiguous)
	fmt.Fprintf(&b, "**M2′ 命中**：%d（期望值口径 %.2f）\n", r.M2Hits, r.M2Expected)
	fmt.Fprintf(&b, "**M1 命中**：%d\n", r.M1Hits)
	fmt.Fprintf(&b, "**M0 期望**：%.2f\n", r.M0Expected)
	fmt.Fprintf(&b, "**读数点**：%s", r.ReadPoint)
	if r.PValue != nil {
		fmt.Fprintf(&b, " · p=%.4g", *r.PValue)
	}
	b.WriteString("\n")
	if len(r.Episodes) > 0 {
		b.WriteString("\n**Episode**（节点 · onset · 状态 · M2′ 名次/K · M1 名次/K）\n")
		for i, e := range r.Episodes {
			if i == 20 {
				fmt.Fprintf(&b, "- …另 %d 条见完整报告\n", len(r.Episodes)-i)
				break
			}
			m2, m1 := "-", "-"
			if e.M2 != nil {
				m2 = fmt.Sprintf("%d/%d", e.M2.Rank, e.M2.K)
			}
			if e.M1 != nil {
				m1 = fmt.Sprintf("%d/%d", e.M1.Rank, e.M1.K)
			}
			status := e.Status
			if s, ok := e2StatusZH[status]; ok {
				status = s
			}
			on := e.Onset
			fmt.Fprintf(&b, "- %s · %s · %s · M2′ %s · M1 %s\n", feishuText(e.NodeIP, 40), fmtTime(&on), status, m2, m1)
		}
	}
	fmt.Fprintf(&b, "\n**待审核反馈**（预登记期内）：%d", len(r.UnreviewedFeedback))
	if len(r.UnreviewedFeedback) > 0 {
		fmt.Fprintf(&b, "（%s）", joinIDs(r.UnreviewedFeedback, 20))
	}
	b.WriteString("\n")
	return title, template, feishuFinish(b.String(), feishuWeeklyFooter)
}

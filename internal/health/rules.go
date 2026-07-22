package health

import (
	"fmt"
	"math"
	"strings"

	"atlas/pkg/api"
)

func evaluateRules(metrics api.FloatMap, model, confidence string) scoreResult {
	result := scoreResult{level: "unknown", stability: -1, memory: -1, thermal: -1, power: -1, link: -1, performance: -1}
	if confidence == "D" {
		result.evidence = api.StringList{"insufficient_current_metrics"}
		return result
	}
	result.stability, result.memory, result.thermal, result.power, result.link, result.performance = 100, 100, 100, 100, 100, 100

	add := func(hit ruleHit) {
		result.hits = append(result.hits, hit)
		result.evidence = append(result.evidence, hit.evidence)
		switch hit.domain {
		case "stability":
			result.stability = clamp(result.stability - hit.deduction)
		case "memory":
			result.memory = clamp(result.memory - hit.deduction)
		case "thermal":
			result.thermal = clamp(result.thermal - hit.deduction)
		case "power":
			result.power = clamp(result.power - hit.deduction)
		case "interconnect":
			result.link = clamp(result.link - hit.deduction)
		case "performance":
			result.performance = clamp(result.performance - hit.deduction)
		}
	}

	if value(metrics, "row_remap_failure") > 0 {
		v := value(metrics, "row_remap_failure")
		add(ruleHit{"row_remap_failure", "memory", "critical", 45, v, "> 0", fmt.Sprintf("row remap failure=%.0f", v)})
	}
	if v := value(metrics, "uncorrectable_remapped_rows"); v > 0 {
		add(ruleHit{"uncorrectable_remapped_rows", "memory", "critical", 30, v, "> 0", fmt.Sprintf("uncorrectable remapped rows=%.0f", v)})
	}
	if v := value(metrics, "correctable_remapped_rows"); v > 0 {
		add(ruleHit{"correctable_remapped_rows", "memory", "attention", 8, v, "> 0", fmt.Sprintf("correctable remapped rows=%.0f", v)})
	}
	if v := value(metrics, "xid_changes_24h"); v > 0 {
		xid := value(metrics, "xid_current")
		deduction, severity := 20, "warning"
		if criticalXID(int(math.Round(xid))) {
			deduction, severity = 35, "critical"
		}
		add(ruleHit{"recent_xid_change", "stability", severity, deduction, xid, "changes_24h > 0", fmt.Sprintf("XID %.0f changed in last 24h", xid)})
	}
	if v := value(metrics, "gpu_temp_max_15m"); v >= 85 {
		add(ruleHit{"gpu_temp_critical", "thermal", "critical", 25, v, ">= 85C", fmt.Sprintf("GPU temperature max 15m=%.1fC", v)})
	} else if v >= 80 {
		add(ruleHit{"gpu_temp_high", "thermal", "warning", 10, v, ">= 80C", fmt.Sprintf("GPU temperature max 15m=%.1fC", v)})
	}
	if !strings.Contains(strings.ToLower(model), "4090") {
		if v := value(metrics, "memory_temp_max_15m"); v >= 90 {
			add(ruleHit{"memory_temp_critical", "thermal", "critical", 20, v, ">= 90C", fmt.Sprintf("memory temperature max 15m=%.1fC", v)})
		} else if v >= 85 {
			add(ruleHit{"memory_temp_high", "thermal", "warning", 8, v, ">= 85C", fmt.Sprintf("memory temperature max 15m=%.1fC", v)})
		}
	}
	if v := value(metrics, "pcie_replay_increase_1h"); v >= 100 {
		add(ruleHit{"pcie_replay_spike", "interconnect", "warning", 20, v, ">= 100/hour", fmt.Sprintf("PCIe replay increase 1h=%.1f", v)})
	} else if v >= 10 {
		add(ruleHit{"pcie_replay_growth", "interconnect", "attention", 8, v, ">= 10/hour", fmt.Sprintf("PCIe replay increase 1h=%.1f", v)})
	}
	if util, clock := value(metrics, "gpu_util_avg_15m"), value(metrics, "sm_clock_avg_15m"); util >= 80 && clock > 0 && clock < modelClockFloor(model) {
		add(ruleHit{"high_load_low_sm_clock", "performance", "warning", 15, clock, fmt.Sprintf("< %.0fMHz at util >= 80%%", modelClockFloor(model)), fmt.Sprintf("GPU util avg 15m=%.1f%%, SM clock avg=%.1fMHz", util, clock)})
	}

	totalDeduction := 0
	for _, hit := range result.hits {
		totalDeduction += hit.deduction
	}
	score := clamp(100 - totalDeduction)
	result.score = &score
	switch {
	case score >= 90:
		result.level = "healthy"
	case score >= 75:
		result.level = "attention"
	case score >= 60:
		result.level = "warning"
	default:
		result.level = "critical"
	}
	for _, hit := range result.hits {
		if riskRank(hit.severity) > riskRank(result.level) {
			result.level = hit.severity
		}
	}
	if len(result.evidence) == 0 {
		result.evidence = api.StringList{"no_v1_rule_hits"}
	}
	return result
}

func riskRank(level string) int {
	switch level {
	case "attention":
		return 1
	case "warning":
		return 2
	case "critical":
		return 3
	default:
		return 0
	}
}

func value(metrics api.FloatMap, key string) float64 { return metrics[key] }
func clamp(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func criticalXID(code int) bool {
	switch code {
	case 31, 43, 48, 63, 64, 74, 79, 94, 95:
		return true
	default:
		return false
	}
}

func modelClockFloor(model string) float64 {
	if strings.Contains(strings.ToLower(model), "4090") {
		return 1200
	}
	return 900
}

package health

import (
	"fmt"
	"math"
	"strconv"
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
	uncorrectableRows := value(metrics, "uncorrectable_remapped_rows")
	uncorrectableDelta1h := value(metrics, "uncorrectable_remapped_rows_delta_1h")
	uncorrectableDelta24h := value(metrics, "uncorrectable_remapped_rows_delta_24h")
	if uncorrectableDelta1h > 0 || uncorrectableDelta24h > 0 {
		add(ruleHit{
			"uncorrectable_remapped_rows_growth", "memory", "critical", 30,
			math.Max(uncorrectableDelta1h, uncorrectableDelta24h),
			"delta_1h > 0 or delta_24h > 0",
			fmt.Sprintf("new uncorrectable remapped rows: total=%.0f delta_1h=%.0f delta_24h=%.0f", uncorrectableRows, uncorrectableDelta1h, uncorrectableDelta24h),
		})
	} else if uncorrectableRows > 0 {
		result.evidence = append(result.evidence, fmt.Sprintf(
			"historical uncorrectable remapped rows observation only: total=%.0f delta_1h=%.0f delta_24h=%.0f",
			uncorrectableRows, uncorrectableDelta1h, uncorrectableDelta24h,
		))
	}
	correctableRows := value(metrics, "correctable_remapped_rows")
	correctableDelta1h := value(metrics, "correctable_remapped_rows_delta_1h")
	correctableDelta24h := value(metrics, "correctable_remapped_rows_delta_24h")
	switch {
	case correctableDelta1h >= 4 || correctableDelta24h >= 8:
		add(ruleHit{"correctable_remapped_rows_rapid_growth", "memory", "warning", 12, correctableDelta24h, "delta_1h >= 4 or delta_24h >= 8", fmt.Sprintf("correctable remapped rows growing rapidly: total=%.0f delta_1h=%.0f delta_24h=%.0f", correctableRows, correctableDelta1h, correctableDelta24h)})
	case correctableDelta1h >= 2 || correctableDelta24h >= 4:
		add(ruleHit{"correctable_remapped_rows_growth", "memory", "attention", 8, correctableDelta24h, "delta_1h >= 2 or delta_24h >= 4", fmt.Sprintf("correctable remapped rows increased: total=%.0f delta_1h=%.0f delta_24h=%.0f", correctableRows, correctableDelta1h, correctableDelta24h)})
	case correctableRows > 0 || correctableDelta1h > 0 || correctableDelta24h > 0:
		result.evidence = append(result.evidence, fmt.Sprintf("correctable remapped rows observation only: total=%.0f delta_1h=%.0f delta_24h=%.0f", correctableRows, correctableDelta1h, correctableDelta24h))
	}
	if v := value(metrics, "gpu_reset_required"); v > 0 {
		add(ruleHit{"gpu_reset_required", "stability", "critical", 40, v, "> 0", "gpu_exporter reports that a GPU reset is required"})
	}
	volatileUncorrectedECC := value(metrics, "uncorrected_ecc_volatile")
	aggregateUncorrectedDelta24h := value(metrics, "uncorrected_ecc_delta_24h")
	if volatileUncorrectedECC > 0 || aggregateUncorrectedDelta24h > 0 {
		add(ruleHit{
			"recent_uncorrected_ecc", "memory", "critical", 30,
			math.Max(volatileUncorrectedECC, aggregateUncorrectedDelta24h),
			"volatile > 0 or aggregate_delta_24h > 0",
			fmt.Sprintf("new uncorrected ECC: volatile=%.0f aggregate_delta_24h=%.0f", volatileUncorrectedECC, aggregateUncorrectedDelta24h),
		})
	}
	if hit, ok := xidHit(metrics); ok {
		add(hit)
	}
	if v := value(metrics, "gpu_temp_min_5m"); v >= 85 {
		add(ruleHit{"gpu_temp_sustained_5m_critical", "thermal", "critical", 25, v, ">= 85C continuously for 5m", fmt.Sprintf("GPU temperature remained at or above 85C for 5m; floor=%.1fC", v)})
	} else if v := value(metrics, "gpu_temp_min_15m"); v >= 80 {
		add(ruleHit{"gpu_temp_sustained_15m", "thermal", "warning", 15, v, ">= 80C continuously for 15m", fmt.Sprintf("GPU temperature remained at or above 80C for 15m; floor=%.1fC", v)})
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
	if current, maximum, util := value(metrics, "pcie_link_width_current"), value(metrics, "pcie_link_width_max"), value(metrics, "gpu_util_avg_15m"); current > 0 && maximum > 0 && current < maximum && util >= 80 {
		add(ruleHit{"pcie_link_width_degraded", "interconnect", "warning", 15, current, fmt.Sprintf("< %.0f lanes at util >= 80%%", maximum), fmt.Sprintf("PCIe link width %.0f/%.0f at GPU util %.1f%%", current, maximum, util)})
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

// XID grading (gpu-health-v1.7.0). Counts are onsets per (GPU, XID code): the
// code reappearing after >=15 minutes of absence. XID 0 ("No Error") and label
// churn are ignored; the old rule fired on any changes() of the raw series,
// including XID 0 and exporter relabels.
//
//	core XIDs (same set as the GPU risk ledger): any onset in 24h -> critical
//	XID 94 (contained ECC): onset in 24h -> attention; >=3 onsets in 7d -> warning
//	other XIDs (mostly workload-triggered: 13/31/43/45/109...): onset in 24h and
//	  >=5 onsets in 7d -> attention; otherwise evidence only
const (
	xidOnsets24hPrefix  = "xid_onsets_24h_"
	xidOnsets7dPrefix   = "xid_onsets_7d_"
	xid94WarnOnsets7d   = 3
	xidOtherAttention7d = 5
)

var coreXIDs = map[int]bool{48: true, 62: true, 63: true, 64: true, 74: true, 79: true, 92: true, 95: true, 119: true, 120: true, 154: true}

var xidNames = map[int]string{
	13: "Graphics engine exception", 31: "GPU memory page fault", 32: "Invalid or corrupted push buffer", 43: "GPU stopped processing",
	45: "Preemptive cleanup", 48: "Double-bit ECC error", 62: "Internal micro-controller halt", 63: "ECC page retirement / row remap event",
	64: "ECC page retirement / row remap failure", 68: "Video processor exception", 69: "Graphics engine class error", 74: "NVLink error",
	79: "GPU fallen off the bus", 92: "High single-bit ECC rate", 94: "Contained ECC error", 95: "Uncontained ECC error",
	109: "Context switch timeout", 119: "GSP RPC timeout", 120: "GSP error", 137: "NVLink/PRIV error", 154: "GPU recovery action changed",
}

func xidLabel(code int) string {
	if name, ok := xidNames[code]; ok {
		return fmt.Sprintf("XID %d (%s)", code, name)
	}
	return fmt.Sprintf("XID %d", code)
}

func xidHit(metrics api.FloatMap) (ruleHit, bool) {
	best, found := ruleHit{}, false
	for key, n24 := range metrics {
		if !strings.HasPrefix(key, xidOnsets24hPrefix) || n24 <= 0 {
			continue
		}
		code, err := strconv.Atoi(strings.TrimPrefix(key, xidOnsets24hPrefix))
		if err != nil || code == 0 {
			continue
		}
		n7 := math.Max(metrics[xidOnsets7dPrefix+strconv.Itoa(code)], n24)
		evidence := fmt.Sprintf("%s: %.0f new in 24h, %.0f in 7d", xidLabel(code), n24, n7)
		var hit ruleHit
		switch {
		case coreXIDs[code]:
			hit = ruleHit{"recent_xid_change", "stability", "critical", 35, float64(code), "core XID, any onset in 24h", evidence}
		case code == 94 && n7 >= xid94WarnOnsets7d:
			hit = ruleHit{"recent_xid_change", "stability", "warning", 20, float64(code), fmt.Sprintf("XID 94 >= %d onsets in 7d", xid94WarnOnsets7d), evidence}
		case code == 94:
			hit = ruleHit{"recent_xid_change", "stability", "attention", 8, float64(code), "XID 94 onset in 24h", evidence}
		case n7 >= xidOtherAttention7d:
			hit = ruleHit{"recent_xid_change", "stability", "attention", 8, float64(code), fmt.Sprintf("non-core XID >= %d onsets in 7d", xidOtherAttention7d), evidence}
		default:
			continue
		}
		if !found || riskRank(hit.severity) > riskRank(best.severity) || (riskRank(hit.severity) == riskRank(best.severity) && hit.value < best.value) {
			best, found = hit, true
		}
	}
	return best, found
}

func modelClockFloor(model string) float64 {
	if strings.Contains(strings.ToLower(model), "4090") {
		return 1200
	}
	return 900
}

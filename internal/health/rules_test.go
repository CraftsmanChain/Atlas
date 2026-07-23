package health

import (
	"testing"

	"atlas/pkg/api"
)

func TestEvaluateRulesUnknownAndCritical(t *testing.T) {
	unknown := evaluateRules(api.FloatMap{}, "NVIDIA H100 80GB HBM3", "D")
	if unknown.score != nil || unknown.level != "unknown" || unknown.stability != -1 {
		t.Fatalf("unexpected unknown result: %+v", unknown)
	}

	critical := evaluateRules(api.FloatMap{
		"xid_changes_24h":             1,
		"xid_current":                 43,
		"uncorrectable_remapped_rows": 2,
		"gpu_temp_max_15m":            70,
		"memory_temp_max_15m":         75,
	}, "NVIDIA H100 80GB HBM3", "A")
	if critical.score == nil || *critical.score != 35 || critical.level != "critical" || len(critical.hits) != 2 {
		t.Fatalf("unexpected critical result: %+v", critical)
	}
}

func TestEvaluateRulesUsesCounterDeltaAndLoadGuard(t *testing.T) {
	result := evaluateRules(api.FloatMap{
		"pcie_replay_counter":     22000,
		"pcie_replay_increase_1h": 0,
		"gpu_util_avg_15m":        10,
		"sm_clock_avg_15m":        300,
	}, "NVIDIA H200", "A")
	if result.score == nil || *result.score != 100 || len(result.hits) != 0 {
		t.Fatalf("cumulative counter or idle clock caused a false deduction: %+v", result)
	}
}

func TestEvaluateRulesUsesRuleSeverityAsLevelFloor(t *testing.T) {
	critical := evaluateRules(api.FloatMap{
		"uncorrectable_remapped_rows": 1,
	}, "NVIDIA H100 80GB HBM3", "A")
	if critical.score == nil || *critical.score != 70 || critical.level != "critical" {
		t.Fatalf("critical rule did not set the risk-level floor: %+v", critical)
	}

	attention := evaluateRules(api.FloatMap{
		"correctable_remapped_rows": 1,
	}, "NVIDIA H100 80GB HBM3", "A")
	if attention.score == nil || *attention.score != 92 || attention.level != "attention" {
		t.Fatalf("attention rule did not set the risk-level floor: %+v", attention)
	}
}

func TestEvaluateRulesUsesGPUExporterSupplementsWithoutDuplicateEquivalentDeduction(t *testing.T) {
	result := evaluateRules(api.FloatMap{
		"gpu_reset_required":        1,
		"uncorrected_ecc_delta_24h": 2,
		"pcie_link_width_current":   8,
		"pcie_link_width_max":       16,
		"gpu_util_avg_15m":          90,
	}, "NVIDIA H100 80GB HBM3", "A")
	if result.score == nil || *result.score != 15 || result.level != "critical" || len(result.hits) != 3 {
		t.Fatalf("unexpected supplemental rules: %+v", result)
	}
}

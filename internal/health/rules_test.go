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
		"xid_changes_24h":                      1,
		"xid_current":                          43,
		"uncorrectable_remapped_rows":          2,
		"uncorrectable_remapped_rows_delta_1h": 1,
		"gpu_temp_max_15m":                     70,
		"memory_temp_max_15m":                  75,
	}, "NVIDIA H100 80GB HBM3", "A")
	if critical.score == nil || *critical.score != 35 || critical.level != "critical" || len(critical.hits) != 2 {
		t.Fatalf("unexpected critical result: %+v", critical)
	}
}

func TestEvaluateRulesRequiresSustainedHighTemperature(t *testing.T) {
	warning := evaluateRules(api.FloatMap{"gpu_temp_min_15m": 80}, "NVIDIA GeForce RTX 4090", "A")
	if warning.level != "warning" || len(warning.hits) != 1 || warning.hits[0].code != "gpu_temp_sustained_15m" {
		t.Fatalf("expected 80C sustained for 15m warning: %+v", warning)
	}
	critical := evaluateRules(api.FloatMap{"gpu_temp_min_15m": 80, "gpu_temp_min_5m": 85}, "NVIDIA GeForce RTX 4090", "A")
	if critical.level != "critical" || len(critical.hits) != 1 || critical.hits[0].code != "gpu_temp_sustained_5m_critical" {
		t.Fatalf("expected 85C sustained for 5m critical: %+v", critical)
	}
	notSustained := evaluateRules(api.FloatMap{"gpu_temp": 90, "gpu_temp_max_15m": 90, "gpu_temp_min_15m": 70}, "NVIDIA GeForce RTX 4090", "A")
	if len(notSustained.hits) != 0 {
		t.Fatalf("temperature spikes must not become sustained alerts: %+v", notSustained)
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
	historicalOnly := evaluateRules(api.FloatMap{
		"uncorrectable_remapped_rows": 1,
	}, "NVIDIA H100 80GB HBM3", "A")
	if historicalOnly.score == nil || *historicalOnly.score != 100 || historicalOnly.level != "healthy" || len(historicalOnly.hits) != 0 {
		t.Fatalf("stable lifetime aggregate must remain observation-only: %+v", historicalOnly)
	}

	critical := evaluateRules(api.FloatMap{
		"uncorrectable_remapped_rows":           2,
		"uncorrectable_remapped_rows_delta_1h":  1,
		"uncorrectable_remapped_rows_delta_24h": 1,
	}, "NVIDIA H100 80GB HBM3", "A")
	if critical.score == nil || *critical.score != 70 || critical.level != "critical" || len(critical.hits) != 1 ||
		critical.hits[0].code != "uncorrectable_remapped_rows_growth" {
		t.Fatalf("new uncorrectable row did not set the risk-level floor: %+v", critical)
	}

	stableCorrectable := evaluateRules(api.FloatMap{
		"correctable_remapped_rows":           1,
		"correctable_remapped_rows_delta_1h":  0,
		"correctable_remapped_rows_delta_24h": 0,
	}, "NVIDIA H100 80GB HBM3", "A")
	if stableCorrectable.score == nil || *stableCorrectable.score != 100 || stableCorrectable.level != "healthy" || len(stableCorrectable.hits) != 0 {
		t.Fatalf("stable correctable rows must remain observation-only: %+v", stableCorrectable)
	}
}

func TestEvaluateRulesScoresOnlyCorrectableRowGrowth(t *testing.T) {
	tests := []struct {
		name         string
		delta1h      float64
		delta24h     float64
		wantScore    int
		wantLevel    string
		wantHitCount int
	}{
		{name: "single recent row is observation only", delta1h: 1, delta24h: 1, wantScore: 100, wantLevel: "healthy", wantHitCount: 0},
		{name: "sustained growth needs attention", delta1h: 0, delta24h: 4, wantScore: 92, wantLevel: "attention", wantHitCount: 1},
		{name: "rapid hourly growth is warning", delta1h: 4, delta24h: 4, wantScore: 88, wantLevel: "warning", wantHitCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evaluateRules(api.FloatMap{
				"correctable_remapped_rows":           10,
				"correctable_remapped_rows_delta_1h":  tt.delta1h,
				"correctable_remapped_rows_delta_24h": tt.delta24h,
			}, "NVIDIA H100 80GB HBM3", "A")
			if result.score == nil || *result.score != tt.wantScore || result.level != tt.wantLevel || len(result.hits) != tt.wantHitCount {
				t.Fatalf("unexpected result: %+v", result)
			}
		})
	}
}

func TestEvaluateRulesUsesGPUExporterSupplementsWithoutDuplicateEquivalentDeduction(t *testing.T) {
	result := evaluateRules(api.FloatMap{
		"gpu_reset_required":        1,
		"uncorrected_ecc_volatile":  2,
		"uncorrected_ecc_delta_24h": 2,
		"pcie_link_width_current":   8,
		"pcie_link_width_max":       16,
		"gpu_util_avg_15m":          90,
	}, "NVIDIA H100 80GB HBM3", "A")
	if result.score == nil || *result.score != 15 || result.level != "critical" || len(result.hits) != 3 {
		t.Fatalf("equivalent volatile and aggregate ECC signals must produce one deduction: %+v", result)
	}
}

package featurestats

import (
	"testing"
	"time"

	promclient "atlas/internal/prometheus"
)

func TestTrailing24hContractParsesAndComputesColumns(t *testing.T) {
	base, statistic, ok := ParseTrailing24hColumn("gpu_temp_slope_per_hour_24h")
	if !ok || base != "gpu_temp" || statistic != "slope_per_hour_24h" {
		t.Fatalf("unexpected parsed column: %q %q %t", base, statistic, ok)
	}
	points := []promclient.RangePoint{
		{Timestamp: time.Unix(0, 0), Value: 10},
		{Timestamp: time.Unix(3600, 0), Value: 12},
		{Timestamp: time.Unix(7200, 0), Value: 14},
	}
	values := map[string]float64{}
	AddTrailing24hStatistics(values, "gpu_temp", points)
	if values["gpu_temp_last_24h"] != 14 || values["gpu_temp_mean_24h"] != 12 || values["gpu_temp_delta_24h"] != 4 || values["gpu_temp_slope_per_hour_24h"] != 2 || values["gpu_temp_sample_count_24h"] != 3 {
		t.Fatalf("unexpected statistics: %+v", values)
	}
}

func TestTrailingRangeContractComputesOnlyPointInTimeSafeWindows(t *testing.T) {
	cutoff := time.Unix(8*3600, 0)
	points := []promclient.RangePoint{
		{Timestamp: cutoff.Add(-7 * time.Hour), Value: 1},
		{Timestamp: cutoff.Add(-time.Hour), Value: 10},
		{Timestamp: cutoff.Add(-15 * time.Minute), Value: 20},
		{Timestamp: cutoff, Value: 30},
		{Timestamp: cutoff.Add(time.Minute), Value: 999},
	}
	values := map[string]float64{}
	AddTrailingRangeStatistics(values, "gpu_temp", points, cutoff)
	if values["gpu_temp_mean_15m"] != 25 || values["gpu_temp_delta_1h"] != 20 || values["gpu_temp_max_6h"] != 30 || values["gpu_temp_max_24h"] != 30 {
		t.Fatalf("unexpected multi-scale statistics: %+v", values)
	}
	base, statistic, duration, ok := ParseTrailingRangeColumn("gpu_temp_slope_per_hour_6h")
	if !ok || base != "gpu_temp" || statistic != "slope_per_hour_6h" || duration != 6*time.Hour {
		t.Fatalf("unexpected multi-scale parse: %q %q %s %t", base, statistic, duration, ok)
	}
	if len(TrailingRangeStatistics()) != 47 {
		t.Fatalf("unexpected multi-scale suffix count: %d", len(TrailingRangeStatistics()))
	}
}

func TestTrailingLongRangeStatisticsAreCoarseAndPointInTimeSafe(t *testing.T) {
	cutoff := time.Unix(40*24*3600, 0)
	points := []promclient.RangePoint{
		{Timestamp: cutoff.Add(-31 * 24 * time.Hour), Value: 999},
		{Timestamp: cutoff.Add(-20 * 24 * time.Hour), Value: 10},
		{Timestamp: cutoff.Add(-6 * 24 * time.Hour), Value: 20},
		{Timestamp: cutoff.Add(-2 * 24 * time.Hour), Value: 30},
		{Timestamp: cutoff, Value: 40},
		{Timestamp: cutoff.Add(time.Hour), Value: 999},
	}
	values := map[string]float64{}
	AddTrailingLongRangeStatistics(values, "pcie_replay_counter", points, cutoff)
	if values["pcie_replay_counter_delta_3d"] != 10 || values["pcie_replay_counter_max_7d"] != 40 || values["pcie_replay_counter_min_30d"] != 10 || values["pcie_replay_counter_last_30d"] != 40 {
		t.Fatalf("unexpected long-range statistics: %+v", values)
	}
	base, statistic, duration, ok := ParseTrailingRangeColumn("pcie_replay_counter_slope_per_hour_30d")
	if !ok || base != "pcie_replay_counter" || statistic != "slope_per_hour_30d" || duration != 30*24*time.Hour {
		t.Fatalf("unexpected long-range parse: %q %q %s %t", base, statistic, duration, ok)
	}
}

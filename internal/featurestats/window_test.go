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
	if len(TrailingRangeStatistics()) != 23 {
		t.Fatalf("unexpected multi-scale suffix count: %d", len(TrailingRangeStatistics()))
	}
}

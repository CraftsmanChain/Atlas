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

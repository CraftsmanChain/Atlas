package history

import (
	"strings"
	"testing"
	"time"

	promclient "atlas/internal/prometheus"
	"atlas/pkg/api"
)

func TestCanonicalSeriesByGPUKeepsEntitiesIsolated(t *testing.T) {
	series := []promclient.RangeSeries{
		{Metric: map[string]string{"__name__": "DCGM_FI_DEV_GPU_TEMP", "UUID": "GPU-A"}, Values: []promclient.RangePoint{{Timestamp: time.Unix(1, 0), Value: 50}}},
		{Metric: map[string]string{"__name__": "DCGM_FI_DEV_GPU_TEMP", "UUID": "GPU-B"}, Values: []promclient.RangePoint{{Timestamp: time.Unix(1, 0), Value: 60}}},
	}
	values := canonicalSeriesByGPU(series)
	if len(values) != 2 || values[normalizeHistoricalGPUUUID("GPU-A")]["gpu_temp"][0].Value != 50 || values[normalizeHistoricalGPUUUID("GPU-B")]["gpu_temp"][0].Value != 60 {
		t.Fatalf("GPU range series were mixed: %+v", values)
	}
}

func TestHistoricalMetricQuerySupportsUUIDChunks(t *testing.T) {
	query := historicalMetricQueryUUIDs([]string{"GPU-B", "GPU-A"})
	if !strings.Contains(query, "GPU-A|GPU-B") || !strings.Contains(query, "DCGM_FI_DEV_GPU_TEMP") {
		t.Fatalf("unexpected chunk query: %s", query)
	}
}

func TestParityRequiresLongRangeOnlyForLongWindowColumns(t *testing.T) {
	short := api.PredictionFeatureParityAudit{ContractMatchedColumns: api.StringList{"gpu_temp_mean_24h", "power_usage_slope_per_hour_3h"}}
	if parityRequiresLongRange(short) {
		t.Fatal("24h-and-shorter feature contract must not trigger the coarse 30d query")
	}
	long := api.PredictionFeatureParityAudit{ContractMatchedColumns: api.StringList{"gpu_temp_mean_24h", "gpu_temp_mean_30d"}}
	if !parityRequiresLongRange(long) {
		t.Fatal("30d feature contract must trigger the coarse 30d query")
	}
	sources := longRangeSourceMetrics(api.StringList{"gpu_temp_mean_30d", "power_usage_mean_24h"})
	if len(sources) != 1 {
		t.Fatalf("only metrics referenced by long-window columns should require long coverage: %+v", sources)
	}
	if _, ok := sources["gpu_temp"]; !ok {
		t.Fatalf("gpu_temp should require long coverage: %+v", sources)
	}
}

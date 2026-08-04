package history

import (
	"strings"
	"testing"
	"time"

	promclient "atlas/internal/prometheus"
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

package history

import (
	"testing"
	"time"
)

func TestQualifyControlFeatureRequiresTelemetryAndComparableLoad(t *testing.T) {
	positive := extractedFeatureRow{Features: map[string]float64{"gpu_util_mean_24h": 72}}
	control := extractedFeatureRow{
		MetricCoverage: 0.8,
		Features: map[string]float64{
			"gpu_util_mean_24h": 68, "gpu_temp_sample_count_24h": 289,
			"power_usage_sample_count_24h": 289, "gpu_util_sample_count_24h": 289,
		},
	}
	row := qualifyControlFeature(healthyControlRequest{}, control, positive, 0.7)
	if row.TrainingStatus != "eligible" || row.ExclusionReason != "" || row.TelemetryContinuity != 1 {
		t.Fatalf("expected eligible matched control, got %+v", row)
	}
	control.Features["gpu_util_mean_24h"] = 5
	row = qualifyControlFeature(healthyControlRequest{}, control, positive, 0.7)
	if row.ExclusionReason != "load_mismatch" {
		t.Fatalf("expected load mismatch, got %+v", row)
	}
	control.Features["gpu_util_mean_24h"] = 68
	control.Features["gpu_temp_sample_count_24h"] = 10
	row = qualifyControlFeature(healthyControlRequest{}, control, positive, 0.7)
	if row.ExclusionReason != "telemetry_discontinuous" {
		t.Fatalf("expected discontinuous telemetry, got %+v", row)
	}
}

func TestControlFeatureWindowKeyDeduplicatesPairedHorizons(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	left := healthyControlRequest{GPUUUID: "GPU-ABC", FeatureCutoffAt: cutoff, PairedSampleKey: "1m"}
	right := healthyControlRequest{GPUUUID: "gpu-abc", FeatureCutoffAt: cutoff, PairedSampleKey: "7d"}
	if controlFeatureWindowKey(left) != controlFeatureWindowKey(right) {
		t.Fatal("same GPU and cutoff must share one Prometheus query")
	}
}

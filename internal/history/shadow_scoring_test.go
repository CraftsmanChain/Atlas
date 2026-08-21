package history

import (
	"math"
	"testing"

	"atlas/pkg/api"
)

func TestScoreShadowModelStandardizesAndCalibrates(t *testing.T) {
	model := logisticModel{
		FeatureColumns: []string{"gpu_temp_mean_24h", "power_usage_mean_24h"},
		Means:          []float64{50, 300}, Scales: []float64{10, 50}, Coefficients: []float64{1, -0.5},
		Intercept: 0.2, Calibration: probabilityCalibration{Status: "fitted", Slope: 0.8, Intercept: -0.1},
	}
	features := map[string]float64{"gpu_temp_mean_24h": 60, "power_usage_mean_24h": 250}
	rawLogit := 0.2 + 1 + 0.5
	expected := sigmoid(0.8*logitProbability(sigmoid(rawLogit)) - 0.1)
	if actual := scoreShadowModel(model, features); math.Abs(actual-expected) > 1e-12 {
		t.Fatalf("probability=%v expected=%v", actual, expected)
	}
}

func TestShadowFeatureVectorSHAIsStableAndColumnOrdered(t *testing.T) {
	features := map[string]float64{"a": 1, "b": 2}
	left := shadowFeatureVectorSHA([]string{"a", "b"}, features)
	right := shadowFeatureVectorSHA([]string{"a", "b"}, map[string]float64{"b": 2, "a": 1})
	if left == "" || left != right {
		t.Fatalf("feature hash is not deterministic: %q %q", left, right)
	}
	if left == shadowFeatureVectorSHA([]string{"b", "a"}, features) {
		t.Fatal("feature hash lost model column order")
	}
}

func TestShadowQuantileInterpolatesFleetDistribution(t *testing.T) {
	values := []float64{0.1, 0.2, 0.3, 0.4}
	if actual := shadowQuantile(values, 0.5); math.Abs(actual-0.25) > 1e-12 {
		t.Fatalf("median=%v expected=0.25", actual)
	}
}

func TestUnambiguousDataCentersByNodeRejectsConflicts(t *testing.T) {
	assets := []api.InfrastructureAsset{
		{IPAddress: "10.0.0.1", DataCenterID: "dc-a"},
		{IPAddress: "10.0.0.1", DataCenterID: "dc-a"},
		{IPAddress: "10.0.0.2", DataCenterID: "dc-a"},
		{IPAddress: "10.0.0.2", DataCenterID: "dc-b"},
		{IPAddress: "10.0.0.3", DataCenterID: ""},
	}
	result := unambiguousDataCentersByNode(assets)
	if result["10.0.0.1"] != "dc-a" {
		t.Fatalf("matching authoritative sources should freeze one data center: %+v", result)
	}
	if _, exists := result["10.0.0.2"]; exists {
		t.Fatalf("conflicting data centers must remain missing instead of choosing one: %+v", result)
	}
	if _, exists := result["10.0.0.3"]; exists {
		t.Fatalf("empty data centers must not be frozen: %+v", result)
	}
}

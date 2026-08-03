package history

import (
	"testing"
	"time"
)

func TestAssembleTrainingMatrixPreservesMissingValuesAndWeightsClasses(t *testing.T) {
	onset := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	positive := extractedFeatureRow{SampleKey: "p1", GPUUUID: "GPU-A", NodeIP: "10.0.0.1", ModelName: "H100", HorizonMinutes: 60,
		FeatureCutoffAt: onset.Add(-time.Hour), LabelOnsetAt: onset, LabelWeight: 0.8, FeatureContract: "1.9.0", MetricCoverage: 0.8,
		Features: map[string]float64{"gpu_util_mean_24h": 70, "gpu_temp_mean_24h": 65}}
	controlFeature := extractedFeatureRow{SampleKey: "window", GPUUUID: "GPU-A", NodeIP: "10.0.0.1", ModelName: "H100", HorizonMinutes: 60,
		FeatureCutoffAt: onset.Add(-10 * 24 * time.Hour), FeatureContract: "1.9.0", MetricCoverage: 0.9,
		Features: map[string]float64{"gpu_util_mean_24h": 75}}
	metadata := trainingLabelMetadata{EventTypes: []string{"xid_94_contained_ecc"}, DriverVersions: []string{"560.35.03"}, LabelSources: []string{"versioned_rule"}}
	positives := []preparedTrainingSample{{Sample: positive, LabelMetadata: metadata, LabelValue: 1, TrainingStatus: "eligible", Split: "train"}}
	controls := []controlFeatureRow{{Request: healthyControlRequest{ControlKey: "c1", PairedSampleKey: "p1", GPUUUID: "GPU-A", NodeIP: "10.0.0.1", ModelName: "H100", HorizonMinutes: 60, FeatureCutoffAt: controlFeature.FeatureCutoffAt, Split: "train"}, Feature: controlFeature, ControlLoadBucket: "high", TrainingStatus: "eligible"}}
	rows, columns, audit := assembleTrainingMatrix(positives, controls, "1.9.0")
	if len(rows) != 2 || len(columns) != 2 || audit != (matrixAudit{}) {
		t.Fatalf("unexpected rows=%d columns=%v audit=%+v", len(rows), columns, audit)
	}
	applyClassWeights(rows)
	for _, row := range rows {
		if row.ClassWeight != 1 {
			t.Fatalf("balanced pair class weight=%v", row.ClassWeight)
		}
		if row.SampleKind == "positive" && row.TrainingWeight != 0.8 {
			t.Fatalf("positive evidence weight lost: %+v", row)
		}
		if row.SampleKind == "healthy_control" {
			if len(row.LabelMetadata.EventTypes) != 1 || row.LabelMetadata.EventTypes[0] != "xid_94_contained_ecc" {
				t.Fatalf("control lost paired fault metadata: %+v", row.LabelMetadata)
			}
			if _, exists := row.Features["gpu_temp_mean_24h"]; exists {
				t.Fatal("missing control feature must stay absent rather than become zero")
			}
		}
	}
}

func TestAssembleTrainingMatrixRejectsLeakageAndPairMismatch(t *testing.T) {
	onset := time.Now().UTC()
	positive := extractedFeatureRow{SampleKey: "p1", GPUUUID: "GPU-A", HorizonMinutes: 10, FeatureCutoffAt: onset, LabelOnsetAt: onset, FeatureContract: "1.9.0", Features: map[string]float64{"gpu_util_mean_24h": 1}}
	positives := []preparedTrainingSample{{Sample: positive, TrainingStatus: "eligible", Split: "train"}}
	control := controlFeatureRow{Request: healthyControlRequest{ControlKey: "c1", PairedSampleKey: "missing", GPUUUID: "GPU-A", HorizonMinutes: 10, Split: "test"}, Feature: extractedFeatureRow{FeatureContract: "1.9.0"}, TrainingStatus: "eligible"}
	rows, _, audit := assembleTrainingMatrix(positives, []controlFeatureRow{control}, "1.9.0")
	if len(rows) != 0 || audit.pointInTime != 1 || audit.pairing != 1 {
		t.Fatalf("audit did not reject leakage: rows=%d audit=%+v", len(rows), audit)
	}
}

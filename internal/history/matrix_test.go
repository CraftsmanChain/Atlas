package history

import (
	"encoding/json"
	"fmt"
	"strings"
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

func TestCohortReadinessGatesEachFaultModelAndHorizon(t *testing.T) {
	rows := make([]trainingMatrixRow, 0, 150)
	add := func(eventType, split string, horizon, positives, controls, positiveGPUs int) {
		metadata := trainingLabelMetadata{EventTypes: []string{eventType}}
		for index := 0; index < positives; index++ {
			rows = append(rows, trainingMatrixRow{LabelValue: 1, Split: split, HorizonMinutes: horizon, GPUUUID: fmt.Sprintf("GPU-%s-P-%02d", split, index%positiveGPUs), ModelName: "H100", LabelMetadata: metadata})
		}
		for index := 0; index < controls; index++ {
			rows = append(rows, trainingMatrixRow{LabelValue: 0, Split: split, HorizonMinutes: horizon, GPUUUID: fmt.Sprintf("GPU-%s-C-%02d", split, index), ModelName: "H100", LabelMetadata: metadata})
		}
	}
	add("gpu_dropout", "train", 60, 30, 60, 10)
	add("gpu_dropout", "validation", 60, 10, 20, 5)
	add("gpu_dropout", "test", 60, 10, 20, 5)
	add("xid_94_contained_ecc", "test", 60, 1, 1, 1)
	report := evaluateCohortReadiness("matrix", rows)
	if report.ReadyStrata != 1 || report.InsufficientStrata != 1 || len(report.Strata) != 2 {
		t.Fatalf("unexpected readiness summary: %+v", report)
	}
	if report.Strata[0].EventType != "gpu_dropout" || report.Strata[0].Status != "exploratory_ready" {
		t.Fatalf("expected dropout stratum to pass: %+v", report.Strata[0])
	}
	if report.Strata[0].BlockingReasons == nil {
		t.Fatal("ready stratum blocking_reasons must be an empty array, not null")
	}
	payload, err := json.Marshal(report.Strata[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"blocking_reasons":[]`) {
		t.Fatalf("ready stratum JSON must preserve an empty blocking_reasons array: %s", payload)
	}
	if report.Strata[1].Status != "insufficient_data" || len(report.Strata[1].BlockingReasons) == 0 {
		t.Fatalf("expected sparse XID stratum to be blocked: %+v", report.Strata[1])
	}
}

package diskhealth

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

func pm893Fixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/smart-pm893-redacted.json")
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFieldPM893Profile(t *testing.T) {
	for _, name := range []string{"Reported_Uncorrect", "Uncorrectable_Error_Cnt"} {
		data := pm893Fixture(t)
		ataAttribute(data, 187)["name"] = name
		row := Assess(sampleInput(), marshal(t, data), nil, cutoff)
		if row.AssessmentStatus != "assessed" || row.Level != "clear_for_checked_signals" || row.Disk.Profile != "ata-samsung-pm893-v1" {
			t.Fatalf("invalid field profile: %+v", row)
		}
		if row.Disk.Wear.Value != "98" || row.Disk.Wear.Kind != "normalized_index" || row.Disk.Wear.Unit != "normalized" || *row.Disk.Temperature != "30" {
			t.Fatal("wear or packed temperature semantics lost")
		}
		if row.Disk.Dimensions[1].State != "insufficient_data" || row.Disk.Dimensions[2].State != "insufficient_data" {
			t.Fatal("unverified endurance/temperature range became checked")
		}
		if row.TrendStatus != "unavailable" || len(row.Disk.Deltas) != 0 {
			t.Fatal("single field reading fabricated a trend")
		}
	}
}

func TestPM893SafetyBoundaries(t *testing.T) {
	tests := []struct {
		name, level, blocker string
		change               func(map[string]any, *Input)
	}{
		{"firmware", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any, _ *Input) { d["firmware_version"] = "JXTC999Q" }},
		{"capacity", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any, _ *Input) { d["model_name"] = "SAMSUNG MZ7L3960HCJR-00B7C" }},
		{"oem_suffix", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any, _ *Input) { d["model_name"] = "SAMSUNG MZ7L3480HCHQ-00A07" }},
		{"rotation", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any, _ *Input) { d["rotation_rate"] = 7200 }},
		{"reallocation_history", "attention", "", func(d map[string]any, _ *Input) { setATACounter(d, 5, "3") }},
		{"media_history", "attention", "", func(d map[string]any, _ *Input) { setATACounter(d, 187, "9007199254740993") }},
		{"crc_history", "attention", "", func(d map[string]any, _ *Input) { setATACounter(d, 199, "2") }},
		{"pending_current", "warning", "", func(d map[string]any, _ *Input) { setATACounter(d, 197, "1") }},
		{"packed_187", "unknown", "ata_raw_encoding_unverified:187", func(d map[string]any, _ *Input) { ataAttribute(d, 187)["raw"].(map[string]any)["string"] = "0 0 0" }},
		{"overflow_187", "unknown", "ata_raw_encoding_unverified:187", func(d map[string]any, _ *Input) { setATACounter(d, 187, "18446744073709551616") }},
		{"wrong_187_name", "unknown", "ata_raw_encoding_unverified:187", func(d map[string]any, _ *Input) { ataAttribute(d, 187)["name"] = "Unknown_SSD_Attribute" }},
		{"missing_pending", "unknown", "ata_raw_encoding_unverified:197", func(d map[string]any, _ *Input) { delete(ataAttribute(d, 197), "raw") }},
		{"missing_wear", "unknown", "ata_wear_indicator_missing_or_invalid", func(d map[string]any, _ *Input) { delete(ataAttribute(d, 177), "value") }},
		{"past_threshold", "attention", "", func(d map[string]any, _ *Input) { ataAttribute(d, 177)["when_failed"] = "past" }},
		{"current_threshold", "critical", "", func(d map[string]any, _ *Input) {
			a := ataAttribute(d, 177)
			a["value"], a["when_failed"] = a["thresh"], "now"
		}},
		{"threshold_conflict", "critical", "ata_threshold_status_conflict", func(d map[string]any, _ *Input) { ataAttribute(d, 177)["value"] = float64(5) }},
		{"critical_with_missing", "critical", "ata_raw_encoding_unverified:197", func(d map[string]any, _ *Input) {
			d["smart_status"].(map[string]any)["passed"] = false
			delete(ataAttribute(d, 197), "raw")
		}},
		{"unknown_with_failed", "critical", "protocol_or_model_rules_unsupported", func(d map[string]any, _ *Input) {
			d["firmware_version"] = "unknown"
			d["smart_status"].(map[string]any)["passed"] = false
		}},
		{"partial_command", "unknown", "partial_smart_command_failure", func(d map[string]any, in *Input) {
			d["smartctl"].(map[string]any)["exit_status"] = float64(4)
			*in.ExitCode = 4
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, in := pm893Fixture(t), sampleInput()
			test.change(data, &in)
			row := Assess(in, marshal(t, data), nil, cutoff)
			if row.Level != test.level || test.blocker != "" && (!slices.Contains(row.Blockers, test.blocker) || row.AssessmentStatus != "insufficient_data") {
				t.Fatalf("row=%+v", row)
			}
		})
	}
	data := ataFixture(t, true)
	ataAttribute(data, 187)["name"] = "Reported_Uncorrect"
	if row := Assess(sampleInput(), marshal(t, data), nil, cutoff); !slices.Contains(row.Blockers, "ata_raw_encoding_unverified:187") {
		t.Fatal("PM893 alias leaked to consumer SSD")
	}
}

func TestPM893CounterAndGaugeHistory(t *testing.T) {
	for _, scenario := range []string{"increase", "reset", "pending_decrease", "firmware_change", "identity_change", "future_baseline"} {
		t.Run(scenario, func(t *testing.T) {
			before, after := pm893Fixture(t), pm893Fixture(t)
			setATACounter(before, 187, "9007199254740993")
			setATACounter(after, 187, "9007199254740994")
			in, base := sampleInput(), sampleInput()
			base.ObservedAt, base.AvailableAt = in.ObservedAt.Add(-time.Minute), in.AvailableAt.Add(-time.Minute)
			in.Baseline = &base
			expected := "available"
			switch scenario {
			case "reset":
				setATACounter(after, 187, "0")
				expected = "counter_reset"
			case "pending_decrease":
				setATACounter(before, 197, "5")
				setATACounter(after, 197, "0")
			case "firmware_change":
				before["firmware_version"] = "JXTC104Q"
				expected = "invalid_baseline"
			case "identity_change":
				after["serial_number"] = "replacement-fixture"
				expected = "invalid_baseline"
			case "future_baseline":
				base.AvailableAt = cutoff.Add(time.Second)
				expected = "invalid_baseline"
			}
			row := Assess(in, marshal(t, after), marshal(t, before), cutoff)
			if row.TrendStatus != expected {
				t.Fatalf("row=%+v", row)
			}
			if expected == "available" {
				if len(row.Disk.Deltas) != 3 || row.Disk.Deltas[2].Field != "ata.187.raw" || row.Disk.Deltas[2].Value != "1" {
					t.Fatal("exact media delta lost or pending gauge treated as monotonic")
				}
			} else if len(row.Disk.Deltas) != 0 {
				t.Fatal("invalid/reset history leaked partial deltas")
			}
		})
	}
}

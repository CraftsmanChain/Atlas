package diskhealth

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

func ataFixture(t *testing.T, ssd bool) map[string]any {
	t.Helper()
	name := "testdata/smart-ata.json"
	if ssd {
		name = "testdata/smart-sata-ssd.json"
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func ataAttribute(data map[string]any, id int) map[string]any {
	for _, attr := range data["ata_smart_attributes"].(map[string]any)["table"].([]any) {
		value := attr.(map[string]any)
		if int(value["id"].(float64)) == id {
			return value
		}
	}
	panic("fixture attribute missing")
}

func setATACounter(data map[string]any, id int, value string) {
	ataAttribute(data, id)["raw"] = map[string]any{"value": json.Number(value), "string": value}
}

func TestPublicATAProfiles(t *testing.T) {
	for _, ssd := range []bool{false, true} {
		row := Assess(sampleInput(), marshal(t, ataFixture(t, ssd)), nil, cutoff)
		if row.AssessmentStatus != "assessed" || row.Level != "clear_for_checked_signals" || row.Disk == nil || len(row.Disk.Dimensions) != 4 {
			t.Fatalf("invalid ATA assessment: %+v", row)
		}
		if row.Disk.Dimensions[2].State != "insufficient_data" {
			t.Fatal("unverified ATA temperature range became clear")
		}
		if ssd {
			if row.Disk.Dimensions[1].State != "insufficient_data" {
				t.Fatal("normalized wear alone became checked endurance")
			}
			if row.Disk.Wear == nil || row.Disk.Wear.Value != "81" || row.Disk.Wear.Kind != "normalized_index" {
				t.Fatal("SSD normalized wear treated as percent")
			}
		} else if row.Disk.Wear != nil || row.Disk.Dimensions[1].State != "not_applicable" {
			t.Fatal("HDD assigned SSD endurance")
		}
	}
}

func TestATABoundaries(t *testing.T) {
	tests := []struct {
		name, level, blocker string
		change               func(map[string]any, *Input)
	}{
		{"pending", "warning", "", func(d map[string]any, _ *Input) { setATACounter(d, 197, "2") }},
		{"uncorrectable", "warning", "", func(d map[string]any, _ *Input) { setATACounter(d, 198, "1") }},
		{"historical_reallocation", "attention", "", func(d map[string]any, _ *Input) { setATACounter(d, 5, "9007199254740993") }},
		{"historical_crc", "attention", "", func(d map[string]any, _ *Input) { setATACounter(d, 199, "1") }},
		{"packed_raw", "unknown", "ata_raw_encoding_unverified:5", func(d map[string]any, _ *Input) { ataAttribute(d, 5)["raw"].(map[string]any)["string"] = "0 0 0" }},
		{"wrong_name", "unknown", "ata_raw_encoding_unverified:5", func(d map[string]any, _ *Input) { ataAttribute(d, 5)["name"] = "Vendor_Encoded" }},
		{"unknown_model", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any, _ *Input) { d["model_name"] = "unverified-disk" }},
		{"unverified_firmware", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any, _ *Input) { d["firmware_version"] = "unverified-firmware" }},
		{"missing_rotation", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any, _ *Input) { delete(d, "rotation_rate") }},
		{"missing_when_failed", "unknown", "ata_threshold_metadata_invalid", func(d map[string]any, _ *Input) { delete(ataAttribute(d, 5), "when_failed") }},
		{"null_when_failed", "unknown", "ata_threshold_metadata_invalid", func(d map[string]any, _ *Input) { ataAttribute(d, 5)["when_failed"] = nil }},
		{"triple_duplicate", "unknown", "ata_attribute_id_duplicate", func(d map[string]any, _ *Input) {
			table := d["ata_smart_attributes"].(map[string]any)
			table["table"] = append(table["table"].([]any), ataAttribute(d, 5), ataAttribute(d, 5))
		}},
		{"missing_actual_exit", "unknown", "execution_exit_missing", func(_ map[string]any, in *Input) { in.ExitCode = nil }},
		{"unknown_but_failed", "critical", "protocol_or_model_rules_unsupported", func(d map[string]any, _ *Input) {
			d["model_name"] = "unverified-disk"
			d["smart_status"].(map[string]any)["passed"] = false
		}},
		{"current_prefailure", "critical", "", func(d map[string]any, _ *Input) {
			a := ataAttribute(d, 5)
			a["value"], a["when_failed"] = a["thresh"], "now"
		}},
		{"past_threshold", "attention", "", func(d map[string]any, _ *Input) { ataAttribute(d, 5)["when_failed"] = "past" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, in := ataFixture(t, false), sampleInput()
			test.change(data, &in)
			row := Assess(in, marshal(t, data), nil, cutoff)
			if row.Level != test.level || (test.blocker != "" && !slices.Contains(row.Blockers, test.blocker)) {
				t.Fatalf("row=%+v", row)
			}
			if test.blocker != "" && row.AssessmentStatus != "insufficient_data" {
				t.Fatal("blocked row became assessed")
			}
			if test.name == "triple_duplicate" {
				for _, e := range row.Evidence {
					if e.Field == "ata.5.raw" {
						t.Fatal("duplicate poisoned attribute reused")
					}
				}
			}
		})
	}
}

func TestATABaselineDeltasAndReset(t *testing.T) {
	for _, ssd := range []bool{false, true} {
		for _, scenario := range []string{"increase", "no_change", "reset", "boot_change", "identity_change", "missing_current"} {
			t.Run(scenario+map[bool]string{true: "_ssd", false: "_hdd"}[ssd], func(t *testing.T) {
				before, after := ataFixture(t, ssd), ataFixture(t, ssd)
				setATACounter(before, 199, "9007199254740993")
				setATACounter(after, 199, "9007199254740994")
				in, baseline := sampleInput(), sampleInput()
				baseline.ObservedAt = in.ObservedAt.Add(-time.Minute)
				baseline.AvailableAt = in.AvailableAt.Add(-time.Minute)
				in.Baseline = &baseline
				expected := "available"
				switch scenario {
				case "no_change":
					setATACounter(after, 199, "9007199254740993")
				case "reset":
					setATACounter(after, 199, "0")
					expected = "counter_reset"
				case "boot_change":
					in.BootID = "other"
					expected = "invalid_baseline"
				case "identity_change":
					after["serial_number"] = "other"
					expected = "invalid_baseline"
				case "missing_current":
					delete(ataAttribute(after, 5), "raw")
					expected = "invalid_baseline"
				}
				row := Assess(in, marshal(t, after), marshal(t, before), cutoff)
				if row.TrendStatus != expected {
					t.Fatalf("row=%+v", row)
				}
				if expected == "available" {
					want := "1"
					if scenario == "no_change" {
						want = "0"
					}
					if row.Disk.Deltas[1].Value != want || row.MediaDelta != nil {
						t.Fatal("counter delta precision/namespace lost")
					}
				} else if len(row.Disk.Deltas) != 0 {
					t.Fatal("invalid baseline leaked partial deltas")
				}
			})
		}
	}
}

func TestSATASSDBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, level, blocker string
		change               func(map[string]any)
	}{
		{"media_history", "attention", "", func(d map[string]any) { setATACounter(d, 187, "7") }},
		{"missing_wear", "unknown", "ata_wear_indicator_missing_or_invalid", func(d map[string]any) { delete(ataAttribute(d, 177), "value") }},
		{"family_unverified", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any) { d["model_family"] = "unverified" }},
		{"firmware_unverified", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any) { d["firmware_version"] = "unverified" }},
		{"manufacturer_failed", "critical", "", func(d map[string]any) { d["smart_status"].(map[string]any)["passed"] = false }},
		{"current_threshold", "critical", "", func(d map[string]any) { a := ataAttribute(d, 5); a["value"], a["when_failed"] = a["thresh"], "now" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := ataFixture(t, true)
			test.change(data)
			row := Assess(sampleInput(), marshal(t, data), nil, cutoff)
			if row.Level != test.level || (test.blocker != "" && !slices.Contains(row.Blockers, test.blocker)) {
				t.Fatalf("row=%+v", row)
			}
		})
	}
}

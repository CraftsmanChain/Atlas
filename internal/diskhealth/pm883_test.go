package diskhealth

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

func pm883Fixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/smart-pm883-redacted.json")
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFieldPM883Profile(t *testing.T) {
	data := pm883Fixture(t)
	row := Assess(sampleInput(), marshal(t, data), nil, cutoff)
	if row.AssessmentStatus != "assessed" || row.Level != "clear_for_checked_signals" ||
		row.Support != "fixture_verified" || row.Disk == nil || row.Disk.Profile != "ata-samsung-pm883-v1" {
		t.Fatalf("invalid PM883 field assessment: %+v", row)
	}
	if row.RuleVersion != "disk-health-v1.3.0" || ReportRuleVersion != "hardware-health-v1.4.0" {
		t.Fatalf("PM883 semantics did not advance versioned contracts: %s / %s", row.RuleVersion, ReportRuleVersion)
	}
	if row.Disk.Wear == nil || row.Disk.Wear.Field != "ata.177.normalized" || row.Disk.Wear.Value != "99" ||
		row.Disk.Wear.Kind != "normalized_index" || row.Disk.Wear.Unit != "normalized" {
		t.Fatalf("PM883 normalized wear became health or lifetime: %+v", row.Disk.Wear)
	}
	if len(row.Disk.Dimensions) != 4 || row.Disk.Dimensions[1].State != "insufficient_data" || row.Disk.Dimensions[2].State != "insufficient_data" {
		t.Fatalf("unverified endurance/temperature alarm coverage became checked: %+v", row.Disk.Dimensions)
	}
	if row.TrendStatus != "unavailable" || len(row.Disk.Deltas) != 0 {
		t.Fatal("one field capture fabricated history")
	}
	for _, evidence := range row.Evidence {
		if evidence.Field == "ata.197.raw" || evidence.Field == "ata.202.raw" {
			t.Fatalf("unverified optional field entered PM883 core rules: %+v", evidence)
		}
	}
}

func TestPM883SafetyBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, level, blocker string
		change               func(map[string]any)
	}{
		{"unknown_firmware", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any) { d["firmware_version"] = "HXT7404Q" }},
		{"other_capacity", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any) { d["model_name"] = "SAMSUNG MZ7LH960HAJR-00005" }},
		{"other_suffix", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any) { d["model_name"] = "SAMSUNG MZ7LH480HAHQ-00000" }},
		{"rotation", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any) { d["rotation_rate"] = 7200 }},
		{"missing_rotation", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any) { delete(d, "rotation_rate") }},
		{"pm893_alias_rejected", "unknown", "ata_raw_encoding_unverified:187", func(d map[string]any) { ataAttribute(d, 187)["name"] = "Reported_Uncorrect" }},
		{"missing_reallocation", "unknown", "ata_raw_encoding_unverified:5", func(d map[string]any) { delete(ataAttribute(d, 5), "raw") }},
		{"missing_media", "unknown", "ata_raw_encoding_unverified:187", func(d map[string]any) { delete(ataAttribute(d, 187), "raw") }},
		{"missing_crc", "unknown", "ata_raw_encoding_unverified:199", func(d map[string]any) { delete(ataAttribute(d, 199), "raw") }},
		{"missing_wear", "unknown", "ata_wear_indicator_missing_or_invalid", func(d map[string]any) { delete(ataAttribute(d, 177), "value") }},
		{"packed_raw", "unknown", "ata_raw_encoding_unverified:187", func(d map[string]any) { ataAttribute(d, 187)["raw"].(map[string]any)["string"] = "0 0 0" }},
		{"mismatched_raw", "unknown", "ata_raw_encoding_unverified:199", func(d map[string]any) { ataAttribute(d, 199)["raw"].(map[string]any)["string"] = "1" }},
		{"overflow_64", "unknown", "ata_raw_encoding_unverified:187", func(d map[string]any) { setATACounter(d, 187, "18446744073709551616") }},
		{"negative_counter", "unknown", "ata_raw_encoding_unverified:199", func(d map[string]any) { setATACounter(d, 199, "-1") }},
		{"uint64_maximum", "attention", "", func(d map[string]any) { setATACounter(d, 187, "18446744073709551615") }},
		{"reallocation_history", "attention", "", func(d map[string]any) { setATACounter(d, 5, "1") }},
		{"media_history", "attention", "", func(d map[string]any) { setATACounter(d, 187, "1") }},
		{"crc_history", "attention", "", func(d map[string]any) { setATACounter(d, 199, "1") }},
		{"unsupported_pending", "clear_for_checked_signals", "", func(d map[string]any) { setATACounter(d, 197, "1") }},
		{"current_threshold", "critical", "", func(d map[string]any) { a := ataAttribute(d, 5); a["value"], a["when_failed"] = a["thresh"], "now" }},
		{"exception_threshold_still_visible", "critical", "", func(d map[string]any) {
			a := ataAttribute(d, 202)
			a["value"], a["when_failed"] = a["thresh"], "now"
			a["flags"].(map[string]any)["value"] = float64(1)
		}},
		{"critical_with_missing", "critical", "ata_raw_encoding_unverified:187", func(d map[string]any) {
			d["smart_status"].(map[string]any)["passed"] = false
			delete(ataAttribute(d, 187), "raw")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := pm883Fixture(t)
			test.change(data)
			row := Assess(sampleInput(), marshal(t, data), nil, cutoff)
			if row.Level != test.level || (test.blocker != "" && (!slices.Contains(row.Blockers, test.blocker) || row.AssessmentStatus != "insufficient_data")) {
				t.Fatalf("unexpected PM883 safety result: %+v", row)
			}
		})
	}
}

func TestPM883ExceptionModeIsNotWearAndPendingIsNotRequired(t *testing.T) {
	for _, name := range []string{"Exception_Mode_Status", "Lifetime_Remaining"} {
		data := pm883Fixture(t)
		ataAttribute(data, 202)["name"] = name
		ataAttribute(data, 202)["value"] = float64(75)
		setATACounter(data, 202, "123")
		delete(ataAttribute(data, 177), "value")
		row := Assess(sampleInput(), marshal(t, data), nil, cutoff)
		if row.Disk.Wear != nil || !slices.Contains(row.Blockers, "ata_wear_indicator_missing_or_invalid") {
			t.Fatalf("attribute 202 replaced verified wear field: %+v", row)
		}
	}
	data := pm883Fixture(t)
	attrs := data["ata_smart_attributes"].(map[string]any)
	attrs["table"] = slices.DeleteFunc(attrs["table"].([]any), func(a any) bool { return a.(map[string]any)["id"] == float64(197) })
	row := Assess(sampleInput(), marshal(t, data), nil, cutoff)
	if row.AssessmentStatus != "assessed" || row.Disk.Profile != "ata-samsung-pm883-v1" {
		t.Fatalf("PM883 acquired the PM893-only pending requirement: %+v", row)
	}
}

func TestPM883ExactHistoryAndEntityIsolation(t *testing.T) {
	for _, scenario := range []string{"increase", "reset", "identity_change", "old_pm893", "firmware_change", "generation_change"} {
		t.Run(scenario, func(t *testing.T) {
			before, after := pm883Fixture(t), pm883Fixture(t)
			setATACounter(before, 187, "18446744073709551614")
			setATACounter(after, 187, "18446744073709551615")
			in, base := sampleInput(), sampleInput()
			base.ObservedAt, base.AvailableAt = in.ObservedAt.Add(-time.Minute), in.AvailableAt.Add(-time.Minute)
			in.Baseline = &base
			want := "available"
			switch scenario {
			case "reset":
				setATACounter(after, 187, "0")
				want = "counter_reset"
			case "identity_change":
				after["serial_number"] = "fixture-pm883-replacement-not-real"
				want = "invalid_baseline"
			case "old_pm893":
				before = pm893Fixture(t)
				want = "invalid_baseline"
			case "firmware_change":
				before["firmware_version"] = "HXT7404Q"
				want = "invalid_baseline"
			case "generation_change":
				base.Generation = "previous-installation"
				want = "invalid_baseline"
			}
			row := Assess(in, marshal(t, after), marshal(t, before), cutoff)
			if row.TrendStatus != want {
				t.Fatalf("incorrect PM883 history gate: %+v", row)
			}
			if want == "available" {
				if len(row.Disk.Deltas) != 3 || row.Disk.Deltas[2].Field != "ata.187.raw" || row.Disk.Deltas[2].Value != "1" {
					t.Fatalf("PM883 uint64 delta lost precision or scope: %+v", row.Disk.Deltas)
				}
			} else if len(row.Disk.Deltas) != 0 {
				t.Fatal("invalid or reset history leaked deltas")
			}
		})
	}
	current := Assess(sampleInput(), marshal(t, pm883Fixture(t)), nil, cutoff)
	previous := Assess(sampleInput(), marshal(t, pm893Fixture(t)), nil, cutoff)
	if current.EntityID == previous.EntityID {
		t.Fatal("replacement PM883 reused the earlier PM893 entity")
	}
}

func TestPM883FieldFixtureContainsOnlyAllowedProjection(t *testing.T) {
	data := pm883Fixture(t)
	if data["serial_number"] != "fixture-pm883-not-real" {
		t.Fatal("field fixture must carry an explicitly synthetic serial")
	}
	allowed := []string{"json_format_version", "smartctl", "device", "model_family", "model_name", "firmware_version", "serial_number", "rotation_rate", "in_smartctl_database", "smart_status", "temperature", "ata_smart_attributes"}
	for key := range data {
		if !slices.Contains(allowed, key) {
			t.Fatalf("field fixture contains unapproved top-level metadata %q", key)
		}
	}
	for key := range data["smartctl"].(map[string]any) {
		if key != "version" && key != "exit_status" {
			t.Fatalf("smartctl metadata can contain private command output: %q", key)
		}
	}
	for key := range data["device"].(map[string]any) {
		if key != "name" && key != "type" && key != "protocol" {
			t.Fatalf("device identity outside the fixture allowlist: %q", key)
		}
	}
	for _, value := range data["ata_smart_attributes"].(map[string]any)["table"].([]any) {
		attribute := value.(map[string]any)
		for key := range attribute {
			if !slices.Contains([]string{"id", "name", "value", "worst", "thresh", "when_failed", "flags", "raw"}, key) {
				t.Fatalf("SMART row contains unapproved metadata %q", key)
			}
		}
		for key := range attribute["flags"].(map[string]any) {
			if !slices.Contains([]string{"value", "string", "prefailure", "updated_online", "performance", "error_rate", "event_count", "auto_keep"}, key) {
				t.Fatalf("SMART flags contain unapproved metadata %q", key)
			}
		}
		for key := range attribute["raw"].(map[string]any) {
			if key != "value" && key != "string" {
				t.Fatalf("SMART raw values contain unapproved metadata %q", key)
			}
		}
	}
}

package diskhealth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var cutoff = time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)

func sampleInput() Input {
	exit := 0
	return Input{Reference: "disk-a", Scope: "test-cluster", Node: "test-node", Generation: "1", BootID: "boot-1", Source: "smartctl", SourceKind: "fixture", ObservedAt: cutoff.Add(-time.Minute), AvailableAt: cutoff.Add(-30 * time.Second), MaxAgeSeconds: 1800, ExitCode: &exit}
}

func sampleJSON() map[string]any {
	return map[string]any{"json_format_version": []int{1, 0}, "smartctl": map[string]any{"version": []int{7, 4}, "exit_status": 0}, "device": map[string]any{"name": "/dev/nvme0", "protocol": "NVMe"}, "nvme_number_of_namespaces": 1, "serial_number": "fixture-serial", "model_name": "fixture-model", "firmware_version": "fixture-fw", "smart_status": map[string]any{"passed": true}, "nvme_smart_health_information_log": map[string]any{"critical_warning": 0, "available_spare": 100, "available_spare_threshold": 10, "percentage_used": 20, "media_errors": 0, "num_err_log_entries": 0, "unsafe_shutdowns": 0, "temperature": 36}}
}
func marshal(t *testing.T, data any) []byte {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAssessmentBoundaries(t *testing.T) {
	tests := []struct {
		name, level, blocker string
		change               func(map[string]any, *Input)
	}{
		{"clear_subset", "clear_for_checked_signals", "", func(_ map[string]any, _ *Input) {}},
		{"multi_namespace", "unknown", "namespace_scope_unverified", func(d map[string]any, _ *Input) { d["nvme_number_of_namespaces"] = 2 }},
		{"zero_namespace", "unknown", "namespace_scope_unverified", func(d map[string]any, _ *Input) { d["nvme_number_of_namespaces"] = 0 }},
		{"broadcast_controller_nn132", "clear_for_checked_signals", "", func(d map[string]any, _ *Input) {
			d["nvme_number_of_namespaces"] = 132
			d["device"] = map[string]any{"name": "/dev/nvme0", "type": "nvme", "protocol": "NVMe"}
			d["smartctl"].(map[string]any)["argv"] = []string{"smartctl", "-d", "nvme,0xffffffff", "-j", "-a", "/dev/nvme0"}
		}},
		{"spare_bit", "warning", "", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["critical_warning"] = 1
		}},
		{"temperature_bit", "warning", "", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["critical_warning"] = 2
		}},
		{"reliability_bit", "critical", "smart_status_warning_conflict", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["critical_warning"] = 4
		}},
		{"readonly_bit", "critical", "smart_status_warning_conflict", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["critical_warning"] = 8
		}},
		{"capability_missing", "unknown", "warning_capability_unverified", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["critical_warning"] = 16
		}},
		{"backup_supported", "critical", "", func(d map[string]any, in *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["critical_warning"] = 16
			yes := true
			in.VolatileBackup = &yes
		}},
		{"pmr_supported", "critical", "", func(d map[string]any, in *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["critical_warning"] = 32
			yes := true
			in.PMR = &yes
		}},
		{"unknown_bit", "unknown", "unknown_warning_bits", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["critical_warning"] = 64
		}},
		{"wear100", "warning", "", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["percentage_used"] = 100
		}},
		{"wear255", "warning", "", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["percentage_used"] = 255
		}},
		{"wear256", "unknown", "invalid_or_missing:percentage_used", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["percentage_used"] = 256
		}},
		{"spare_range", "unknown", "invalid_range:available_spare", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["available_spare"] = 101
		}},
		{"spare_low", "warning", "", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["available_spare"] = 5
		}},
		{"historical_media", "attention", "", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["media_errors"] = 7
		}},
		{"nonmedia_log", "clear_for_checked_signals", "", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["num_err_log_entries"] = 9999
		}},
		{"unsafe_shutdowns", "clear_for_checked_signals", "", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["unsafe_shutdowns"] = 9999
		}},
		{"negative", "unknown", "invalid_or_missing:media_errors", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["media_errors"] = -1
		}},
		{"fraction", "unknown", "invalid_or_missing:media_errors", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["media_errors"] = 1.5
		}},
		{"null", "unknown", "invalid_or_missing:media_errors", func(d map[string]any, _ *Input) {
			d["nvme_smart_health_information_log"].(map[string]any)["media_errors"] = nil
		}},
		{"missing", "unknown", "invalid_or_missing:media_errors", func(d map[string]any, _ *Input) {
			delete(d["nvme_smart_health_information_log"].(map[string]any), "media_errors")
		}},
		{"null_status", "unknown", "smart_status_missing_or_invalid", func(d map[string]any, _ *Input) { d["smart_status"] = map[string]any{"passed": nil} }},
		{"known_critical_missing", "critical", "invalid_or_missing:media_errors", func(d map[string]any, _ *Input) {
			d["smart_status"] = map[string]any{"passed": false}
			delete(d["nvme_smart_health_information_log"].(map[string]any), "media_errors")
		}},
		{"fatal_exit", "unknown", "command_or_device_failure", func(d map[string]any, in *Input) {
			d["smartctl"].(map[string]any)["exit_status"] = 2
			value := 2
			in.ExitCode = &value
		}},
		{"partial_exit", "unknown", "partial_smart_command_failure", func(d map[string]any, in *Input) {
			d["smartctl"].(map[string]any)["exit_status"] = 4
			value := 4
			in.ExitCode = &value
		}},
		{"nonzero_health_exit", "critical", "", func(d map[string]any, in *Input) {
			d["smartctl"].(map[string]any)["exit_status"] = 8
			d["smart_status"] = map[string]any{"passed": false}
			value := 8
			in.ExitCode = &value
		}},
		{"exit_conflict", "unknown", "exit_status_conflict", func(_ map[string]any, in *Input) { value := 8; in.ExitCode = &value }},
		{"exit_missing", "unknown", "execution_exit_missing", func(_ map[string]any, in *Input) { in.ExitCode = nil }},
		{"truncated", "unknown", "stdout_truncated", func(_ map[string]any, in *Input) { in.Truncated = true }},
		{"format_missing", "unknown", "unsupported_json_format", func(d map[string]any, _ *Input) { delete(d, "json_format_version") }},
		{"format_future", "unknown", "unsupported_json_format", func(d map[string]any, _ *Input) { d["json_format_version"] = []int{2, 0} }},
		{"tool_future", "unknown", "unsupported_tool_version", func(d map[string]any, _ *Input) { d["smartctl"].(map[string]any)["version"] = []int{8, 0} }},
		{"ata_unsupported", "unknown", "protocol_or_model_rules_unsupported", func(d map[string]any, _ *Input) { d["device"] = map[string]any{"protocol": "ATA"} }},
		{"ata_failed", "critical", "protocol_or_model_rules_unsupported", func(d map[string]any, _ *Input) {
			d["device"] = map[string]any{"protocol": "ATA"}
			d["smart_status"] = map[string]any{"passed": false}
		}},
		{"missing_identity", "unknown", "identity_missing", func(d map[string]any, _ *Input) { delete(d, "serial_number") }},
		{"future_available", "unknown", "after_cutoff", func(_ map[string]any, in *Input) { in.AvailableAt = cutoff.Add(time.Second) }},
		{"stale", "unknown", "stale_evidence", func(_ map[string]any, in *Input) { in.ObservedAt = cutoff.Add(-time.Hour) }},
		{"bad_time_order", "unknown", "time_missing_or_invalid", func(_ map[string]any, in *Input) { in.AvailableAt = in.ObservedAt.Add(-time.Second) }},
		{"unknown_source", "unknown", "source_kind_unknown", func(_ map[string]any, in *Input) { in.SourceKind = "live" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, in := sampleJSON(), sampleInput()
			tc.change(d, &in)
			got := Assess(in, marshal(t, d), nil, cutoff)
			if got.Level != tc.level {
				t.Fatalf("level %s, want %s; %+v", got.Level, tc.level, got)
			}
			if tc.blocker != "" && !contains(got.Blockers, tc.blocker) {
				t.Fatalf("missing blocker %s: %+v", tc.blocker, got)
			}
		})
	}
}

func TestNVMeBroadcastScope(t *testing.T) {
	for _, test := range []struct {
		name, path string
		argv       []string
		valid      bool
	}{
		{"controller", "/dev/nvme0", []string{"smartctl", "-d", "nvme,0xffffffff", "-j", "-a", "/dev/nvme0"}, true},
		{"namespace", "/dev/nvme0n1", []string{"smartctl", "-d", "nvme,0xffffffff", "-j", "-a", "/dev/nvme0n1"}, false},
		{"unspecified", "/dev/nvme0", []string{"smartctl", "-d", "nvme", "-j", "-a", "/dev/nvme0"}, false},
		{"specific_namespace", "/dev/nvme0", []string{"smartctl", "-d", "nvme,1", "-j", "-a", "/dev/nvme0"}, false},
		{"conflicting_type", "/dev/nvme0", []string{"smartctl", "-d", "nvme,0xffffffff", "--device=nvme,1", "/dev/nvme0"}, false},
		{"duplicate_type", "/dev/nvme0", []string{"smartctl", "-d", "nvme,0xffffffff", "-d", "nvme,0xffffffff", "/dev/nvme0"}, false},
		{"different_device", "/dev/nvme0", []string{"smartctl", "-d", "nvme,0xffffffff", "-j", "-a", "/dev/nvme1"}, false},
		{"missing_argv", "/dev/nvme0", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := sampleJSON()
			d["nvme_number_of_namespaces"] = 132
			d["device"] = map[string]any{"name": test.path, "type": "nvme", "protocol": "NVMe"}
			d["smartctl"].(map[string]any)["argv"] = test.argv
			got := Assess(sampleInput(), marshal(t, d), nil, cutoff)
			if (got.AssessmentStatus == "assessed") != test.valid || got.EntityType != "nvme_controller" {
				t.Fatalf("scope result: %+v", got)
			}
			if !test.valid && !strings.Contains(strings.Join(got.Blockers, ","), "namespace_scope_unverified") {
				t.Fatal("unverified scope must remain blocked")
			}
		})
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func TestExactCounters(t *testing.T) {
	for _, value := range []string{"9007199254740993", "9223372036854775808", "340282366920938463463374607431768211455"} {
		t.Run(value, func(t *testing.T) {
			d := sampleJSON()
			d["nvme_smart_health_information_log"].(map[string]any)["media_errors"] = json.Number(value)
			r := Assess(sampleInput(), marshal(t, d), nil, cutoff)
			for _, e := range r.Evidence {
				if e.Field == "nvme.media_errors" && e.Value == value {
					return
				}
			}
			t.Fatalf("lost exact counter: %+v", r)
		})
	}
	for _, value := range []string{"340282366920938463463374607431768211456", "-1", "1e5", "01x"} {
		if _, err := exactInteger(json.RawMessage(value), 128); err == nil {
			t.Errorf("accepted invalid %s", value)
		}
	}
	d := sampleJSON()
	log := d["nvme_smart_health_information_log"].(map[string]any)
	log["media_errors_s"] = "9007199254740993"
	log["media_errors"] = json.Number("9007199254740993")
	r := Assess(sampleInput(), marshal(t, d), nil, cutoff)
	if r.Level != "attention" {
		t.Fatal(r)
	}
	log["media_errors"] = 1
	r = Assess(sampleInput(), marshal(t, d), nil, cutoff)
	if !contains(r.Blockers, "invalid_or_missing:media_errors") {
		t.Fatal("conflict accepted")
	}
}

func TestInvalidMetadataRemainsSchemaSafe(t *testing.T) {
	in := sampleInput()
	in.SourceKind = "unrecognized"
	code := -1
	in.ExitCode = &code
	r := Assess(in, marshal(t, sampleJSON()), nil, cutoff)
	if r.SourceKind != "" || r.ExitCode != nil || r.AssessmentStatus != "insufficient_data" || !contains(r.Blockers, "execution_exit_out_of_range") {
		t.Fatalf("invalid metadata leaked into report schema: %+v", r)
	}
}

func TestLittleEndianCounters(t *testing.T) {
	for _, test := range []struct {
		raw   string
		valid bool
	}{
		{`{"media_errors":256,"media_errors_s":"256","media_errors_le":[0,1]}`, true},
		{`{"media_errors":256,"media_errors_le":[0,2]}`, false},
		{`{"media_errors":256,"media_errors_le":[256]}`, false},
		{`{"media_errors":256,"media_errors_le":null}`, false},
	} {
		o, err := decodeObject([]byte(test.raw))
		if err != nil {
			t.Fatal(err)
		}
		value, err := integer(o, "media_errors", 128)
		if (err == nil) != test.valid {
			t.Fatalf("%s: %v", test.raw, err)
		}
		if test.valid && value.String() != "256" {
			t.Fatal(value)
		}
	}
}

func TestBaseline(t *testing.T) {
	for _, tc := range []struct {
		name, old, current, trend string
		mutate                    func(*Input)
	}{
		{"new", "9007199254740993", "9007199254740994", "available", func(*Input) {}},
		{"reset", "9", "2", "counter_reset", func(*Input) {}},
		{"reboot", "1", "2", "invalid_baseline", func(in *Input) { in.BootID = "new-boot" }},
		{"replacement", "1", "2", "invalid_baseline", func(in *Input) { in.Generation = "2" }},
		{"future_baseline", "1", "2", "invalid_baseline", func(in *Input) { in.AvailableAt = cutoff.Add(time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := sampleInput()
			old := sampleInput()
			old.ObservedAt = cutoff.Add(-time.Hour)
			old.AvailableAt = old.ObservedAt
			tc.mutate(&old)
			in.Baseline = &old
			d, b := sampleJSON(), sampleJSON()
			d["nvme_smart_health_information_log"].(map[string]any)["media_errors"] = json.Number(tc.current)
			b["nvme_smart_health_information_log"].(map[string]any)["media_errors"] = json.Number(tc.old)
			r := Assess(in, marshal(t, d), marshal(t, b), cutoff)
			if r.TrendStatus != tc.trend {
				t.Fatal(r)
			}
			if tc.trend == "available" && (r.MediaDelta == nil || *r.MediaDelta != "1" || r.Level != "warning") {
				t.Fatal(r)
			}
		})
	}
}

func TestJSONAndIdentity(t *testing.T) {
	for _, raw := range []string{`{"x":1,"x":2}`, `{"x":{"a":1,"a":2}}`, `{"x":1} {}`, `{`, strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34)} {
		if ValidateJSON([]byte(raw)) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	d, in := sampleJSON(), sampleInput()
	a := Assess(in, marshal(t, d), nil, cutoff)
	d["device"].(map[string]any)["name"] = "/dev/nvme9"
	b := Assess(in, marshal(t, d), nil, cutoff)
	if a.EntityID != b.EntityID {
		t.Fatal("path is identity")
	}
	in.Generation = "2"
	c := Assess(in, marshal(t, d), nil, cutoff)
	if c.EntityID == a.EntityID {
		t.Fatal("replacement reused identity")
	}
}

func TestBaselineRejectsInvalidCurrentTimeAndFirmwareChange(t *testing.T) {
	for _, mode := range []string{"current_time", "firmware", "tool", "source_kind"} {
		t.Run(mode, func(t *testing.T) {
			in, old := sampleInput(), sampleInput()
			old.ObservedAt = cutoff.Add(-time.Hour)
			old.AvailableAt = old.ObservedAt
			in.Baseline = &old
			current, baseline := sampleJSON(), sampleJSON()
			current["nvme_smart_health_information_log"].(map[string]any)["media_errors"] = 2
			switch mode {
			case "current_time":
				in.AvailableAt = in.ObservedAt.Add(-time.Second)
			case "firmware":
				baseline["firmware_version"] = "other"
			case "tool":
				baseline["smartctl"].(map[string]any)["version"] = []int{7, 1}
			case "source_kind":
				old.SourceKind = "operator_snapshot"
			}
			r := Assess(in, marshal(t, current), marshal(t, baseline), cutoff)
			if r.TrendStatus != "invalid_baseline" || r.MediaDelta != nil {
				t.Fatal(r)
			}
		})
	}
}

func TestInvalidJSONStillProducesUnknownRecord(t *testing.T) {
	r := Assess(sampleInput(), []byte(`{"smart_status":`), nil, cutoff)
	if r.Level != "unknown" || len(r.EntityID) != 64 || len(r.InputSHA256) != 64 || r.AssessmentStatus != "insufficient_data" {
		t.Fatal(r)
	}
}

func writeInput(t *testing.T, dir string, inputs []Input, raw []byte) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "disk.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for i := range inputs {
		inputs[i].File = "disk.json"
		inputs[i].SHA256 = Digest(raw)
	}
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, marshal(t, InputManifest{Schema: "hardware-health-input-v1", Inputs: inputs}), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBuildAndArtifacts(t *testing.T) {
	dir := t.TempDir()
	path := writeInput(t, dir, []Input{sampleInput()}, marshal(t, sampleJSON()))
	report, err := Build(path, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Assessed != 1 || report.AlertsEmitted || report.ActionsExecuted {
		t.Fatal(report)
	}
	for i := 0; i < 2; i++ {
		out := filepath.Join(dir, fmt.Sprint("out-", i))
		if err := WriteReport(report, path, out); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := os.ReadFile(filepath.Join(dir, "out-0/report.json"))
	b, _ := os.ReadFile(filepath.Join(dir, "out-1/report.json"))
	if string(a) != string(b) {
		t.Fatal("nondeterministic report")
	}
	if err := WriteReport(report, path, filepath.Join(dir, "out-0")); err == nil {
		t.Fatal("overwrote report")
	}
	manifestRaw, _ := os.ReadFile(path)
	_ = os.WriteFile(path, append(manifestRaw, ' '), 0600)
	if err := WriteReport(report, path, filepath.Join(dir, "changed-input")); err == nil {
		t.Fatal("accepted changed input manifest")
	}
	if _, err := os.Stat(filepath.Join(dir, "changed-input")); !os.IsNotExist(err) {
		t.Fatal("failed publication left partial directory")
	}
	_ = os.WriteFile(path, manifestRaw, 0600)
	var meta OutputManifest
	raw, _ := os.ReadFile(filepath.Join(dir, "out-0/manifest.json"))
	if json.Unmarshal(raw, &meta) != nil || meta.ReportSHA256 != Digest(a) || len(meta.ExecutableSHA256) != 64 {
		t.Fatal("invalid fingerprints")
	}
	one, two := sampleInput(), sampleInput()
	two.Reference = "disk-b"
	path = writeInput(t, dir, []Input{one, two}, marshal(t, sampleJSON()))
	report, err = Build(path, cutoff)
	if err != nil || report.Summary.Unknown != 2 {
		t.Fatalf("identity conflict not blocked: %+v %v", report, err)
	}
}

func TestManifestSafety(t *testing.T) {
	for _, mode := range []string{"hash", "path", "symlink", "duplicate_reference", "nested_baseline", "unknown_field"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			in := sampleInput()
			raw := marshal(t, sampleJSON())
			path := writeInput(t, dir, []Input{in}, raw)
			data, _ := os.ReadFile(path)
			var m InputManifest
			_ = json.Unmarshal(data, &m)
			switch mode {
			case "hash":
				m.Inputs[0].SHA256 = strings.Repeat("0", 64)
			case "path":
				m.Inputs[0].File = "../disk.json"
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside.json")
				_ = os.WriteFile(outside, raw, 0600)
				_ = os.Symlink(outside, filepath.Join(dir, "link.json"))
				m.Inputs[0].File = "link.json"
			case "duplicate_reference":
				m.Inputs = append(m.Inputs, m.Inputs[0])
			case "nested_baseline":
				m.Inputs[0].Baseline = &Input{Baseline: &Input{}}
			}
			data = marshal(t, m)
			if mode == "unknown_field" {
				data = append([]byte(`{"unexpected":true,`), data[1:]...)
			}
			_ = os.WriteFile(path, data, 0600)
			if _, err := Build(path, cutoff); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
	if _, err := ReadLimited(filepath.Join(t.TempDir(), "missing"), 10); err == nil {
		t.Fatal("missing file accepted")
	}
	dir := t.TempDir()
	large := filepath.Join(dir, "large")
	_ = os.WriteFile(large, make([]byte, MaxInputBytes+1), 0600)
	if _, err := ReadLimited(large, MaxInputBytes); err == nil {
		t.Fatal("unbounded read")
	}
}

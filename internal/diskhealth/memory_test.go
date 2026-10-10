package diskhealth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func memoryInput() Input { in := sampleInput(); in.Adapter = "linux-edac-sysfs-v1"; return in }
func memorySample() edacSnapshot {
	return edacSnapshot{Schema: "linux-edac-sysfs-snapshot-v1", Controller: "mc0", Driver: "synthetic-driver", KernelRelease: "synthetic-6.12", Files: map[string]string{"mc_name": "synthetic-controller\n", "ce_count": "0\n", "ue_count": "0\n", "ce_noinfo_count": "0\n", "ue_noinfo_count": "0\n", "seconds_since_reset": "1060\n"}}
}

func TestMemoryRawBoundaries(t *testing.T) {
	tests := []struct {
		name, level, blocker string
		change               func(*edacSnapshot, *Input)
	}{
		{"clear_controller", "clear_for_checked_signals", "", func(_ *edacSnapshot, _ *Input) {}},
		{"historical_ce", "attention", "", func(s *edacSnapshot, _ *Input) { s.Files["ce_count"] = "1\n" }},
		{"historical_ue_not_new_failure", "attention", "", func(s *edacSnapshot, _ *Input) { s.Files["ue_count"] = "5\n" }},
		{"exact_above_js", "attention", "", func(s *edacSnapshot, _ *Input) { s.Files["ce_count"] = "9007199254740993\n" }},
		{"missing_ue", "unknown", "invalid_or_missing:ue_count", func(s *edacSnapshot, _ *Input) { delete(s.Files, "ue_count") }},
		{"missing_timer", "unknown", "invalid_or_missing:seconds_since_reset", func(s *edacSnapshot, _ *Input) { delete(s.Files, "seconds_since_reset") }},
		{"negative", "unknown", "invalid_or_missing:ce_count", func(s *edacSnapshot, _ *Input) { s.Files["ce_count"] = "-1\n" }},
		{"fraction", "unknown", "invalid_or_missing:ce_count", func(s *edacSnapshot, _ *Input) { s.Files["ce_count"] = "1.2\n" }},
		{"quoted_number", "unknown", "invalid_or_missing:ce_count", func(s *edacSnapshot, _ *Input) { s.Files["ce_count"] = "\"5\"\n" }},
		{"exponent", "unknown", "invalid_or_missing:ce_count", func(s *edacSnapshot, _ *Input) { s.Files["ce_count"] = "1e2\n" }},
		{"overflow", "unknown", "invalid_or_missing:ce_count", func(s *edacSnapshot, _ *Input) { s.Files["ce_count"] = "18446744073709551616\n" }},
		{"noinfo_conflict", "unknown", "edac_counter_consistency:ce", func(s *edacSnapshot, _ *Input) { s.Files["ce_noinfo_count"] = "2\n" }},
		{"unknown_driver", "unknown", "edac_platform_metadata_missing", func(s *edacSnapshot, _ *Input) { s.Driver = "" }},
		{"unknown_kernel", "unknown", "edac_platform_metadata_missing", func(s *edacSnapshot, _ *Input) { s.KernelRelease = "" }},
		{"bad_controller", "unknown", "edac_controller_invalid", func(s *edacSnapshot, _ *Input) { s.Controller = "../../mc0" }},
		{"schema_future", "unknown", "edac_snapshot_contract_invalid", func(s *edacSnapshot, _ *Input) { s.Schema = "v2" }},
		{"missing_boot", "unknown", "memory_session_identity_missing", func(_ *edacSnapshot, in *Input) { in.BootID = "" }},
		{"missing_node", "unknown", "memory_session_identity_missing", func(_ *edacSnapshot, in *Input) { in.Node = "" }},
		{"blank_node", "unknown", "memory_session_identity_missing", func(_ *edacSnapshot, in *Input) { in.Node = "  " }},
		{"missing_exit", "unknown", "snapshot_execution_unverified", func(_ *edacSnapshot, in *Input) { in.ExitCode = nil }},
		{"failed_snapshot", "unknown", "snapshot_execution_unverified", func(_ *edacSnapshot, in *Input) { code := 1; in.ExitCode = &code }},
		{"truncated", "unknown", "stdout_truncated", func(_ *edacSnapshot, in *Input) { in.Truncated = true }},
		{"stale", "unknown", "stale_evidence", func(_ *edacSnapshot, in *Input) { in.ObservedAt = cutoff.Add(-time.Hour) }},
		{"future", "unknown", "after_cutoff", func(_ *edacSnapshot, in *Input) { in.AvailableAt = cutoff.Add(time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, in := memorySample(), memoryInput()
			test.change(&s, &in)
			r := Assess(in, marshal(t, s), nil, cutoff)
			if r.Level != test.level || test.blocker != "" && !contains(r.Blockers, test.blocker) {
				t.Fatalf("unexpected memory result: %+v", r)
			}
			if r.EntityType != "memory_controller" || r.Memory == nil || r.MediaDelta != nil {
				t.Fatalf("invalid component contract: %+v", r)
			}
		})
	}
}

func TestMemoryBaseline(t *testing.T) {
	tests := []struct {
		name, trend, level string
		change             func(*edacSnapshot, *edacSnapshot, *Input, *Input)
	}{
		{"new_ue", "available", "critical", func(now, old *edacSnapshot, _, _ *Input) {
			old.Files["ue_count"] = "1\n"
			now.Files["ue_count"] = "2\n"
		}},
		{"new_ce_single_interval", "available", "attention", func(now, _ *edacSnapshot, _, _ *Input) { now.Files["ce_count"] = "1\n" }},
		{"no_new_errors", "available", "clear_for_checked_signals", func(_, _ *edacSnapshot, _, _ *Input) {}},
		{"reset_counter", "counter_reset", "unknown", func(now, old *edacSnapshot, _, _ *Input) {
			old.Files["ce_count"] = "2\n"
			now.Files["ce_count"] = "0\n"
		}},
		{"reset_timer", "counter_reset", "unknown", func(now, _ *edacSnapshot, _, _ *Input) { now.Files["seconds_since_reset"] = "10\n" }},
		{"reset_timer_grows_but_epoch_changes", "counter_reset", "unknown", func(now, _ *edacSnapshot, _, _ *Input) { now.Files["seconds_since_reset"] = "1010\n" }},
		{"reboot", "invalid_baseline", "clear_for_checked_signals", func(_, _ *edacSnapshot, in, _ *Input) { in.BootID = "new-boot" }},
		{"node_change", "invalid_baseline", "clear_for_checked_signals", func(_, _ *edacSnapshot, in, _ *Input) { in.Node = "different-node" }},
		{"generation_change", "invalid_baseline", "clear_for_checked_signals", func(_, _ *edacSnapshot, in, _ *Input) { in.Generation = "2" }},
		{"driver_change", "invalid_baseline", "clear_for_checked_signals", func(now, _ *edacSnapshot, _, _ *Input) { now.Driver = "different-driver" }},
		{"kernel_change", "invalid_baseline", "clear_for_checked_signals", func(now, _ *edacSnapshot, _, _ *Input) { now.KernelRelease = "different-kernel" }},
		{"future_baseline", "invalid_baseline", "clear_for_checked_signals", func(_, _ *edacSnapshot, _, old *Input) { old.AvailableAt = cutoff.Add(time.Second) }},
		{"missing_current_time", "invalid_baseline", "unknown", func(_, _ *edacSnapshot, in, _ *Input) { in.ObservedAt = time.Time{} }},
		{"source_change", "invalid_baseline", "clear_for_checked_signals", func(_, _ *edacSnapshot, in, _ *Input) { in.Source = "other-source" }},
		{"noinfo_reset", "counter_reset", "attention", func(now, old *edacSnapshot, _, _ *Input) {
			old.Files["ce_count"] = "4\n"
			now.Files["ce_count"] = "5\n"
			old.Files["ce_noinfo_count"] = "1\n"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now, old := memorySample(), memorySample()
			old.Files["seconds_since_reset"] = "1000\n"
			in, b := memoryInput(), memoryInput()
			b.ObservedAt = in.ObservedAt.Add(-time.Minute)
			b.AvailableAt = b.ObservedAt.Add(time.Second)
			test.change(&now, &old, &in, &b)
			in.Baseline = &b
			r := Assess(in, marshal(t, now), marshal(t, old), cutoff)
			if r.TrendStatus != test.trend || r.Level != test.level {
				t.Fatalf("unexpected baseline: %+v", r)
			}
			if test.trend != "available" && (r.Memory.CEDelta != nil || r.Memory.UEDelta != nil) {
				t.Fatal("invalid delta exposed")
			}
			if test.name == "new_ue" && (*r.Memory.UEDelta != "1" || !containsRule(r, "new_ue_errors")) {
				t.Fatal(r)
			}
		})
	}
}
func containsRule(r Result, name string) bool {
	for _, hit := range r.Hits {
		if hit.Rule == name {
			return true
		}
	}
	return false
}

func TestPublicEDACIsNotSilentlyRepaired(t *testing.T) {
	raw, err := os.ReadFile("testdata/edac-public.json")
	if err != nil {
		t.Fatal(err)
	}
	r := Assess(memoryInput(), raw, nil, cutoff)
	if r.AssessmentStatus != "insufficient_data" || r.Level != "attention" || r.Memory.CEDelta != nil || containsRule(r, "new_ue_errors") {
		t.Fatal(r)
	}
	for _, block := range []string{"edac_platform_metadata_missing", "invalid_or_missing:seconds_since_reset", "edac_counter_consistency:ce", "edac_counter_consistency:ue"} {
		if !contains(r.Blockers, block) {
			t.Fatalf("missing %s", block)
		}
	}
}

func TestMixedComponentReportAndIdentityConflict(t *testing.T) {
	dir := t.TempDir()
	disk, memory := sampleInput(), memoryInput()
	disk.Reference, memory.Reference = "disk", "memory"
	diskRaw, memoryRaw := marshal(t, sampleJSON()), marshal(t, memorySample())
	disk.File, disk.SHA256 = "disk.json", Digest(diskRaw)
	memory.File, memory.SHA256 = "memory.json", Digest(memoryRaw)
	for name, raw := range map[string][]byte{disk.File: diskRaw, memory.File: memoryRaw} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "input.json")
	check := func(inputs []Input) Report {
		t.Helper()
		if err := os.WriteFile(path, marshal(t, InputManifest{Schema: "hardware-health-input-v1", Inputs: inputs}), 0600); err != nil {
			t.Fatal(err)
		}
		r, err := Build(path, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := check([]Input{disk, memory})
	if r.RuleVersion != ReportRuleVersion || r.Summary.Assessed != 2 || r.Rows[1].Memory == nil || r.Rows[0].Memory != nil {
		t.Fatal(r)
	}
	duplicate := memory
	duplicate.Reference = "memory-duplicate"
	r = check([]Input{disk, memory, duplicate})
	if r.Summary.Unknown != 2 || r.Summary.Assessed != 1 {
		t.Fatal(r)
	}
	for _, row := range r.Rows[1:] {
		if row.IdentityStatus != "conflict" || !contains(row.Blockers, "duplicate_or_conflicting_identity") {
			t.Fatal(row)
		}
	}
	memory.Adapter = "unverified-adapter"
	unsupported := Assess(memory, memoryRaw, nil, cutoff)
	if unsupported.Level != "unknown" || !contains(unsupported.Blockers, "adapter_unsupported") {
		t.Fatal(unsupported)
	}
}

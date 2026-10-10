package diskhealth

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
)

func Assess(in Input, raw []byte, baseline []byte, asOf time.Time) Result {
	if in.Adapter == "linux-edac-sysfs-v1" {
		return assessMemory(in, raw, baseline, asOf)
	}
	r := newResult(in, raw)
	if in.Adapter != "" && in.Adapter != "smartctl" {
		r.block("adapter_unsupported")
		r.finish()
		return r
	}
	return assess(&r, in, raw, baseline, asOf)
}

func assess(r *Result, in Input, raw, baseline []byte, asOf time.Time) Result {
	if len(raw) > MaxInputBytes || ValidateJSON(raw) != nil {
		r.block("invalid_json_or_input_limit")
		r.finish()
		return *r
	}
	o, err := decodeObject(raw)
	if err != nil {
		r.block("invalid_object")
		r.finish()
		return *r
	}
	r.Protocol = o.child("device").text("protocol")
	r.Device = o.child("device").text("name")
	r.Serial, r.Model, r.Firmware = o.text("serial_number"), o.text("model_name"), o.text("firmware_version")
	r.EntityType = "disk"
	if r.Protocol == "NVMe" {
		r.EntityType = "nvme_controller"
	}
	if r.Serial != "" && in.Scope != "" && in.Generation != "" {
		identity, _ := json.Marshal([]string{in.Scope, in.Generation, r.EntityType, r.Serial})
		r.EntityID, r.IdentityStatus = Digest(identity), "bound"
	} else {
		r.EntityID = Digest([]byte(in.Reference + ":" + r.InputSHA256))
		r.block("identity_missing")
	}
	for _, value := range []string{r.Model, r.Firmware, in.Source} {
		if strings.TrimSpace(value) == "" {
			r.block("model_firmware_or_source_missing")
		}
	}
	if in.SourceKind != "fixture" && in.SourceKind != "operator_snapshot" {
		r.block("source_kind_unknown")
	}
	if in.Truncated {
		r.block("stdout_truncated")
		r.finish()
		return *r
	}
	if asOf.IsZero() || in.ObservedAt.IsZero() || in.AvailableAt.IsZero() || in.AvailableAt.Before(in.ObservedAt) {
		r.block("time_missing_or_invalid")
	} else if in.ObservedAt.After(asOf) || in.AvailableAt.After(asOf) {
		r.Freshness = "future"
		r.block("after_cutoff")
		r.finish()
		return *r
	} else if in.MaxAgeSeconds <= 0 || in.MaxAgeSeconds > 86400 {
		r.block("freshness_budget_missing_or_invalid")
	} else if asOf.Sub(in.ObservedAt) > time.Duration(in.MaxAgeSeconds)*time.Second {
		r.Freshness = "stale"
		r.block("stale_evidence")
	} else {
		r.Freshness = "fresh_at_cutoff"
	}
	var format []int
	if json.Unmarshal(o["json_format_version"], &format) != nil || len(format) != 2 || format[0] != 1 || format[1] != 0 {
		r.block("unsupported_json_format")
		r.finish()
		return *r
	}
	tool := o.child("smartctl")
	var version []int
	if json.Unmarshal(tool["version"], &version) != nil || len(version) != 2 || version[0] != 7 || version[1] < 0 || version[1] > 5 {
		r.block("unsupported_tool_version")
	} else {
		r.ToolVersion = fmt.Sprintf("smartctl %d.%d", version[0], version[1])
	}
	exit, e := integer(tool, "exit_status", 8)
	if e != nil {
		r.block("exit_status_missing_or_invalid")
	} else {
		code := int(exit.Int64())
		r.evidence("smartctl.exit_status", exit.String(), "bitmask")
		if in.ExitCode == nil {
			r.block("execution_exit_missing")
		} else if *in.ExitCode != code {
			r.block("exit_status_conflict")
			r.finish()
			return *r
		}
		if code&3 != 0 {
			r.block("command_or_device_failure")
			r.finish()
			return *r
		}
		if code&4 != 0 {
			r.block("partial_smart_command_failure")
		}
	}
	var passed bool
	status := o.child("smart_status")
	if err := json.Unmarshal(status["passed"], &passed); err != nil || string(status["passed"]) == "null" {
		r.block("smart_status_missing_or_invalid")
	} else if !passed {
		id := r.evidence("smart_status.passed", "false", "boolean")
		r.hit("manufacturer_smart_failure", "critical", id)
	}
	if r.Protocol != "NVMe" {
		r.block("protocol_or_model_rules_unsupported")
		r.finish()
		return *r
	}
	r.Support = "fixture_verified"
	namespaces, namespaceErr := integer(o, "nvme_number_of_namespaces", 32)
	if namespaceErr != nil || namespaces.Int64() != 1 {
		r.block("namespace_scope_unverified")
	}
	log := o.child("nvme_smart_health_information_log")
	values := map[string]*big.Int{}
	ids := map[string]string{}
	for _, field := range []string{"critical_warning", "available_spare", "available_spare_threshold", "percentage_used", "media_errors", "num_err_log_entries", "unsafe_shutdowns", "temperature"} {
		bits, unit := 128, "count"
		if field == "critical_warning" {
			bits, unit = 8, "bitmask"
		}
		if field == "available_spare" || field == "available_spare_threshold" || field == "percentage_used" {
			bits, unit = 8, "percent"
		}
		if field == "temperature" {
			bits, unit = 16, "celsius"
		}
		value, err := integer(log, field, bits)
		if err != nil {
			r.block("invalid_or_missing:" + field)
			continue
		}
		if (field == "available_spare" || field == "available_spare_threshold") && value.Int64() > 100 {
			r.block("invalid_range:" + field)
			continue
		}
		values[field] = value
		ids[field] = r.evidence("nvme."+field, value.String(), unit)
	}
	if warning := values["critical_warning"]; warning != nil {
		mask := warning.Int64()
		for bit, rule := range []string{"spare_below_threshold", "temperature_threshold", "reliability_degraded", "media_read_only", "volatile_backup_failed", "pmr_unreliable"} {
			if mask&(1<<bit) == 0 {
				continue
			}
			if bit == 4 && (in.VolatileBackup == nil || !*in.VolatileBackup) || bit == 5 && (in.PMR == nil || !*in.PMR) {
				r.block("warning_capability_unverified")
				continue
			}
			level := "warning"
			if bit >= 2 {
				level = "critical"
			}
			r.hit("nvme_"+rule, level, ids["critical_warning"])
		}
		if mask&192 != 0 {
			r.block("unknown_warning_bits")
		}
		if passed && mask&12 != 0 {
			r.block("smart_status_warning_conflict")
		}
	}
	if spare, threshold := values["available_spare"], values["available_spare_threshold"]; spare != nil && threshold != nil && spare.Cmp(threshold) < 0 {
		r.hit("nvme_spare_below_threshold", "warning", ids["available_spare"])
	}
	if used := values["percentage_used"]; used != nil && used.Int64() >= 100 {
		r.hit("nvme_endurance_consumed", "warning", ids["percentage_used"])
	}
	if media := values["media_errors"]; media != nil && media.Sign() > 0 {
		r.hit("historical_media_errors", "attention", ids["media_errors"])
	}
	if in.Baseline != nil && len(baseline) > 0 {
		applyBaseline(r, in, baseline, values["media_errors"], asOf)
	}
	r.finish()
	return *r
}

func applyBaseline(r *Result, in Input, raw []byte, media *big.Int, asOf time.Time) {
	b := in.Baseline
	if in.ObservedAt.IsZero() || in.AvailableAt.IsZero() || in.AvailableAt.Before(in.ObservedAt) || in.ObservedAt.After(asOf) || in.AvailableAt.After(asOf) || r.IdentityStatus != "bound" || b.Baseline != nil || b.Scope != in.Scope || b.Generation != in.Generation || b.BootID == "" || b.BootID != in.BootID || b.Source != in.Source || !b.ObservedAt.Before(in.ObservedAt) || b.AvailableAt.After(asOf) {
		r.TrendStatus = "invalid_baseline"
		return
	}
	previous := Assess(*b, raw, nil, asOf)
	r.BaselineSHA256 = previous.InputSHA256
	// An old baseline may be stale at cutoff; other blockers invalidate deltas.
	for _, blocker := range previous.Blockers {
		if blocker != "stale_evidence" {
			r.TrendStatus = "invalid_baseline"
			return
		}
	}
	if previous.IdentityStatus != "bound" || previous.EntityID != r.EntityID || previous.Model != r.Model || previous.Firmware != r.Firmware || previous.ToolVersion != r.ToolVersion || b.SourceKind != in.SourceKind || b.Adapter != in.Adapter || media == nil {
		r.TrendStatus = "invalid_baseline"
		return
	}
	for _, evidence := range previous.Evidence {
		if evidence.Field != "nvme.media_errors" {
			continue
		}
		old, ok := new(big.Int).SetString(evidence.Value, 10)
		if !ok || media.Cmp(old) < 0 {
			r.TrendStatus = "counter_reset"
			return
		}
		delta := new(big.Int).Sub(media, old).String()
		r.MediaDelta, r.TrendStatus = &delta, "available"
		id := r.evidence("nvme.media_errors_delta", delta, "count")
		if delta != "0" {
			r.hit("new_media_errors", "warning", id)
		}
		return
	}
	r.TrendStatus = "invalid_baseline"
}

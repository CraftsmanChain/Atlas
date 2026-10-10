package diskhealth

import (
	"bytes"
	"encoding/json"
	"math/big"
	"regexp"
	"strings"
	"time"
)

// Files retain EDAC sysfs contents verbatim; metadata is declared by the snapshot provider.
type edacSnapshot struct {
	Schema        string            `json:"schema"`
	Controller    string            `json:"controller"`
	Driver        string            `json:"driver"`
	KernelRelease string            `json:"kernel_release"`
	Files         map[string]string `json:"files"`
}

var controllerPattern = regexp.MustCompile(`^mc[0-9]{1,4}$`)
var sysfsCounterPattern = regexp.MustCompile(`^[0-9]+$`)
var memoryCounters = []string{"ce_count", "ue_count", "ce_noinfo_count", "ue_noinfo_count", "seconds_since_reset"}

func assessMemory(in Input, raw, baseline []byte, asOf time.Time) Result {
	r := newResult(in, raw)
	r.EntityType, r.Protocol, r.RuleVersion = "memory_controller", "EDAC", MemoryRuleVersion
	r.Memory = &MemoryDetails{LocationPrecision: "controller"}
	done := func() Result { r.finish(); return r }
	if len(raw) > MaxInputBytes || ValidateJSON(raw) != nil {
		r.block("invalid_json_or_input_limit")
		return done()
	}
	var s edacSnapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&s) != nil || s.Schema != "linux-edac-sysfs-snapshot-v1" || len(s.Files) > 64 {
		r.block("edac_snapshot_contract_invalid")
		return done()
	}
	r.Memory.Controller, r.Memory.Driver, r.Memory.KernelRelease = s.Controller, s.Driver, s.KernelRelease
	r.Model, r.ToolVersion = strings.TrimSpace(s.Files["mc_name"]), "Linux EDAC sysfs v1"
	r.Device = "/sys/devices/system/edac/mc/" + s.Controller
	if !controllerPattern.MatchString(s.Controller) {
		r.block("edac_controller_invalid")
		return done()
	}
	if strings.TrimSpace(in.Scope) != "" && strings.TrimSpace(in.Node) != "" && strings.TrimSpace(in.Generation) != "" && strings.TrimSpace(in.BootID) != "" {
		identity, _ := json.Marshal([]string{in.Scope, in.Node, in.Generation, in.BootID, r.EntityType, s.Controller})
		r.EntityID, r.IdentityStatus = Digest(identity), "bound"
	} else {
		r.block("memory_session_identity_missing")
	}
	if strings.TrimSpace(in.Source) == "" || r.Model == "" || strings.TrimSpace(s.Driver) == "" || strings.TrimSpace(s.KernelRelease) == "" {
		r.block("edac_platform_metadata_missing")
	}
	if in.Truncated {
		r.block("stdout_truncated")
		return done()
	}
	if in.ExitCode == nil || *in.ExitCode != 0 {
		r.block("snapshot_execution_unverified")
	}
	if asOf.IsZero() || in.ObservedAt.IsZero() || in.AvailableAt.IsZero() || in.AvailableAt.Before(in.ObservedAt) {
		r.block("time_missing_or_invalid")
	} else if in.ObservedAt.After(asOf) || in.AvailableAt.After(asOf) {
		r.Freshness = "future"
		r.block("after_cutoff")
		return done()
	} else if in.MaxAgeSeconds <= 0 || in.MaxAgeSeconds > 86400 {
		r.block("freshness_budget_missing_or_invalid")
	} else if asOf.Sub(in.ObservedAt) > time.Duration(in.MaxAgeSeconds)*time.Second {
		r.Freshness = "stale"
		r.block("stale_evidence")
	} else {
		r.Freshness = "fresh_at_cutoff"
	}
	r.Support = "fixture_verified"
	values := map[string]*big.Int{}
	for _, field := range memoryCounters {
		content, present := s.Files[field]
		decimal := strings.TrimSpace(content)
		v, err := exactInteger(json.RawMessage(decimal), 64)
		if !present || !sysfsCounterPattern.MatchString(decimal) || err != nil {
			r.block("invalid_or_missing:" + field)
			continue
		}
		values[field] = v
		unit := "count"
		if field == "seconds_since_reset" {
			unit = "seconds"
		}
		r.evidence("edac."+field, v.String(), unit)
	}
	for _, family := range []string{"ce", "ue"} {
		total, noinfo := values[family+"_count"], values[family+"_noinfo_count"]
		if total != nil && noinfo != nil && noinfo.Cmp(total) > 0 {
			r.block("edac_counter_consistency:" + family)
		}
		if total != nil && total.Sign() > 0 {
			for _, e := range r.Evidence {
				if e.Field == "edac."+family+"_count" {
					r.hit("historical_"+family+"_errors", "attention", e.ID)
				}
			}
		}
	}
	if in.Baseline != nil && len(baseline) > 0 {
		applyMemoryBaseline(&r, in, baseline, values, asOf)
	}
	return done()
}

func applyMemoryBaseline(r *Result, in Input, raw []byte, current map[string]*big.Int, asOf time.Time) {
	r.BaselineSHA256 = Digest(raw)
	r.TrendStatus = "invalid_baseline"
	b := in.Baseline
	// Blockers prevent a partial, unverified or contradictory current snapshot from making a delta.
	for _, block := range r.Blockers {
		if block != "stale_evidence" {
			return
		}
	}
	if b.Baseline != nil || b.Adapter != in.Adapter || b.Source != in.Source || b.SourceKind != in.SourceKind || b.BootID != in.BootID || b.Scope != in.Scope || b.Node != in.Node || b.Generation != in.Generation || !b.ObservedAt.Before(in.ObservedAt) || b.AvailableAt.After(asOf) {
		return
	}
	previous := assessMemory(*b, raw, nil, asOf)
	for _, block := range previous.Blockers {
		if block != "stale_evidence" {
			return
		}
	}
	if previous.IdentityStatus != "bound" || previous.EntityID != r.EntityID || previous.Model != r.Model || previous.Memory.Driver != r.Memory.Driver || previous.Memory.KernelRelease != r.Memory.KernelRelease {
		return
	}
	old := map[string]*big.Int{}
	for _, e := range previous.Evidence {
		if strings.HasPrefix(e.Field, "edac.") {
			old[strings.TrimPrefix(e.Field, "edac.")], _ = new(big.Int).SetString(e.Value, 10)
		}
	}
	for _, field := range memoryCounters {
		if current[field] == nil || old[field] == nil {
			return
		}
		if current[field].Cmp(old[field]) < 0 {
			r.TrendStatus = "counter_reset"
			r.block("edac_counter_reset")
			return
		}
	}
	elapsed := in.ObservedAt.Sub(b.ObservedAt)
	if elapsed > 24*time.Hour {
		return
	}
	// Reset timer must advance with wall time; five seconds covers separately read sysfs attributes.
	timer := new(big.Int).Sub(current["seconds_since_reset"], old["seconds_since_reset"])
	drift := new(big.Int).Sub(timer, big.NewInt(int64(elapsed/time.Second)))
	if drift.Abs(drift).Cmp(big.NewInt(5)) > 0 {
		r.TrendStatus = "counter_reset"
		r.block("edac_reset_epoch_changed")
		return
	}
	ce := new(big.Int).Sub(current["ce_count"], old["ce_count"]).String()
	ue := new(big.Int).Sub(current["ue_count"], old["ue_count"]).String()
	r.Memory.CEDelta, r.Memory.UEDelta, r.TrendStatus = &ce, &ue, "available"
	ceID, ueID := r.evidence("edac.ce_delta", ce, "count"), r.evidence("edac.ue_delta", ue, "count")
	if ce != "0" {
		r.hit("new_ce_errors", "attention", ceID)
	}
	if ue != "0" {
		r.hit("new_ue_errors", "critical", ueID)
	}
}

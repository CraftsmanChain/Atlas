package diskhealth

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
)

// These profiles use decoded smartctl fields, not vendor packed raw values.
// Add a profile only with a named raw fixture and explicit field semantics.
func ataProfile(o object) (string, string) {
	rotation, err := integer(o, "rotation_rate", 16)
	if err != nil {
		return "unknown", ""
	}
	model := o.text("model_name")
	if rotation.Sign() > 0 {
		if model == "WDC WD140EDFZ-11A0VA0" && o.text("firmware_version") == "81.00A81" {
			return "HDD", "ata-wdc-hdd-v1"
		}
		return "HDD", ""
	}
	if model == "Samsung SSD 860 EVO 500GB" && o.text("firmware_version") == "RVT02B6Q" && o.text("model_family") == "Samsung based SSDs" {
		return "SSD", "ata-samsung-860-evo-v1"
	}
	return "SSD", ""
}

func assessATA(r *Result, in Input, o object, baseline []byte, asOf time.Time) {
	media, profile := ataProfile(o)
	r.Disk = newDiskDetails(media, profile)
	attrs := readATAAttributes(r, o)
	if profile == "" {
		r.block("protocol_or_model_rules_unsupported")
		return
	}
	r.Support = "fixture_verified"
	if temperature, err := integer(o.child("temperature"), "current", 16); err == nil && temperature.Int64() <= 200 {
		value := temperature.String()
		r.Disk.Temperature = &value
		r.evidence("ata.temperature", value, "celsius")
	} else {
		r.block("ata_temperature_missing_or_invalid")
	}
	names := map[int][]string{
		5:   {"Reallocated_Sector_Ct"},
		197: {"Current_Pending_Sector"}, 198: {"Offline_Uncorrectable"},
		199: {"UDMA_CRC_Error_Count", "CRC_Error_Count"},
		187: {"Uncorrectable_Error_Cnt"},
	}
	required := []int{5, 197, 198, 199}
	if media == "SSD" {
		required = []int{5, 187, 199}
	}
	values, ids := map[int]*big.Int{}, map[int]string{}
	for _, id := range required {
		attr := attrs[id]
		nameOK := false
		for _, name := range names[id] {
			nameOK = nameOK || attr.text("name") == name
		}
		value, err := integer(attr.child("raw"), "value", 64)
		// Even a matching attribute ID can contain an encoded tuple. Require the
		// printable representation to agree before treating it as a counter.
		printedRaw, _ := json.Marshal(attr.child("raw").text("string"))
		printed, printErr := exactInteger(printedRaw, 64)
		if !nameOK || err != nil || printErr != nil || printed.Cmp(value) != 0 {
			r.block(fmt.Sprintf("ata_raw_encoding_unverified:%d", id))
			continue
		}
		values[id] = value
		unit := "count"
		if media == "HDD" && id != 199 {
			unit = "sectors"
		}
		ids[id] = r.evidence(fmt.Sprintf("ata.%d.raw", id), value.String(), unit)
	}
	if value := values[5]; value != nil && value.Sign() > 0 {
		r.hit("ata_historical_reallocated", "attention", ids[5])
	}
	if media == "HDD" {
		for _, id := range []int{197, 198} {
			if value := values[id]; value != nil && value.Sign() > 0 {
				r.hit(map[int]string{197: "ata_pending_sectors", 198: "ata_uncorrectable_sectors"}[id], "warning", ids[id])
			}
		}
	} else {
		if value := values[187]; value != nil && value.Sign() > 0 {
			r.hit("historical_media_errors", "attention", ids[187])
		}
		attr := attrs[177]
		if value, err := integer(attr, "value", 8); err == nil && attr.text("name") == "Wear_Leveling_Count" {
			id := r.evidence("ata.177.normalized", value.String(), "normalized")
			r.Disk.Wear = &WearIndicator{Field: "ata.177.normalized", Value: value.String(), Unit: "normalized", Kind: "normalized_index", EvidenceID: id}
		} else {
			r.block("ata_wear_indicator_missing_or_invalid")
		}
	}
	if value := values[199]; value != nil && value.Sign() > 0 {
		r.hit("ata_historical_crc_errors", "attention", ids[199])
	}
	if in.Baseline != nil && len(baseline) > 0 {
		applyATABaseline(r, in, baseline, values, asOf)
	}
}

func readATAAttributes(r *Result, o object) map[int]object {
	attrs := map[int]object{}
	seen := map[int]bool{}
	var table []json.RawMessage
	if json.Unmarshal(o.child("ata_smart_attributes")["table"], &table) != nil || len(table) == 0 || len(table) > 255 {
		r.block("ata_attributes_missing_or_invalid")
		return attrs
	}
	currentFailure, pastFailure, oldAgeFailure := []string{}, []string{}, []string{}
	for _, raw := range table {
		attr, err := decodeObject(raw)
		if err != nil {
			r.block("ata_attribute_invalid")
			continue
		}
		id, err := integer(attr, "id", 8)
		if err != nil || id.Sign() == 0 {
			r.block("ata_attribute_id_invalid")
			continue
		}
		number := int(id.Int64())
		if seen[number] {
			r.block("ata_attribute_id_duplicate")
			delete(attrs, number)
			continue
		}
		seen[number] = true
		attrs[number] = attr
	}
	ordered := make([]int, 0, len(attrs))
	for id := range attrs {
		ordered = append(ordered, id)
	}
	sort.Ints(ordered)
	for _, number := range ordered {
		attr := attrs[number]
		id := big.NewInt(int64(number))
		value, valueErr := integer(attr, "value", 8)
		threshold, thresholdErr := integer(attr, "thresh", 8)
		var when string
		whenErr := json.Unmarshal(attr["when_failed"], &when)
		if valueErr != nil || thresholdErr != nil || whenErr != nil || string(attr["when_failed"]) == "null" || (when != "" && when != "now" && when != "past") {
			r.block("ata_threshold_metadata_invalid")
			continue
		}
		failedNow := threshold.Sign() > 0 && value.Cmp(threshold) <= 0
		if when == "now" && !failedNow || failedNow && when != "now" {
			r.block("ata_threshold_status_conflict")
		}
		if failedNow || when == "now" {
			flags, flagsErr := integer(attr.child("flags"), "value", 16)
			if flagsErr != nil {
				r.block("ata_attribute_flags_missing_or_invalid")
				oldAgeFailure = append(oldAgeFailure, id.String())
			} else if flags.Bit(0) == 1 {
				currentFailure = append(currentFailure, id.String())
			} else {
				oldAgeFailure = append(oldAgeFailure, id.String())
			}
		} else if when == "past" {
			pastFailure = append(pastFailure, id.String())
		}
	}
	for _, group := range []struct {
		ids         []string
		rule, level string
	}{
		{currentFailure, "ata_current_prefailure_threshold", "critical"},
		{oldAgeFailure, "ata_current_usage_threshold", "warning"},
		{pastFailure, "ata_past_threshold_failure", "attention"},
	} {
		if len(group.ids) > 0 {
			value := strings.Join(group.ids, ",")
			if len(value) > 512 {
				value = value[:512]
				r.block("ata_threshold_evidence_limit")
			}
			id := r.evidence("ata."+group.rule+".ids", value, "attribute_ids")
			r.hit(group.rule, group.level, id)
		}
	}
	return attrs
}

func applyATABaseline(r *Result, in Input, raw []byte, values map[int]*big.Int, asOf time.Time) {
	b := in.Baseline
	if len(r.Blockers) > 0 || b.Baseline != nil || b.Scope != in.Scope || b.Generation != in.Generation || b.Source != in.Source || b.SourceKind != in.SourceKind || b.Adapter != in.Adapter || b.BootID == "" || b.BootID != in.BootID || !b.ObservedAt.Before(in.ObservedAt) || b.AvailableAt.After(asOf) {
		r.TrendStatus = "invalid_baseline"
		return
	}
	old := Assess(*b, raw, nil, asOf)
	r.BaselineSHA256 = old.InputSHA256
	for _, blocker := range old.Blockers {
		if blocker != "stale_evidence" {
			r.TrendStatus = "invalid_baseline"
			return
		}
	}
	if old.Disk == nil || old.IdentityStatus != "bound" || r.IdentityStatus != "bound" || old.EntityID != r.EntityID || old.Model != r.Model || old.Firmware != r.Firmware || old.ToolVersion != r.ToolVersion || old.Disk.Profile != r.Disk.Profile {
		r.TrendStatus = "invalid_baseline"
		return
	}
	previous := map[string]string{}
	for _, evidence := range old.Evidence {
		previous[evidence.Field] = evidence.Value
	}
	ids := []int{5, 199}
	if r.Disk.MediaType == "SSD" {
		ids = append(ids, 187)
	}
	var deltas []CounterDelta
	for _, attr := range ids {
		field := fmt.Sprintf("ata.%d.raw", attr)
		before, ok := new(big.Int).SetString(previous[field], 10)
		current := values[attr]
		if !ok || current == nil {
			r.TrendStatus = "invalid_baseline"
			return
		}
		if current.Cmp(before) < 0 {
			r.TrendStatus = "counter_reset"
			return
		}
		unit := "count"
		if attr == 5 && r.Disk.MediaType == "HDD" {
			unit = "sectors"
		}
		deltas = append(deltas, CounterDelta{Field: field, Value: new(big.Int).Sub(current, before).String(), Unit: unit})
	}
	for i := range deltas {
		delta := &deltas[i]
		delta.EvidenceID = r.evidence(delta.Field+"_delta", delta.Value, delta.Unit)
		if delta.Value != "0" {
			rule := "ata_new_reallocated"
			if delta.Field == "ata.199.raw" {
				rule = "ata_new_crc_errors"
			}
			if delta.Field == "ata.187.raw" {
				rule = "new_media_errors"
			}
			r.hit(rule, "warning", delta.EvidenceID)
		}
	}
	r.Disk.Deltas, r.TrendStatus = deltas, "available"
}

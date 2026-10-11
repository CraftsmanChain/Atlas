package diskhealth

import "strings"

func newDiskDetails(media, profile string) *DiskDetails {
	return &DiskDetails{MediaType: media, Profile: profile, Dimensions: []DiskDimension{}, Deltas: []CounterDelta{}}
}

func dimensionForRule(rule string) string {
	switch {
	case strings.Contains(rule, "temperature"):
		return "temperature"
	case strings.Contains(rule, "endurance"), strings.Contains(rule, "spare"), strings.Contains(rule, "wear"):
		return "endurance"
	case strings.Contains(rule, "crc"), strings.Contains(rule, "shutdown"), strings.Contains(rule, "backup"), strings.Contains(rule, "pmr"):
		return "path_power"
	default:
		return "media"
	}
}

func (r *Result) finishDiskDimensions() {
	if r.Disk == nil {
		return
	}
	r.Disk.Dimensions = []DiskDimension{}
	for _, id := range []string{"media", "endurance", "temperature", "path_power"} {
		d := DiskDimension{ID: id, Level: "clear_for_checked_signals", State: "checked"}
		if r.AssessmentStatus != "assessed" {
			d.Level, d.State = "unknown", "insufficient_data"
		}
		if id == "endurance" && r.Disk.MediaType == "HDD" {
			d.Level, d.State = "unknown", "not_applicable"
		}
		if id == "endurance" && r.Protocol == "ATA" && r.Disk.MediaType != "HDD" {
			d.Level, d.State = "unknown", "insufficient_data"
		}
		// ATA temperature is a reading, not a checked model-specific alarm range.
		if id == "temperature" && r.Protocol == "ATA" {
			d.Level, d.State = "unknown", "insufficient_data"
		}
		for _, hit := range r.Hits {
			if dimensionForRule(hit.Rule) == id && Severity(hit.Level) > Severity(d.Level) {
				d.Level = hit.Level
			}
		}
		r.Disk.Dimensions = append(r.Disk.Dimensions, d)
	}
}

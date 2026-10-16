package diskhealth

import (
	"encoding/json"
	"math/big"
	"reflect"
	"testing"
	"time"
)

func trendTestRows(t *testing.T, end time.Time, count int) []SnapshotView {
	t.Helper()
	rows := make([]SnapshotView, 0, count)
	base, _ := new(big.Int).SetString("340282366920938463463374607431768210000", 10)
	for i := 0; i < count; i++ {
		observed := end.Add(-time.Duration(count-1-i) * 30 * time.Minute)
		in, data := sampleInput(), sampleJSON()
		in.ObservedAt, in.AvailableAt = observed, observed.Add(time.Second)
		log := data["nvme_smart_health_information_log"].(map[string]any)
		log["media_errors"] = json.Number(new(big.Int).Add(base, big.NewInt(int64(i))).String())
		log["temperature"] = 30 + i%3
		row := Assess(in, marshal(t, data), nil, observed.Add(time.Minute))
		if row.AssessmentStatus != "assessed" {
			t.Fatalf("invalid synthetic trend input: %+v", row)
		}
		rows = append(rows, SnapshotView{SnapshotID: uint(i + 1), Result: row})
	}
	return rows
}

func trendFeature(t *testing.T, window TrendWindow, field, kind string) WindowFeature {
	t.Helper()
	for _, feature := range window.Features {
		if feature.Field == field && feature.Kind == kind {
			return feature
		}
	}
	t.Fatalf("feature %s/%s missing: %+v", field, kind, window)
	return WindowFeature{}
}

func setTrendEvidence(row *SnapshotView, field, value string) {
	for i := range row.Evidence {
		if row.Evidence[i].Field == field {
			row.Evidence[i].Value = value
			return
		}
	}
}

func cloneTrendRow(t *testing.T, row SnapshotView) SnapshotView {
	t.Helper()
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var clone SnapshotView
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestTrendCompleteWindowsHaveIndependentExactFeatures(t *testing.T) {
	rows := trendTestRows(t, cutoff, 337)
	windows := trendWindows(rows, cutoff.Add(time.Minute))
	for i, expected := range []struct {
		count int
		delta string
	}{{3, "2"}, {49, "48"}, {337, "336"}} {
		window := windows[i]
		if window.ContractVersion != TrendContractVersion || window.CoverageStatus != "complete" || window.SampleCount != expected.count || window.ExpectedSampleCount != expected.count || window.OccupiedSampleCount != expected.count || window.CoverageRatio != 1 || len(window.Blockers) != 0 {
			t.Fatalf("unexpected complete window: %+v", window)
		}
		feature := trendFeature(t, window, "nvme.media_errors", "counter_delta")
		if feature.Status != "available" || feature.Value == nil || *feature.Value != expected.delta {
			t.Fatalf("window delta was rounded or copied from latest pair: %+v", feature)
		}
		if window.CoveredSeconds != int64(time.Duration(expected.count-1)*30*time.Minute/time.Second) {
			t.Fatal("covered duration did not match nominal span")
		}
	}
	// The exact unsigned128 pair differs by one, above float64/int64 precision.
	short := trendWindows(rows[len(rows)-2:], cutoff.Add(time.Minute))[0]
	if feature := trendFeature(t, short, "nvme.media_errors", "counter_delta"); feature.Value == nil || *feature.Value != "1" || feature.Status != "partial" {
		t.Fatalf("precise partial pair lost: %+v", feature)
	}
}

func TestTrendTwoPointsDoNotCompleteLongWindows(t *testing.T) {
	rows := trendTestRows(t, cutoff, 3)
	rows = []SnapshotView{rows[0], rows[2]}
	for _, window := range trendWindows(rows, cutoff.Add(time.Minute)) {
		if window.SampleCount != 2 || window.CoverageStatus != "partial" || !contains(window.Blockers, "insufficient_slot_coverage") {
			t.Fatalf("two points became a complete %s window: %+v", window.Window, window)
		}
	}
	for _, count := range []int{0, 1} {
		for _, window := range trendWindows(rows[:count], cutoff.Add(time.Minute)) {
			if window.CoverageStatus != "collecting" || window.SampleCount != count || window.Features == nil || window.Blockers == nil || window.LatestCounterDeltas == nil {
				t.Fatalf("empty/single window contract: %+v", window)
			}
		}
	}
}

func TestTrendSlotCoverageCannotBeInflatedByDenseOrDuplicateSamples(t *testing.T) {
	rows := trendTestRows(t, cutoff, 2)
	for i := 0; i < 100; i++ {
		row := cloneTrendRow(t, rows[1])
		row.ObservedAt = cutoff.Add(-time.Duration(i+1) * time.Second)
		row.AvailableAt = row.ObservedAt.Add(time.Second)
		rows = append(rows, row)
	}
	window := trendWindows(rows, cutoff.Add(time.Minute))[1]
	if window.SampleCount != 102 || window.OccupiedSampleCount > 3 || window.CoverageStatus != "partial" {
		t.Fatalf("dense samples filled missing nominal slots: %+v", window)
	}
	rows = trendTestRows(t, cutoff, 3)
	duplicate := cloneTrendRow(t, rows[1])
	duplicate.AvailableAt = duplicate.AvailableAt.Add(time.Second)
	rows = append(rows, duplicate, duplicate)
	window = trendWindows(rows, cutoff.Add(time.Minute))[0]
	if window.SampleCount != 3 || window.DuplicateObservationCount != 2 || window.CoverageStatus != "complete" {
		t.Fatalf("same observation was counted more than once: %+v", window)
	}
	conflict := cloneTrendRow(t, duplicate)
	setTrendEvidence(&conflict, "nvme.media_errors", "9")
	rows = append(rows, conflict)
	window = trendWindows(rows, cutoff.Add(time.Minute))[0]
	if window.SampleCount != 3 || window.CoverageStatus != "partial" || !contains(window.Blockers, "conflicting_observation") {
		t.Fatalf("timestamp conflict was silently selected: %+v", window)
	}
	if feature := trendFeature(t, window, "nvme.media_errors", "counter_delta"); feature.Status != "blocked" || feature.Value != nil {
		t.Fatal("conflicting observation emitted a delta")
	}
}

func TestTrendPointInTimeAndLatestFailedObservation(t *testing.T) {
	for _, mode := range []string{"observed", "available"} {
		rows := trendTestRows(t, cutoff, 3)
		future := cloneTrendRow(t, rows[2])
		if mode == "observed" {
			future.ObservedAt, future.AvailableAt = cutoff.Add(2*time.Minute), cutoff.Add(3*time.Minute)
		} else {
			future.ObservedAt, future.AvailableAt = cutoff.Add(time.Second), cutoff.Add(2*time.Minute)
		}
		rows = append(rows, future)
		window := trendWindows(rows, cutoff.Add(time.Minute))[0]
		if window.ExcludedFutureCount != 1 || window.CoverageStatus != "complete" || !window.WindowEndAt.Equal(cutoff) {
			t.Fatalf("future %s entered historical window: %+v", mode, window)
		}
	}
	rows := trendTestRows(t, cutoff, 3)
	rows[2].AssessmentStatus, rows[2].Blockers = "insufficient_data", []string{"partial_smart_command_failure"}
	window := trendWindows(rows, cutoff.Add(time.Minute))[0]
	if !window.WindowEndAt.Equal(cutoff) || window.CoverageStatus != "partial" || !contains(window.Blockers, "quality:partial_smart_command_failure") {
		t.Fatalf("failed latest observation was discarded: %+v", window)
	}
	if feature := trendFeature(t, window, "nvme.media_errors", "counter_delta"); feature.Status != "blocked" || feature.Value != nil {
		t.Fatal("failed evidence produced available feature")
	}
}

func TestTrendInvalidTimesAreExplicitAndBounded(t *testing.T) {
	for _, mode := range []string{"inside", "outside", "unlocatable"} {
		rows := trendTestRows(t, cutoff, 3)
		invalid := cloneTrendRow(t, rows[0])
		invalid.AvailableAt = time.Time{}
		if mode == "outside" {
			invalid.ObservedAt = cutoff.Add(-8 * 24 * time.Hour)
		} else if mode == "unlocatable" {
			invalid.ObservedAt = time.Time{}
		}
		window := trendWindows(append(rows, invalid), cutoff.Add(time.Minute))[0]
		if window.ExcludedInvalidTimeCount != 1 || contains(window.Blockers, "invalid_observation_time") != (mode != "outside") || (window.CoverageStatus == "complete") != (mode == "outside") {
			t.Fatalf("invalid time %s boundary: %+v", mode, window)
		}
	}
	// Equality is allowed at the cutoff for both event and availability time.
	rows := trendTestRows(t, cutoff, 3)
	rows[2].AvailableAt = cutoff
	if window := trendWindows(rows, cutoff)[0]; window.CoverageStatus != "complete" {
		t.Fatalf("cutoff equality rejected: %+v", window)
	}
}

func TestTrendAnchorGapAndEightyPercentGates(t *testing.T) {
	rows := trendTestRows(t, cutoff, 3)
	rows[0].ObservedAt = rows[0].ObservedAt.Add(-30 * time.Minute)
	rows[0].AvailableAt = rows[0].ObservedAt.Add(time.Second)
	window := trendWindows(rows, cutoff.Add(time.Minute))[0]
	feature := trendFeature(t, window, "nvme.media_errors", "counter_delta")
	if window.CoverageStatus != "complete" || !feature.StartAt.Equal(cutoff.Add(-90*time.Minute)) || window.CoveredSeconds != 3600 {
		t.Fatalf("tolerated anchor did not expose actual span: %+v", window)
	}
	rows[0].ObservedAt = rows[0].ObservedAt.Add(-time.Nanosecond)
	rows[0].AvailableAt = rows[0].ObservedAt.Add(time.Second)
	if window := trendWindows(rows, cutoff.Add(time.Minute))[0]; window.CoverageStatus != "partial" || !contains(window.Blockers, "start_anchor_missing") {
		t.Fatalf("old anchor completed window: %+v", window)
	}
	for _, removed := range []int{9, 10} {
		rows = trendTestRows(t, cutoff, 49)
		selected := []SnapshotView{}
		for i, row := range rows {
			if i%2 == 1 && i < 2*removed {
				continue
			}
			selected = append(selected, row)
		}
		window = trendWindows(selected, cutoff.Add(time.Minute))[1]
		if (window.CoverageStatus == "complete") != (removed == 9) || window.SampleCount != 49-removed {
			t.Fatalf("80%% boundary removed=%d: %+v", removed, window)
		}
	}
	rows = trendTestRows(t, cutoff, 49)
	rows = append(rows[:10], rows[12:]...)
	window = trendWindows(rows, cutoff.Add(time.Minute))[1]
	if window.CoverageStatus != "partial" || !contains(window.Blockers, "maximum_gap_exceeded") {
		t.Fatalf("high density hid 90min gap: %+v", window)
	}
}

func TestTrendMetadataAndHistoricalQuality(t *testing.T) {
	mutations := map[string]func(*SnapshotView){
		"entity":      func(r *SnapshotView) { r.EntityID = "other" },
		"generation":  func(r *SnapshotView) { r.Generation = "other" },
		"boot":        func(r *SnapshotView) { r.BootID = "other" },
		"model":       func(r *SnapshotView) { r.Model = "other" },
		"firmware":    func(r *SnapshotView) { r.Firmware = "other" },
		"profile":     func(r *SnapshotView) { r.Disk.Profile = "other" },
		"source":      func(r *SnapshotView) { r.Source = "other" },
		"source_kind": func(r *SnapshotView) { r.SourceKind = "other" },
		"tool":        func(r *SnapshotView) { r.ToolVersion = "other" },
		"rule":        func(r *SnapshotView) { r.RuleVersion = "other" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			rows := trendTestRows(t, cutoff, 3)
			mutate(&rows[1])
			window := trendWindows(rows, cutoff.Add(time.Minute))[0]
			if window.CoverageStatus != "partial" || !contains(window.Blockers, "incomparable_observations") {
				t.Fatalf("metadata transition accepted: %+v", window)
			}
		})
	}
	rows := trendTestRows(t, cutoff, 3)
	for i := range rows {
		rows[i].AssessmentStatus, rows[i].Freshness, rows[i].Blockers = "insufficient_data", "stale", []string{"stale_evidence"}
		rows[i].Device = "/dev/path-renamed"
	}
	if window := trendWindows(rows, cutoff.Add(24*time.Hour))[0]; window.CoverageStatus != "complete" {
		t.Fatalf("historical age or path rename invalidated comparable evidence: %+v", window)
	}
}

func TestTrendResetMissingFieldAndGaugeChanges(t *testing.T) {
	for _, mode := range []string{"reset", "missing", "invalid"} {
		rows := trendTestRows(t, cutoff, 3)
		switch mode {
		case "reset":
			setTrendEvidence(&rows[1], "nvme.media_errors", "0")
		case "missing":
			for i, evidence := range rows[1].Evidence {
				if evidence.Field == "nvme.media_errors" {
					rows[1].Evidence = append(rows[1].Evidence[:i], rows[1].Evidence[i+1:]...)
					break
				}
			}
		case "invalid":
			setTrendEvidence(&rows[1], "nvme.media_errors", "340282366920938463463374607431768211456")
		}
		window := trendWindows(rows, cutoff.Add(time.Minute))[0]
		feature := trendFeature(t, window, "nvme.media_errors", "counter_delta")
		if window.CoverageStatus != "partial" || feature.Status != "blocked" || feature.Value != nil {
			t.Fatalf("%s produced full window feature: %+v", mode, window)
		}
	}
	rows := trendTestRows(t, cutoff, 3)
	for i, value := range []string{"100", "99", "98"} {
		setTrendEvidence(&rows[i], "nvme.available_spare", value)
	}
	for i, value := range []string{"20", "19", "18"} {
		setTrendEvidence(&rows[i], "nvme.percentage_used", value)
	}
	window := trendWindows(rows, cutoff.Add(time.Minute))[0]
	for _, field := range []string{"nvme.available_spare", "nvme.percentage_used"} {
		if feature := trendFeature(t, window, field, "gauge_change"); feature.Value == nil || *feature.Value != "-2" || feature.Status != "available" || feature.Unit != "percentage_points" {
			t.Fatalf("gauge decrease became reset: %+v", feature)
		}
	}
	for kind, expected := range map[string]string{"minimum": "30", "maximum": "32", "mean": "31.000"} {
		if feature := trendFeature(t, window, "nvme.temperature", kind); feature.Value == nil || *feature.Value != expected {
			t.Fatalf("temperature %s: %+v", kind, feature)
		}
	}
	setTrendEvidence(&rows[2], "nvme.temperature", "30")
	window = trendWindows(rows, cutoff.Add(time.Minute))[0]
	if feature := trendFeature(t, window, "nvme.temperature", "mean"); feature.Value == nil || *feature.Value != "30.333" {
		t.Fatalf("non-terminating mean rounding: %+v", feature)
	}
}

func TestTrendATAFieldsAndStableOrdering(t *testing.T) {
	for _, ssd := range []bool{false, true} {
		rows := []SnapshotView{}
		for i := 0; i < 3; i++ {
			in, data := sampleInput(), ataFixture(t, ssd)
			in.ObservedAt = cutoff.Add(-time.Duration(2-i) * 30 * time.Minute)
			in.AvailableAt = in.ObservedAt.Add(time.Second)
			setATACounter(data, 5, string(rune('1'+i)))
			setATACounter(data, 199, string(rune('2'+i)))
			if ssd {
				setATACounter(data, 187, string(rune('3'+i)))
			}
			rows = append(rows, SnapshotView{Result: Assess(in, marshal(t, data), nil, in.ObservedAt.Add(time.Minute))})
		}
		window := trendWindows(rows, cutoff.Add(time.Minute))[0]
		if window.CoverageStatus != "complete" || len(window.Features) != 5+map[bool]int{true: 1}[ssd] {
			t.Fatalf("ATA window missing fields: %+v", window)
		}
		for _, field := range []string{"ata.5.raw", "ata.199.raw"} {
			feature := trendFeature(t, window, field, "counter_delta")
			if feature.Value == nil || *feature.Value != "2" {
				t.Fatalf("ATA delta: %+v", feature)
			}
		}
		reversed := []SnapshotView{rows[2], rows[0], rows[1]}
		if got := trendWindows(reversed, cutoff.Add(time.Minute))[0]; !reflect.DeepEqual(got, window) {
			t.Fatal("history input order changed deterministic window")
		}
	}
}

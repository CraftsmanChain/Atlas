package diskhealth

import (
	"encoding/json"
	"math/big"
	"sort"
	"strings"
	"time"
)

const (
	TrendContractVersion = "disk-history-window-v2"
	trendNominalPeriod   = 30 * time.Minute
	trendMaximumGap      = time.Hour
)

// TrendWindow describes a nominal window and the actual, non-interpolated
// observations used for it. A start anchor may precede WindowStartAt by 30m.
// Latest* remain the last report's two-point evidence, not window features.
type TrendWindow struct {
	ContractVersion           string          `json:"contract_version"`
	Window                    string          `json:"window"`
	SampleCount               int             `json:"sample_count"`
	FirstObservedAt           *time.Time      `json:"first_observed_at,omitempty"`
	LastObservedAt            *time.Time      `json:"last_observed_at,omitempty"`
	MaxGapSeconds             *int64          `json:"max_gap_seconds,omitempty"`
	CoverageStatus            string          `json:"coverage_status"`
	LatestTrendStatus         string          `json:"latest_trend_status"`
	LatestCounterDeltas       []CounterDelta  `json:"latest_counter_deltas"`
	LatestMediaDelta          *string         `json:"latest_media_delta,omitempty"`
	WindowStartAt             *time.Time      `json:"window_start_at,omitempty"`
	WindowEndAt               *time.Time      `json:"window_end_at,omitempty"`
	ExpectedSampleCount       int             `json:"expected_sample_count"`
	OccupiedSampleCount       int             `json:"occupied_sample_count"`
	CoverageRatio             float64         `json:"coverage_ratio"`
	CoveredSeconds            int64           `json:"covered_seconds"`
	ExcludedFutureCount       int             `json:"excluded_future_count"`
	ExcludedInvalidTimeCount  int             `json:"excluded_invalid_time_count"`
	DuplicateObservationCount int             `json:"duplicate_observation_count"`
	Blockers                  []string        `json:"blockers"`
	Features                  []WindowFeature `json:"features"`
}

type WindowFeature struct {
	Field    string     `json:"field"`
	Kind     string     `json:"kind"`
	Value    *string    `json:"value,omitempty"`
	Unit     string     `json:"unit"`
	Status   string     `json:"status"`
	Blockers []string   `json:"blockers"`
	StartAt  *time.Time `json:"start_at,omitempty"`
	EndAt    *time.Time `json:"end_at,omitempty"`
}

type trendPoint struct {
	row        SnapshotView
	duplicates int
	conflict   bool
}

func trendWindows(rows []SnapshotView, asOf time.Time) []TrendWindow {
	var eligible, invalid []SnapshotView
	futureCount := 0
	for _, row := range rows {
		if row.ObservedAt.IsZero() || row.AvailableAt.IsZero() || row.AvailableAt.Before(row.ObservedAt) {
			invalid = append(invalid, row)
			continue
		}
		if row.ObservedAt.After(asOf) || row.AvailableAt.After(asOf) {
			futureCount++
			continue
		}
		eligible = append(eligible, row)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if !eligible[i].ObservedAt.Equal(eligible[j].ObservedAt) {
			return eligible[i].ObservedAt.Before(eligible[j].ObservedAt)
		}
		return trendObservationKey(eligible[i]) < trendObservationKey(eligible[j])
	})
	points := make([]trendPoint, 0, len(eligible))
	for _, row := range eligible {
		if len(points) == 0 || !points[len(points)-1].row.ObservedAt.Equal(row.ObservedAt) {
			points = append(points, trendPoint{row: row})
			continue
		}
		point := &points[len(points)-1]
		point.duplicates++
		if trendObservationKey(point.row) != trendObservationKey(row) {
			point.conflict = true
		}
	}
	definitions := []struct {
		name string
		span time.Duration
	}{{"1h", time.Hour}, {"24h", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}}
	result := make([]TrendWindow, 0, len(definitions))
	for _, definition := range definitions {
		window := TrendWindow{
			ContractVersion: TrendContractVersion, Window: definition.name,
			ExpectedSampleCount: int(definition.span/trendNominalPeriod) + 1,
			CoverageStatus:      "collecting", LatestTrendStatus: "unavailable",
			LatestCounterDeltas: []CounterDelta{}, Blockers: []string{}, Features: []WindowFeature{},
			ExcludedFutureCount: futureCount, ExcludedInvalidTimeCount: len(invalid),
		}
		if len(points) == 0 {
			window.Blockers = append(window.Blockers, "insufficient_samples")
			if len(invalid) > 0 {
				window.Blockers = append(window.Blockers, "invalid_observation_time")
			}
			result = append(result, window)
			continue
		}
		latest := points[len(points)-1].row
		end := latest.ObservedAt.UTC()
		start := end.Add(-definition.span)
		window.WindowStartAt, window.WindowEndAt = &start, &end
		window.LatestTrendStatus, window.LatestMediaDelta = latest.TrendStatus, latest.MediaDelta
		if latest.Disk != nil {
			window.LatestCounterDeltas = append(window.LatestCounterDeltas, latest.Disk.Deltas...)
		}
		var selected []trendPoint
		anchor := -1
		for i, point := range points {
			if !point.row.ObservedAt.After(start) && !point.row.ObservedAt.Before(start.Add(-trendNominalPeriod)) {
				anchor = i
			}
		}
		if anchor >= 0 {
			selected = append(selected, points[anchor])
		} else {
			window.Blockers = append(window.Blockers, "start_anchor_missing")
		}
		for _, point := range points {
			if point.row.ObservedAt.After(start) && !point.row.ObservedAt.After(end) {
				selected = append(selected, point)
			}
		}
		for _, row := range invalid {
			if row.ObservedAt.IsZero() || !row.ObservedAt.Before(start.Add(-trendNominalPeriod)) && !row.ObservedAt.After(end) {
				window.Blockers = append(window.Blockers, "invalid_observation_time")
			}
		}
		window.SampleCount = len(selected)
		if len(selected) > 0 {
			first, last := selected[0].row.ObservedAt.UTC(), selected[len(selected)-1].row.ObservedAt.UTC()
			window.FirstObservedAt, window.LastObservedAt = &first, &last
			coveredStart := first
			if coveredStart.Before(start) {
				coveredStart = start
			}
			window.CoveredSeconds = int64(last.Sub(coveredStart) / time.Second)
		}
		sequenceBlockers := []string{}
		occupied := map[int]bool{}
		var maxGap time.Duration
		for i, point := range selected {
			window.DuplicateObservationCount += point.duplicates
			pointBlockers := trendQualityBlockers(point.row)
			if point.conflict {
				pointBlockers = append(pointBlockers, "conflicting_observation")
			}
			if !trendComparable(latest, point.row) {
				pointBlockers = append(pointBlockers, "incomparable_observations")
			}
			sequenceBlockers = append(sequenceBlockers, pointBlockers...)
			if len(pointBlockers) == 0 {
				slot := int(point.row.ObservedAt.Sub(start) / trendNominalPeriod)
				if slot < 0 {
					slot = 0
				}
				occupied[slot] = true
			}
			if i > 0 {
				gap := point.row.ObservedAt.Sub(selected[i-1].row.ObservedAt)
				if gap > maxGap {
					maxGap = gap
				}
			}
		}
		window.OccupiedSampleCount = len(occupied)
		window.CoverageRatio = float64(len(occupied)) / float64(window.ExpectedSampleCount)
		if len(selected) < 2 {
			window.Blockers = append(window.Blockers, "insufficient_samples")
		} else {
			gapSeconds := int64(maxGap / time.Second)
			window.MaxGapSeconds = &gapSeconds
			if maxGap > trendMaximumGap {
				window.Blockers = append(window.Blockers, "maximum_gap_exceeded")
			}
		}
		if len(occupied)*5 < window.ExpectedSampleCount*4 {
			window.Blockers = append(window.Blockers, "insufficient_slot_coverage")
		}
		window.Blockers = stableTrendBlockers(append(window.Blockers, sequenceBlockers...))
		if len(selected) >= 2 {
			window.CoverageStatus = "partial"
			if len(window.Blockers) == 0 {
				window.CoverageStatus = "complete"
			}
		}
		window.Features = windowFeatures(selected, latest, window.CoverageStatus, sequenceBlockers)
		for _, feature := range window.Features {
			if feature.Status == "blocked" {
				window.Blockers = append(window.Blockers, feature.Blockers...)
			}
		}
		window.Blockers = stableTrendBlockers(window.Blockers)
		if len(window.Blockers) > 0 && window.CoverageStatus == "complete" {
			window.CoverageStatus = "partial"
			for i := range window.Features {
				if window.Features[i].Status == "available" {
					window.Features[i].Status = "partial"
				}
			}
		}
		result = append(result, window)
	}
	return result
}

func trendQualityBlockers(row SnapshotView) []string {
	blockers := []string{}
	for _, blocker := range row.Blockers {
		if blocker != "stale_evidence" {
			blockers = append(blockers, "quality:"+blocker)
		}
	}
	if row.IdentityStatus != "bound" || row.EntityID == "" || row.Scope == "" || row.Generation == "" || row.Serial == "" {
		blockers = append(blockers, "identity_unverified")
	}
	if row.BootID == "" || row.Model == "" || row.Firmware == "" || row.ToolVersion == "" || row.Source == "" || row.SourceKind == "" || row.RuleVersion == "" {
		blockers = append(blockers, "comparison_metadata_missing")
	}
	if row.Disk == nil || row.Disk.Profile == "" || row.Support == "unsupported" {
		blockers = append(blockers, "profile_unverified")
	}
	if row.AssessmentStatus != "assessed" && !(row.AssessmentStatus == "insufficient_data" && len(row.Blockers) == 1 && row.Blockers[0] == "stale_evidence") {
		blockers = append(blockers, "assessment_quality_insufficient")
	}
	return stableTrendBlockers(blockers)
}

func trendComparable(a, b SnapshotView) bool {
	if a.Disk == nil || b.Disk == nil {
		return false
	}
	return a.EntityID == b.EntityID && a.EntityType == b.EntityType && a.Scope == b.Scope && a.Node == b.Node &&
		a.Generation == b.Generation && a.Serial == b.Serial && a.BootID == b.BootID && a.Model == b.Model &&
		a.Firmware == b.Firmware && a.Protocol == b.Protocol && a.Disk.Profile == b.Disk.Profile &&
		a.Disk.MediaType == b.Disk.MediaType && a.ToolVersion == b.ToolVersion && a.Source == b.Source &&
		a.SourceKind == b.SourceKind && a.RuleVersion == b.RuleVersion
}

func trendObservationKey(row SnapshotView) string {
	// Availability and historical report age do not create a new observation.
	// Derived baseline deltas may differ on a replay; window values use raw fields.
	fields := map[string][]string{}
	for _, evidence := range row.Evidence {
		if !strings.HasSuffix(evidence.Field, "_delta") {
			fields[evidence.Field] = append(fields[evidence.Field], evidence.Value+":"+evidence.Unit)
		}
	}
	for _, values := range fields {
		sort.Strings(values)
	}
	profile, media := "", ""
	if row.Disk != nil {
		profile, media = row.Disk.Profile, row.Disk.MediaType
	}
	encoded, _ := json.Marshal(struct {
		Metadata []string
		Fields   map[string][]string
		Quality  []string
	}{[]string{row.InputSHA256, row.EntityID, row.EntityType, row.Scope, row.Node, row.Generation, row.Serial, row.BootID, row.Model, row.Firmware, row.Protocol, profile, media, row.ToolVersion, row.Source, row.SourceKind, row.RuleVersion}, fields, trendQualityBlockers(row)})
	return string(encoded)
}

func stableTrendBlockers(blockers []string) []string {
	set := map[string]bool{}
	for _, blocker := range blockers {
		set[blocker] = true
	}
	result := make([]string, 0, len(set))
	for blocker := range set {
		result = append(result, blocker)
	}
	sort.Strings(result)
	return result
}

type trendFeatureDefinition struct {
	field, kind, unit string
	bits              int
}

func windowFeatures(points []trendPoint, latest SnapshotView, coverage string, sequenceBlockers []string) []WindowFeature {
	definitions := []trendFeatureDefinition{}
	temperature := ""
	if latest.Protocol == "NVMe" {
		definitions = append(definitions,
			trendFeatureDefinition{"nvme.media_errors", "counter_delta", "count", 128},
			trendFeatureDefinition{"nvme.available_spare", "gauge_change", "percent", 8},
			trendFeatureDefinition{"nvme.percentage_used", "gauge_change", "percent", 8})
		temperature = "nvme.temperature"
	} else if latest.Protocol == "ATA" {
		unit := "count"
		if latest.Disk != nil && latest.Disk.MediaType == "HDD" {
			unit = "sectors"
		}
		definitions = append(definitions, trendFeatureDefinition{"ata.5.raw", "counter_delta", unit, 128}, trendFeatureDefinition{"ata.199.raw", "counter_delta", "count", 128})
		if latest.Disk != nil && latest.Disk.MediaType == "SSD" {
			definitions = append(definitions, trendFeatureDefinition{"ata.187.raw", "counter_delta", "count", 128})
		}
		temperature = "ata.temperature"
	}
	if temperature != "" {
		for _, kind := range []string{"minimum", "maximum", "mean"} {
			definitions = append(definitions, trendFeatureDefinition{temperature, kind, "celsius", 16})
		}
	}
	features := make([]WindowFeature, 0, len(definitions))
	for _, definition := range definitions {
		feature := WindowFeature{Field: definition.field, Kind: definition.kind, Unit: definition.unit, Status: "blocked", Blockers: append([]string{}, sequenceBlockers...)}
		if definition.kind == "gauge_change" && definition.unit == "percent" {
			feature.Unit = "percentage_points"
		}
		if len(points) > 0 {
			first, last := points[0].row.ObservedAt.UTC(), points[len(points)-1].row.ObservedAt.UTC()
			feature.StartAt, feature.EndAt = &first, &last
		}
		if len(points) < 2 {
			feature.Blockers = append(feature.Blockers, "insufficient_samples")
		}
		values := make([]*big.Int, 0, len(points))
		for _, point := range points {
			var value *big.Int
			for _, evidence := range point.row.Evidence {
				if evidence.Field != definition.field {
					continue
				}
				encoded, _ := json.Marshal(evidence.Value)
				parsed, err := exactInteger(encoded, definition.bits)
				if err != nil || evidence.Unit != definition.unit || definition.field == "nvme.available_spare" && parsed.Int64() > 100 {
					feature.Blockers = append(feature.Blockers, "invalid_field:"+definition.field)
					continue
				}
				if value != nil && value.Cmp(parsed) != 0 {
					feature.Blockers = append(feature.Blockers, "conflicting_field:"+definition.field)
				}
				value = parsed
			}
			if value == nil {
				feature.Blockers = append(feature.Blockers, "missing_field:"+definition.field)
				continue
			}
			if definition.kind == "counter_delta" && len(values) > 0 && value.Cmp(values[len(values)-1]) < 0 {
				feature.Blockers = append(feature.Blockers, "counter_reset:"+definition.field)
			}
			values = append(values, value)
		}
		feature.Blockers = stableTrendBlockers(feature.Blockers)
		if len(feature.Blockers) == 0 && len(values) == len(points) {
			var value string
			switch definition.kind {
			case "counter_delta", "gauge_change":
				value = new(big.Int).Sub(values[len(values)-1], values[0]).String()
			case "minimum", "maximum":
				selected := values[0]
				for _, candidate := range values[1:] {
					if definition.kind == "minimum" && candidate.Cmp(selected) < 0 || definition.kind == "maximum" && candidate.Cmp(selected) > 0 {
						selected = candidate
					}
				}
				value = selected.String()
			case "mean":
				sum := new(big.Int)
				for _, candidate := range values {
					sum.Add(sum, candidate)
				}
				value = new(big.Rat).SetFrac(sum, big.NewInt(int64(len(values)))).FloatString(3)
			}
			feature.Value = &value
			feature.Status = "partial"
			if coverage == "complete" {
				feature.Status = "available"
			}
		}
		features = append(features, feature)
	}
	return features
}

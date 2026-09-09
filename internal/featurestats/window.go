package featurestats

import (
	"math"
	"strings"
	"time"

	promclient "atlas/internal/prometheus"
)

const TrailingRangeContractVersion = "trailing-multiscale-range-statistics-v2"

// Trailing24hContractVersion remains as a compatibility alias for callers that
// only consume the original 24h statistics.
const Trailing24hContractVersion = TrailingRangeContractVersion

var trailing24hStatistics = []string{
	"last_24h", "mean_24h", "min_24h", "max_24h", "stddev_24h",
	"delta_24h", "slope_per_hour_24h", "sample_count_24h",
}

var shortWindowDurations = []struct {
	label    string
	duration time.Duration
}{
	{label: "15m", duration: 15 * time.Minute},
	{label: "1h", duration: time.Hour},
	{label: "6h", duration: 6 * time.Hour},
}

var shortWindowStatistics = []string{"mean", "max", "delta", "slope_per_hour", "sample_count"}

func Trailing24hStatistics() []string {
	return append([]string(nil), trailing24hStatistics...)
}

func ParseTrailing24hColumn(column string) (string, string, bool) {
	for _, statistic := range trailing24hStatistics {
		suffix := "_" + statistic
		if strings.HasSuffix(column, suffix) && len(column) > len(suffix) {
			return strings.TrimSuffix(column, suffix), statistic, true
		}
	}
	return "", "", false
}

// ParseTrailingRangeColumn accepts both the original 24h columns and the
// short-window columns introduced by the shared multi-scale transformation.
func ParseTrailingRangeColumn(column string) (string, string, time.Duration, bool) {
	if base, statistic, ok := ParseTrailing24hColumn(column); ok {
		return base, statistic, 24 * time.Hour, true
	}
	for _, window := range shortWindowDurations {
		for _, statistic := range shortWindowStatistics {
			suffix := "_" + statistic + "_" + window.label
			if strings.HasSuffix(column, suffix) && len(column) > len(suffix) {
				return strings.TrimSuffix(column, suffix), statistic + "_" + window.label, window.duration, true
			}
		}
	}
	return "", "", 0, false
}

func TrailingRangeStatistics() []string {
	result := Trailing24hStatistics()
	for _, window := range shortWindowDurations {
		for _, statistic := range shortWindowStatistics {
			result = append(result, statistic+"_"+window.label)
		}
	}
	return result
}

func AddTrailing24hStatistics(result map[string]float64, name string, points []promclient.RangePoint) {
	addStatistics(result, name, "24h", points, true)
}

// AddTrailingRangeStatistics computes all windows from the same point-in-time
// safe 24h query. No sample after cutoff participates in any derived value.
func AddTrailingRangeStatistics(result map[string]float64, name string, points []promclient.RangePoint, cutoff time.Time) {
	safePoints := make([]promclient.RangePoint, 0, len(points))
	for _, point := range points {
		if !point.Timestamp.After(cutoff) && !math.IsNaN(point.Value) && !math.IsInf(point.Value, 0) {
			safePoints = append(safePoints, point)
		}
	}
	if len(safePoints) == 0 {
		return
	}
	AddTrailing24hStatistics(result, name, safePoints)
	for _, window := range shortWindowDurations {
		start := cutoff.Add(-window.duration)
		windowPoints := make([]promclient.RangePoint, 0, len(safePoints))
		for _, point := range safePoints {
			if !point.Timestamp.Before(start) {
				windowPoints = append(windowPoints, point)
			}
		}
		if len(windowPoints) > 0 {
			addStatistics(result, name, window.label, windowPoints, false)
		}
	}
}

func addStatistics(result map[string]float64, name, window string, points []promclient.RangePoint, includeFullSummary bool) {
	minimum, maximum, sum := points[0].Value, points[0].Value, float64(0)
	for _, point := range points {
		sum += point.Value
		minimum = math.Min(minimum, point.Value)
		maximum = math.Max(maximum, point.Value)
	}
	mean := sum / float64(len(points))
	var squared float64
	for _, point := range points {
		delta := point.Value - mean
		squared += delta * delta
	}
	if includeFullSummary {
		result[name+"_last_"+window] = points[len(points)-1].Value
		result[name+"_min_"+window] = minimum
		result[name+"_stddev_"+window] = math.Sqrt(squared / float64(len(points)))
	}
	result[name+"_mean_"+window] = mean
	result[name+"_max_"+window] = maximum
	result[name+"_delta_"+window] = points[len(points)-1].Value - points[0].Value
	result[name+"_slope_per_hour_"+window] = linearSlopePerHour(points)
	result[name+"_sample_count_"+window] = float64(len(points))
}

func linearSlopePerHour(points []promclient.RangePoint) float64 {
	if len(points) < 2 {
		return 0
	}
	origin := points[0].Timestamp
	var sumX, sumY, sumXY, sumXX float64
	for _, point := range points {
		x := point.Timestamp.Sub(origin).Hours()
		sumX += x
		sumY += point.Value
		sumXY += x * point.Value
		sumXX += x * x
	}
	n := float64(len(points))
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denominator
}

package featurestats

import (
	"math"
	"strings"

	promclient "atlas/internal/prometheus"
)

const Trailing24hContractVersion = "trailing-24h-range-statistics-v1"

var trailing24hStatistics = []string{
	"last_24h", "mean_24h", "min_24h", "max_24h", "stddev_24h",
	"delta_24h", "slope_per_hour_24h", "sample_count_24h",
}

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

func AddTrailing24hStatistics(result map[string]float64, name string, points []promclient.RangePoint) {
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
	result[name+"_last_24h"] = points[len(points)-1].Value
	result[name+"_mean_24h"] = mean
	result[name+"_min_24h"] = minimum
	result[name+"_max_24h"] = maximum
	result[name+"_stddev_24h"] = math.Sqrt(squared / float64(len(points)))
	result[name+"_delta_24h"] = points[len(points)-1].Value - points[0].Value
	result[name+"_slope_per_hour_24h"] = linearSlopePerHour(points)
	result[name+"_sample_count_24h"] = float64(len(points))
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

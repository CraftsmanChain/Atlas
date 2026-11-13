package inventory

import (
	"atlas/internal/prometheus"
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// Match the existing GPU telemetry stale-age boundary (300 seconds).
const collectionSampleMaxAge = 5 * time.Minute

const collectionHealthQuery = `{job="gpu_exporter",__name__=~"nvidia_smi_last_collect_success|nvidia_smi_command_exit_code|nvidia_smi_utilization_gpu_ratio"}`

// A missing/failed diagnostics query cannot prove recovery of a known failure.
const collectionTimestampQuery = `timestamp(label_replace(` + collectionHealthQuery + `,"atlas_source_metric","$1","__name__","(.+)"))`

func collectionSampleKey(labels map[string]string) string {
	m := map[string]string{}
	for k, v := range labels {
		if k != "__name__" && k != "atlas_source_metric" {
			m[k] = v
		}
	}
	name := labels["__name__"]
	if labels["atlas_source_metric"] != "" {
		name = labels["atlas_source_metric"]
	}
	m["metric_name"] = name
	b, _ := json.Marshal(m)
	return string(b)
}

func (s *Service) collectionHealth(ctx context.Context, now time.Time) map[string]string {
	rows, err := s.prom.Query(ctx, collectionHealthQuery)
	if err != nil {
		return nil
	}
	stamps, err := s.prom.Query(ctx, collectionTimestampQuery)
	if err != nil {
		return nil
	}
	times := map[string]time.Time{}
	for _, v := range stamps {
		if v.Metric["atlas_source_metric"] != "" && !math.IsNaN(v.Value) && !math.IsInf(v.Value, 0) && v.Value > 0 {
			times[collectionSampleKey(v.Metric)] = time.Unix(0, int64(v.Value*1e9))
		}
	}
	rows = append([]prometheus.Sample(nil), rows...)
	for i := range rows {
		rows[i].Timestamp = times[collectionSampleKey(rows[i].Metric)]
	}
	return classifyCollections(rows, now)
}
func classifyCollections(rows []prometheus.Sample, now time.Time) map[string]string {
	type state struct {
		success, exit                           float64
		hasSuccess, hasExit, business, conflict bool
	}
	by := map[string]*state{}
	for _, r := range rows {
		if r.Metric["job"] != "gpu_exporter" || r.Metric["instance"] == "" || r.Timestamp.IsZero() || now.Sub(r.Timestamp) >= collectionSampleMaxAge || r.Timestamp.After(now.Add(time.Minute)) || math.IsNaN(r.Value) || math.IsInf(r.Value, 0) {
			continue
		}
		key := r.Metric["instance"]
		v := by[key]
		if v == nil {
			v = &state{}
			by[key] = v
		}
		switch r.Metric["__name__"] {
		case "nvidia_smi_last_collect_success":
			if v.hasSuccess && v.success != r.Value {
				v.conflict = true
			}
			v.success = r.Value
			v.hasSuccess = true
		case "nvidia_smi_command_exit_code":
			if v.hasExit && v.exit != r.Value {
				v.conflict = true
			}
			v.exit = r.Value
			v.hasExit = true
		case "nvidia_smi_utilization_gpu_ratio":
			v.business = true
		}
	}
	result := map[string]string{}
	for instance, v := range by {
		switch {
		case v.conflict:
			result[instance] = "gpu_collection_unverified"
		case v.hasSuccess && v.success == 0 || v.hasExit && v.exit > 0:
			result[instance] = "gpu_collection_failed"
		case v.hasSuccess && v.success == 1 && v.hasExit && v.exit == 0:
			if v.business {
				result[instance] = "healthy"
			} else {
				result[instance] = "gpu_business_metrics_missing"
			}
		}
	}
	return result
}
func collectionReason(v string) bool {
	return strings.HasPrefix(v, "gpu_collection_") || v == "gpu_business_metrics_missing"
}

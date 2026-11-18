package inventory

import (
	"context"
	"math"
	"strings"
	"time"

	"atlas/internal/prometheus"
)

// IPMI Exporter coverage is judged by the exporter's own collector="ipmi"
// result, not by the Prometheus scrape alone: ipmi_exporter answers the scrape
// even when the BMC cannot be read, and its other collectors (sel, sel-events,
// bmc, chassis, dcmi) fail too often on this fleet to be a coverage signal.
//
//	UP          latest ipmi_up{collector="ipmi"} == 1 and the sample is ≤5 min old
//	continuous  over the last hour no sample gap > ipmiMaxGap and ≥95% of the
//	            collector="ipmi" samples were successful
const (
	ipmiSelector       = `ipmi_up{job="ipmi_exporter",collector="ipmi"}`
	ipmiWindow         = "1h"
	ipmiMaxGap         = 3 * time.Minute
	ipmiMinSuccess     = 0.95
	ipmiDiagnosticsKey = "atlas:ipmi_collector_query_ok"
	// "atlas:ipmi_scrape_recent:<instance>" present = scrape succeeded within ipmiHold.
	ipmiScrapeRecentPrefix = "atlas:ipmi_scrape_recent:"
	// A failure or absence must persist this long before down/missing is reported.
	ipmiHold       = 10 * time.Minute
	ipmiHoldWindow = "10m"
)

const (
	ipmiHealthy       = "healthy"
	ipmiMissing       = "ipmi_collector_missing"
	ipmiStale         = "ipmi_collector_stale"
	ipmiFailed        = "ipmi_collector_failed"     // BMC answers bmc/chassis but sensor read (collector=ipmi) fails
	ipmiAllFailed     = "ipmi_collector_all_failed" // bmc, chassis and ipmi all fail: login rejected or BMC unreachable
	ipmiDiscontinuous = "ipmi_collector_discontinuous"
	ipmiUnverified    = "ipmi_collector_unverified"
)

// One bounded query (one series per BMC instance per signal). The gap signal
// is a 15s-step subquery of sample age, so it resolves gaps to ~15s.
var ipmiCollectorQuery = strings.Join([]string{
	// Debounce: judged over the last ipmiHold, so one failed scrape never flips a node.
	ipmiSignal("min", `min by (instance) (min_over_time(`+ipmiSelector+`[`+ipmiHoldWindow+`]))`),
	ipmiSignal("max", `max by (instance) (max_over_time(`+ipmiSelector+`[`+ipmiHoldWindow+`]))`),
	ipmiSignal("sample_ts", `max by (instance) (timestamp(last_over_time(`+ipmiSelector+`[`+ipmiHoldWindow+`])))`),
	ipmiSignal("scrape_max", `max by (instance) (max_over_time(up{job="ipmi_exporter"}[`+ipmiHoldWindow+`]))`),
	ipmiSignal("success_1h", `min by (instance) (avg_over_time(`+ipmiSelector+`[`+ipmiWindow+`]))`),
	// Diagnosis only (never decides UP): tells "BMC unusable" from "sensor read broken".
	ipmiSignal("bmc_max", `max by (instance) (ipmi_up{job="ipmi_exporter",collector="bmc"})`),
	ipmiSignal("chassis_max", `max by (instance) (ipmi_up{job="ipmi_exporter",collector="chassis"})`),
	// Exact failure cause reported passively from the exporter host's journal.
	`label_replace(label_replace(max by (ipmi_target, reason) (atlas_ipmi_failure_reason), "instance", "$1", "ipmi_target", "(.+)"), "atlas_signal", "reason", "", "")`,
	ipmiSignal("max_gap_1h", `max by (instance) (max_over_time((time() - timestamp(`+ipmiSelector+`))[`+ipmiWindow+`:15s]))`),
}, " or ")

func ipmiSignal(name, expr string) string {
	return `label_replace(` + expr + `, "atlas_signal", "` + name + `", "", "")`
}

func (s *Service) ipmiCollectorHealth(ctx context.Context, now time.Time) map[string]string {
	rows, err := s.prom.Query(ctx, ipmiCollectorQuery)
	if err != nil {
		return nil
	}
	return classifyIPMICollectors(rows, now)
}

func classifyIPMICollectors(rows []prometheus.Sample, now time.Time) map[string]string {
	type state struct {
		min, max, ts, success, gap, bmc, chassis float64
		reason                                   string
		hasMin, hasMax, hasTS, hasSucc, hasGap   bool
		hasBMC, hasChassis                       bool
	}
	by := map[string]*state{}
	scrape := map[string]float64{}
	for _, r := range rows {
		instance := r.Metric["instance"]
		if instance == "" || math.IsNaN(r.Value) || math.IsInf(r.Value, 0) {
			continue
		}
		if r.Metric["atlas_signal"] == "scrape_max" {
			scrape[instance] = r.Value
			continue
		}
		v := by[instance]
		if v == nil {
			v = &state{}
			by[instance] = v
		}
		switch r.Metric["atlas_signal"] {
		case "min":
			v.min, v.hasMin = r.Value, true
		case "max":
			v.max, v.hasMax = r.Value, true
		case "sample_ts":
			v.ts, v.hasTS = r.Value, true
		case "success_1h":
			v.success, v.hasSucc = r.Value, true
		case "reason":
			v.reason = r.Metric["reason"]
		case "bmc_max":
			v.bmc, v.hasBMC = r.Value, true
		case "chassis_max":
			v.chassis, v.hasChassis = r.Value, true
		case "max_gap_1h":
			v.gap, v.hasGap = r.Value, true
		}
	}
	result := map[string]string{ipmiDiagnosticsKey: "ok"}
	for instance, up := range scrape {
		if up == 1 {
			result[ipmiScrapeRecentPrefix+instance] = "up"
		}
	}
	for instance, v := range by {
		if !v.hasMin || !v.hasMax || !v.hasTS {
			result[instance] = ipmiMissing
			continue
		}
		sampled := time.Unix(0, int64(v.ts*1e9))
		switch {
		case now.Sub(sampled) > ipmiHold || sampled.After(now.Add(time.Minute)):
			result[instance] = ipmiStale
		// max over the hold window is 0: every sample in the last 10 minutes failed.
		case v.max != 1 && ipmiCauseCode(v.reason) != "":
			result[instance] = ipmiCauseCode(v.reason)
		case v.max != 1 && v.hasBMC && v.hasChassis && v.bmc == 0 && v.chassis == 0:
			result[instance] = ipmiAllFailed
		case v.max != 1:
			result[instance] = ipmiFailed
		case !v.hasSucc || !v.hasGap || v.success < ipmiMinSuccess || v.gap > ipmiMaxGap.Seconds():
			result[instance] = ipmiDiscontinuous
		default:
			result[instance] = ipmiHealthy
		}
	}
	return result
}

// ipmiCauseCode maps the exporter-host reason label to a stored reason code.
func ipmiCauseCode(reason string) string {
	switch reason {
	case "username_invalid", "password_invalid", "connection_timeout", "sdr_cache_error", "privilege_insufficient", "bmc_busy":
		return "ipmi_collector_" + reason
	}
	return ""
}

// ipmiAssessment turns a scrape-up ipmi_exporter target into the stored
// health/reason using the collector="ipmi" diagnosis. A failed diagnostics
// query cannot prove recovery, so a previous ipmi_collector_* verdict is kept.
func ipmiAssessment(diagnostics map[string]string, instance string, existingHealth, existingReason string, hasExisting bool) (string, string) {
	if diagnostics[ipmiDiagnosticsKey] == "" {
		if hasExisting && strings.HasPrefix(existingReason, "ipmi_collector_") {
			return existingHealth, existingReason
		}
		return "up", ""
	}
	switch diagnosis := diagnostics[instance]; diagnosis {
	case ipmiHealthy:
		return "up", ""
	case "":
		return "down", ipmiMissing
	case ipmiDiscontinuous, ipmiUnverified:
		return "degraded", diagnosis
	default:
		return "down", diagnosis
	}
}

func mergeDiagnostics(parts ...map[string]string) map[string]string {
	var out map[string]string
	for _, part := range parts {
		for k, v := range part {
			if out == nil {
				out = map[string]string{}
			}
			out[k] = v
		}
	}
	return out
}

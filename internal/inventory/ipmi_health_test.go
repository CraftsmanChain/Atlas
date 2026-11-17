package inventory

import (
	"atlas/internal/issues"
	"atlas/internal/prometheus"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
	"strings"
	"testing"
	"time"
)

func ipmiRows(instance string, now time.Time, latest, success, gapSeconds float64, age time.Duration) []prometheus.Sample {
	row := func(signal string, v float64) prometheus.Sample {
		return prometheus.Sample{Metric: map[string]string{"instance": instance, "atlas_signal": signal}, Value: v, Timestamp: now}
	}
	return []prometheus.Sample{row("min", latest), row("max", latest), row("sample_ts", float64(now.Add(-age).UnixNano())/1e9), row("success_1h", success), row("max_gap_1h", gapSeconds)}
}

func TestIPMICollectorClassification(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name string
		rows []prometheus.Sample
		want string
	}{
		{"healthy", ipmiRows("b", now, 1, 1, 45, 20*time.Second), ipmiHealthy},
		{"collector_failed_while_scrape_up", ipmiRows("b", now, 0, 0, 45, 20*time.Second), ipmiFailed},
		{"all_collectors_failed", append(ipmiRows("b", now, 0, 0, 45, 20*time.Second), prometheus.Sample{Metric: map[string]string{"instance": "b", "atlas_signal": "bmc_max"}, Value: 0}, prometheus.Sample{Metric: map[string]string{"instance": "b", "atlas_signal": "chassis_max"}, Value: 0}), ipmiAllFailed},
		{"sensor_only_failed", append(ipmiRows("b", now, 0, 0, 45, 20*time.Second), prometheus.Sample{Metric: map[string]string{"instance": "b", "atlas_signal": "bmc_max"}, Value: 1}, prometheus.Sample{Metric: map[string]string{"instance": "b", "atlas_signal": "chassis_max"}, Value: 1}), ipmiFailed},
		{"username_invalid_cause", append(ipmiRows("b", now, 0, 0, 45, 20*time.Second), prometheus.Sample{Metric: map[string]string{"instance": "b", "atlas_signal": "reason", "reason": "username_invalid"}}), "ipmi_collector_username_invalid"},
		{"sdr_cause_beats_sensor_only", append(ipmiRows("b", now, 0, 0, 45, 20*time.Second), prometheus.Sample{Metric: map[string]string{"instance": "b", "atlas_signal": "reason", "reason": "sdr_cache_error"}}), "ipmi_collector_sdr_cache_error"},
		{"cause_ignored_when_healthy", append(ipmiRows("b", now, 1, 1, 45, 20*time.Second), prometheus.Sample{Metric: map[string]string{"instance": "b", "atlas_signal": "reason", "reason": "username_invalid"}}), ipmiHealthy},
		{"stale", ipmiRows("b", now, 1, 1, 45, 6*time.Minute), ipmiStale},
		{"gap", ipmiRows("b", now, 1, 1, 400, 20*time.Second), ipmiDiscontinuous},
		{"flapping", ipmiRows("b", now, 1, 0.9, 45, 20*time.Second), ipmiDiscontinuous},
		{"conflict", append(ipmiRows("b", now, 1, 1, 45, 0), prometheus.Sample{Metric: map[string]string{"instance": "b", "atlas_signal": "min"}, Value: 0}), ipmiUnverified},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyIPMICollectors(test.rows, now)
			if got["b"] != test.want || got[ipmiDiagnosticsKey] == "" {
				t.Fatal(got)
			}
		})
	}
	if !strings.Contains(ipmiCollectorQuery, `collector="ipmi"`) || strings.Contains(ipmiCollectorQuery, `collector="sel"`) {
		t.Fatal("IPMI coverage must be judged by collector=ipmi only")
	}
}

func TestIPMITargetHealthFollowsCollectorIPMI(t *testing.T) {
	db, e := storage.InitDB(t.TempDir() + "/a.db")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	node := api.GPUNode{NodeIP: "10.80.1.5", Lifecycle: "active", State: "up"}
	if e = db.Create(&node).Error; e != nil {
		t.Fatal(e)
	}
	s := NewService(db, &fakePrometheus{}, config.InventoryConfig{TargetJobs: []string{"node_exporter", "ipmi_exporter"}, BMCFromHostLabel: true})
	bmc := prometheus.Target{Health: "up", Labels: map[string]string{"job": "ipmi_exporter", "instance": "10.200.0.5", "host_ip": node.NodeIP}}
	targets := map[string]prometheus.Target{targetKey("node_exporter", node.NodeIP): {Health: "up"}, targetKey("ipmi_host", node.NodeIP): bmc}
	for _, phase := range []struct {
		name         string
		diagnostics  map[string]string
		health, code string
		active       int64
	}{
		{"collector_ipmi_failed", map[string]string{ipmiDiagnosticsKey: "ok", "10.200.0.5": ipmiFailed}, "down", ipmiFailed, 1},
		{"query_failed_keeps_verdict", nil, "down", ipmiFailed, 1},
		{"gappy", map[string]string{ipmiDiagnosticsKey: "ok", "10.200.0.5": ipmiDiscontinuous}, "degraded", ipmiDiscontinuous, 1},
		{"no_collector_series", map[string]string{ipmiDiagnosticsKey: "ok"}, "down", ipmiMissing, 1},
		{"recovered", map[string]string{ipmiDiagnosticsKey: "ok", "10.200.0.5": ipmiHealthy}, "up", "", 0},
		{"query_failed_after_healthy", nil, "up", "", 0},
	} {
		if _, e = s.persistTargets(now, 0, []string{node.NodeIP}, targets, phase.diagnostics); e != nil {
			t.Fatal(e)
		}
		var target api.CollectorTarget
		if e = db.Where("target_key = ?", targetKey("ipmi_exporter", node.NodeIP)).First(&target).Error; e != nil {
			t.Fatal(e)
		}
		if target.Health != phase.health || target.ReasonCode != phase.code || target.TargetIP != "10.200.0.5" {
			t.Fatalf("%s: %+v", phase.name, target)
		}
		if e = issues.NewService(db).SyncDetectedIssues(); e != nil {
			t.Fatal(e)
		}
		var count int64
		db.Model(&api.PlatformIssue{}).Where("detection_source = ? AND detection_state = ?", "collector_target", "active").Count(&count)
		if count != phase.active {
			t.Fatalf("%s: active issues %d", phase.name, count)
		}
	}
}

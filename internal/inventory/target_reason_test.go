package inventory

import (
	"testing"

	"atlas/internal/prometheus"
)

func TestAssessTarget(t *testing.T) {
	nodeIP := "10.114.4.21"
	tests := []struct {
		name       string
		job        string
		health     string
		lastError  string
		targets    map[string]prometheus.Target
		reason     string
		suppressed bool
	}{
		{name: "up target", job: "dcgm_exporter", health: "up", reason: ""},
		{name: "inband unreachable suppresses gpu exporter", job: "dcgm_exporter", health: "down", reason: "node_inband_unreachable", suppressed: true},
		{name: "missing target on live node", job: "gpu_exporter", health: "missing", targets: map[string]prometheus.Target{targetKey("node_exporter", nodeIP): {Health: "up"}}, reason: "target_missing"},
		{name: "timeout on live node", job: "dcgm_exporter", health: "down", lastError: "context deadline exceeded", targets: map[string]prometheus.Target{targetKey("node_exporter", nodeIP): {Health: "up"}}, reason: "scrape_timeout"},
		{name: "bmc remains independent", job: "ipmi_exporter", health: "down", lastError: "dial tcp: connect: connection refused", reason: "connection_refused"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assessment := assessTarget(test.job, nodeIP, test.health, test.lastError, test.targets)
			if assessment.reasonCode != test.reason || assessment.suppressed != test.suppressed {
				t.Fatalf("unexpected assessment: %+v", assessment)
			}
		})
	}
}

package notifier

import (
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
)

func TestFormatAlertContentIncludesNormalizedOperationalEvidence(t *testing.T) {
	event := &api.AlertEvent{
		Source:     "alertmanager",
		Level:      "critical",
		Message:    "节点失活",
		LastSeenAt: time.Date(2026, 7, 26, 17, 44, 10, 0, time.Local),
		Labels: api.StringMap{
			"host_ip":       "10.114.4.113",
			"alert_state":   "recovered",
			"duration_text": "8h 5m 0s",
			"exporter":      "node_exporter",
			"model":         "H100",
			"sn":            "A514359X4407148",
		},
	}
	content := formatAlertContent(event)
	for _, expected := range []string{
		"**主机**: 10.114.4.113",
		"**内容**: 节点失活",
		"**最后发生时间**: 2026-07-26 17:44:10",
		"- alert_state: recovered",
		"- duration_text: 8h 5m 0s",
		"- exporter: node_exporter",
		"- model: H100",
		"- sn: A514359X4407148",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("expected %q in notification:\n%s", expected, content)
		}
	}
}

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

func TestFormatThermalAlertContentIsFocusedAndComplete(t *testing.T) {
	event := &api.AlertEvent{Source: "atlas_thermal_monitor", Host: "gpu-node-04", Level: "critical", Message: "持续 GPU 高温告警", LastSeenAt: time.Date(2026, 8, 19, 14, 20, 0, 0, time.Local), Labels: api.StringMap{
		"alert_topic": "GPU sustained high temperature", "hostname": "gpu-node-04", "host_ip": "10.114.4.43", "gpu_index": "5", "gpu_model": "NVIDIA GeForce RTX 4090", "gpu_uuid": "GPU-abc", "temperature_celsius": "90.0", "threshold": ">= 90C continuously for 5m", "sustained_duration": "5m", "evidence_collection": "nvidia-smi -q -d PERFORMANCE (read-only follow-up)",
	}}
	content := formatAlertContent(event)
	for _, expected := range []string{"**告警主题**", "**主机名**: gpu-node-04", "**IP**: 10.114.4.43", "GPU 5 · NVIDIA GeForce RTX 4090", "**温度**: 90.0°C", "**持续时间**: 5m", "nvidia-smi -q -d PERFORMANCE"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("expected %q in thermal notification:\n%s", expected, content)
		}
	}
}

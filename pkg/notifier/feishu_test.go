package notifier

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
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

func TestSendBotCardPostsInteractiveCard(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer server.Close()

	bot := config.FeishuBotConfig{Enabled: true, WebhookURL: server.URL}
	if err := SendBotCard(bot, "Atlas 资产核查", "orange", "**发现 1 条差异**"); err != nil {
		t.Fatalf("card send failed: %v", err)
	}
	if received["msg_type"] != "interactive" {
		t.Fatalf("expected an interactive card, got %v", received["msg_type"])
	}
	card, ok := received["card"].(map[string]any)
	if !ok {
		t.Fatalf("card payload missing: %v", received)
	}
	header, _ := card["header"].(map[string]any)
	if header["template"] != "orange" {
		t.Fatalf("template not forwarded: %v", header)
	}
	if _, signed := received["sign"]; signed {
		t.Fatal("a bot without signature enabled must not attach a sign")
	}

	disabled := config.FeishuBotConfig{Enabled: false, WebhookURL: server.URL}
	if err := SendBotCard(disabled, "t", "blue", "c"); err == nil {
		t.Fatal("a disabled bot must be rejected")
	}
}

func TestSendBotCardRejectsWebhookError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":19021,"msg":"sign match fail"}`))
	}))
	defer server.Close()

	if err := SendBotCard(config.FeishuBotConfig{Enabled: true, WebhookURL: server.URL}, "t", "blue", "c"); err == nil {
		t.Fatal("a non-zero webhook code must surface as an error")
	}
}

func TestSendCardFansOutToEnabledBots(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer server.Close()

	notifier := &FeishuNotifier{cfg: &config.FeishuConfig{Bots: []config.FeishuBotConfig{
		{Enabled: true, WebhookURL: server.URL},
		{Enabled: false, WebhookURL: server.URL},
	}}}
	if err := notifier.SendCard("t", "green", "c"); err != nil {
		t.Fatalf("fan-out failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one delivery, got %d", count)
	}

	if err := (&FeishuNotifier{cfg: &config.FeishuConfig{}}).SendCard("t", "green", "c"); err == nil {
		t.Fatal("no configured bot must be reported as an error")
	}
}


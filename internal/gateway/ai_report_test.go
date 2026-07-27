package gateway

import (
	"strings"
	"testing"

	"atlas/pkg/api"
)

func TestBuildPlaceholderAIReportForXIDAlert(t *testing.T) {
	event := api.AlertEvent{
		Source:  "alertmanager",
		Level:   "warning",
		Host:    "4090GPU-03",
		Message: "XID故障-低优先级",
		Labels: api.StringMap{
			"err_code": "43",
			"gpu":      "3",
		},
	}

	report := buildPlaceholderAIReport(event)
	if report["status"] != "completed" {
		t.Fatalf("expected completed report, got %#v", report["status"])
	}
	if report["model"] != "atlas-placeholder" {
		t.Fatalf("expected atlas-placeholder model, got %#v", report["model"])
	}
	summary, _ := report["summary"].(string)
	if summary == "" || !strings.Contains(summary, "GPU XID") {
		t.Fatalf("expected xid summary, got %q", summary)
	}
}

func TestBuildPlaceholderAIReportForNetworkAlert(t *testing.T) {
	event := api.AlertEvent{
		Source:  "alertmanager",
		Level:   "warning",
		Host:    "10.111.101.4",
		Message: "网络失活-外网",
		Labels: api.StringMap{
			"target": "www.dingtalk.com",
			"job":    "blackbox-icmp-wwxq",
		},
	}

	report := buildPlaceholderAIReport(event)
	summary, _ := report["summary"].(string)
	if summary == "" || !strings.Contains(summary, "Network reachability") {
		t.Fatalf("expected network summary, got %q", summary)
	}
}

func TestRefreshPlaceholderAIReportUsesReparsedFields(t *testing.T) {
	report := &api.AIAnalysisReport{
		Model: "atlas-placeholder", Severity: "info",
		Summary: "Alert from alertmanager on unknown-host requires triage",
	}
	event := api.AlertEvent{
		Source: "alertmanager", Level: "critical", Host: "10.114.4.113",
		Message: "节点失活", Labels: api.StringMap{"alert_state": "recovered", "model": "H100"},
	}
	refreshPlaceholderAIReport(report, event)
	if report.Severity != "critical" || !strings.Contains(report.Summary, "10.114.4.113") {
		t.Fatalf("expected refreshed host/severity, got severity=%q summary=%q", report.Severity, report.Summary)
	}
	if len(report.Evidence) < 2 || report.Evidence[1] != "level=critical" {
		t.Fatalf("expected refreshed evidence, got %#v", report.Evidence)
	}
}

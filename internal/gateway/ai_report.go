package gateway

import (
	"fmt"
	"strings"

	"atlas/pkg/api"
)

func buildPlaceholderAIReport(event api.AlertEvent) map[string]interface{} {
	summary := fmt.Sprintf("Alert from %s on %s requires triage", fallbackString(event.Source, "unknown-source"), fallbackString(event.Host, "unknown-host"))
	probableCauses := api.StringList{
		"Source alert requires operator review",
		"Need to correlate alert labels, recent logs and host metrics",
	}
	recommendedActions := api.StringList{
		"Review labels and recent changes around the affected host or service",
		"Check related logs and metrics in the 15 minutes before the alert",
	}
	evidence := api.StringList{
		fmt.Sprintf("message=%s", event.Message),
		fmt.Sprintf("level=%s", event.Level),
	}
	confidence := 0.35

	lowerMessage := strings.ToLower(event.Message)
	alertName := strings.ToLower(firstNonEmpty(event.Labels["alertname"], event.Message))
	if strings.Contains(lowerMessage, "xid") || strings.Contains(alertName, "xid") || event.Labels["err_code"] != "" {
		summary = fmt.Sprintf("GPU XID alert detected on %s; prioritize hardware, driver and PCIe checks", fallbackString(event.Host, "unknown-host"))
		probableCauses = api.StringList{
			"GPU driver or hardware fault indicated by XID event",
			"PCIe, power or thermal instability affecting the GPU",
			"Workload-triggered GPU reset or compute stall",
		}
		recommendedActions = api.StringList{
			"Check XID error code, dmesg and recent GPU logs on the affected host",
			"Compare the affected GPU with peer GPUs on the same host",
			"Inspect temperature, power, PCIe and ECC related metrics before the alert",
		}
		evidence = append(evidence,
			fmt.Sprintf("err_code=%s", event.Labels["err_code"]),
			fmt.Sprintf("gpu=%s", event.Labels["gpu"]),
		)
		confidence = 0.72
	} else if strings.Contains(event.Message, "网络") || strings.Contains(lowerMessage, "network") || strings.Contains(lowerMessage, "blackbox") {
		target := firstNonEmpty(event.Labels["target"], event.Labels["instance"])
		summary = fmt.Sprintf("Network reachability alert detected; validate target path and probe host for %s", fallbackString(target, "target"))
		probableCauses = api.StringList{
			"Target endpoint may be unreachable from the probe host",
			"ICMP or external network path may be degraded",
			"blackbox probe host or exporter may be unavailable",
		}
		recommendedActions = api.StringList{
			"Verify reachability from the probe node to the target endpoint",
			"Check exporter and blackbox probe health on the affected instance",
			"Correlate with recent routing, DNS or firewall changes",
		}
		evidence = append(evidence,
			fmt.Sprintf("target=%s", event.Labels["target"]),
			fmt.Sprintf("job=%s", event.Labels["job"]),
		)
		confidence = 0.66
	}

	return map[string]interface{}{
		"status":              "completed",
		"model":               "atlas-placeholder",
		"prompt_version":      "v1-rule-draft",
		"summary":             summary,
		"probable_causes":     probableCauses,
		"recommended_actions": recommendedActions,
		"evidence":            evidence,
		"confidence":          confidence,
		"error_message":       "",
	}
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

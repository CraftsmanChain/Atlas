package inventory

import (
	"strings"

	"atlas/internal/prometheus"
)

type targetAssessment struct {
	reasonCode        string
	suppressed        bool
	suppressionReason string
}

func assessTarget(job, nodeIP, health, lastError string, targets map[string]prometheus.Target) targetAssessment {
	if strings.EqualFold(health, "up") {
		return targetAssessment{}
	}

	inbandUnreachable := !targetUp(targets, "node_exporter", nodeIP) &&
		!targetUp(targets, "dcgm_exporter", nodeIP) &&
		!targetUp(targets, "gpu_exporter", nodeIP)
	if inbandUnreachable && (job == "dcgm_exporter" || job == "gpu_exporter") {
		return targetAssessment{reasonCode: "node_inband_unreachable", suppressed: true, suppressionReason: "inband_targets_unreachable"}
	}

	if strings.EqualFold(health, "missing") {
		return targetAssessment{reasonCode: "target_missing"}
	}
	errorText := strings.ToLower(lastError)
	switch {
	case strings.Contains(errorText, "connection refused"):
		return targetAssessment{reasonCode: "connection_refused"}
	case strings.Contains(errorText, "context deadline exceeded"), strings.Contains(errorText, "timeout"), strings.Contains(errorText, "timed out"):
		return targetAssessment{reasonCode: "scrape_timeout"}
	case strings.Contains(errorText, "no route to host"), strings.Contains(errorText, "network is unreachable"), strings.Contains(errorText, "host is unreachable"):
		return targetAssessment{reasonCode: "network_unreachable"}
	case strings.Contains(errorText, "no such host"), strings.Contains(errorText, "server misbehaving"):
		return targetAssessment{reasonCode: "dns_error"}
	case strings.Contains(errorText, "tls"), strings.Contains(errorText, "certificate"):
		return targetAssessment{reasonCode: "tls_error"}
	case errorText != "":
		return targetAssessment{reasonCode: "scrape_error"}
	default:
		return targetAssessment{reasonCode: "exporter_down"}
	}
}

func targetUp(targets map[string]prometheus.Target, job, ip string) bool {
	return strings.EqualFold(targets[targetKey(job, ip)].Health, "up")
}

func assessmentValue(reason string, suppressed bool, suppressionReason string) string {
	return reason + "|" + strings.ToLower(strings.TrimSpace(stringBool(suppressed))) + "|" + suppressionReason
}

func stringBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

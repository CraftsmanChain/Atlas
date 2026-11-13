package assetaudit

import (
	"fmt"
	"strings"
)

// maxNotificationFindings bounds how many rows one card carries so a large
// mismatch set stays readable in the chat group. The full set is always kept in
// the run record.
const maxNotificationFindings = 40

// notificationTitle prefixes a card title with the deployment instance name so
// every Feishu card is identified by the live instance (Platform Config →
// Instance name) instead of a hard-coded product label.
func notificationTitle(instanceName, topic string) string {
	name := strings.TrimSpace(instanceName)
	if name == "" {
		name = "Atlas"
	}
	return name + " · " + topic
}

// deliveryNotification renders the delivered-node verification result for the
// Feishu group.
func deliveryNotification(instanceName string, summary DeliverySummary, findings []DeliveryFinding) (title, template, content string) {
	title = notificationTitle(instanceName, "资产核查 · 已交付节点核对")
	template = "green"
	differences := make([]DeliveryFinding, 0, len(findings))
	notices := make([]DeliveryFinding, 0, len(findings))
	for _, finding := range findings {
		if isNotice(finding.Issue) {
			notices = append(notices, finding)
			continue
		}
		differences = append(differences, finding)
	}
	if len(differences) > 0 {
		template = "orange"
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf(
		"**交付表行数**: %d\n**已交付节点**: %d\n**已排除（存储/网络/ODF）**: %d\n**一致**: %d\n**未录入资产**: %d\n**IP 不一致**: %d\n**SN 提示（已录入，仅提示）**: %d\n",
		summary.SheetRows, summary.DeliveredRows, summary.ExcludedRows, summary.Matched, summary.MissingAsset, summary.IPMismatch, summary.SNNotice,
	))
	builder.WriteString(fmt.Sprintf("**核查时间**: %s\n", summary.GeneratedAt.Format("2006-01-02 15:04:05")))
	if len(differences) == 0 && len(notices) == 0 {
		builder.WriteString("\n全部已交付节点与资产记录一致。")
		return title, template, builder.String()
	}
	// Informational serial notices are counted in the headline statistics only;
	// their rows stay on the Atlas page so the chat card never lists them.
	if len(differences) == 0 {
		builder.WriteString("\n全部已交付节点均已录入资产，无差异。")
	} else {
		builder.WriteString(fmt.Sprintf("\n**发现 %d 条差异**：\n", len(differences)))
		for index, finding := range differences {
			if index >= maxNotificationFindings {
				builder.WriteString(fmt.Sprintf("\n… 其余 %d 条请在 Atlas「GPU 资产 · 资产核查」页查看。\n", len(differences)-maxNotificationFindings))
				break
			}
			builder.WriteString(fmt.Sprintf("- [%s] %s\n", issueLabel(finding.Issue), describeFinding(finding)))
		}
	}
	return title, template, builder.String()
}

// scanNotification renders the live-IP discovery result for the Feishu group.
func scanNotification(instanceName string, summary ScanSummary, findings []ScanFinding) (title, template, content string) {
	title = notificationTitle(instanceName, "资产核查 · 存活 IP 扫描")
	template = "green"
	if len(findings) > 0 {
		template = "orange"
	}
	if summary.Truncated {
		template = "yellow"
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf(
		"**扫描网段**: %d\n**已登记资产 IP**: %d\n**交付表节点 IP**: %d\n**扫描地址数**: %d\n**ICMP 存活**: %d\n**22 端口不通**: %d\n**SSH 登录失败**: %d\n**已标记（不计数）**: %d\n**发现存活未登记 IP**: %d\n",
		summary.SubnetCount, summary.KnownAssetIPs, summary.DeliverySheetIPs, summary.ScannedTargets, summary.LiveCount, summary.Port22Closed, summary.SSHLoginFailed, summary.Marked, summary.Unregistered,
	))
	builder.WriteString(fmt.Sprintf("**扫描时间**: %s\n", summary.GeneratedAt.Format("2006-01-02 15:04:05")))
	if summary.Truncated {
		builder.WriteString("\n> 本次扫描达到地址上限，结果可能不完整。\n")
	}
	if len(findings) == 0 {
		builder.WriteString("\n未发现未录入资产的存活 IP。")
		return title, template, builder.String()
	}
	builder.WriteString(fmt.Sprintf("\n**发现 %d 个未录入资产的存活 IP**：\n", len(findings)))
	for index, finding := range findings {
		if index >= maxNotificationFindings {
			builder.WriteString(fmt.Sprintf("\n… 其余 %d 个请在 Atlas「GPU 资产 · 资产核查」页查看。\n", len(findings)-maxNotificationFindings))
			break
		}
		builder.WriteString(fmt.Sprintf("- %s（网段 %s）\n", finding.IPAddress, finding.Subnet))
	}
	return title, template, builder.String()
}

func issueLabel(issue string) string {
	switch issue {
	case IssueMissingAsset:
		return "未录入资产"
	case IssueIPMismatch:
		return "IP 不一致"
	case IssueSNNotice:
		return "SN 提示"
	default:
		return issue
	}
}

func describeFinding(finding DeliveryFinding) string {
	identity := firstValue(finding.Hostname, finding.IPAddress, "未知节点")
	parts := []string{fmt.Sprintf("%s（交付表第 %d 行，交付时间 %s）", identity, finding.Row, firstValue(finding.DeliveredAt, "—"))}
	if finding.Detail != "" {
		parts = append(parts, finding.Detail)
	}
	return strings.Join(parts, " · ")
}

func firstValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

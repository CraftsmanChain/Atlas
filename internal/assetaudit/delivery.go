package assetaudit

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
)

// deliveryDatePattern matches the "交付时间" cells that mark a row as delivered.
var deliveryDatePattern = regexp.MustCompile(`^\d{4}[/\-.]\d{1,2}[/\-.]\d{1,2}`)

// deliveryRow is one parsed row of the delivery sheet.
type deliveryRow struct {
	Index         int
	DeliveredAt   string
	Hostname      string
	SystemName    string
	IP            string
	Mask          int // prefix length; 0 means the sheet did not declare one
	HostSN        string
	NodeSN        string
	DeviceType    string
	Model         string
	Delivered     bool
	Excluded      bool
	ExcludeReason string
}

// storageNodeKeywords identify storage nodes by their system hostname. Storage
// nodes are inventoried outside the GPU asset registry, so they are out of
// scope for both the delivered-node check and the live-IP sweep.
var storageNodeKeywords = []string{"gpfs", "obs"}

// excludedDeviceTypes are the "类型" values Atlas does not track. The match is
// case-insensitive so "ODF"/"odf" are equivalent.
var excludedDeviceTypes = []string{"网络设备", "odf"}

// assetSourceMachine is the recorded-asset registry. The sibling "ops_host"
// source is the operations platform's own host inventory, whose addresses sit
// on the 10G management network (PXE/NTP/Kubernetes masters) rather than on the
// delivered in-band (25G) fabric, so it must never widen the sweep scope.
const assetSourceMachine = "asset_machine"

// exclusionReason reports why a delivered row is out of scope, or "" when the
// row must be checked.
func exclusionReason(systemName, deviceType string) string {
	lowerSystem := strings.ToLower(strings.TrimSpace(systemName))
	for _, keyword := range storageNodeKeywords {
		if strings.Contains(lowerSystem, keyword) {
			return "存储节点（系统主机名含 " + strings.ToUpper(keyword) + "）"
		}
	}
	lowerType := strings.ToLower(strings.TrimSpace(deviceType))
	for _, keyword := range excludedDeviceTypes {
		if strings.Contains(lowerType, keyword) {
			return "类型为 " + strings.ToUpper(keyword)
		}
	}
	return ""
}

type deliveryColumns struct {
	deliveredAt, hostname, systemName, deviceType, model, hostSN, nodeSN, osIP, mask int
}

// defaultDeliveryColumns mirrors the current "智元整体扩容建设项目-交付资料"
// layout and is only used for columns the header row does not name.
var defaultDeliveryColumns = deliveryColumns{deliveredAt: 2, hostname: 3, systemName: 4, deviceType: 6, model: 7, hostSN: 8, nodeSN: 9, osIP: 13, mask: 14}

func resolveDeliveryColumns(header []string) deliveryColumns {
	columns := defaultDeliveryColumns
	hostnameSet, systemSet := false, false
	for index, cell := range header {
		key := normalizeHeader(cell)
		switch {
		case key == "":
		case strings.Contains(key, "交付时间"):
			columns.deliveredAt = index
		case strings.Contains(key, "系统主机名"):
			columns.systemName, systemSet = index, true
		case strings.Contains(key, "主机名"):
			if !hostnameSet {
				columns.hostname, hostnameSet = index, true
			}
		case strings.Contains(key, "nodesn"):
			columns.nodeSN = index
		case strings.Contains(key, "主机sn") || key == "sn":
			columns.hostSN = index
		case strings.Contains(key, "掩码"):
			columns.mask = index
		case strings.Contains(key, "osip"):
			columns.osIP = index
		case strings.Contains(key, "型号"):
			columns.model = index
		case strings.Contains(key, "类型"):
			columns.deviceType = index
		}
	}
	_ = systemSet
	return columns
}

func normalizeHeader(value string) string {
	replacer := strings.NewReplacer(" ", "", "\t", "", "\u3000", "", "_", "", "(", "", ")", "", "（", "", "）", "")
	return strings.ToLower(replacer.Replace(strings.TrimSpace(value)))
}

func cellAt(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[index])
}

// isBlankRow reports whether a row carries no value at all. Feishu pads every
// read range up to the requested size with empty rows, so those trailing rows
// are not sheet data and must never inflate the reported row count.
func isBlankRow(row []string) bool {
	for _, cell := range row {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

// parseDeliveryRows turns raw sheet values into typed rows. Blank padding rows
// are dropped while Index keeps pointing at the sheet's own row number.
func parseDeliveryRows(rows [][]string) ([]deliveryRow, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("delivery sheet returned no rows")
	}
	columns := resolveDeliveryColumns(rows[0])
	parsed := make([]deliveryRow, 0, len(rows)-1)
	for index, row := range rows[1:] {
		if isBlankRow(row) {
			continue
		}
		deliveredAt := cellAt(row, columns.deliveredAt)
		ip := cellAt(row, columns.osIP)
		if net.ParseIP(ip) == nil {
			ip = ""
		}
		hostSN := cellAt(row, columns.hostSN)
		nodeSN := cellAt(row, columns.nodeSN)
		sn := hostSN
		if sn == "" {
			sn = nodeSN
		}
		hostname := cellAt(row, columns.hostname)
		if hostname == "" {
			hostname = cellAt(row, columns.systemName)
		}
		systemName := cellAt(row, columns.systemName)
		deviceType := cellAt(row, columns.deviceType)
		reason := exclusionReason(systemName, deviceType)
		parsed = append(parsed, deliveryRow{
			Index: index + 2, DeliveredAt: deliveredAt, Hostname: hostname, SystemName: systemName, IP: ip,
			Mask: parseMask(cellAt(row, columns.mask)), HostSN: hostSN, NodeSN: nodeSN,
			DeviceType: deviceType, Model: cellAt(row, columns.model),
			Delivered: deliveryDatePattern.MatchString(deliveredAt),
			Excluded:  reason != "", ExcludeReason: reason,
		})
	}
	return parsed, nil
}

var maskBlocks = map[string]int{
	"255.255.128.0": 17, "255.255.192.0": 18, "255.255.224.0": 19, "255.255.240.0": 20,
	"255.255.248.0": 21, "255.255.252.0": 22, "255.255.254.0": 23, "255.255.255.0": 24,
	"255.255.255.128": 25, "255.255.255.192": 26, "255.255.255.224": 27, "255.255.255.240": 28,
	"255.255.255.248": 29, "255.255.255.252": 30, "255.255.0.0": 16, "255.0.0.0": 8,
}

// parseMask accepts "24", "/24", "255.255.255.0" or an empty cell.
func parseMask(value string) int {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "/"))
	if value == "" {
		return 0
	}
	if prefix, err := strconv.Atoi(value); err == nil {
		if prefix >= 1 && prefix <= 32 {
			return prefix
		}
		return 0
	}
	if prefix, ok := maskBlocks[value]; ok {
		return prefix
	}
	return 0
}

// Delivery issue codes. IssueMissingAsset and IssueIPMismatch are real
// discrepancies and drive the alert; IssueSNNotice is informational only: the
// node is already recorded in the asset registry, so a serial-text difference
// (stale sheet, reformatted serial, telemetry identity drift) is reported for
// review but must not raise a difference or a notification.
const (
	IssueMissingAsset = "missing_asset"
	IssueIPMismatch   = "ip_mismatch"
	IssueSNNotice     = "sn_notice"
)

// isNotice reports whether an issue is informational rather than a difference.
func isNotice(issue string) bool { return issue == IssueSNNotice }

// DeliveryFinding is one delivered node that is missing from the asset registry
// or whose recorded address or serial disagrees with the sheet or telemetry.
type DeliveryFinding struct {
	Row         int    `json:"row"`
	Hostname    string `json:"hostname"`
	IPAddress   string `json:"ip_address"`
	Serial      string `json:"sn"`
	DeliveredAt string `json:"delivered_at"`
	Issue       string `json:"issue"`
	Detail      string `json:"detail"`
	AssetIP     string `json:"asset_ip,omitempty"`
	AssetSN     string `json:"asset_sn,omitempty"`
	TelemetrySN string `json:"telemetry_sn,omitempty"`
}

// DeliveryExclusion is one delivered row Atlas skipped because it is out of
// scope, so the page can list the rows behind the exclusion count.
type DeliveryExclusion struct {
	Row       int    `json:"row"`
	Hostname  string `json:"hostname"`
	IPAddress string `json:"ip_address"`
	Reason    string `json:"reason"`
}

// DeliverySummary is the headline result of one delivery check. MissingAsset,
// IPMismatch and Matched describe the real reconciliation; SNNotice is the
// informational tail that the operator can expand on the page.
type DeliverySummary struct {
	SheetRows     int                 `json:"sheet_rows"`
	DeliveredRows int                 `json:"delivered_rows"`
	ExcludedRows  int                 `json:"excluded_rows"`
	Excluded      []DeliveryExclusion `json:"excluded,omitempty"`
	Matched       int                 `json:"matched"`
	MissingAsset  int                 `json:"missing_asset"`
	IPMismatch    int                 `json:"ip_mismatch"`
	SNNotice      int                 `json:"sn_notice"`
	AssetIPCount  int                 `json:"asset_ip_count"`
	GeneratedAt   time.Time           `json:"generated_at"`
}

// deliveryDifferenceCount counts the findings that are real discrepancies, so
// an informational serial notice never triggers a notification on its own.
func deliveryDifferenceCount(findings []DeliveryFinding) int {
	count := 0
	for _, finding := range findings {
		if !isNotice(finding.Issue) {
			count++
		}
	}
	return count
}

func normalizeSerial(value string) string {
	trimmed := strings.TrimSpace(strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")[0])
	switch strings.ToUpper(trimmed) {
	case "", "-", "--", "N/A", "NA", "NULL", "UNKNOWN", "未知", "无":
		return ""
	}
	replacer := strings.NewReplacer(" ", "", "\t", "", "-", "")
	return strings.ToUpper(replacer.Replace(trimmed))
}

// runDeliveryCheck compares the delivered sheet rows with the recorded assets
// and telemetry identity.
func (s *Service) runDeliveryCheck(ctx context.Context, fetch configForFetch, now time.Time) (DeliverySummary, []DeliveryFinding, error) {
	rows, err := s.fetchDeliveryRows(ctx, fetch)
	if err != nil {
		return DeliverySummary{}, nil, err
	}
	parsed, err := parseDeliveryRows(rows)
	if err != nil {
		return DeliverySummary{}, nil, err
	}
	assets, nodes, err := s.loadIdentityIndex()
	if err != nil {
		return DeliverySummary{}, nil, err
	}
	summary := DeliverySummary{SheetRows: len(parsed), AssetIPCount: len(assets.byIP), GeneratedAt: now}
	findings := make([]DeliveryFinding, 0)
	for _, row := range parsed {
		if !row.Delivered {
			continue
		}
		summary.DeliveredRows++
		if row.Excluded {
			summary.ExcludedRows++
			summary.Excluded = append(summary.Excluded, DeliveryExclusion{
				Row: row.Index, Hostname: firstValue(row.Hostname, row.SystemName), IPAddress: row.IP, Reason: row.ExcludeReason,
			})
			continue
		}
		if row.IP == "" && row.HostSN == "" && row.NodeSN == "" {
			continue
		}
		issues := evaluateDeliveredRow(row, assets, nodes)
		differences := 0
		for _, issue := range issues {
			if !isNotice(issue.Issue) {
				differences++
			}
			switch issue.Issue {
			case IssueMissingAsset:
				summary.MissingAsset++
			case IssueIPMismatch:
				summary.IPMismatch++
			case IssueSNNotice:
				summary.SNNotice++
			}
			findings = append(findings, issue)
		}
		// A row whose only finding is an informational serial notice still
		// reconciles, so Matched counts every in-scope row without a difference.
		if differences == 0 {
			summary.Matched++
		}
	}
	return summary, findings, nil
}

func evaluateDeliveredRow(row deliveryRow, assets assetIndex, nodes map[string]api.GPUNode) []DeliveryFinding {
	base := DeliveryFinding{
		Row: row.Index, Hostname: row.Hostname, IPAddress: row.IP, Serial: row.HostSN,
		DeliveredAt: row.DeliveredAt,
	}
	if base.Serial == "" {
		base.Serial = row.NodeSN
	}
	sheetSN := normalizeSerial(base.Serial)

	asset, byIP := assets.match(row.IP, sheetSN)
	if asset == nil {
		missing := base
		missing.Issue = IssueMissingAsset
		missing.Detail = fmt.Sprintf("交付表第 %d 行（%s）在资产中未找到匹配记录", row.Index, describeRow(row))
		return []DeliveryFinding{missing}
	}
	base.AssetIP, base.AssetSN = asset.IPAddress, asset.SerialNumber

	// The node is already recorded, so a serial-text difference is reported as
	// an informational notice rather than a discrepancy.
	findings := make([]DeliveryFinding, 0, 2)
	if byIP {
		if sheetSN != "" && normalizeSerial(asset.SerialNumber) != "" && normalizeSerial(asset.SerialNumber) != sheetSN {
			issue := base
			issue.Issue = IssueSNNotice
			issue.Detail = fmt.Sprintf("资产 SN 为 %s，交付表 SN 为 %s", asset.SerialNumber, base.Serial)
			findings = append(findings, issue)
		}
	} else if asset.IPAddress != "" && row.IP != "" && asset.IPAddress != row.IP {
		issue := base
		issue.Issue = IssueIPMismatch
		issue.Detail = fmt.Sprintf("资产 IP 为 %s，交付表 OSIP 为 %s", asset.IPAddress, row.IP)
		findings = append(findings, issue)
	}
	if node, ok := nodes[row.IP]; ok && row.IP != "" {
		telemetry := normalizeSerial(node.AssetSerial)
		if telemetry != "" && sheetSN != "" && telemetry != sheetSN {
			issue := base
			issue.Issue = IssueSNNotice
			issue.TelemetrySN = node.AssetSerial
			issue.Detail = fmt.Sprintf("监控实测 SN 为 %s，交付表 SN 为 %s", node.AssetSerial, base.Serial)
			findings = append(findings, issue)
		}
	}
	return findings
}

func describeRow(row deliveryRow) string {
	parts := make([]string, 0, 3)
	if row.Hostname != "" {
		parts = append(parts, row.Hostname)
	}
	if row.IP != "" {
		parts = append(parts, row.IP)
	}
	if row.Model != "" {
		parts = append(parts, row.Model)
	}
	if len(parts) == 0 {
		return "无主机名/IP"
	}
	return strings.Join(parts, " · ")
}

type assetIndex struct {
	byIP map[string]api.InfrastructureAsset
	bySN map[string]api.InfrastructureAsset
}

func (index assetIndex) match(ip, serial string) (*api.InfrastructureAsset, bool) {
	if serial != "" {
		if asset, ok := index.bySN[serial]; ok {
			return &asset, false
		}
	}
	if ip != "" {
		if asset, ok := index.byIP[ip]; ok {
			return &asset, true
		}
	}
	return nil, false
}

func (s *Service) loadIdentityIndex() (assetIndex, map[string]api.GPUNode, error) {
	var assets []api.InfrastructureAsset
	if err := s.db.Where("present = ?", true).Find(&assets).Error; err != nil {
		return assetIndex{}, nil, err
	}
	index := assetIndex{byIP: make(map[string]api.InfrastructureAsset, len(assets)), bySN: make(map[string]api.InfrastructureAsset, len(assets))}
	for _, asset := range assets {
		if asset.IPAddress != "" {
			existing, ok := index.byIP[asset.IPAddress]
			if !ok || (!existing.InUse && asset.InUse) {
				index.byIP[asset.IPAddress] = asset
			}
		}
		if serial := normalizeSerial(asset.SerialNumber); serial != "" {
			if _, ok := index.bySN[serial]; !ok {
				index.bySN[serial] = asset
			}
		}
	}
	var nodes []api.GPUNode
	if err := s.db.Where("lifecycle <> ?", "retired").Find(&nodes).Error; err != nil {
		return index, nil, err
	}
	byNode := make(map[string]api.GPUNode, len(nodes))
	for _, node := range nodes {
		if node.NodeIP != "" {
			byNode[node.NodeIP] = node
		}
	}
	return index, byNode, nil
}

// assetOSIPs returns the in-band (OS) address of every recorded asset. Only
// these networks join the delivery sheet's OSIP column as sweep scope, so the
// out-of-band (BMC) and 10G management networks are never scanned. Records are
// limited to the recorded-asset registry: the operations host inventory would
// otherwise drag the platform's own 10G management hosts into scope.
func (s *Service) assetOSIPs() ([]string, error) {
	var assets []api.InfrastructureAsset
	if err := s.db.Where("present = ? AND source = ?", true, assetSourceMachine).Find(&assets).Error; err != nil {
		return nil, err
	}
	addresses := make([]string, 0, len(assets))
	for _, asset := range assets {
		if ip := strings.TrimSpace(asset.IPAddress); ip != "" {
			addresses = append(addresses, ip)
		}
	}
	return addresses, nil
}

// knownAssetIPs collects every address Atlas already knows about so the live-IP
// scan only reports genuinely unregistered hosts.
func (s *Service) knownAssetIPs() (map[string]struct{}, error) {
	known := make(map[string]struct{})
	var assets []api.InfrastructureAsset
	if err := s.db.Where("present = ?", true).Find(&assets).Error; err != nil {
		return nil, err
	}
	for _, asset := range assets {
		if ip := strings.TrimSpace(asset.IPAddress); ip != "" {
			known[ip] = struct{}{}
		}
	}
	var nodes []api.GPUNode
	if err := s.db.Where("lifecycle <> ?", "retired").Find(&nodes).Error; err != nil {
		return nil, err
	}
	for _, node := range nodes {
		if ip := strings.TrimSpace(node.NodeIP); ip != "" {
			known[ip] = struct{}{}
		}
		if ip := strings.TrimSpace(node.BMCIP); ip != "" {
			known[ip] = struct{}{}
		}
	}
	var targets []api.CollectorTarget
	if err := s.db.Find(&targets).Error; err != nil {
		return nil, err
	}
	for _, target := range targets {
		if ip := strings.TrimSpace(target.TargetIP); ip != "" {
			known[ip] = struct{}{}
		}
		if ip := strings.TrimSpace(target.NodeIP); ip != "" {
			known[ip] = struct{}{}
		}
	}
	return known, nil
}

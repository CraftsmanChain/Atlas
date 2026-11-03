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
	Index       int
	DeliveredAt string
	Hostname    string
	IP          string
	Mask        int // prefix length; 0 means the sheet did not declare one
	HostSN      string
	NodeSN      string
	DeviceType  string
	Model       string
	Delivered   bool
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

// parseDeliveryRows turns raw sheet values into typed rows.
func parseDeliveryRows(rows [][]string) ([]deliveryRow, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("delivery sheet returned no rows")
	}
	columns := resolveDeliveryColumns(rows[0])
	parsed := make([]deliveryRow, 0, len(rows)-1)
	for index, row := range rows[1:] {
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
		parsed = append(parsed, deliveryRow{
			Index: index + 2, DeliveredAt: deliveredAt, Hostname: hostname, IP: ip,
			Mask: parseMask(cellAt(row, columns.mask)), HostSN: hostSN, NodeSN: nodeSN,
			DeviceType: cellAt(row, columns.deviceType), Model: cellAt(row, columns.model),
			Delivered: deliveryDatePattern.MatchString(deliveredAt),
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

// DeliverySummary is the headline result of one delivery check.
type DeliverySummary struct {
	SheetRows     int       `json:"sheet_rows"`
	DeliveredRows int       `json:"delivered_rows"`
	Matched       int       `json:"matched"`
	MissingAsset  int       `json:"missing_asset"`
	IPMismatch    int       `json:"ip_mismatch"`
	SNMismatch    int       `json:"sn_mismatch"`
	AssetIPCount  int       `json:"asset_ip_count"`
	GeneratedAt   time.Time `json:"generated_at"`
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
		if row.IP == "" && row.HostSN == "" && row.NodeSN == "" {
			continue
		}
		issues := evaluateDeliveredRow(row, assets, nodes)
		if len(issues) == 0 {
			summary.Matched++
			continue
		}
		for _, issue := range issues {
			switch issue.Issue {
			case "missing_asset":
				summary.MissingAsset++
			case "ip_mismatch":
				summary.IPMismatch++
			case "sn_mismatch":
				summary.SNMismatch++
			}
			findings = append(findings, issue)
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
		missing.Issue = "missing_asset"
		missing.Detail = fmt.Sprintf("交付表第 %d 行（%s）在资产中未找到匹配记录", row.Index, describeRow(row))
		return []DeliveryFinding{missing}
	}
	base.AssetIP, base.AssetSN = asset.IPAddress, asset.SerialNumber

	findings := make([]DeliveryFinding, 0, 2)
	if byIP {
		if sheetSN != "" && normalizeSerial(asset.SerialNumber) != "" && normalizeSerial(asset.SerialNumber) != sheetSN {
			issue := base
			issue.Issue = "sn_mismatch"
			issue.Detail = fmt.Sprintf("资产 SN 为 %s，交付表 SN 为 %s", asset.SerialNumber, base.Serial)
			findings = append(findings, issue)
		}
	} else if asset.IPAddress != "" && row.IP != "" && asset.IPAddress != row.IP {
		issue := base
		issue.Issue = "ip_mismatch"
		issue.Detail = fmt.Sprintf("资产 IP 为 %s，交付表 OSIP 为 %s", asset.IPAddress, row.IP)
		findings = append(findings, issue)
	}
	if node, ok := nodes[row.IP]; ok && row.IP != "" {
		telemetry := normalizeSerial(node.AssetSerial)
		if telemetry != "" && sheetSN != "" && telemetry != sheetSN {
			issue := base
			issue.Issue = "sn_mismatch"
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

// assetIPs collects every address Atlas already knows about so the live-IP scan
// only reports genuinely unregistered hosts.
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

package prediction

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

const HardwareFaultFeedbackImportVersion = "hardware-fault-feedback-import-v1"

var (
	ledgerTicketPattern   = regexp.MustCompile(`GD\d{14}`)
	ledgerDatePattern     = regexp.MustCompile(`20\d{2}/\d{2}/\d{2}\s+\d{2}:\d{2}`)
	ledgerHostnamePattern = regexp.MustCompile(`(?i)(?:h100|h200|4090)gpu-\d+`)
	ledgerGPUIndexPattern = regexp.MustCompile(`(?i)gpu\s*([0-9]+)`)
)

type HardwareFaultFeedbackImportInput struct {
	SourceSystem string `json:"source_system"`
	Operator     string `json:"operator"`
	RawText      string `json:"raw_text"`
	Commit       bool   `json:"commit"`
}

type HardwareFaultFeedbackImportRecord struct {
	RowNumber             int      `json:"row_number"`
	SourceRecordID        string   `json:"source_record_id,omitempty"`
	SourceRowSHA256       string   `json:"source_row_sha256"`
	SourceHostSerial      string   `json:"source_host_serial,omitempty"`
	SourceHostname        string   `json:"source_hostname,omitempty"`
	SourceStatus          string   `json:"source_status,omitempty"`
	RawCategory           string   `json:"raw_category,omitempty"`
	RawPhenomenon         string   `json:"raw_phenomenon,omitempty"`
	ReportedAt            string   `json:"reported_at,omitempty"`
	ResolvedAt            string   `json:"resolved_at,omitempty"`
	NodeIP                string   `json:"node_ip,omitempty"`
	AssetResolutionStatus string   `json:"asset_resolution_status"`
	AssetResolutionNote   string   `json:"asset_resolution_note,omitempty"`
	TargetScope           string   `json:"target_scope,omitempty"`
	AffectedGPUIndexes    []string `json:"affected_gpu_indexes,omitempty"`
	FaultType             string   `json:"fault_type,omitempty"`
	Decision              string   `json:"decision"`
	BlockingReasons       []string `json:"blocking_reasons,omitempty"`
	ExistingRequestID     uint     `json:"existing_request_id,omitempty"`
	ImportedRequestID     uint     `json:"imported_request_id,omitempty"`
	RawRecord             string   `json:"-"`
}

type HardwareFaultFeedbackImportReport struct {
	Version              string                              `json:"version"`
	Mode                 string                              `json:"mode"`
	SourceSystem         string                              `json:"source_system"`
	SourceSHA256         string                              `json:"source_sha256"`
	ParsedRows           int                                 `json:"parsed_rows"`
	UniqueSourceRecords  int                                 `json:"unique_source_records"`
	MappedRows           int                                 `json:"mapped_rows"`
	UnresolvedRows       int                                 `json:"unresolved_rows"`
	DuplicateRows        int                                 `json:"duplicate_rows"`
	ExistingRows         int                                 `json:"existing_rows"`
	ImportableRows       int                                 `json:"importable_rows"`
	ImportedRows         int                                 `json:"imported_rows"`
	SkippedRows          int                                 `json:"skipped_rows"`
	TrainingEligibleRows int                                 `json:"training_eligible_rows"`
	Records              []HardwareFaultFeedbackImportRecord `json:"records"`
	GeneratedAt          time.Time                           `json:"generated_at"`
}

type ledgerFaultRow struct {
	serial     string
	status     string
	category   string
	phenomenon string
	ticket     string
	reportedAt string
	resolvedAt string
	hostname   string
	raw        string
}

func (s *Service) ImportHardwareFaultFeedback(input HardwareFaultFeedbackImportInput) (HardwareFaultFeedbackImportReport, error) {
	sourceSystem := strings.TrimSpace(input.SourceSystem)
	operator := strings.TrimSpace(input.Operator)
	if sourceSystem == "" {
		return HardwareFaultFeedbackImportReport{}, fmt.Errorf("source_system is required")
	}
	if operator == "" {
		return HardwareFaultFeedbackImportReport{}, fmt.Errorf("operator is required")
	}
	rows := parseHardwareFaultLedger(input.RawText)
	if len(rows) == 0 {
		return HardwareFaultFeedbackImportReport{}, fmt.Errorf("raw_text contains no recognizable hardware fault rows")
	}
	sourceSum := sha256.Sum256([]byte(strings.TrimSpace(strings.ReplaceAll(input.RawText, "\r\n", "\n"))))
	report := HardwareFaultFeedbackImportReport{
		Version: HardwareFaultFeedbackImportVersion, Mode: "preview", SourceSystem: sourceSystem,
		SourceSHA256: fmt.Sprintf("%x", sourceSum), Records: make([]HardwareFaultFeedbackImportRecord, 0, len(rows)), GeneratedAt: s.now(),
	}
	if input.Commit {
		report.Mode = "commit"
	}
	seen := map[string]int{}
	unique := map[string]bool{}
	for index, parsed := range rows {
		record := s.previewHardwareFaultLedgerRow(index+1, sourceSystem, parsed)
		if record.SourceRecordID != "" {
			unique[record.SourceRecordID] = true
			if firstRow, ok := seen[record.SourceRecordID]; ok {
				record.Decision = "duplicate_in_batch"
				record.BlockingReasons = append(record.BlockingReasons, fmt.Sprintf("source record already appeared at row %d", firstRow))
				report.DuplicateRows++
			} else {
				seen[record.SourceRecordID] = record.RowNumber
			}
		}
		if record.AssetResolutionStatus == "resolved" {
			report.MappedRows++
		} else {
			report.UnresolvedRows++
		}
		if record.ExistingRequestID > 0 {
			report.ExistingRows++
		}
		if record.Decision == "ready_to_import" {
			report.ImportableRows++
			if input.Commit {
				created, err := s.CreateHardwareFaultFeedback(importInputFromLedger(sourceSystem, operator, parsed, record))
				if err != nil {
					record.Decision = "import_failed"
					record.BlockingReasons = append(record.BlockingReasons, err.Error())
				} else {
					record.Decision = "imported_pending_monitoring_confirmation"
					record.ImportedRequestID = created.ID
					report.ImportedRows++
				}
			}
		}
		if record.Decision != "ready_to_import" && record.Decision != "imported_pending_monitoring_confirmation" {
			report.SkippedRows++
		}
		report.Records = append(report.Records, record)
	}
	report.ParsedRows = len(rows)
	report.UniqueSourceRecords = len(unique)
	return report, nil
}

func parseHardwareFaultLedger(raw string) []ledgerFaultRow {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	logical := make([]string, 0)
	current := ""
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		startsRow := len(fields) >= 8 && strings.TrimSpace(fields[0]) != "" && strings.TrimSpace(fields[6]) == "硬件" && isLedgerStatus(fields[4])
		if startsRow {
			if strings.TrimSpace(current) != "" {
				logical = append(logical, current)
			}
			current = line
			continue
		}
		if current != "" {
			current += "\n" + line
		}
	}
	if strings.TrimSpace(current) != "" {
		logical = append(logical, current)
	}
	rows := make([]ledgerFaultRow, 0, len(logical))
	for _, item := range logical {
		firstLine := strings.SplitN(item, "\n", 2)[0]
		fields := strings.Split(firstLine, "\t")
		if len(fields) < 8 {
			continue
		}
		ticket := ledgerTicketPattern.FindString(item)
		dates := ledgerDatePattern.FindAllString(item, -1)
		reportedAt := ""
		resolvedAt := ""
		if len(dates) > 0 {
			reportedAt = dates[0]
		}
		if len(dates) > 1 {
			resolvedAt = dates[1]
		}
		hostname := ledgerHostnamePattern.FindString(item)
		rows = append(rows, ledgerFaultRow{
			serial: strings.TrimSpace(fields[0]), status: strings.TrimSpace(fields[4]), category: strings.TrimSpace(fields[5]),
			phenomenon: strings.TrimSpace(fields[7]), ticket: ticket, reportedAt: reportedAt, resolvedAt: resolvedAt, hostname: strings.ToLower(hostname), raw: strings.TrimSpace(item),
		})
	}
	return rows
}

func isLedgerStatus(value string) bool {
	switch strings.TrimSpace(value) {
	case "工程处理完单", "厂商处理完单", "工程师处理中", "等待备件":
		return true
	default:
		return false
	}
}

func (s *Service) previewHardwareFaultLedgerRow(rowNumber int, sourceSystem string, row ledgerFaultRow) HardwareFaultFeedbackImportRecord {
	rowSum := sha256.Sum256([]byte(row.raw))
	record := HardwareFaultFeedbackImportRecord{
		RowNumber: rowNumber, SourceRecordID: row.ticket, SourceRowSHA256: fmt.Sprintf("%x", rowSum), SourceHostSerial: row.serial,
		SourceHostname: row.hostname, SourceStatus: row.status, RawCategory: row.category, RawPhenomenon: row.phenomenon,
		ReportedAt: row.reportedAt, ResolvedAt: row.resolvedAt, Decision: "ready_to_import", RawRecord: row.raw,
	}
	if row.ticket == "" {
		record.BlockingReasons = append(record.BlockingReasons, "source ticket id is missing")
	}
	if _, err := parseFeedbackTime(row.reportedAt); err != nil {
		record.BlockingReasons = append(record.BlockingReasons, "reported fault time is missing or invalid")
	}
	record.NodeIP, record.AssetResolutionStatus, record.AssetResolutionNote = s.resolveLedgerNode(row.serial, row.hostname)
	if record.AssetResolutionStatus != "resolved" {
		record.BlockingReasons = append(record.BlockingReasons, "asset serial/hostname could not be mapped to one current node")
	}
	record.TargetScope, record.FaultType, record.AffectedGPUIndexes = classifyLedgerFault(row.category, row.phenomenon)
	var existing api.HardwareFaultFeedbackRequest
	if row.ticket != "" {
		if err := s.db.Where("source_system = ? AND source_record_id = ?", sourceSystem, row.ticket).First(&existing).Error; err == nil {
			record.ExistingRequestID = existing.ID
			record.Decision = "already_imported"
			if strings.TrimSpace(existing.SourceRowSHA256) != "" && existing.SourceRowSHA256 != record.SourceRowSHA256 {
				record.Decision = "already_imported_source_changed"
				record.BlockingReasons = append(record.BlockingReasons, "source ticket already exists but the pasted row content changed; review before updating provenance")
			}
		} else if err != gorm.ErrRecordNotFound {
			record.BlockingReasons = append(record.BlockingReasons, "existing source lookup failed")
		}
	}
	if len(record.BlockingReasons) > 0 && record.Decision == "ready_to_import" {
		record.Decision = "blocked"
	}
	return record
}

func (s *Service) resolveLedgerNode(serial, hostname string) (string, string, string) {
	serialIPs := s.ledgerNodeIPs("asset_serial", serial)
	if len(serialIPs) == 0 && strings.TrimSpace(serial) != "" {
		var gpuAssets []api.GPUAsset
		if err := s.db.Where("LOWER(host_serial) = LOWER(?)", strings.TrimSpace(serial)).Where("current_node_identity = ?", true).Find(&gpuAssets).Error; err == nil {
			for _, asset := range gpuAssets {
				if strings.TrimSpace(asset.NodeIP) != "" {
					serialIPs = append(serialIPs, strings.TrimSpace(asset.NodeIP))
				}
			}
			serialIPs = uniqueStrings(serialIPs)
		}
	}
	if len(serialIPs) == 0 && strings.TrimSpace(serial) != "" {
		var assets []api.InfrastructureAsset
		if err := s.db.Where("LOWER(serial_number) = LOWER(?)", strings.TrimSpace(serial)).Find(&assets).Error; err == nil {
			for _, asset := range assets {
				if strings.TrimSpace(asset.IPAddress) != "" {
					serialIPs = append(serialIPs, strings.TrimSpace(asset.IPAddress))
				}
			}
			serialIPs = uniqueStrings(serialIPs)
		}
	}
	hostIPs := s.ledgerNodeIPs("hostname", hostname)
	if len(serialIPs) > 1 || len(hostIPs) > 1 {
		return "", "ambiguous", fmt.Sprintf("serial candidates=%s hostname candidates=%s", strings.Join(serialIPs, ","), strings.Join(hostIPs, ","))
	}
	if len(serialIPs) == 1 && len(hostIPs) == 1 && serialIPs[0] != hostIPs[0] {
		return "", "conflict", fmt.Sprintf("serial maps to %s but hostname maps to %s", serialIPs[0], hostIPs[0])
	}
	if len(serialIPs) == 1 {
		note := "matched current node asset serial"
		if len(hostIPs) == 1 {
			note = "asset serial and hostname agree"
		}
		return serialIPs[0], "resolved", note
	}
	if len(hostIPs) == 1 {
		return hostIPs[0], "resolved", "matched current node hostname"
	}
	return "", "unresolved", "no current node matched source serial or hostname"
}

func (s *Service) ledgerNodeIPs(column, value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var nodes []api.GPUNode
	if err := s.db.Where(fmt.Sprintf("LOWER(%s) = LOWER(?)", column), strings.TrimSpace(value)).Where("lifecycle = '' OR lifecycle <> ?", "retired").Find(&nodes).Error; err != nil {
		return nil
	}
	values := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if strings.TrimSpace(node.NodeIP) != "" {
			values = append(values, strings.TrimSpace(node.NodeIP))
		}
	}
	return uniqueStrings(values)
}

func classifyLedgerFault(category, phenomenon string) (string, string, []string) {
	lower := strings.ToLower(phenomenon)
	gpuRelated := strings.EqualFold(strings.TrimSpace(category), "GPU") || containsAny(lower, "xid", "gpu", "pcie", "ecc", "掉卡", "显卡")
	targetScope := "node"
	indexes := []string{}
	if gpuRelated {
		targetScope = "multi_gpu"
		matches := ledgerGPUIndexPattern.FindAllStringSubmatch(lower, -1)
		for _, match := range matches {
			if len(match) == 2 {
				if _, err := strconv.Atoi(match[1]); err == nil {
					indexes = append(indexes, match[1])
				}
			}
		}
		indexes = uniqueStrings(indexes)
		if len(indexes) == 1 {
			targetScope = "gpu"
		}
	}
	faultType := "node_hardware_fault"
	switch {
	case containsAny(lower, "ecc", "显存"):
		faultType = "gpu_memory_failure"
	case containsAny(lower, "pcie", "掉卡"):
		faultType = "pcie_link_failure"
	case gpuRelated:
		faultType = "gpu_hardware_failure"
	case strings.TrimSpace(category) == "电源" || strings.TrimSpace(category) == "风扇":
		faultType = "thermal_power_failure"
	}
	return targetScope, faultType, indexes
}

func importInputFromLedger(sourceSystem, operator string, row ledgerFaultRow, preview HardwareFaultFeedbackImportRecord) HardwareFaultFeedbackInput {
	reported, _ := parseFeedbackTime(row.reportedAt)
	gpuIndex := -1
	if len(preview.AffectedGPUIndexes) == 1 {
		gpuIndex, _ = strconv.Atoi(preview.AffectedGPUIndexes[0])
	}
	repairAction := "pending"
	if containsAny(row.raw, "拔插", "静置", "重启") {
		repairAction = "reseat"
	}
	if containsAny(row.raw, "并无硬件异常", "暂未发现问题", "无掉卡") {
		repairAction = "no_hardware_fault"
	}
	hardwareReplaced := preview.TargetScope != "node" && containsAny(strings.ToLower(row.raw), "更换gpu", "更换 gpu", "更换显卡", "gpu单卡更换")
	if hardwareReplaced {
		repairAction = "replace_gpu"
	}
	return HardwareFaultFeedbackInput{
		NodeIP: preview.NodeIP, TargetScope: preview.TargetScope, GPUIndex: gpuIndex, AffectedGPUIndexes: preview.AffectedGPUIndexes,
		FaultType: preview.FaultType, FaultOccurredAt: reported.Format("2006-01-02T15:04"), FaultTimePrecision: "window",
		FaultWindowStartAt: reported.Add(-6 * time.Hour).Format("2006-01-02T15:04"), FaultWindowEndAt: reported.Add(6 * time.Hour).Format("2006-01-02T15:04"),
		PreWindowHours: 24 * 7, PostWindowHours: 24, Operator: operator, Description: row.phenomenon,
		RepairAction: repairAction, HardwareReplaced: hardwareReplaced, EvidenceNote: "reported time is a reference; monitoring confirmation is required",
		SourceSystem: sourceSystem, SourceRecordID: row.ticket, SourceRowSHA256: preview.SourceRowSHA256, SourceHostSerial: row.serial,
		SourceHostname: row.hostname, SourceStatus: row.status, SourceRawRecord: row.raw, AssetResolutionStatus: preview.AssetResolutionStatus,
		SourceReportedAt: row.reportedAt, SourceResolvedAt: row.resolvedAt,
		AssetResolutionNote: preview.AssetResolutionNote, TriageStatus: "pending_monitoring_confirmation", EpisodeKey: "source-ticket:" + row.ticket,
		TrainingEligible: false,
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

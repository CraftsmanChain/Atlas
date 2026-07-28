package inventory

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
)

const (
	reconcileBoth        = "both"
	reconcileOpsOnly     = "ops_only"
	reconcileMachineOnly = "asset_only"
)

var gpuTypePattern = regexp.MustCompile(`(?i)^(?:rtx[\s_-]*)?(?:4090|[ahbvltp]\d{2,4}[a-z]*)$`)

type sourceAssetView struct {
	AssetKey string `json:"asset_key"`
	IP       string `json:"ip_address"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Model    string `json:"model"`
	State    string `json:"state"`
	SN       string `json:"sn"`
	InUse    bool   `json:"in_use"`
}

type reconciliationRow struct {
	Key          string           `json:"key"`
	Scope        string           `json:"scope"`
	Category     string           `json:"category"`
	Type         string           `json:"type"`
	GPUModel     string           `json:"gpu_model,omitempty"`
	IPAddress    string           `json:"ip_address"`
	Name         string           `json:"name"`
	SerialNumber string           `json:"sn"`
	OpsHost      *sourceAssetView `json:"ops_host,omitempty"`
	AssetMachine *sourceAssetView `json:"asset_machine,omitempty"`
}

type namedCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type reconciliationSummary struct {
	Total       int            `json:"total"`
	ByScope     map[string]int `json:"by_scope"`
	ByCategory  []namedCount   `json:"by_category"`
	ByType      []namedCount   `json:"by_type"`
	GPUModels   []namedCount   `json:"gpu_models"`
	GeneratedAt time.Time      `json:"generated_at"`
}

func (h *Handler) HandleReconciliation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	var assets []api.InfrastructureAsset
	if err := h.db.Where("present = ?", true).Order("source ASC, id ASC").Find(&assets).Error; err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows := reconcileAssets(assets)
	summary := summarizeReconciliation(rows)
	filtered := filterReconciliation(rows, r)
	limit, offset := pagination(r, 100, 1000)
	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":    filtered[offset:end],
		"summary": summary,
		"meta":    map[string]any{"total": len(filtered), "limit": limit, "offset": offset, "generated_at": time.Now().Format(time.RFC3339)},
	})
}

func reconcileAssets(assets []api.InfrastructureAsset) []reconciliationRow {
	ops, machines := make([]api.InfrastructureAsset, 0), make([]api.InfrastructureAsset, 0)
	for _, asset := range assets {
		switch asset.Source {
		case "ops_host":
			ops = append(ops, asset)
		case "asset_machine":
			machines = append(machines, asset)
		}
	}
	machineBySN, machineByIP := make(map[string][]int), make(map[string][]int)
	for index, asset := range machines {
		if value := normalizedSN(asset.SerialNumber); value != "" {
			machineBySN[value] = append(machineBySN[value], index)
		}
		if value := normalizedIP(asset.IPAddress); value != "" {
			machineByIP[value] = append(machineByIP[value], index)
		}
	}
	used := make(map[int]bool)
	rows := make([]reconciliationRow, 0, len(ops)+len(machines))
	for _, asset := range ops {
		match := firstUnused(machineBySN[normalizedSN(asset.SerialNumber)], used)
		if match < 0 {
			match = firstUnused(machineByIP[normalizedIP(asset.IPAddress)], used)
		}
		if match >= 0 {
			used[match] = true
			rows = append(rows, buildReconciliationRow(&asset, &machines[match]))
		} else {
			rows = append(rows, buildReconciliationRow(&asset, nil))
		}
	}
	for index := range machines {
		if !used[index] {
			rows = append(rows, buildReconciliationRow(nil, &machines[index]))
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if scopeOrder(rows[i].Scope) != scopeOrder(rows[j].Scope) {
			return scopeOrder(rows[i].Scope) < scopeOrder(rows[j].Scope)
		}
		if rows[i].Category != rows[j].Category {
			return rows[i].Category < rows[j].Category
		}
		return compareAssetIdentity(rows[i], rows[j])
	})
	return rows
}

func buildReconciliationRow(ops, machine *api.InfrastructureAsset) reconciliationRow {
	scope := reconcileBoth
	switch {
	case ops == nil:
		scope = reconcileMachineOnly
	case machine == nil:
		scope = reconcileOpsOnly
	}
	authoritative := machine
	if authoritative == nil {
		authoritative = ops
	}
	category, assetType, gpuModel := assetCategory(*authoritative)
	row := reconciliationRow{
		Scope: scope, Category: category, Type: assetType, GPUModel: gpuModel,
		IPAddress: authoritative.IPAddress, Name: authoritative.Name, SerialNumber: authoritative.SerialNumber,
	}
	if row.IPAddress == "" && ops != nil {
		row.IPAddress = ops.IPAddress
	}
	if row.Name == "" && ops != nil {
		row.Name = ops.Name
	}
	if row.SerialNumber == "" && ops != nil {
		row.SerialNumber = ops.SerialNumber
	}
	if ops != nil {
		view := toSourceAssetView(*ops)
		row.OpsHost = &view
	}
	if machine != nil {
		view := toSourceAssetView(*machine)
		row.AssetMachine = &view
	}
	row.Key = stableReconciliationKey(row)
	return row
}

func assetCategory(asset api.InfrastructureAsset) (category, assetType, gpuModel string) {
	assetType = strings.TrimSpace(asset.Type)
	normalized := strings.ToLower(assetType)
	switch normalized {
	case "交换机":
		return "network", assetType, ""
	case "ufm":
		return "firewall", assetType, ""
	case "腾讯云":
		return "cloud", assetType, ""
	case "监控运维":
		return "operations", assetType, ""
	case "存储":
		return "storage", assetType, ""
	}
	if gpuTypePattern.MatchString(assetType) {
		return "gpu", assetType, strings.ToUpper(strings.ReplaceAll(assetType, " ", ""))
	}
	if asset.EntityKind == "gpu_node" {
		model := strings.TrimSpace(asset.Model)
		if gpuTypePattern.MatchString(model) {
			return "gpu", defaultString(assetType, model), strings.ToUpper(strings.ReplaceAll(model, " ", ""))
		}
	}
	return "other", defaultString(assetType, "UNKNOWN"), ""
}

func summarizeReconciliation(rows []reconciliationRow) reconciliationSummary {
	scopeCounts := map[string]int{reconcileBoth: 0, reconcileOpsOnly: 0, reconcileMachineOnly: 0}
	categoryCounts, typeCounts, modelCounts := make(map[string]int), make(map[string]int), make(map[string]int)
	for _, row := range rows {
		scopeCounts[row.Scope]++
		categoryCounts[row.Category]++
		typeCounts[row.Type]++
		if row.GPUModel != "" {
			modelCounts[row.GPUModel]++
		}
	}
	return reconciliationSummary{
		Total: len(rows), ByScope: scopeCounts,
		ByCategory: sortedCounts(categoryCounts), ByType: sortedCounts(typeCounts),
		GPUModels: sortedCounts(modelCounts), GeneratedAt: time.Now(),
	}
}

func filterReconciliation(rows []reconciliationRow, r *http.Request) []reconciliationRow {
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	assetType := strings.TrimSpace(r.URL.Query().Get("type"))
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	result := make([]reconciliationRow, 0, len(rows))
	for _, row := range rows {
		if scope != "" && row.Scope != scope {
			continue
		}
		if category != "" && row.Category != category {
			continue
		}
		if assetType != "" && !strings.EqualFold(row.Type, assetType) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(strings.Join([]string{row.IPAddress, row.Name, row.SerialNumber, row.Type, row.GPUModel}, " ")), query) {
			continue
		}
		result = append(result, row)
	}
	return result
}

func sortedCounts(values map[string]int) []namedCount {
	result := make([]namedCount, 0, len(values))
	for name, count := range values {
		result = append(result, namedCount{Name: name, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func toSourceAssetView(asset api.InfrastructureAsset) sourceAssetView {
	return sourceAssetView{
		AssetKey: asset.AssetKey, IP: asset.IPAddress, Name: asset.Name, Type: asset.Type,
		Model: asset.Model, State: asset.State, SN: asset.SerialNumber, InUse: asset.InUse,
	}
}

func normalizedSN(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	switch value {
	case "", "-", "N/A", "NA", "UNKNOWN", "未知":
		return ""
	default:
		return value
	}
}

func normalizedIP(value string) string { return strings.TrimSpace(value) }

func firstUnused(indexes []int, used map[int]bool) int {
	for _, index := range indexes {
		if !used[index] {
			return index
		}
	}
	return -1
}

func scopeOrder(scope string) int {
	switch scope {
	case reconcileOpsOnly:
		return 0
	case reconcileMachineOnly:
		return 1
	default:
		return 2
	}
}

func compareAssetIdentity(left, right reconciliationRow) bool {
	if left.IPAddress != right.IPAddress {
		return left.IPAddress < right.IPAddress
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	return left.SerialNumber < right.SerialNumber
}

func stableReconciliationKey(row reconciliationRow) string {
	if row.OpsHost != nil && row.AssetMachine != nil {
		return "both:" + row.OpsHost.AssetKey + "|" + row.AssetMachine.AssetKey
	}
	if row.AssetMachine != nil {
		return "asset:" + row.AssetMachine.AssetKey
	}
	return "ops:" + row.OpsHost.AssetKey
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

package features

import (
	"errors"
	"fmt"
	"strings"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const CatalogVersion = "1.0.0"

type MetricSpec struct {
	Key   string
	Query string
}

type ListOptions struct {
	Domain  string
	Status  string
	Purpose string
}

func Builtins() []api.FeatureDefinition {
	definitions := []api.FeatureDefinition{
		metric("gpu_temp", "thermal", "DCGM_FI_DEV_GPU_TEMP", "instant", "GPU temperature", "GPU 温度"),
		metric("gpu_temp_max_15m", "thermal", "max_over_time(DCGM_FI_DEV_GPU_TEMP[15m])", "15m", "GPU temperature maximum", "GPU 温度最大值"),
		metric("memory_temp", "thermal", "DCGM_FI_DEV_MEMORY_TEMP", "instant", "Memory temperature", "显存温度"),
		metric("memory_temp_max_15m", "thermal", "max_over_time(DCGM_FI_DEV_MEMORY_TEMP[15m])", "15m", "Memory temperature maximum", "显存温度最大值"),
		metric("power_usage", "power", "DCGM_FI_DEV_POWER_USAGE", "instant", "Power usage", "GPU 功耗"),
		metric("gpu_util", "performance", "DCGM_FI_DEV_GPU_UTIL", "instant", "GPU utilization", "GPU 利用率"),
		metric("gpu_util_avg_15m", "performance", "avg_over_time(DCGM_FI_DEV_GPU_UTIL[15m])", "15m", "GPU utilization average", "GPU 利用率均值"),
		metric("mem_copy_util", "performance", "DCGM_FI_DEV_MEM_COPY_UTIL", "instant", "Memory copy utilization", "显存复制利用率"),
		metric("sm_clock", "performance", "DCGM_FI_DEV_SM_CLOCK", "instant", "SM clock", "SM 时钟"),
		metric("sm_clock_avg_15m", "degradation", "avg_over_time(DCGM_FI_DEV_SM_CLOCK[15m])", "15m", "SM clock average", "SM 时钟均值"),
		metric("mem_clock", "performance", "DCGM_FI_DEV_MEM_CLOCK", "instant", "Memory clock", "显存时钟"),
		metric("fb_used", "memory", "DCGM_FI_DEV_FB_USED", "instant", "Framebuffer used", "已用显存"),
		metric("fb_free", "memory", "DCGM_FI_DEV_FB_FREE", "instant", "Framebuffer free", "空闲显存"),
		metric("xid_current", "stability", "DCGM_FI_DEV_XID_ERRORS", "instant", "Current XID code", "当前 XID 代码"),
		metric("xid_changes_24h", "stability", "changes(DCGM_FI_DEV_XID_ERRORS[24h])", "24h", "XID changes", "XID 变化次数"),
		metricForDatacenter("uncorrectable_remapped_rows", "memory", "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", "instant", "Uncorrectable remapped rows", "不可纠正重映射行"),
		metricForDatacenter("correctable_remapped_rows", "memory", "DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS", "instant", "Correctable remapped rows", "可纠正重映射行"),
		metricForDatacenter("row_remap_failure", "memory", "DCGM_FI_DEV_ROW_REMAP_FAILURE", "instant", "Row remap failure", "行重映射失败"),
		metric("pcie_replay_counter", "interconnect", "DCGM_FI_DEV_PCIE_REPLAY_COUNTER", "instant", "PCIe replay counter", "PCIe 重放计数"),
		metric("pcie_replay_increase_1h", "interconnect", "increase(DCGM_FI_DEV_PCIE_REPLAY_COUNTER[1h])", "1h", "PCIe replay increase", "PCIe 重放增量"),
	}
	return definitions
}

func metric(name, domain, query, window, en, zh string) api.FeatureDefinition {
	return api.FeatureDefinition{
		Name: name, Version: CatalogVersion, DisplayNameZH: zh, DisplayNameEN: en,
		Domain: domain, EntityType: "gpu", Granularity: "gpu_uuid", SourceType: "prometheus",
		SourceReference: query, Computation: "promql:" + query, TimeSemantics: "event_time",
		Window: window, FreshnessSLASeconds: 900, MissingStrategy: "unknown_not_zero",
		QualityStatus: "validated", SupportedModels: api.StringList{"*"},
		Purposes: api.StringList{"health", "anomaly", "risk_ranking", "prediction", "degradation"},
		Lineage:  api.StringList{"prometheus", "dcgm_exporter"}, Owner: "atlas-health", Status: "active",
	}
}

func metricForDatacenter(name, domain, query, window, en, zh string) api.FeatureDefinition {
	definition := metric(name, domain, query, window, en, zh)
	definition.SupportedModels = api.StringList{"H100", "H200"}
	definition.MissingStrategy = "not_supported_or_not_collected"
	return definition
}

func HealthMetricSpecs() []MetricSpec {
	definitions := Builtins()
	result := make([]MetricSpec, 0, len(definitions))
	for _, definition := range definitions {
		if definition.SourceType == "prometheus" && contains(definition.Purposes, "health") {
			result = append(result, MetricSpec{Key: definition.Name, Query: definition.SourceReference})
		}
	}
	return result
}

func HealthFeatureVersions() api.StringMap {
	result := api.StringMap{}
	for _, definition := range Builtins() {
		if contains(definition.Purposes, "health") {
			result[definition.Name] = definition.Version
		}
	}
	return result
}

func ExpectedHealthKeys(model string) []string {
	model = strings.ToLower(model)
	result := []string{}
	for _, definition := range Builtins() {
		if !contains(definition.Purposes, "health") || !supportsModel(definition.SupportedModels, model) {
			continue
		}
		// Consumer compatibility: 4090 exporters do not expose memory
		// temperature even though other generic DCGM metrics are supported.
		if strings.Contains(model, "4090") && strings.HasPrefix(definition.Name, "memory_temp") {
			continue
		}
		result = append(result, definition.Name)
	}
	return result
}

func supportsModel(supported api.StringList, model string) bool {
	for _, candidate := range supported {
		if candidate == "*" || strings.Contains(model, strings.ToLower(candidate)) {
			return true
		}
	}
	return false
}

func contains(values api.StringList, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func Validate(definition *api.FeatureDefinition) error {
	if definition == nil {
		return errors.New("definition is required")
	}
	required := map[string]string{
		"name": definition.Name, "version": definition.Version, "domain": definition.Domain,
		"entity_type": definition.EntityType, "granularity": definition.Granularity,
		"source_type": definition.SourceType, "source_reference": definition.SourceReference,
		"computation": definition.Computation, "time_semantics": definition.TimeSemantics,
		"missing_strategy": definition.MissingStrategy, "quality_status": definition.QualityStatus,
		"owner": definition.Owner, "status": definition.Status,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	if definition.FreshnessSLASeconds <= 0 {
		return errors.New("freshness_sla_seconds must be positive")
	}
	if len(definition.Purposes) == 0 {
		return errors.New("purposes is required")
	}
	if len(definition.SupportedModels) == 0 {
		return errors.New("supported_models is required")
	}
	if len(definition.Lineage) == 0 {
		return errors.New("lineage is required")
	}
	if !oneOf(definition.Status, "draft", "shadow", "active", "deprecated") {
		return errors.New("invalid status")
	}
	if !oneOf(definition.QualityStatus, "draft", "experimental", "validated", "degraded") {
		return errors.New("invalid quality_status")
	}
	if !oneOf(definition.TimeSemantics, "event_time", "processing_time", "interval_end") {
		return errors.New("invalid time_semantics")
	}
	for _, purpose := range definition.Purposes {
		if !oneOf(purpose, "health", "detection", "anomaly", "risk_ranking", "prediction", "degradation", "explanation") {
			return fmt.Errorf("invalid purpose %q", purpose)
		}
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func Register(db *storage.DB, definition *api.FeatureDefinition) error {
	if err := Validate(definition); err != nil {
		return err
	}
	return db.Create(definition).Error
}

func SeedBuiltins(db *storage.DB) error {
	for _, definition := range Builtins() {
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&definition).Error; err != nil {
			return err
		}
	}
	return nil
}

func List(db *storage.DB, options ListOptions) ([]api.FeatureDefinition, error) {
	query := db.Model(&api.FeatureDefinition{})
	if value := strings.TrimSpace(options.Domain); value != "" {
		query = query.Where("domain = ?", value)
	}
	if value := strings.TrimSpace(options.Status); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := strings.TrimSpace(options.Purpose); value != "" {
		query = query.Where("purposes LIKE ?", "%\""+value+"\"%")
	}
	var definitions []api.FeatureDefinition
	err := query.Order("domain ASC, name ASC, version DESC").Find(&definitions).Error
	return definitions, err
}

func Get(db *storage.DB, name, version string) (*api.FeatureDefinition, error) {
	query := db.Where("name = ?", name)
	if version != "" {
		query = query.Where("version = ?", version)
	}
	var definition api.FeatureDefinition
	if err := query.Order("id DESC").First(&definition).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &definition, nil
}

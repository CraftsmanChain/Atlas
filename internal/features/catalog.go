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

const CatalogVersion = "1.4.0"

type MetricSpec struct {
	Key      string
	Query    string
	Source   string
	Priority int
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
		metricForDatacenter("correctable_remapped_rows_delta_1h", "memory", "clamp_min(delta(DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS[1h]), 0)", "1h", "Correctable remapped rows increase", "可纠正重映射行一小时增量"),
		metricForDatacenter("correctable_remapped_rows_delta_24h", "memory", "clamp_min(delta(DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS[24h]), 0)", "24h", "Correctable remapped rows increase", "可纠正重映射行二十四小时增量"),
		metricForDatacenter("row_remap_failure", "memory", "DCGM_FI_DEV_ROW_REMAP_FAILURE", "instant", "Row remap failure", "行重映射失败"),
		metric("pcie_replay_counter", "interconnect", "DCGM_FI_DEV_PCIE_REPLAY_COUNTER", "instant", "PCIe replay counter", "PCIe 重放计数"),
		metric("pcie_replay_increase_1h", "interconnect", "increase(DCGM_FI_DEV_PCIE_REPLAY_COUNTER[1h])", "1h", "PCIe replay increase", "PCIe 重放增量"),
		structuralMetric("gpu_metric_samples_1h", "max by(UUID)(count_over_time(DCGM_FI_DEV_GPU_UTIL[1h]))", "1h", "GPU metric samples", "GPU 指标一小时样本数"),
		structuralMetric("gpu_metric_presence_ratio_1h", "clamp_max(max by(UUID)(count_over_time(DCGM_FI_DEV_GPU_UTIL[1h])) / 240 * 100, 100)", "1h", "GPU metric presence ratio", "GPU 指标一小时存在率"),
		structuralMetric("gpu_metric_sample_age_seconds", "min by(UUID)(time() - timestamp(DCGM_FI_DEV_GPU_UTIL))", "instant", "GPU metric sample age", "GPU 指标当前样本年龄"),
		structuralMetric("gpu_uuid_presence_flap_count_1h", "changes((max by(UUID)(present_over_time(DCGM_FI_DEV_GPU_UTIL[1m])))[1h:1m])", "1h", "GPU UUID presence flaps", "GPU UUID 一小时存在性波动"),
		structuralMetric("gpu_metric_gap_max_seconds_1h", "max by(UUID)(max_over_time((timestamp(DCGM_FI_DEV_GPU_UTIL) - timestamp(DCGM_FI_DEV_GPU_UTIL offset 15s))[1h:15s]))", "1h", "GPU metric maximum gap", "GPU 指标一小时最大间隔"),
		structuralMetric("target_scrape_success_ratio_5m", "max by(instance,UUID)(DCGM_FI_DEV_GPU_UTIL * 0 + 1) * on(instance) group_left max by(instance)(avg_over_time(up{job=\"dcgm_exporter\"}[5m])) * 100", "5m", "DCGM target scrape success ratio", "DCGM Target 五分钟抓取成功率"),
		structuralMetric("target_scrape_samples_ratio_5m", "max by(instance,UUID)(DCGM_FI_DEV_GPU_UTIL * 0 + 1) * on(instance) group_left max by(instance)(avg_over_time(scrape_samples_scraped{job=\"dcgm_exporter\"}[5m]) / clamp_min(avg_over_time(scrape_samples_scraped{job=\"dcgm_exporter\"}[1h]), 1) * 100)", "5m", "DCGM target scrape samples ratio", "DCGM Target 五分钟样本量比"),
		structuralMetric("target_scrape_duration_ratio_5m", "max by(instance,UUID)(DCGM_FI_DEV_GPU_UTIL * 0 + 1) * on(instance) group_left max by(instance)(avg_over_time(scrape_duration_seconds{job=\"dcgm_exporter\"}[5m]) / clamp_min(avg_over_time(scrape_duration_seconds{job=\"dcgm_exporter\"}[1h]), 0.000001) * 100)", "5m", "DCGM target scrape duration ratio", "DCGM Target 五分钟抓取耗时比"),
		gpuMetric("gpu_reset_required", "stability", "nvidia_smi_reset_status_reset_required", "instant", "GPU reset required", "GPU 需要重置", api.StringList{"*"}),
		gpuMetric("uncorrected_ecc_delta_24h", "memory", "clamp_min(delta(nvidia_smi_ecc_errors_uncorrected_aggregate_total[24h]), 0)", "24h", "Uncorrected aggregate ECC increase", "不可纠正 ECC 累计增量", api.StringList{"H100", "H200"}),
		gpuMetric("fan_speed_pct", "thermal", "nvidia_smi_fan_speed_ratio * 100", "instant", "Fan speed ratio", "风扇转速比例", api.StringList{"4090"}),
		gpuMetric("pcie_link_width_current", "interconnect", "nvidia_smi_pcie_link_width_current", "instant", "Current PCIe link width", "当前 PCIe 链路宽度", api.StringList{"*"}),
		gpuMetric("pcie_link_width_max", "interconnect", "nvidia_smi_pcie_link_width_max", "instant", "Maximum PCIe link width", "最大 PCIe 链路宽度", api.StringList{"*"}),
	}
	for index := range definitions {
		for _, fallback := range gpuFallbackSpecs() {
			if definitions[index].Name != fallback.Key {
				continue
			}
			definitions[index].Lineage = api.StringList{"prometheus", "dcgm_exporter", "gpu_exporter"}
			definitions[index].Computation = fmt.Sprintf("priority_coalesce(dcgm_exporter:%s,gpu_exporter:%s)", definitions[index].SourceReference, fallback.Query)
			break
		}
	}
	return definitions
}

func structuralMetric(name, query, window, en, zh string) api.FeatureDefinition {
	definition := metric(name, "availability", query, window, en, zh)
	definition.MissingStrategy = "missing_is_quality_unknown"
	definition.Purposes = api.StringList{"health", "anomaly", "risk_ranking", "prediction"}
	return definition
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

func gpuMetric(name, domain, query, window, en, zh string, models api.StringList) api.FeatureDefinition {
	definition := metric(name, domain, query, window, en, zh)
	definition.SourceReference = query
	definition.Computation = "promql:" + query
	definition.Lineage = api.StringList{"prometheus", "gpu_exporter"}
	definition.SupportedModels = models
	definition.MissingStrategy = "source_required_unknown_not_zero"
	return definition
}

func HealthMetricSpecs() []MetricSpec {
	definitions := Builtins()
	result := make([]MetricSpec, 0, len(definitions)+14)
	for _, definition := range definitions {
		if definition.SourceType == "prometheus" && contains(definition.Purposes, "health") {
			source := "dcgm_exporter"
			if strings.Contains(definition.SourceReference, "nvidia_smi_") {
				source = "gpu_exporter"
			}
			result = append(result, MetricSpec{Key: definition.Name, Query: definition.SourceReference, Source: source, Priority: 0})
		}
	}
	result = append(result, gpuFallbackSpecs()...)
	return result
}

// gpuFallbackSpecs maps semantically equivalent gpu_exporter values into the
// canonical health features. PromQL performs unit normalization before Atlas
// compares or selects values: ratio→percent, Hz→MHz and bytes→MiB.
func gpuFallbackSpecs() []MetricSpec {
	return []MetricSpec{
		{Key: "gpu_temp", Query: "nvidia_smi_temperature_gpu", Source: "gpu_exporter", Priority: 1},
		{Key: "gpu_temp_max_15m", Query: "max_over_time(nvidia_smi_temperature_gpu[15m])", Source: "gpu_exporter", Priority: 1},
		{Key: "memory_temp", Query: "nvidia_smi_temperature_memory", Source: "gpu_exporter", Priority: 1},
		{Key: "memory_temp_max_15m", Query: "max_over_time(nvidia_smi_temperature_memory[15m])", Source: "gpu_exporter", Priority: 1},
		{Key: "power_usage", Query: "nvidia_smi_power_draw_watts", Source: "gpu_exporter", Priority: 1},
		{Key: "gpu_util", Query: "nvidia_smi_utilization_gpu_ratio * 100", Source: "gpu_exporter", Priority: 1},
		{Key: "gpu_util_avg_15m", Query: "avg_over_time(nvidia_smi_utilization_gpu_ratio[15m]) * 100", Source: "gpu_exporter", Priority: 1},
		{Key: "mem_copy_util", Query: "nvidia_smi_utilization_memory_ratio * 100", Source: "gpu_exporter", Priority: 1},
		{Key: "sm_clock", Query: "nvidia_smi_clocks_current_sm_clock_hz / 1000000", Source: "gpu_exporter", Priority: 1},
		{Key: "sm_clock_avg_15m", Query: "avg_over_time(nvidia_smi_clocks_current_sm_clock_hz[15m]) / 1000000", Source: "gpu_exporter", Priority: 1},
		{Key: "mem_clock", Query: "nvidia_smi_clocks_current_memory_clock_hz / 1000000", Source: "gpu_exporter", Priority: 1},
		{Key: "fb_used", Query: "nvidia_smi_memory_used_bytes / 1048576", Source: "gpu_exporter", Priority: 1},
		{Key: "fb_free", Query: "nvidia_smi_memory_free_bytes / 1048576", Source: "gpu_exporter", Priority: 1},
		{Key: "uncorrectable_remapped_rows", Query: "nvidia_smi_remapped_rows_uncorrectable", Source: "gpu_exporter", Priority: 1},
		{Key: "correctable_remapped_rows", Query: "nvidia_smi_remapped_rows_correctable", Source: "gpu_exporter", Priority: 1},
		{Key: "correctable_remapped_rows_delta_1h", Query: "clamp_min(delta(nvidia_smi_remapped_rows_correctable[1h]), 0)", Source: "gpu_exporter", Priority: 1},
		{Key: "correctable_remapped_rows_delta_24h", Query: "clamp_min(delta(nvidia_smi_remapped_rows_correctable[24h]), 0)", Source: "gpu_exporter", Priority: 1},
		{Key: "row_remap_failure", Query: "nvidia_smi_remapped_rows_failure", Source: "gpu_exporter", Priority: 1},
	}
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

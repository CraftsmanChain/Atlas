package features

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestBuiltinsCoverHealthConsumerAndModelCapabilities(t *testing.T) {
	definitions := Builtins()
	specs := HealthMetricSpecs()
	if len(definitions) != 36 || len(specs) != 53 {
		t.Fatalf("expected 36 catalog features and 53 live source specs, got definitions=%d specs=%d", len(definitions), len(specs))
	}
	for _, definition := range definitions {
		if err := Validate(&definition); err != nil {
			t.Fatalf("invalid builtin %s: %v", definition.Name, err)
		}
	}
	h100 := ExpectedHealthKeys("NVIDIA H100 80GB HBM3")
	rtx4090 := ExpectedHealthKeys("NVIDIA GeForce RTX 4090")
	if !contains(api.StringList(h100), "row_remap_failure") {
		t.Fatal("H100 must consume row remap features")
	}
	if !contains(api.StringList(h100), "correctable_remapped_rows_delta_1h") || !contains(api.StringList(h100), "correctable_remapped_rows_delta_24h") {
		t.Fatal("H100 must consume correctable row-remap trend features")
	}
	if !contains(api.StringList(h100), "gpu_metric_presence_ratio_1h") || !contains(api.StringList(rtx4090), "gpu_metric_sample_age_seconds") {
		t.Fatal("all GPU models must consume structural telemetry features")
	}
	if !contains(api.StringList(h100), "gpu_metric_gap_max_seconds_1h") || !contains(api.StringList(rtx4090), "target_scrape_success_ratio_5m") {
		t.Fatal("all GPU models must consume gap, UUID and target scrape features")
	}
	var recordingRule *api.FeatureDefinition
	for index := range definitions {
		if definitions[index].Name == "gpu_metric_family_count_delta_5m" {
			recordingRule = &definitions[index]
			break
		}
	}
	if recordingRule == nil || recordingRule.SourceType != "recording_rule" || recordingRule.Status != "active" || recordingRule.QualityStatus != "validated" || recordingRule.SourceReference != "atlas:gpu_metric_family_count_delta_5m" || contains(recordingRule.Purposes, "health") {
		t.Fatalf("metric-family recording rule must be active, validated and excluded from health: definition=%+v", recordingRule)
	}
	if contains(api.StringList(rtx4090), "row_remap_failure") || contains(api.StringList(rtx4090), "memory_temp") {
		t.Fatal("4090 must not count unsupported row-remap or memory-temperature features as missing")
	}
	if !contains(api.StringList(h100), "gpu_reset_required") || !contains(api.StringList(h100), "uncorrected_ecc_delta_24h") {
		t.Fatal("H100 must consume gpu_exporter reset and detailed ECC supplements")
	}
	if !contains(api.StringList(rtx4090), "fan_speed_pct") || contains(api.StringList(h100), "fan_speed_pct") {
		t.Fatal("fan speed support must follow the observed 4090 capability")
	}
}

func TestSeedRegisterListAndRead(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := SeedBuiltins(db); err != nil {
		t.Fatal(err)
	}
	if err := SeedBuiltins(db); err != nil {
		t.Fatalf("seeding must be idempotent: %v", err)
	}
	definitions, err := List(db, ListOptions{Purpose: "health", Status: "active"})
	if err != nil || len(definitions) != 35 {
		t.Fatalf("unexpected list result count=%d err=%v", len(definitions), err)
	}
	definition, err := Get(db, "gpu_temp", CatalogVersion)
	if err != nil || definition == nil || definition.SourceReference != "DCGM_FI_DEV_GPU_TEMP" {
		t.Fatalf("unexpected get result definition=%+v err=%v", definition, err)
	}
	custom := metric("gpu_temp_slope_1h", "thermal", "deriv(DCGM_FI_DEV_GPU_TEMP[1h])", "1h", "GPU temperature slope", "GPU 温度斜率")
	custom.Version = CatalogVersion
	custom.Status = "shadow"
	if err := Register(db, &custom); err != nil {
		t.Fatal(err)
	}
	if err := Register(db, &custom); err == nil {
		t.Fatal("duplicate name/version must be rejected")
	}
}

func TestFeatureHTTPRegistrationAndRead(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(db)
	definition := metric("metric_gap_max_seconds_1h", "availability", "atlas:metric_gap_max_seconds_1h", "1h", "Maximum metric gap", "指标最大缺口")
	definition.SourceType = "recording_rule"
	payload, _ := json.Marshal(definition)
	response := httptest.NewRecorder()
	handler.HandleCollection(response, httptest.NewRequest("POST", "/api/v1/features", bytes.NewReader(payload)))
	if response.Code != 201 {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleItem(response, httptest.NewRequest("GET", "/api/v1/features/metric_gap_max_seconds_1h?version="+CatalogVersion, nil))
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"missing_strategy":"unknown_not_zero"`)) {
		t.Fatalf("read status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleCollection(response, httptest.NewRequest("POST", "/api/v1/features", bytes.NewBufferString(`{"name":"incomplete"}`)))
	if response.Code != 400 {
		t.Fatalf("invalid definition status=%d body=%s", response.Code, response.Body.String())
	}
}

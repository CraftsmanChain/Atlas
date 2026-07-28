package platformconfig

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func newTestAssetSource(t *testing.T) (*AssetSource, *storage.DB) {
	t.Helper()
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewAssetSource(db, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return source, db
}

func TestAssetConfigEncryptsKeyAndNeverReturnsIt(t *testing.T) {
	source, db := newTestAssetSource(t)
	const secret = "lxop-test-secret"
	row, err := source.SaveConfig(AssetConfigInput{
		OpsHostURL: "https://lxop.test/openapi/v1/ops/host/list", AssetMachineURL: "https://lxop.test/openapi/v1/asset/machine/list",
		DataCenterID: "dc-1", APIKey: secret, InsecureSkipVerify: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(row.APIKeyCiphertext, []byte(secret)) {
		t.Fatal("API key appears in ciphertext")
	}
	var stored api.LXOPAssetConfig
	if err := db.First(&stored, singletonID).Error; err != nil {
		t.Fatal(err)
	}
	if key, err := source.decryptAPIKey(stored); err != nil || key != secret {
		t.Fatalf("encrypted key is not usable: key=%q err=%v", key, err)
	}
	encoded, _ := json.Marshal(assetConfigView(stored))
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "ciphertext") {
		t.Fatalf("safe view leaked secret material: %s", encoded)
	}
}

func TestAssetSyncPersistsBothSourcesAndFiltersActiveGPUAssets(t *testing.T) {
	source, db := newTestAssetSource(t)
	source.client = func(bool) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("X-API-Key") != "key" || r.URL.Query().Get("dataCenterId") != "dc-1" {
				return jsonResponse(http.StatusUnauthorized, `{"code":401,"message":"Unauthorized"}`), nil
			}
			if r.URL.Path == "/ops" {
				// The deployed LXOP omits the optional application code and relies
				// on HTTP 200. Atlas accepts both this shape and explicit code=200.
				return jsonResponse(http.StatusOK, `{"data":{"list":[{"dataCenterId":"dc-1","ipAddress":"10.0.0.1","name":"H100gpu-01","type":"server","model":"H100","state":"on","sn":"GPU-1"},{"dataCenterId":"dc-1","ipAddress":"10.0.0.2","name":"cpu-01","type":"server","model":"CPU","state":"on","sn":"CPU-1"}]}}`), nil
			}
			return jsonResponse(http.StatusOK, `{"code":200,"message":"success","data":{"list":[{"dataCenterId":"dc-1","ipAddress":"10.0.0.3","name":"H100gpu-03","type":"H100","model":"SYSGP801","state":"已上架使用中","sn":"GPU-3"},{"dataCenterId":"dc-1","ipAddress":"10.0.0.4","name":"H100gpu-04","type":"H100","model":"SYSGP801","state":"下架","sn":"GPU-4"}]}}`), nil
		})}
	}
	if _, err := source.SaveConfig(AssetConfigInput{
		OpsHostURL: "https://lxop.test/ops", AssetMachineURL: "https://lxop.test/machines",
		DataCenterID: "dc-1", APIKey: "key", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := source.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.OpsHosts != 2 || result.Machines != 2 || len(result.GPUCatalog) != 2 {
		t.Fatalf("unexpected sync result: %+v catalog=%v", result, result.GPUCatalog)
	}
	if _, exists := result.GPUCatalog["10.0.0.2"]; exists {
		t.Fatal("CPU host entered GPU catalog")
	}
	if _, exists := result.GPUCatalog["10.0.0.4"]; exists {
		t.Fatal("inactive GPU entered active GPU catalog")
	}
	var total, activeGPU int64
	db.Model(&api.InfrastructureAsset{}).Count(&total)
	db.Model(&api.InfrastructureAsset{}).Where("present = ? AND in_use = ? AND entity_kind = ?", true, true, "gpu_node").Count(&activeGPU)
	if total != 4 || activeGPU != 2 {
		t.Fatalf("unexpected persisted counts: total=%d active_gpu=%d", total, activeGPU)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestAssetConfigHandlerRequiresAuthorizedCompatibilityMode(t *testing.T) {
	source, _ := newTestAssetSource(t)
	body := `{"ops_host_url":"http://lxop.test/ops","asset_machine_url":"http://lxop.test/machines","data_center_id":"dc-1","api_key":"key","insecure_skip_verify":false,"enabled":true}`
	handler := NewAssetConfigHandler(source, "admin", false)
	response := httptest.NewRecorder()
	handler.Handle(response, httptest.NewRequest(http.MethodPut, "/api/v1/platform-config/assets", strings.NewReader(body)))
	if response.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected transport rejection, got %d", response.Code)
	}
	handler = NewAssetConfigHandler(source, "admin", true)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/platform-config/assets", strings.NewReader(body))
	request.Header.Set("X-Atlas-Admin-Token", "admin")
	response = httptest.NewRecorder()
	handler.Handle(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected authorized compatibility write, got %d: %s", response.Code, response.Body.String())
	}
}

func TestClassifyAssetPrefersNetworkIdentityOverGPUFabricName(t *testing.T) {
	item := LXOPAsset{Name: "GPU33-48_leaf8", Type: "交换机", Model: "MQM9790-NS2S", State: "已上架使用中"}
	if kind := classifyAsset(item); kind != "network" {
		t.Fatalf("GPU fabric switch entered %q domain", kind)
	}
}

func TestInUseStateBoundary(t *testing.T) {
	tests := map[string]bool{
		"on":     true,
		" ON ":   true,
		"已上架使用中": true,
		"停机":     false,
		"off":    false,
		"":       false,
	}
	for state, expected := range tests {
		if actual := isInUseState(state); actual != expected {
			t.Fatalf("state %q: expected in_use=%t, got %t", state, expected, actual)
		}
	}
}

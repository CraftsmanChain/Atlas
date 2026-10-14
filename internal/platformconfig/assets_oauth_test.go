package platformconfig

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atlas/pkg/api"
)

func TestOAuthAssetSnapshotMappingAtomicityAndStableIDs(t *testing.T) {
	source, db := newTestAssetSource(t)
	mode := "success"
	tokens := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokens++
			fmt.Fprint(w, `{"access_token":"fixture-token","token_type":"Bearer","expires_in":900}`)
			return
		}
		if r.Header.Get("X-Dc-Id") != "dc-1" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("incorrect authentication")
		}
		if r.URL.Path == "/ops" {
			fmt.Fprint(w, `{"success":true,"errorCode":0,"data":{"list":[{"id":"host-id","dataCenterID":"dc-1","ipAddress":"10.0.0.1","name":"cpu","sn":"fixture-sn","powerState":"off","machine":{"state":"运行","type":"服务器","model":"cpu-model","sn":"fixture-sn","dataCenterId":"dc-1"},"sudoPassword":"must-not-persist"}],"total":1,"current":1,"pageSize":200}}`)
			return
		}
		if mode == "fail" {
			w.WriteHeader(500)
			fmt.Fprint(w, "fixture-token secret")
			return
		}
		dc := "dc-1"
		if mode == "wrong_dc" {
			dc = "other"
		}
		ip := "10.0.0.1"
		if mode == "move" {
			ip = "10.0.0.2"
		}
		fmt.Fprintf(w, `{"success":true,"errorCode":0,"data":{"list":[{"id":"machine-id","dataCenterId":%q,"ipAddress":%q,"name":"cpu","state":"运行","type":"服务器","sn":"fixture-sn"}],"total":1,"current":1,"pageSize":200}}`, dc, ip)
	}))
	defer server.Close()
	source.client = func(bool) *http.Client { return server.Client() }
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if _, err := platformCertPool(string(cert)); err != nil {
		t.Fatal(err)
	}
	if _, err := platformCertPool("private text\n" + string(cert)); err == nil {
		t.Fatal("CA field accepted non-certificate prefix")
	}
	if _, err := platformCertPool(string(cert) + "\n-----BEGIN PRIVATE KEY-----\nZmFrZQ==\n-----END PRIVATE KEY-----"); err == nil {
		t.Fatal("CA field accepted private-key material")
	}
	input := AssetConfigInput{AuthType: "oauth_client_credentials", TokenURL: server.URL + "/token", OpsHostURL: server.URL + "/ops", AssetMachineURL: server.URL + "/machines", DataCenterID: "dc-1", ClientID: "fixture-client", ClientSecret: "fixture-secret", Enabled: true}
	row, err := source.SaveConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(row.ClientSecretCiphertext), input.ClientSecret) {
		t.Fatal("secret is not encrypted")
	}
	encoded, _ := json.Marshal(assetConfigView(row))
	for _, private := range []string{"fixture-client", "fixture-secret", "fixture-token", "ciphertext"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("public config contains credential")
		}
	}
	result, err := source.Sync(context.Background())
	if err != nil || result.OpsHosts != 1 || result.Machines != 1 {
		t.Fatalf("snapshot failed: %v", err)
	}
	var host api.InfrastructureAsset
	if err := db.Where("source = ?", "ops_host").First(&host).Error; err != nil {
		t.Fatal(err)
	}
	if host.Model != "cpu-model" || host.State != "运行" || !host.InUse || host.EntityKind != "cpu_node" || host.SourceID != "host-id" {
		t.Fatal("linked machine was not normalized or power state defined lifecycle")
	}
	for _, next := range []string{"fail", "wrong_dc"} {
		mode = next
		if _, err := source.Sync(context.Background()); err == nil {
			t.Fatal("partial/foreign scope accepted")
		}
		var count int64
		db.Model(&api.InfrastructureAsset{}).Where("present = ?", true).Count(&count)
		if count != 2 {
			t.Fatal("failed refresh retired previous snapshot")
		}
	}
	mode = "move"
	if _, err := source.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	var machines int64
	db.Model(&api.InfrastructureAsset{}).Where("source = ?", "asset_machine").Count(&machines)
	if machines != 1 {
		t.Fatal("path/name change created new source identity")
	}
	if tokens != 1 {
		t.Fatal("token was not reused between snapshots")
	}
	input.ClientID, input.ClientSecret = "", ""
	if _, err := source.SaveConfig(input); err != nil {
		t.Fatal("blank secret did not retain current credentials")
	}
	input.ClientID = "changed"
	if _, err := source.SaveConfig(input); err == nil {
		t.Fatal("client changed without a new secret")
	}
	input.ClientID = ""
	input.TokenURL = "https://other.test/token"
	if _, err := source.SaveConfig(input); err == nil {
		t.Fatal("token and data origins diverged")
	}
}

func TestManualAssetSyncIsProtectedAndDoesNotNeedGPUDiscovery(t *testing.T) {
	source, _ := newTestAssetSource(t)
	h := NewAssetConfigHandler(source, "admin", true)
	for _, tc := range []struct {
		method, token string
		want          int
	}{{"GET", "admin", 405}, {"POST", "", 401}, {"POST", "admin", 409}} {
		r := httptest.NewRequest(tc.method, "/api/v1/platform-config/assets/sync", nil)
		r.Header.Set("X-Atlas-Admin-Token", tc.token)
		w := httptest.NewRecorder()
		h.HandleSync(w, r)
		if w.Code != tc.want {
			t.Fatalf("sync status %d wanted %d", w.Code, tc.want)
		}
	}
	h = NewAssetConfigHandler(source, "admin", false)
	r := httptest.NewRequest("POST", "/sync", nil)
	r.Header.Set("X-Atlas-Admin-Token", "admin")
	w := httptest.NewRecorder()
	h.HandleSync(w, r)
	if w.Code != 426 {
		t.Fatal("plaintext sync bypassed transport gate")
	}
	source.syncMu.Lock()
	if _, err := source.Sync(context.Background()); err == nil {
		t.Fatal("concurrent asset sync was queued without a bound")
	}
	source.syncMu.Unlock()
}

func TestAssetCachedCatalogCannotCrossConfiguredDC(t *testing.T) {
	source, db := newTestAssetSource(t)
	if _, err := source.SaveConfig(AssetConfigInput{OpsHostURL: "https://fixture.test/ops", AssetMachineURL: "https://fixture.test/machines", DataCenterID: "current-dc", APIKey: "fixture-key", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, dc := range []string{"current-dc", "old-dc"} {
		if err := db.Create(&api.InfrastructureAsset{AssetKey: dc, Source: "ops_host", DataCenterID: dc, IPAddress: dc, Name: dc, EntityKind: "gpu_node", InUse: true, Present: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := source.LastGPUCatalog()
	if err != nil || len(catalog) != 1 || catalog["current-dc"] == "" {
		t.Fatal("cached inventory crossed selected data center")
	}
}

package nodeaccess

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestOverviewHandlerIsReadOnlyAndRedacted(t *testing.T) {
	handler := NewHandler(NewService(config.NodeAccessConfig{
		Enabled: true,
		CredentialProfiles: []config.NodeCredentialProfile{
			{ID: "a", Priority: 10, Username: "atlas", AuthType: "password", SecretRef: "env:ATLAS_SECRET_A", Enabled: true},
		},
	}, mapResolver{"env:ATLAS_SECRET_A": "top-secret"}))

	response := httptest.NewRecorder()
	handler.HandleOverview(response, httptest.NewRequest(http.MethodGet, "/api/v1/node-access/overview", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, SkillID) || !strings.Contains(body, `"execution_enabled":false`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	for _, forbidden := range []string{"ATLAS_SECRET_A", "top-secret", "secret_ref"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response exposed %q: %s", forbidden, body)
		}
	}

	response = httptest.NewRecorder()
	handler.HandleOverview(response, httptest.NewRequest(http.MethodPost, "/api/v1/node-access/overview", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", response.Code)
	}
}

func TestCredentialHandlerRequiresManagementTokenAndReturnsOnlyMaskedMetadata(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 32))
	vault, err := NewCredentialVault(db, key)
	if err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithVault(config.NodeAccessConfig{Enabled: true}, nil, vault)
	handler := NewHandlerWithVault(service, vault, "management-secret")
	body := `{"profile_id":"node-password-a","priority":10,"username":"atlas-readonly","password":"node-secret","enabled":true}`

	response := httptest.NewRecorder()
	unauthorizedRequest := httptest.NewRequest(http.MethodPost, "/api/v1/node-access/credentials", strings.NewReader(body))
	unauthorizedRequest.TLS = &tls.ConnectionState{}
	handler.HandleCredentials(response, unauthorizedRequest)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized write, got %d: %s", response.Code, response.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/node-access/credentials", strings.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("X-Atlas-Admin-Token", "management-secret")
	response = httptest.NewRecorder()
	handler.HandleCredentials(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("unexpected create response %d: %s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"atlas-readonly", "node-secret", "management-secret", "ciphertext"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("create response exposed %q: %s", forbidden, response.Body.String())
		}
	}

	response = httptest.NewRecorder()
	handler.HandleOverview(response, httptest.NewRequest(http.MethodGet, "/api/v1/node-access/overview", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"management_ready":true`) || !strings.Contains(response.Body.String(), `"username_masked":"••••••"`) {
		t.Fatalf("unexpected overview: %d %s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"atlas-readonly", "node-secret", "management-secret"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("overview exposed %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestCredentialHandlerDeletesOnlyWithManagementToken(t *testing.T) {
	vault, _ := testVault(t)
	if _, err := vault.Create(CredentialInput{
		ProfileID: "node-password-a", Priority: 10, Username: "atlas", Password: "secret", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithVault(NewServiceWithVault(config.NodeAccessConfig{}, nil, vault), vault, "management-secret")
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/node-access/credentials/node-password-a", nil)
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("X-Atlas-Admin-Token", "management-secret")
	response := httptest.NewRecorder()
	handler.HandleCredential(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected delete response %d: %s", response.Code, response.Body.String())
	}
	rows, err := vault.List()
	if err != nil || len(rows) != 0 {
		t.Fatalf("credential was not deleted: %#v err=%v", rows, err)
	}
}

func TestCredentialHandlerRejectsPlainHTTP(t *testing.T) {
	vault, _ := testVault(t)
	handler := NewHandlerWithVault(NewServiceWithVault(config.NodeAccessConfig{}, nil, vault), vault, "management-secret")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/node-access/credentials", strings.NewReader(`{"profile_id":"a","priority":10,"username":"user","password":"secret","enabled":true}`))
	request.Header.Set("X-Atlas-Admin-Token", "management-secret")
	response := httptest.NewRecorder()
	handler.HandleCredentials(response, request)
	if response.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected HTTPS enforcement, got %d: %s", response.Code, response.Body.String())
	}
}

func TestCredentialHandlerAllowsExplicitHTTPCompatibility(t *testing.T) {
	vault, _ := testVault(t)
	handler := NewHandlerWithVault(NewServiceWithVault(config.NodeAccessConfig{}, nil, vault), vault, "management-secret", false, true)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/node-access/credentials", strings.NewReader(`{"profile_id":"http-compatible","priority":10,"username":"user","password":"secret","enabled":true}`))
	request.Header.Set("X-Atlas-Admin-Token", "management-secret")
	response := httptest.NewRecorder()
	handler.HandleCredentials(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected explicit HTTP compatibility, got %d: %s", response.Code, response.Body.String())
	}
	overviewResponse := httptest.NewRecorder()
	handler.HandleOverview(overviewResponse, httptest.NewRequest(http.MethodGet, "/api/v1/node-access/overview", nil))
	if !strings.Contains(overviewResponse.Body.String(), `"insecure_http_allowed":true`) || !strings.Contains(overviewResponse.Body.String(), `"secure_write_only":false`) {
		t.Fatalf("overview did not expose HTTP compatibility warning: %s", overviewResponse.Body.String())
	}
}

func TestConnectivityHandlerRequiresManagementTokenAndReturnsRedactedResult(t *testing.T) {
	db := connectivityTestDB(t)
	vault, _ := testVault(t)
	access := NewService(config.NodeAccessConfig{CredentialProfiles: []config.NodeCredentialProfile{
		{ID: "node-a", Priority: 10, Username: "atlas", AuthType: "password", SecretRef: "env:A", Enabled: true},
	}}, mapResolver{"env:A": "top-secret"})
	checks := NewConnectivityService(db, access, authFunc(func(context.Context, string, string, string, []byte) error {
		return nil
	}), true)
	handler := NewHandlerWithVault(access, vault, "management-secret")
	handler.SetConnectivity(checks)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/node-access/checks", strings.NewReader(`{"node_ip":"10.114.4.25"}`))
	request.TLS = &tls.ConnectionState{}
	response := httptest.NewRecorder()
	handler.HandleChecks(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized check, got %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/node-access/checks", strings.NewReader(`{"node_ip":"10.114.4.25"}`))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("X-Atlas-Admin-Token", "management-secret")
	response = httptest.NewRecorder()
	handler.HandleChecks(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"no_command_executed":true`) {
		t.Fatalf("unexpected connectivity response %d: %s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"top-secret", `"atlas"`, "management-secret"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("connectivity response exposed %q: %s", forbidden, response.Body.String())
		}
	}
}

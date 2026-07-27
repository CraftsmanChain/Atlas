package nodeaccess

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atlas/pkg/config"
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

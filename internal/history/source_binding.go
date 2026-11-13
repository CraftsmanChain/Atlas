package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"

	"atlas/pkg/api"
	"atlas/pkg/config"
)

// historySourceEndpoint preserves the adapter's existing tenant path construction.
func historySourceEndpoint(source config.HistorySourceConfig) string {
	endpoint := strings.TrimRight(source.BaseURL, "/")
	if source.Type == "victoriametrics-cluster" {
		endpoint += "/select/" + source.TenantID + "/prometheus"
	}
	return endpoint
}

// sourceBinding records configuration provenance only: it does not certify a
// successful request, dataset identity, or source coverage. Authentication is
// deliberately excluded. Normalization matches prometheus.buildClient.
func sourceBinding(source config.HistorySourceConfig, endpoint string) api.StringMap {
	hash := func(value any) string {
		encoded, _ := json.Marshal(value) // only strings and string slices below
		sum := sha256.Sum256(encoded)
		return hex.EncodeToString(sum[:])
	}
	safeEndpoint := func(value string) string {
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
			parsed.User = nil
			value = parsed.String()
		}
		return value
	}
	base := safeEndpoint(source.BaseURL)
	effective := safeEndpoint(endpoint)
	return api.StringMap{
		"source_binding_version":         "history-source-binding-v1",
		"source_binding_scope":           "configured_endpoint_not_server_identity",
		"source_binding_sha256":          hash([]string{source.ID, source.Type, base, source.TenantID, effective}),
		"source_binding_source_sha256":   hash([]string{source.ID, source.Type}),
		"source_binding_base_sha256":     hash(base),
		"source_binding_tenant_sha256":   hash(source.TenantID),
		"source_binding_endpoint_sha256": hash(effective),
	}
}

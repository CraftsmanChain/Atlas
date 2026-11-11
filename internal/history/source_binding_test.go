package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestSourceBindingConfigurationOnly(t *testing.T) {
	s := config.HistorySourceConfig{ID: "vm", Type: "victoriametrics-cluster", BaseURL: "http://example.test/", TenantID: "7"}
	binding := sourceBinding(s, historySourceEndpoint(s))
	t.Logf("fixed_nonsecret_binding=%v", binding)
	s.BaseURL = "http://example.test"
	if !reflect.DeepEqual(binding, sourceBinding(s, historySourceEndpoint(s))) {
		t.Fatal("trailing slash changed binding")
	}
	for _, change := range []func(*config.HistorySourceConfig){func(s *config.HistorySourceConfig) { s.TenantID = "8" }, func(s *config.HistorySourceConfig) { s.BaseURL += "/other" }, func(s *config.HistorySourceConfig) { s.ID = "other" }, func(s *config.HistorySourceConfig) { s.Type = "prometheus" }} {
		other := s
		change(&other)
		if binding["source_binding_sha256"] == sourceBinding(other, historySourceEndpoint(other))["source_binding_sha256"] {
			t.Fatal("changed binding was not detected")
		}
	}
	s.SecretRef = "secret-ref-test"
	s.AuthType = "bearer"
	s.BaseURL = "http://user-secret:password-secret@example.test"
	details := sourceBinding(s, historySourceEndpoint(s))
	encoded, _ := json.Marshal(details)
	for _, secret := range []string{"user-secret", "password-secret", "secret-ref-test", "example.test", "bearer"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("new metadata exposed raw configuration")
		}
	}
	if details["source_binding_base_sha256"] != binding["source_binding_base_sha256"] {
		t.Fatal("userinfo must be excluded from binding")
	}
}

func TestAuditBindingActualTenantPathsAndLegacyRows(t *testing.T) {
	var mu sync.Mutex
	paths := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		if strings.HasPrefix(path, "/select/8/prometheus/") && strings.HasSuffix(path, "/status/buildinfo") {
			http.Error(w, "unavailable", 503)
			return
		}
		switch {
		case strings.HasSuffix(path, "/status/buildinfo"):
			fmt.Fprint(w, `{"status":"success","data":{"version":"test"}}`)
		case strings.HasSuffix(path, "/status/flags"):
			fmt.Fprint(w, `{"status":"success","data":{}}`)
		case strings.HasSuffix(path, "/targets"):
			fmt.Fprint(w, `{"status":"success","data":{"activeTargets":[]}}`)
		case strings.HasSuffix(path, "/values"):
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		case strings.HasSuffix(path, "/query_range"):
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
		case strings.HasSuffix(path, "/query"):
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	db, err := storage.InitDB(fmt.Sprintf("file:binding-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	old := api.MonitoringHistoryAudit{SourceKey: "legacy", Status: "partial", Details: api.StringMap{"execution": "old"}, StartedAt: time.Now().UTC()}
	if err = db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(old)
	sources := []config.HistorySourceConfig{}
	for _, tenant := range []string{"7", "8", "9"} {
		sources = append(sources, config.HistorySourceConfig{ID: "vm-" + tenant, Type: "victoriametrics-cluster", BaseURL: server.URL, TenantID: tenant, Enabled: true, Retention: "1d"})
	}
	sources[2].AuthType = "bearer"
	sources[2].SecretRef = "must-not-appear"
	rows, err := NewService(db, config.HistoryConfig{Enabled: true, Sources: sources}, time.Second).AuditAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatal(len(rows))
	}
	for _, model := range []any{&api.GPURiskEvidenceRevision{}, &api.GPURiskSourceCoverageCheck{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("history audit signed coverage or evidence")
		}
	}

	for i, row := range rows {
		want := []string{"success", "partial", "failed"}[i]
		if row.Status != want {
			t.Fatalf("source %d status %s", i, row.Status)
		}
		if row.CurrentGPUSeries != 0 || row.EarliestSampleAt != nil || row.LatestSampleAt != nil {
			t.Fatal("empty data became observations")
		}
		expected := sourceBinding(sources[i], server.URL+"/select/"+sources[i].TenantID+"/prometheus")
		encodedEndpoint, _ := json.Marshal(server.URL + "/select/" + sources[i].TenantID + "/prometheus")
		endpointSum := sha256.Sum256(encodedEndpoint)
		if row.Details["source_binding_endpoint_sha256"] != hex.EncodeToString(endpointSum[:]) {
			t.Fatal("endpoint digest does not bind actual mock path")
		}

		for k, v := range expected {
			if row.Details[k] != v {
				t.Fatalf("binding mismatch %s", k)
			}
		}
		if i > 0 && row.Details["source_binding_sha256"] == rows[i-1].Details["source_binding_sha256"] {
			t.Fatal("sources collapsed")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, tenant := range []string{"7", "8"} {
		for _, suffix := range []string{"/api/v1/status/buildinfo", "/api/v1/query", "/api/v1/query_range"} {
			if paths["/select/"+tenant+"/prometheus"+suffix] == 0 {
				t.Fatal("actual tenant endpoint not requested", tenant, suffix)
			}
		}
	}
	for path := range paths {
		if strings.Contains(path, "/select/9/") {
			t.Fatal("authenticated source unexpectedly requested")
		}
	}
	var loaded api.MonitoringHistoryAudit
	if err = db.First(&loaded, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(loaded)
	if string(before) != string(after) {
		t.Fatalf("legacy record changed: %s != %s", before, after)
	}
	if loaded.Details["source_binding_version"] != "" {
		t.Fatal("legacy binding was backfilled")
	}
}

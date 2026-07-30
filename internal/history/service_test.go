package history

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestAuditPersistsBoundedPrometheusCoverage(t *testing.T) {
	prometheus := newTestPrometheus(t)
	defer prometheus.Close()
	db, err := storage.InitDB(fmt.Sprintf("file:history-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.HistoryConfig{
		Enabled: true, DatasetDir: "/mnt/public/atlas/training",
		Sources: []config.HistorySourceConfig{{
			ID: "primary", Name: "Primary", Type: "prometheus", BaseURL: prometheus.URL, Enabled: true,
		}},
	}
	service := NewService(db, cfg, time.Second)
	service.now = func() time.Time { return time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC) }
	rows, err := service.AuditAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	row := rows[0]
	if row.Status != "success" || row.SourceVersion != "2.54.0" || row.ConfiguredRetention != "5y" {
		t.Fatalf("unexpected audit: %+v", row)
	}
	if row.DCGMTargetCount != 1 || row.GPUExporterTargetCount != 1 || row.CurrentGPUSeries != 720 {
		t.Fatalf("unexpected coverage: %+v", row)
	}
	if row.ScrapeIntervalSeconds != 15 || row.LatestSampleAt == nil {
		t.Fatalf("unexpected cadence: %+v", row)
	}
	if len(row.MetricFamilies) != 3 || len(row.MissingMetricFamilies) == 0 {
		t.Fatalf("unexpected metric inventory: %+v", row)
	}
	sources, err := service.Sources()
	if err != nil || len(sources) != 1 || sources[0].LatestAudit == nil {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
}

func TestHistoryHandlerIsReadOnlyExceptExplicitAudit(t *testing.T) {
	prometheus := newTestPrometheus(t)
	defer prometheus.Close()
	db, err := storage.InitDB(fmt.Sprintf("file:history-handler-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{
		DatasetDir: "/mnt/public/atlas/training",
		Sources: []config.HistorySourceConfig{{
			ID: "primary", Name: "Primary", Type: "prometheus", BaseURL: prometheus.URL, Enabled: true,
		}},
	}, time.Second)
	handler := NewHandler(service)

	response := httptest.NewRecorder()
	handler.HandleSources(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/history/sources", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"execution":"atlas_deployment_node"`) {
		t.Fatalf("GET sources status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleAudits(response, httptest.NewRequest(http.MethodPost, "/api/v1/prediction/history/audits", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"source_version":"2.54.0"`) {
		t.Fatalf("POST audit status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleCandidates(response, httptest.NewRequest(http.MethodGet, "/api/v1/prediction/history/candidates", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":"gpu-training-cohort-v1"`) {
		t.Fatalf("GET candidates status=%d body=%s", response.Code, response.Body.String())
	}
}

func newTestPrometheus(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/status/buildinfo":
			_, _ = w.Write([]byte(`{"status":"success","data":{"version":"2.54.0","revision":"abc"}}`))
		case "/api/v1/status/flags":
			_, _ = w.Write([]byte(`{"status":"success","data":{"storage.tsdb.retention.time":"5y","query.max-samples":"50000000","query.max-concurrency":"20"}}`))
		case "/api/v1/targets":
			_, _ = w.Write([]byte(`{"status":"success","data":{"activeTargets":[{"labels":{"job":"dcgm_exporter"},"health":"up"},{"labels":{"job":"gpu_exporter"},"health":"up"}]}}`))
		case "/api/v1/label/__name__/values":
			_, _ = w.Write([]byte(`{"status":"success","data":["DCGM_FI_DEV_GPU_TEMP","DCGM_FI_DEV_GPU_UTIL","DCGM_FI_DEV_XID_ERRORS"]}`))
		case "/api/v1/query":
			query := r.URL.Query().Get("query")
			value := "0"
			timestamp := "1785398400"
			switch query {
			case "count(count by(UUID) (DCGM_FI_DEV_GPU_UTIL))":
				value = "720"
			case "quantile(0.5, count_over_time(DCGM_FI_DEV_GPU_UTIL[5m]))":
				value = "20"
			case "max(timestamp(DCGM_FI_DEV_GPU_UTIL))":
				value = timestamp
			default:
				t.Fatalf("unexpected query %q", query)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[%s,"%s"]}]}}`, timestamp, value)))
		case "/api/v1/query_range":
			if strings.Contains(r.URL.Query().Get("query"), "ALERTS") {
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
					{"metric":{"__name__":"ALERTS","UUID":"GPU-1","instance":"10.0.0.1:9400","modelName":"NVIDIA H100","pci_bus_id":"0000:01:00.0","alertname":"hash-critical","alert_template":"XID故障-高优先级","alertstate":"firing","err_code":"79","err_msg":"GPU has fallen off the bus","severity":"紧急"},"values":[[1782864000,"1"],[1782864060,"1"],[1782864120,"1"],[1782871200,"1"]]},
					{"metric":{"__name__":"ALERTS","UUID":"GPU-2","instance":"10.0.0.2:9400","device_type":"H100","alertname":"hash-ecc","alert_template":"XID故障-高优先级","alertstate":"firing","err_code":"94","err_msg":"Contained ECC error","severity":"紧急"},"values":[[1782864300,"1"],[1782864360,"1"]]}
				]}}`))
			} else if strings.Contains(r.URL.Query().Get("query"), "UNCORRECTABLE_REMAPPED_ROWS") {
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
					{"metric":{"__name__":"DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS","UUID":"GPU-3","instance":"10.0.0.3:9400","modelName":"NVIDIA H100","pci_bus_id":"0000:03:00.0"},"values":[[1782864500,"8"]]}
				]}}`))
			} else {
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1750000000,"0"],[1756425600,"472"]]}]}}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

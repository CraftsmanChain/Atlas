package prometheus

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestClientTargetsAndQuery(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Path {
		case "/api/v1/targets":
			body = `{"status":"success","data":{"activeTargets":[{"labels":{"job":"node_exporter","instance":"10.0.0.1:9100"},"health":"up","lastError":"","scrapeUrl":"http://10.0.0.1:9100/metrics","lastScrape":"2026-07-17T00:00:00Z"}]}}`
		case "/api/v1/query":
			if r.URL.Query().Get("query") != "metric_name" {
				t.Fatalf("unexpected query: %s", r.URL.RawQuery)
			}
			body = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"UUID":"GPU-1","gpu":"0"},"value":[1,"42.5"]}]}}`
		case "/api/v1/status/buildinfo":
			body = `{"status":"success","data":{"version":"2.54.0","revision":"abc","branch":"HEAD","buildDate":"today","goVersion":"go1.22"}}`
		case "/api/v1/status/flags":
			body = `{"status":"success","data":{"storage.tsdb.retention.time":"5y"}}`
		case "/api/v1/label/__name__/values":
			if r.URL.Query().Get("match[]") != `{__name__=~"DCGM_FI_DEV_.+"}` {
				t.Fatalf("unexpected label query: %s", r.URL.RawQuery)
			}
			body = `{"status":"success","data":["DCGM_FI_DEV_GPU_UTIL","DCGM_FI_DEV_XID_ERRORS"]}`
		case "/api/v1/query_range":
			body = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1,"0"],[2,"720"]]}]}}`
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBuffer(nil)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
	})

	client, err := NewClient("http://prometheus.test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = transport
	targets, err := client.ActiveTargets(context.Background())
	if err != nil || len(targets) != 1 || targets[0].Health != "up" {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	samples, err := client.Query(context.Background(), "metric_name")
	if err != nil || len(samples) != 1 || samples[0].Value != 42.5 || samples[0].Timestamp.Unix() != 1 {
		t.Fatalf("samples=%+v err=%v", samples, err)
	}
	build, err := client.BuildInfo(context.Background())
	if err != nil || build.Version != "2.54.0" {
		t.Fatalf("build=%+v err=%v", build, err)
	}
	flags, err := client.Flags(context.Background())
	if err != nil || flags["storage.tsdb.retention.time"] != "5y" {
		t.Fatalf("flags=%+v err=%v", flags, err)
	}
	values, err := client.LabelValues(context.Background(), "__name__", `{__name__=~"DCGM_FI_DEV_.+"}`, time.Unix(1, 0), time.Unix(2, 0))
	if err != nil || len(values) != 2 {
		t.Fatalf("values=%+v err=%v", values, err)
	}
	rangeSeries, err := client.QueryRange(context.Background(), "count(metric_name)", time.Unix(1, 0), time.Unix(2, 0), time.Second)
	if err != nil || len(rangeSeries) != 1 || len(rangeSeries[0].Values) != 2 || rangeSeries[0].Values[1].Value != 720 {
		t.Fatalf("range=%+v err=%v", rangeSeries, err)
	}
}

func TestClientRejectsInvalidURL(t *testing.T) {
	if _, err := NewClient("file:///tmp/prometheus", time.Second); err == nil {
		t.Fatal("expected invalid URL error")
	}
}

func TestActiveTargetsFallsBackToUpQuery(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/targets"):
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(bytes.NewBufferString(`{"status":"error","error":"unsupported path"}`)), Header: make(http.Header)}, nil
		case strings.HasSuffix(r.URL.Path, "/api/v1/query"):
			if r.URL.Query().Get("query") != "up" {
				t.Fatalf("unexpected fallback query: %s", r.URL.RawQuery)
			}
			body := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"dcgm_exporter","instance":"10.80.16.42","host_ip":"10.80.16.42"},"value":[1791971969,"1"]},{"metric":{"job":"rdma_exporter","instance":"10.80.16.42"},"value":[1791971969,"0"]}]}}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBuffer(nil)), Header: make(http.Header)}, nil
		}
	})

	client, err := NewClient("http://vmselect.test/select/0/prometheus", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = transport
	targets, err := client.ActiveTargets(context.Background())
	if err != nil || len(targets) != 2 {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	if targets[0].Health != "up" || !targets[0].LastScrape.IsZero() {
		t.Fatalf("unexpected up target: %+v", targets[0])
	}
	if targets[1].Health != "down" {
		t.Fatalf("unexpected down target: %+v", targets[1])
	}
}

func TestTargetsFallbackRejectsFailuresAndInvalidSamples(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		queryCalled bool
	}{
		{"unauthorized", 401, "", false},
		{"server error", 500, "", false},
		{"other bad request", 400, "bad request", false},
		{"partial", 404, `{"status":"success","isPartial":true,"data":{"result":[]}}`, true},
		{"NaN", 404, `{"status":"success","data":{"result":[{"metric":{"job":"node_exporter","instance":"node"},"value":[1,"NaN"]}]}}`, true},
		{"malformed", 404, `{"status":"success","data":{"result":[{"metric":{"job":"node_exporter","instance":"node"},"value":[1,"broken"]}]}}`, true},
		{"missing labels", 404, `{"status":"success","data":{"result":[{"metric":{},"value":[1,"1"]}]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := NewClient("http://vm.test", time.Second)
			called := false
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				status, body := tc.status, tc.body
				if strings.HasSuffix(r.URL.Path, "/query") {
					called = true
					status = 200
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			if _, err := client.ActiveTargets(context.Background()); err == nil {
				t.Fatal("expected discovery rejection")
			}
			if called != tc.queryCalled {
				t.Fatalf("query called=%v", called)
			}
		})
	}
}

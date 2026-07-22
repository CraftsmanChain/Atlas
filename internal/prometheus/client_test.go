package prometheus

import (
	"bytes"
	"context"
	"io"
	"net/http"
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
}

func TestClientRejectsInvalidURL(t *testing.T) {
	if _, err := NewClient("file:///tmp/prometheus", time.Second); err == nil {
		t.Fatal("expected invalid URL error")
	}
}

package opsplatform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{TokenURL: server.URL + "/token", ClientID: "test-client", ClientSecret: "test-secret", DataCenterID: "dc-test"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c, server
}

func tokenResponse(w http.ResponseWriter) {
	fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":900}`)
}
func pageResponse(w http.ResponseWriter, page, total int, ids []string) {
	rows := []map[string]string{}
	for _, id := range ids {
		rows = append(rows, map[string]string{"id": id})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errorCode": 0, "data": map[string]any{"list": rows, "total": total, "current": page, "pageSize": PageSize}})
}

func TestPaginationTokenCacheAndRefresh(t *testing.T) {
	var tokens atomic.Int32
	c, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			id, secret, ok := r.BasicAuth()
			_ = r.ParseForm()
			if !ok || id != "test-client" || secret != "test-secret" || r.Method != "POST" || r.Form.Get("grant_type") != "client_credentials" {
				t.Error("incorrect OAuth request")
			}
			tokens.Add(1)
			tokenResponse(w)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-Dc-Id") != "dc-test" {
			t.Error("missing token or DC header")
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("current"))
		if r.URL.Query().Get("pageSize") != "200" {
			t.Error("invalid page size")
		}
		ids := []string{}
		for i := (page - 1) * PageSize; i < min(page*PageSize, 201); i++ {
			ids = append(ids, fmt.Sprint(i))
		}
		pageResponse(w, page, 201, ids)
	})
	for i := 0; i < 2; i++ {
		rows, err := c.List(context.Background(), server.URL+"/assets")
		if err != nil || len(rows) != 201 {
			t.Fatalf("incomplete pagination: %v", err)
		}
	}
	if tokens.Load() != 1 {
		t.Fatal("token was not reused")
	}
	c.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	if _, err := c.List(context.Background(), server.URL+"/assets"); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 2 {
		t.Fatal("expired token was not refreshed")
	}
}

func TestUnauthorizedRetriesOnceWithoutCredentialLogging(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(fmt.Sprint(permanent), func(t *testing.T) {
			var tokens, queries atomic.Int32
			c, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					tokens.Add(1)
					tokenResponse(w)
					return
				}
				if queries.Add(1) == 1 || permanent {
					w.WriteHeader(401)
					fmt.Fprint(w, "test-secret test-token")
					return
				}
				pageResponse(w, 1, 0, []string{})
			})
			_, err := c.List(context.Background(), server.URL+"/assets")
			if (err != nil) != permanent || tokens.Load() != 2 || queries.Load() != 2 {
				t.Fatal("unbounded or missing unauthorized retry")
			}
			if err != nil && (strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "test-token")) {
				t.Fatal("error leaked upstream body")
			}
		})
	}
}

func TestInvalidSnapshotsAreNeverReturnedAsEmptySuccess(t *testing.T) {
	for _, scenario := range []string{"missing_envelope", "failure", "missing_list", "short", "duplicate", "drifting_total", "wrong_page", "over_limit", "invalid_id", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			c, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					tokenResponse(w)
					return
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("current"))
				switch scenario {
				case "missing_envelope":
					fmt.Fprint(w, `{"data":{"list":[]}}`)
				case "failure":
					fmt.Fprint(w, `{"success":false,"errorCode":0,"message":"test-secret"}`)
				case "missing_list":
					fmt.Fprint(w, `{"success":true,"errorCode":0,"data":{"total":0,"current":1,"pageSize":200}}`)
				case "short":
					pageResponse(w, page, 201, []string{"only"})
				case "duplicate":
					pageResponse(w, page, 2, []string{"same", "same"})
				case "drifting_total":
					ids := []string{}
					for i := 0; i < 200; i++ {
						ids = append(ids, fmt.Sprint(i))
					}
					if page == 1 {
						pageResponse(w, 1, 201, ids)
					} else {
						pageResponse(w, 2, 200, []string{})
					}
				case "wrong_page":
					pageResponse(w, 2, 0, []string{})
				case "over_limit":
					pageResponse(w, 1, maxRows+1, []string{})
				case "invalid_id":
					pageResponse(w, 1, 1, []string{""})
				case "malformed":
					fmt.Fprint(w, `{"success":true`)
				}
			})
			rows, err := c.List(context.Background(), server.URL+"/assets")
			if err == nil || rows != nil {
				t.Fatal("unsafe snapshot was accepted")
			}
			if strings.Contains(err.Error(), "test-secret") {
				t.Fatal("upstream body leaked")
			}
		})
	}
}

func TestOriginRedirectTLSAndConcurrentTokenBoundaries(t *testing.T) {
	var tokens atomic.Int32
	c, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokens.Add(1)
			tokenResponse(w)
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://example.invalid/leak", 302)
			return
		}
		pageResponse(w, 1, 0, []string{})
	})
	for _, endpoint := range []string{"http://example.invalid/assets", "https://example.invalid/assets", server.URL + "/assets?key=secret", server.URL + "/redirect"} {
		if _, err := c.List(context.Background(), endpoint); err == nil {
			t.Fatal("unsafe endpoint or redirect accepted")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.List(context.Background(), server.URL+"/assets"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if tokens.Load() != 1 {
		t.Fatal("concurrent calls repeatedly minted tokens")
	}
	untrusted, _ := New(c.config, &http.Client{Timeout: time.Second})
	if _, err := untrusted.List(context.Background(), server.URL+"/assets"); err == nil {
		t.Fatal("self-signed TLS was silently trusted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.List(ctx, server.URL+"/assets"); err == nil {
		t.Fatal("canceled request succeeded")
	}
}

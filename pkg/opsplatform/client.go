// Package opsplatform is the server-side read client for the shared operations
// aggregation platform. It never logs credentials or persists access tokens.
package opsplatform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const PageSize = 200
const maxRows = 20000
const maxBytes = 32 << 20

type Config struct {
	TokenURL, ClientID, ClientSecret, DataCenterID string
}

type Client struct {
	config  Config
	http    *http.Client
	now     func() time.Time
	mu      sync.Mutex
	token   string
	expires time.Time
}

func ParseURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("platform endpoints require credential-free HTTPS URLs without query or fragment")
	}
	return u, nil
}

func New(config Config, client *http.Client) (*Client, error) {
	if _, err := ParseURL(config.TokenURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(config.ClientSecret) == "" || strings.TrimSpace(config.DataCenterID) == "" || client == nil {
		return nil, errors.New("platform client credentials, data center and HTTP client are required")
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("platform redirects are disabled") }
	return &Client{config: config, http: &copy, now: time.Now}, nil
}

func (c *Client) read(req *http.Request, limit int64) ([]byte, int, error) {
	res, err := c.http.Do(req)
	if err != nil {
		return nil, 0, errors.New("platform request failed (network, TLS, redirect or timeout)")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, res.StatusCode, fmt.Errorf("platform HTTP status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, res.StatusCode, errors.New("platform response unreadable or over byte budget")
	}
	return body, res.StatusCode, nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.expires) {
		return c.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.TokenURL, strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", errors.New("invalid token request")
	}
	req.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, _, err := c.read(req, 64<<10)
	if err != nil {
		return "", err
	}
	var response struct {
		Token   string `json:"access_token"`
		Type    string `json:"token_type"`
		Seconds int    `json:"expires_in"`
	}
	if json.Unmarshal(body, &response) != nil || response.Token == "" || strings.ContainsAny(response.Token, "\r\n") || !strings.EqualFold(response.Type, "Bearer") || response.Seconds < 1 || response.Seconds > 86400 {
		return "", errors.New("invalid platform token response")
	}
	leeway := time.Duration(response.Seconds) * time.Second / 10
	if leeway > 30*time.Second {
		leeway = 30 * time.Second
	}
	c.token = response.Token
	c.expires = c.now().Add(time.Duration(response.Seconds)*time.Second - leeway)
	return c.token, nil
}

// List returns a complete, bounded snapshot or an error. Callers must not apply
// removals from a partial page, drifting total, duplicate ID or invalid scope.
func (c *Client) List(ctx context.Context, endpoint string) ([]json.RawMessage, error) {
	u, err := ParseURL(endpoint)
	if err != nil {
		return nil, err
	}
	tokenURL, _ := ParseURL(c.config.TokenURL)
	if !strings.EqualFold(u.Host, tokenURL.Host) {
		return nil, errors.New("platform endpoint must share token origin")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	rows := []json.RawMessage{}
	seen := map[string]bool{}
	total := -1
	budget := 0
	for page := 1; page <= maxRows/PageSize+1; page++ {
		q := u.Query()
		q.Set("current", fmt.Sprint(page))
		q.Set("pageSize", fmt.Sprint(PageSize))
		u.RawQuery = q.Encode()
		var body []byte
		for attempt := 0; attempt < 2; attempt++ {
			token, tokenErr := c.accessToken(ctx)
			if tokenErr != nil {
				return nil, tokenErr
			}
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			if reqErr != nil {
				return nil, errors.New("invalid platform list request")
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-Dc-Id", c.config.DataCenterID)
			req.Header.Set("Accept", "application/json")
			var status int
			body, status, err = c.read(req, 8<<20)
			if status == http.StatusUnauthorized && attempt == 0 {
				c.mu.Lock()
				if c.token == token {
					c.token = ""
					c.expires = time.Time{}
				}
				c.mu.Unlock()
				continue
			}
			if err != nil {
				return nil, err
			}
			break
		}
		budget += len(body)
		if budget > maxBytes {
			return nil, errors.New("platform snapshot exceeds byte budget")
		}
		var response struct {
			Success   *bool `json:"success"`
			ErrorCode *int  `json:"errorCode"`
			Data      *struct {
				List     *[]json.RawMessage `json:"list"`
				Total    *int               `json:"total"`
				Current  *int               `json:"current"`
				PageSize *int               `json:"pageSize"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &response) != nil || response.Success == nil || !*response.Success || response.ErrorCode == nil || *response.ErrorCode != 0 || response.Data == nil {
			return nil, errors.New("platform list envelope invalid or unsuccessful")
		}
		d := response.Data
		if d.List == nil || d.Total == nil || d.Current == nil || d.PageSize == nil || *d.Total < 0 || *d.Total > maxRows || *d.Current != page || *d.PageSize != PageSize {
			return nil, errors.New("platform pagination metadata invalid or missing")
		}
		if total < 0 {
			total = *d.Total
		} else if total != *d.Total {
			return nil, errors.New("platform total changed during snapshot; retry required")
		}
		expected := min(PageSize, total-len(rows))
		if len(*d.List) != expected {
			return nil, errors.New("platform page incomplete; snapshot not applied")
		}
		for _, raw := range *d.List {
			var identity struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &identity) != nil || strings.TrimSpace(identity.ID) == "" || seen[identity.ID] {
				return nil, errors.New("platform row ID missing or duplicated")
			}
			seen[identity.ID] = true
			rows = append(rows, raw)
		}
		if len(rows) == total {
			return rows, nil
		}
	}
	return nil, errors.New("platform snapshot exceeds page budget")
}

package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

type Target struct {
	Labels     map[string]string `json:"labels"`
	Health     string            `json:"health"`
	LastError  string            `json:"lastError"`
	ScrapeURL  string            `json:"scrapeUrl"`
	LastScrape time.Time         `json:"lastScrape"`
}

type Sample struct {
	Metric    map[string]string
	Value     float64
	Timestamp time.Time
}

func NewClient(baseURL string, timeout time.Duration) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Prometheus base URL %q", baseURL)
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: timeout}}, nil
}

func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) ActiveTargets(ctx context.Context) ([]Target, error) {
	var result struct {
		ActiveTargets []Target `json:"activeTargets"`
	}
	if err := c.get(ctx, "/api/v1/targets", nil, &result); err != nil {
		return nil, err
	}
	return result.ActiveTargets, nil
}

func (c *Client) Query(ctx context.Context, query string) ([]Sample, error) {
	var result struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := c.get(ctx, "/api/v1/query", url.Values{"query": []string{query}}, &result); err != nil {
		return nil, err
	}
	samples := make([]Sample, 0, len(result.Result))
	for _, row := range result.Result {
		var valueText string
		var timestampSeconds float64
		if len(row.Value) > 0 {
			_ = json.Unmarshal(row.Value[0], &timestampSeconds)
		}
		if len(row.Value) > 1 {
			_ = json.Unmarshal(row.Value[1], &valueText)
		}
		var value float64
		_, _ = fmt.Sscan(valueText, &value)
		seconds := int64(timestampSeconds)
		nanos := int64((timestampSeconds - float64(seconds)) * float64(time.Second))
		samples = append(samples, Sample{Metric: row.Metric, Value: value, Timestamp: time.Unix(seconds, nanos)})
	}
	return samples, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, result any) error {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Prometheus request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("Prometheus returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Status    string          `json:"status"`
		Data      json.RawMessage `json:"data"`
		ErrorType string          `json:"errorType"`
		Error     string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode Prometheus response: %w", err)
	}
	if envelope.Status != "success" {
		return fmt.Errorf("Prometheus %s: %s", envelope.ErrorType, envelope.Error)
	}
	if err := json.Unmarshal(envelope.Data, result); err != nil {
		return fmt.Errorf("decode Prometheus data: %w", err)
	}
	return nil
}

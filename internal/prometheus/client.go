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

type BuildInfo struct {
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	Branch    string `json:"branch"`
	BuildDate string `json:"buildDate"`
	GoVersion string `json:"goVersion"`
}

type RangePoint struct {
	Timestamp time.Time
	Value     float64
}

type RangeSeries struct {
	Metric map[string]string
	Values []RangePoint
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

func (c *Client) BuildInfo(ctx context.Context) (BuildInfo, error) {
	var result BuildInfo
	err := c.get(ctx, "/api/v1/status/buildinfo", nil, &result)
	return result, err
}

func (c *Client) Flags(ctx context.Context) (map[string]string, error) {
	result := map[string]string{}
	err := c.get(ctx, "/api/v1/status/flags", nil, &result)
	return result, err
}

func (c *Client) LabelValues(ctx context.Context, label, selector string, start, end time.Time) ([]string, error) {
	label = strings.TrimSpace(label)
	if label == "" || strings.Contains(label, "/") {
		return nil, fmt.Errorf("invalid Prometheus label %q", label)
	}
	query := url.Values{}
	if strings.TrimSpace(selector) != "" {
		query.Set("match[]", selector)
	}
	if !start.IsZero() {
		query.Set("start", start.UTC().Format(time.RFC3339))
	}
	if !end.IsZero() {
		query.Set("end", end.UTC().Format(time.RFC3339))
	}
	var result []string
	if err := c.get(ctx, "/api/v1/label/"+url.PathEscape(label)+"/values", query, &result); err != nil {
		return nil, err
	}
	return result, nil
}

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

func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]RangeSeries, error) {
	if step <= 0 {
		return nil, fmt.Errorf("Prometheus range-query step must be positive")
	}
	var result struct {
		Result []struct {
			Metric map[string]string   `json:"metric"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	}
	values := url.Values{
		"query": []string{query},
		"start": []string{start.UTC().Format(time.RFC3339)},
		"end":   []string{end.UTC().Format(time.RFC3339)},
		"step":  []string{fmt.Sprintf("%.0f", step.Seconds())},
	}
	if err := c.get(ctx, "/api/v1/query_range", values, &result); err != nil {
		return nil, err
	}
	series := make([]RangeSeries, 0, len(result.Result))
	for _, row := range result.Result {
		item := RangeSeries{Metric: row.Metric, Values: make([]RangePoint, 0, len(row.Values))}
		for _, raw := range row.Values {
			if len(raw) < 2 {
				continue
			}
			var timestampSeconds float64
			var valueText string
			_ = json.Unmarshal(raw[0], &timestampSeconds)
			_ = json.Unmarshal(raw[1], &valueText)
			var value float64
			_, _ = fmt.Sscan(valueText, &value)
			seconds := int64(timestampSeconds)
			nanos := int64((timestampSeconds - float64(seconds)) * float64(time.Second))
			item.Values = append(item.Values, RangePoint{Timestamp: time.Unix(seconds, nanos), Value: value})
		}
		series = append(series, item)
	}
	return series, nil
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

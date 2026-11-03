package assetaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	feishuOpenAPIBase = "https://open.feishu.cn"
	// deliveryRange covers the columns the delivery sheet exposes for identity
	// and addressing. The sheet has more columns, but Atlas only needs A..O.
	deliveryRange = "A1:O20000"
)

// sheetRef locates one tab inside a Feishu spreadsheet.
type sheetRef struct {
	SpreadsheetToken string
	SheetID          string
}

// parseSheetReference accepts the operator-pasted browser URL and extracts the
// spreadsheet token and tab id.
func parseSheetReference(raw string) (sheetRef, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return sheetRef{}, errors.New("delivery_sheet_url is not a valid URL")
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index, segment := range segments {
		if (segment == "sheets" || segment == "sheet") && index+1 < len(segments) && segments[index+1] != "" {
			ref := sheetRef{SpreadsheetToken: segments[index+1], SheetID: parsed.Query().Get("sheet")}
			if ref.SheetID == "" {
				return sheetRef{}, errors.New("delivery_sheet_url must include the tab id (?sheet=...)")
			}
			return ref, nil
		}
	}
	return sheetRef{}, errors.New("delivery_sheet_url must be a Feishu spreadsheet URL (/sheets/<token>)")
}

type sheetClient struct {
	client  *http.Client
	baseURL string
}

func newSheetClient() *sheetClient {
	return &sheetClient{client: &http.Client{Timeout: 60 * time.Second}, baseURL: feishuOpenAPIBase}
}

func (c *sheetClient) tenantAccessToken(ctx context.Context, appID, appSecret string) (string, error) {
	body, err := json.Marshal(map[string]string{"app_id": appID, "app_secret": appSecret})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := c.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var payload struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode Feishu token response: %w", err)
	}
	if payload.Code != 0 || payload.TenantAccessToken == "" {
		return "", fmt.Errorf("Feishu token request failed: code=%d msg=%s", payload.Code, payload.Msg)
	}
	return payload.TenantAccessToken, nil
}

// readRange returns the requested range as string cells. Empty cells decode to
// an empty string.
func (c *sheetClient) readRange(ctx context.Context, ref sheetRef, accessToken string) ([][]string, error) {
	endpoint, err := url.Parse(fmt.Sprintf("%s/open-apis/sheets/v2/spreadsheets/%s/values/%s",
		c.baseURL, url.PathEscape(ref.SpreadsheetToken), url.PathEscape(ref.SheetID+"!"+deliveryRange)))
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("valueRenderOption", "ToString")
	query.Set("dateTimeRenderOption", "FormattedString")
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ValueRange struct {
				Values [][]any `json:"values"`
			} `json:"valueRange"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Feishu sheet response: %w", err)
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("Feishu sheet read failed: code=%d msg=%s", payload.Code, payload.Msg)
	}
	rows := make([][]string, 0, len(payload.Data.ValueRange.Values))
	for _, row := range payload.Data.ValueRange.Values {
		cells := make([]string, len(row))
		for index, cell := range row {
			cells[index] = cellString(cell)
		}
		rows = append(rows, cells)
	}
	return rows, nil
}

func cellString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

// fetchDeliveryRows resolves credentials, reads the configured tab and returns
// the raw rows.
func (s *Service) fetchDeliveryRows(ctx context.Context, row configForFetch) ([][]string, error) {
	ref, err := parseSheetReference(row.SheetURL)
	if err != nil {
		return nil, err
	}
	token, err := s.sheet.tenantAccessToken(ctx, row.AppID, row.AppSecret)
	if err != nil {
		return nil, err
	}
	return s.sheet.readRange(ctx, ref, token)
}

type configForFetch struct {
	SheetURL  string
	AppID     string
	AppSecret string
}

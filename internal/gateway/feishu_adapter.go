package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"atlas/pkg/api"
)

const feishuHookPathPrefix = "/open-apis/bot/v2/hook/"

type feishuWebhookPayload struct {
	MsgType string `json:"msg_type"`
	Content struct {
		Text string                                 `json:"text"`
		Post map[string]feishuWebhookPostLocaleBody `json:"post"`
	} `json:"content"`
	Card json.RawMessage `json:"card"`
}

type feishuWebhookPostLocaleBody struct {
	Title   string                      `json:"title"`
	Content [][]feishuWebhookPostObject `json:"content"`
}

type feishuWebhookPostObject struct {
	Tag  string `json:"tag"`
	Text string `json:"text"`
}

func extractFeishuHookToken(path string) string {
	if !strings.HasPrefix(path, feishuHookPathPrefix) {
		return ""
	}
	token := strings.TrimPrefix(path, feishuHookPathPrefix)
	token = strings.Trim(token, "/")
	return token
}

func parseFeishuWebhookAlert(body []byte) (api.AlertEvent, error) {
	var payload feishuWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return api.AlertEvent{}, err
	}

	text, err := extractFeishuMessageText(payload)
	if err != nil {
		return api.AlertEvent{}, err
	}

	return normalizeFeishuAlertText(text), nil
}

func extractFeishuMessageText(payload feishuWebhookPayload) (string, error) {
	switch payload.MsgType {
	case "text":
		if strings.TrimSpace(payload.Content.Text) == "" {
			return "", fmt.Errorf("empty feishu text content")
		}
		return payload.Content.Text, nil
	case "post":
		return extractFeishuPostText(payload.Content.Post)
	case "interactive":
		return extractFeishuCardText(payload.Card)
	default:
		return "", fmt.Errorf("unsupported feishu msg_type: %s", payload.MsgType)
	}
}

func extractFeishuPostText(post map[string]feishuWebhookPostLocaleBody) (string, error) {
	for _, locale := range post {
		var lines []string
		if title := strings.TrimSpace(locale.Title); title != "" {
			lines = append(lines, title)
		}
		for _, row := range locale.Content {
			var rowTexts []string
			for _, item := range row {
				if item.Tag == "text" && strings.TrimSpace(item.Text) != "" {
					rowTexts = append(rowTexts, item.Text)
				}
			}
			if len(rowTexts) > 0 {
				lines = append(lines, strings.Join(rowTexts, " "))
			}
		}
		if len(lines) > 0 {
			return strings.Join(lines, "\n"), nil
		}
	}
	return "", fmt.Errorf("empty feishu post content")
}

func extractFeishuCardText(cardRaw json.RawMessage) (string, error) {
	if len(cardRaw) == 0 {
		return "", fmt.Errorf("empty feishu interactive card")
	}

	var card map[string]interface{}
	if err := json.Unmarshal(cardRaw, &card); err != nil {
		return "", err
	}

	var lines []string
	if header, ok := card["header"].(map[string]interface{}); ok {
		if title, ok := header["title"].(map[string]interface{}); ok {
			if content, ok := title["content"].(string); ok && strings.TrimSpace(content) != "" {
				lines = append(lines, content)
			}
		}
	}

	if elements, ok := card["elements"].([]interface{}); ok {
		for _, element := range elements {
			elementMap, ok := element.(map[string]interface{})
			if !ok {
				continue
			}
			textMap, ok := elementMap["text"].(map[string]interface{})
			if !ok {
				continue
			}
			if content, ok := textMap["content"].(string); ok && strings.TrimSpace(content) != "" {
				lines = append(lines, content)
			}
		}
	}

	if len(lines) == 0 {
		return "", fmt.Errorf("empty feishu interactive card content")
	}
	return strings.Join(lines, "\n"), nil
}

func normalizeFeishuAlertText(text string) api.AlertEvent {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "[atlas-alert]", "[atlas-alert]\n")
	event := api.AlertEvent{
		Source:  "feishu",
		Level:   "info",
		Message: text,
		Labels:  api.StringMap{},
	}

	if text == "" {
		return event
	}

	if strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}") {
		var structured api.AlertEvent
		if err := json.Unmarshal([]byte(text), &structured); err == nil {
			if structured.Source == "" {
				structured.Source = "feishu"
			}
			if structured.Level == "" {
				structured.Level = "info"
			}
			if structured.Labels == nil {
				structured.Labels = api.StringMap{}
			}
			if structured.Message == "" {
				structured.Message = text
			}
			return structured
		}
	}

	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "[atlas-alert]" {
			start = i
			break
		}
	}
	if start < 0 {
		if parsed, ok := parseLegacyChineseFeishuAlertText(text); ok {
			return parsed
		}
		return event
	}

	event.Message = ""
	for _, line := range lines[start+1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		sep := strings.IndexAny(line, "=:")
		if sep <= 0 {
			appendMessageLine(&event, line)
			continue
		}

		key := strings.ToLower(strings.TrimSpace(line[:sep]))
		value := strings.TrimSpace(line[sep+1:])
		switch key {
		case "source":
			if value != "" {
				event.Source = value
			}
		case "level":
			if value != "" {
				event.Level = strings.ToLower(value)
			}
		case "host":
			event.Host = value
		case "message":
			appendMessageLine(&event, value)
		case "timestamp":
			if ts, err := time.Parse(time.RFC3339, value); err == nil {
				event.Timestamp = ts
			}
		case "callback_url":
			event.CallbackURL = value
		case "callback_token":
			event.CallbackToken = value
		case "labels":
			mergeInlineLabels(event.Labels, value)
		default:
			if value != "" {
				event.Labels[key] = value
				continue
			}
			appendMessageLine(&event, line)
		}
	}

	if strings.TrimSpace(event.Message) == "" {
		event.Message = text
	}
	return event
}

func parseLegacyChineseFeishuAlertText(text string) (api.AlertEvent, bool) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return api.AlertEvent{}, false
	}

	event := api.AlertEvent{
		Source: "alertmanager",
		Level:  "info",
		Labels: api.StringMap{},
	}

	inLabelSection := false
	recognized := false
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "-\t") {
			if !inLabelSection {
				continue
			}
			labelLine := strings.TrimSpace(strings.TrimPrefix(line, "-"))
			key, value, ok := splitChineseKeyValue(labelLine)
			if !ok || key == "" {
				continue
			}
			event.Labels[key] = value
			recognized = true
			continue
		}

		key, value, ok := splitChineseKeyValue(line)
		if !ok {
			continue
		}

		switch key {
		case "级别状态":
			event.Level = normalizeChineseSeverity(value)
			event.Labels["severity_text"] = strings.TrimSpace(value)
			recognized = true
			inLabelSection = false
		case "告警名称":
			event.Message = value
			event.Labels["alertname"] = value
			recognized = true
			inLabelSection = false
		case "告警标签":
			inLabelSection = true
			recognized = true
		case "触发时间":
			if ts, err := parseChineseAlertTime(value); err == nil {
				event.Timestamp = ts
			}
			event.Labels["starts_at_text"] = value
			recognized = true
			inLabelSection = false
		case "发送时间":
			event.Labels["sent_at"] = value
			recognized = true
			inLabelSection = false
		case "触发时值":
			event.Labels["trigger_value"] = value
			recognized = true
			inLabelSection = false
		default:
			inLabelSection = false
		}
	}

	if !recognized {
		return api.AlertEvent{}, false
	}

	if event.Message == "" {
		event.Message = strings.TrimSpace(text)
	}
	if host := firstNonEmpty(event.Labels["Hostname"], event.Labels["instance"], event.Labels["ext"]); host != "" {
		event.Host = host
	}
	if levelText, ok := event.Labels["severity_text"]; ok {
		event.Labels["severity_text"] = cleanChineseSeverityText(levelText)
	}
	return event, true
}

func splitChineseKeyValue(line string) (string, string, bool) {
	separators := []string{":", "："}
	for _, sep := range separators {
		if idx := strings.Index(line, sep); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			value := strings.TrimSpace(line[idx+len(sep):])
			return key, value, true
		}
	}
	return "", "", false
}

func normalizeChineseSeverity(value string) string {
	value = cleanChineseSeverityText(value)
	switch {
	case strings.Contains(value, "紧急"), strings.Contains(value, "严重"):
		return "critical"
	case strings.Contains(value, "主要"), strings.Contains(value, "错误"):
		return "error"
	case strings.Contains(value, "次要"), strings.Contains(value, "警告"), strings.Contains(value, "告警"):
		return "warning"
	default:
		return "info"
	}
}

func cleanChineseSeverityText(value string) string {
	value = strings.TrimSpace(value)
	for _, marker := range []string{"Triggered", "Resolved"} {
		value = strings.ReplaceAll(value, marker, "")
	}
	if idx := strings.Index(value, "["); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}
	return strings.TrimSpace(value)
}

func parseChineseAlertTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty time value")
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts, nil
	}
	return time.ParseInLocation("2006-01-02 15:04:05", value, time.Local)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func appendMessageLine(event *api.AlertEvent, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if event.Message == "" {
		event.Message = value
		return
	}
	event.Message += "\n" + value
}

func mergeInlineLabels(labels api.StringMap, raw string) {
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		sep := strings.Index(pair, "=")
		if sep <= 0 {
			continue
		}
		key := strings.TrimSpace(pair[:sep])
		value := strings.TrimSpace(pair[sep+1:])
		if key == "" {
			continue
		}
		labels[key] = value
	}
}

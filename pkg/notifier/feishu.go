package notifier

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
)

type FeishuNotifier struct {
	cfg *config.FeishuConfig
}

func NewFeishuNotifier(cfg *config.FeishuConfig) *FeishuNotifier {
	return &FeishuNotifier{
		cfg: cfg,
	}
}

// GenSign 生成飞书机器人签名
func GenSign(secret string, timestamp int64) (string, error) {
	stringToSign := fmt.Sprintf("%v", timestamp) + "\n" + secret
	var data []byte
	h := hmac.New(sha256.New, []byte(stringToSign))
	_, err := h.Write(data)
	if err != nil {
		return "", err
	}
	signature := base64.StdEncoding.EncodeToString(h.Sum(nil))
	return signature, nil
}

// SendAlert 将告警信息格式化为富文本并发送到所有启用的飞书群
func (n *FeishuNotifier) SendAlert(event *api.AlertEvent, isRepeat bool) {
	if n.cfg == nil || len(n.cfg.Bots) == 0 {
		return
	}

	for i, bot := range n.cfg.Bots {
		if !bot.Enabled || bot.WebhookURL == "" {
			continue
		}
		// 启动 goroutine 并发发送给各个机器人
		go n.sendToBot(bot, event, isRepeat, i)
	}
}

func (n *FeishuNotifier) sendToBot(bot config.FeishuBotConfig, event *api.AlertEvent, isRepeat bool, botIndex int) {
	// 构造飞书消息的 Markdown 格式内容
	title := fmt.Sprintf("🚨 Atlas 告警通知: [%s]", event.Level)
	if isRepeat {
		title = fmt.Sprintf("🔁 Atlas 重复告警: [%s] (第 %d 次)", event.Level, event.RepeatCount)
	}

	content := formatAlertContent(event)

	// 构建飞书机器人所需的 JSON 载荷 (使用 interactive 消息卡片)
	payload := map[string]interface{}{
		"msg_type": "interactive",
		"card": map[string]interface{}{
			"config": map[string]bool{
				"wide_screen_mode": true,
			},
			"header": map[string]interface{}{
				"title": map[string]string{
					"tag":     "plain_text",
					"content": title,
				},
				"template": getTemplateColor(event.Level),
			},
			"elements": []interface{}{
				map[string]interface{}{
					"tag": "div",
					"text": map[string]string{
						"tag":     "lark_md",
						"content": content,
					},
				},
			},
		},
	}

	// 添加签名
	if bot.EnableSignature && bot.Secret != "" {
		timestamp := time.Now().Unix()
		sign, err := GenSign(bot.Secret, timestamp)
		if err != nil {
			log.Printf("[FeishuNotifier] Bot[%d] Error generating signature: %v", botIndex, err)
			return
		}
		payload["timestamp"] = fmt.Sprintf("%d", timestamp)
		payload["sign"] = sign
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[FeishuNotifier] Bot[%d] Error marshalling payload: %v", botIndex, err)
		return
	}

	resp, err := http.Post(bot.WebhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[FeishuNotifier] Bot[%d] Error sending alert to feishu: %v", botIndex, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[FeishuNotifier] Bot[%d] Feishu returned non-OK status: %d", botIndex, resp.StatusCode)
	} else {
		log.Printf("[FeishuNotifier] Bot[%d] Alert sent to feishu successfully", botIndex)
	}
}

func formatAlertContent(event *api.AlertEvent) string {
	if event.Source == "atlas_thermal_monitor" {
		return formatThermalAlertContent(event)
	}
	content := fmt.Sprintf(
		"**来源**: %s\n**主机**: %s\n**内容**: %s\n**最后发生时间**: %s\n",
		event.Source,
		firstNonEmpty(event.Host, event.Labels["host_ip"], event.Labels["Hostname"], event.Labels["instance"], "—"),
		event.Message,
		event.LastSeenAt.Format("2006-01-02 15:04:05"),
	)
	if len(event.Labels) == 0 {
		return content
	}
	content += "**标签**:\n"
	keys := make([]string, 0, len(event.Labels))
	for key := range event.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		content += fmt.Sprintf("- %s: %s\n", key, event.Labels[key])
	}
	return content
}

func formatThermalAlertContent(event *api.AlertEvent) string {
	labels := event.Labels
	return fmt.Sprintf(
		"**告警主题**: %s\n**级别**: %s\n**主机名**: %s\n**IP**: %s\n**显卡**: GPU %s · %s\n**GPU UUID**: %s\n**告警内容**: %s\n**温度**: %s°C\n**触发条件**: %s\n**持续时间**: %s\n**发生时间**: %s\n**复核证据**: %s\n",
		firstNonEmpty(labels["alert_topic"], "GPU sustained high temperature"), strings.ToUpper(event.Level),
		firstNonEmpty(labels["hostname"], event.Host, "—"), firstNonEmpty(labels["host_ip"], "—"),
		firstNonEmpty(labels["gpu_index"], "—"), firstNonEmpty(labels["gpu_model"], "—"), firstNonEmpty(labels["gpu_uuid"], "—"),
		firstNonEmpty(event.Message, "—"), firstNonEmpty(labels["temperature_celsius"], "—"),
		firstNonEmpty(labels["threshold"], "—"), firstNonEmpty(labels["sustained_duration"], "—"),
		event.LastSeenAt.Format("2006-01-02 15:04:05"), firstNonEmpty(labels["evidence_collection"], "—"),
	)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// 根据告警级别设置卡片头部颜色
func getTemplateColor(level string) string {
	switch level {
	case "critical", "fatal":
		return "red"
	case "error":
		return "orange"
	case "warning", "warn":
		return "yellow"
	case "info":
		return "blue"
	default:
		return "grey"
	}
}

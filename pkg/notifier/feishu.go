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

	content := fmt.Sprintf(
		"**来源**: %s\n**主机**: %s\n**内容**: %s\n**最后发生时间**: %s\n",
		event.Source,
		event.Host,
		event.Message,
		event.LastSeenAt.Format("2006-01-02 15:04:05"),
	)

	// 如果有 Labels 则附加上去
	if len(event.Labels) > 0 {
		content += "**标签**:\n"
		for k, v := range event.Labels {
			content += fmt.Sprintf("- %s: %s\n", k, v)
		}
	}

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

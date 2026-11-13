package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"atlas/pkg/config"
	"atlas/pkg/notifier"
)

// defaultReportConfig is the production Atlas config holding feishu.bots.
const defaultReportConfig = "/ops/atlas/configs/config.yaml"

// sendReportCard pushes one report card to every enabled Feishu bot of the
// config at configPath. It is a package variable so tests can inject a sender.
// Errors never carry webhook URLs or signing secrets.
var sendReportCard = func(configPath, title, template, content string) error {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		// Parse errors may quote config content; report only the class.
		switch {
		case errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("feishu config %s does not exist", configPath)
		case errors.Is(err, os.ErrPermission):
			return fmt.Errorf("feishu config %s is not readable", configPath)
		default:
			return fmt.Errorf("feishu config %s could not be loaded", configPath)
		}
	}
	if err := notifier.NewFeishuNotifier(&cfg.Feishu).SendCard(title, template, content); err != nil {
		return errors.New(redactFeishu(err.Error(), cfg.Feishu))
	}
	return nil
}

// redactFeishu removes any configured webhook URL, its token path and signing
// secret from an error message (defence in depth; the notifier already strips
// URLs from transport errors).
func redactFeishu(msg string, cfg config.FeishuConfig) string {
	for _, bot := range cfg.Bots {
		for _, secret := range []string{strings.TrimSpace(bot.WebhookURL), strings.TrimSpace(bot.Secret)} {
			if secret != "" {
				msg = strings.ReplaceAll(msg, secret, "[redacted]")
			}
		}
		if u := strings.TrimSpace(bot.WebhookURL); u != "" {
			if i := strings.LastIndex(strings.TrimRight(u, "/"), "/"); i >= 0 {
				if token := strings.Trim(u[i+1:], "/"); len(token) >= 8 {
					msg = strings.ReplaceAll(msg, token, "[redacted]")
				}
			}
		}
	}
	return msg
}

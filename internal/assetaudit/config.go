package assetaudit

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"gorm.io/gorm"
)

const singletonID uint = 1

// ConfigView is the credential-free projection returned to the UI.
type ConfigView struct {
	Enabled                 bool       `json:"enabled"`
	DeliveryEnabled         bool       `json:"delivery_check_enabled"`
	ScanEnabled             bool       `json:"scan_enabled"`
	DeliverySheetURL        string     `json:"delivery_sheet_url"`
	FeishuAppID             string     `json:"feishu_app_id"`
	FeishuAppSecretConfigured bool     `json:"feishu_app_secret_configured"`
	NotifyEnabled           bool       `json:"notify_enabled"`
	NotifyAlways            bool       `json:"notify_always"`
	NotifyWebhookURL        string     `json:"notify_webhook_url"`
	NotifySecretConfigured  bool       `json:"notify_secret_configured"`
	ScheduleHour            int        `json:"schedule_hour"`
	Timezone                string     `json:"timezone"`
	ScanConcurrency         int        `json:"scan_concurrency"`
	ScanTimeoutMS           int        `json:"scan_timeout_ms"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

// ConfigInput is the write payload. Blank secret fields keep the stored value.
type ConfigInput struct {
	Enabled           bool   `json:"enabled"`
	DeliveryEnabled   bool   `json:"delivery_check_enabled"`
	ScanEnabled       bool   `json:"scan_enabled"`
	DeliverySheetURL  string `json:"delivery_sheet_url"`
	FeishuAppID       string `json:"feishu_app_id"`
	FeishuAppSecret   string `json:"feishu_app_secret"`
	NotifyEnabled     bool   `json:"notify_enabled"`
	NotifyAlways      bool   `json:"notify_always"`
	NotifyWebhookURL  string `json:"notify_webhook_url"`
	NotifySecret      string `json:"notify_secret"`
	ScheduleHour      int    `json:"schedule_hour"`
	Timezone          string `json:"timezone"`
	ScanConcurrency   int    `json:"scan_concurrency"`
	ScanTimeoutMS     int    `json:"scan_timeout_ms"`
}

func configView(row api.AssetAuditConfig) ConfigView {
	return ConfigView{
		Enabled: row.Enabled, DeliveryEnabled: row.DeliveryEnabled, ScanEnabled: row.ScanEnabled,
		DeliverySheetURL: row.DeliverySheetURL, FeishuAppID: row.FeishuAppID,
		FeishuAppSecretConfigured: isConfigured(row.FeishuAppSecretCT),
		NotifyEnabled:             row.NotifyEnabled, NotifyAlways: row.NotifyAlways, NotifyWebhookURL: row.NotifyWebhookURL,
		NotifySecretConfigured: isConfigured(row.NotifySecretCT),
		ScheduleHour:           row.ScheduleHour, Timezone: row.Timezone,
		ScanConcurrency: row.ScanConcurrency, ScanTimeoutMS: row.ScanTimeoutMS,
		UpdatedAt: row.UpdatedAt,
	}
}

// GetConfig returns the persisted singleton row.
func (s *Service) GetConfig() (api.AssetAuditConfig, error) {
	var row api.AssetAuditConfig
	err := s.db.First(&row, singletonID).Error
	return row, err
}

// EnsureConfig creates the singleton row from the deployment defaults the first
// time Atlas starts, so a cluster can ship preconfigured without an operator
// write. It never overwrites an existing row.
func (s *Service) EnsureConfig(opts config.AssetAuditOptions) error {
	var existing api.AssetAuditConfig
	err := s.db.First(&existing, singletonID).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	row := api.AssetAuditConfig{
		ID: singletonID, Enabled: opts.Enabled, DeliveryEnabled: opts.DeliveryEnabled,
		ScanEnabled: opts.ScanEnabled, DeliverySheetURL: strings.TrimSpace(opts.DeliverySheetURL),
		NotifyEnabled: opts.NotifyEnabled, NotifyAlways: opts.NotifyAlways, ScheduleHour: opts.ScheduleHour, Timezone: opts.Timezone,
		ScanConcurrency: opts.ScanConcurrency, ScanTimeoutMS: opts.ScanTimeoutMS,
	}
	if row.ScheduleHour <= 0 || row.ScheduleHour > 23 {
		row.ScheduleHour = 5
	}
	if strings.TrimSpace(row.Timezone) == "" {
		row.Timezone = "Asia/Shanghai"
	}
	if row.ScanConcurrency <= 0 {
		row.ScanConcurrency = 1
	}
	if row.ScanTimeoutMS <= 0 {
		row.ScanTimeoutMS = 1000
	}
	if err := s.db.Create(&row).Error; err != nil {
		// A concurrent first start may have inserted the row already.
		if retryErr := s.db.First(&existing, singletonID).Error; retryErr == nil {
			return nil
		}
		return err
	}
	return nil
}

// SaveConfig validates and persists the operator-entered configuration. Empty
// secret fields preserve the already stored ciphertext.
func (s *Service) SaveConfig(input ConfigInput) (api.AssetAuditConfig, error) {
	input.DeliverySheetURL = strings.TrimSpace(input.DeliverySheetURL)
	input.FeishuAppID = strings.TrimSpace(input.FeishuAppID)
	input.NotifyWebhookURL = strings.TrimSpace(input.NotifyWebhookURL)
	input.Timezone = strings.TrimSpace(input.Timezone)
	input.FeishuAppSecret = strings.TrimSpace(input.FeishuAppSecret)
	input.NotifySecret = strings.TrimSpace(input.NotifySecret)

	if input.DeliverySheetURL != "" {
		if err := validateSheetURL(input.DeliverySheetURL); err != nil {
			return api.AssetAuditConfig{}, err
		}
	}
	if input.NotifyWebhookURL != "" {
		parsed, err := url.ParseRequestURI(input.NotifyWebhookURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return api.AssetAuditConfig{}, errors.New("notify_webhook_url must be an absolute HTTP(S) URL")
		}
	}
	if input.ScheduleHour < 0 || input.ScheduleHour > 23 {
		return api.AssetAuditConfig{}, errors.New("schedule_hour must be between 0 and 23")
	}
	if input.Timezone == "" {
		input.Timezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return api.AssetAuditConfig{}, fmt.Errorf("timezone %q is not a valid IANA name", input.Timezone)
	}
	if input.ScanConcurrency <= 0 {
		input.ScanConcurrency = 1
	}
	if input.ScanTimeoutMS <= 0 {
		input.ScanTimeoutMS = 1000
	}
	if input.DeliveryEnabled && (input.DeliverySheetURL == "" || input.FeishuAppID == "") {
		return api.AssetAuditConfig{}, errors.New("delivery check requires a sheet URL and Feishu app id")
	}

	var row api.AssetAuditConfig
	err := s.db.First(&row, singletonID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return row, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row.ID = singletonID
	}
	row.Enabled = input.Enabled
	row.DeliveryEnabled = input.DeliveryEnabled
	row.ScanEnabled = input.ScanEnabled
	row.DeliverySheetURL = input.DeliverySheetURL
	row.FeishuAppID = input.FeishuAppID
	row.NotifyEnabled = input.NotifyEnabled
	row.NotifyAlways = input.NotifyAlways
	row.NotifyWebhookURL = input.NotifyWebhookURL
	row.ScheduleHour = input.ScheduleHour
	row.Timezone = input.Timezone
	row.ScanConcurrency = input.ScanConcurrency
	row.ScanTimeoutMS = input.ScanTimeoutMS
	if input.FeishuAppSecret != "" {
		sealed, sealErr := s.cipher.seal("feishu_app_secret", input.FeishuAppSecret)
		if sealErr != nil {
			return row, sealErr
		}
		row.FeishuAppSecretCT, row.KeyVersion = sealed, keyVersion
	}
	if input.NotifySecret != "" {
		sealed, sealErr := s.cipher.seal("notify_secret", input.NotifySecret)
		if sealErr != nil {
			return row, sealErr
		}
		row.NotifySecretCT, row.KeyVersion = sealed, keyVersion
	}
	if input.DeliveryEnabled && row.KeyVersion == "" {
		return row, errors.New("Feishu app secret is required before delivery check can run")
	}
	if input.DeliveryEnabled && !isConfigured(row.FeishuAppSecretCT) {
		return row, errors.New("Feishu app secret is required before delivery check can run")
	}
	if err := s.db.Save(&row).Error; err != nil {
		return row, err
	}
	return row, nil
}

// resolvedSecrets decrypts the credential fields for outbound calls.
func (s *Service) resolvedSecrets(row api.AssetAuditConfig) (appSecret, notifySecret string, err error) {
	if isConfigured(row.FeishuAppSecretCT) {
		appSecret, err = s.cipher.open("feishu_app_secret", row.KeyVersion, row.FeishuAppSecretCT)
		if err != nil {
			return "", "", err
		}
	}
	if isConfigured(row.NotifySecretCT) {
		notifySecret, err = s.cipher.open("notify_secret", row.KeyVersion, row.NotifySecretCT)
		if err != nil {
			return "", "", err
		}
	}
	return appSecret, notifySecret, nil
}

func validateSheetURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("delivery_sheet_url must be an absolute HTTP(S) URL")
	}
	if _, err := parseSheetReference(value); err != nil {
		return err
	}
	return nil
}

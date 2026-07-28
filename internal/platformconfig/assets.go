package platformconfig

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const lxopKeyVersion = "aes-256-gcm-v1"

type LXOPAsset struct {
	DataCenterID string `json:"dataCenterId"`
	IPAddress    string `json:"ipAddress"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Model        string `json:"model"`
	State        string `json:"state"`
	SerialNumber string `json:"sn"`
}

type AssetSyncResult struct {
	GPUCatalog map[string]string
	OpsHosts   int
	Machines   int
	SyncedAt   time.Time
	Configured bool
}

type AssetSource struct {
	db     *storage.DB
	aead   cipher.AEAD
	client func(bool) *http.Client
	now    func() time.Time
}

func NewAssetSource(db *storage.DB, encodedKey string) (*AssetSource, error) {
	if db == nil {
		return nil, errors.New("asset configuration database is required")
	}
	key, err := decodeAssetMasterKey(encodedKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, fmt.Errorf("create asset configuration cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create asset configuration AEAD: %w", err)
	}
	return &AssetSource{
		db: db, aead: aead, now: time.Now,
		client: func(skipVerify bool) *http.Client {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: skipVerify} // #nosec G402 -- operator-controlled for current internal self-signed LXOP.
			return &http.Client{Timeout: 20 * time.Second, Transport: transport}
		},
	}, nil
}

func decodeAssetMasterKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("asset secret master key is not configured")
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		key, err := encoding.DecodeString(value)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, errors.New("asset secret master key must be base64-encoded 32 bytes")
}

func (s *AssetSource) encryptAPIKey(value string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := s.aead.Seal(nil, nonce, []byte(value), []byte("atlas|lxop-assets|"+lxopKeyVersion))
	return append(nonce, sealed...), nil
}

func (s *AssetSource) decryptAPIKey(config api.LXOPAssetConfig) (string, error) {
	if config.KeyVersion != lxopKeyVersion || len(config.APIKeyCiphertext) <= s.aead.NonceSize() {
		return "", errors.New("unsupported or invalid LXOP API key ciphertext")
	}
	plain, err := s.aead.Open(nil, config.APIKeyCiphertext[:s.aead.NonceSize()], config.APIKeyCiphertext[s.aead.NonceSize():], []byte("atlas|lxop-assets|"+lxopKeyVersion))
	if err != nil {
		return "", errors.New("decrypt LXOP API key")
	}
	return string(plain), nil
}

type AssetConfigView struct {
	OpsHostURL         string     `json:"ops_host_url"`
	AssetMachineURL    string     `json:"asset_machine_url"`
	DataCenterID       string     `json:"data_center_id"`
	InsecureSkipVerify bool       `json:"insecure_skip_verify"`
	Enabled            bool       `json:"enabled"`
	APIKeyConfigured   bool       `json:"api_key_configured"`
	LastSyncStatus     string     `json:"last_sync_status"`
	LastSyncError      string     `json:"last_sync_error,omitempty"`
	LastSyncAt         *time.Time `json:"last_sync_at,omitempty"`
	LastOpsHostCount   int        `json:"last_ops_host_count"`
	LastMachineCount   int        `json:"last_machine_count"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func assetConfigView(row api.LXOPAssetConfig) AssetConfigView {
	return AssetConfigView{
		OpsHostURL: row.OpsHostURL, AssetMachineURL: row.AssetMachineURL,
		DataCenterID: row.DataCenterID, InsecureSkipVerify: row.InsecureSkipVerify,
		Enabled: row.Enabled, APIKeyConfigured: len(row.APIKeyCiphertext) > 0,
		LastSyncStatus: row.LastSyncStatus, LastSyncError: row.LastSyncError,
		LastSyncAt: row.LastSyncAt, LastOpsHostCount: row.LastOpsHostCount,
		LastMachineCount: row.LastMachineCount, UpdatedAt: row.UpdatedAt,
	}
}

type AssetConfigInput struct {
	OpsHostURL         string `json:"ops_host_url"`
	AssetMachineURL    string `json:"asset_machine_url"`
	DataCenterID       string `json:"data_center_id"`
	APIKey             string `json:"api_key"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	Enabled            bool   `json:"enabled"`
}

func (s *AssetSource) GetConfig() (api.LXOPAssetConfig, error) {
	var row api.LXOPAssetConfig
	err := s.db.First(&row, singletonID).Error
	return row, err
}

func (s *AssetSource) SaveConfig(input AssetConfigInput) (api.LXOPAssetConfig, error) {
	input.OpsHostURL = strings.TrimSpace(input.OpsHostURL)
	input.AssetMachineURL = strings.TrimSpace(input.AssetMachineURL)
	input.DataCenterID = strings.TrimSpace(input.DataCenterID)
	input.APIKey = strings.TrimSpace(input.APIKey)
	for label, value := range map[string]string{"ops_host_url": input.OpsHostURL, "asset_machine_url": input.AssetMachineURL} {
		parsed, err := url.ParseRequestURI(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return api.LXOPAssetConfig{}, fmt.Errorf("%s must be an absolute HTTP(S) URL", label)
		}
	}
	if input.DataCenterID == "" {
		return api.LXOPAssetConfig{}, errors.New("data_center_id is required")
	}
	var row api.LXOPAssetConfig
	err := s.db.First(&row, singletonID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return row, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row.ID = singletonID
	}
	if input.APIKey != "" {
		row.APIKeyCiphertext, err = s.encryptAPIKey(input.APIKey)
		if err != nil {
			return row, err
		}
		row.KeyVersion = lxopKeyVersion
	}
	if len(row.APIKeyCiphertext) == 0 {
		return row, errors.New("api_key is required for initial configuration")
	}
	row.OpsHostURL, row.AssetMachineURL, row.DataCenterID = input.OpsHostURL, input.AssetMachineURL, input.DataCenterID
	row.InsecureSkipVerify, row.Enabled = input.InsecureSkipVerify, input.Enabled
	if row.LastSyncStatus == "" {
		row.LastSyncStatus = "never"
	}
	err = s.db.Save(&row).Error
	input.APIKey = ""
	return row, err
}

func (s *AssetSource) Sync(ctx context.Context) (*AssetSyncResult, error) {
	config, err := s.GetConfig()
	if errors.Is(err, gorm.ErrRecordNotFound) || !config.Enabled {
		return &AssetSyncResult{Configured: false}, nil
	}
	if err != nil {
		return nil, err
	}
	key, err := s.decryptAPIKey(config)
	if err != nil {
		return nil, s.recordFailure(config, err)
	}
	defer func() { key = "" }()
	ops, err := s.fetch(ctx, config.OpsHostURL, config.DataCenterID, key, config.InsecureSkipVerify)
	if err != nil {
		return nil, s.recordFailure(config, fmt.Errorf("ops host API: %w", err))
	}
	machines, err := s.fetch(ctx, config.AssetMachineURL, config.DataCenterID, key, config.InsecureSkipVerify)
	if err != nil {
		return nil, s.recordFailure(config, fmt.Errorf("asset machine API: %w", err))
	}
	now := s.now()
	if err := s.persistSnapshot(now, ops, machines); err != nil {
		return nil, s.recordFailure(config, err)
	}
	config.LastSyncStatus, config.LastSyncError, config.LastSyncAt = "success", "", &now
	config.LastOpsHostCount, config.LastMachineCount = len(ops), len(machines)
	if err := s.db.Save(&config).Error; err != nil {
		return nil, err
	}
	return &AssetSyncResult{GPUCatalog: s.gpuCatalog(ops, machines), OpsHosts: len(ops), Machines: len(machines), SyncedAt: now, Configured: true}, nil
}

func (s *AssetSource) recordFailure(config api.LXOPAssetConfig, cause error) error {
	now := s.now()
	config.LastSyncStatus, config.LastSyncError, config.LastSyncAt = "failed", cause.Error(), &now
	_ = s.db.Save(&config).Error
	return cause
}

func (s *AssetSource) fetch(ctx context.Context, endpoint, dataCenterID, key string, skipVerify bool) ([]LXOPAsset, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	query := parsed.Query()
	query.Set("dataCenterId", dataCenterID)
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", key)
	resp, err := s.client(skipVerify).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			List []LXOPAsset `json:"list"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 16<<20))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if payload.Code != http.StatusOK {
		return nil, fmt.Errorf("LXOP code %d: %s", payload.Code, payload.Message)
	}
	return payload.Data.List, nil
}

func (s *AssetSource) persistSnapshot(now time.Time, ops, machines []LXOPAsset) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&api.InfrastructureAsset{}).Where("present = ?", true).Updates(map[string]any{"present": false, "last_synced_at": now}).Error; err != nil {
			return err
		}
		for _, batch := range []struct {
			source string
			rows   []LXOPAsset
		}{{"ops_host", ops}, {"asset_machine", machines}} {
			for _, item := range batch.rows {
				record := infrastructureRecord(batch.source, item, now)
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "asset_key"}},
					DoUpdates: clause.AssignmentColumns([]string{"data_center_id", "ip_address", "name", "type", "model", "state", "serial_number", "in_use", "present", "entity_kind", "last_seen_at", "last_synced_at", "updated_at"}),
				}).Create(&record).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func infrastructureRecord(source string, item LXOPAsset, now time.Time) api.InfrastructureAsset {
	identity := strings.Join([]string{source, item.DataCenterID, item.IPAddress, item.SerialNumber, item.Name}, "|")
	sum := sha256.Sum256([]byte(identity))
	return api.InfrastructureAsset{
		AssetKey: fmt.Sprintf("lxop-%x", sum[:12]), Source: source,
		DataCenterID: strings.TrimSpace(item.DataCenterID), IPAddress: strings.TrimSpace(item.IPAddress),
		Name: strings.TrimSpace(item.Name), Type: strings.TrimSpace(item.Type), Model: strings.TrimSpace(item.Model),
		State: strings.TrimSpace(item.State), SerialNumber: strings.TrimSpace(item.SerialNumber),
		InUse: isInUseState(item.State), Present: true, EntityKind: classifyAsset(item),
		FirstSeenAt: now, LastSeenAt: now, LastSyncedAt: now,
	}
}

func isInUseState(state string) bool {
	value := strings.ToLower(strings.TrimSpace(state))
	return value == "on" || value == "已上架使用中"
}

func classifyAsset(item LXOPAsset) string {
	value := strings.ToLower(strings.Join([]string{item.Name, item.Type, item.Model}, " "))
	for _, hint := range []string{"gpu", "h100", "h800", "a800", "a100", "4090", "l40", "v100", "rtx"} {
		if strings.Contains(value, hint) {
			return "gpu_node"
		}
	}
	for _, hint := range []string{"switch", "交换机", "network", "网络"} {
		if strings.Contains(value, hint) {
			return "network"
		}
	}
	for _, hint := range []string{"server", "服务器", "cpu"} {
		if strings.Contains(value, hint) {
			return "cpu_node"
		}
	}
	return "unknown"
}

func (s *AssetSource) gpuCatalog(ops, machines []LXOPAsset) map[string]string {
	result := make(map[string]string)
	for _, batch := range [][]LXOPAsset{ops, machines} {
		for _, item := range batch {
			if isInUseState(item.State) && classifyAsset(item) == "gpu_node" && strings.TrimSpace(item.IPAddress) != "" {
				result[strings.TrimSpace(item.IPAddress)] = strings.TrimSpace(item.Name)
			}
		}
	}
	return result
}

func (s *AssetSource) LastGPUCatalog() (map[string]string, error) {
	var rows []api.InfrastructureAsset
	if err := s.db.Where("present = ? AND in_use = ? AND entity_kind = ?", true, true, "gpu_node").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.IPAddress != "" {
			result[row.IPAddress] = row.Name
		}
	}
	return result, nil
}

type AssetConfigHandler struct {
	source            *AssetSource
	adminToken        string
	allowInsecureHTTP bool
}

func NewAssetConfigHandler(source *AssetSource, adminToken string, allowInsecureHTTP bool) *AssetConfigHandler {
	return &AssetConfigHandler{source: source, adminToken: strings.TrimSpace(adminToken), allowInsecureHTTP: allowInsecureHTTP}
}

func (h *AssetConfigHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if h.source == nil {
		writeError(w, http.StatusServiceUnavailable, "asset configuration encryption is unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		row, err := h.source.GetConfig()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"data": AssetConfigView{LastSyncStatus: "not_configured"}})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read asset configuration")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": assetConfigView(row)})
	case http.MethodPut:
		if r.TLS == nil && !h.allowInsecureHTTP {
			writeError(w, http.StatusUpgradeRequired, "asset configuration writes require HTTPS or approved HTTP compatibility mode")
			return
		}
		if !h.authorized(r) {
			writeError(w, http.StatusUnauthorized, "invalid management credential")
			return
		}
		var input AssetConfigInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid asset configuration")
			return
		}
		row, err := h.source.SaveConfig(input)
		input.APIKey = ""
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": assetConfigView(row)})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *AssetConfigHandler) authorized(r *http.Request) bool {
	provided := r.Header.Get("X-Atlas-Admin-Token")
	return h.adminToken != "" && len(provided) == len(h.adminToken) && subtle.ConstantTimeCompare([]byte(provided), []byte(h.adminToken)) == 1
}

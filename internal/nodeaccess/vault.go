package nodeaccess

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

const (
	vaultKeyVersion = "aes-256-gcm-v1"
	vaultRefPrefix  = "vault:"
)

var profileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type credentialPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type CredentialInput struct {
	ProfileID string
	Priority  int
	Username  string
	Password  string
	Enabled   bool
}

type CredentialVault struct {
	db   *storage.DB
	aead cipher.AEAD
}

func NewCredentialVault(db *storage.DB, encodedKey string) (*CredentialVault, error) {
	if db == nil {
		return nil, errors.New("credential database is required")
	}
	key, err := decodeMasterKey(encodedKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, fmt.Errorf("create credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create credential AEAD: %w", err)
	}
	return &CredentialVault{db: db, aead: aead}, nil
}

func decodeMasterKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("ATLAS_NODE_CREDENTIAL_MASTER_KEY is not configured")
	}
	var (
		key []byte
		err error
	)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		key, err = encoding.DecodeString(value)
		if err == nil {
			break
		}
	}
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, errors.New("node credential master key must be base64-encoded 32 bytes")
	}
	return key, nil
}

func (v *CredentialVault) Create(input CredentialInput) (*api.NodeCredentialProfile, error) {
	input.ProfileID = strings.TrimSpace(input.ProfileID)
	input.Username = strings.TrimSpace(input.Username)
	if !profileIDPattern.MatchString(input.ProfileID) {
		return nil, errors.New("profile_id must use lowercase letters, digits, and hyphens")
	}
	if input.Priority < 1 || input.Priority > 10000 {
		return nil, errors.New("priority must be between 1 and 10000")
	}
	if input.Username == "" || len([]rune(input.Username)) > 128 {
		return nil, errors.New("username is required and must not exceed 128 characters")
	}
	if input.Password == "" || len(input.Password) > 4096 {
		return nil, errors.New("password is required and must not exceed 4096 bytes")
	}
	ciphertext, err := v.encrypt(input.ProfileID, credentialPayload{Username: input.Username, Password: input.Password})
	if err != nil {
		return nil, err
	}
	record := &api.NodeCredentialProfile{
		ProfileID: input.ProfileID, Priority: input.Priority, AuthType: "password",
		UsernameMasked: "••••••", Ciphertext: ciphertext, KeyVersion: vaultKeyVersion, Enabled: input.Enabled,
	}
	if err := v.db.Create(record).Error; err != nil {
		return nil, err
	}
	return record, nil
}

func (v *CredentialVault) Delete(profileID string) error {
	result := v.db.Where("profile_id = ?", strings.TrimSpace(profileID)).Delete(&api.NodeCredentialProfile{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (v *CredentialVault) List() ([]api.NodeCredentialProfile, error) {
	var rows []api.NodeCredentialProfile
	err := v.db.Order("priority ASC").Order("profile_id ASC").Find(&rows).Error
	return rows, err
}

func (v *CredentialVault) Statuses() ([]CredentialProfileStatus, error) {
	rows, err := v.List()
	if err != nil {
		return nil, err
	}
	result := make([]CredentialProfileStatus, 0, len(rows))
	for _, row := range rows {
		status := "disabled"
		if row.Enabled {
			status = "ready"
		}
		result = append(result, CredentialProfileStatus{
			ID: row.ProfileID, Priority: row.Priority, Username: row.UsernameMasked,
			AuthType: row.AuthType, SecretProvider: "encrypted_db", Enabled: row.Enabled,
			SecretAvailable: row.Enabled, Status: status,
		})
	}
	return result, nil
}

func (v *CredentialVault) Profiles() ([]config.NodeCredentialProfile, error) {
	rows, err := v.List()
	if err != nil {
		return nil, err
	}
	result := make([]config.NodeCredentialProfile, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		payload, err := v.decrypt(row)
		if err != nil {
			return nil, fmt.Errorf("decrypt credential profile %q: %w", row.ProfileID, err)
		}
		result = append(result, config.NodeCredentialProfile{
			ID: row.ProfileID, Priority: row.Priority, Username: payload.Username,
			AuthType: row.AuthType, SecretRef: vaultRefPrefix + row.ProfileID, Enabled: true,
		})
		payload.Username = ""
		payload.Password = ""
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Priority == result[j].Priority {
			return result[i].ID < result[j].ID
		}
		return result[i].Priority < result[j].Priority
	})
	return result, nil
}

func (v *CredentialVault) Resolve(secretRef string) ([]byte, error) {
	profileID := strings.TrimPrefix(secretRef, vaultRefPrefix)
	if profileID == secretRef || profileID == "" {
		return nil, errors.New("unsupported credential reference")
	}
	var row api.NodeCredentialProfile
	if err := v.db.Where("profile_id = ? AND enabled = ?", profileID, true).First(&row).Error; err != nil {
		return nil, err
	}
	payload, err := v.decrypt(row)
	if err != nil {
		return nil, err
	}
	secret := []byte(payload.Password)
	payload.Password = ""
	payload.Username = ""
	return secret, nil
}

func (v *CredentialVault) encrypt(profileID string, payload credentialPayload) ([]byte, error) {
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate credential nonce: %w", err)
	}
	return append(nonce, v.aead.Seal(nil, nonce, plaintext, []byte(profileID+"|"+vaultKeyVersion))...), nil
}

func (v *CredentialVault) decrypt(row api.NodeCredentialProfile) (credentialPayload, error) {
	var payload credentialPayload
	if row.KeyVersion != vaultKeyVersion || len(row.Ciphertext) <= v.aead.NonceSize() {
		return payload, errors.New("unsupported or invalid credential ciphertext")
	}
	nonce := row.Ciphertext[:v.aead.NonceSize()]
	plaintext, err := v.aead.Open(nil, nonce, row.Ciphertext[v.aead.NonceSize():], []byte(row.ProfileID+"|"+row.KeyVersion))
	if err != nil {
		return payload, errors.New("credential authentication failed")
	}
	defer clear(plaintext)
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return credentialPayload{}, errors.New("invalid credential payload")
	}
	return payload, nil
}

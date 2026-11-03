package assetaudit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	keyVersion = "aes-256-gcm-v1"
	aadPrefix  = "atlas|asset-audit|" + keyVersion + "|"
)

// secretCipher seals and opens the credential fields of the asset audit
// configuration with AES-256-GCM under the platform secret master key. The
// field name is bound as additional authenticated data so a ciphertext cannot
// be replayed into a different slot.
type secretCipher struct {
	aead cipher.AEAD
}

func newSecretCipher(encodedKey string) (*secretCipher, error) {
	value := strings.TrimSpace(encodedKey)
	if value == "" {
		return nil, errors.New("platform secret master key is not configured")
	}
	var key []byte
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		decoded, err := encoding.DecodeString(value)
		if err == nil && len(decoded) == 32 {
			key = decoded
			break
		}
	}
	if len(key) == 0 {
		return nil, errors.New("platform secret master key must be base64-encoded 32 bytes")
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, fmt.Errorf("create asset audit cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create asset audit AEAD: %w", err)
	}
	return &secretCipher{aead: aead}, nil
}

func (c *secretCipher) seal(field, plaintext string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, []byte(plaintext), []byte(aadPrefix+field)), nil
}

func (c *secretCipher) open(field string, version string, ciphertext []byte) (string, error) {
	if version != keyVersion || len(ciphertext) <= c.aead.NonceSize() {
		return "", errors.New("unsupported or invalid asset audit ciphertext")
	}
	size := c.aead.NonceSize()
	plain, err := c.aead.Open(nil, ciphertext[:size], ciphertext[size:], []byte(aadPrefix+field))
	if err != nil {
		return "", fmt.Errorf("decrypt asset audit %s", field)
	}
	return string(plain), nil
}

// isConfigured reports whether a sealed value is present without decrypting it.
func isConfigured(ciphertext []byte) bool { return len(ciphertext) > 0 }

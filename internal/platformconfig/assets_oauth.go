package platformconfig

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"atlas/pkg/api"
	"atlas/pkg/opsplatform"
)

func assetAuthType(value string) string {
	if value == "" {
		return "api_key"
	}
	return value
}

func platformCertPool(value string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	rest := []byte(strings.TrimSpace(value))
	count := 0
	for len(rest) > 0 {
		if !strings.HasPrefix(string(rest), "-----BEGIN CERTIFICATE-----") {
			return nil, errors.New("platform CA field accepts only certificate PEM blocks")
		}
		block, next := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) > 0 {
			return nil, errors.New("platform CA field accepts only certificate PEM blocks")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid platform CA certificate")
		}
		pool.AddCert(cert)
		count++
		rest = []byte(strings.TrimSpace(string(next)))
	}
	if count == 0 {
		return nil, errors.New("invalid platform CA certificate PEM")
	}
	return pool, nil
}

func (s *AssetSource) saveOAuthConfig(row *api.LXOPAssetConfig, in AssetConfigInput) error {
	u, err := opsplatform.ParseURL(in.TokenURL)
	if err != nil {
		return err
	}
	if row.TokenURL != "" && row.AuthType == "oauth_client_credentials" {
		old, oldErr := opsplatform.ParseURL(row.TokenURL)
		if (oldErr != nil || !strings.EqualFold(old.Host, u.Host)) && strings.TrimSpace(in.ClientSecret) == "" {
			return errors.New("client_secret required when changing platform origin")
		}
	}
	for _, endpoint := range []string{in.OpsHostURL, in.AssetMachineURL} {
		e, err := opsplatform.ParseURL(endpoint)
		if err != nil || !strings.EqualFold(e.Host, u.Host) {
			return errors.New("OAuth asset endpoints must share token HTTPS origin")
		}
	}
	if in.CACertPEM != "" {
		if _, err := platformCertPool(in.CACertPEM); err != nil {
			return err
		}
	}
	if row.AuthType != "oauth_client_credentials" && (strings.TrimSpace(in.ClientID) == "" || strings.TrimSpace(in.ClientSecret) == "") {
		return errors.New("client_id and client_secret required for initial OAuth configuration")
	}
	if strings.TrimSpace(in.ClientID) != "" && in.ClientID != row.ClientID && strings.TrimSpace(in.ClientSecret) == "" {
		return errors.New("client_secret required when changing client_id")
	}
	if strings.TrimSpace(in.ClientID) != "" {
		row.ClientID = strings.TrimSpace(in.ClientID)
	}
	if strings.TrimSpace(in.ClientSecret) != "" {
		nonce := make([]byte, s.aead.NonceSize())
		if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
			return errors.New("encrypt platform credential failed")
		}
		row.ClientSecretCiphertext = append(nonce, s.aead.Seal(nil, nonce, []byte(in.ClientSecret), []byte("atlas|ops-platform|client-secret|v1"))...)
	}
	if row.ClientID == "" || len(row.ClientSecretCiphertext) == 0 {
		return errors.New("platform credentials are not configured")
	}
	row.TokenURL, row.CACertPEM = in.TokenURL, in.CACertPEM
	// Keep the legacy NOT NULL column and old API-key configuration compatible.
	if row.APIKeyCiphertext == nil {
		row.APIKeyCiphertext = []byte{}
	}
	if row.KeyVersion == "" {
		row.KeyVersion = lxopKeyVersion
	}
	return nil
}

func (s *AssetSource) platformClient(config api.LXOPAssetConfig) (*opsplatform.Client, error) {
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{config.TokenURL, config.ClientID, string(config.ClientSecretCiphertext), config.DataCenterID, config.CACertPEM, fmt.Sprint(config.InsecureSkipVerify)}, "|"))))
	if s.oauth != nil && s.oauthFingerprint == fingerprint {
		return s.oauth, nil
	}
	if len(config.ClientSecretCiphertext) <= s.aead.NonceSize() {
		return nil, errors.New("invalid platform credential ciphertext")
	}
	secret, err := s.aead.Open(nil, config.ClientSecretCiphertext[:s.aead.NonceSize()], config.ClientSecretCiphertext[s.aead.NonceSize():], []byte("atlas|ops-platform|client-secret|v1"))
	if err != nil {
		return nil, errors.New("decrypt platform credential failed")
	}
	defer clear(secret)
	client := s.client(config.InsecureSkipVerify)
	if config.CACertPEM != "" {
		base, ok := client.Transport.(*http.Transport)
		if !ok {
			return nil, errors.New("platform CA requires HTTP transport")
		}
		transport := base.Clone()
		pool, err := platformCertPool(config.CACertPEM)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, InsecureSkipVerify: config.InsecureSkipVerify} // #nosec G402 -- explicit development compatibility, visible in config.
		copy := *client
		copy.Transport = transport
		client = &copy
	}
	s.oauth, err = opsplatform.New(opsplatform.Config{TokenURL: config.TokenURL, ClientID: config.ClientID, ClientSecret: string(secret), DataCenterID: config.DataCenterID}, client)
	if err == nil {
		s.oauthFingerprint = fingerprint
	}
	return s.oauth, err
}

func (s *AssetSource) fetchSources(ctx context.Context, config api.LXOPAssetConfig) ([]LXOPAsset, []LXOPAsset, error) {
	if assetAuthType(config.AuthType) == "api_key" {
		key, err := s.decryptAPIKey(config)
		if err != nil {
			return nil, nil, err
		}
		ops, err := s.fetch(ctx, config.OpsHostURL, config.DataCenterID, key, config.InsecureSkipVerify)
		if err != nil {
			return nil, nil, fmt.Errorf("ops host API: %w", err)
		}
		machines, err := s.fetch(ctx, config.AssetMachineURL, config.DataCenterID, key, config.InsecureSkipVerify)
		return ops, machines, err
	}
	if config.AuthType != "oauth_client_credentials" {
		return nil, nil, errors.New("unsupported asset authentication")
	}
	client, err := s.platformClient(config)
	if err != nil {
		return nil, nil, err
	}
	results := make([][]LXOPAsset, 2)
	for i, endpoint := range []string{config.OpsHostURL, config.AssetMachineURL} {
		raws, err := client.List(ctx, endpoint)
		if err != nil {
			return nil, nil, fmt.Errorf("platform source %d: %w", i+1, err)
		}
		for _, raw := range raws {
			var item LXOPAsset
			if json.Unmarshal(raw, &item) != nil {
				return nil, nil, errors.New("invalid platform asset fields")
			}
			if item.DataCenterID != "" && item.DataCenterID != config.DataCenterID {
				return nil, nil, errors.New("platform asset outside selected data center")
			}
			item.DataCenterID = config.DataCenterID
			if i == 0 {
				var host struct {
					DeviceType string     `json:"deviceType"`
					Machine    *LXOPAsset `json:"machine"`
				}
				if json.Unmarshal(raw, &host) != nil {
					return nil, nil, errors.New("invalid platform host fields")
				}
				item.Type = host.DeviceType
				if host.Machine != nil {
					if host.Machine.DataCenterID != "" && host.Machine.DataCenterID != config.DataCenterID {
						return nil, nil, errors.New("linked machine outside selected data center")
					}
					if item.SerialNumber != "" && host.Machine.SerialNumber != "" && item.SerialNumber != host.Machine.SerialNumber {
						return nil, nil, errors.New("host and linked machine identities conflict")
					}
					item.Model, item.Type, item.State = host.Machine.Model, host.Machine.Type, host.Machine.State
				}
			}
			results[i] = append(results[i], item)
		}
	}
	return results[0], results[1], nil
}

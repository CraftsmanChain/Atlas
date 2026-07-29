package nodeaccess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"atlas/pkg/config"
)

var (
	ErrAuthenticationRejected = errors.New("authentication rejected")
	ErrHostIdentityFailed     = errors.New("host identity verification failed")
	ErrNetworkUnavailable     = errors.New("network unavailable")
)

type SecretResolver interface {
	Available(secretRef string) bool
	Resolve(secretRef string) ([]byte, error)
}

type Authenticator interface {
	Authenticate(ctx context.Context, node, username, authType string, secret []byte) error
}

type EnvSecretResolver struct{}

func (EnvSecretResolver) Available(secretRef string) bool {
	if !strings.HasPrefix(secretRef, "env:") {
		return false
	}
	_, ok := os.LookupEnv(strings.TrimPrefix(secretRef, "env:"))
	return ok
}

func (EnvSecretResolver) Resolve(secretRef string) ([]byte, error) {
	if !strings.HasPrefix(secretRef, "env:") {
		return nil, fmt.Errorf("unsupported secret provider")
	}
	value, ok := os.LookupEnv(strings.TrimPrefix(secretRef, "env:"))
	if !ok || value == "" {
		return nil, fmt.Errorf("secret unavailable")
	}
	return []byte(value), nil
}

type Service struct {
	cfg      config.NodeAccessConfig
	resolver SecretResolver
	vault    *CredentialVault
	now      func() time.Time
}

func NewService(cfg config.NodeAccessConfig, resolver SecretResolver) *Service {
	if resolver == nil {
		resolver = EnvSecretResolver{}
	}
	return &Service{cfg: cfg, resolver: resolver, now: time.Now}
}

func NewServiceWithVault(cfg config.NodeAccessConfig, resolver SecretResolver, vault *CredentialVault) *Service {
	service := NewService(cfg, resolver)
	service.vault = vault
	return service
}

func (s *Service) Overview() Overview {
	profiles := sortedProfiles(s.cfg.CredentialProfiles)
	statuses := make([]CredentialProfileStatus, 0, len(profiles))
	ready := 0
	for _, profile := range profiles {
		provider := "unsupported"
		if strings.HasPrefix(profile.SecretRef, "env:") {
			provider = "env"
		}
		available := profile.Enabled && s.resolver.Available(profile.SecretRef)
		status := "disabled"
		if profile.Enabled && available {
			status, ready = "ready", ready+1
		} else if profile.Enabled {
			status = "secret_unavailable"
		}
		statuses = append(statuses, CredentialProfileStatus{
			ID: profile.ID, Priority: profile.Priority, Username: "••••••",
			AuthType: profile.AuthType, SecretProvider: provider, Enabled: profile.Enabled,
			SecretAvailable: available, Status: status,
		})
	}
	if s.vault != nil {
		stored, err := s.vault.Statuses()
		if err == nil {
			statuses = append(statuses, stored...)
			for _, profile := range stored {
				if profile.Enabled && profile.SecretAvailable {
					ready++
				}
			}
		}
	}
	sort.SliceStable(statuses, func(i, j int) bool {
		if statuses[i].Priority == statuses[j].Priority {
			return statuses[i].ID < statuses[j].ID
		}
		return statuses[i].Priority < statuses[j].Priority
	})
	status := "disabled"
	if s.cfg.Enabled && ready == 0 {
		status = "credentials_missing"
	} else if s.cfg.Enabled {
		status = "ready_no_transport"
	}
	return Overview{
		SkillID: firstNonEmpty(s.cfg.SkillID, SkillID), SkillVersion: firstNonEmpty(s.cfg.SkillVersion, SkillVersion),
		Status: status, Enabled: s.cfg.Enabled, ExecutionEnabled: false,
		NoArbitraryShell: true, NoChangeExecuted: true, EncryptionReady: s.vault != nil,
		DefaultReadOnlyMode: true,
		Budget: Budget{
			ConnectTimeoutSeconds: durationSeconds(s.cfg.ConnectTimeout, 5*time.Second),
			CommandTimeoutSeconds: durationSeconds(s.cfg.CommandTimeout, 10*time.Second),
			MaxOutputBytes:        s.cfg.MaxOutputBytes, MaxConcurrentNodes: s.cfg.MaxConcurrentNodes,
			MaxCommandsPerNode: s.cfg.MaxCommandsPerNode, MaxLogLines: 2000, DefaultWindowMinutes: 30,
		},
		CredentialProfiles: statuses, Skills: skillCatalog(), Commands: commandCatalog(),
		AlertPolicies: alertEvidencePolicies(),
		Boundaries: []Text{
			{ZH: "低负载只读证据按注册命令和固定预算默认采集，无需逐次计划或确认；每次采集均保留脱敏审计。", EN: "Low-impact read-only evidence is collected by default through registered commands and fixed budgets without per-run planning or confirmation; every collection retains a redacted audit record."},
			{ZH: "密码和私钥只通过受控密钥引用解析，永不进入接口、日志或证据。", EN: "Passwords and private keys resolve only through controlled secret references and never enter APIs, logs, or evidence."},
			{ZH: "诊断、性能或功能测试、服务或节点重启、GPU 重置及 GPU 任务终止必须指定节点和操作并由人工确认。", EN: "Diagnostics, performance or functional tests, service or node restarts, GPU resets, and GPU workload termination require an exact node and operation plus human confirmation."},
		},
		GeneratedAt: s.now(),
	}
}

func (s *Service) Authenticate(ctx context.Context, node string, authenticator Authenticator) AuthenticationResult {
	result := AuthenticationResult{Node: node, Attempts: []CredentialAttempt{}, NoCredentialDisclosed: true}
	profiles := append([]config.NodeCredentialProfile(nil), s.cfg.CredentialProfiles...)
	if s.vault != nil {
		stored, err := s.vault.Profiles()
		if err != nil {
			result.Status, result.AlertRequired = "credential_store_failed", true
			return result
		}
		profiles = append(profiles, stored...)
	}
	for _, profile := range sortedProfiles(profiles) {
		if !profile.Enabled {
			continue
		}
		var (
			secret []byte
			err    error
		)
		if strings.HasPrefix(profile.SecretRef, vaultRefPrefix) && s.vault != nil {
			secret, err = s.vault.Resolve(profile.SecretRef)
		} else {
			secret, err = s.resolver.Resolve(profile.SecretRef)
		}
		if err != nil {
			result.Attempts = append(result.Attempts, CredentialAttempt{ProfileID: profile.ID, Outcome: "secret_unavailable"})
			continue
		}
		err = authenticator.Authenticate(ctx, node, profile.Username, profile.AuthType, secret)
		clear(secret)
		switch {
		case err == nil:
			result.Status, result.CredentialProfileID = "authenticated", profile.ID
			result.Attempts = append(result.Attempts, CredentialAttempt{ProfileID: profile.ID, Outcome: "success"})
			return result
		case errors.Is(err, ErrAuthenticationRejected):
			result.Attempts = append(result.Attempts, CredentialAttempt{ProfileID: profile.ID, Outcome: "rejected"})
		case errors.Is(err, ErrHostIdentityFailed):
			result.Status = "host_identity_failed"
			result.Attempts = append(result.Attempts, CredentialAttempt{ProfileID: profile.ID, Outcome: "host_identity_failed"})
			result.AlertRequired = true
			return result
		default:
			result.Status = "network_failed"
			result.Attempts = append(result.Attempts, CredentialAttempt{ProfileID: profile.ID, Outcome: "network_failed"})
			result.AlertRequired = true
			return result
		}
	}
	result.Status, result.AlertRequired = "credential_exhausted", true
	return result
}

func (s *Service) ExecuteReadOnly(ctx context.Context, node string, commands []ReadOnlyCommand, executor ReadOnlyExecutor) ExecutionResult {
	result := ExecutionResult{
		Node: node, Attempts: []CredentialAttempt{}, Outcomes: []CommandOutcome{},
		NoCredentialDisclosed: true,
	}
	if executor == nil {
		result.Status = "transport_unavailable"
		return result
	}
	maxCommands := s.cfg.MaxCommandsPerNode
	if maxCommands <= 0 {
		maxCommands = 6
	}
	if len(commands) == 0 || len(commands) > maxCommands {
		result.Status = "command_budget_rejected"
		return result
	}
	timeout, err := time.ParseDuration(s.cfg.CommandTimeout)
	if err != nil || timeout <= 0 {
		timeout = 10 * time.Second
	}
	profiles := append([]config.NodeCredentialProfile(nil), s.cfg.CredentialProfiles...)
	if s.vault != nil {
		stored, vaultErr := s.vault.Profiles()
		if vaultErr != nil {
			result.Status = "credential_store_failed"
			return result
		}
		profiles = append(profiles, stored...)
	}
	for _, profile := range sortedProfiles(profiles) {
		if !profile.Enabled {
			continue
		}
		var secret []byte
		if strings.HasPrefix(profile.SecretRef, vaultRefPrefix) && s.vault != nil {
			secret, err = s.vault.Resolve(profile.SecretRef)
		} else {
			secret, err = s.resolver.Resolve(profile.SecretRef)
		}
		if err != nil {
			result.Attempts = append(result.Attempts, CredentialAttempt{ProfileID: profile.ID, Outcome: "secret_unavailable"})
			continue
		}
		outcomes, executeErr := executor.Execute(ctx, node, profile.Username, profile.AuthType, secret, commands, timeout)
		clear(secret)
		switch {
		case executeErr == nil:
			result.Status, result.CredentialProfileID, result.Outcomes = "completed", profile.ID, outcomes
			result.Attempts = append(result.Attempts, CredentialAttempt{ProfileID: profile.ID, Outcome: "success"})
			return result
		case errors.Is(executeErr, ErrAuthenticationRejected):
			result.Attempts = append(result.Attempts, CredentialAttempt{ProfileID: profile.ID, Outcome: "rejected"})
		case errors.Is(executeErr, ErrHostIdentityFailed):
			result.Status = "host_identity_failed"
			result.Attempts = append(result.Attempts, CredentialAttempt{ProfileID: profile.ID, Outcome: "host_identity_failed"})
			return result
		default:
			result.Status = "network_failed"
			result.Attempts = append(result.Attempts, CredentialAttempt{ProfileID: profile.ID, Outcome: "network_failed"})
			return result
		}
	}
	result.Status = "credential_exhausted"
	return result
}

func sortedProfiles(profiles []config.NodeCredentialProfile) []config.NodeCredentialProfile {
	result := append([]config.NodeCredentialProfile(nil), profiles...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Priority == result[j].Priority {
			return result[i].ID < result[j].ID
		}
		return result[i].Priority < result[j].Priority
	})
	return result
}

func durationSeconds(value string, fallback time.Duration) int {
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		parsed = fallback
	}
	return int(parsed.Seconds())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

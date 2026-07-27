package nodeaccess

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

var (
	ErrNodeAccessDisabled      = errors.New("node access connectivity checks are disabled")
	ErrConnectivityUnavailable = errors.New("known-host SSH connectivity is unavailable")
	ErrNodeNotManaged          = errors.New("node is not an active Atlas-managed asset")
)

type ConnectivityService struct {
	db            *storage.DB
	access        *Service
	authenticator Authenticator
	enabled       bool
	now           func() time.Time
}

func NewConnectivityService(db *storage.DB, access *Service, authenticator Authenticator, enabled bool) *ConnectivityService {
	return &ConnectivityService{db: db, access: access, authenticator: authenticator, enabled: enabled, now: time.Now}
}

func (s *ConnectivityService) Enabled() bool { return s != nil && s.enabled }

func (s *ConnectivityService) KnownHostsReady() bool {
	return s != nil && s.authenticator != nil
}

func (s *ConnectivityService) Check(ctx context.Context, node string) (*api.NodeAccessCheck, error) {
	if !s.Enabled() {
		return nil, ErrNodeAccessDisabled
	}
	if !s.KnownHostsReady() {
		return nil, ErrConnectivityUnavailable
	}
	node = strings.TrimSpace(node)
	if net.ParseIP(node) == nil {
		return nil, ErrNodeNotManaged
	}
	var managed api.GPUNode
	if err := s.db.Where("node_ip = ? AND lifecycle <> ?", node, "retired").First(&managed).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotManaged
		}
		return nil, err
	}
	startedAt := s.now()
	result := s.access.Authenticate(ctx, node, s.authenticator)
	finishedAt := s.now()
	attempts := make(api.StringList, 0, len(result.Attempts))
	for _, attempt := range result.Attempts {
		attempts = append(attempts, attempt.ProfileID+":"+attempt.Outcome)
	}
	record := &api.NodeAccessCheck{
		NodeIP: node, Status: result.Status, CredentialProfileID: result.CredentialProfileID,
		Attempts: attempts, AlertRequired: result.AlertRequired,
		NoCredentialDisclosed: result.NoCredentialDisclosed, NoCommandExecuted: true,
		StartedAt: startedAt, FinishedAt: finishedAt,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(record).Error; err != nil {
			return err
		}
		return syncConnectivityIssue(tx, record, finishedAt)
	}); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *ConnectivityService) List(limit int) ([]api.NodeAccessCheck, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	var rows []api.NodeAccessCheck
	err := s.db.Order("id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func syncConnectivityIssue(tx *gorm.DB, check *api.NodeAccessCheck, now time.Time) error {
	key := "node_access_auth:" + check.NodeIP
	var issue api.PlatformIssue
	err := tx.Where("issue_key = ?", key).First(&issue).Error
	if check.Status == "authenticated" {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if issue.DetectionState == "active" {
			updates := map[string]any{"detection_state": "cleared", "source_recovered_at": &now}
			if issue.Status == "open" || issue.Status == "in_progress" {
				updates["status"], updates["resolved_at"] = "resolved", &now
			}
			return tx.Model(&issue).Updates(updates).Error
		}
		return nil
	}
	title := fmt.Sprintf("Node authentication check failed on %s", check.NodeIP)
	description := fmt.Sprintf("known-host SSH authentication check status=%s; no command executed", check.Status)
	severity := "warning"
	if check.Status == "host_identity_failed" {
		severity = "critical"
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&api.PlatformIssue{
			IssueKey: key, Category: "access", IssueType: "node_authentication",
			Title: title, Description: description, EntityType: "node", EntityKey: check.NodeIP,
			NodeIP: check.NodeIP, Severity: severity, Status: "open", DetectionState: "active",
			DetectionSource: "node_access_auth", SourceRecordID: check.ID,
			FirstDetectedAt: now, LastDetectedAt: now,
		}).Error
	}
	if err != nil {
		return err
	}
	updates := map[string]any{
		"title": title, "description": description, "severity": severity,
		"detection_state": "active", "source_record_id": check.ID,
		"last_detected_at": now, "source_recovered_at": nil,
	}
	if issue.DetectionState == "cleared" && issue.Status == "resolved" {
		updates["status"], updates["resolved_at"] = "open", nil
	}
	return tx.Model(&issue).Updates(updates).Error
}

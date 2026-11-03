package assetaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/notifier"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

const (
	// KindDelivery is the delivered-node versus asset consistency check.
	KindDelivery = "delivery_check"
	// KindScan is the live-IP subnet sweep.
	KindScan = "ip_scan"

	triggerScheduled = "scheduled"
	triggerManual    = "manual"
)

// Service owns the delivery verification and live-IP discovery jobs.
type Service struct {
	db         *storage.DB
	cipher     *secretCipher
	sheet      *sheetClient
	notifier   *notifier.FeishuNotifier
	probePort  func(ctx context.Context, address string, timeout time.Duration) bool
	loginSSH   func(ctx context.Context, host string, credentials []sshCredential, timeout time.Duration) bool
	sweepHosts func(ctx context.Context, targets []scanTarget, timeoutMS int) (map[string]bool, error)
	now        func() time.Time
	executing  sync.Map
}

// dialTCP reports whether a TCP connection to address is established within the
// timeout.
func dialTCP(ctx context.Context, address string, timeout time.Duration) bool {
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

// NewService builds the service. It returns an error when the platform secret
// master key is unavailable, because credentials must never be stored in the
// clear.
func NewService(db *storage.DB, encodedMasterKey string, feishu *notifier.FeishuNotifier) (*Service, error) {
	if db == nil {
		return nil, errors.New("asset audit database is required")
	}
	cipher, err := newSecretCipher(encodedMasterKey)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, cipher: cipher, sheet: newSheetClient(), notifier: feishu, probePort: dialTCP, now: time.Now}, nil
}

// Run executes the daily schedule. It re-reads the stored configuration every
// cycle so schedule and enablement changes take effect without a restart.
func (s *Service) Run(ctx context.Context, opts config.AssetAuditOptions) {
	if err := s.EnsureConfig(opts); err != nil {
		log.Printf("[AssetAudit] failed to seed initial configuration: %v", err)
	}
	for {
		hour, location := s.schedule()
		next := nextDailyAt(s.now(), hour, location)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.runScheduled(ctx)
		}
	}
}

func (s *Service) schedule() (int, *time.Location) {
	hour := 5
	location := time.Local
	row, err := s.GetConfig()
	if err != nil {
		return hour, location
	}
	if row.ScheduleHour >= 0 && row.ScheduleHour <= 23 {
		hour = row.ScheduleHour
	}
	if parsed, loadErr := time.LoadLocation(row.Timezone); loadErr == nil {
		location = parsed
	}
	return hour, location
}

func nextDailyAt(now time.Time, hour int, location *time.Location) time.Time {
	local := now.In(location)
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, 0, 0, 0, location)
	if !next.After(local) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func (s *Service) runScheduled(ctx context.Context) {
	row, err := s.GetConfig()
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("[AssetAudit] scheduled run skipped: %v", err)
		}
		return
	}
	if !row.Enabled {
		return
	}
	for _, kind := range []string{KindDelivery, KindScan} {
		if !kindEnabled(row, kind) {
			continue
		}
		if _, err := s.Trigger(ctx, kind, triggerScheduled); err != nil {
			log.Printf("[AssetAudit] scheduled %s not started: %v", kind, err)
		}
	}
}

func kindEnabled(row api.AssetAuditConfig, kind string) bool {
	switch kind {
	case KindDelivery:
		return row.DeliveryEnabled
	case KindScan:
		return row.ScanEnabled
	default:
		return false
	}
}

// Trigger records a run and executes it in the background. It returns
// immediately so an HTTP caller is never blocked by a long sweep.
func (s *Service) Trigger(ctx context.Context, kind, trigger string) (api.AssetAuditRun, error) {
	if kind != KindDelivery && kind != KindScan {
		return api.AssetAuditRun{}, fmt.Errorf("unsupported audit kind %q", kind)
	}
	if _, loaded := s.executing.LoadOrStore(kind, true); loaded {
		return api.AssetAuditRun{}, fmt.Errorf("%s is already running", kind)
	}
	run := api.AssetAuditRun{Kind: kind, Status: "running", Trigger: trigger, StartedAt: s.now()}
	if err := s.db.Create(&run).Error; err != nil {
		s.executing.Delete(kind)
		return run, err
	}
	go func() {
		defer s.executing.Delete(kind)
		s.execute(context.WithoutCancel(ctx), run)
	}()
	return run, nil
}

func (s *Service) execute(ctx context.Context, run api.AssetAuditRun) {
	row, err := s.GetConfig()
	if err != nil {
		s.fail(run, fmt.Errorf("load configuration: %w", err))
		return
	}
	if !row.Enabled || !kindEnabled(row, run.Kind) {
		s.fail(run, errors.New("this check is disabled in the asset audit configuration"))
		return
	}
	appSecret, notifySecret, err := s.resolvedSecrets(row)
	if err != nil {
		s.fail(run, err)
		return
	}
	if row.DeliverySheetURL == "" || row.FeishuAppID == "" || appSecret == "" {
		s.fail(run, errors.New("delivery sheet URL and Feishu application credentials are required"))
		return
	}
	fetch := configForFetch{SheetURL: row.DeliverySheetURL, AppID: row.FeishuAppID, AppSecret: appSecret}
	now := s.now()

	var summaryJSON, findingsJSON []byte
	var title, template, content string
	findingCount := 0
	switch run.Kind {
	case KindDelivery:
		summary, findings, checkErr := s.runDeliveryCheck(ctx, fetch, now)
		if checkErr != nil {
			s.fail(run, checkErr)
			return
		}
		// An informational serial notice must never raise a notification on its
		// own; only real discrepancies count.
		findingCount = deliveryDifferenceCount(findings)
		summaryJSON, findingsJSON = mustJSON(summary), mustJSON(findings)
		title, template, content = deliveryNotification(summary, findings)
	default:
		credentials, credentialErr := s.resolveSSHCredentials(row)
		if credentialErr != nil {
			s.fail(run, credentialErr)
			return
		}
		summary, findings, scanErr := s.runScan(ctx, fetch, now, row.ScanTimeoutMS, credentials)
		if scanErr != nil {
			s.fail(run, scanErr)
			return
		}
		findingCount = len(findings)
		summaryJSON, findingsJSON = mustJSON(summary), mustJSON(findings)
		title, template, content = scanNotification(summary, findings)
	}

	notified, notifyError := false, ""
	if shouldNotify(row, findingCount) {
		if err := s.sendCard(row, notifySecret, title, template, content); err != nil {
			notifyError = err.Error()
			log.Printf("[AssetAudit] %s notification failed: %v", run.Kind, err)
		} else {
			notified = true
		}
	}
	finished := s.now()
	updates := map[string]any{
		"status": "success", "finished_at": finished, "summary_json": string(summaryJSON),
		"findings_json": string(findingsJSON), "notified": notified, "notify_error": notifyError,
	}
	if err := s.db.Model(&api.AssetAuditRun{}).Where("id = ?", run.ID).Updates(updates).Error; err != nil {
		log.Printf("[AssetAudit] failed to persist run %d: %v", run.ID, err)
	}
}

func (s *Service) fail(run api.AssetAuditRun, cause error) {
	finished := s.now()
	if err := s.db.Model(&api.AssetAuditRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status": "failed", "finished_at": finished, "error_message": cause.Error(),
	}).Error; err != nil {
		log.Printf("[AssetAudit] failed to persist run %d failure: %v", run.ID, err)
	}
	log.Printf("[AssetAudit] %s run %d failed: %v", run.Kind, run.ID, cause)
}

func (s *Service) sendCard(row api.AssetAuditConfig, notifySecret, title, template, content string) error {
	if strings.TrimSpace(row.NotifyWebhookURL) != "" {
		return notifier.SendBotCard(config.FeishuBotConfig{
			Enabled: true, WebhookURL: row.NotifyWebhookURL,
			EnableSignature: notifySecret != "", Secret: notifySecret,
		}, title, template, content)
	}
	if s.notifier == nil {
		return errors.New("no notification target is configured")
	}
	return s.notifier.SendCard(title, template, content)
}

// shouldNotify reports whether a finished run must reach the Feishu group. By
// default Atlas only notifies when the run found anomalies, so a nightly clean
// result stays silent; operators can opt into a notice on every run to confirm
// the check itself ran.
func shouldNotify(row api.AssetAuditConfig, findingCount int) bool {
	if !row.NotifyEnabled {
		return false
	}
	return findingCount > 0 || row.NotifyAlways
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("null")
	}
	return encoded
}

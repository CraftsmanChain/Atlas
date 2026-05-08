package analyzer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/notifier"
	"atlas/pkg/storage"
)

// AlertAnalyzer 负责处理告警分析与通知
type AlertAnalyzer struct {
	db          *storage.DB
	notifier    *notifier.FeishuNotifier
	recentCache map[string]*api.AlertEvent // 预留缓存，当前不做重复告警折叠
	mu          sync.RWMutex
}

func NewAlertAnalyzer(db *storage.DB, notifier *notifier.FeishuNotifier) *AlertAnalyzer {
	analyzer := &AlertAnalyzer{
		db:          db,
		notifier:    notifier,
		recentCache: make(map[string]*api.AlertEvent),
	}

	// 启动定期清理缓存的 goroutine，防止内存泄漏 (暂时设置为1小时清理)
	go analyzer.cleanupCache()
	return analyzer
}

// Process 接收一条告警并进行处理
func (a *AlertAnalyzer) Process(event *api.AlertEvent) error {
	now := time.Now()
	if event.Timestamp.IsZero() {
		event.Timestamp = now
	}
	event.LastSeenAt = now
	event.IsProcessed = true

	// 当前暂不识别重复告警，所有入站事件都按独立告警处理。
	event.ID = a.generateEventID(event, now)
	event.RepeatCount = 1

	// 存入数据库
	if err := a.db.Create(event).Error; err != nil {
		log.Printf("[Analyzer] Failed to save new alert to DB: %v", err)
		return err
	} else {
		log.Printf("[Analyzer] Processed and saved new alert: [%s] %s", event.Level, event.Message)
	}

	// 发送首次告警通知到飞书
	if a.notifier != nil {
		a.notifier.SendAlert(event, false)
	}
	return nil
}

// generateEventID 为每条入站告警生成唯一 ID，避免当前阶段错误折叠同机不同卡告警。
func (a *AlertAnalyzer) generateEventID(event *api.AlertEvent, now time.Time) string {
	data := fmt.Sprintf(
		"%s|%s|%s|%s|%s|%v|%d",
		event.Source,
		event.Host,
		event.Level,
		event.Message,
		event.Timestamp.Format(time.RFC3339Nano),
		event.Labels,
		now.UnixNano(),
	)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])[:24]
}

// cleanupCache 每隔1小时清理一次过期的缓存
func (a *AlertAnalyzer) cleanupCache() {
	ticker := time.NewTicker(1 * time.Hour)
	for range ticker.C {
		a.mu.Lock()
		for fp, cachedEvent := range a.recentCache {
			if time.Since(cachedEvent.LastSeenAt) > 1*time.Hour {
				delete(a.recentCache, fp)
			}
		}
		a.mu.Unlock()
	}
}

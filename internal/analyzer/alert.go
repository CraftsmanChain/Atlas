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

// AlertAnalyzer 负责处理告警降噪、去重和分析
type AlertAnalyzer struct {
	db          *storage.DB
	notifier    *notifier.FeishuNotifier
	recentCache map[string]*api.AlertEvent // 记录指纹到最新事件的映射
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
func (a *AlertAnalyzer) Process(event *api.AlertEvent) {
	// 1. 生成告警指纹 (用于去重)
	fingerprint := a.generateFingerprint(event)

	now := time.Now()
	if event.Timestamp.IsZero() {
		event.Timestamp = now
	}
	event.LastSeenAt = now
	event.IsProcessed = true

	a.mu.Lock()
	cachedEvent, exists := a.recentCache[fingerprint]

	// 尝试从数据库中查找，防止重启后缓存丢失导致的 UNIQUE constraint 错误
	if !exists {
		var dbEvent api.AlertEvent
		if err := a.db.Where("id = ?", fingerprint).First(&dbEvent).Error; err == nil {
			cachedEvent = &dbEvent
			exists = true
			a.recentCache[fingerprint] = cachedEvent
		}
	}
	
	if exists {
		// 发现重复告警，不丢弃而是累加次数更新
		cachedEvent.RepeatCount++
		cachedEvent.LastSeenAt = now
		// 注意：实际更新数据库时我们要基于原有的ID进行更新
		a.mu.Unlock()

		if err := a.db.Save(cachedEvent).Error; err != nil {
			log.Printf("[Analyzer] Failed to update repeated alert in DB: %v", err)
		} else {
			log.Printf("[Analyzer] Updated repeated alert: %s (Count: %d)", event.Message, cachedEvent.RepeatCount)
		}

		// 发送重复告警通知到飞书
		if a.notifier != nil {
			a.notifier.SendAlert(cachedEvent, true)
		}
		return
	}

	// 首次出现的告警
	event.ID = fingerprint
	event.RepeatCount = 1
	a.recentCache[fingerprint] = event
	a.mu.Unlock()

	// 存入数据库
	if err := a.db.Create(event).Error; err != nil {
		log.Printf("[Analyzer] Failed to save new alert to DB: %v", err)
	} else {
		log.Printf("[Analyzer] Processed and saved new alert: [%s] %s", event.Level, event.Message)
	}

	// 发送首次告警通知到飞书
	if a.notifier != nil {
		a.notifier.SendAlert(event, false)
	}
}

// generateFingerprint 简单生成告警的唯一哈希指纹
func (a *AlertAnalyzer) generateFingerprint(event *api.AlertEvent) string {
	data := fmt.Sprintf("%s|%s|%s|%s", event.Source, event.Host, event.Level, event.Message)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])[:16]
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

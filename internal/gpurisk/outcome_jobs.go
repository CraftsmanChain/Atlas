package gpurisk

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OutcomeDaily processes at most 200 queries in a rolling 31-day reconciliation
// window. Older label corrections remain available via explicit CLI replay.
// Each query/day freezes as_of; retries are bounded and publish under fencing.
func OutcomeDaily(ctx context.Context, db *gorm.DB, now time.Time) error {
	now = now.UTC()
	local := now.In(time.FixedZone("Asia/Shanghai", 8*3600))
	if local.Hour() < 2 || (local.Hour() == 2 && local.Minute() < 10) {
		return nil
	}
	db = db.WithContext(ctx)
	queries := []api.GPURiskQuery{}
	if e := db.Where("status = ? AND cutoff >= ? AND cutoff <= ?", "completed", now.Add(-31*24*time.Hour), now).Order("id DESC").Limit(201).Find(&queries).Error; e != nil {
		return e
	}
	if len(queries) > 200 {
		return ErrInput
	}
	for _, q := range queries {
		key, _ := digest([]any{"outcome-daily", OutcomeVersion, q.Key, local.Format("2006-01-02")})
		input, _ := json.Marshal(now.Truncate(time.Microsecond))
		job := api.GPURiskJob{Key: key, Status: "pending", Input: string(input)}
		if e := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&job).Error; e != nil {
			return e
		}
		if e := db.Where("key = ?", key).First(&job).Error; e != nil {
			return e
		}
		if job.Status == "completed" || job.Attempt >= 2 {
			continue
		}
		owner, _ := digest([]any{key, time.Now().UnixNano()})
		lease, e := Claim(db, key, owner, time.Now().UTC())
		if errors.Is(e, ErrBusy) {
			continue
		}
		if e != nil {
			return e
		}
		var asOf time.Time
		if json.Unmarshal([]byte(job.Input), &asOf) != nil {
			e = ErrInput
		} else {
			_, e = recordOutcomes(ctx, db, q.ID, asOf, &lease)
		}
		if e != nil {
			release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			db.WithContext(release).Model(&api.GPURiskJob{}).Where("key = ? AND owner = ? AND token = ? AND status = ?", key, lease.Owner, lease.Token, "running").Updates(map[string]any{"status": "failed", "error_code": "outcome_record_failed"})
			cancel()
			return e
		}
	}
	return nil
}
func RunOutcomes(ctx context.Context, db *gorm.DB) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		run, cancel := context.WithTimeout(ctx, 10*time.Minute)
		e := OutcomeDaily(run, db, time.Now())
		cancel()
		if e != nil {
			log.Print("GPU outcome job failed; inspect ranking job ledger")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

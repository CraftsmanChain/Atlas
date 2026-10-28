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

// Daily freezes a single input per Shanghai calendar day across all workers.
// Retries consume that same stored input, even when live inventory changes.
func Daily(ctx context.Context, db *gorm.DB, now time.Time) error {
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	local := now.In(zone)
	if local.Hour() < 2 {
		return nil
	}
	key, _ := digest([]any{"daily", local.Format("2006-01-02"), Contract, Target})
	db = db.WithContext(ctx)
	var job api.GPURiskJob
	err := db.Where("key = ?", key).First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		job = api.GPURiskJob{Key: key, Status: "pending"}
		if e := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&job).Error; e != nil {
			return e
		}
		if e := db.Where("key = ?", key).First(&job).Error; e != nil {
			return e
		}
	} else if err != nil {
		return err
	}
	if job.Status == "completed" || job.Attempt >= 2 {
		return nil
	}
	owner, _ := digest([]any{key, time.Now().UnixNano()})
	lease, e := Claim(db, key, owner, time.Now().UTC())
	if errors.Is(e, ErrBusy) {
		return nil
	}
	if e != nil {
		return e
	}
	var p Snapshot
	if job.Input == "" {
		p, e = Capture(ctx, db)
		if e == nil {
			var input []byte
			input, e = json.Marshal(p)
			if e == nil {
				result := db.Model(&api.GPURiskJob{}).Where("key = ? AND owner = ? AND token = ? AND status = ? AND lease_until > ?", key, lease.Owner, lease.Token, "running", time.Now().UTC()).Update("input", string(input))
				e = result.Error
				if e == nil && result.RowsAffected != 1 {
					e = ErrFence
				}
			}
		}
	} else if json.Unmarshal([]byte(job.Input), &p) != nil {
		e = ErrInput
	}
	if e == nil {
		_, e = Record(ctx, db, p)
	}

	status, code := "completed", ""
	if e != nil {
		status, code = "failed", "daily_record_failed"
	}
	// Cancellation must still release this worker's lease; never affect a newer owner.
	releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelRelease()
	result := db.WithContext(releaseCtx).Model(&api.GPURiskJob{}).Where("key = ? AND owner = ? AND token = ? AND status = ? AND lease_until > ?", key, lease.Owner, lease.Token, "running", time.Now().UTC()).Updates(map[string]any{"status": status, "error_code": code})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrFence
	}
	return e
}

// RunDaily is only called after the operator explicitly enables the separate
// daily ranking switch. It does not attach to the legacy outcome loop.
func RunDaily(ctx context.Context, db *gorm.DB) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		err := Daily(runCtx, db, time.Now())
		cancel()
		if err != nil {
			log.Print("GPU ranking daily job failed; inspect ranking job ledger")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

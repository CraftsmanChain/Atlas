package gpurisk

import (
	"atlas/pkg/api"
	"context"
	"database/sql"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"time"
)

// OperationsConfig selects the operations stages. Each stage keeps its own
// explicit permission; coverage is off unless separately enabled.
type OperationsConfig struct {
	Ranking  bool
	Coverage bool
	Outcomes bool
	// IncidentOutcomes enables the additive T-A node incident outcome stage
	// (gpu-node-incident-outcome-v1), after the v1 outcomes stage.
	IncidentOutcomes bool
	// Shadow freezes the shadow-strategy ranking (ShadowStrategy) of the newest
	// query right after ranking; it never changes the frozen query or scores.
	Shadow bool
	CoverageOptions
}

// OperationsOnce preserves the two existing permissions. It never signs sources.
func OperationsOnce(ctx context.Context, db *gorm.DB, now time.Time, ranking, outcomes bool) error {
	return OperationsOnceWith(ctx, db, now, OperationsConfig{Ranking: ranking, Outcomes: outcomes})
}

// OperationsOnceWith runs ranking -> coverage -> outcomes. The coverage stage
// writes only system-owned (atlas-system) assertions derived from source checks
// and the operator ledger declaration; it never synthesizes Complete=true.
func OperationsOnceWith(ctx context.Context, db *gorm.DB, now time.Time, cfg OperationsConfig) error {
	var first error
	coverage := func(ctx context.Context, db *gorm.DB, now time.Time) error {
		return CoverageDaily(ctx, db, now, cfg.CoverageOptions)
	}
	for _, stage := range []struct {
		name    string
		enabled bool
		run     func(context.Context, *gorm.DB, time.Time) error
	}{{"ranking", cfg.Ranking, Daily}, {"shadow", cfg.Shadow, ShadowDaily}, {"coverage", cfg.Coverage, coverage}, {"outcomes", cfg.Outcomes, OutcomeDaily}, {"incident_outcomes", cfg.IncidentOutcomes, IncidentOutcomeDaily}} {
		if !stage.enabled {
			continue
		}
		stageCtx, cancelStage := context.WithTimeout(ctx, 10*time.Minute)
		err := stage.run(stageCtx, db, now)
		cancelStage()
		var sourceIncomplete *CoverageStageError
		if err != nil && first == nil && !errors.As(err, &sourceIncomplete) {
			// A per-source fail-closed result is recorded in the operations
			// ledger below; it must not mask the other stages as a loop failure.
			first = err
		}
		// Persist failure observation even when the invocation context was cancelled.
		save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		e := observeOperation(save, db, now, stage.name, err)
		cancel()
		if e != nil && first == nil {
			first = e
		}
	}
	return first
}
func observeOperation(ctx context.Context, db *gorm.DB, now time.Time, stage string, runErr error) error {
	day := now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seed := api.GPURiskOperation{Day: day, Stage: stage}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "day"}, {Name: "stage"}}, DoNothing: true}).Create(&seed).Error; err != nil {
			return err
		}
		// Reserve the row before taking the consistent source observation. SQLite's
		// insert obtains its writer reservation; PostgreSQL locks the same row.
		var lock api.GPURiskOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("day = ? AND stage = ?", day, stage).First(&lock).Error; err != nil {
			return err
		}
		return observeOperationLocked(ctx, tx, now, stage, runErr)
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
}
func observeOperationLocked(ctx context.Context, db *gorm.DB, now time.Time, stage string, runErr error) error {
	local := now.In(time.FixedZone("Asia/Shanghai", 8*3600))
	day := local.Format("2006-01-02")
	op := api.GPURiskOperation{Day: day, Stage: stage, Status: "waiting_schedule", ObservedAt: time.Now().UTC()}
	if local.Hour() >= 2 && (stage == "ranking" || local.Hour() > 2 || local.Minute() >= 10) {
		op.Status = "waiting_work"
	}
	var q api.GPURiskQuery
	if e := db.WithContext(ctx).Where("status = ?", "completed").Order("cutoff DESC,id DESC").First(&q).Error; e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return e
	}
	op.QueryID = q.ID
	op.MemberCount = q.MemberCount
	op.ResultSHA = q.ResultSHA
	if (stage == "outcomes" || stage == "incident_outcomes") && q.ID != 0 {
		// Each outcome stage observes only its own label contract's batches.
		version := OutcomeVersion
		if stage == "incident_outcomes" {
			version = IncidentOutcomeVersion
		}
		var batch api.GPURiskOutcomeBatch
		if e := db.WithContext(ctx).Where("query_id = ? AND version = ? AND as_of <= ?", q.ID, version, now.UTC()).Order("as_of DESC,id DESC").First(&batch).Error; e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		op.OutcomeBatchID = batch.ID
		op.OutcomeSHA = batch.SHA
	}

	keys := []string{}
	if stage == "ranking" {
		k, _ := digest([]any{"daily", day, Contract, Target})
		keys = append(keys, k)
	} else if stage == "coverage" {
		keys = append(keys, coverageJobKey(day))
	} else if stage == "shadow" {
		// The shadow stage has no job ledger; its frozen batch is the record.
		var n int64
		if e := db.WithContext(ctx).Model(&api.GPURiskShadowBatch{}).Where("query_id = ? AND strategy = ?", q.ID, ShadowStrategy).Count(&n).Error; e != nil {
			return e
		}
		if runErr == nil && n == 1 {
			op.Status = "completed"
		} else if runErr != nil {
			op.Status, op.ErrorCode = "failed", "operation_failed"
		}
		return saveOperation(ctx, db, op)
	} else {
		var queries []api.GPURiskQuery
		if e := db.WithContext(ctx).Where("status = ? AND cutoff >= ? AND cutoff <= ?", "completed", now.UTC().Add(-31*24*time.Hour), now.UTC()).Order("id DESC").Limit(201).Find(&queries).Error; e != nil {
			return e
		}
		if len(queries) > 200 {
			return ErrInput
		}
		for _, v := range queries {
			version := OutcomeVersion
			if stage == "incident_outcomes" {
				version = IncidentOutcomeVersion
			}
			k, _ := digest([]any{"outcome-daily", version, v.Key, day})
			keys = append(keys, k)
		}
	}
	var jobs []api.GPURiskJob
	if len(keys) > 0 {
		if e := db.WithContext(ctx).Where("key IN ?", keys).Find(&jobs).Error; e != nil {
			return e
		}
	}
	op.JobCount = len(jobs)
	complete := 0
	failed := false
	running := false
	sourceCode := ""
	for _, j := range jobs {
		if j.Status == "completed" {
			complete++
			if stage == "coverage" && j.ErrorCode != "" {
				sourceCode = j.ErrorCode
			}
		} else if j.Attempt >= 2 {
			op.Exhausted++
		} else if j.Status == "failed" {
			failed = true
		} else if j.Status == "running" {
			running = true
		}
	}
	switch {
	case op.Exhausted > 0:
		op.Status = "manual_required"
		op.ErrorCode = "attempt_budget_exhausted"
	case runErr != nil && !errors.As(runErr, new(*CoverageStageError)):
		op.Status = "failed"
		op.ErrorCode = "operation_failed"
	case failed:
		op.Status = "failed"
		op.ErrorCode = "job_failed"
	case sourceCode != "":
		// The coverage job ran; the named sources failed closed and wrote no
		// assertion. This is persisted from the job so later ticks keep it.
		op.Status = "blocked"
		op.ErrorCode = sourceCode
	case len(jobs) > 0 && complete == len(jobs) && (stage == "ranking" || len(jobs) == len(keys)):
		op.Status = "completed"
	case running:
		op.Status = "running"
	}
	return saveOperation(ctx, db, op)
}

func saveOperation(ctx context.Context, db *gorm.DB, op api.GPURiskOperation) error {
	op.SHA, _ = digest([]any{op.Day, op.Stage, op.Status, op.ErrorCode, op.JobCount, op.Exhausted, op.QueryID, op.MemberCount, op.ResultSHA, op.OutcomeBatchID, op.OutcomeSHA})
	return db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "day"}, {Name: "stage"}}, DoUpdates: clause.AssignmentColumns([]string{"status", "error_code", "job_count", "exhausted", "query_id", "member_count", "result_sha", "outcome_batch_id", "outcome_sha", "sha", "observed_at"}), Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "gpu_risk_operations.sha <> excluded.sha"}}}}).Create(&op).Error
}
func RunOperations(ctx context.Context, db *gorm.DB, ranking, outcomes bool) {
	RunOperationsWith(ctx, db, OperationsConfig{Ranking: ranking, Outcomes: outcomes})
}

// RunOperationsWith is RunOperations with the separately permitted coverage stage.
func RunOperationsWith(ctx context.Context, db *gorm.DB, cfg OperationsConfig) {
	if !cfg.Ranking && !cfg.Outcomes && !cfg.Coverage {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		err := OperationsOnceWith(ctx, db, time.Now(), cfg)
		if err != nil {
			log.Print("GPU operations failed; inspect operational ledger")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

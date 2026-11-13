package gpurisk

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CoverageStageVersion pins the daily coverage stage job contract.
const CoverageStageVersion = "gpu-coverage-daily-v1"

// CoverageOwner is the fixed owner of system-written coverage assertions.
const CoverageOwner = "atlas-system"

// IdentityBackfillMaxWindow caps one daily identity backfill window.
const IdentityBackfillMaxWindow = 48 * time.Hour

// IdentityBackfillLag keeps the daily window end one hour behind the clock.
const IdentityBackfillLag = time.Hour

// IdentityBackfillWait bounds how long the coverage stage waits for the daily
// identity backfill it started before judging identity on its old watermark.
const IdentityBackfillWait = 30 * time.Minute

// IdentityBackfillStarter starts an asynchronous identity backfill of
// [start, end) for the history source and returns its run ID. It is injected
// by the server so gpurisk does not depend on internal/history.
type IdentityBackfillStarter func(ctx context.Context, sourceKey string, start, end time.Time) (uint, error)

// CoverageOptions configures the daily coverage stage.
type CoverageOptions struct {
	// IdentityBackfill enables the daily identity backfill sub-step.
	IdentityBackfill bool
	// IdentitySource is the history source key; DefaultIdentitySource if empty.
	IdentitySource string
	// StartIdentityBackfill is required when IdentityBackfill is set.
	StartIdentityBackfill IdentityBackfillStarter
}

func (o CoverageOptions) identitySource() string {
	if s := strings.TrimSpace(o.IdentitySource); s != "" {
		return s
	}
	return DefaultIdentitySource
}

// CoverageStageError lists the per-source fail-closed codes of one stage run.
// Every other source was still processed.
type CoverageStageError struct{ Codes []string }

func (e *CoverageStageError) Error() string {
	return "coverage stage incomplete: " + strings.Join(e.Codes, ",")
}

var coverageSources = []string{"hardware_fault_ledger", "monitoring", "identity"}

func coverageJobKey(day string) string {
	k, _ := digest([]any{"coverage-daily", CoverageStageVersion, day})
	return k
}
func identityBackfillJobKey(day, source string) string {
	k, _ := digest([]any{"identity-backfill-daily", CoverageStageVersion, source, day})
	return k
}

// CoverageDaily runs once per Shanghai day after the day's ranking job is
// terminal. monitoring and identity are re-checked (owner atlas-system); the
// ledger only uses the newest operator declaration. Member assertions are
// written for the newest completed query and for every matured query whose
// newest outcome batch still has pending_watermark members. Each source fails
// closed independently and Complete=true is never synthesized.
func CoverageDaily(ctx context.Context, db *gorm.DB, now time.Time, opts CoverageOptions) error {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	local := now.In(zone)
	if local.Hour() < 2 {
		return nil
	}
	day := local.Format("2006-01-02")
	db = db.WithContext(ctx)
	// Wait for today's ranking so the newest query is today's query.
	rankKey, _ := digest([]any{"daily", day, Contract, Target})
	var ranking []api.GPURiskJob
	if e := db.Where("key = ?", rankKey).Limit(1).Find(&ranking).Error; e != nil {
		return e
	}
	if len(ranking) == 1 && ranking[0].Status != "completed" && ranking[0].Attempt < 2 {
		return nil
	}
	codes := []string{}
	if opts.IdentityBackfill {
		waiting, code, e := ensureIdentityBackfill(ctx, db, now, day, opts)
		if e != nil {
			return e
		}
		if waiting {
			return nil
		}
		if code != "" {
			codes = append(codes, code)
		}
	}
	key := coverageJobKey(day)
	job := api.GPURiskJob{Key: key, Status: "pending"}
	if e := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&job).Error; e != nil {
		return e
	}
	if e := db.Where("key = ?", key).First(&job).Error; e != nil {
		return e
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
	sourceCodes, runErr := runCoverageSources(ctx, db, now, opts)
	codes = append(codes, sourceCodes...)
	status, code := "completed", strings.Join(codes, ",")
	if runErr != nil {
		status, code = "failed", "coverage_stage_failed"
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	result := db.WithContext(releaseCtx).Model(&api.GPURiskJob{}).Where("key = ? AND owner = ? AND token = ? AND status = ? AND lease_until > ?", key, lease.Owner, lease.Token, "running", time.Now().UTC()).Updates(map[string]any{"status": status, "error_code": code})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrFence
	}
	if runErr != nil {
		return runErr
	}
	if len(codes) > 0 {
		return &CoverageStageError{Codes: codes}
	}
	return nil
}

// coverageTargetQueries returns (a) the newest completed query and (b) every
// completed query in the 31-day window with cutoff+horizon+24h <= now whose
// newest outcome batch still has a pending_watermark member.
func coverageTargetQueries(ctx context.Context, db *gorm.DB, now time.Time) ([]api.GPURiskQuery, error) {
	now = now.UTC()
	out := []api.GPURiskQuery{}
	seen := map[uint]bool{}
	var latest []api.GPURiskQuery
	if e := db.WithContext(ctx).Where("status = ? AND cutoff <= ?", "completed", now).Order("cutoff DESC,id DESC").Limit(1).Find(&latest).Error; e != nil {
		return nil, e
	}
	for _, q := range latest {
		out = append(out, q)
		seen[q.ID] = true
	}
	var matured []api.GPURiskQuery
	horizon := 7*24*time.Hour + 24*time.Hour
	if e := db.WithContext(ctx).Where("status = ? AND cutoff >= ? AND cutoff <= ?", "completed", now.Add(-31*24*time.Hour), now.Add(-horizon)).Order("id DESC").Limit(201).Find(&matured).Error; e != nil {
		return nil, e
	}
	if len(matured) > 200 {
		return nil, ErrInput
	}
	for _, q := range matured {
		if seen[q.ID] || q.Cutoff.Add(horizon).After(now) {
			continue
		}
		var batch []api.GPURiskOutcomeBatch
		if e := db.WithContext(ctx).Where("query_id = ? AND as_of <= ?", q.ID, now).Order("as_of DESC,id DESC").Limit(1).Find(&batch).Error; e != nil {
			return nil, e
		}
		if len(batch) != 1 {
			continue
		}
		var pending int64
		if e := db.WithContext(ctx).Model(&api.GPURiskNodeOutcome{}).Where("batch_id = ? AND status = ?", batch[0].ID, "pending_watermark").Count(&pending).Error; e != nil {
			return nil, e
		}
		if pending > 0 {
			out = append(out, q)
			seen[q.ID] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// runCoverageSources processes each source independently. It returns the
// fail-closed codes of the sources that could not be fully asserted; the error
// is reserved for failures that are not a property of one source.
func runCoverageSources(ctx context.Context, db *gorm.DB, now time.Time, opts CoverageOptions) ([]string, error) {
	queries, err := coverageTargetQueries(ctx, db, now)
	if err != nil {
		return nil, err
	}
	codes := []string{}
	for _, source := range coverageSources {
		ok := true
		if source != "hardware_fault_ledger" {
			if _, e := ensureSourceCheckV3(ctx, db, source, CoverageOwner, opts.identitySource()); e != nil {
				if ctx.Err() != nil {
					return codes, ctx.Err()
				}
				codes = append(codes, "coverage_"+source+"_incomplete")
				continue
			}
		}
		for _, q := range queries {
			if _, e := RecordMemberCoverage(ctx, db, q.ID, source, CoverageOwner); e != nil {
				if ctx.Err() != nil {
					return codes, ctx.Err()
				}
				ok = false
			}
		}
		if !ok || len(queries) == 0 {
			codes = append(codes, "coverage_"+source+"_incomplete")
		}
	}
	return codes, nil
}

type identityBackfillInput struct {
	RunID uint      `json:"run_id"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// identityBackfillWindow is [RangeEnd of newest completed run, now-1h), capped
// at IdentityBackfillMaxWindow from its start so coverage stays contiguous.
func identityBackfillWindow(ctx context.Context, db *gorm.DB, source string, now time.Time) (time.Time, time.Time, error) {
	var runs []api.HistoryBackfillRun
	if e := db.WithContext(ctx).Where("source_key = ? AND job_type = ? AND status = ? AND finished_at IS NOT NULL", source, identityBackfillJobType, "completed").Order("range_end DESC, id DESC").Limit(1).Find(&runs).Error; e != nil {
		return time.Time{}, time.Time{}, e
	}
	if len(runs) != 1 {
		return time.Time{}, time.Time{}, ErrCoverageSourceIncomplete
	}
	start := runs[0].RangeEnd.UTC().Truncate(time.Minute)
	end := now.UTC().Add(-IdentityBackfillLag).Truncate(time.Minute)
	if end.Sub(start) > IdentityBackfillMaxWindow {
		end = start.Add(IdentityBackfillMaxWindow)
	}
	return start, end, nil
}

// ensureIdentityBackfill starts at most one identity backfill per day (job
// key + Claim/lease) and reports whether the stage should wait for it. code
// is a non-fatal identity code; identity is then judged on its old watermark.
func ensureIdentityBackfill(ctx context.Context, db *gorm.DB, now time.Time, day string, opts CoverageOptions) (bool, string, error) {
	if opts.StartIdentityBackfill == nil {
		return false, "identity_backfill_unavailable", nil
	}
	source := opts.identitySource()
	key := identityBackfillJobKey(day, source)
	job := api.GPURiskJob{Key: key, Status: "pending"}
	if e := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&job).Error; e != nil {
		return false, "", e
	}
	if e := db.Where("key = ?", key).First(&job).Error; e != nil {
		return false, "", e
	}
	if job.Status == "completed" {
		var in identityBackfillInput
		if job.Input == "" || json.Unmarshal([]byte(job.Input), &in) != nil || in.RunID == 0 {
			return false, job.ErrorCode, nil
		}
		var run api.HistoryBackfillRun
		if e := db.Where("id = ?", in.RunID).Limit(1).Find(&run).Error; e != nil {
			return false, "", e
		}
		if (run.Status == "queued" || run.Status == "running") && now.UTC().Sub(run.StartedAt.UTC()) < IdentityBackfillWait {
			return true, "", nil
		}
		if run.Status != "completed" {
			return false, "identity_backfill_" + sanitizeCode(run.Status), nil
		}
		return false, "", nil
	}
	if job.Attempt >= 2 {
		return false, "identity_backfill_start_failed", nil
	}
	owner, _ := digest([]any{key, time.Now().UnixNano()})
	lease, e := Claim(db, key, owner, time.Now().UTC())
	if errors.Is(e, ErrBusy) {
		return true, "", nil
	}
	if e != nil {
		return false, "", e
	}
	status, code, input := "completed", "", ""
	start, end, werr := identityBackfillWindow(ctx, db, source, now)
	switch {
	case errors.Is(werr, ErrCoverageSourceIncomplete):
		code = "identity_backfill_no_completed_baseline"
	case werr != nil:
		status, code = "failed", "identity_backfill_start_failed"
	case !start.Before(end):
		code = ""
	default:
		runID, se := opts.StartIdentityBackfill(ctx, source, start, end)
		if se != nil {
			status, code = "failed", "identity_backfill_start_failed"
			log.Print("GPU coverage identity backfill could not start; inspect history backfill runs")
		} else {
			raw, _ := json.Marshal(identityBackfillInput{RunID: runID, Start: start, End: end})
			input = string(raw)
		}
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	result := db.WithContext(releaseCtx).Model(&api.GPURiskJob{}).Where("key = ? AND owner = ? AND token = ? AND status = ? AND lease_until > ?", key, lease.Owner, lease.Token, "running", time.Now().UTC()).Updates(map[string]any{"status": status, "error_code": code, "input": input})
	if result.Error != nil {
		return false, "", result.Error
	}
	if result.RowsAffected != 1 {
		return false, "", ErrFence
	}
	if status == "failed" {
		// A retry is allowed on the next tick within the attempt budget.
		return job.Attempt+1 < 2, code, nil
	}
	return input != "", code, nil
}

func sanitizeCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "unknown"
	}
	b := strings.Builder{}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RunCoverage is the standalone loop used only when the operations loop is
// disabled but the coverage switch is on.
func RunCoverage(ctx context.Context, db *gorm.DB, opts CoverageOptions) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		err := CoverageDaily(runCtx, db, time.Now(), opts)
		cancel()
		var stage *CoverageStageError
		if err != nil && !errors.As(err, &stage) {
			log.Print("GPU coverage job failed; inspect ranking job ledger")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

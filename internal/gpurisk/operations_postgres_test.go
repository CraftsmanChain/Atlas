package gpurisk

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"atlas/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const operationsPGEnv = "ATLAS_GPU_RISK_OPERATIONS_TEST_PG"

func operationsPGConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	value := os.Getenv(operationsPGEnv)
	if value == "" {
		t.Skip("isolated operations PostgreSQL not supplied")
	}
	cfg, err := pgx.ParseConfig(value)
	if err != nil {
		t.Fatal("invalid isolated PG configuration")
	}
	if (cfg.Host != "127.0.0.1" && cfg.Host != "localhost") || !regexp.MustCompile(`^atlas_p3_[a-zA-Z0-9_]+$`).MatchString(cfg.Database) {
		t.Fatal("operations PG requires loopback and dedicated atlas_p3_* database")
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.Host != "127.0.0.1" && fallback.Host != "localhost" {
			t.Fatal("non-loopback fallback rejected")
		}
	}
	cfg.ConnectTimeout = 3 * time.Second
	return cfg
}
func operationsPGOpen(t *testing.T, cfg *pgx.ConnConfig, schema string) *gorm.DB {
	t.Helper()
	c := cfg.Copy()
	c.RuntimeParams["search_path"] = schema
	raw := stdlib.OpenDB(*c)
	raw.SetMaxOpenConns(1)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: raw}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		raw.Close()
		t.Fatal("isolated PG open failed")
	}
	t.Cleanup(func() { raw.Close() })
	return db
}
func operationsPGFixture(t *testing.T) (*gorm.DB, func() *gorm.DB, string) {
	t.Helper()
	cfg := operationsPGConfig(t)
	admin := operationsPGOpen(t, cfg, "public")
	schema := fmt.Sprintf("operations_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal("create isolated schema failed")
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error("cleanup schema failed")
		}
	})
	open := func() *gorm.DB { return operationsPGOpen(t, cfg, schema) }
	db := open()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&api.GPURiskOperation{}); err != nil {
		t.Fatal(err)
	}
	return db, open, schema
}
func operationsPGNow() time.Time {
	return time.Date(2026, 10, 28, 3, 0, 0, 0, time.FixedZone("Shanghai", 8*3600))
}
func operationsPGJob(t *testing.T, db *gorm.DB, status string, attempt int) api.GPURiskJob {
	t.Helper()
	key, _ := digest([]any{"daily", "2026-10-28", Contract, Target})
	j := api.GPURiskJob{Key: key, Status: status, Attempt: attempt, Input: `"2026-10-28T02:10:00Z"`}
	if err := db.Create(&j).Error; err != nil {
		t.Fatal(err)
	}
	return j
}
func operationsPGRow(t *testing.T, db *gorm.DB) api.GPURiskOperation {
	t.Helper()
	var rows []api.GPURiskOperation
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("operation rows=%d", len(rows))
	}
	return rows[0]
}
func operationsPGCode(err error) string {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return state.SQLState()
	}
	return ""
}

func TestOperationsPostgresConcurrentObservers(t *testing.T) {
	db, open, _ := operationsPGFixture(t)
	operationsPGJob(t, db, "completed", 1)
	const workers = 6
	conns := make([]*gorm.DB, workers)
	pids := map[int]bool{}
	for i := range conns {
		conns[i] = open()
		var pid int
		if err := conns[i].Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
			t.Fatal(err)
		}
		pids[pid] = true
	}
	if len(pids) != workers {
		t.Fatal("workers share physical connections")
	}
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for _, conn := range conns {
		wg.Add(1)
		go func(c *gorm.DB) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			results <- observeOperation(ctx, c, operationsPGNow(), "ranking", nil)
		}(conn)
	}
	close(start)
	wg.Wait()
	close(results)
	rejected := map[string]int{}
	for err := range results {
		if err == nil {
			continue
		}
		code := operationsPGCode(err)
		if code != "40001" && code != "40P01" {
			t.Fatalf("unexpected concurrent SQLSTATE=%q", code)
		}
		rejected[code]++
	}
	// A distinct later invocation models the existing next-loop recovery, not a
	// hidden production retry or acceptance of arbitrary transaction errors.
	if err := observeOperation(context.Background(), db, operationsPGNow(), "ranking", nil); err != nil {
		t.Fatal(err)
	}
	row := operationsPGRow(t, db)
	if row.Status != "completed" {
		t.Fatal(row.Status)
	}
	for i := 0; i < 5; i++ {
		if err := observeOperation(context.Background(), open(), operationsPGNow(), "ranking", nil); err != nil {
			t.Fatal(err)
		}
	}
	after := operationsPGRow(t, db)
	if row.SHA != after.SHA || !row.ObservedAt.Equal(after.ObservedAt) {
		t.Fatal("unchanged observation mutated")
	}
	t.Logf("OBSERVED physical_connections=%d rows=1 stable_sha=%s safe_rejections=%v", len(pids), after.SHA, rejected)
}

func TestOperationsPostgresLockCancellation(t *testing.T) {
	db, open, _ := operationsPGFixture(t)
	operationsPGJob(t, db, "completed", 1)
	if err := observeOperation(context.Background(), db, operationsPGNow(), "ranking", nil); err != nil {
		t.Fatal(err)
	}
	before := operationsPGRow(t, db)
	holder := open().Begin()
	if holder.Error != nil {
		t.Fatal(holder.Error)
	}
	defer holder.Rollback()
	if err := holder.Exec("SELECT id FROM gpu_risk_operations FOR UPDATE").Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := observeOperation(ctx, open(), operationsPGNow(), "ranking", nil)
	if err == nil || ctx.Err() == nil {
		t.Fatal("lock wait did not cancel")
	}
	if err := holder.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	after := operationsPGRow(t, db)
	if before.SHA != after.SHA || !before.ObservedAt.Equal(after.ObservedAt) {
		t.Fatal("cancel published change")
	}
	if err := observeOperation(context.Background(), open(), operationsPGNow(), "ranking", nil); err != nil {
		t.Fatal(err)
	}
	t.Log("OBSERVED lock-wait deadline rejected, previous row intact, new connection resumed")
}

func TestOperationsPostgresCrashRollback(t *testing.T) {
	db, open, schema := operationsPGFixture(t)
	operationsPGJob(t, db, "completed", 1)
	if err := observeOperation(context.Background(), db, operationsPGNow(), "ranking", nil); err != nil {
		t.Fatal(err)
	}
	before := operationsPGRow(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOperationsPostgresCrashChild$")
	cmd.Env = append(os.Environ(), "ATLAS_P3_CHILD_SCHEMA="+schema)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	})
	ready := make(chan bool, 1)
	go func() { s := bufio.NewScanner(stdout); ready <- s.Scan() && s.Text() == "TRANSACTION_LOCKED" }()
	select {
	case ok := <-ready:
		if !ok {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatal("child did not establish transaction")
		}
	case <-ctx.Done():
		cmd.Wait()
		t.Fatal("child readiness timeout")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("child unexpectedly exited normally")
	}
	fresh := open()
	after := operationsPGRow(t, fresh)
	if before.SHA != after.SHA || before.Status != after.Status {
		t.Fatal("uncommitted child write survived termination")
	}
	if err = observeOperation(context.Background(), fresh, operationsPGNow(), "ranking", nil); err != nil {
		t.Fatal(err)
	}
	t.Log("OBSERVED own child process killed after locked UPDATE; rollback and new-connection observe verified (not whole worker lease-crash certification)")
}
func TestOperationsPostgresCrashChild(t *testing.T) {
	schema := os.Getenv("ATLAS_P3_CHILD_SCHEMA")
	if schema == "" {
		t.Skip("child-only helper")
	}
	if !regexp.MustCompile(`^operations_[0-9]+$`).MatchString(schema) {
		t.Fatal("invalid child schema")
	}
	db := operationsPGOpen(t, operationsPGConfig(t), schema)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal("child begin failed")
	}
	defer tx.Rollback()
	if err := tx.Exec("UPDATE gpu_risk_operations SET status='uncommitted_child' WHERE stage='ranking'").Error; err != nil {
		t.Fatal("child update failed")
	}
	fmt.Println("TRANSACTION_LOCKED")
	time.Sleep(20 * time.Second)
	t.Fatal("child should have been terminated")
}

func TestOperationsPostgresBudgetAndReopen(t *testing.T) {
	db, open, _ := operationsPGFixture(t)
	job := operationsPGJob(t, db, "failed", 2)
	for i := 0; i < 3; i++ {
		if err := OperationsOnce(context.Background(), open(), operationsPGNow(), true, false); err != nil {
			t.Fatal(err)
		}
	}
	row := operationsPGRow(t, db)
	if row.Status != "manual_required" || row.Exhausted != 1 {
		t.Fatal("exhaustion not surfaced")
	}
	var j api.GPURiskJob
	if err := db.First(&j, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if j.Input != job.Input || j.Attempt != 2 || j.Status != "failed" {
		t.Fatal("budget/frozen input changed")
	}
	for _, model := range []any{&api.GPURiskEvidenceRevision{}, &api.GPURiskOutcomeBatch{}, &api.GPURiskQuery{}} {
		var n int64
		if err := db.Model(model).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("exhausted ranking created domain evidence")
		}
	}
	t.Logf("OBSERVED reopened_connections=3 attempt=2 frozen_input_equal=true rows=1 sha=%s", row.SHA)
	if strings.TrimSpace(job.Input) == "" {
		t.Fatal("empty fixture input")
	}
}

// A blocked observer must not publish the pre-commit source snapshot after a
// writer has advanced both the job and its operational observation.
func TestOperationsPostgresConcurrentSourceAdvanceDoesNotRegress(t *testing.T) {
	db, open, _ := operationsPGFixture(t)
	job := operationsPGJob(t, db, "running", 1)
	if err := observeOperation(context.Background(), db, operationsPGNow(), "ranking", nil); err != nil {
		t.Fatal(err)
	}
	before := operationsPGRow(t, db)
	if before.Status != "running" {
		t.Fatal("fixture not running")
	}
	writer := open().Begin()
	if writer.Error != nil {
		t.Fatal(writer.Error)
	}
	defer writer.Rollback()
	var writerPID int
	if err := writer.Raw("SELECT pg_backend_pid()").Scan(&writerPID).Error; err != nil {
		t.Fatal(err)
	}
	if err := writer.Exec("SELECT id FROM gpu_risk_operations FOR UPDATE").Error; err != nil {
		t.Fatal(err)
	}
	if err := writer.Model(&api.GPURiskJob{}).Where("id = ?", job.ID).Update("status", "completed").Error; err != nil {
		t.Fatal(err)
	}
	if err := observeOperationLocked(context.Background(), writer, operationsPGNow(), "ranking", nil); err != nil {
		t.Fatal(err)
	}
	observer := open()
	var pid int
	if err := observer.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- observeOperation(ctx, observer, operationsPGNow(), "ranking", nil) }()
	if pid == writerPID {
		t.Fatal("writer and observer share backend")
	}
	inspector := open()
	blocked := false
	blockerPID := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("observer finished before blocking barrier SQLSTATE=%q", operationsPGCode(err))
		default:
		}
		var n int
		if err := inspector.Raw("SELECT COALESCE((pg_blocking_pids(?))[1],0)", pid).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			blocked = true
			blockerPID = n
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !blocked || blockerPID != writerPID {
		t.Fatal("observer never demonstrably blocked on writer")
	}
	if err := writer.Commit().Error; err != nil {
		t.Fatal(err)
	}
	err := <-done
	code := operationsPGCode(err)
	if err != nil && code != "40001" {
		t.Fatalf("unexpected observer SQLSTATE=%q", code)
	}
	committed := operationsPGRow(t, db)
	if committed.Status != "completed" {
		t.Fatal("observer regressed committed completion")
	}
	if err := observeOperation(context.Background(), open(), operationsPGNow(), "ranking", nil); err != nil {
		t.Fatal(err)
	}
	after := operationsPGRow(t, db)
	if after.Status != "completed" || after.SHA != committed.SHA || !after.ObservedAt.Equal(committed.ObservedAt) {
		t.Fatal("new observation regressed or mutated completed state")
	}
	t.Logf("OBSERVED writer_pid=%d observer_pid=%d blocker_pid=%d blocking_barrier=true transition=running_to_completed observer_sqlstate=%q stable_sha=%s", writerPID, pid, blockerPID, code, after.SHA)
}

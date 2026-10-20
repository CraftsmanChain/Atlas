package diskhealth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This test must use a dedicated throwaway database, never Atlas's live DB.
// Its random schema and two independent physical connections are isolated from
// public tables; cleanup removes only that schema. No DSN is printed.
func TestInstallationPostgresTwoConnectionTransactions(t *testing.T) {
	dsn := os.Getenv("ATLAS_DISK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PostgreSQL acceptance not run: ATLAS_DISK_TEST_POSTGRES_DSN must reference a dedicated throwaway database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid dedicated PostgreSQL test configuration")
	}
	adminSQL := stdlib.OpenDB(*config)
	defer adminSQL.Close()
	admin, err := gorm.Open(postgres.New(postgres.Config{Conn: adminSQL}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("cannot open dedicated PostgreSQL test database")
	}
	schema := "disk_installation_test_" + Digest([]byte(t.Name() + time.Now().UTC().Format(time.RFC3339Nano)))[:16]
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal("cannot create isolated PostgreSQL test schema")
	}
	defer func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error("cannot remove isolated PostgreSQL test schema")
		}
	}()
	newConnection := func() (*storage.DB, *Service) {
		cfg := config.Copy()
		if cfg.RuntimeParams == nil {
			cfg.RuntimeParams = map[string]string{}
		}
		cfg.RuntimeParams["search_path"] = schema
		sqlDB := stdlib.OpenDB(*cfg)
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal("cannot open isolated PostgreSQL test connection")
		}
		wrapped := &storage.DB{DB: db}
		s := NewService(wrapped)
		s.now = func() time.Time { return installationTestNow }
		return wrapped, s
	}
	db1, s1 := newConnection()
	db2, s2 := newConnection()
	if err := db1.AutoMigrate(&api.InfrastructureAsset{}, &api.PhysicalDiskSnapshot{}, &api.DiskHealthEpisode{}, &api.DiskObservationScope{}, &api.DiskInstallation{}, &api.DiskInstallationReconcile{}); err != nil {
		t.Fatal("cannot migrate isolated PostgreSQL test schema")
	}
	var pid1, pid2 int
	if err := db1.Raw("SELECT pg_backend_pid()").Scan(&pid1).Error; err != nil {
		t.Fatal("cannot inspect first PostgreSQL connection")
	}
	if err := db2.Raw("SELECT pg_backend_pid()").Scan(&pid2).Error; err != nil {
		t.Fatal("cannot inspect second PostgreSQL connection")
	}
	if pid1 == pid2 {
		t.Fatal("PostgreSQL acceptance requires two distinct physical connections")
	}
	t.Log("PostgreSQL acceptance uses two distinct physical connections and an isolated schema")
	a := installationTestSnapshot(t, db1, "PG-A")
	base := installationTestManifest(a)
	type outcome struct {
		result InstallationReconcileResult
		err    error
	}
	race := func(left, right InstallationReconcileManifest) []outcome {
		start := make(chan struct{})
		out := make(chan outcome, 2)
		var wg sync.WaitGroup
		for i, candidate := range []InstallationReconcileManifest{left, right} {
			wg.Add(1)
			go func(index int, m InstallationReconcileManifest) {
				defer wg.Done()
				<-start
				raw, _ := json.Marshal(m)
				service := s1
				if index == 1 {
					service = s2
				}
				r, e := service.ReconcileInstallations(m, Digest(raw))
				out <- outcome{r, e}
			}(i, candidate)
		}
		close(start)
		wg.Wait()
		close(out)
		all := []outcome{}
		for r := range out {
			all = append(all, r)
		}
		return all
	}
	oneWinner := func(all []outcome) {
		t.Helper()
		success, conflict := 0, 0
		for _, o := range all {
			if o.err == nil {
				success++
			} else if strings.Contains(o.err.Error(), "expected roster version conflict") {
				conflict++
			} else {
				t.Fatalf("unexpected PostgreSQL reconcile failure: %v", o.err)
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatalf("expected one winner: successes=%d conflicts=%d", success, conflict)
		}
	}
	left, right := base, base
	left.Actor = "first-scope-writer-a"
	right.Actor = "first-scope-writer-b"
	oneWinner(race(left, right)) // advisory lock covers a scope row that does not exist yet
	active := installationTestActive(t, db1)
	if len(active) != 1 {
		t.Fatal("concurrent scope creation duplicated installation")
	}
	left, right = base, base
	left.ExpectedRosterVersion = 1
	right.ExpectedRosterVersion = 1
	left.KnownInstallationIDs = []string{active[0].InstallationID}
	right.KnownInstallationIDs = left.KnownInstallationIDs
	left.Actor = "existing-writer-a"
	right.Actor = "existing-writer-b"
	oneWinner(race(left, right))
	idempotent := left
	idempotent.ExpectedRosterVersion = 2
	idempotent.Actor = "same-manifest-concurrent"
	duplicates := 0
	for _, o := range race(idempotent, idempotent) {
		if o.err != nil {
			t.Fatalf("concurrent idempotent replay failed: %v", o.err)
		}
		if o.result.Idempotent {
			duplicates++
		}
		if o.result.RosterVersion != 3 {
			t.Fatal("duplicate manifest advanced roster twice")
		}
	}
	if duplicates != 1 {
		t.Fatalf("expected one idempotent result, got %d", duplicates)
	}
	var auditCount int64
	db1.Model(&api.DiskInstallationReconcile{}).Count(&auditCount)
	if auditCount != 3 {
		t.Fatalf("unexpected audit count: %d", auditCount)
	}
	// Reusing a bound SHA with changed content is rejected before any write.
	raw, _ := json.Marshal(idempotent)
	changed := idempotent
	changed.Actor = "conflicting-body"
	if _, err := s2.ReconcileInstallations(changed, Digest(raw)); err == nil {
		t.Fatal("conflicting SHA reuse accepted")
	}
	// Force a failure after closures/opens and the version update. Everything,
	// including the old active entity, must roll back on the second connection.
	b := installationTestSnapshot(t, db1, "PG-B")
	if err := db2.Callback().Create().Before("gorm:create").Register("fail-pg-installation-audit", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "disk_installation_reconciles" {
			tx.AddError(errors.New("synthetic PostgreSQL audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	replace := installationTestManifest(b)
	replace.ExpectedRosterVersion = 3
	replace.KnownInstallationIDs = []string{active[0].InstallationID}
	replace.Closures = []InstallationReconcileClosure{{InstallationID: active[0].InstallationID, Reason: "explicit_replacement", EvidenceSHA256: Digest([]byte("pg-close"))}}
	installationTestReject(t, s2, replace, "synthetic PostgreSQL audit failure")
	after := installationTestActive(t, db1)
	if len(after) != 1 || after[0].EntityID != a.EntityID {
		t.Fatal("PostgreSQL rollback changed active roster")
	}
	var scope api.DiskObservationScope
	if err := db1.First(&scope, "scope_id = ?", base.ScopeID).Error; err != nil {
		t.Fatal(err)
	}
	if scope.RosterVersion != 3 {
		t.Fatal("PostgreSQL rollback advanced version")
	}
	db1.Model(&api.DiskInstallationReconcile{}).Count(&auditCount)
	if auditCount != 3 {
		t.Fatal("PostgreSQL rollback retained audit")
	}

	// First registration and a cross-node import of the same EntityID must
	// serialize against the same identity lock. Either the unregistered import
	// wins and registration rejects mixed history, or registration wins and
	// the import rejects the new node. Both must never be accepted together.
	raceSnapshot := installationTestSnapshot(t, db1, "PG-NODE-RACE")
	raceManifest := installationTestManifest(raceSnapshot)
	raceManifest.ScopeID = "fixture-node-race-roster"
	raceManifest.AssetKey = "fixture:node-race-host"
	raceManifest.Actor = "first-registration-node-race"
	badRow := Result{Reference: "cross-node-race", EntityID: raceSnapshot.EntityID, EntityType: raceSnapshot.EntityType, IdentityStatus: "bound", Scope: raceSnapshot.Scope, Generation: raceSnapshot.Generation, Node: "different-node", Serial: raceSnapshot.Serial, SourceKind: "fixture", InputSHA256: Digest([]byte("cross-node-race-input")), ObservedAt: installationTestNow.Add(-30 * time.Minute), AvailableAt: installationTestNow.Add(-30*time.Minute + time.Second), RuleVersion: RuleVersion, AssessmentStatus: "insufficient_data", Level: "unknown"}
	start := make(chan struct{})
	registration := make(chan error, 1)
	imported := make(chan error, 1)
	go func() {
		<-start
		raw, _ := json.Marshal(raceManifest)
		_, e := s1.ReconcileInstallations(raceManifest, Digest(raw))
		registration <- e
	}()
	go func() {
		<-start
		_, e := s2.PersistReport(Report{Schema: SchemaVersion, RuleVersion: ReportRuleVersion, AsOf: installationTestNow, Rows: []Result{badRow}}, "")
		imported <- e
	}()
	close(start)
	registerErr, importErr := <-registration, <-imported
	if (registerErr == nil) == (importErr == nil) {
		t.Fatalf("first registration/cross-node import must have exactly one winner: registration=%v import=%v", registerErr, importErr)
	}
	if registerErr != nil && !strings.Contains(registerErr.Error(), "history contains identity conflicts") {
		t.Fatalf("unexpected registration race rejection: %v", registerErr)
	}
	if importErr != nil && !strings.Contains(importErr.Error(), "registered entity observation scope mismatch") {
		t.Fatalf("unexpected cross-node import rejection: %v", importErr)
	}
	var raceActive int64
	if err := db1.Model(&api.DiskInstallation{}).Where("entity_id = ? AND state = ?", raceSnapshot.EntityID, "active").Count(&raceActive).Error; err != nil {
		t.Fatal(err)
	}
	if registerErr == nil && raceActive != 1 || registerErr != nil && raceActive != 0 {
		t.Fatal("first-registration race left inconsistent installed state")
	}
	t.Run("observation-binding-rechecked-after-writer-lock", func(t *testing.T) {
		observed := installationTestSnapshot(t, db1, "PG-OBSERVATION-BINDING")
		manifest := installationTestManifest(observed)
		manifest.ScopeID = "fixture-observation-binding-roster"
		manifest.AssetKey = "fixture:observation-binding-host"
		installationTestApply(t, s1, manifest)
		binding := observationTestBinding(t, db1, manifest.ScopeID)
		if err := s2.CheckObservationBinding(context.Background(), binding); err != nil {
			t.Fatal(err)
		}
		entered := make(chan struct{})
		var once sync.Once
		callback := "test:observation-before-identity-lock"
		if err := db2.Callback().Raw().Before("gorm:raw").Register(callback, func(tx *gorm.DB) {
			if strings.Contains(tx.Statement.SQL.String(), "pg_advisory_xact_lock") {
				once.Do(func() { close(entered) })
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer db2.Callback().Raw().Remove(callback)
		finished := make(chan error, 1)
		report := observationTestReport(observed)
		// Hold the actual registry writer lock while a second physical connection
		// attempts persistence with the formerly valid, frozen roster version.
		err := db1.Transaction(func(tx *gorm.DB) error {
			if err := lockInstallationIdentity(tx); err != nil {
				return err
			}
			go func() {
				_, err := s2.PersistReportWithBinding(context.Background(), report, Digest([]byte("pg-bound-report")), binding)
				finished <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				return errors.New("bound writer did not reach identity lock")
			}
			return tx.Model(&api.DiskObservationScope{}).Where("scope_id = ?", binding.ScopeID).Update("roster_version", 2).Error
		})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-finished:
			if err == nil || !strings.Contains(err.Error(), "expected roster version changed") {
				t.Fatalf("stale bound writer was not rejected after lock: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("bound writer did not finish after registry commit")
		}
		var count int64
		if err := db1.Model(&api.PhysicalDiskSnapshot{}).Where("entity_id = ?", observed.EntityID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("stale PG bound writer persisted a snapshot: %d", count)
		}
	})
}

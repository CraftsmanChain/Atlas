// gpu-risk defaults to read-only plan. Only explicit record may migrate/write
// the database selected through ATLAS_GPU_RISK_DSN; DSNs are never printed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"atlas/internal/eventauditcli"
	"atlas/internal/gpurisk"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func run(args []string) error {
	if len(args) > 0 && args[0] == "event-audit" {
		if eventauditcli.Run(args[1:], os.Stdout, os.Stderr) != 0 {
			return fmt.Errorf("event audit failed")
		}
		return nil
	}
	command := "plan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("gpu-risk", flag.ContinueOnError)
	driver := flags.String("driver", "postgres", "postgres or sqlite")
	modelID := flags.Uint("model", 0, "explicit candidate model ID")
	mode := flags.String("mode", "full", "full or identity-only (evidence-ingest-prepare only)")
	reviewID := flags.Uint("review", 0, "review row ID; selects latest feedback revision")
	queryID := flags.Uint("query", 0, "frozen query ID for outcomes")
	target := flags.String("target", "confirmed_hardware_failure", "outcome target for outcome-plan/outcomes: confirmed_hardware_failure (v1) or node_hardware_incident (T-A)")
	source := flags.String("source", "", "history source for coverage-check/coverage-members: hardware_fault_ledger, monitoring or identity")
	owner := flags.String("owner", "", "signing owner for coverage-check/coverage-members")
	asOfText := flags.String("as-of", "", "RFC3339 outcome evaluation time; defaults to now")
	input := flags.String("input", "", "frozen plan JSON (required for record)")
	datasetFile := flags.String("dataset-file", "", "pinned local dataset JSON for verify-model")
	artifactFile := flags.String("artifact-file", "", "local model JSON for verify-model")
	scoresFile := flags.String("scores-file", "", "local scores JSON for verify-model")
	datasetSHA := flags.String("dataset-sha", "", "exact dataset file SHA256")
	artifactSHA := flags.String("artifact-sha", "", "exact model file SHA256")
	scoresSHA := flags.String("scores-sha", "", "exact score file SHA256")
	throughText := flags.String("through", "", "RFC3339 ledger complete-through for ledger-declare")
	if flags.Parse(args) != nil || flags.NArg() != 0 || (command != "evidence-ingest-prepare" && command != "evidence-ingest-apply" && command != "context-prepare" && command != "core-source-plan" && command != "coverage-plan" && command != "coverage-check" && command != "ledger-declare" && command != "coverage-members" && command != "plan" && command != "record" && command != "evidence" && command != "outcomes" && command != "outcome-plan" && command != "dataset" && command != "verify-model" && command != "register-model" && command != "model-request" && command != "model-record") {
		return fmt.Errorf("usage: gpu-risk [plan|record|context-prepare|core-source-plan|coverage-plan|evidence-ingest-prepare|evidence-ingest-apply|coverage-check|ledger-declare|coverage-members|evidence|outcome-plan|outcomes|dataset|verify-model|register-model|model-request|model-record] [-driver postgres|sqlite] [-input plan.json]")
	}
	modeSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "mode" {
			modeSet = true
		}
	})
	if (modeSet && command != "evidence-ingest-prepare") || (*mode != "full" && *mode != "identity-only") {
		return fmt.Errorf("-mode full|identity-only is only valid for evidence-ingest-prepare")
	}
	if command == "verify-model" {
		report, err := gpurisk.VerifyRankerFiles(*datasetFile, *datasetSHA, *artifactFile, *artifactSHA, *scoresFile, *scoresSHA)
		if err != nil {
			return fmt.Errorf("model parity rejected: file fingerprint, contract or node scores")
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if command == "evidence-ingest-prepare" && (*queryID == 0 || *reviewID == 0) {
		return fmt.Errorf("prepare requires -query and -review")
	}
	if command == "evidence-ingest-apply" && *input == "" {
		return fmt.Errorf("apply requires -input approved.json")
	}
	if command == "coverage-check" && (*source == "" || *owner == "") {
		return fmt.Errorf("coverage-check requires -source monitoring|identity and -owner")
	}
	if command == "coverage-check" && strings.TrimSpace(*source) == "hardware_fault_ledger" {
		// v3: reading the ledger table at now would certify an offline import as
		// complete through now. Refuse before any database access.
		fmt.Fprintln(os.Stderr, "DEPRECATED: coverage-check -source hardware_fault_ledger is refused; use: gpu-risk ledger-declare -through <RFC3339> -owner <name>")
		return fmt.Errorf("coverage check rejected: %w", gpurisk.ErrLedgerDeclarationRequired)
	}
	throughSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "through" {
			throughSet = true
		}
	})
	if throughSet && command != "ledger-declare" {
		return fmt.Errorf("-through is only valid for ledger-declare")
	}
	var declaredThrough time.Time
	if command == "ledger-declare" {
		allowed := map[string]bool{"through": true, "owner": true, "driver": true}
		invalid := false
		flags.Visit(func(f *flag.Flag) {
			if !allowed[f.Name] {
				invalid = true
			}
		})
		if invalid || strings.TrimSpace(*throughText) == "" || strings.TrimSpace(*owner) == "" {
			return fmt.Errorf("ledger-declare requires -through <RFC3339> and -owner <name>, and only accepts -through, -owner and -driver")
		}
		at, e := time.Parse(time.RFC3339Nano, *throughText)
		if e != nil {
			return fmt.Errorf("invalid -through")
		}
		if at.After(time.Now()) {
			return fmt.Errorf("ledger declaration rejected: through is in the future")
		}
		declaredThrough = at
	}
	if command == "coverage-members" && (*queryID == 0 || *source == "" || *owner == "") {
		return fmt.Errorf("coverage-members requires -query and -source hardware_fault_ledger|monitoring|identity and -owner")
	}
	if command == "context-prepare" || command == "coverage-plan" || command == "core-source-plan" {
		allowed := map[string]bool{"query": true, "as-of": true, "driver": true}
		invalid := false
		flags.Visit(func(f *flag.Flag) {
			if !allowed[f.Name] {
				invalid = true
			}
		})
		if invalid || *queryID == 0 {
			return fmt.Errorf("%s requires -query and only accepts -query, -as-of and -driver", command)
		}
		if *asOfText != "" {
			at, e := time.Parse(time.RFC3339Nano, *asOfText)
			if e != nil || at.After(time.Now()) {
				return fmt.Errorf("invalid as-of")
			}
		}
	}
	dsn := os.Getenv("ATLAS_GPU_RISK_DSN")
	if dsn == "" {
		return fmt.Errorf("ATLAS_GPU_RISK_DSN is required")
	}
	var dialector gorm.Dialector
	switch *driver {
	case "postgres":
		dialector = postgres.Open(dsn)
	case "sqlite":
		if command == "context-prepare" || command == "core-source-plan" || command == "coverage-plan" || command == "plan" || command == "outcome-plan" || command == "dataset" || command == "model-request" || command == "evidence-ingest-prepare" {
			if strings.Contains(dsn, "?") || strings.HasPrefix(dsn, "file:") || dsn == ":memory:" {
				return fmt.Errorf("plan requires a plain existing SQLite path")
			}
			if _, e := os.Stat(dsn); e != nil {
				return fmt.Errorf("SQLite plan database must exist")
			}
			uri := url.URL{Scheme: "file", Path: dsn}
			values := url.Values{}
			values.Set("mode", "ro")
			uri.RawQuery = values.Encode()
			dsn = uri.String()
		}
		dialector = sqlite.Open(dsn)
	default:
		return fmt.Errorf("unsupported driver")
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		return fmt.Errorf("database connection failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("database unavailable")
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if command == "context-prepare" || command == "core-source-plan" || command == "coverage-plan" || command == "plan" || command == "outcome-plan" || command == "dataset" || command == "model-request" || command == "evidence-ingest-prepare" {
		// Enforce read-only at the database connection, not only by convention.
		if *driver == "sqlite" {
			if db.Exec("PRAGMA query_only = ON").Error != nil {
				return fmt.Errorf("read-only guard failed")
			}
		} else {
			if db.Exec("SET default_transaction_read_only = on").Error != nil {
				return fmt.Errorf("read-only guard failed")
			}
		}
		if command == "context-prepare" {
			asOf := time.Now().UTC()
			if *asOfText != "" {
				asOf, _ = time.Parse(time.RFC3339Nano, *asOfText)
			}
			report, e := gpurisk.BuildContextPreparation(ctx, db, *queryID, asOf)
			if e != nil {
				return fmt.Errorf("context prepare failed: %w", e)
			}
			return json.NewEncoder(os.Stdout).Encode(report)
		}
		if command == "core-source-plan" {
			asOf := time.Now().UTC()
			if *asOfText != "" {
				asOf, _ = time.Parse(time.RFC3339Nano, *asOfText)
			}
			report, e := gpurisk.BuildCoreSourcePlan(ctx, db, *queryID, asOf)
			if e != nil {
				return fmt.Errorf("core source plan failed: %w", e)
			}
			return json.NewEncoder(os.Stdout).Encode(report)
		}
		if command == "coverage-plan" {
			asOf := time.Now().UTC()
			if *asOfText != "" {
				asOf, _ = time.Parse(time.RFC3339Nano, *asOfText)
			}
			report, e := gpurisk.BuildCoveragePlan(ctx, db, *queryID, asOf)
			if e != nil {
				return fmt.Errorf("coverage plan failed: %w", e)
			}
			return json.NewEncoder(os.Stdout).Encode(report)
		}
		if command == "evidence-ingest-prepare" {
			m, e := gpurisk.IngestPrepareMode(ctx, db, *reviewID, *queryID, *mode == "identity-only")
			if e != nil {
				return fmt.Errorf("ingest prepare rejected: %w", e)
			}
			return json.NewEncoder(os.Stdout).Encode(m)
		}
		if command == "model-request" {
			request, e := gpurisk.BuildModelRequest(ctx, db, *queryID, *modelID)
			if e != nil {
				return fmt.Errorf("model request rejected: model availability, query or source contract")
			}
			return json.NewEncoder(os.Stdout).Encode(request)
		}
		if command == "dataset" {
			if *input == "" {
				return fmt.Errorf("dataset requires -input spec.json")
			}
			f, e := os.Open(*input)
			if e != nil {
				return fmt.Errorf("dataset spec unavailable")
			}
			defer f.Close()
			decoder := json.NewDecoder(io.LimitReader(f, 1024*1024))
			decoder.DisallowUnknownFields()
			var spec gpurisk.DatasetSpec
			if decoder.Decode(&spec) != nil {
				return fmt.Errorf("invalid dataset spec")
			}
			var trailing any
			if decoder.Decode(&trailing) != io.EOF {
				return fmt.Errorf("trailing dataset spec")
			}
			data, e := gpurisk.BuildDataset(ctx, db, spec)
			if e != nil {
				return fmt.Errorf("dataset export rejected; check pinned inputs and temporal contract")
			}
			return json.NewEncoder(os.Stdout).Encode(data)
		}
		if command == "outcome-plan" {
			asOf := time.Now().UTC()
			if *asOfText != "" {
				var e error
				asOf, e = time.Parse(time.RFC3339Nano, *asOfText)
				if e != nil {
					return fmt.Errorf("invalid as-of")
				}
			}
			evaluate := gpurisk.EvaluateOutcomes
			switch *target {
			case "confirmed_hardware_failure":
			case gpurisk.IncidentTarget:
				evaluate = gpurisk.EvaluateIncidentOutcomes
			default:
				return fmt.Errorf("unknown outcome target")
			}
			batch, rows, e := evaluate(ctx, db, *queryID, asOf)
			if e != nil {
				return fmt.Errorf("outcome plan failed")
			}
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"batch": batch, "data": rows})
		}
		p, e := gpurisk.Capture(ctx, db)
		if e != nil {
			return fmt.Errorf("snapshot capture failed; check inventory schema and source timestamps")
		}
		return json.NewEncoder(os.Stdout).Encode(p)
	}
	if command == "outcomes" {
		asOf := time.Now().UTC()
		if *asOfText != "" {
			var e error
			asOf, e = time.Parse(time.RFC3339Nano, *asOfText)
			if e != nil {
				return fmt.Errorf("invalid as-of")
			}
		}
		if *queryID == 0 || asOf.After(time.Now()) {
			return fmt.Errorf("query and valid as-of required")
		}
		if e := gpurisk.Migrate(db.WithContext(ctx)); e != nil {
			return fmt.Errorf("outcome migration failed")
		}
		record := gpurisk.RecordOutcomes
		switch *target {
		case "confirmed_hardware_failure":
		case gpurisk.IncidentTarget:
			record = gpurisk.RecordIncidentOutcomes
		default:
			return fmt.Errorf("unknown outcome target")
		}
		batch, e := record(ctx, db, *queryID, asOf)
		if e != nil {
			return fmt.Errorf("outcome record failed")
		}
		return json.NewEncoder(os.Stdout).Encode(batch)
	}
	if command == "coverage-check" {
		if e := gpurisk.Migrate(db.WithContext(ctx)); e != nil {
			return fmt.Errorf("coverage migration failed")
		}
		identitySource := strings.TrimSpace(os.Getenv("ATLAS_GPU_RISK_IDENTITY_SOURCE"))
		if identitySource == "" {
			identitySource = gpurisk.DefaultIdentitySource
		}
		row, e := gpurisk.RecordSourceCoverageV3(ctx, db, gpurisk.CoverageCheckInput{Source: *source, Owner: *owner}, identitySource)
		if e != nil {
			return fmt.Errorf("coverage check rejected: %w", e)
		}
		return json.NewEncoder(os.Stdout).Encode(row)
	}
	if command == "ledger-declare" {
		if e := gpurisk.Migrate(db.WithContext(ctx)); e != nil {
			return fmt.Errorf("coverage migration failed")
		}
		row, e := gpurisk.DeclareLedgerCoverage(ctx, db, declaredThrough, *owner)
		if e != nil {
			return fmt.Errorf("ledger declaration rejected: %w", e)
		}
		if e := json.NewEncoder(os.Stdout).Encode(row); e != nil {
			return e
		}
		// Mint the anchor query's ledger member coverage now, so a declaration
		// made mid-day is usable before the next query cutoff / stage run.
		anchor, rows, minted, e := gpurisk.MintLedgerAnchorCoverage(ctx, db)
		if errors.Is(e, gpurisk.ErrNoLedgerAnchor) {
			fmt.Fprintln(os.Stdout, "ledger anchor: none (no completed query with cutoff before the declared through); declaration recorded")
			return nil
		}
		if e != nil {
			return fmt.Errorf("ledger declaration %d recorded, but anchor member coverage rejected: %w", row.ID, e)
		}
		fmt.Fprintf(os.Stdout, "ledger anchor query %d cutoff %s: %d member assertions, %d new revisions\n", anchor.ID, anchor.Cutoff.UTC().Format(time.RFC3339Nano), len(rows), minted)
		return nil
	}
	if command == "coverage-members" {
		if e := gpurisk.Migrate(db.WithContext(ctx)); e != nil {
			return fmt.Errorf("coverage migration failed")
		}
		rows, e := gpurisk.RecordMemberCoverage(ctx, db, *queryID, *source, *owner)
		if e != nil {
			return fmt.Errorf("member coverage rejected: %w", e)
		}
		return json.NewEncoder(os.Stdout).Encode(rows)
	}
	if command == "register-model" {
		if e := gpurisk.Migrate(db.WithContext(ctx)); e != nil {
			return fmt.Errorf("model migration failed")
		}
		model, e := gpurisk.RegisterRanker(ctx, db, *datasetFile, *datasetSHA, *artifactFile, *artifactSHA, *scoresFile, *scoresSHA)
		if e != nil {
			return fmt.Errorf("model registration rejected: genuine ready dataset and parity required")
		}
		return json.NewEncoder(os.Stdout).Encode(model)
	}
	if *input == "" {
		return fmt.Errorf("record requires -input frozen-plan.json")
	}
	f, e := os.Open(*input)
	if e != nil {
		return fmt.Errorf("cannot open frozen plan")
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 32*1024*1024))
	decoder.DisallowUnknownFields()
	if command == "evidence-ingest-apply" {
		var approved gpurisk.IngestManifest
		if decoder.Decode(&approved) != nil {
			return fmt.Errorf("invalid approved manifest")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return fmt.Errorf("trailing approved manifest")
		}
		rows, e := gpurisk.IngestApply(ctx, db, approved)
		if e != nil {
			return fmt.Errorf("ingest apply rejected: %w", e)
		}
		return json.NewEncoder(os.Stdout).Encode(rows)
	}
	if command == "model-record" {
		var request gpurisk.ModelScoreRequest
		if decoder.Decode(&request) != nil {
			return fmt.Errorf("invalid score request JSON")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return fmt.Errorf("trailing score request JSON")
		}
		if gpurisk.Migrate(db.WithContext(ctx)) != nil {
			return fmt.Errorf("model migration failed")
		}
		batch, e := gpurisk.RecordModelScores(ctx, db, request)
		if e != nil {
			return fmt.Errorf("model score record rejected: pinned request changed or model unavailable")
		}
		return json.NewEncoder(os.Stdout).Encode(batch)
	}
	if command == "evidence" {
		var evidence gpurisk.EvidenceInput
		if decoder.Decode(&evidence) != nil {
			return fmt.Errorf("invalid evidence JSON")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return fmt.Errorf("trailing evidence JSON")
		}
		if e := gpurisk.Migrate(db.WithContext(ctx)); e != nil {
			return fmt.Errorf("evidence migration failed")
		}
		row, e := gpurisk.AppendEvidence(ctx, db, evidence)
		if e != nil {
			return fmt.Errorf("evidence rejected: validation or revision conflict")
		}
		return json.NewEncoder(os.Stdout).Encode(row)
	}
	var p gpurisk.Snapshot
	if decoder.Decode(&p) != nil {
		return fmt.Errorf("invalid frozen plan JSON")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fmt.Errorf("trailing or oversized plan JSON")
	}
	if _, _, e := gpurisk.Build(p); e != nil {
		return fmt.Errorf("frozen plan validation failed")
	}
	if e := gpurisk.Migrate(db.WithContext(ctx)); e != nil {
		return fmt.Errorf("ranking schema migration failed")
	}
	q, e := gpurisk.Record(ctx, db, p)
	if e != nil {
		return fmt.Errorf("record failed: snapshot, lease or database check did not pass")
	}
	return json.NewEncoder(os.Stdout).Encode(q)
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

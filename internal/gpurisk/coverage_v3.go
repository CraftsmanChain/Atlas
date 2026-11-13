package gpurisk

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// LedgerDeclaredContract marks a hardware_fault_ledger coverage check that is
// an explicit operator delivery declaration ("every fault known up to Through
// is in the ledger"). The ledger is an irregular offline import, so reading
// the table at now can never certify it complete through now.
const LedgerDeclaredContract = "gpu-host-coverage-watermark-v3-declared"

// IdentityBackfillContract marks an identity coverage check whose watermark is
// the RangeEnd of the newest completed identity backfill run, not the clock.
const IdentityBackfillContract = "gpu-host-coverage-watermark-v3-backfill"

// DefaultIdentitySource is the configured history source whose identity
// backfill runs define the identity watermark.
const DefaultIdentitySource = "xiaozhen-zhiyuan-vm"

// identityBackfillJobType is the HistoryBackfillRun.JobType written by
// internal/history StartIdentityBackfill.
const identityBackfillJobType = "gpu_identity_interval"

// ErrLedgerDeclarationRequired refuses the deprecated read-at-now ledger check.
var ErrLedgerDeclarationRequired = errors.New("hardware_fault_ledger coverage-check is deprecated: the ledger is an offline import and is only certified by an explicit `gpu-risk ledger-declare -through <RFC3339> -owner <name>`")

// ErrLedgerDeclarationRejected marks an invalid delivery declaration.
var ErrLedgerDeclarationRejected = errors.New("ledger declaration rejected")

// coverageCheckContractValid reports whether a check contract may certify the
// given source. The ledger is certified only by a declaration (v3).
func coverageCheckContractValid(source, contract string) bool {
	switch source {
	case "hardware_fault_ledger":
		return contract == LedgerDeclaredContract
	case "identity":
		return contract == CoverageWatermarkContract || contract == IdentityBackfillContract
	case "monitoring":
		return contract == CoverageWatermarkContract
	}
	return false
}

// DeclareLedgerCoverage appends an operator delivery declaration for the
// hardware fault ledger. RecordCount/CheckSHA256 are still computed from the
// rows actually read. A declaration in the future, one that regresses behind
// an earlier declaration, or one without a responsible owner is refused.
func DeclareLedgerCoverage(ctx context.Context, db *gorm.DB, through time.Time, owner string) (api.GPURiskSourceCoverageCheck, error) {
	var out api.GPURiskSourceCoverageCheck
	owner = strings.TrimSpace(owner)
	if owner == "" || len(owner) > 128 {
		return out, fmt.Errorf("%w: owner is required", ErrLedgerDeclarationRejected)
	}
	now := evidenceNow().UTC().Truncate(time.Microsecond)
	through = through.UTC().Truncate(time.Microsecond)
	if through.IsZero() {
		return out, fmt.Errorf("%w: through is required", ErrLedgerDeclarationRejected)
	}
	if through.After(now) {
		return out, fmt.Errorf("%w: through is in the future", ErrLedgerDeclarationRejected)
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var prior []api.GPURiskSourceCoverageCheck
		if e := tx.Where("source = ? AND contract = ?", "hardware_fault_ledger", LedgerDeclaredContract).Order("verified_through DESC, id DESC").Limit(1).Find(&prior).Error; e != nil {
			return e
		}
		if len(prior) == 1 && through.Before(prior[0].VerifiedThrough.UTC()) {
			return fmt.Errorf("%w: through regresses behind the previous declaration", ErrLedgerDeclarationRejected)
		}
		keys, e := ledgerKeys(ctx, tx)
		if e != nil {
			return e
		}
		sha, e := digest(keys)
		if e != nil {
			return e
		}
		out = api.GPURiskSourceCoverageCheck{Source: "hardware_fault_ledger", VerifiedThrough: through, ObservedAt: now, RecordCount: len(keys), CheckSHA256: sha, Owner: owner, Contract: LedgerDeclaredContract, CreatedAt: now}
		return tx.Create(&out).Error
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return api.GPURiskSourceCoverageCheck{}, err
	}
	return out, nil
}

// identityWatermark is the RangeEnd of the newest completed identity backfill
// run of the configured source. Without one, the source is incomplete.
func identityWatermark(ctx context.Context, db *gorm.DB, sourceKey string, now time.Time) (time.Time, error) {
	var runs []api.HistoryBackfillRun
	if err := db.WithContext(ctx).Where("source_key = ? AND job_type = ? AND status = ? AND finished_at IS NOT NULL", sourceKey, identityBackfillJobType, "completed").Order("finished_at DESC, id DESC").Limit(1).Find(&runs).Error; err != nil {
		return time.Time{}, err
	}
	if len(runs) != 1 {
		return time.Time{}, ErrCoverageSourceIncomplete
	}
	through := runs[0].RangeEnd.UTC().Truncate(time.Microsecond)
	if through.IsZero() || through.After(now) || !runs[0].RangeStart.Before(runs[0].RangeEnd) {
		return time.Time{}, ErrCoverageSourceIncomplete
	}
	return through, nil
}

// buildSourceCheckV3 computes, without writing, the v3 coverage check for
// monitoring or identity. The ledger is refused: it is declared, not read.
func buildSourceCheckV3(ctx context.Context, db *gorm.DB, source, owner, identitySource string) (api.GPURiskSourceCoverageCheck, error) {
	var out api.GPURiskSourceCoverageCheck
	source = strings.TrimSpace(source)
	owner = strings.TrimSpace(owner)
	if owner == "" || len(owner) > 128 {
		return out, ErrInput
	}
	now := evidenceNow().UTC().Truncate(time.Microsecond)
	contract := CoverageWatermarkContract
	var keys []string
	var through time.Time
	var err error
	switch source {
	case "hardware_fault_ledger":
		return out, ErrLedgerDeclarationRequired
	case "identity":
		if strings.TrimSpace(identitySource) == "" {
			return out, ErrInput
		}
		through, err = identityWatermark(ctx, db, strings.TrimSpace(identitySource), now)
		if err != nil {
			return out, err
		}
		keys, err = identityKeys(ctx, db)
		contract = IdentityBackfillContract
	case "monitoring":
		keys, through, err = readSourceCoverage(ctx, db, source, now)
	default:
		return out, ErrInput
	}
	if err != nil {
		return out, err
	}
	sha, err := digest(keys)
	if err != nil {
		return out, err
	}
	return api.GPURiskSourceCoverageCheck{Source: source, VerifiedThrough: through, ObservedAt: now, RecordCount: len(keys), CheckSHA256: sha, Owner: owner, Contract: contract, CreatedAt: now}, nil
}

// RecordSourceCoverageV3 is the v3 source check used by the CLI and the daily
// coverage stage. monitoring keeps its audit watermark; identity uses the
// newest completed identity backfill RangeEnd; the ledger is refused
// (ErrLedgerDeclarationRequired). It always appends a row.
func RecordSourceCoverageV3(ctx context.Context, db *gorm.DB, in CoverageCheckInput, identitySource string) (api.GPURiskSourceCoverageCheck, error) {
	out, err := buildSourceCheckV3(ctx, db, in.Source, in.Owner, identitySource)
	if err != nil {
		return api.GPURiskSourceCoverageCheck{}, err
	}
	if err := db.WithContext(ctx).Create(&out).Error; err != nil {
		return api.GPURiskSourceCoverageCheck{}, err
	}
	return out, nil
}

// ensureSourceCheckV3 is RecordSourceCoverageV3 with head reuse: when the
// newest check of the source already states the identical watermark over the
// identical records under the same contract and owner, it is reused instead of
// appending, so an unchanged state re-runs without minting member revisions.
func ensureSourceCheckV3(ctx context.Context, db *gorm.DB, source, owner, identitySource string) (api.GPURiskSourceCoverageCheck, error) {
	candidate, err := buildSourceCheckV3(ctx, db, source, owner, identitySource)
	if err != nil {
		return api.GPURiskSourceCoverageCheck{}, err
	}
	var heads []api.GPURiskSourceCoverageCheck
	if err := db.WithContext(ctx).Where("source = ?", candidate.Source).Order("observed_at DESC, id DESC").Limit(1).Find(&heads).Error; err != nil {
		return api.GPURiskSourceCoverageCheck{}, err
	}
	if len(heads) == 1 {
		h := heads[0]
		if h.Contract == candidate.Contract && h.Owner == candidate.Owner && h.RecordCount == candidate.RecordCount && h.CheckSHA256 == candidate.CheckSHA256 && h.VerifiedThrough.UTC().Truncate(time.Microsecond).Equal(candidate.VerifiedThrough) && !h.ObservedAt.After(candidate.ObservedAt) {
			return h, nil
		}
	}
	if err := db.WithContext(ctx).Create(&candidate).Error; err != nil {
		return api.GPURiskSourceCoverageCheck{}, err
	}
	return candidate, nil
}

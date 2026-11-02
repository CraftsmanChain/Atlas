package gpurisk

import (
	"context"
	"errors"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

// ErrCoverageSourceUnsupported marks a history source that has no single
// canonical durable table in this repository, so no honest completeness
// watermark can be derived from real rows. It is never synthesized.
var ErrCoverageSourceUnsupported = errors.New("coverage source has no canonical durable table")

// CoverageCheckInput names the history source an operator pulled in full. It
// deliberately carries no timestamp: ObservedAt is the server clock taken while
// the source is actually read and is never accepted from a caller.
type CoverageCheckInput struct {
	Source string
	Owner  string
}

// RecordSourceCoverage reads the named source and appends an immutable record
// that it was complete through the current server time. RecordCount and
// CheckSHA256 come from the rows actually read, never from the caller, so the
// record restates what was visible when it ran. VerifiedThrough equals
// ObservedAt (rule A: the moment of a full pull); a new row is always appended,
// and consumers take the latest.
func RecordSourceCoverage(ctx context.Context, db *gorm.DB, in CoverageCheckInput) (api.GPURiskSourceCoverageCheck, error) {
	var out api.GPURiskSourceCoverageCheck
	source := strings.TrimSpace(in.Source)
	owner := strings.TrimSpace(in.Owner)
	if owner == "" || len(owner) > 128 {
		return out, ErrInput
	}
	keys := []string{}
	switch source {
	case "hardware_fault_ledger":
		var rows []api.HardwareFaultFeedbackRequest
		if err := db.WithContext(ctx).Order("request_key").Find(&rows).Error; err != nil {
			return out, err
		}
		for _, r := range rows {
			keys = append(keys, r.RequestKey)
		}
	case "identity":
		var rows []api.HistoricalGPUIdentityInterval
		if err := db.WithContext(ctx).Order("interval_key").Find(&rows).Error; err != nil {
			return out, err
		}
		for _, r := range rows {
			keys = append(keys, r.IntervalKey)
		}
	case "monitoring":
		return out, ErrCoverageSourceUnsupported
	default:
		return out, ErrInput
	}
	sha, err := digest(keys)
	if err != nil {
		return out, err
	}
	now := evidenceNow().UTC().Truncate(time.Microsecond)
	out = api.GPURiskSourceCoverageCheck{
		Source:          source,
		VerifiedThrough: now,
		ObservedAt:      now,
		RecordCount:     len(keys),
		CheckSHA256:     sha,
		Owner:           owner,
		Contract:        CoverageWatermarkContract,
		CreatedAt:       now,
	}
	if err := db.WithContext(ctx).Create(&out).Error; err != nil {
		return api.GPURiskSourceCoverageCheck{}, err
	}
	return out, nil
}

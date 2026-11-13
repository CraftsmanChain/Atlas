package gpurisk

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

const OutcomeVersion = "gpu-node-outcome-v1"
const HistorySourceVersion = "gpu-host-history-source-v1"
const CoverageWatermarkContract = "gpu-host-coverage-watermark-v2"

// evidenceNow is the server clock; tests can advance ingestion time without
// importing timestamps or rewriting immutable revisions. Never sourced from input.
var evidenceNow = time.Now

// HostContext is an explicitly reviewed physical-host assertion, never a lookup
// of current inventory for an old prediction cutoff. Missing values stay null.
type HostContext struct {
	HostType     string `json:"host_type"`
	GPUModel     string `json:"gpu_model"`
	Manufacturer string `json:"manufacturer"`
	MainBoard    string `json:"main_board"`
	QuotaGroup   string `json:"quota_group"`
	DataCenter   string `json:"data_center"`
	Rack         string `json:"rack"`
}

// EvidenceInput is a controlled human assertion, not an automatic claim that
// telemetry or incident ingestion was complete. All three coverage sources are
// mandatory for a healthy negative: hardware_fault_ledger, monitoring, identity.
type EvidenceInput struct {
	SourceVersion string       `json:"source_version,omitempty"`
	Context       *HostContext `json:"context,omitempty"`
	Severity      string       `json:"severity,omitempty"`
	Kind          string       `json:"kind"`
	Key           string       `json:"key"`
	PreviousSHA   string       `json:"previous_sha"`
	Owner         string       `json:"owner"`
	EvidenceSHA   string       `json:"evidence_sha"`
	NodeID        uint         `json:"node_id"`
	Generation    int          `json:"generation"`
	QueryID       uint         `json:"query_id"`
	EntityRef     string       `json:"entity_ref"`
	From          time.Time    `json:"from"`
	// For coverage, Through is complete_through and must not exceed ingestion time.
	Through    time.Time `json:"through"`
	FeedbackID uint      `json:"feedback_id"`
	ReviewSHA  string    `json:"review_sha"`
	GPUUUID    string    `json:"gpu_uuid"`
	GPUIndex   *int      `json:"gpu_index,omitempty"`
	Source     string    `json:"source"`
	Complete   bool      `json:"complete"`
	State      string    `json:"state"`
}

func validSHA(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func evidenceValid(db *gorm.DB, in EvidenceInput, now time.Time) error {
	if len(in.Key) == 0 || len(in.Key) > 128 || strings.TrimSpace(in.Owner) == "" || len(in.Owner) > 128 || len(in.EntityRef) > 256 || len(in.GPUUUID) > 128 || !validSHA(in.EvidenceSHA) || in.NodeID == 0 || in.Generation < 0 || in.From.IsZero() || in.Through.IsZero() || in.Through.Before(in.From) || in.Through.After(now) {
		return ErrInput
	}
	if in.PreviousSHA != "" && !validSHA(in.PreviousSHA) {
		return ErrInput
	}
	if in.Kind != "host_context" && in.Kind != "episode_state" && (in.SourceVersion != "" || in.Context != nil || in.Severity != "") {
		return ErrInput
	}
	switch in.Kind {
	case "host_context", "episode_state":
		if err := historyEvidenceValid(db, in); err != nil {
			return err
		}
	case "identity":
		if in.FeedbackID == 0 || !validSHA(in.ReviewSHA) || in.EntityRef == "" || in.QueryID != 0 || in.Source != "" || in.State != "" {
			return ErrInput
		}
		var review api.HardwareFaultFeedbackReview
		if err := db.Where("feedback_request_id = ? AND review_sha256 = ?", in.FeedbackID, in.ReviewSHA).First(&review).Error; err != nil {
			return ErrInput
		}
		if in.GPUUUID != review.ConfirmedGPUUUID || (in.GPUIndex != nil && (in.GPUUUID == "" || *in.GPUIndex < 0 || *in.GPUIndex != review.ConfirmedGPUIndex)) {
			return ErrInput
		}
		var node api.GPUNode
		if err := db.First(&node, in.NodeID).Error; err != nil {
			return ErrInput
		}
		// Current identity is deliberately NOT used as historical proof. The CLI
		// assertion supplies its own interval and evidence; outcome matches frozen members.
	case "coverage":
		if in.QueryID == 0 || in.EntityRef == "" || in.FeedbackID != 0 || in.ReviewSHA != "" || in.State != "" || in.GPUUUID != "" {
			return ErrInput
		}
		if in.Source != "hardware_fault_ledger" && in.Source != "monitoring" && in.Source != "identity" {
			return ErrInput
		}
		var m api.GPURiskMember
		if err := db.Where("query_id = ? AND node_id = ? AND generation = ? AND entity_ref = ?", in.QueryID, in.NodeID, in.Generation, in.EntityRef).First(&m).Error; err != nil {
			return ErrInput
		}
	case "repair", "intervention":
		if in.EntityRef == "" || (in.Kind == "repair" && in.FeedbackID == 0) || (in.FeedbackID != 0 && !validSHA(in.ReviewSHA)) || (in.FeedbackID == 0 && in.ReviewSHA != "") || in.Source != "" {
			return ErrInput
		}
		var review api.HardwareFaultFeedbackReview
		if in.FeedbackID != 0 && db.Where("feedback_request_id = ? AND review_sha256 = ?", in.FeedbackID, in.ReviewSHA).First(&review).Error != nil {
			return ErrInput
		}
		states := map[string]bool{"in_progress": true, "completed": true, "observing": true, "recovered": true, "recurred": true, "withdrawn": true}
		if !states[in.State] {
			return ErrInput
		}
	default:
		return ErrInput
	}
	return nil
}

// AppendEvidence is CAS against the previous immutable SHA. AvailableAt is
// assigned by this server/CLI, never accepted from an imported/backdated file.
func AppendEvidence(ctx context.Context, db *gorm.DB, in EvidenceInput) (api.GPURiskEvidenceRevision, error) {
	in.From = in.From.UTC().Truncate(time.Microsecond)
	in.Through = in.Through.UTC().Truncate(time.Microsecond)
	var out api.GPURiskEvidenceRevision
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := evidenceNow().UTC().Truncate(time.Microsecond)
		if err := evidenceValid(tx, in, now); err != nil {
			return err
		}
		// Serialize writers per logical evidence chain in PostgreSQL. Unique indexes
		// also protect first insertion on SQLite and unexpected competing clients.
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", in.Kind+":"+in.Key).Error; err != nil {
				return err
			}
		}
		var prev api.GPURiskEvidenceRevision
		err := tx.Where("kind = ? AND key = ?", in.Kind, in.Key).Order("revision DESC").First(&prev).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		data, _ := json.Marshal(in)
		if prev.ID != 0 && prev.Data == string(data) {
			out = prev
			return nil
		}
		if (prev.ID == 0 && in.PreviousSHA != "") || (prev.ID != 0 && in.PreviousSHA != prev.SHA) {
			return ErrFence
		}
		if prev.ID != 0 {
			var prior EvidenceInput
			if json.Unmarshal([]byte(prev.Data), &prior) != nil {
				return ErrInput
			}
			if prior.Kind != in.Kind || (in.Kind != "identity" && (prior.NodeID != in.NodeID || prior.Generation != in.Generation || prior.EntityRef != in.EntityRef)) || prior.FeedbackID != in.FeedbackID || prior.QueryID != in.QueryID || prior.Source != in.Source {
				return ErrInput
			}
		}
		sha, e := digest([]any{OutcomeVersion, in, prev.Revision + 1, now})
		if e != nil {
			return e
		}
		out = api.GPURiskEvidenceRevision{Kind: in.Kind, Key: in.Key, Revision: prev.Revision + 1, PreviousSHA: prev.SHA, SHA: sha, AvailableAt: now, Data: string(data)}
		return tx.Create(&out).Error
	})
	return out, err
}

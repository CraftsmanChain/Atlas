package eventaudit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func fixture(t *testing.T) (*gorm.DB, time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "audit.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&api.GPUFaultEvent{}, &api.FailureLabel{}, &api.HistoricalGPUIdentityInterval{}, &api.HardwareFaultFeedbackRequest{}, &api.HardwareFaultFeedbackReview{}, &api.PlatformIssue{}, &api.IssueResolution{}, &api.GPUAsset{}, &api.GPUNode{}, &api.GPURiskMember{}, &api.GPURiskQuery{}, &api.GPURiskEvidenceRevision{}, &api.GPURiskSourceCoverageCheck{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour)
	e := api.GPUFaultEvent{ID: 1, EpisodeKey: "GPU-A:reset", GPUUUID: "GPU-A", NodeIP: "10.1.1.1", RuleCode: "gpu_reset_required", State: "open", Evidence: "PRIVATE-EVIDENCE-DO-NOT-EXPORT", FirstObservedAt: now, LastObservedAt: now, CreatedAt: now, UpdatedAt: now}
	if err = db.Create(&e).Error; err != nil {
		t.Fatal(err)
	}
	return db, time.Now().UTC().Add(time.Minute)
}
func capture(t *testing.T, db *gorm.DB, now time.Time) Snapshot {
	t.Helper()
	s, e := Capture(context.Background(), db, "cluster-a", now)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func action(t *testing.T, old Snapshot, new Snapshot) string {
	t.Helper()
	r, e := Compare(&old, new)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Changes) != 1 {
		t.Fatalf("changes=%+v", r)
	}
	return r.Changes[0].Action
}
func TestCaptureDeltaLifecycleAndPrivacy(t *testing.T) {
	db, now := fixture(t)
	s := capture(t, db, now)
	again := capture(t, db, now)
	if s.SHA != again.SHA {
		t.Fatal("unstable repeated read")
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "10.1.1.1") {
		t.Fatal("private payload leaked")
	}
	if got := action(t, s, again); got != "unchanged" {
		t.Fatal(got)
	}
	if err := db.Model(&api.GPUFaultEvent{}).Where("id=1").Updates(map[string]any{"occurrence_count": 2, "last_observed_at": now.Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	heart := capture(t, db, now)
	if got := action(t, s, heart); got != "observation_only" {
		t.Fatal(got)
	}
	if err := db.Model(&api.GPUFaultEvent{}).Where("id=1").Updates(map[string]any{"state": "recovered", "recovered_at": now.Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	recovered := capture(t, db, now)
	if got := action(t, heart, recovered); got != "changed" {
		t.Fatal(got)
	}
	if err := db.Delete(&api.GPUFaultEvent{}, 1).Error; err != nil {
		t.Fatal(err)
	}
	empty := capture(t, db, now)
	if got := action(t, recovered, empty); got != "missing" {
		t.Fatal(got)
	}
}
func TestAssociatedEvidenceAndFreshness(t *testing.T) {
	for _, kind := range []string{"late_review", "identity_revised_same_id", "label_excluded", "evidence_private_data", "event_evidence", "rule_version", "freshness"} {
		t.Run(kind, func(t *testing.T) {
			db, now := fixture(t)
			if kind == "identity_revised_same_id" {
				if e := db.Create(&api.HistoricalGPUIdentityInterval{ID: 1, IntervalKey: "i", SourceKey: "x", GPUUUID: "GPU-A", NodeIP: "10.1.1.1"}).Error; e != nil {
					t.Fatal(e)
				}
			}
			if kind == "label_excluded" {
				if e := db.Create(&api.FailureLabel{ID: 1, LabelKey: "l", SourceType: "gpu_fault_event", SourceRecordID: 1}).Error; e != nil {
					t.Fatal(e)
				}
			}
			if kind == "evidence_private_data" {
				if e := db.Create(&api.GPURiskEvidenceRevision{ID: 1, Kind: "identity", Key: "x", SHA: "a", Data: "private-a"}).Error; e != nil {
					t.Fatal(e)
				}
			}
			old := capture(t, db, now)
			var err error
			switch kind {
			case "late_review":
				err = db.Create(&api.HardwareFaultFeedbackReview{ID: 1, FeedbackRequestID: 99, Revision: 1, ConfirmedGPUUUID: "GPU-A", Decision: "confirmed_hardware", ReviewSHA256: "r", ReviewNote: "PRIVATE-REVIEW"}).Error
			case "identity_revised_same_id":
				err = db.Model(&api.HistoricalGPUIdentityInterval{}).Where("id=1").Update("evidence_strength", "revoked").Error
			case "label_excluded":
				err = db.Model(&api.FailureLabel{}).Where("id=1").Update("excluded", true).Error
			case "evidence_private_data":
				err = db.Model(&api.GPURiskEvidenceRevision{}).Where("id=1").Update("data", "private-b").Error
			case "event_evidence":
				err = db.Model(&api.GPUFaultEvent{}).Where("id=1").Update("evidence", "PRIVATE-NEW").Error
			case "rule_version":
				err = db.Model(&api.GPUFaultEvent{}).Where("id=1").Update("rule_version", "new-contract").Error
			case "freshness":
				now = now.Add(25 * time.Hour)
			}
			if err != nil {
				t.Fatal(err)
			}
			next := capture(t, db, now)
			if got := action(t, old, next); got != "changed" {
				t.Fatal(got)
			}
			r, _ := Compare(&old, next)
			if r.TrainingEligible || !r.NoActionExecuted {
				t.Fatal("unsafe admission")
			}
		})
	}
}
func TestCheckpointValidationAndBaseline(t *testing.T) {
	db, now := fixture(t)
	s := capture(t, db, now)
	r, e := Compare(nil, s)
	if e != nil || r.Counts["comparison_unknown"] != 1 {
		t.Fatal(r, e)
	}
	old := s
	old.Namespace = "cluster-b"
	old.Seal()
	if got := action(t, old, s); got != "comparison_unknown" {
		t.Fatal(got)
	}
	old = s
	old.Version = "v1"
	if got := action(t, old, s); got != "comparison_unknown" {
		t.Fatal(got)
	}
	old = s
	old.AsOf = now.Add(time.Hour)
	old.Seal()
	if _, e = Compare(&old, s); e == nil {
		t.Fatal("time reversal allowed")
	}
	bad := s
	bad.Complete = false
	bad.Seal()
	if bad.Validate() == nil {
		t.Fatal("partial checkpoint allowed")
	}
	bad = s
	bad.Entries = append(append([]Entry{}, s.Entries...), s.Entries[0])
	bad.Seal()
	if bad.Validate() == nil {
		t.Fatal("duplicate allowed")
	}
	bad = s
	bad.SHA = "bad"
	if bad.Validate() == nil {
		t.Fatal("tamper allowed")
	}
	// A genuinely new ID is new only against a valid compatible prior baseline.
	empty := s
	empty.Entries = []Entry{}
	empty.Seal()
	if got := action(t, empty, s); got != "new" {
		t.Fatal(got)
	}
	if _, e = Capture(context.Background(), db, "invalid/namespace", now); e == nil {
		t.Fatal("namespace allowed")
	}
	if _, e = Capture(context.Background(), db, "cluster-a", now.Add(-2*time.Hour)); e == nil {
		t.Fatal("historical current-row reconstruction allowed")
	}
}

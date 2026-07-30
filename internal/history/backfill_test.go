package history

import (
	"fmt"
	"testing"
	"time"

	"atlas/pkg/config"
	"atlas/pkg/storage"
)

func TestAlertBackfillCreatesReviewableEpisodes(t *testing.T) {
	prometheus := newTestPrometheus(t)
	defer prometheus.Close()
	db, err := storage.InitDB(fmt.Sprintf("file:history-backfill-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{
		DatasetDir: "/mnt/public/atlas/training",
		Sources: []config.HistorySourceConfig{{
			ID: "primary", Name: "Primary", Type: "prometheus", BaseURL: prometheus.URL, Enabled: true,
		}},
	}, time.Second)
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	run, err := service.StartAlertBackfill(BackfillRequest{SourceKey: "primary", Start: &start, End: &end})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, listErr := service.BackfillRuns(1)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(runs) == 1 && runs[0].Status == "completed" {
			run = runs[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "completed" || run.ChunksCompleted != 1 || run.SignalPoints != 7 {
		t.Fatalf("unexpected run: %+v", run)
	}
	summary, rows, err := service.Candidates(100)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 4 || summary.Pending != 4 || len(rows) != 4 {
		t.Fatalf("summary=%+v rows=%+v", summary, rows)
	}
	if summary.ByEventCode["79"] != 2 || summary.ByEventCode["94"] != 1 {
		t.Fatalf("unexpected event codes: %+v", summary.ByEventCode)
	}
	for _, row := range rows {
		if row.ReviewStatus != "pending_review" {
			t.Fatalf("candidate was silently promoted: %+v", row)
		}
		if row.EventCode == "79" && row.QualityTier != "strong_proxy" {
			t.Fatalf("unexpected XID 79 quality: %+v", row)
		}
		if row.EventCode == "94" && (row.OperationalPriority != "high" || row.QualityTier != "strong_proxy") {
			t.Fatalf("high-priority XID classification mismatch: %+v", row)
		}
		if row.EventCode == "uncorrectable_remapped_rows" &&
			(row.HardwareCertainty != "deterministic_hardware" || row.TrainingDisposition != "positive_after_identity_review") {
			t.Fatalf("uncorrectable rows classification mismatch: %+v", row)
		}
	}
}

func TestSignalClassificationSeparatesOperationsFromTrainingUse(t *testing.T) {
	low := classifySignal(map[string]string{"alert_template": "XID故障-低优先级", "err_code": "31"}, "ALERTS")
	if low.OperationalPriority != "low" || low.HardwareCertainty != "operational_signal" ||
		low.TrainingDisposition != "context_only" {
		t.Fatalf("unexpected low-priority classification: %+v", low)
	}
	recovery := classifySignal(map[string]string{"alert_template": "XID故障", "Xid": "120 | 154"}, "ALERTS")
	if !recovery.RecoveryAware || recovery.OperationalPriority != "critical" ||
		recovery.HardwareCertainty == "deterministic_hardware" {
		t.Fatalf("unexpected recovery-aware classification: %+v", recovery)
	}
	dropout := classifySignal(map[string]string{"alert_template": "GPU掉卡"}, "ALERTS")
	if dropout.HardwareCertainty != "deterministic_hardware" ||
		dropout.TrainingDisposition != "positive_after_identity_review" {
		t.Fatalf("unexpected dropout classification: %+v", dropout)
	}
}

func TestAlertBackfillRejectsConcurrentRun(t *testing.T) {
	service := &Service{backfillRunning: true}
	if _, err := service.StartAlertBackfill(BackfillRequest{}); err == nil {
		t.Fatal("expected concurrent backfill rejection")
	}
}

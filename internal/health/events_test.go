package health

import (
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestReconcileFaultEventsDeduplicatesRecoversAndReopens(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	asset := api.GPUAsset{ID: 9, CurrentUUID: "GPU-A", NodeIP: "10.0.0.1", GPUIndex: 2, ModelName: "NVIDIA H100"}
	hit := ruleHit{code: "gpu_temp_high", domain: "thermal", severity: "warning", deduction: 10, value: 82, threshold: ">= 80C", evidence: "GPU temperature max 15m=82.0C"}
	t0 := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)

	if err := reconcileFaultEvents(db.DB, asset, api.GPUHealthScore{ID: 1}, []ruleHit{hit}, "A", t0, "test-v1"); err != nil {
		t.Fatal(err)
	}
	if err := reconcileFaultEvents(db.DB, asset, api.GPUHealthScore{ID: 2}, []ruleHit{hit}, "A", t0.Add(30*time.Minute), "test-v1"); err != nil {
		t.Fatal(err)
	}
	var event api.GPUFaultEvent
	if err := db.First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.State != "open" || event.OccurrenceCount != 2 || event.LatestScoreID != 2 {
		t.Fatalf("unexpected open event: %+v", event)
	}

	if err := reconcileFaultEvents(db.DB, asset, api.GPUHealthScore{ID: 3}, nil, "D", t0.Add(time.Hour), "test-v1"); err != nil {
		t.Fatal(err)
	}
	db.First(&event, event.ID)
	if event.State != "open" {
		t.Fatalf("confidence D incorrectly recovered the event: %+v", event)
	}

	if err := reconcileFaultEvents(db.DB, asset, api.GPUHealthScore{ID: 4}, nil, "A", t0.Add(90*time.Minute), "test-v1"); err != nil {
		t.Fatal(err)
	}
	db.First(&event, event.ID)
	if event.State != "recovered" || event.RecoveredAt == nil {
		t.Fatalf("observable clear did not recover the event: %+v", event)
	}

	if err := reconcileFaultEvents(db.DB, asset, api.GPUHealthScore{ID: 5}, []ruleHit{hit}, "A", t0.Add(2*time.Hour), "test-v1"); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&api.GPUFaultEvent{}).Where("episode_key = ?", "GPU-A:gpu_temp_high").Count(&count)
	if count != 2 {
		t.Fatalf("expected a new episode after recovery, got %d", count)
	}
}

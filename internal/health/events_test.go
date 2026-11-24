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

func TestReconcileThermalEventsAccumulatesAcrossRecovery(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	asset := api.GPUAsset{ID: 1, CurrentUUID: "GPU-T", NodeIP: "10.0.0.1", GPUIndex: 2, ModelName: "RTX 4090"}
	hit := ruleHit{code: "gpu_temp_sustained_15m", domain: "thermal", severity: "warning", deduction: 15, value: 80, threshold: ">= 80C continuously for 15m", evidence: "sustained"}
	t0 := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)
	if err := reconcileFaultEvents(db.DB, asset, api.GPUHealthScore{ID: 1}, []ruleHit{hit}, "A", t0, "test"); err != nil {
		t.Fatal(err)
	}
	if err := reconcileFaultEvents(db.DB, asset, api.GPUHealthScore{ID: 2}, nil, "A", t0.Add(10*time.Minute), "test"); err != nil {
		t.Fatal(err)
	}
	if err := reconcileFaultEvents(db.DB, asset, api.GPUHealthScore{ID: 3}, []ruleHit{hit}, "A", t0.Add(20*time.Minute), "test"); err != nil {
		t.Fatal(err)
	}
	var events []api.GPUFaultEvent
	if err := db.Where("episode_key = ?", "GPU-T:gpu_temp_sustained_15m").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].State != "open" || events[0].OccurrenceCount != 2 || !events[0].LastObservedAt.Equal(t0.Add(20*time.Minute)) {
		t.Fatalf("thermal recurrences must reopen and accumulate one event: %+v", events)
	}
}

func TestXIDEventsAreRecordedClosed(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	asset := api.GPUAsset{ID: 9, CurrentUUID: "GPU-X", NodeIP: "10.0.0.1", GPUIndex: 1, ModelName: "NVIDIA H100"}
	hit := ruleHit{code: "recent_xid_change", domain: "stability", severity: "attention", value: 94, threshold: "XID 94 onset in 24h", evidence: "XID 94: 1 new in 24h, 1 in 7d"}
	t0 := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)

	if err := reconcileFaultEvents(db.DB, asset, api.GPUHealthScore{ID: 1}, []ruleHit{hit}, "A", t0, "test"); err != nil {
		t.Fatal(err)
	}
	hit.severity = "warning"
	if err := reconcileFaultEvents(db.DB, asset, api.GPUHealthScore{ID: 2}, []ruleHit{hit}, "A", t0.Add(10*time.Minute), "test"); err != nil {
		t.Fatal(err)
	}
	var events []api.GPUFaultEvent
	db.Order("id").Find(&events)
	if len(events) != 1 || events[0].State != "recovered" || events[0].RecoveredAt == nil || !events[0].RecoveredAt.Equal(t0) || events[0].OccurrenceCount != 2 || events[0].Severity != "warning" {
		t.Fatalf("XID hits within the window must update one closed event: %+v", events)
	}
	if err := reconcileFaultEvents(db.DB, asset, api.GPUHealthScore{ID: 3}, []ruleHit{hit}, "A", t0.Add(10*time.Minute+25*time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	db.Order("id").Find(&events)
	if len(events) != 2 || events[1].State != "recovered" {
		t.Fatalf("XID hit after the window must create a new closed event: %+v", events)
	}
}

func TestCloseOpenOneShotEventsClosesLegacyXID(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	first := now.Add(-time.Hour)
	events := []api.GPUFaultEvent{
		{EpisodeKey: "GPU-A:recent_xid_change", Source: healthRuleEventSource, State: "open", GPUUUID: "GPU-A", RuleCode: "recent_xid_change", FirstObservedAt: first, LastObservedAt: now},
		{EpisodeKey: "GPU-C:gpu_temp_sustained_15m", Source: healthRuleEventSource, State: "open", GPUUUID: "GPU-C", RuleCode: "gpu_temp_sustained_15m", FirstObservedAt: first, LastObservedAt: now},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	if err := closeOpenOneShotEvents(db.DB); err != nil {
		t.Fatal(err)
	}
	var got []api.GPUFaultEvent
	db.Order("id").Find(&got)
	if got[0].State != "recovered" || got[0].RecoveredAt == nil || !got[0].RecoveredAt.Equal(first) || got[1].State != "open" {
		t.Fatalf("unexpected states: %s %s", got[0].State, got[1].State)
	}
}

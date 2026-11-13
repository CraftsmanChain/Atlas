package history

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

// P1 taskcard §4 item 4: an incremental identity backfill (the daily
// coverage sub-step) may refresh derived fields of existing intervals, but the
// protected identity fields never change.
func TestIdentityBackfillPreservesProtectedIdentityFields(t *testing.T) {
	prometheus := newTestPrometheus(t)
	defer prometheus.Close()
	db, err := storage.InitDB(fmt.Sprintf("file:history-identity-protected-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	// A pre-existing interval with the same key as a scanned series but with
	// different host labels: the backfill must not rewrite them.
	key := historicalIdentityKey("primary", "10.0.0.9", "0000:01:00.0", "GPU-OLD", "0")
	seeded := api.HistoricalGPUIdentityInterval{
		IntervalKey: key, SourceKey: "primary", BackfillRunID: 77, NodeIP: "10.0.0.9",
		HostID: "HOST-PRESEEDED", HostSerial: "SN-PRESEEDED", GPUIndex: 0, GPUUUID: "GPU-old",
		PCIBusID: "0000:01:00.0", FirstSeenAt: time.Unix(1782000000, 0).UTC(), LastSeenAt: time.Unix(1782100000, 0).UTC(),
		ObservationCount: 3, EvidenceStrength: "observed_identity",
	}
	if err := db.Create(&seeded).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, config.HistoryConfig{Sources: []config.HistorySourceConfig{{ID: "primary", Name: "Primary", Type: "prometheus", BaseURL: prometheus.URL, Enabled: true}}}, time.Second)
	runBackfill := func(start, end time.Time) api.HistoryBackfillRun {
		// The previous run marks itself completed before releasing the
		// single-backfill guard; retry only that transient refusal.
		var run api.HistoryBackfillRun
		var err error
		for attempt := 0; attempt < 200; attempt++ {
			run, err = service.StartIdentityBackfill(BackfillRequest{SourceKey: "primary", Start: &start, End: &end})
			if err == nil || !strings.Contains(err.Error(), "already running") {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var current api.HistoryBackfillRun
			if err := db.First(&current, run.ID).Error; err != nil {
				t.Fatal(err)
			}
			if current.Status == "completed" || current.Status == "failed" {
				if current.Status != "completed" {
					t.Fatalf("backfill failed: %+v", current)
				}
				return current
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("backfill did not finish")
		return api.HistoryBackfillRun{}
	}
	type protected struct {
		UUID, NodeIP, IntervalKey, HostSerial, HostID, PCIBusID string
		GPUIndex                                                int
	}
	snapshot := func() map[uint]protected {
		var rows []api.HistoricalGPUIdentityInterval
		if err := db.Order("id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		out := map[uint]protected{}
		for _, r := range rows {
			out[r.ID] = protected{r.GPUUUID, r.NodeIP, r.IntervalKey, r.HostSerial, r.HostID, r.PCIBusID, r.GPUIndex}
		}
		return out
	}
	initial := snapshot()
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	first := runBackfill(start, start.Add(7*24*time.Hour))
	if first.RecordsUpdated < 1 {
		t.Fatalf("fixture did not touch the existing interval: %+v", first)
	}
	afterFirst := snapshot()
	for id, before := range initial {
		if afterFirst[id] != before {
			t.Fatalf("protected identity fields changed by backfill: before=%+v after=%+v", before, afterFirst[id])
		}
	}
	// The daily incremental window overlaps already covered time.
	second := runBackfill(start.Add(6*24*time.Hour), start.Add(8*24*time.Hour))
	if second.RecordsUpdated < len(afterFirst) {
		t.Fatalf("incremental run did not update existing intervals: %+v", second)
	}
	afterSecond := snapshot()
	if len(afterSecond) != len(afterFirst) {
		t.Fatalf("incremental run changed the interval set: %d -> %d", len(afterFirst), len(afterSecond))
	}
	for id, before := range afterFirst {
		if afterSecond[id] != before {
			t.Fatalf("protected identity fields changed by incremental backfill: before=%+v after=%+v", before, afterSecond[id])
		}
	}
	// Derived fields were refreshed (the known, approved impact).
	var refreshed api.HistoricalGPUIdentityInterval
	if err := db.Where("interval_key = ?", key).First(&refreshed).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.BackfillRunID != second.ID || !refreshed.LastSeenAt.After(seeded.LastSeenAt) || refreshed.HostSerial != "SN-PRESEEDED" || refreshed.HostID != "HOST-PRESEEDED" {
		t.Fatalf("unexpected refreshed interval: %+v", refreshed)
	}
}

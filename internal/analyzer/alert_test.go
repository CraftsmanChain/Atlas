package analyzer

import (
	"testing"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func TestProcessTreatsSameAlertAsIndependentEvents(t *testing.T) {
	db, err := storage.InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("InitDB returned error: %v", err)
	}

	analyzer := NewAlertAnalyzer(db, nil)

	first := &api.AlertEvent{
		Source:  "alertmanager",
		Host:    "4090gpu-14",
		Level:   "warning",
		Message: "XID故障-高优先级",
		Labels: api.StringMap{
			"gpu":      "4",
			"err_code": "79",
		},
	}
	second := &api.AlertEvent{
		Source:  "alertmanager",
		Host:    "4090gpu-14",
		Level:   "warning",
		Message: "XID故障-高优先级",
		Labels: api.StringMap{
			"gpu":      "6",
			"err_code": "79",
		},
	}

	if err := analyzer.Process(first); err != nil {
		t.Fatalf("Process(first) returned error: %v", err)
	}
	if err := analyzer.Process(second); err != nil {
		t.Fatalf("Process(second) returned error: %v", err)
	}

	if first.ID == second.ID {
		t.Fatalf("expected independent event IDs, got same ID %q", first.ID)
	}
	if first.RepeatCount != 1 || second.RepeatCount != 1 {
		t.Fatalf("expected repeat_count to remain 1, got first=%d second=%d", first.RepeatCount, second.RepeatCount)
	}

	var count int64
	if err := db.Model(&api.AlertEvent{}).Count(&count).Error; err != nil {
		t.Fatalf("Count returned error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 alert rows, got %d", count)
	}
}

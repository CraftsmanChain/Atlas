package history

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	promclient "atlas/internal/prometheus"
	"atlas/pkg/api"
)

func TestReplayOneRowComparesSharedStatistics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{"__name__":"DCGM_FI_DEV_GPU_TEMP","UUID":"GPU-1"},"values":[[0,"10"],[3600,"12"]]}]}}`)
	}))
	defer server.Close()
	client, err := promclient.NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	columns := []string{"gpu_temp_mean_24h", "gpu_temp_slope_per_hour_24h"}
	results := map[string]*replayColumnResult{}
	for _, column := range columns {
		results[column] = &replayColumnResult{}
	}
	row := trainingMatrixRow{
		RowKey: "row-1", GPUUUID: "GPU-1", Split: "test", LabelValue: 1,
		FeatureCutoffAt: time.Unix(3600, 0),
		Features:        map[string]float64{"gpu_temp_mean_24h": 11, "gpu_temp_slope_per_hour_24h": 2},
	}
	service := &Service{timeout: time.Second}
	result := service.replayOneRow(client, row, columns, results)
	if result.Status != "completed" || result.Compared != 2 || result.Mismatched != 0 || result.MissingReplay != 0 {
		t.Fatalf("unexpected replay result: %+v columns=%+v", result, results)
	}
	row.Features["gpu_temp_mean_24h"] = 20
	results["gpu_temp_mean_24h"] = &replayColumnResult{}
	results["gpu_temp_slope_per_hour_24h"] = &replayColumnResult{}
	result = service.replayOneRow(client, row, columns, results)
	if result.Mismatched != 1 || results["gpu_temp_mean_24h"].MaximumAbsoluteError != 9 {
		t.Fatalf("feature drift was not detected: %+v columns=%+v", result, results)
	}
}

func TestSelectReplayRowsBalancesSplitsAndLabels(t *testing.T) {
	spec := api.PredictionModelSpec{HorizonMinutes: 10080, ScopeModelName: "H100", ScopeEventType: "xid_94"}
	rows := make([]trainingMatrixRow, 0, 12)
	for _, split := range []string{"train", "validation", "test"} {
		for label := 0; label <= 1; label++ {
			for sample := 0; sample < 2; sample++ {
				rows = append(rows, trainingMatrixRow{
					RowKey: fmt.Sprintf("%s-%d-%d", split, label, sample), Split: split, LabelValue: label,
					HorizonMinutes: 10080, ModelName: "H100",
					LabelMetadata: trainingLabelMetadata{EventTypes: []string{"xid_94"}},
					Features:      map[string]float64{"gpu_temp_mean_24h": float64(sample)},
				})
			}
		}
	}
	selected := selectReplayRows(rows, spec, []string{"gpu_temp_mean_24h"}, 6)
	if len(selected) != 6 {
		t.Fatalf("selected %d rows", len(selected))
	}
	seen := map[string]bool{}
	for _, row := range selected {
		seen[row.Split+fmt.Sprintf(":%d", row.LabelValue)] = true
	}
	if len(seen) != 6 {
		t.Fatalf("replay sample lost split/label balance: %+v", seen)
	}
}

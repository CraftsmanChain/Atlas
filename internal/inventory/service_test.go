package inventory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atlas/internal/platformconfig"
	"atlas/internal/prometheus"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
)

type fakeAssetSource struct {
	catalog map[string]string
}

func (f *fakeAssetSource) Sync(context.Context) (*platformconfig.AssetSyncResult, error) {
	return &platformconfig.AssetSyncResult{Configured: true, GPUCatalog: f.catalog}, nil
}

func (f *fakeAssetSource) LastGPUCatalog() (map[string]string, error) {
	return f.catalog, nil
}

type fakePrometheus struct {
	targets          []prometheus.Target
	current          []prometheus.Sample
	history          []prometheus.Sample
	lastHistoryQuery string
	queryCalls       int
}

func (f *fakePrometheus) BaseURL() string { return "http://prometheus.test" }
func (f *fakePrometheus) ActiveTargets(context.Context) ([]prometheus.Target, error) {
	return f.targets, nil
}
func (f *fakePrometheus) Query(_ context.Context, query string) ([]prometheus.Sample, error) {
	f.queryCalls++
	if strings.HasPrefix(query, "last_over_time") {
		f.lastHistoryQuery = query
		return f.history, nil
	}
	return f.current, nil
}

func TestSyncBuildsSlotsRecoversHistoryAndTracksReplacement(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakePrometheus{targets: []prometheus.Target{
		{Labels: map[string]string{"job": "ipmi_exporter", "instance": "10.114.1.21:9290"}, Health: "up"},
		{Labels: map[string]string{"job": "ipmi_exporter", "instance": "10.114.1.22:9290"}, Health: "up"},
		{Labels: map[string]string{"job": "node_exporter", "instance": "10.114.4.21:9100"}, Health: "down"},
	}}
	reader.history = []prometheus.Sample{{Metric: map[string]string{"instance": "10.114.4.21:9400", "Hostname": "gpu-21", "gpu": "0", "UUID": "GPU-OLD", "modelName": "RTX 4090"}}}
	assetFile := filepath.Join(t.TempDir(), "assets.csv")
	if err := os.WriteFile(assetFile, []byte("node_ip,name\n10.114.4.21,gpu-21\n10.114.4.22,cpu-22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.InventoryConfig{
		ExpectedGPUCount: 8, NodePrefix: "10.114.4.", BMCPrefix: "10.114.1.", BMCLastOctetMin: 20,
		AssetFile: assetFile, HistoryWindow: "365d", TargetJobs: []string{"dcgm_exporter", "gpu_exporter", "node_exporter", "ipmi_exporter"},
	}
	service := NewService(db, reader, cfg)
	clock := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }

	run, err := service.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.NodeCount != 1 || run.GPUCount != 8 || run.KnownUUIDCount != 1 {
		t.Fatalf("unexpected run: %+v", run)
	}
	if run.TaskType != TaskFullReconcile {
		t.Fatalf("unexpected task type: %s", run.TaskType)
	}
	if !strings.Contains(reader.lastHistoryQuery, `10\\.114\\.4\\.21`) {
		t.Fatalf("PromQL regex was not double escaped: %s", reader.lastHistoryQuery)
	}
	var cpuNodes int64
	db.Model(&api.GPUNode{}).Where("node_ip = ?", "10.114.4.22").Count(&cpuNodes)
	if cpuNodes != 0 {
		t.Fatal("CPU hostname entered GPU inventory")
	}
	var node api.GPUNode
	if err := db.Where("node_ip = ?", "10.114.4.21").First(&node).Error; err != nil {
		t.Fatal(err)
	}
	if node.State != "degraded" || node.ObservedGPUCount != 1 {
		t.Fatalf("unexpected node: %+v", node)
	}
	var assets []api.GPUAsset
	db.Where("node_ip = ?", node.NodeIP).Order("gpu_index").Find(&assets)
	if len(assets) != 8 || assets[0].CurrentUUID != "GPU-OLD" || assets[0].State != "history_only" || assets[1].State != "uuid_unknown" {
		t.Fatalf("unexpected assets: %+v", assets)
	}
	var targets int64
	db.Model(&api.CollectorTarget{}).Where("node_ip = ?", node.NodeIP).Count(&targets)
	if targets != 4 {
		t.Fatalf("expected 4 target matrix rows, got %d", targets)
	}

	clock = clock.Add(5 * time.Minute)
	reader.targets = append(reader.targets,
		prometheus.Target{Labels: map[string]string{"job": "node_exporter", "instance": "10.114.4.21:9100"}, Health: "up"},
		prometheus.Target{Labels: map[string]string{"job": "dcgm_exporter", "instance": "10.114.4.21:9400"}, Health: "up"},
	)
	reader.current = []prometheus.Sample{{Metric: map[string]string{"instance": "10.114.4.21:9400", "Hostname": "gpu-21", "gpu": "0", "UUID": "GPU-NEW", "modelName": "RTX 4090"}}}
	run, err = service.SyncIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.TaskType != TaskIdentityIncremental {
		t.Fatalf("recovery used wrong task type: %+v", run)
	}
	if run.ChangeCount == 0 {
		t.Fatalf("expected reconciliation changes, run=%+v", run)
	}
	var changes []api.AssetChangeEvent
	db.Where("event_type = ?", "gpu_uuid_changed").Find(&changes)
	if len(changes) != 1 || changes[0].OldValue != "GPU-OLD" || changes[0].NewValue != "GPU-NEW" || changes[0].SyncRunID != run.ID {
		t.Fatalf("unexpected replacement changes: %+v", changes)
	}

	queryCalls := reader.queryCalls
	reader.targets = append(reader.targets, prometheus.Target{Labels: map[string]string{"job": "dcgm_exporter", "instance": "10.114.4.21:9400"}, Health: "down"})
	clock = clock.Add(10 * time.Minute)
	targetRun, err := service.SyncTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if targetRun.TaskType != TaskTargetStatus || reader.queryCalls != queryCalls {
		t.Fatalf("target task queried GPU metrics or used wrong type: run=%+v query_calls=%d->%d", targetRun, queryCalls, reader.queryCalls)
	}
	var targetChanges []api.AssetChangeEvent
	db.Where("sync_run_id = ? AND event_type = ?", targetRun.ID, "target_state_changed").Find(&targetChanges)
	if len(targetChanges) != 1 || targetChanges[0].AssetKey != "dcgm_exporter|10.114.4.21" || targetChanges[0].NewValue != "down" {
		t.Fatalf("unexpected target changes: %+v", targetChanges)
	}
}

func TestIdentitySyncImmediatelyRetiresNodesRemovedFromAuthoritativeCatalog(t *testing.T) {
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakePrometheus{}
	source := &fakeAssetSource{catalog: map[string]string{
		"10.114.4.21": "H100gpu-21",
		"10.114.4.23": "H100gpu-23",
	}}
	cfg := config.InventoryConfig{
		ExpectedGPUCount: 1,
		TargetJobs:       []string{"dcgm_exporter"},
	}
	service := NewServiceWithAssets(db, reader, cfg, source)
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	if _, err := service.SyncFull(context.Background()); err != nil {
		t.Fatal(err)
	}
	var retiredAsset api.GPUAsset
	if err := db.Where("node_ip = ?", "10.114.4.23").First(&retiredAsset).Error; err != nil {
		t.Fatal(err)
	}
	score := api.GPUHealthScore{
		GPUAssetID:  retiredAsset.ID,
		NodeIP:      retiredAsset.NodeIP,
		Level:       "unknown",
		Current:     true,
		EvaluatedAt: now,
	}
	if err := db.Create(&score).Error; err != nil {
		t.Fatal(err)
	}

	source.catalog = map[string]string{"10.114.4.21": "H100gpu-21"}
	now = now.Add(10 * time.Minute)
	run, err := service.SyncIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var removed api.GPUNode
	if err := db.Where("node_ip = ?", "10.114.4.23").First(&removed).Error; err != nil {
		t.Fatal(err)
	}
	if removed.Lifecycle != "retired" || run.ChangeCount == 0 {
		t.Fatalf("authoritative removal was not applied in identity sync: node=%+v run=%+v", removed, run)
	}
	if err := db.First(&score, score.ID).Error; err != nil {
		t.Fatal(err)
	}
	if score.Current {
		t.Fatal("retired node retained a current health score")
	}
}

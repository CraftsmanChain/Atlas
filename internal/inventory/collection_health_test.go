package inventory

import (
	"atlas/internal/issues"
	"atlas/internal/prometheus"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
	"context"
	"testing"
	"time"
)

func TestCollectionHealthSignals(t *testing.T) {
	now := time.Now()
	sample := func(name string, value float64) prometheus.Sample {
		return prometheus.Sample{Metric: map[string]string{"job": "gpu_exporter", "instance": "10.1.1.1:9835", "__name__": name}, Value: value, Timestamp: now}
	}
	success := sample("nvidia_smi_last_collect_success", 1)
	exit := sample("nvidia_smi_command_exit_code", 0)
	business := sample("nvidia_smi_utilization_gpu_ratio", 0)
	for _, test := range []struct {
		name string
		rows []prometheus.Sample
		want string
	}{{"healthy_idle", []prometheus.Sample{success, exit, business}, "healthy"}, {"empty_gpu_business", []prometheus.Sample{success, exit}, "gpu_business_metrics_missing"}, {"failure", []prometheus.Sample{sample("nvidia_smi_last_collect_success", 0), sample("nvidia_smi_command_exit_code", 255)}, "gpu_collection_failed"}, {"unsupported", []prometheus.Sample{business}, ""}, {"conflicting", []prometheus.Sample{success, sample("nvidia_smi_last_collect_success", 0)}, "gpu_collection_unverified"}} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyCollections(test.rows, now)["10.1.1.1:9835"]; got != test.want {
				t.Fatal(got)
			}
		})
	}
	old := success
	old.Timestamp = now.Add(-6 * time.Minute)
	future := exit
	future.Timestamp = now.Add(2 * time.Minute)
	if len(classifyCollections([]prometheus.Sample{old, future}, now)) != 0 {
		t.Fatal("stale/future evidence used")
	}
}
func TestCollectionIssueReusesTargetAndRequiresPositiveRecovery(t *testing.T) {
	db, e := storage.InitDB(t.TempDir() + "/a.db")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	node := api.GPUNode{NodeIP: "10.1.1.1", Lifecycle: "active", State: "up"}
	if e = db.Create(&node).Error; e != nil {
		t.Fatal(e)
	}
	cfg := config.InventoryConfig{TargetJobs: []string{"gpu_exporter", "node_exporter"}}
	s := NewService(db, &fakePrometheus{}, cfg)
	targets := map[string]prometheus.Target{targetKey("gpu_exporter", node.NodeIP): {Health: "up", Labels: map[string]string{"instance": "10.1.1.1:9835"}}, targetKey("node_exporter", node.NodeIP): {Health: "up"}}
	for _, phase := range []struct {
		diagnostics map[string]string
		reason      string
		active      int64
	}{{map[string]string{"10.1.1.1:9835": "gpu_collection_failed"}, "gpu_collection_failed", 1}, {nil, "gpu_collection_unverified", 1}, {map[string]string{"10.1.1.1:9835": "healthy"}, "", 0}} {
		if _, e = s.persistTargets(now, 0, []string{node.NodeIP}, targets, phase.diagnostics); e != nil {
			t.Fatal(e)
		}
		var target api.CollectorTarget
		if e = db.Where("target_key = ?", targetKey("gpu_exporter", node.NodeIP)).First(&target).Error; e != nil {
			t.Fatal(e)
		}
		if target.Health != "up" || target.ReasonCode != phase.reason {
			t.Fatal(target)
		}
		service := issues.NewService(db)
		if e = service.SyncDetectedIssues(); e != nil {
			t.Fatal(e)
		}
		if e = service.SyncDetectedIssues(); e != nil {
			t.Fatal(e)
		}
		var count int64
		db.Model(&api.PlatformIssue{}).Where("detection_source = ? AND detection_state = ?", "collector_target", "active").Count(&count)
		if count != phase.active {
			t.Fatal(count)
		}
		db.Model(&api.GPUFaultEvent{}).Count(&count)
		if count != 0 {
			t.Fatal("collector failure became hardware fault")
		}
	}
}

type collectionReader struct {
	fakePrometheus
	values, stamps []prometheus.Sample
}

func (f *collectionReader) Query(_ context.Context, q string) ([]prometheus.Sample, error) {
	if q == collectionHealthQuery {
		return f.values, nil
	}
	return f.stamps, nil
}
func TestCollectionUsesSourceTimestampNotEvaluationTime(t *testing.T) {
	now := time.Now()
	r := &collectionReader{}
	for _, x := range []struct {
		name  string
		value float64
	}{{"nvidia_smi_last_collect_success", 1}, {"nvidia_smi_command_exit_code", 0}, {"nvidia_smi_utilization_gpu_ratio", 0}} {
		r.values = append(r.values, prometheus.Sample{Metric: map[string]string{"job": "gpu_exporter", "instance": "a:9835", "__name__": x.name}, Value: x.value, Timestamp: now})
		r.stamps = append(r.stamps, prometheus.Sample{Metric: map[string]string{"job": "gpu_exporter", "instance": "a:9835", "atlas_source_metric": x.name}, Value: float64(now.Add(-6 * time.Minute).Unix()), Timestamp: now})
	}
	s := &Service{prom: r}
	if len(s.collectionHealth(context.Background(), now)) != 0 {
		t.Fatal("old source sample treated as fresh")
	}
	for i := range r.stamps {
		r.stamps[i].Value = float64(now.Unix())
	}
	if s.collectionHealth(context.Background(), now)["a:9835"] != "healthy" {
		t.Fatal("fresh zero utilization is valid business sample")
	}
}

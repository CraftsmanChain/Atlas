package gpurisk

import (
	"atlas/pkg/api"
	"gorm.io/gorm"
	"testing"
	"time"
)

func contextFixture(t *testing.T) (*gorm.DB, api.GPURiskQuery, time.Time) {
	t.Helper()
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPUNode{}, &api.GPUAsset{}, &api.InfrastructureAsset{}); e != nil {
		t.Fatal(e)
	}
	p := fixture(t, 1)
	p.Nodes[0].Cards = []Card{{AssetID: 1, UUID: "GPU-context", State: "active", AvailableAt: p.Cutoff.Add(-time.Hour)}}
	p.Nodes[0].SourceAssetIDs = []uint{1}
	p = reseal(t, p)
	at := time.Now().UTC().Add(-time.Second)
	old := p.Cutoff.Add(-time.Hour)
	if e := db.Create(&api.GPUNode{ID: 1, NodeIP: "node-1", AssetSerial: "sn-1", Lifecycle: "active", CreatedAt: old, UpdatedAt: old}).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&api.GPUAsset{ID: 1, AssetKey: "card", NodeID: 1, CurrentUUID: "GPU-context", CurrentNodeIdentity: true, HostSerial: "sn-1", State: "active", ModelName: "Real Model", CreatedAt: old, UpdatedAt: old}).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&api.InfrastructureAsset{ID: 1, AssetKey: "source", SerialNumber: "sn-1", Present: true, InUse: true, DataCenterID: "dc-source-id", CreatedAt: old, UpdatedAt: old}).Error; e != nil {
		t.Fatal(e)
	}
	q, e := Record(t.Context(), db, p)
	if e != nil {
		t.Fatal(e)
	}
	return db, q, at.Add(2 * time.Second)
}
func TestContextPrepareCandidatesIdentityAndTime(t *testing.T) {
	for _, kind := range []string{"valid", "generation", "serial", "uuid", "asset-serial", "future", "mixed"} {
		t.Run(kind, func(t *testing.T) {
			db, q, at := contextFixture(t)
			at = time.Now().UTC()
			switch kind {
			case "generation":
				if e := db.Model(&api.GPUNode{}).Where("id=1").Update("identity_generation", 1).Error; e != nil {
					t.Fatal(e)
				}
			case "serial":
				if e := db.Model(&api.GPUNode{}).Where("id=1").Update("asset_serial", "changed").Error; e != nil {
					t.Fatal(e)
				}
			case "uuid":
				if e := db.Model(&api.GPUAsset{}).Where("id=1").Update("current_uuid", "GPU-other").Error; e != nil {
					t.Fatal(e)
				}
			case "asset-serial":
				if e := db.Model(&api.InfrastructureAsset{}).Where("id=1").Update("serial_number", "changed").Error; e != nil {
					t.Fatal(e)
				}
			case "future":
				if e := db.Model(&api.GPUAsset{}).Where("id=1").Update("updated_at", at.Add(time.Hour)).Error; e != nil {
					t.Fatal(e)
				}
			case "mixed":
				if e := db.Create(&api.GPUAsset{ID: 2, AssetKey: "second", GPUIndex: 1, NodeID: 1, CurrentUUID: "GPU-extra", HostSerial: "sn-1", CurrentNodeIdentity: true, State: "active", ModelName: "Other Model", CreatedAt: q.Cutoff, UpdatedAt: q.Cutoff}).Error; e != nil {
					t.Fatal(e)
				}
			}
			if kind != "future" {
				at = time.Now().UTC()
			}
			p, e := BuildContextPreparation(t.Context(), db, q.ID, at)
			if e != nil {
				t.Fatal(e)
			}
			r := p.Members[0]
			if kind == "valid" {
				if r.Candidates["gpu_model"] == nil || r.Candidates["data_center"] == nil || r.Candidates["host_type"] != nil || r.ReviewTemplate["owner"] != "" {
					t.Fatal(r)
				}
			}
			if kind == "generation" || kind == "serial" {
				if r.Candidates["gpu_model"] != nil || r.Candidates["data_center"] != nil {
					t.Fatal(r)
				}
			}
			if kind == "uuid" || kind == "mixed" || kind == "future" {
				if r.Candidates["gpu_model"] != nil || r.Candidates["data_center"] == nil {
					t.Fatal(r)
				}
			}
			if kind == "asset-serial" && r.Candidates["data_center"] != nil {
				t.Fatal(r)
			}
			again, e := BuildContextPreparation(t.Context(), db, q.ID, at)
			if e != nil || p.SHA != again.SHA {
				t.Fatal(e, "unstable")
			}
		})
	}
}
func TestContextPrepareMissingAndFingerprint(t *testing.T) {
	db, q, _ := contextFixture(t)
	at := time.Now().UTC()
	p, e := BuildContextPreparation(t.Context(), db, q.ID, at)
	if e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&api.GPUAsset{}).Where("id=1").Updates(map[string]any{"model_name": "unknown", "model": ""}).Error; e != nil {
		t.Fatal(e)
	}
	next, e := BuildContextPreparation(t.Context(), db, q.ID, time.Now())
	if e != nil || next.Members[0].Candidates["gpu_model"] != nil || next.InputSHA == p.InputSHA {
		t.Fatal(e, next)
	}
	for _, bad := range []time.Time{time.Time{}, q.Cutoff, time.Now().Add(time.Hour)} {
		if _, e := BuildContextPreparation(t.Context(), db, q.ID, bad); e == nil {
			t.Fatal("accepted time", bad)
		}
	}
}

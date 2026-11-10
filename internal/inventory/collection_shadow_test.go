package inventory

import (
	"atlas/internal/prometheus"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestProductionCollectionShadowFixture(t *testing.T) {
	path := os.Getenv("ATLAS_COLLECTION_SHADOW_FIXTURE")
	if path == "" {
		t.Skip("optional authorized read-only production projection")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var p struct {
		Observed string `json:"observed_at"`
		Queries  []struct {
			Rows []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"rows"`
		} `json:"queries"`
	}
	if e = json.Unmarshal(b, &p); e != nil {
		t.Fatal(e)
	}
	now, e := time.Parse(time.RFC3339Nano, p.Observed)
	if e != nil {
		t.Fatal(e)
	}
	r := &collectionReader{}
	for qi, q := range p.Queries {
		for _, v := range q.Rows {
			num, e := strconv.ParseFloat(v.Value[1].(string), 64)
			if e != nil {
				t.Fatal(e)
			}
			x := prometheus.Sample{Metric: v.Metric, Value: num, Timestamp: time.Unix(int64(v.Value[0].(float64)), 0)}
			if qi == 0 {
				r.values = append(r.values, x)
			} else {
				r.stamps = append(r.stamps, x)
			}
		}
	}
	got := (&Service{prom: r}).collectionHealth(context.Background(), now)
	if got["10.80.174.216:9835"] != "gpu_collection_failed" || got["10.80.174.172:9835"] != "healthy" {
		t.Fatal(got)
	}
	t.Log("216=collection_failed, 172=healthy collection; read-only projection, no DB writes")
}

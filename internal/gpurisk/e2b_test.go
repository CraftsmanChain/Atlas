package gpurisk

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
)

func TestE2BHolm(t *testing.T) {
	got := holm([]float64{0.01, 0.04, 0.03})
	want := []float64{0.03, 0.06, 0.06}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatal(got)
		}
	}
}

// Under a true null the Monte Carlo p must be (approximately) uniform: the
// rejection rate at 0.05 may not exceed it beyond sampling error.
func TestE2BPermutationCalibratedAndPowerful(t *testing.T) {
	old := e2bDraws
	e2bDraws = 2000
	defer func() { e2bDraws = old }()
	r := rand.New(rand.NewPCG(1, 2))
	mk := func(alt bool) []e2bScored {
		sc := make([]e2bScored, 20)
		for i := range sc {
			n := 80 + r.IntN(40)
			sc[i].null = make([]float64, n)
			for j := range sc[i].null {
				sc[i].null[j] = float64(j) / float64(n-1)
			}
			if alt {
				sc[i].x = sc[i].null[n-1-r.IntN(n/10)] // top 10%
			} else {
				sc[i].x = sc[i].null[r.IntN(n)]
			}
		}
		return sc
	}
	rej := 0
	const reps = 400
	for k := 0; k < reps; k++ {
		if e2bPermP(mk(false), uint64(k+10)) < 0.05 {
			rej++
		}
	}
	if rate := float64(rej) / reps; rate > 0.085 {
		t.Fatalf("null rejection rate %.3f > 0.085", rate)
	}
	if p := e2bPermP(mk(true), 3); p > 0.001 {
		t.Fatalf("top-10%% episodes not detected, p=%.4f", p)
	}
	if p := e2bSignFlipP([]float64{0.2, 0.1, 0.3, 0.25, 0.15, 0.2, 0.1, 0.3, 0.2, 0.1}, 4); p > 0.01 {
		t.Fatal("sign flip", p)
	}
	if p := e2bSignFlipP([]float64{0, 0, 0}, 5); p < 0.99 {
		t.Fatal("zero diffs must not be significant", p)
	}
}

// End to end through the database: daily prospective batches for the three
// family routes, five persistently top-scored nodes that fail every 5 days,
// one failure on an unranked node and one before the episode window.
func TestBuildE2BLooksAndDecision(t *testing.T) {
	old := e2bDraws
	e2bDraws = 5000
	defer func() { e2bDraws = old }()
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPUCoreEvent{}, &api.GPURiskShadowBatch{}, &api.GPURiskShadowScore{}, &api.GPURiskMember{}); e != nil {
		t.Fatal(e)
	}
	const nodes = 40
	base := time.Date(2026, 11, 16, 2, 0, 34, 0, shanghai)
	bid := uint(0)
	for d := 0; d < 30; d++ {
		qid := uint(d + 1)
		cut := base.Add(time.Duration(d) * 24 * time.Hour)
		for n := 1; n <= nodes; n++ {
			if e := db.Create(&api.GPURiskMember{QueryID: qid, NodeID: uint(n), Generation: 1, NodeIP: fmt.Sprintf("10.0.0.%d", n), Status: "included"}).Error; e != nil {
				t.Fatal(e)
			}
		}
		for si, st := range E2BFamily {
			bid++
			if e := db.Create(&api.GPURiskShadowBatch{ID: bid, QueryID: qid, Strategy: st, Cutoff: cut.UTC(), ComputedAt: cut.UTC(), Timing: "prospective", InputSHA: "x", SHA: fmt.Sprintf("%064d", bid)}).Error; e != nil {
				t.Fatal(e)
			}
			for n := 1; n <= nodes; n++ {
				var s float64
				switch si {
				case 0, 1: // R1, R4: hot nodes 1..5 on top every day
					if n <= 5 {
						s = 10
					}
					if si == 1 {
						s += 0.1
					}
				default: // precursor: unrelated to failures
					s = float64((n*7 + d) % 11)
				}
				v := s
				if e := db.Create(&api.GPURiskShadowScore{BatchID: bid, NodeID: uint(n), Generation: 1, RawScore: &v}).Error; e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	ev := 0
	add := func(ip string, at time.Time) {
		ev++
		if e := db.Create(&api.GPUCoreEvent{Key: fmt.Sprint("k", ev), Source: CoreEventSourceVM, NodeIP: ip, Code: "xid79", Day: at.In(shanghai).Format("2006-01-02"), ObservedAt: at.UTC(), ImportedAt: at.Add(time.Hour).UTC()}).Error; e != nil {
			t.Fatal(e)
		}
	}
	for h := 1; h <= 5; h++ {
		for k := 0; k < 5; k++ {
			add(fmt.Sprintf("10.0.0.%d", h), time.Date(2026, 11, 17, 10+h, 0, 0, 0, shanghai).Add(time.Duration(k)*5*24*time.Hour))
		}
	}
	add("10.9.9.9", time.Date(2026, 11, 18, 12, 0, 0, 0, shanghai))  // never ranked
	add("10.0.0.20", time.Date(2026, 11, 15, 12, 0, 0, 0, shanghai)) // before the window

	// 11-23 readout: only k=0 episodes are older than 48h -> 5 ranked, accumulating.
	r, err := BuildE2B(context.Background(), db, time.Date(2026, 11, 23, 9, 30, 0, 0, shanghai))
	if err != nil || r.State != "accumulating" || r.N != 5 || r.NoRanking != 1 || len(r.Looks) != 0 {
		t.Fatalf("11-23: %+v %v", r, err)
	}
	// 11-30 readout: k=0..2 -> 15 ranked >= 12: interim look; hot nodes rank top -> pass, ends test.
	r, err = BuildE2B(context.Background(), db, time.Date(2026, 12, 20, 0, 0, 0, 0, shanghai))
	if err != nil || len(r.Looks) != 1 {
		t.Fatalf("looks: %+v %v", r.Looks, err)
	}
	l := r.Looks[0]
	if l.Kind != "interim" || l.N != 15 || !l.AsOf.Equal(time.Date(2026, 11, 30, 9, 0, 0, 0, shanghai)) || l.Decision != "pass" || r.State != "final_done" {
		t.Fatalf("look %+v state %s", l, r.State)
	}
	if !l.Routes[0].Pass || !l.Routes[1].Pass || l.Routes[2].Pass {
		t.Fatalf("route passes %+v", l.Routes)
	}
	// R4 ranks exactly like R1 -> paired difference 0 -> parsimony keeps R1.
	if l.Winner != CoreRecurrenceStrategy || math.Abs(l.PairedD) > 1e-12 {
		t.Fatalf("winner %s d=%v", l.Winner, l.PairedD)
	}
	if l.Routes[0].MeanPct < 0.9 || l.Routes[2].MeanPct > 0.75 {
		t.Fatalf("pct %+v", l.Routes)
	}
	md := RenderE2BMarkdown(r)
	if !strings.Contains(md, "首读") || !strings.Contains(md, CoreRecurrenceStrategy) {
		t.Fatal(md)
	}
	// Empty database: well-formed accumulating report.
	empty := testDB(t)
	r, err = BuildE2B(context.Background(), empty, time.Now())
	if err != nil || r.State != "accumulating" || r.N != 0 {
		t.Fatal(r, err)
	}
}

// Parsimony: R4 replaces a passing R1 only when it is better on the same
// episodes with paired p < 0.05.
func TestE2BParsimonyRule(t *testing.T) {
	old := e2bDraws
	e2bDraws = 5000
	defer func() { e2bDraws = old }()
	base := time.Date(2026, 11, 16, 2, 0, 34, 0, shanghai)
	// decoy[st][day] = true puts node 36 above the hot nodes in that route's batch.
	build := func(decoy map[string]map[int]bool) map[string][]e2bBatch {
		routes := map[string][]e2bBatch{}
		for si, st := range E2BFamily {
			for d := 0; d < 30; d++ {
				sc := map[string]float64{}
				for n := 1; n <= 40; n++ {
					s := 0.0
					if si < 2 && n <= 5 {
						s = 10
					}
					if si == 2 {
						s = float64((n*7 + d) % 11)
					}
					if n == 36 && decoy[st][d] {
						s = 20
					}
					sc[fmt.Sprintf("10.0.0.%d", n)] = s
				}
				b := e2bBatchStats(sc)
				b.cutoff = base.Add(time.Duration(d) * 24 * time.Hour).UTC()
				routes[st] = append(routes[st], b)
			}
		}
		return routes
	}
	var eps []E2BEpisode
	for h := 1; h <= 5; h++ {
		for k := 0; k < 3; k++ {
			eps = append(eps, E2BEpisode{NodeIP: fmt.Sprintf("10.0.0.%d", h), Start: time.Date(2026, 11, 17, 10+h, 0, 0, 0, shanghai).Add(time.Duration(k) * 5 * 24 * time.Hour).UTC(), FirstTime: k == 0})
		}
	}
	all := map[int]bool{}
	for d := 0; d < 30; d++ {
		all[d] = true
	}
	at := time.Date(2026, 11, 30, 9, 0, 0, 0, shanghai)
	// R1 always has the decoy, R4 never: R4 better on every episode -> R4 wins.
	l := e2bLook("final", at, append([]E2BEpisode(nil), eps...), build(map[string]map[int]bool{CoreRecurrenceStrategy: all}))
	if l.Decision != "pass" || l.Winner != CoreCohortStrategy || l.PairedD <= 0 || l.PairedP >= 0.05 {
		t.Fatalf("consistent R4 gain: %+v", l)
	}
	// R1 decoy on one day, R4 decoy on another: R4 slightly higher on average but
	// not significantly -> parsimony keeps R1.
	l = e2bLook("final", at, append([]E2BEpisode(nil), eps...), build(map[string]map[int]bool{CoreRecurrenceStrategy: {2: true, 3: true}, CoreCohortStrategy: {9: true}}))
	if !(l.Routes[1].MeanPct > l.Routes[0].MeanPct) || l.PairedP < 0.05 {
		t.Fatalf("fixture precondition: R1 %.4f R4 %.4f paired p %.4f", l.Routes[0].MeanPct, l.Routes[1].MeanPct, l.PairedP)
	}
	if l.Decision != "pass" || l.Winner != CoreRecurrenceStrategy {
		t.Fatalf("parsimony: %+v", l)
	}
}

package gpurisk

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
)

func TestPoissonBinomialUpper(t *testing.T) {
	cases := []struct {
		p    []float64
		x    int
		want float64
	}{
		{[]float64{.5, .5}, 0, 1}, {[]float64{.5, .5}, 1, .75}, {[]float64{.5, .5}, 2, .25}, {[]float64{.5, .5}, 3, 0},
		{[]float64{.05, .05, .05}, 1, 1 - math.Pow(.95, 3)},
	}
	for _, c := range cases {
		if got := PoissonBinomialUpper(c.p, c.x); math.Abs(got-c.want) > 1e-12 {
			t.Fatal(c, got)
		}
	}
	// 4 of 12 at p=0.05 must match the binomial tail used in the route review.
	p := make([]float64, 12)
	for i := range p {
		p[i] = .05
	}
	if got := PoissonBinomialUpper(p, 4); got > .01 || got < .001 {
		t.Fatal(got)
	}
}

func TestE2EvaluateTiesAndHash(t *testing.T) {
	rows := []e2Ranked{{1, 0, 3, "b"}, {2, 0, 3, "a"}, {3, 0, 1, "c"}}
	for i := 4; i <= 40; i++ {
		rows = append(rows, e2Ranked{uint(i), 0, 0, "z"})
	}
	// N=40, K=2: nodes 1 and 2 are both strictly in.
	if m := e2Evaluate(append([]e2Ranked{}, rows...), 1, 0); !m.Hit || m.K != 2 || m.Expected != 1 {
		t.Fatal(m)
	}
	// Three-way tie at the top with K=2: hash decides, expected = 2/3.
	rows[2].Score = 3
	m := e2Evaluate(append([]e2Ranked{}, rows...), 1, 0) // tie key "b" is second after "a"
	if !m.Hit || m.Rank != 2 || math.Abs(m.Expected-2.0/3) > 1e-12 {
		t.Fatal(m)
	}
	m = e2Evaluate(append([]e2Ranked{}, rows...), 3, 0) // tie key "c" is third
	if m.Hit || m.Rank != 3 || math.Abs(m.Expected-2.0/3) > 1e-12 {
		t.Fatal(m)
	}
	if m := e2Evaluate(append([]e2Ranked{}, rows...), 99, 0); m.Scored || m.Hit {
		t.Fatal(m)
	}
}

func TestBuildE2ReportAttributionAndStatuses(t *testing.T) {
	db, q := shadowDB(t)
	old := E2PrimeStart
	E2PrimeStart = q.Cutoff.Add(-time.Hour)
	defer func() { E2PrimeStart = old }()
	if _, e := RecordShadow(context.Background(), db, q.ID, time.Now()); e != nil {
		t.Fatal(e)
	}
	// Evaluated: confirmed node-scope incident on member node-1 inside (cutoff, cutoff+7d].
	feedbackOn(t, db, q, "node-1", "node", "confirmed_hardware", 24*time.Hour)
	// Merged into the same episode (within 72h).
	feedbackOn(t, db, q, "node-1", "node", "confirmed_hardware", 48*time.Hour)
	// Out of scope: node not a member of any prospective query.
	feedbackOn(t, db, q, "node-9", "node", "confirmed_hardware", 24*time.Hour)
	// Unreviewed within the pre-registration period is listed for follow-up.
	open := feedbackOn(t, db, q, "node-2", "gpu", "", 24*time.Hour)
	// Excluded is ignored.
	feedbackOn(t, db, q, "node-2", "node", "excluded_no_hardware", 30*time.Hour)

	rep, e := BuildE2Report(context.Background(), db, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if rep.ProspectiveBatches != 1 || len(rep.Episodes) != 2 || rep.Evaluated != 1 || rep.OutOfScope != 1 {
		t.Fatalf("%+v", rep)
	}
	var ev E2Episode
	for _, x := range rep.Episodes {
		if x.Status == "evaluated" {
			ev = x
		}
	}
	if ev.NodeIP != "node-1" || len(ev.FeedbackIDs) != 2 || ev.QueryID != q.ID || ev.M2 == nil || ev.M2.N != 2 || ev.M2.K != 1 {
		t.Fatalf("%+v", ev)
	}
	// Both members tie (no signal before cutoff): expected 0.5; hash decides the hit.
	if math.Abs(ev.M2.Expected-.5) > 1e-12 || math.Abs(ev.M0-.5) > 1e-12 {
		t.Fatalf("%+v", ev.M2)
	}
	if len(rep.UnreviewedFeedback) != 1 || rep.UnreviewedFeedback[0] != open.ID {
		t.Fatal(rep.UnreviewedFeedback)
	}
	if rep.ReadPoint != "none" || rep.PValue != nil || rep.Verdict != "accumulating" {
		t.Fatal(rep.ReadPoint, rep.Verdict)
	}
	if md := RenderE2Markdown(rep); !strings.Contains(md, "已评价独立 episode | 1") {
		t.Fatal(md)
	}
}

// Retrospective batches and incidents before the start are never evaluated.
func TestBuildE2ReportIgnoresRetrospectiveAndPreStart(t *testing.T) {
	db, q := shadowDB(t)
	old := E2PrimeStart
	E2PrimeStart = q.Cutoff.Add(-time.Hour)
	defer func() { E2PrimeStart = old }()
	if _, e := RecordShadow(context.Background(), db, q.ID, q.CompletedAt.Add(ShadowProspectiveWindow+time.Minute)); e != nil {
		t.Fatal(e)
	}
	feedbackOn(t, db, q, "node-1", "node", "confirmed_hardware", 24*time.Hour)
	feedbackOn(t, db, q, "node-2", "node", "confirmed_hardware", -2*time.Hour) // before start
	rep, e := BuildE2Report(context.Background(), db, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if rep.ProspectiveBatches != 0 || rep.Evaluated != 0 || len(rep.Episodes) != 1 || rep.Episodes[0].Status != "no_prospective_ranking" {
		t.Fatalf("%+v", rep)
	}
	var n int64
	db.Model(&api.GPURiskShadowBatch{}).Count(&n)
	if n != 1 {
		t.Fatal(n)
	}
}

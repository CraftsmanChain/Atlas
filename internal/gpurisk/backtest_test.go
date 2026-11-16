package gpurisk

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
)

// Fixture: 20 in-scope nodes. Node 1 fails 08-01 (first), 08-20 and 09-10
// (recurrent); node 2 fails once on 09-25 (first). Node 20 joins DCGM on 09-01
// and fails on 08-15 (not a member then). An out-of-scope node also fails.
func TestRouteBacktestReplaysWithoutLeakage(t *testing.T) {
	db := testDB(t)
	if e := db.AutoMigrate(&api.GPUCoreEvent{}, &api.GPUNodeObservation{}, &api.GPURiskQuery{}, &api.GPURiskMember{}); e != nil {
		t.Fatal(e)
	}
	done := time.Date(2026, 11, 15, 2, 0, 0, 0, shanghai)
	q := api.GPURiskQuery{Key: "k", ScopeSHA: "s", Contract: Contract, Target: Target, Cutoff: done.UTC(), Status: "completed", MemberCount: 20, CompletedAt: &done}
	if e := db.Create(&q).Error; e != nil {
		t.Fatal(e)
	}
	for n := 1; n <= 20; n++ {
		ip := fmt.Sprintf("10.0.0.%d", n)
		first := "2026-07-01"
		if n == 20 {
			first = "2026-09-01"
		}
		if e := db.Create(&api.GPUNodeObservation{NodeIP: ip, Model: "NVIDIA GeForce RTX 4090", FirstSeenDay: first, LastSeenDay: "2026-11-15"}).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Create(&api.GPURiskMember{QueryID: q.ID, NodeID: uint(n), Generation: 1, NodeIP: ip, Status: "included"}).Error; e != nil {
			t.Fatal(e)
		}
	}
	k := 0
	add := func(ip string, m time.Month, d int) {
		k++
		at := time.Date(2026, m, d, 12, 0, 0, 0, shanghai)
		if e := db.Create(&api.GPUCoreEvent{Key: fmt.Sprint("e", k), Source: CoreEventSourceVM, NodeIP: ip, Code: "xid79", Day: at.Format("2006-01-02"), ObservedAt: at.UTC(), ImportedAt: time.Date(2026, 11, 14, 0, 0, 0, 0, shanghai).UTC()}).Error; e != nil {
			t.Fatal(e)
		}
	}
	add("10.0.0.1", 8, 1)
	add("10.0.0.1", 8, 20)
	add("10.0.0.1", 9, 10)
	add("10.0.0.2", 9, 25)
	add("10.0.0.20", 8, 15)
	add("10.9.9.9", 8, 5)
	bt, err := BuildRouteBacktest(context.Background(), db, time.Date(2026, 11, 16, 11, 0, 0, 0, shanghai))
	if err != nil {
		t.Fatal(err)
	}
	if bt.Scope != 20 || bt.NoMember != 1 || bt.FirstDay != "2026-07-09" || bt.LastDay != "2026-11-07" {
		t.Fatalf("header %+v", bt)
	}
	byP := map[string]BacktestRow{}
	for _, r := range bt.Rows {
		byP[r.Period] = r
	}
	all := byP["all"]
	if all.Episodes != 4 || all.FirstTime != 2 || byP["2026-08"].Episodes != 2 || byP["2026-09"].Episodes != 2 {
		t.Fatalf("rows %+v", bt.Rows)
	}
	// Recurrent failures: node 1 is the unique top scorer before 08-20 and 09-10.
	if all.R1.RecurCapture != 1 {
		t.Fatalf("recurrent capture %v", all.R1.RecurCapture)
	}
	// First-time failures must not see their own events: node 1 ties with the
	// other 18 members at 0 before 08-01 (credit 1/19), node 2 sits below node 1
	// before 09-25 (credit 0).
	if want := (1.0 / 19) / 2; math.Abs(all.R1.FirstCapture-want) > 1e-9 {
		t.Fatalf("first-time capture %v want %v (leakage?)", all.R1.FirstCapture, want)
	}
	if byP["2026-08"].MedianMembers != 19 || byP["2026-09"].MedianMembers != 20 {
		t.Fatalf("members %+v", bt.Rows)
	}
	md := RenderRouteBacktestMarkdown(bt)
	if !strings.Contains(md, "合计") || !strings.Contains(md, "2026-09") {
		t.Fatal(md)
	}
	if _, err := BuildRouteBacktest(context.Background(), testDB(t), time.Now()); err != nil {
		t.Fatal(err)
	}
}

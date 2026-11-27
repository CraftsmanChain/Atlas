// Command node-hw-memrt replays the MEM-RT near-real-time memory rules
// (internal/nodehw/memrt.go, memory plan v1.2 3.3) over a past window against
// VictoriaMetrics, read-only. Used for the V-6 acceptance check:
//
//	node-hw-memrt -vm http://10.80.192.252:28481/select/0/prometheus -from "2026-11-26 16:00" -to "2026-11-26 17:00"
//
// Events print as TSV; nothing is written unless -out is given.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"atlas/internal/nodehw"
	promclient "atlas/internal/prometheus"
)

func main() {
	vm := flag.String("vm", "http://10.80.192.252:28481/select/0/prometheus", "Prometheus/VictoriaMetrics base URL")
	from := flag.String("from", "", "start, Asia/Shanghai \"YYYY-MM-DD HH:MM\"")
	to := flag.String("to", "", "end, Asia/Shanghai \"YYYY-MM-DD HH:MM\"")
	step := flag.Duration("step", 5*time.Minute, "evaluation step (the service polls every 5m)")
	out := flag.String("out", "", "optional events TSV to append to")
	resets := flag.String("resets", "/data/atlas-research/node-hw-ledger/sel_reset_events.tsv", "Atlas SEL rotations (by=atlas)")
	flag.Parse()
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	start, err1 := time.ParseInLocation("2006-01-02 15:04", *from, loc)
	end, err2 := time.ParseInLocation("2006-01-02 15:04", *to, loc)
	if err1 != nil || err2 != nil || !end.After(start) {
		log.Fatal("need -from and -to as \"YYYY-MM-DD HH:MM\" with to > from")
	}
	c, err := promclient.NewClient(*vm, 60*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	m := nodehw.NewMemRT(c, *out, *resets, "")
	fmt.Println("eval_at\ttime\tip\tsignal\tbefore\tafter\tdetail\tby")
	for t := start; !t.After(end); t = t.Add(*step) {
		evs, err := m.Poll(context.Background(), t)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", t.Format(time.RFC3339), err)
		}
		for _, e := range evs {
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.In(loc).Format("15:04"), e.Time, e.IP, e.Signal, e.Before, e.After, e.Detail, e.By)
		}
	}
}

package gpurisk

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Kernel-log source of the core-event ledger (plan v5 M1 second source).
// Input is the S1 collector's xid.tsv: node_ip, local RFC3339 timestamp, PCI
// address, XID code, line count. Onset rule mirrors the VM source: a
// (node, GPU PCI address, code) has kernel lines on a day and none on the two
// days before. Only days with a full two-day lookback inside the collected
// window are emitted; re-imports are idempotent on Key.

// KernelXIDLine is one aggregated kernel NVRM Xid line group.
type KernelXIDLine struct {
	NodeIP string
	At     time.Time
	PCI    string
	Code   string
}

// parseKernelTime accepts RFC3339 or a syslog stamp ("Nov  5 09:22:26",
// Asia/Shanghai); the syslog year is inferred so the time is not after now+1d.
func parseKernelTime(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	s = strings.Join(strings.Fields(s), " ")
	t, err := time.ParseInLocation("Jan 2 15:04:05 2006", fmt.Sprintf("%s %d", s, now.In(shanghai).Year()), shanghai)
	if err != nil {
		return t, err
	}
	if t.After(now.Add(24 * time.Hour)) {
		t = t.AddDate(-1, 0, 0)
	}
	return t, nil
}

// ParseKernelXID parses xid.tsv; malformed rows are counted, not fatal.
func ParseKernelXID(r io.Reader) ([]KernelXIDLine, int, error) {
	return ParseKernelXIDAt(r, time.Now())
}

// ParseKernelXIDAt is ParseKernelXID with an explicit reference time.
func ParseKernelXIDAt(r io.Reader, now time.Time) ([]KernelXIDLine, int, error) {
	out, bad := []KernelXIDLine{}, 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Split(strings.TrimRight(sc.Text(), "\r"), "\t")
		if len(f) < 4 {
			bad++
			continue
		}
		at, err := parseKernelTime(strings.TrimSpace(f[1]), now)
		code := strings.TrimSpace(f[3])
		if err != nil || strings.TrimSpace(f[0]) == "" || code == "" {
			bad++
			continue
		}
		out = append(out, KernelXIDLine{NodeIP: strings.TrimSpace(f[0]), At: at, PCI: NormPCI(f[2]), Code: code})
	}
	return out, bad, sc.Err()
}

// NormPCI reduces any PCI address form ("0000:e1:00", "00000000:E1:00.0") to
// lower-case "bus:device" so both ledger sources name a GPU the same way.
func NormPCI(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndex(s, "."); i > 0 {
		s = s[:i]
	}
	p := strings.Split(s, ":")
	if len(p) >= 2 {
		return p[len(p)-2] + ":" + p[len(p)-1]
	}
	return s
}

// DetectKernelCoreEvents returns onsets for critical codes on days in
// [windowStart+2d, windowEnd], where the window is the collected day range.
func DetectKernelCoreEvents(lines []KernelXIDLine, windowStart, windowEnd time.Time) []api.GPUCoreEvent {
	crit := map[string]bool{}
	for _, c := range CoreXIDCodes {
		crit[c] = true
	}
	type key struct{ ip, pci, code string }
	first := map[key]map[string]time.Time{}
	for _, l := range lines {
		if !crit[l.Code] {
			continue
		}
		k := key{l.NodeIP, l.PCI, l.Code}
		d := l.At.In(shanghai).Format("2006-01-02")
		if first[k] == nil {
			first[k] = map[string]time.Time{}
		}
		if t, ok := first[k][d]; !ok || l.At.Before(t) {
			first[k][d] = l.At
		}
	}
	emitFrom := dayStart(windowStart).AddDate(0, 0, 2).Format("2006-01-02")
	last := dayStart(windowEnd).Format("2006-01-02")
	out := []api.GPUCoreEvent{}
	for k, byDay := range first {
		for d, at := range byDay {
			if d < emitFrom || d > last {
				continue
			}
			if _, a := byDay[prevDay(d, 1)]; a {
				continue
			}
			if _, b := byDay[prevDay(d, 2)]; b {
				continue
			}
			ev := api.GPUCoreEvent{Source: CoreEventSourceKernel, NodeIP: k.ip, PCIBusID: k.pci, Code: "xid" + k.code, Day: d, ObservedAt: at.UTC().Truncate(time.Microsecond)}
			ev.Key, _ = digest([]any{CoreEventLedgerVersion, ev.Source, ev.NodeIP, ev.PCIBusID, ev.Code, ev.Day})
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// ImportKernelCoreEvents stores kernel onsets and records a ledger run.
// GPU UUID/model are attached from the VM source when the same node and PCI
// address are already known there.
func ImportKernelCoreEvents(ctx context.Context, db *gorm.DB, lines []KernelXIDLine, windowStart, windowEnd time.Time) (api.GPUCoreEventRun, error) {
	db = db.WithContext(ctx)
	run := api.GPUCoreEventRun{Source: CoreEventSourceKernel, WindowStart: dayStart(windowStart).UTC(), WindowEnd: windowEnd.UTC(), StartedAt: time.Now().UTC(), Status: "running", Series: len(lines)}
	if err := db.Create(&run).Error; err != nil {
		return run, err
	}
	events := DetectKernelCoreEvents(lines, windowStart, windowEnd)
	var known []api.GPUCoreEvent
	err := db.Where("source = ?", CoreEventSourceVM).Find(&known).Error
	if err == nil {
		ident := map[string]api.GPUCoreEvent{}
		for _, k := range known {
			ident[k.NodeIP+"|"+NormPCI(k.PCIBusID)] = k
		}
		imported := time.Now().UTC().Truncate(time.Microsecond)
		keys := []string{}
		for i := range events {
			if k, ok := ident[events[i].NodeIP+"|"+events[i].PCIBusID]; ok {
				events[i].GPUUUID, events[i].Model, events[i].Hostname = k.GPUUUID, k.Model, k.Hostname
			}
			events[i].ImportedAt, events[i].RunID = imported, run.ID
			keys = append(keys, events[i].Key)
		}
		run.SHA, _ = digest([]any{CoreEventLedgerVersion, CoreEventSourceKernel, run.WindowStart, run.WindowEnd, keys})
		if len(events) > 0 {
			res := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).CreateInBatches(events, 200)
			err = res.Error
			run.NewEvents = int(res.RowsAffected)
		}
	}
	run.FinishedAt = time.Now().UTC()
	run.Status = "completed"
	if err != nil {
		run.Status, run.ErrorCode, run.Error = "failed", "core_event_kernel_import_failed", fmt.Sprint(err)
		if len(run.Error) > 500 {
			run.Error = run.Error[:500]
		}
	}
	if e := db.Save(&run).Error; e != nil && err == nil {
		err = e
	}
	return run, err
}

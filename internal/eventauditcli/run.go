package eventauditcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"atlas/internal/eventaudit"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func read(path string) (eventaudit.Snapshot, error) {
	f, e := os.Open(path)
	if e != nil {
		return eventaudit.Snapshot{}, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 128<<20+1))
	if e != nil || len(b) > 128<<20 {
		return eventaudit.Snapshot{}, fmt.Errorf("checkpoint too large")
	}
	var header struct {
		Version string `json:"version"`
	}
	if e = json.Unmarshal(b, &header); e != nil {
		return eventaudit.Snapshot{}, e
	}
	if header.Version != eventaudit.Version {
		return eventaudit.Snapshot{Version: "legacy"}, nil
	}
	var s eventaudit.Snapshot
	e = json.Unmarshal(b, &s)
	return s, e
}
func Run(args []string, out, errout io.Writer) int {
	f := flag.NewFlagSet("hardware-event-audit", flag.ContinueOnError)
	f.SetOutput(errout)
	driver := f.String("driver", "postgres", "postgres or sqlite (read-only)")
	env := f.String("dsn-env", "ATLAS_DATABASE_DSN", "DSN environment reference; never printed")
	ns := f.String("source-namespace", "", "Stable non-secret source identifier")
	input := f.String("snapshot", "", "Offline v2 snapshot; alternative to database")
	previous := f.String("previous", "", "Previous checkpoint; legacy establishes a new baseline")
	dir := f.String("out-dir", "", "New private directory; never overwritten")
	if f.Parse(args) != nil {
		return 2
	}
	if f.NArg() != 0 || *dir == "" {
		fmt.Fprintln(errout, "new out-dir required; no positional arguments")
		return 2
	}
	var old *eventaudit.Snapshot
	if *previous != "" {
		s, e := read(*previous)
		if e != nil {
			fmt.Fprintln(errout, "cannot read previous checkpoint")
			return 1
		}
		old = &s
	}
	var s eventaudit.Snapshot
	if *input != "" {
		var e error
		s, e = read(*input)
		if e != nil || s.Validate() != nil {
			fmt.Fprintln(errout, "invalid offline v2 checkpoint")
			return 1
		}
	} else {
		dsn := os.Getenv(*env)
		var dial gorm.Dialector
		switch *driver {
		case "postgres":
			dial = postgres.Open(dsn)
		case "sqlite":
			if strings.Contains(dsn, "?") || strings.HasPrefix(dsn, "file:") || dsn == "" {
				fmt.Fprintln(errout, "sqlite requires a plain existing database path")
				return 2
			}
			if _, e := os.Stat(dsn); e != nil {
				fmt.Fprintln(errout, "sqlite database unavailable")
				return 1
			}
			abs, e := filepath.Abs(dsn)
			if e != nil {
				return 2
			}
			u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: "mode=ro"}
			dial = sqlite.Open(u.String())
		default:
			fmt.Fprintln(errout, "unsupported database driver")
			return 2
		}
		if dsn == "" {
			fmt.Fprintln(errout, "database environment unavailable")
			return 1
		}
		db, e := gorm.Open(dial, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
		if e != nil {
			fmt.Fprintln(errout, "database connection unavailable")
			return 1
		}
		sqlDB, e := db.DB()
		if e != nil {
			return 1
		}
		defer sqlDB.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		s, e = eventaudit.Capture(ctx, db, *ns, time.Now().UTC())
		if e != nil {
			fmt.Fprintln(errout, "read-only inventory failed; no checkpoint emitted (check schema, bounds, timestamps and source availability)")
			return 1
		}
	}
	report, e := eventaudit.Compare(old, s)
	if e != nil {
		fmt.Fprintln(errout, "checkpoint comparison rejected")
		return 1
	}
	if e = os.Mkdir(*dir, 0700); e != nil {
		fmt.Fprintln(errout, "cannot reserve new output directory; nothing overwritten")
		return 1
	}
	for _, a := range []struct {
		name  string
		value any
	}{{"checkpoint.json", s}, {"delta.json", report}} {
		b, e := json.MarshalIndent(a.value, "", "  ")
		if e != nil {
			return 1
		}
		b = append(b, '\n')
		p := filepath.Join(*dir, a.name)
		file, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return 1
		}
		_, e = file.Write(b)
		if e == nil {
			e = file.Sync()
		}
		closeErr := file.Close()
		if e != nil || closeErr != nil {
			fmt.Fprintln(errout, "artifact write failed; inspect reserved output directory")
			return 1
		}
	}
	fmt.Fprintf(out, "Read-only inventory: %d events; snapshot=%s; no labels or actions emitted\n", len(s.Entries), s.SHA)
	return 0
}

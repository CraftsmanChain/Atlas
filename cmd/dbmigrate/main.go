package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"atlas/pkg/storage"
)

func main() {
	var source string
	var target string
	var tables string
	var batchSize int
	var resume bool
	flag.StringVar(&source, "source", "", "read-only SQLite source DSN or path")
	flag.StringVar(&target, "target", "", "PostgreSQL target DSN (defaults to ATLAS_DATABASE_DSN)")
	flag.StringVar(&tables, "tables", "", "optional comma-separated Atlas table names")
	flag.IntVar(&batchSize, "batch-size", 500, "rows per insert batch")
	flag.BoolVar(&resume, "resume", false, "resume an interrupted initial import from the target maximum IDs")
	flag.Parse()

	if strings.TrimSpace(target) == "" {
		target = os.Getenv("ATLAS_DATABASE_DSN")
	}
	if strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" {
		flag.Usage()
		log.Fatal("both --source and PostgreSQL target DSN are required")
	}
	var selected []string
	if strings.TrimSpace(tables) != "" {
		selected = strings.Split(tables, ",")
	}
	results, err := storage.MigrateSQLite(source, target, selected, batchSize, resume)
	if err != nil {
		log.Fatal(err)
	}
	for _, result := range results {
		fmt.Printf("%s source=%d target=%d\n", result.Table, result.SourceCount, result.TargetCount)
	}
}

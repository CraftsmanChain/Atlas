package logging

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveDirDefault(t *testing.T) {
	if got := resolveDir(""); got != "logs" {
		t.Fatalf("resolveDir default = %q, want %q", got, "logs")
	}
}

func TestDailyRotatingWriterWriteAndRotate(t *testing.T) {
	tmpDir := t.TempDir()

	writer, err := NewDailyRotatingWriter(tmpDir)
	if err != nil {
		t.Fatalf("NewDailyRotatingWriter failed: %v", err)
	}
	defer writer.Close()

	baseTime := time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)
	writer.mu.Lock()
	writer.now = func() time.Time { return baseTime }
	writer.currentDate = ""
	writer.mu.Unlock()

	if _, err := writer.Write([]byte("first-line\n")); err != nil {
		t.Fatalf("first write failed: %v", err)
	}

	day1 := filepath.Join(tmpDir, "atlas-2026-04-09.log")
	contentDay1, err := os.ReadFile(day1)
	if err != nil {
		t.Fatalf("read day1 log failed: %v", err)
	}
	if !strings.Contains(string(contentDay1), "first-line") {
		t.Fatalf("day1 log missing expected content: %q", string(contentDay1))
	}

	nextDay := baseTime.Add(24 * time.Hour)
	writer.mu.Lock()
	writer.now = func() time.Time { return nextDay }
	writer.mu.Unlock()

	if _, err := writer.Write([]byte("second-line\n")); err != nil {
		t.Fatalf("second write failed: %v", err)
	}

	day2 := filepath.Join(tmpDir, "atlas-2026-04-10.log")
	contentDay2, err := os.ReadFile(day2)
	if err != nil {
		t.Fatalf("read day2 log failed: %v", err)
	}
	if !strings.Contains(string(contentDay2), "second-line") {
		t.Fatalf("day2 log missing expected content: %q", string(contentDay2))
	}
}

func TestInitGlobalLoggerWritesToFile(t *testing.T) {
	tmpDir := t.TempDir()
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	})

	writer, err := InitGlobalLogger(tmpDir)
	if err != nil {
		t.Fatalf("InitGlobalLogger failed: %v", err)
	}
	defer writer.Close()

	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	writer.mu.Lock()
	writer.now = func() time.Time { return now }
	writer.currentDate = ""
	writer.mu.Unlock()

	log.Printf("hello-logger-test")

	logFile := filepath.Join(tmpDir, "atlas-2026-04-09.log")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log file failed: %v", err)
	}
	if !strings.Contains(string(data), "hello-logger-test") {
		t.Fatalf("expected log message not found in %s", logFile)
	}
}

package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const defaultLogDir = "logs"

// DailyRotatingWriter 按天写入日志文件，跨天自动切换到新文件。
type DailyRotatingWriter struct {
	mu          sync.Mutex
	dir         string
	file        *os.File
	currentDate string
	now         func() time.Time
}

func NewDailyRotatingWriter(dir string) (*DailyRotatingWriter, error) {
	w := &DailyRotatingWriter{
		dir: resolveDir(dir),
		now: time.Now,
	}
	if err := w.rotateIfNeededLocked(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *DailyRotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.rotateIfNeededLocked(); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}

func (w *DailyRotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *DailyRotatingWriter) rotateIfNeededLocked() error {
	date := w.now().Format("2006-01-02")
	if w.file != nil && w.currentDate == date {
		return nil
	}

	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return fmt.Errorf("create log dir %q: %w", w.dir, err)
	}

	path := filepath.Join(w.dir, fmt.Sprintf("atlas-%s.log", date))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file %q: %w", path, err)
	}

	if w.file != nil {
		_ = w.file.Close()
	}
	w.file = f
	w.currentDate = date
	return nil
}

func resolveDir(dir string) string {
	if dir == "" {
		return defaultLogDir
	}
	return dir
}

// InitGlobalLogger 初始化全局 logger，将日志同时输出到控制台和按天轮转文件。
func InitGlobalLogger(dir string) (*DailyRotatingWriter, error) {
	writer, err := NewDailyRotatingWriter(dir)
	if err != nil {
		return nil, err
	}

	log.SetOutput(io.MultiWriter(os.Stdout, writer))
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	return writer, nil
}

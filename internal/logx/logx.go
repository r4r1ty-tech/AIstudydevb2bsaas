package logx

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var (
	mu    sync.RWMutex
	level = LevelInfo
)

func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

func Setup() {
	mu.Lock()
	level = ParseLevel(os.Getenv("LOG_LEVEL"))
	mu.Unlock()

	w := io.Writer(os.Stderr)
	if path := strings.TrimSpace(os.Getenv("LOG_FILE")); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			w = io.MultiWriter(os.Stderr, f)
		} else {
			log.Printf("logx: открыть %s: %v", path, err)
		}
	}
	log.SetOutput(w)
}

func Enabled(l Level) bool {
	mu.RLock()
	defer mu.RUnlock()
	return l >= level
}

func CurrentLevel() Level {
	mu.RLock()
	defer mu.RUnlock()
	return level
}

func LevelName(l Level) string {
	names := [...]string{"debug", "info", "warn", "error"}
	if l < LevelDebug || int(l) >= len(names) {
		return "info"
	}
	return names[l]
}

func Debugf(component, format string, args ...any) { logf(LevelDebug, component, format, args...) }
func Infof(component, format string, args ...any)  { logf(LevelInfo, component, format, args...) }
func Warnf(component, format string, args ...any)  { logf(LevelWarn, component, format, args...) }
func Errorf(component, format string, args ...any) { logf(LevelError, component, format, args...) }

func logf(l Level, component, format string, args ...any) {
	if !Enabled(l) {
		return
	}
	tags := [...]string{"DEBUG", "INFO", "WARN", "ERROR"}
	msg := fmt.Sprintf(format, args...)
	_ = log.Output(3, "["+tags[l]+"] "+component+": "+msg)
}

// maxLine caps a pending partial line so a chatty child cannot grow memory.
const maxLine = 4096

// LineWriter turns a child process's stderr into log lines, so ffmpeg or
// wake.py errors land in LOG_FILE and not only in the short journald window.
// keep, if set, drops lines it returns false for (noisy Chromium stderr).
func LineWriter(l Level, component, prefix string, keep func(string) bool) io.Writer {
	return &lineWriter{level: l, component: component, prefix: prefix, keep: keep}
}

type lineWriter struct {
	level     Level
	component string
	prefix    string
	keep      func(string) bool

	mu  sync.Mutex
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		w.emit(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxLine {
		w.emit(string(w.buf))
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

func (w *lineWriter) emit(line string) {
	line = strings.TrimSpace(strings.TrimRight(line, "\r"))
	if line == "" || (w.keep != nil && !w.keep(line)) {
		return
	}
	logf(w.level, w.component, "%s%s", w.prefix, line)
}

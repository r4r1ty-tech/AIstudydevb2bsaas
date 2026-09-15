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

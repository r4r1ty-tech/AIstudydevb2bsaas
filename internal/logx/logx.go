package logx

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
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

// Подробный лог отладки: независимо от LOG_LEVEL каждый процесс пишет всё,
// включая DEBUG, в <LOG_DIR>/<процесс>.log с ротацией (LOG_DEBUG_MAX_MB ×
// LOG_DEBUG_KEEP сжатых кусков), а снимки страниц и промптов — в
// <LOG_DIR>/dumps с общим потолком LOG_DUMP_MAX_MB. LOG_DEBUG=0 — выключить.
var (
	dbgMu  sync.RWMutex
	dbg    io.Writer
	dumper *Dumper
)

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
	setupDebug()
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && v > 0 {
		return v
	}
	return def
}

func setupDebug() {
	if v := strings.TrimSpace(os.Getenv("LOG_DEBUG")); v == "0" || strings.EqualFold(v, "false") || strings.EqualFold(v, "off") {
		return
	}
	proc := filepath.Base(os.Args[0])
	dir := strings.TrimSpace(os.Getenv("LOG_DIR"))
	if dir == "" {
		if strings.HasSuffix(proc, ".test") {
			return // go test: не мусорим в папке пакета
		}
		dir = "logs"
	}
	r, err := NewRotator(filepath.Join(dir, proc+".log"), int64(envInt("LOG_DEBUG_MAX_MB", 50))<<20, envInt("LOG_DEBUG_KEEP", 20))
	if err != nil {
		log.Printf("logx: подробный лог %s: %v", dir, err)
		return
	}
	dbgMu.Lock()
	dbg = r
	dumper = &Dumper{Dir: filepath.Join(dir, "dumps"), MaxBytes: int64(envInt("LOG_DUMP_MAX_MB", 500)) << 20}
	dbgMu.Unlock()
	log.Printf("[INFO] logx: подробный лог %s (%d МБ × %d), снимки %s",
		r.Path, r.Max>>20, r.Keep, dumper.Dir)
}

// SetDebugSink — подменить подробный лог (тесты); nil — выключить.
func SetDebugSink(w io.Writer, d *Dumper) {
	dbgMu.Lock()
	dbg, dumper = w, d
	dbgMu.Unlock()
}

// Verbose — пишется ли DEBUG хоть куда-то: можно собирать дорогие подробности.
func Verbose() bool {
	dbgMu.RLock()
	on := dbg != nil
	dbgMu.RUnlock()
	return on || Enabled(LevelDebug)
}

// Dump — снимок целиком (HTML страницы теста, промпт, ответ ИИ) в
// LOG_DIR/dumps/<kind>/…gz. Возвращает путь или пусто, если снимки выключены.
func Dump(kind, name string, data []byte) string {
	dbgMu.RLock()
	d := dumper
	dbgMu.RUnlock()
	if d == nil {
		return ""
	}
	p := d.Dump(kind, name, data)
	if p != "" {
		logf(LevelDebug, "dump", "%s/%s → %s (%d байт)", kind, name, p, len(data))
	}
	return p
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
	main := Enabled(l)
	dbgMu.RLock()
	d := dbg
	dbgMu.RUnlock()
	if !main && d == nil {
		return
	}
	tags := [...]string{"DEBUG", "INFO", "WARN", "ERROR"}
	msg := fmt.Sprintf(format, args...)
	if main {
		_ = log.Output(3, "["+tags[l]+"] "+component+": "+msg)
	}
	if d != nil {
		line := time.Now().Format("2006-01-02 15:04:05.000000") + " [" + tags[l] + "] " + component + ": " + msg + "\n"
		_, _ = io.WriteString(d, line)
	}
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

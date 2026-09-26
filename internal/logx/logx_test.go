package logx

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reset(t *testing.T) {
	t.Helper()
	mu.Lock()
	level = LevelInfo
	mu.Unlock()
	log.SetOutput(os.Stderr)
}

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"debug":   LevelDebug,
		"DEBUG":   LevelDebug,
		" info ":  LevelInfo,
		"warn":    LevelWarn,
		"warning": LevelWarn,
		"error":   LevelError,
		"":        LevelInfo,
		"junk":    LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %d want %d", in, got, want)
		}
	}
}

func TestEnabledFiltering(t *testing.T) {
	reset(t)
	t.Cleanup(func() { reset(t) })

	mu.Lock()
	level = LevelWarn
	mu.Unlock()
	if Enabled(LevelDebug) || Enabled(LevelInfo) {
		t.Error("debug/info must be disabled at warn")
	}
	if !Enabled(LevelWarn) || !Enabled(LevelError) {
		t.Error("warn/error must be enabled")
	}
}

func TestWritesFormattedLine(t *testing.T) {
	reset(t)
	t.Cleanup(func() { reset(t) })
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FILE", "")
	Setup()

	var buf bytes.Buffer
	log.SetOutput(&buf)
	Infof("bbb", "seated key=%s", "1:2")

	out := buf.String()
	if !strings.Contains(out, "[INFO] bbb: seated key=1:2") {
		t.Fatalf("output = %q", out)
	}
}

func TestDebugSuppressedAtInfo(t *testing.T) {
	reset(t)
	t.Cleanup(func() { reset(t) })
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("LOG_FILE", "")
	Setup()

	var buf bytes.Buffer
	log.SetOutput(&buf)
	Debugf("bbb", "noise")
	if buf.Len() != 0 {
		t.Fatalf("debug should be suppressed, got %q", buf.String())
	}
	Errorf("bbb", "boom %d", 7)
	if !strings.Contains(buf.String(), "[ERROR] bbb: boom 7") {
		t.Fatalf("error missing: %q", buf.String())
	}
}

func TestSetupSplitsToFile(t *testing.T) {
	reset(t)
	t.Cleanup(func() { reset(t) })
	path := filepath.Join(t.TempDir(), "ssau.log")
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("LOG_FILE", path)
	Setup()
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	Warnf("panel", "bad password %s", "/api/now")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[WARN] panel: bad password /api/now") {
		t.Fatalf("file = %q", string(data))
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	reset(t)
	t.Cleanup(func() { reset(t) })
	var buf bytes.Buffer
	log.SetOutput(&buf)
	return &buf
}

func TestLineWriterSplitsLines(t *testing.T) {
	buf := captureLog(t)
	w := LineWriter(LevelWarn, "capture", "ffmpeg: ", nil)
	for _, chunk := range []string{"first li", "ne\r\nsecond\n\n   \nthi", "rd\n"} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	out := buf.String()
	for _, want := range []string{"[WARN] capture: ffmpeg: first line", "ffmpeg: second", "ffmpeg: third"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Count(out, "\n") != 3 {
		t.Errorf("blank lines must be dropped: %q", out)
	}
}

func TestLineWriterFilterAndLevel(t *testing.T) {
	buf := captureLog(t)
	keep := func(s string) bool { return strings.Contains(s, "pulse") }
	w := LineWriter(LevelWarn, "bbb", "chromium: ", keep)
	_, _ = w.Write([]byte("dbus noise\npa_context_connect: pulse refused\n"))
	if out := buf.String(); strings.Contains(out, "dbus") || !strings.Contains(out, "pulse refused") {
		t.Fatalf("filter: %q", out)
	}

	buf.Reset()
	setLevel(t, LevelError)
	_, _ = LineWriter(LevelWarn, "x", "", nil).Write([]byte("hidden\n"))
	if buf.Len() != 0 {
		t.Fatalf("warn line must be filtered at error level: %q", buf.String())
	}
}

func TestLineWriterFlushesHugeLine(t *testing.T) {
	buf := captureLog(t)
	w := LineWriter(LevelWarn, "x", "", nil).(*lineWriter)
	_, _ = w.Write(bytes.Repeat([]byte("a"), maxLine+10))
	if buf.Len() == 0 || len(w.buf) != 0 {
		t.Fatalf("oversized partial line must flush, pending=%d", len(w.buf))
	}
}

func TestCurrentLevelAndName(t *testing.T) {
	reset(t)
	t.Cleanup(func() { reset(t) })
	setLevel(t, LevelWarn)
	if CurrentLevel() != LevelWarn {
		t.Fatalf("CurrentLevel = %d", CurrentLevel())
	}
	for l, want := range map[Level]string{LevelDebug: "debug", LevelInfo: "info", LevelWarn: "warn", LevelError: "error", Level(-1): "info", Level(9): "info"} {
		if got := LevelName(l); got != want {
			t.Errorf("LevelName(%d) = %q, want %q", l, got, want)
		}
	}
}

func setLevel(t *testing.T, l Level) {
	t.Helper()
	mu.Lock()
	level = l
	mu.Unlock()
}

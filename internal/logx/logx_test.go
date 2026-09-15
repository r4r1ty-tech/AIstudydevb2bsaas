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

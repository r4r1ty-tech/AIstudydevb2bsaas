package capture

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeBins puts shell stand-ins for pulseaudio/pactl/ffmpeg first in PATH.
// Each appends its argv to calls.log; pactl answers from the sinks file, so
// the real PulseAudio of the machine is never touched.
type fakeBins struct {
	dir   string
	log   string
	sinks string
}

func newFakeBins(t *testing.T) *fakeBins {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	f := &fakeBins{dir: dir, log: filepath.Join(dir, "calls.log"), sinks: filepath.Join(dir, "sinks")}
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho \"$(basename $0) $*\" >> "+f.log+"\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("pulseaudio", `case "$1" in --check) [ -f `+dir+`/running ] ;; --start) touch `+dir+`/running ;; esac`)
	write("pactl", `case "$1 $2" in
"list short") [ "$3" = sinks ] && cat `+f.sinks+` 2>/dev/null; exit 0 ;;
"load-module module-null-sink") echo "1 ssau_rec module-null-sink.c s16le 2ch 44100Hz IDLE" >> `+f.sinks+`; echo 7 ;;
"set-default-sink ssau_rec") exit 0 ;;
"get-default-sink ") echo ssau_rec ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac`)
	// ffmpeg: emit some PCM on stdout, a byte into the output file, stop on INT.
	write("ffmpeg", `out=""
for a in "$@"; do case "$a" in *.ogg) out="$a" ;; esac; done
printf 'OggS' > "$out"
trap 'exit 255' INT
while :; do printf '\001\000\002\000'; sleep 0.05; done`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

func (f *fakeBins) calls(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(f.log)
	return string(b)
}

func TestEnsureSinkStartsPulseAndLoadsOnce(t *testing.T) {
	f := newFakeBins(t)
	if err := EnsureSink(); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSink(); err != nil {
		t.Fatal(err)
	}
	calls := f.calls(t)
	if strings.Count(calls, "pulseaudio --start") != 1 {
		t.Errorf("pulseaudio should start once:\n%s", calls)
	}
	if strings.Count(calls, "load-module module-null-sink") != 1 {
		t.Errorf("sink should be loaded once:\n%s", calls)
	}
	if err := EnsureDefaultSink(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.calls(t), "pactl set-default-sink ssau_rec") {
		t.Error("default sink not set")
	}
}

func TestEnsureSinkLoadFailure(t *testing.T) {
	f := newFakeBins(t)
	if err := os.WriteFile(filepath.Join(f.dir, "pactl"), []byte("#!/bin/sh\necho 'Connection refused' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := EnsureSink()
	if err == nil || !strings.Contains(err.Error(), "Connection refused") {
		t.Fatalf("EnsureSink = %v", err)
	}
	if err := EnsureDefaultSink(); err == nil {
		t.Fatal("set-default-sink failure must surface")
	}
	LogRoute("test", "broken") // pactl errors are folded into the log line
	if got := pactlOut("info"); !strings.Contains(got, "err=") {
		t.Fatalf("pactlOut error = %q", got)
	}
}

func TestEnsureDefaultSinkWithoutPactl(t *testing.T) {
	t.Setenv("PATH", "")
	if err := EnsureDefaultSink(); err == nil {
		t.Fatal("no pactl must fail")
	}
	EnsurePulse() // no pulseaudio: silently skipped
}

func TestStartRecordsAndStops(t *testing.T) {
	newFakeBins(t)
	out := filepath.Join(t.TempDir(), "pack", "audio-1.ogg")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec, err := Start(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	if _, err := io.ReadFull(rec, buf); err != nil {
		t.Fatalf("pcm: %v", err)
	}
	if err := rec.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("segment: %v", err)
	}
}

// Cancelling the context (service stop) must SIGINT ffmpeg, not SIGKILL it.
func TestStartCtxCancelInterruptsFFmpeg(t *testing.T) {
	f := newFakeBins(t)
	if err := os.WriteFile(filepath.Join(f.dir, "ffmpeg"), []byte(`#!/bin/sh
out=""
for a in "$@"; do case "$a" in *.ogg) out="$a" ;; esac; done
trap 'echo interrupted > "$out"; exit 255' INT
while :; do printf '\000\000'; sleep 0.05; done
`), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "audio-1.ogg")
	ctx, cancel := context.WithCancel(context.Background())
	rec, err := Start(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, rec) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) == "interrupted" {
			_ = rec.Stop()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("ffmpeg did not get SIGINT on ctx cancel")
}

func TestSegmentPath(t *testing.T) {
	t.Parallel()
	if got := SegmentPath("/r/p", 42); got != filepath.Join("/r/p", "audio-42.ogg") {
		t.Fatalf("SegmentPath = %q", got)
	}
}

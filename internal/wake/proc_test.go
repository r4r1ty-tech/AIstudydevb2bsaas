package wake

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// wake.py loads the vosk model before it reads stdin. Push must not block
// meanwhile: the spotter shares ffmpeg's stdout, and a blocked spotter stalls
// the recording itself (prod 17.09: 0-byte segments).
func TestProcPushNeverBlocksOnStuckScript(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep")
	}
	rec, err := startCmd(exec.Command("sleep", "30"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 64*1024) // pipes hold 64K: two writes would block
	done := make(chan struct{})
	go func() {
		for i := 0; i < feedQueue*3; i++ {
			rec.Push(chunk, 16000)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Push blocked on a script that does not read stdin")
	}
	rec.mu.Lock()
	dropped := rec.dropped
	rec.mu.Unlock()
	if dropped == 0 {
		t.Fatal("expected overflow chunks to be dropped")
	}

	closed := make(chan struct{})
	go func() { _ = rec.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on a stuck writer")
	}
	if got := rec.Push(chunk, 16000); got != nil {
		t.Fatalf("Push after Close = %v", got)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestProcPushAfterScriptDied(t *testing.T) {
	rec, err := startCmd(exec.Command("true"))
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 5; i++ {
		rec.Push(make([]byte, 1024), 16000) // EPIPE in writeLoop, no panic
	}
}

func TestStartCmdNil(t *testing.T) {
	t.Parallel()
	if _, err := startCmd(nil); err == nil {
		t.Fatal("nil cmd must fail")
	}
	if _, err := startCmd(exec.Command(filepath.Join(t.TempDir(), "missing"))); err == nil {
		t.Fatal("missing binary must fail")
	}
}

func TestOpenValidation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := filepath.Join(dir, "wake.py")
	if err := os.WriteFile(script, []byte("print()"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ model, script string }{
		{"", script},
		{dir, ""},
		{filepath.Join(dir, "nope"), script},
		{script, script}, // model must be a dir
		{dir, filepath.Join(dir, "nope.py")},
	}
	for _, c := range cases {
		if r, err := Open(c.model, c.script, []string{"тест"}); err == nil {
			closeRecognizer(r)
			t.Errorf("Open(%q,%q) should fail", c.model, c.script)
		}
	}
}

func TestProcNilSafe(t *testing.T) {
	t.Parallel()
	var p *ProcRecognizer
	if p.Push([]byte{1, 2}, 16000) != nil || p.Close() != nil {
		t.Fatal("nil recognizer must be a no-op")
	}
	if drain(nil) != nil {
		t.Fatal("drain(nil)")
	}
	closeRecognizer(noopRecognizer{})
}

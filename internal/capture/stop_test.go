package capture

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func startShell(t *testing.T, script string) *Rec {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	out := filepath.Join(t.TempDir(), "audio-1.ogg")
	if err := os.WriteFile(out, []byte("ogg"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the trap install
	return &Rec{cmd: cmd, path: out}
}

func TestRecStopCleanOnSIGINT255(t *testing.T) {
	r := startShell(t, `trap 'exit 255' INT; while :; do sleep 0.05; done`)
	if err := r.Stop(); err != nil {
		t.Fatalf("exit 255 after SIGINT is a clean ffmpeg stop, got %v", err)
	}
	if err := r.Stop(); err != nil {
		t.Fatalf("second Stop must be a no-op: %v", err)
	}
}

func TestRecStopKillsWhenSIGINTIgnored(t *testing.T) {
	old := stopGrace
	stopGrace = 300 * time.Millisecond
	t.Cleanup(func() { stopGrace = old })
	r := startShell(t, `trap '' INT; while :; do sleep 0.05; done`)
	start := time.Now()
	err := r.Stop()
	if err == nil {
		t.Fatal("killed process must report an error")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Stop hung for %s", time.Since(start))
	}
}

func TestRecStopNil(t *testing.T) {
	t.Parallel()
	var r *Rec
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := (&Rec{}).Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRecReadClosed(t *testing.T) {
	t.Parallel()
	var r *Rec
	if _, err := r.Read(make([]byte, 4)); err == nil {
		t.Fatal("nil rec must return EOF")
	}
}

func TestIsInterrupted(t *testing.T) {
	t.Parallel()
	if isInterrupted(nil) || isInterrupted(errors.New("x")) {
		t.Fatal("non-exit errors are not interrupts")
	}
	err := exec.Command("sh", "-c", "exit 255").Run()
	if !isInterrupted(err) {
		t.Fatalf("exit 255 should count: %v", err)
	}
	if isInterrupted(exec.Command("sh", "-c", "exit 1").Run()) {
		t.Fatal("exit 1 is a real failure")
	}
}

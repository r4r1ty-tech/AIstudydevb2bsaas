package capture

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const SinkName = "ssau_rec"

type Rec struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	path   string
	mu     sync.Mutex
}

func EnsurePulse() {
	if _, err := exec.LookPath("pulseaudio"); err != nil {
		return
	}
	if exec.Command("pulseaudio", "--check").Run() == nil {
		return
	}
	_ = exec.Command("pulseaudio", "--start", "--exit-idle-time=-1", "--disallow-exit").Run()
}

func EnsureSink() error {
	EnsurePulse()
	if _, err := exec.LookPath("pactl"); err != nil {
		return fmt.Errorf("pactl не найден")
	}
	out, _ := exec.Command("pactl", "list", "short", "sinks").Output()
	if strings.Contains(string(out), SinkName) {
		return nil
	}
	cmd := exec.Command("pactl", "load-module", "module-null-sink",
		"sink_name="+SinkName,
		"sink_properties=device.description=SSAU")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("null sink: %s: %w", strings.TrimSpace(string(b)), err)
	}
	return nil
}

func Start(ctx context.Context, outPath string) (*Rec, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := EnsureSink(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return nil, err
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg не найден")
	}
	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, ffmpeg,
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "pulse", "-i", SinkName+".monitor",
		"-ac", "1", "-ar", "48000",
		"-c:a", "libopus", "-b:a", "32k",
		"-y", outPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}
	log.Printf("capture: ffmpeg pid=%d -> %s", cmd.Process.Pid, outPath)
	return &Rec{cmd: cmd, cancel: cancel, path: outPath}, nil
}

func (r *Rec) Stop() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil || r.cmd.Process == nil {
		return nil
	}
	_ = r.cmd.Process.Signal(syscall.SIGINT)
	err := r.cmd.Wait()
	if r.cancel != nil {
		r.cancel()
	}
	r.cmd = nil
	return err
}

func PulseEnv() []string {
	return append(os.Environ(),
		"PULSE_SINK="+SinkName,
		"PULSE_SOURCE="+SinkName+".monitor",
	)
}

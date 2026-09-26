package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

const (
	SinkName = "ssau_rec"
	WakeRate = 16000
)

// stopGrace is how long ffmpeg gets to finish the ogg after SIGINT.
var stopGrace = 10 * time.Second

type Rec struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	pcm    io.ReadCloser
	path   string
	mu     sync.Mutex
}

func EnsurePulse() {
	logx.Debugf("capture", "EnsurePulse: enter")
	if _, err := exec.LookPath("pulseaudio"); err != nil {
		logx.Debugf("capture", "EnsurePulse: pulseaudio not found: %v", err)
		return
	}
	if err := exec.Command("pulseaudio", "--check").Run(); err == nil {
		logx.Debugf("capture", "EnsurePulse: pulseaudio already running")
		return
	} else {
		logx.Debugf("capture", "EnsurePulse: check failed: %v", err)
	}
	if out, err := exec.Command("pulseaudio", "--start", "--exit-idle-time=-1", "--disallow-exit").CombinedOutput(); err != nil {
		logx.Errorf("capture", "EnsurePulse: start pulseaudio: %v: %s", err, strings.TrimSpace(string(out)))
	} else {
		logx.Infof("capture", "pulseaudio started")
	}
}

func EnsureSink() error {
	logx.Debugf("capture", "EnsureSink: enter sink=%s", SinkName)
	EnsurePulse()
	if _, err := exec.LookPath("pactl"); err != nil {
		logx.Errorf("capture", "EnsureSink: pactl not found: %v", err)
		return fmt.Errorf("pactl не найден")
	}
	out, err := exec.Command("pactl", "list", "short", "sinks").Output()
	if err != nil {
		logx.Warnf("capture", "EnsureSink: list sinks: %v", err)
	}
	if strings.Contains(string(out), SinkName) {
		logx.Debugf("capture", "EnsureSink: sink %s already present", SinkName)
		return nil
	}
	cmd := exec.Command("pactl", "load-module", "module-null-sink",
		"sink_name="+SinkName,
		"sink_properties=device.description=SSAU")
	b, err := cmd.CombinedOutput()
	if err != nil {
		logx.Errorf("capture", "EnsureSink: null sink: %s: %v", strings.TrimSpace(string(b)), err)
		return fmt.Errorf("null sink: %s: %w", strings.TrimSpace(string(b)), err)
	}
	logx.Infof("capture", "null sink %s loaded", SinkName)
	return nil
}

// EnsureDefaultSink makes ssau_rec the sink new streams land on, so Chromium
// audio reaches its monitor even if it ignores PULSE_SINK.
func EnsureDefaultSink() error {
	logx.Debugf("capture", "EnsureDefaultSink: enter sink=%s", SinkName)
	if _, err := exec.LookPath("pactl"); err != nil {
		logx.Errorf("capture", "EnsureDefaultSink: pactl not found: %v", err)
		return fmt.Errorf("pactl не найден")
	}
	out, err := exec.Command("pactl", "set-default-sink", SinkName).CombinedOutput()
	if err != nil {
		logx.Errorf("capture", "EnsureDefaultSink: set-default-sink %s: %s: %v", SinkName, strings.TrimSpace(string(out)), err)
		return fmt.Errorf("set-default-sink: %s: %w", strings.TrimSpace(string(out)), err)
	}
	logx.Infof("capture", "default sink -> %s", SinkName)
	return nil
}

func FFmpegArgs(outPath string) []string {
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "pulse", "-i", SinkName + ".monitor",
		"-filter_complex", "[0:a]asplit=2[rec][wake]",
		"-map", "[rec]", "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "32k", "-y", outPath,
		"-map", "[wake]", "-ac", "1", "-ar", fmt.Sprintf("%d", WakeRate), "-c:a", "pcm_s16le", "-f", "s16le", "pipe:1",
	}
	logx.Debugf("capture", "FFmpegArgs: out=%s args=%d", outPath, len(args))
	return args
}

func Start(ctx context.Context, outPath string) (*Rec, error) {
	logx.Debugf("capture", "Start: enter out=%s ctx=%v", outPath, ctx != nil)
	if ctx == nil {
		ctx = context.Background()
	}
	if err := EnsureSink(); err != nil {
		logx.Errorf("capture", "Start: ensure sink: %v", err)
		return nil, err
	}
	LogRoute("capture", "sink-ready")
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		logx.Errorf("capture", "Start: mkdir %s: %v", filepath.Dir(outPath), err)
		return nil, fmt.Errorf("Start: mkdir: %w", err)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		logx.Errorf("capture", "Start: ffmpeg not found: %v", err)
		return nil, fmt.Errorf("ffmpeg не найден")
	}
	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, ffmpeg, FFmpegArgs(outPath)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// По умолчанию CommandContext шлёт SIGKILL: ogg не дописывается и недавний
	// звук теряется при каждом рестарте сервиса. SIGINT даёт ffmpeg закрыть файл.
	cmd.Cancel = func() error {
		logx.Infof("capture", "ffmpeg ctx done, SIGINT pid=%d", cmd.Process.Pid)
		return cmd.Process.Signal(syscall.SIGINT)
	}
	cmd.WaitDelay = stopGrace
	cmd.Stderr = os.Stderr
	pcm, err := cmd.StdoutPipe()
	if err != nil {
		logx.Errorf("capture", "Start: stdout pipe: %v", err)
		cancel()
		return nil, fmt.Errorf("ffmpeg stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		logx.Errorf("capture", "Start: ffmpeg start: %v", err)
		cancel()
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}
	logx.Infof("capture", "ffmpeg pid=%d -> %s + pcm %dHz", cmd.Process.Pid, outPath, WakeRate)
	LogRoute("capture", "ffmpeg-started")
	return &Rec{cmd: cmd, cancel: cancel, pcm: pcm, path: outPath}, nil
}

func (r *Rec) Read(p []byte) (int, error) {
	if r == nil || r.pcm == nil {
		logx.Debugf("capture", "Rec.Read: closed r=%v pcm=%v", r == nil, r != nil && r.pcm == nil)
		return 0, io.EOF
	}
	return r.pcm.Read(p)
}

func (r *Rec) Stop() error {
	logx.Debugf("capture", "Rec.Stop: enter")
	if r == nil {
		logx.Debugf("capture", "Rec.Stop: nil rec")
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil || r.cmd.Process == nil {
		logx.Debugf("capture", "Rec.Stop: no process")
		return nil
	}
	pid := r.cmd.Process.Pid
	if err := r.cmd.Process.Signal(syscall.SIGINT); err != nil {
		logx.Warnf("capture", "Rec.Stop: signal pid=%d: %v", pid, err)
	} else {
		logx.Infof("capture", "ffmpeg stop signal pid=%d", pid)
	}
	done := make(chan error, 1)
	go func(cmd *exec.Cmd) { done <- cmd.Wait() }(r.cmd)
	var err error
	select {
	case err = <-done:
	case <-time.After(stopGrace):
		logx.Warnf("capture", "Rec.Stop: ffmpeg pid=%d ignored SIGINT for %s, killing", pid, stopGrace)
		if kerr := r.cmd.Process.Kill(); kerr != nil {
			logx.Warnf("capture", "Rec.Stop: kill pid=%d: %v", pid, kerr)
		}
		err = <-done
	}
	if isInterrupted(err) {
		logx.Debugf("capture", "Rec.Stop: pid=%d exited 255 after SIGINT — норма", pid)
		err = nil
	}
	if err != nil {
		logx.Warnf("capture", "Rec.Stop: wait pid=%d: %v", pid, err)
	}
	if st, serr := os.Stat(r.path); serr == nil {
		logx.Infof("capture", "Rec.Stop: %s size=%d", r.path, st.Size())
	} else {
		logx.Warnf("capture", "Rec.Stop: stat %s: %v", r.path, serr)
	}
	if r.cancel != nil {
		r.cancel()
	}
	r.cmd = nil
	logx.Debugf("capture", "Rec.Stop: exit err=%v", err)
	return err
}

// isInterrupted: ffmpeg exits 255 when it stops on SIGINT; that is a clean stop.
func isInterrupted(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 255
}

func PulseEnv() []string {
	env := append(os.Environ(),
		"PULSE_SINK="+SinkName,
		"PULSE_SOURCE="+SinkName+".monitor",
	)
	logx.Debugf("capture", "PulseEnv: vars=%d sink=%s", len(env), SinkName)
	return env
}

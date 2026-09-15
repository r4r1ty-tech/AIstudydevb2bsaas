package wake

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

type ProcRecognizer struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   chan string
	mu    sync.Mutex
}

func Open(model, script string, vocab []string) (Recognizer, error) {
	model = strings.TrimSpace(model)
	script = strings.TrimSpace(script)
	if model == "" || script == "" {
		return nil, fmt.Errorf("нет vosk model/script")
	}
	if st, err := os.Stat(model); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("vosk model: %s", model)
	}
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("wake.py: %w", err)
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		return nil, fmt.Errorf("python3: %w", err)
	}
	raw, _ := json.Marshal(vocab)
	cmd := exec.Command(py, script, "--model", model, "--vocab", string(raw), "--rate", "16000")
	return startCmd(cmd)
}

func startCmd(cmd *exec.Cmd) (*ProcRecognizer, error) {
	if cmd == nil {
		return nil, fmt.Errorf("nil cmd")
	}
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	ch := make(chan string, 32)
	go scanLines(stdout, ch)
	if cmd.Process != nil {
		logx.Infof("wake", "vosk pid=%d", cmd.Process.Pid)
	}
	return &ProcRecognizer{cmd: cmd, stdin: stdin, out: ch}, nil
}

func scanLines(r io.Reader, ch chan string) {
	defer close(ch)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		select {
		case ch <- line:
		default:
		}
	}
}

func (p *ProcRecognizer) Push(pcm []byte, sampleRate int) []string {
	if p == nil || len(pcm) == 0 {
		return nil
	}
	p.mu.Lock()
	in := p.stdin
	p.mu.Unlock()
	if in == nil {
		return drain(p.out)
	}
	if _, err := in.Write(pcm); err != nil {
		return drain(p.out)
	}
	return drain(p.out)
}

func (p *ProcRecognizer) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	in := p.stdin
	cmd := p.cmd
	p.stdin = nil
	p.cmd = nil
	p.mu.Unlock()
	if in != nil {
		_ = in.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}
	return nil
}

func drain(ch <-chan string) []string {
	if ch == nil {
		return nil
	}
	var out []string
	for {
		select {
		case s, ok := <-ch:
			if !ok {
				return out
			}
			if s != "" {
				out = append(out, s)
			}
		default:
			return out
		}
	}
}

func closeRecognizer(rec Recognizer) {
	if c, ok := rec.(io.Closer); ok {
		_ = c.Close()
	}
}

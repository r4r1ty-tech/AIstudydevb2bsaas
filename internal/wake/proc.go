package wake

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// feedQueue is how many PCM chunks may wait for wake.py (~2s each from
// Engine.Feed). Beyond that chunks are dropped: vosk loading its model or
// lagging must never back up into ffmpeg, which records through the same pipe.
const feedQueue = 30

type ProcRecognizer struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   chan string
	mu    sync.Mutex

	feed    chan []byte
	done    chan struct{}
	closed  bool
	dropped int
}

func Open(model, script string, vocab []string) (Recognizer, error) {
	model = strings.TrimSpace(model)
	script = strings.TrimSpace(script)
	logx.Debugf("wake", "Open: model=%q script=%q vocab=%d", model, script, len(vocab))
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
	// Словарь в wake.py не уходит: он распознаёт всю речь, а слова ищет Match.
	// Заодно фамилии из словаря не светятся в argv.
	cmd := exec.Command(py, script, "--model", model, "--rate", "16000")
	return startCmd(cmd)
}

func startCmd(cmd *exec.Cmd) (*ProcRecognizer, error) {
	if cmd == nil {
		return nil, fmt.Errorf("nil cmd")
	}
	cmd.Stderr = logx.LineWriter(logx.LevelWarn, "wake", "wake.py: ", nil)
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
		// Свободное распознавание ест около половины ядра: запись (ffmpeg) и
		// Chromium важнее, вейкворды подождут.
		if err := syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10); err != nil {
			logx.Debugf("wake", "startCmd: nice pid=%d: %v", cmd.Process.Pid, err)
		}
	}
	p := &ProcRecognizer{
		cmd: cmd, stdin: stdin, out: ch,
		feed: make(chan []byte, feedQueue),
		done: make(chan struct{}),
	}
	go p.writeLoop(stdin)
	return p, nil
}

// writeLoop is the only writer to wake.py stdin. A write error (the script
// died) is logged once; later chunks are discarded until Close.
func (p *ProcRecognizer) writeLoop(w io.Writer) {
	defer close(p.done)
	broken := false
	for pcm := range p.feed {
		if broken {
			continue
		}
		if _, err := w.Write(pcm); err != nil {
			broken = true
			logx.Warnf("wake", "vosk stdin: %v — пейджер молчит до конца записи", err)
		}
	}
	logx.Debugf("wake", "writeLoop: done broken=%v", broken)
}

func scanLines(r io.Reader, ch chan string) {
	defer close(ch)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	lines, lost := 0, 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		lines++
		select {
		case ch <- line:
		default:
			lost++
		}
	}
	// Конец stdout = wake.py завершился (или упал: трейсбек в journald).
	logx.Infof("wake", "vosk stdout closed lines=%d lost=%d err=%v", lines, lost, sc.Err())
}

func (p *ProcRecognizer) Push(pcm []byte, sampleRate int) []string {
	if p == nil || len(pcm) == 0 {
		return nil
	}
	p.mu.Lock()
	if !p.closed && p.feed != nil {
		buf := append([]byte(nil), pcm...)
		select {
		case p.feed <- buf:
		default:
			p.dropped++
			if p.dropped == 1 || p.dropped%100 == 0 {
				logx.Warnf("wake", "vosk lags: dropped %d chunks (queue=%d)", p.dropped, feedQueue)
			}
		}
	}
	p.mu.Unlock()
	return drain(p.out)
}

func (p *ProcRecognizer) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	in := p.stdin
	cmd := p.cmd
	feed := p.feed
	already := p.closed
	p.stdin = nil
	p.cmd = nil
	p.closed = true
	dropped := p.dropped
	p.mu.Unlock()
	if already {
		return nil
	}
	// Closing stdin first unblocks a writeLoop stuck in Write.
	if in != nil {
		_ = in.Close()
	}
	if feed != nil {
		close(feed)
		<-p.done
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		// Python, зависший в нативном коде (загрузка модели), SIGTERM игнорирует —
		// без Kill горутина спотера висит в Wait навсегда.
		waited := make(chan struct{})
		go func() { _ = cmd.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-waited
		}
	}
	logx.Infof("wake", "vosk closed dropped=%d", dropped)
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

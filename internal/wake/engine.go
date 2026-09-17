package wake

import (
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

type Hit struct {
	Word string
}

type Recognizer interface {
	Push(pcm []byte, sampleRate int) []string
}

type Engine struct {
	Rec      Recognizer
	Cooldown time.Duration
	Window   time.Duration
	Now      func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
	buf  []byte
}

func NewEngine() *Engine {
	e := &Engine{
		Rec:      newDefaultRecognizer(),
		Cooldown: 45 * time.Second,
		Window:   2 * time.Second,
		last:     make(map[string]time.Time),
	}
	logx.Debugf("wake", "NewEngine: rec=%T cooldown=%s window=%s", e.Rec, e.Cooldown, e.Window)
	return e
}

func (e *Engine) Feed(pcm []byte, sampleRate int, vocab []string) []Hit {
	logx.Debugf("wake", "Feed: enter pcm=%d bytes rate=%d vocab=%d", len(pcm), sampleRate, len(vocab))
	if e == nil {
		logx.Debugf("wake", "Feed: nil engine")
		return nil
	}
	if sampleRate <= 0 {
		logx.Debugf("wake", "Feed: sampleRate %d <= 0, using 16000", sampleRate)
		sampleRate = 16000
	}

	e.mu.Lock()
	if e.last == nil {
		e.last = make(map[string]time.Time)
	}
	e.buf = append(e.buf, pcm...)
	need := e.needBytes(sampleRate)
	maxBuf := need * 8
	if maxBuf < 16000*2*4 {
		maxBuf = 16000 * 2 * 4
	}
	if len(e.buf) > maxBuf {
		logx.Debugf("wake", "Feed: trimming buf=%d to maxBuf=%d", len(e.buf), maxBuf)
		e.buf = append([]byte(nil), e.buf[len(e.buf)-maxBuf:]...)
	}
	overlap := need / 4
	if overlap%2 != 0 {
		overlap--
	}
	if overlap < 2 {
		overlap = 0
	}
	var chunks [][]byte
	for len(e.buf) >= need {
		chunk := make([]byte, need)
		copy(chunk, e.buf[:need])
		chunks = append(chunks, chunk)
		adv := need - overlap
		if adv < 2 {
			adv = need
		}
		e.buf = append([]byte(nil), e.buf[adv:]...)
	}
	rec := e.Rec
	e.mu.Unlock()
	logx.Debugf("wake", "Feed: need=%d overlap=%d chunks=%d bufLeft=%d", need, overlap, len(chunks), len(e.buf))

	if rec == nil || len(chunks) == 0 || len(vocab) == 0 {
		logx.Debugf("wake", "Feed: no work rec=%v chunks=%d vocab=%d", rec != nil, len(chunks), len(vocab))
		return nil
	}
	if gs, ok := rec.(interface{ SetVocab([]string) }); ok {
		logx.Debugf("wake", "Feed: SetVocab %d words", len(vocab))
		gs.SetVocab(vocab)
	}

	var frags []string
	for _, c := range chunks {
		frags = append(frags, rec.Push(c, sampleRate)...)
	}
	logx.Debugf("wake", "Feed: frags=%d %v", len(frags), frags)

	now := e.now()
	cool := e.cool()
	e.mu.Lock()
	defer e.mu.Unlock()
	var hits []Hit
	for _, frag := range frags {
		for _, w := range Match(frag, vocab) {
			if t, ok := e.last[w]; ok && now.Sub(t) < cool {
				logx.Debugf("wake", "Feed: %q in cooldown %s", w, now.Sub(t))
				continue
			}
			e.last[w] = now
			hits = append(hits, Hit{Word: w})
			logx.Infof("wake", "wake word hit: %q", w)
		}
	}
	logx.Debugf("wake", "Feed: exit hits=%d", len(hits))
	return hits
}

func (e *Engine) now() time.Time {
	if e != nil && e.Now != nil {
		t := e.Now()
		logx.Debugf("wake", "now: injected=%s", t)
		return t
	}
	t := time.Now()
	logx.Debugf("wake", "now: %s", t)
	return t
}

func (e *Engine) cool() time.Duration {
	if e != nil && e.Cooldown > 0 {
		logx.Debugf("wake", "cool: %s", e.Cooldown)
		return e.Cooldown
	}
	logx.Debugf("wake", "cool: default %s", 45*time.Second)
	return 45 * time.Second
}

func (e *Engine) needBytes(sampleRate int) int {
	w := 2 * time.Second
	if e != nil && e.Window > 0 {
		w = e.Window
	}
	n := int(int64(w) * int64(sampleRate) * 2 / int64(time.Second))
	if n < 2 {
		n = 2
	}
	if n%2 != 0 {
		n++
	}
	logx.Debugf("wake", "needBytes: rate=%d window=%s bytes=%d", sampleRate, w, n)
	return n
}

type FakeRecognizer struct {
	Frags []string
	Calls int
}

func (f *FakeRecognizer) Push(pcm []byte, sampleRate int) []string {
	if f == nil || len(pcm) == 0 {
		logx.Debugf("wake", "FakeRecognizer.Push: empty pcm=%d nil=%v", len(pcm), f == nil)
		return nil
	}
	f.Calls++
	logx.Debugf("wake", "FakeRecognizer.Push: call=%d bytes=%d rate=%d frags=%d", f.Calls, len(pcm), sampleRate, len(f.Frags))
	return append([]string(nil), f.Frags...)
}

type noopRecognizer struct{}

func (noopRecognizer) Push(pcm []byte, sampleRate int) []string {
	logx.Debugf("wake", "noopRecognizer.Push: bytes=%d rate=%d", len(pcm), sampleRate)
	return nil
}

func newDefaultRecognizer() Recognizer {
	logx.Debugf("wake", "newDefaultRecognizer: noop")
	return noopRecognizer{}
}

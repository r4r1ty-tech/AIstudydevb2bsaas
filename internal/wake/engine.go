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
	// wake.py — один непрерывный поток KaldiRecognizer: перекрытие окон дало бы
	// ему 4/3 реального времени и повторы кусков. Режем без перекрытия.
	overlap := 0
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

	if rec == nil || len(chunks) == 0 || len(vocab) == 0 {
		return nil
	}
	if gs, ok := rec.(interface{ SetVocab([]string) }); ok {
		gs.SetVocab(vocab)
	}

	var frags []string
	for _, c := range chunks {
		frags = append(frags, rec.Push(c, sampleRate)...)
	}
	if len(frags) > 0 {
		logx.Debugf("wake", "Feed: chunks=%d frags=%d %v", len(chunks), len(frags), frags)
	}

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
	return hits
}

func (e *Engine) now() time.Time {
	if e != nil && e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) cool() time.Duration {
	if e != nil && e.Cooldown > 0 {
		return e.Cooldown
	}
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
	return n
}

type FakeRecognizer struct {
	Frags []string
	Calls int
}

func (f *FakeRecognizer) Push(pcm []byte, sampleRate int) []string {
	if f == nil || len(pcm) == 0 {
		return nil
	}
	f.Calls++
	return append([]string(nil), f.Frags...)
}

type noopRecognizer struct{}

func (noopRecognizer) Push([]byte, int) []string { return nil }

func newDefaultRecognizer() Recognizer {
	return noopRecognizer{}
}

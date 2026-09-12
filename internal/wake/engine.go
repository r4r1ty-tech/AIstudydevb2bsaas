package wake

import (
	"sync"
	"time"
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
	return &Engine{
		Rec:      newDefaultRecognizer(),
		Cooldown: 45 * time.Second,
		Window:   2 * time.Second,
		last:     make(map[string]time.Time),
	}
}

func (e *Engine) Feed(pcm []byte, sampleRate int, vocab []string) []Hit {
	if e == nil {
		return nil
	}
	if sampleRate <= 0 {
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

	now := e.now()
	cool := e.cool()
	e.mu.Lock()
	defer e.mu.Unlock()
	var hits []Hit
	for _, frag := range frags {
		for _, w := range Match(frag, vocab) {
			if t, ok := e.last[w]; ok && now.Sub(t) < cool {
				continue
			}
			e.last[w] = now
			hits = append(hits, Hit{Word: w})
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

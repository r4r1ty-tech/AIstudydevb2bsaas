package capture

import (
	"math"
	"sync"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// SilencePeak is the s16 peak under which a minute counts as silence
// (~ -56 dBFS). A null sink nobody plays into gives exact zeros.
const SilencePeak = 50

// Peak returns the largest absolute s16le sample in pcm (0..32768).
func Peak(pcm []byte) int {
	peak := 0
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	return peak
}

// DBFS converts an s16 peak into dBFS; 0 maps to -inf clamped at -96.
func DBFS(peak int) float64 {
	if peak <= 0 {
		return -96
	}
	return math.Round(20*math.Log10(float64(peak)/32768)*10) / 10
}

// LevelEvent is what a Meter reports when a window of audio closes.
type LevelEvent int

const (
	LevelNone LevelEvent = iota
	// LevelTick: a window closed, nothing noteworthy.
	LevelTick
	// LevelSilent: silence just reached AlertAfter windows in a row.
	LevelSilent
	// LevelBack: sound came back after a LevelSilent alert.
	LevelBack
)

func (e LevelEvent) String() string {
	switch e {
	case LevelTick:
		return "tick"
	case LevelSilent:
		return "silent"
	case LevelBack:
		return "back"
	default:
		return "none"
	}
}

// Meter watches the s16le mono PCM of a recording in fixed windows (a minute
// by default) and says when the recording has been silent for too long, which
// is how an audio join that looked fine but plays nothing shows up.
type Meter struct {
	// WindowBytes is how much PCM makes one window.
	WindowBytes int
	// AlertAfter is how many silent windows in a row raise LevelSilent.
	AlertAfter int

	mu       sync.Mutex
	got      int
	peak     int
	silentN  int
	alerted  bool
	windows  int
	lastPeak int
}

// NewMeter measures windows of `seconds` at sampleRate and alerts after
// alertAfter silent windows in a row.
func NewMeter(sampleRate, seconds, alertAfter int) *Meter {
	if sampleRate <= 0 {
		sampleRate = WakeRate
	}
	if seconds <= 0 {
		seconds = 60
	}
	if alertAfter <= 0 {
		alertAfter = 5
	}
	m := &Meter{WindowBytes: sampleRate * 2 * seconds, AlertAfter: alertAfter}
	logx.Debugf("capture", "NewMeter: rate=%d window=%ds bytes=%d alertAfter=%d", sampleRate, seconds, m.WindowBytes, alertAfter)
	return m
}

// Feed adds PCM and returns the event of the window it closed, if any.
// Only one window closes per call; pass chunks smaller than a window.
func (m *Meter) Feed(pcm []byte) LevelEvent {
	if m == nil || len(pcm) == 0 {
		return LevelNone
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := Peak(pcm); p > m.peak {
		m.peak = p
	}
	m.got += len(pcm)
	if m.WindowBytes <= 0 || m.got < m.WindowBytes {
		return LevelNone
	}
	m.got = 0
	m.windows++
	m.lastPeak = m.peak
	m.peak = 0
	if m.lastPeak < SilencePeak {
		m.silentN++
		if m.silentN == m.AlertAfter && !m.alerted {
			m.alerted = true
			return LevelSilent
		}
		return LevelTick
	}
	m.silentN = 0
	if m.alerted {
		m.alerted = false
		return LevelBack
	}
	return LevelTick
}

// Last returns the peak of the window that closed last, the silent streak and
// how many windows have closed so far.
func (m *Meter) Last() (peak, silentWindows, windows int) {
	if m == nil {
		return 0, 0, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPeak, m.silentN, m.windows
}

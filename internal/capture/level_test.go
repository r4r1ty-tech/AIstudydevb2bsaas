package capture

import (
	"encoding/binary"
	"testing"
)

func pcmOf(samples ...int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

func TestPeak(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		pcm  []byte
		want int
	}{
		{"empty", nil, 0},
		{"odd byte ignored", []byte{0x10}, 0},
		{"silence", pcmOf(0, 0, 0), 0},
		{"positive", pcmOf(1, 300, -2), 300},
		{"negative wins", pcmOf(100, -1200), 1200},
		{"min int16", pcmOf(-32768), 32768},
	}
	for _, c := range cases {
		if got := Peak(c.pcm); got != c.want {
			t.Errorf("%s: Peak = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestDBFS(t *testing.T) {
	t.Parallel()
	if got := DBFS(0); got != -96 {
		t.Fatalf("DBFS(0) = %v", got)
	}
	if got := DBFS(32768); got != 0 {
		t.Fatalf("DBFS(full) = %v", got)
	}
	if got := DBFS(16384); got != -6 {
		t.Fatalf("DBFS(half) = %v", got)
	}
}

func TestLevelEventString(t *testing.T) {
	t.Parallel()
	for ev, want := range map[LevelEvent]string{LevelNone: "none", LevelTick: "tick", LevelSilent: "silent", LevelBack: "back", LevelEvent(99): "none"} {
		if ev.String() != want {
			t.Errorf("%d.String() = %q, want %q", ev, ev.String(), want)
		}
	}
}

func TestNewMeterDefaults(t *testing.T) {
	t.Parallel()
	m := NewMeter(0, 0, 0)
	if m.WindowBytes != WakeRate*2*60 || m.AlertAfter != 5 {
		t.Fatalf("defaults: %+v", m)
	}
}

func TestMeterSilenceAlertAndRecovery(t *testing.T) {
	t.Parallel()
	// 4-byte windows (2 samples), alert after 2 silent windows.
	m := &Meter{WindowBytes: 4, AlertAfter: 2}
	quiet := pcmOf(0, 3)
	loud := pcmOf(0, 5000)

	steps := []struct {
		pcm  []byte
		want LevelEvent
	}{
		{pcmOf(0), LevelNone}, // half a window
		{pcmOf(0), LevelTick}, // window 1 closes, silent streak 1
		{quiet, LevelSilent},  // streak 2 → alert
		{quiet, LevelTick},    // no repeat alert
		{loud, LevelBack},     // sound is back
		{loud, LevelTick},
		{quiet, LevelTick},
		{quiet, LevelSilent}, // alerts again after recovery
	}
	for i, s := range steps {
		if got := m.Feed(s.pcm); got != s.want {
			t.Fatalf("step %d: Feed = %s, want %s", i, got, s.want)
		}
	}
	peak, silent, windows := m.Last()
	if peak != 3 || silent != 2 || windows != 7 {
		t.Fatalf("Last = %d %d %d", peak, silent, windows)
	}
}

func TestMeterNilAndEmpty(t *testing.T) {
	t.Parallel()
	var m *Meter
	if m.Feed(pcmOf(1)) != LevelNone {
		t.Fatal("nil meter must be a no-op")
	}
	if p, s, w := m.Last(); p != 0 || s != 0 || w != 0 {
		t.Fatal("nil Last must be zero")
	}
	if (&Meter{WindowBytes: 4}).Feed(nil) != LevelNone {
		t.Fatal("empty pcm must be a no-op")
	}
}

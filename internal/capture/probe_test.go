package capture

import (
	"testing"
	"time"
)

func TestParseProbe(t *testing.T) {
	out := "Input #0, ogg, from 'a.ogg':\n  Duration: 01:02:03.50, start: 0.005646, bitrate: 31 kb/s\n" +
		"[Parsed_volumedetect_0 @ 0x1] mean_volume: -22.1 dB\n[Parsed_volumedetect_0 @ 0x1] max_volume: -2.3 dB\n"
	dur, peak, err := parseProbe(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Hour + 2*time.Minute + 3500*time.Millisecond; dur != want || peak != -2.3 {
		t.Fatalf("dur=%s peak=%v", dur, peak)
	}
	if _, peak, err := parseProbe("Duration: N/A\nmax_volume: -inf dB\n"); err != nil || peak != -120 {
		t.Fatalf("-inf: peak=%v err=%v", peak, err)
	}
	if _, _, err := parseProbe("Invalid data found when processing input"); err == nil {
		t.Fatal("вывод без max_volume должен быть ошибкой")
	}
}

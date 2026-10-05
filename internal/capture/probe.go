package capture

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

var (
	probeDurRe = regexp.MustCompile(`Duration: (\d+):(\d+):(\d+(?:\.\d+)?)`)
	probeMaxRe = regexp.MustCompile(`max_volume: (-?\d+(?:\.\d+)?|-inf) dB`)
)

// Probe decodes a finished recording and returns its length and peak level
// (dBFS, -inf reported as -120). It tells a real lecture from an empty file
// before the audio is sent to speech-to-text.
func Probe(ctx context.Context, path string) (time.Duration, float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-nostdin", "-i", path,
		"-vn", "-af", "volumedetect", "-f", "null", "-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		logx.Debugf("capture", "Probe: ffmpeg %s: %v", path, err)
		return 0, 0, fmt.Errorf("capture: probe %s: %w", path, err)
	}
	return parseProbe(string(out))
}

func parseProbe(out string) (time.Duration, float64, error) {
	m := probeMaxRe.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, fmt.Errorf("capture: probe: нет max_volume в выводе ffmpeg")
	}
	peak := -120.0
	if m[1] != "-inf" {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, 0, fmt.Errorf("capture: probe: max_volume %q: %w", m[1], err)
		}
		peak = v
	}
	var dur time.Duration
	if d := probeDurRe.FindStringSubmatch(out); d != nil {
		h, _ := strconv.Atoi(d[1])
		mi, _ := strconv.Atoi(d[2])
		s, _ := strconv.ParseFloat(d[3], 64)
		dur = time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(s*float64(time.Second))
	}
	logx.Debugf("capture", "parseProbe: dur=%s peak=%.1f dBFS", dur, peak)
	return dur, peak, nil
}

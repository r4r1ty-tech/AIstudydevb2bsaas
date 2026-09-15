package capture

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const segmentGlob = "audio-*.ogg"

func SegmentPath(dir string, unixNano int64) string {
	return filepath.Join(dir, fmt.Sprintf("audio-%d.ogg", unixNano))
}

func Segments(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, segmentGlob))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// MergeSegments concatenates existing master (if any) plus every audio-*.ogg
// segment in dir into out, then removes the segments. Safe to call repeatedly:
// the master is only replaced after a successful ffmpeg run.
func MergeSegments(ctx context.Context, dir, out string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	segs, err := Segments(dir)
	if err != nil {
		return 0, err
	}
	if len(segs) == 0 {
		return 0, fmt.Errorf("capture: нет сегментов в %s", dir)
	}

	var inputs []string
	if _, err := os.Stat(out); err == nil {
		inputs = append(inputs, out)
	}
	inputs = append(inputs, segs...)

	if len(inputs) == 1 {
		if _, err := os.Stat(out); os.IsNotExist(err) {
			if err := os.Rename(segs[0], out); err == nil {
				return 1, nil
			}
		}
	}

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return len(segs), fmt.Errorf("capture: ffmpeg не найден: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return len(segs), err
	}

	list := filepath.Join(dir, "segments.txt")
	var b strings.Builder
	for _, in := range inputs {
		fmt.Fprintf(&b, "file '%s'\n", filepath.Base(in))
	}
	if err := os.WriteFile(list, []byte(b.String()), 0o600); err != nil {
		return len(segs), err
	}
	defer os.Remove(list)

	tmp := out + ".part"
	if outb, err := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-i", list,
		"-fflags", "+genpts", "-c", "copy", "-f", "ogg", "-y", tmp,
	).CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		return len(segs), fmt.Errorf("capture: concat: %s: %w", strings.TrimSpace(string(outb)), err)
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return len(segs), err
	}
	for _, s := range segs {
		_ = os.Remove(s)
	}
	return len(segs), nil
}

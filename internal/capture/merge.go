package capture

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

const segmentGlob = "audio-*.ogg"

func SegmentPath(dir string, unixNano int64) string {
	p := filepath.Join(dir, fmt.Sprintf("audio-%d.ogg", unixNano))
	logx.Debugf("capture", "SegmentPath: dir=%s unixNano=%d -> %s", dir, unixNano, p)
	return p
}

func Segments(dir string) ([]string, error) {
	logx.Debugf("capture", "Segments: enter dir=%s glob=%s", dir, segmentGlob)
	matches, err := filepath.Glob(filepath.Join(dir, segmentGlob))
	if err != nil {
		logx.Errorf("capture", "Segments: glob %s: %v", dir, err)
		return nil, err
	}
	sort.Strings(matches)
	logx.Debugf("capture", "Segments: exit count=%d %v", len(matches), matches)
	return matches, nil
}

// MergeSegments concatenates existing master (if any) plus every audio-*.ogg
// segment in dir into out, then removes the segments. Safe to call repeatedly:
// the master is only replaced after a successful ffmpeg run.
func MergeSegments(ctx context.Context, dir, out string) (int, error) {
	logx.Debugf("capture", "MergeSegments: enter dir=%s out=%s", dir, out)
	if ctx == nil {
		ctx = context.Background()
	}
	all, err := Segments(dir)
	if err != nil {
		logx.Errorf("capture", "MergeSegments: list segments %s: %v", dir, err)
		return 0, fmt.Errorf("MergeSegments: segments: %w", err)
	}
	// Пустой файл (ffmpeg убит до первого flush) ломает concat навсегда:
	// ffmpeg не может его открыть, и каждая следующая склейка падает.
	segs := dropEmpty(all)
	dropEmpty([]string{out})
	if len(segs) == 0 {
		logx.Errorf("capture", "MergeSegments: no non-empty segments in %s (had %d)", dir, len(all))
		return 0, fmt.Errorf("capture: нет сегментов в %s", dir)
	}

	var inputs []string
	if _, err := os.Stat(out); err == nil {
		logx.Debugf("capture", "MergeSegments: existing master %s", out)
		inputs = append(inputs, out)
	} else if !os.IsNotExist(err) {
		logx.Warnf("capture", "MergeSegments: stat master %s: %v", out, err)
	}
	inputs = append(inputs, segs...)
	logx.Debugf("capture", "MergeSegments: inputs=%d segs=%d", len(inputs), len(segs))

	if len(inputs) == 1 {
		if _, err := os.Stat(out); os.IsNotExist(err) {
			if err := os.Rename(segs[0], out); err == nil {
				logx.Infof("capture", "merge: renamed single segment %s -> %s", segs[0], out)
				return 1, nil
			} else {
				logx.Warnf("capture", "MergeSegments: rename %s -> %s: %v", segs[0], out, err)
			}
		}
	}

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		logx.Errorf("capture", "MergeSegments: ffmpeg not found: %v", err)
		return len(segs), fmt.Errorf("capture: ffmpeg не найден: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		logx.Errorf("capture", "MergeSegments: mkdir %s: %v", filepath.Dir(out), err)
		return len(segs), fmt.Errorf("MergeSegments: mkdir: %w", err)
	}

	list := filepath.Join(dir, "segments.txt")
	var b strings.Builder
	for _, in := range inputs {
		fmt.Fprintf(&b, "file '%s'\n", filepath.Base(in))
	}
	if err := os.WriteFile(list, []byte(b.String()), 0o600); err != nil {
		logx.Errorf("capture", "MergeSegments: write list %s: %v", list, err)
		return len(segs), fmt.Errorf("MergeSegments: write list: %w", err)
	}
	defer func() {
		if err := os.Remove(list); err != nil {
			logx.Warnf("capture", "MergeSegments: remove list %s: %v", list, err)
		}
	}()

	tmp := out + ".part"
	logx.Infof("capture", "ffmpeg concat start: %d inputs -> %s", len(inputs), tmp)
	if outb, err := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-i", list,
		"-fflags", "+genpts", "-c", "copy", "-f", "ogg", "-y", tmp,
	).CombinedOutput(); err != nil {
		logx.Errorf("capture", "MergeSegments: concat: %s: %v", strings.TrimSpace(string(outb)), err)
		if rmErr := os.Remove(tmp); rmErr != nil {
			logx.Warnf("capture", "MergeSegments: remove tmp %s: %v", tmp, rmErr)
		}
		return len(segs), fmt.Errorf("capture: concat: %s: %w", strings.TrimSpace(string(outb)), err)
	}
	logx.Infof("capture", "ffmpeg concat done: %s", tmp)
	if err := os.Rename(tmp, out); err != nil {
		logx.Errorf("capture", "MergeSegments: rename %s -> %s: %v", tmp, out, err)
		if rmErr := os.Remove(tmp); rmErr != nil {
			logx.Warnf("capture", "MergeSegments: remove tmp %s: %v", tmp, rmErr)
		}
		return len(segs), fmt.Errorf("MergeSegments: rename: %w", err)
	}
	for _, s := range segs {
		if err := os.Remove(s); err != nil {
			logx.Warnf("capture", "MergeSegments: remove segment %s: %v", s, err)
		}
	}
	logx.Infof("capture", "merge done: %s (%d segments)", out, len(segs))
	return len(segs), nil
}

// dropEmpty removes zero-length files and returns the rest.
func dropEmpty(paths []string) []string {
	out := paths[:0:0]
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			if !os.IsNotExist(err) {
				logx.Warnf("capture", "dropEmpty: stat %s: %v", p, err)
			}
			continue
		}
		if st.Size() > 0 {
			out = append(out, p)
			continue
		}
		if err := os.Remove(p); err != nil {
			logx.Warnf("capture", "dropEmpty: remove empty %s: %v", p, err)
			continue
		}
		logx.Warnf("capture", "dropEmpty: removed empty %s", p)
	}
	return out
}

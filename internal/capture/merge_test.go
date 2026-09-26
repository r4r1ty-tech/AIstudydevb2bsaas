package capture

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSegmentsList(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"audio-2.ogg", "audio-1.ogg", "other.ogg", "audio.ogg"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	segs, err := Segments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 || filepath.Base(segs[0]) != "audio-1.ogg" || filepath.Base(segs[1]) != "audio-2.ogg" {
		t.Fatalf("segments = %v", segs)
	}
}

func TestMergeNoSegments(t *testing.T) {
	if _, err := MergeSegments(context.Background(), t.TempDir(), filepath.Join(t.TempDir(), "audio.ogg")); err == nil {
		t.Fatal("expected error without segments")
	}
}

func TestMergeSingleRename(t *testing.T) {
	dir := t.TempDir()
	seg := filepath.Join(dir, "audio-1.ogg")
	if err := os.WriteFile(seg, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "audio.ogg")
	n, err := MergeSegments(context.Background(), dir, out)
	if err != nil || n != 1 {
		t.Fatalf("merge: n=%d err=%v", n, err)
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != "payload" {
		t.Fatalf("out = %q err=%v", b, err)
	}
	if _, err := os.Stat(seg); !os.IsNotExist(err) {
		t.Fatal("segment should be removed")
	}
}

func TestMergeWithExistingMasterKeepsBoth(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	makeOgg := func(name string) {
		cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono", "-t", "1",
			"-c:a", "libopus", "-y", filepath.Join(dir, name))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("ffmpeg encode failed (%s): %s", err, out)
		}
	}
	makeOgg("audio.ogg")
	makeOgg("audio-1.ogg")

	out := filepath.Join(dir, "audio.ogg")
	n, err := MergeSegments(context.Background(), dir, out)
	if err != nil || n != 1 {
		t.Fatalf("merge: n=%d err=%v", n, err)
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() == 0 {
		t.Fatalf("out missing/empty: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audio-1.ogg")); !os.IsNotExist(err) {
		t.Fatal("segment should be removed")
	}
}

// Prod 17.09: a SIGKILLed ffmpeg left a 0-byte master and 0-byte segments;
// every later merge then failed on "Impossible to open audio.ogg" forever.
func TestMergeSkipsEmptyMasterAndSegments(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	for _, n := range []string{"audio.ogg", "audio-1.ogg", "audio-3.ogg"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=f=440:r=48000", "-t", "1",
		"-c:a", "libopus", "-y", filepath.Join(dir, "audio-2.ogg"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg encode failed (%s): %s", err, out)
	}
	out := filepath.Join(dir, "audio.ogg")
	if _, err := MergeSegments(context.Background(), dir, out); err != nil {
		t.Fatalf("merge must survive empty files: %v", err)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("out missing/empty: %v", err)
	}
	if segs, _ := Segments(dir); len(segs) != 0 {
		t.Fatalf("segments left behind: %v", segs)
	}
}

func TestMergeOnlyEmptySegmentsErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "audio-1.ogg"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeSegments(context.Background(), dir, filepath.Join(dir, "audio.ogg")); err == nil {
		t.Fatal("expected error: nothing but empty segments")
	}
	if segs, _ := Segments(dir); len(segs) != 0 {
		t.Fatalf("empty segment should be removed: %v", segs)
	}
}

package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPackFileNames(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("rec", "Сети", "лекция-1")
	for got, want := range map[string]string{
		AudioFile(dir):      filepath.Join(dir, "audio.ogg"),
		TranscriptFile(dir): filepath.Join(dir, "transcript.txt"),
		SlidesDir(dir):      filepath.Join(dir, "slides"),
		NotesMD(dir):        filepath.Join(dir, "notes.md"),
		NotesPDF(dir):       filepath.Join(dir, "notes.pdf"),
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if got := Abs("", "Сети", 2); got != filepath.Join("recordings", Rel("Сети", 2)) {
		t.Errorf("Abs default root = %q", got)
	}
	if got := Abs("/r", "Сети", 2); got != filepath.Join("/r", Rel("Сети", 2)) {
		t.Errorf("Abs = %q", got)
	}
}

func TestUnderRootRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	ok, err := UnderRoot(root, "Сети/лекция-1/notes.pdf")
	if err != nil || ok != filepath.Join(root, "Сети/лекция-1/notes.pdf") {
		t.Fatalf("inside: %q %v", ok, err)
	}
	if got, err := UnderRoot(root, "."); err != nil || got != root {
		t.Fatalf("root itself: %q %v", got, err)
	}
	for _, bad := range []string{"../etc/passwd", "a/../../x", "/../../etc"} {
		if _, err := UnderRoot(root, bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	// A sibling that only shares the prefix is outside too.
	if _, err := UnderRoot(root, "../"+filepath.Base(root)+"-evil/x"); err == nil {
		t.Error("prefix sibling must be rejected")
	}
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("recordings", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := UnderRoot("", "a.pdf"); err != nil {
		t.Fatalf("default root: %v", err)
	}
}

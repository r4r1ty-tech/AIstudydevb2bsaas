package notes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func chromeForPDF(t *testing.T) string {
	t.Helper()
	for _, c := range []string{"chromium", "chromium-browser", "google-chrome"} {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	t.Skip("no chromium for print-to-pdf")
	return ""
}

// Build end to end: audio → STT (fake Fish) → LLM (fake) → notes.md → PDF.
func TestBuildWritesTranscriptNotesAndPDF(t *testing.T) {
	bin := chromeForPDF(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/asr"):
			_, _ = w.Write([]byte(`{"text":"сегодня про TCP; контрольная через неделю"}`))
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"## Организационное\nКонтрольная через неделю.\n\n## TCP\nРукопожатие."}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)

	root := t.TempDir()
	p := model.LecturePack{Discipline: "Сети", Number: 3, Dir: "Сети/лекция-3"}
	dir := filepath.Join(root, p.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive.AudioFile(dir), []byte(strings.Repeat("x", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		FishStudioAPIKey: "k", FishStudioAPIURL: api.URL,
		LLMAPIKey: "k", LLMAPIURL: api.URL + "/v1", LLMModel: "m",
		ChromeBin: bin,
	}
	if err := Build(context.Background(), cfg, root, p); err != nil {
		t.Fatal(err)
	}
	tr, _ := os.ReadFile(archive.TranscriptFile(dir))
	if !strings.Contains(string(tr), "контрольная") {
		t.Fatalf("transcript = %q", tr)
	}
	md, _ := os.ReadFile(archive.NotesMD(dir))
	if !strings.HasPrefix(string(md), "## Организационное") {
		t.Fatalf("notes.md = %q", md)
	}
	pdf, err := os.ReadFile(archive.NotesPDF(dir))
	if err != nil || !strings.HasPrefix(string(pdf), "%PDF") {
		t.Fatalf("pdf: %v", err)
	}
}

func TestBuildFailsWithoutAnyInput(t *testing.T) {
	root := t.TempDir()
	p := model.LecturePack{Discipline: "Сети", Number: 1, Dir: "Сети/лекция-1"}
	err := Build(nil, &config.Config{}, root, p) //nolint:staticcheck // nil ctx is handled
	if !errors.Is(err, ErrEmptyAudio) {
		t.Fatalf("Build = %v, want ErrEmptyAudio", err)
	}
}

func TestBuildLLMFailureSurfaces(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"text":"расшифровка"}`))
	}))
	t.Cleanup(api.Close)
	root := t.TempDir()
	p := model.LecturePack{Discipline: "Сети", Number: 1, Dir: "Сети/лекция-1"}
	dir := filepath.Join(root, p.Dir)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(archive.AudioFile(dir), []byte(strings.Repeat("x", 8192)), 0o600)
	cfg := &config.Config{FishStudioAPIKey: "k", FishStudioAPIURL: api.URL}
	if err := Build(context.Background(), cfg, root, p); err == nil || !strings.Contains(err.Error(), "LLM_API_KEY") {
		t.Fatalf("missing LLM key must surface: %v", err)
	}
}

func TestWritePDFErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{ChromeBin: filepath.Join(dir, "no-such-chromium")}
	if err := WritePDF(cfg, "t", "md", filepath.Join(dir, "a.pdf")); err == nil {
		t.Fatal("missing browser must fail")
	}
	if err := WritePDF(cfg, "t", "md", filepath.Join(dir, "missing", "a.pdf")); err == nil {
		t.Fatal("unwritable html must fail")
	}
	fake := filepath.Join(dir, "fake-chrome")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WritePDF(&config.Config{ChromeBin: fake}, "t", "md", filepath.Join(dir, "b.pdf")); err == nil || !strings.Contains(err.Error(), "не записался") {
		t.Fatalf("browser that writes nothing: %v", err)
	}
}

// RECORDINGS_DIR на VDS относительный: file://recordings/... Chromium не открывал.
func TestWritePDFRelativePath(t *testing.T) {
	bin := chromeForPDF(t)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("recordings/x", 0o755); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join("recordings", "x", "notes.pdf")
	if err := WritePDF(&config.Config{ChromeBin: bin}, "Тема", "## Раздел\n\nтекст", pdf); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(pdf); err != nil || st.Size() < 100 {
		t.Fatalf("pdf: %v", err)
	}
}

func TestMarkdownHTMLRenders(t *testing.T) {
	h := markdownHTML("Тема", "## Раздел\n\n**важно** и <script>x</script>\n\n| а | б |\n|---|---|\n| 1 | 2 |\n")
	for _, want := range []string{"<h2", "<strong>важно</strong>", "<table>", "<td>1</td>"} {
		if !strings.Contains(h, want) {
			t.Errorf("нет %q в html", want)
		}
	}
	if strings.Contains(h, "<script>") || strings.Contains(h, "##") || strings.Contains(h, "**") {
		t.Errorf("сырой markdown/html просочился: %s", h)
	}
}

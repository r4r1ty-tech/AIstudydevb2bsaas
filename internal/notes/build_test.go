package notes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// Build end to end: audio → STT (fake Fish) → LLM (fake) → notes.md → PDF.
func TestBuildWritesTranscriptNotesAndPDF(t *testing.T) {
	typstForPDF(t)
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

	oldURL := llm.OpenCodeURL
	llm.OpenCodeURL = api.URL
	t.Cleanup(func() { llm.OpenCodeURL = oldURL })

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
		OpenCodeAPIKey: "k",
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
	if err := Build(context.Background(), cfg, root, p); err == nil || !strings.Contains(err.Error(), "нет ключей ИИ") {
		t.Fatalf("missing LLM key must surface: %v", err)
	}
}

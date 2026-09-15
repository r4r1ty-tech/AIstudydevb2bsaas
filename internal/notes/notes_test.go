package notes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func writeAudio(t *testing.T, dir string, size int) string {
	t.Helper()
	p := filepath.Join(dir, "audio.ogg")
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTranscribeNoConfigOrKey(t *testing.T) {
	if _, err := Transcribe(context.Background(), nil, "x"); err == nil {
		t.Fatal("nil cfg must fail")
	}
	if _, err := Transcribe(context.Background(), &config.Config{}, "x"); err == nil {
		t.Fatal("no key must fail")
	}
}

func TestTranscribeShortAudio(t *testing.T) {
	dir := t.TempDir()
	audio := writeAudio(t, dir, 10)
	_, err := Transcribe(context.Background(), &config.Config{FishStudioAPIKey: "k"}, audio)
	if err == nil || !strings.Contains(err.Error(), "коротк") {
		t.Fatalf("err = %v", err)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("  hi  ", 10); got != "hi" {
		t.Errorf("trim = %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc…" {
		t.Errorf("cut = %q", got)
	}
}

func TestDescribeSlidesNoProviders(t *testing.T) {
	got, err := DescribeSlides(context.Background(), &config.Config{}, t.TempDir())
	if err != nil || got != "" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestDescribeSlidesMissingDir(t *testing.T) {
	cfg := &config.Config{GroqAPIKey: "g"}
	got, err := DescribeSlides(context.Background(), cfg, filepath.Join(t.TempDir(), "nope"))
	if err != nil || got != "" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestDescribeSlidesCallsVision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"текст слайда"}}]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slide1.png"), make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{GroqAPIKey: "g", GroqAPIURL: srv.URL, GroqVisionModel: "m"}
	got, err := DescribeSlides(context.Background(), cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Слайд 1") || !strings.Contains(got, "текст слайда") {
		t.Fatalf("got %q", got)
	}
}

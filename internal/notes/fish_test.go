package notes

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func TestTranscribeFish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/asr" {
			t.Errorf("path %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer k") {
			t.Errorf("auth")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"привет лекция"}`))
	}))
	t.Cleanup(srv.Close)

	audio := filepath.Join(t.TempDir(), "audio.ogg")
	if err := os.WriteFile(audio, []byte(strings.Repeat("x", 4096)), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{FishStudioAPIKey: "k", FishStudioAPIURL: srv.URL}
	text, err := Transcribe(context.Background(), cfg, audio)
	if err != nil {
		t.Fatal(err)
	}
	if text != "привет лекция" {
		t.Fatalf("got %q", text)
	}
}

func TestTranscribeFallsBackToGroq(t *testing.T) {
	fish := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	t.Cleanup(fish.Close)
	groq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"text":"запасной текст"}`))
	}))
	t.Cleanup(groq.Close)

	audio := filepath.Join(t.TempDir(), "audio.ogg")
	if err := os.WriteFile(audio, []byte(strings.Repeat("x", 4096)), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		FishStudioAPIKey: "k",
		FishStudioAPIURL: fish.URL,
		GroqAPIKey:       "gsk",
		GroqAPIURL:       groq.URL,
	}
	text, err := Transcribe(context.Background(), cfg, audio)
	if err != nil {
		t.Fatal(err)
	}
	if text != "запасной текст" {
		t.Fatalf("got %q", text)
	}
}

func TestVisionProvidersGroqFirst(t *testing.T) {
	cfg := &config.Config{
		GroqAPIKey:      "gsk",
		GroqVisionModel: "qwen/qwen3.6-27b",
		GrokAPIKey:      "xai",
	}
	p := visionProviders(cfg)
	if len(p) != 2 || p[0].name != "groq" || p[0].model != "qwen/qwen3.6-27b" || p[1].name != "grok" {
		t.Fatalf("%+v", p)
	}
}

func TestMarkdownHTML(t *testing.T) {
	h := markdownHTML("Тема", "раз & два")
	if !strings.Contains(h, "Тема") || !strings.Contains(h, "раз &amp; два") {
		t.Fatalf("html=%s", h)
	}
}

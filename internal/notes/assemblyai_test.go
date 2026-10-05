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
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func fastAssemblyPoll(t *testing.T) {
	t.Helper()
	old := assemblyPoll
	assemblyPoll = time.Millisecond
	t.Cleanup(func() { assemblyPoll = old })
}

func testAudio(t *testing.T) string {
	t.Helper()
	audio := filepath.Join(t.TempDir(), "audio.ogg")
	if err := os.WriteFile(audio, []byte(strings.Repeat("x", 4096)), 0644); err != nil {
		t.Fatal(err)
	}
	return audio
}

func TestTranscribeAssembly(t *testing.T) {
	fastAssemblyPoll(t)
	polls := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "k" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/upload":
			if len(body) != 4096 {
				t.Errorf("upload bytes %d", len(body))
			}
			_, _ = w.Write([]byte(`{"upload_url":"` + srv.URL + `/file"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v2/transcript":
			if !strings.Contains(string(body), `"language_code":"ru"`) || !strings.Contains(string(body), "/file") {
				t.Errorf("transcript body %s", body)
			}
			_, _ = w.Write([]byte(`{"id":"j1","status":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/transcript/j1":
			polls++
			if polls < 2 {
				_, _ = w.Write([]byte(`{"id":"j1","status":"processing"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"j1","status":"completed","text":" привет лекция "}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{AssemblyAPIKey: "k", AssemblyAPIURL: srv.URL}
	text, err := Transcribe(context.Background(), cfg, testAudio(t))
	if err != nil {
		t.Fatal(err)
	}
	if text != "привет лекция" {
		t.Fatalf("got %q", text)
	}
	if polls != 2 {
		t.Fatalf("polls %d", polls)
	}
}

func TestTranscribeAssemblyErrorFallsBackToFish(t *testing.T) {
	fastAssemblyPoll(t)
	aai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/v2/upload":
			_, _ = w.Write([]byte(`{"upload_url":"u"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"j1","status":"error","error":"bad audio"}`))
		}
	}))
	t.Cleanup(aai.Close)
	fish := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"text":"из fish"}`))
	}))
	t.Cleanup(fish.Close)

	cfg := &config.Config{
		AssemblyAPIKey:   "k",
		AssemblyAPIURL:   aai.URL,
		FishStudioAPIKey: "k",
		FishStudioAPIURL: fish.URL,
	}
	text, err := Transcribe(context.Background(), cfg, testAudio(t))
	if err != nil {
		t.Fatal(err)
	}
	if text != "из fish" {
		t.Fatalf("got %q", text)
	}
}

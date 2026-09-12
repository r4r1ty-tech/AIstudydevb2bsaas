package stt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFishStudioTranscribe(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","text":"Здравствуйте, сегодня лекция по высшей математике, откройте moodle для теста."}`))
	}))
	defer mockServer.Close()

	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "sample.ogg")
	if err := os.WriteFile(audioPath, []byte("fake audio content"), 0644); err != nil {
		t.Fatalf("failed to write fake audio: %v", err)
	}

	client := NewClient("test-key", mockServer.URL)
	text, err := client.Transcribe(context.Background(), audioPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Здравствуйте, сегодня лекция по высшей математике, откройте moodle для теста."
	if text != expected {
		t.Errorf("expected %q, got %q", expected, text)
	}
}

func TestFishStudioMissingKey(t *testing.T) {
	client := NewClient("", "http://example.com")
	_, err := client.Transcribe(context.Background(), "file.ogg")
	if err == nil {
		t.Error("expected error for missing api key")
	}
}

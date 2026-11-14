package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBotNameDoesNotLeakTokenInError(t *testing.T) {
	const secretTok = "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"

	t.Run("transport error", func(t *testing.T) {
		c := &http.Client{}
		// Закрытый адрес гарантирует *url.Error с полным URL запроса.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		base := srv.URL
		srv.Close()

		_, err := botNameWithBase(context.Background(), c, base, secretTok)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if strings.Contains(err.Error(), secretTok) {
			t.Fatalf("error leaked secret token: %v", err)
		}
	})

	t.Run("non-2xx response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Unauthorized "+secretTok, http.StatusUnauthorized)
		}))
		defer srv.Close()

		_, err := botNameWithBase(context.Background(), srv.Client(), srv.URL, secretTok)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if strings.Contains(err.Error(), secretTok) {
			t.Fatalf("error leaked secret token: %v", err)
		}
	})

	t.Run("invalid token chars", func(t *testing.T) {
		_, err := botNameWithBase(context.Background(), http.DefaultClient, "https://example.com", "bad token/with?chars")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if strings.Contains(err.Error(), "bad token") {
			t.Fatalf("error leaked secret token: %v", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/bot"+secretTok+"/getMe" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"username":"test_bot"}}`))
		}))
		defer srv.Close()

		name, err := botNameWithBase(context.Background(), srv.Client(), srv.URL, secretTok)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if name != "test_bot" {
			t.Fatalf("got %q, want %q", name, "test_bot")
		}
	})
}

func TestDoJSONBodyLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":"` + strings.Repeat("a", maxHTTPBodyBytes+10) + `"}`))
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	var dst map[string]any
	err = doJSONWithClient(srv.Client(), req, &dst)
	if err == nil {
		t.Fatal("expected size limit error, got nil")
	}
	if !strings.Contains(err.Error(), "лимит") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpdateEnvFileAtomic0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("FOO=bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := updateEnvFile(path, "FOO", "baz"); err != nil {
		t.Fatalf("update existing: %v", err)
	}
	if err := updateEnvFile(path, "NEW_KEY", "secret"); err != nil {
		t.Fatalf("append key: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 0600", got)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "FOO=baz\nNEW_KEY=secret\n"
	if string(gotBytes) != want {
		t.Fatalf("content = %q, want %q", string(gotBytes), want)
	}

	if err := updateEnvFile(path, "BAD", "line1\nline2"); err == nil {
		t.Fatal("expected error for newline in value")
	}
}

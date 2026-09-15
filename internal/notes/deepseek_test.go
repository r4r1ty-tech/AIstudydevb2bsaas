package notes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func TestChatEndpoint(t *testing.T) {
	cases := map[string]string{
		"":                                     "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com":             "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/":            "https://api.deepseek.com/v1/chat/completions",
		"https://api.cheaperinference.com/v1":  "https://api.cheaperinference.com/v1/chat/completions",
		"https://api.cheaperinference.com/v1/": "https://api.cheaperinference.com/v1/chat/completions",
		"https://host/v1/":                     "https://host/v1/chat/completions",
	}
	for in, want := range cases {
		if got := chatEndpoint(in); got != want {
			t.Errorf("chatEndpoint(%q) = %q want %q", in, got, want)
		}
	}
}

func TestSummarizeOpenAICompatible(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		var req chatReq
		_ = json.Unmarshal(b, &req)
		gotModel = req.Model
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  конспект  "}}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		DeepSeekAPIKey: "ci_live_x",
		DeepSeekAPIURL: srv.URL + "/v1",
		DeepSeekModel:  "deepseek-v4-flash-0731",
	}
	got, err := Summarize(context.Background(), cfg, "Матан", 2, "расшифровка", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "конспект" {
		t.Fatalf("content = %q", got)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer ci_live_x" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotModel != "deepseek-v4-flash-0731" {
		t.Fatalf("model = %q", gotModel)
	}
}

func TestSummarizeWithoutKey(t *testing.T) {
	if _, err := Summarize(context.Background(), &config.Config{}, "x", 1, "t", ""); err == nil {
		t.Fatal("expected error without key")
	}
}

func TestSummarizeEmptyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()
	cfg := &config.Config{DeepSeekAPIKey: "k", DeepSeekAPIURL: srv.URL}
	if _, err := Summarize(context.Background(), cfg, "x", 1, "t", ""); err == nil || !strings.Contains(err.Error(), "пустой") {
		t.Fatalf("err = %v", err)
	}
}

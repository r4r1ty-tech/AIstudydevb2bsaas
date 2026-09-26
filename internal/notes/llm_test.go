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
		"":                                     "",
		"https://api.example.com":              "https://api.example.com/v1/chat/completions",
		"https://api.example.com/":             "https://api.example.com/v1/chat/completions",
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
	var gotPath, gotAuth, gotModel, gotSys string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		var req chatReq
		_ = json.Unmarshal(b, &req)
		gotModel = req.Model
		if len(req.Messages) > 0 {
			gotSys = req.Messages[0].Content
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  конспект  "}}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		LLMAPIKey: "ci_live_x",
		LLMAPIURL: srv.URL + "/v1",
		LLMModel:  "llm-v4-flash-0731",
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
	if gotModel != "llm-v4-flash-0731" {
		t.Fatalf("model = %q", gotModel)
	}
	if !strings.Contains(gotSys, "Организационное") {
		t.Fatalf("system prompt must ask for the operational block first: %q", gotSys)
	}
}

func TestSummarizeRequiresConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"no key", &config.Config{}, "LLM_API_KEY"},
		{"no url", &config.Config{LLMAPIKey: "k"}, "LLM_API_URL"},
		{"no model", &config.Config{LLMAPIKey: "k", LLMAPIURL: "https://host"}, "LLM_MODEL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Summarize(context.Background(), tc.cfg, "x", 1, "t", ""); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestSummarizeEmptyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()
	cfg := &config.Config{LLMAPIKey: "k", LLMAPIURL: srv.URL, LLMModel: "m"}
	if _, err := Summarize(context.Background(), cfg, "x", 1, "t", ""); err == nil || !strings.Contains(err.Error(), "пустой") {
		t.Fatalf("err = %v", err)
	}
}

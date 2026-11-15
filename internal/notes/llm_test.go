package notes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
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
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(b, &req)
		gotModel = req.Model
		if len(req.Messages) > 0 {
			gotSys = req.Messages[0].Content
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  конспект  "}}]}`))
	}))
	defer srv.Close()

	oldURL := llm.OpenCodeURL
	llm.OpenCodeURL = srv.URL
	t.Cleanup(func() { llm.OpenCodeURL = oldURL })

	cfg := &config.Config{
		OpenCodeAPIKey: "oc_live_x",
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
	if gotAuth != "Bearer oc_live_x" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotModel != llm.OpenCodeModels[0] {
		t.Fatalf("model = %q, want %q", gotModel, llm.OpenCodeModels[0])
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
		{"no key", &config.Config{}, "нет ключей ИИ"},
		{"only cheaper key", &config.Config{LLMAPIKey: "k"}, "нет ключей ИИ"},
		{"only cheaper url and model", &config.Config{LLMAPIKey: "k", LLMAPIURL: "https://host", LLMModel: "m"}, "нет ключей ИИ"},
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

	oldURL := llm.OpenCodeURL
	llm.OpenCodeURL = srv.URL
	t.Cleanup(func() { llm.OpenCodeURL = oldURL })

	cfg := &config.Config{OpenCodeAPIKey: "k"}
	if _, err := Summarize(context.Background(), cfg, "x", 1, "t", ""); err == nil || !strings.Contains(err.Error(), "пустой") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheaperForbiddenInNotesPath(t *testing.T) {
	// 1. Прямая проверка ValidateChain
	if err := ValidateChain([]llm.Target{{Provider: llm.Cheaper, Model: "m"}}); !errors.Is(err, ErrCheaperNotAllowed) {
		t.Fatalf("ValidateChain с Cheaper: got %v, want ErrCheaperNotAllowed", err)
	}
	if err := ValidateChain([]llm.Target{{Provider: llm.Gemini, AdminOnly: true}}); !errors.Is(err, ErrCheaperNotAllowed) {
		t.Fatalf("ValidateChain с AdminOnly: got %v, want ErrCheaperNotAllowed", err)
	}
	if err := ValidateChain([]llm.Target{{Provider: llm.Gemini, Model: "m"}}); err != nil {
		t.Fatalf("ValidateChain с Gemini: got %v, want nil", err)
	}

	// 2. AskChain не делает сетевых вызовов при наличии cheaper в цепочке
	called := false
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer badSrv.Close()

	badChain := []llm.Target{
		{Provider: llm.Cheaper, Model: "m"},
	}
	if _, err := AskChain(context.Background(), badChain, "", "тест"); !errors.Is(err, ErrCheaperNotAllowed) {
		t.Fatalf("AskChain с cheaper: got %v, want ErrCheaperNotAllowed", err)
	}
	if called {
		t.Fatal("AskChain выполнил сетевой вызов к cheaper")
	}

	// 3. Ask и Summarize с конфигом, содержащим только LLM_API_KEY (cheaper), не находят ключей пула
	cheaperCfg := &config.Config{
		LLMAPIKey: "adm_key",
		LLMAPIURL: badSrv.URL + "/v1",
		LLMModel:  "glm-5.3-flash",
	}
	if _, err := Ask(context.Background(), cheaperCfg, "", "вопрос"); !errors.Is(err, llm.ErrNoProviders) {
		t.Fatalf("Ask с только cheaper: got %v, want ErrNoProviders", err)
	}
	if _, err := Summarize(context.Background(), cheaperCfg, "Предмет", 1, "текст", ""); err == nil || !strings.Contains(err.Error(), "нет ключей ИИ") {
		t.Fatalf("Summarize с только cheaper: got %v, want 'нет ключей ИИ'", err)
	}
	if called {
		t.Fatal("Ask/Summarize с cheaper конфигом сделали сетевой вызов")
	}
}

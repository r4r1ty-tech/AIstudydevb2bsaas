package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestOpenCodeGoSendsSessionHeader(t *testing.T) {
	var session, ua, model string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("путь запроса %q, ожидали /chat/completions", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer go_test_key" {
			t.Errorf("ключ не передан: %q", r.Header.Get("Authorization"))
		}
		session = r.Header.Get("x-opencode-session")
		ua = r.Header.Get("User-Agent")
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		model = body.Model
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]string{"content": "ok"},
			}},
		})
	}))
	defer srv.Close()

	old := OpenCodeGoURL
	OpenCodeGoURL = srv.URL
	defer func() { OpenCodeGoURL = old }()

	cfg := &config.Config{OpenCodeGoAPIKey: "go_test_key"}
	p, ok := Find(cfg, OpenCodeGo)
	if !ok {
		t.Fatal("провайдер opencode-go не найден в пуле")
	}
	if len(p.Models) == 0 {
		t.Fatal("у opencode-go нет моделей")
	}
	chain := targets(p, p.Models[:1])
	if _, err := Ask(context.Background(), chain, "sys", "user"); err != nil {
		t.Fatal(err)
	}
	if !uuidRe.MatchString(session) {
		t.Fatalf("x-opencode-session = %q, ожидали UUID v4", session)
	}
	if ua == "" || strings.Contains(strings.ToLower(ua), "go-http-client") {
		t.Fatalf("User-Agent = %q: Cloudflare режет пустой/скриптовый UA", ua)
	}
	if model != "deepseek-v4.1-flash" {
		t.Fatalf("модель %q, ожидали deepseek-v4.1-flash", model)
	}
}

func TestOpenCodeGoListModelsSendsSessionHeader(t *testing.T) {
	var session string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("путь запроса %q, ожидали /models", r.URL.Path)
		}
		session = r.Header.Get("x-opencode-session")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{map[string]string{"id": "deepseek-v4.1-flash"}},
		})
	}))
	defer srv.Close()

	old := OpenCodeGoURL
	OpenCodeGoURL = srv.URL
	defer func() { OpenCodeGoURL = old }()

	p, ok := BuiltinProvider(OpenCodeGo)
	if !ok {
		t.Fatal("BuiltinProvider не знает opencode-go")
	}
	models, err := ListModelsWithKey(context.Background(), p, "go_test_key")
	if err != nil {
		t.Fatal(err)
	}
	if !uuidRe.MatchString(session) {
		t.Fatalf("x-opencode-session = %q, ожидали UUID v4", session)
	}
	if len(models) != 1 || models[0] != "deepseek-v4.1-flash" {
		t.Fatalf("модели: %v", models)
	}
}

func TestDeadOpenCodeFreeModelNotInChains(t *testing.T) {
	for _, m := range OpenCodeModels {
		if m == "glm-5.3-flash-free" {
			t.Fatal("мёртвая модель glm-5.3-flash-free осталась в пуле")
		}
	}
	cfg := &config.Config{OpenCodeAPIKey: "zen_key", OpenCodeGoAPIKey: "go_key"}
	for _, chain := range [][]Target{DefaultChain(cfg), PoolChain(cfg), GlobalPoolChain(cfg)} {
		found := false
		for _, tg := range chain {
			if tg.Model == "glm-5.3-flash-free" {
				t.Fatalf("мёртвая модель в цепочке: %v", tg)
			}
			if tg.Provider == OpenCodeGo && tg.Provider != "" {
				found = true
			}
		}
		if !found {
			t.Fatalf("opencode-go не попал в цепочку (%d таргетов)", len(chain))
		}
	}
}

func TestOpenCodeGoKeysCountAsDifferentVotes(t *testing.T) {
	cfg := &config.Config{
		OpenCodeGoAPIKeys: []string{"go1", "go2"},
		GeminiAPIKeys:     []string{"g1"},
	}
	distinct := DistinctKeyTargets(GlobalPoolChain(cfg), 0)
	if len(distinct) < 3 {
		t.Fatalf("ожидали минимум 3 разных ключа, получили %d", len(distinct))
	}
	seen := map[string]bool{}
	for _, tg := range distinct {
		seen[tg.Provider] = true
	}
	if !seen[OpenCodeGo] || !seen[Gemini] {
		t.Fatalf("голоса должны приходить с разных провайдеров/ключей: %v", seen)
	}
}

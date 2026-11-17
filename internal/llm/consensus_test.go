package llm

import (
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func TestGlobalPoolChainIncludesAdminAndSharedKeys(t *testing.T) {
	t.Cleanup(func() { RegisterSharedPoolKeys(nil) })
	RegisterSharedPoolKeys(func(provider string) []Key {
		if provider == Gemini {
			return []Key{{Label: "shared", Secret: "g-shared"}}
		}
		return nil
	})
	cfg := &config.Config{
		GeminiAPIKeys: []string{"a:g-one", "b:g-two"},
		LLMAPIKey:     "admin-key",
		LLMAPIURL:     "https://cheaper.example/v1",
		LLMModel:      "cheap-model",
	}
	chain := GlobalPoolChain(cfg)
	targets := DistinctKeyTargets(chain, 0)
	if len(targets) < 4 {
		t.Fatalf("ожидали минимум 4 разных ключа (2 gemini + shared + админ), получили %d: %+v", len(targets), targets)
	}
	seen := map[string]bool{}
	admin := false
	for _, tg := range targets {
		seen[tg.Provider] = true
		if tg.Provider == Cheaper && tg.AdminOnly {
			admin = true
		}
	}
	if !seen[Gemini] || !admin {
		t.Fatalf("в общем пуле должны быть Gemini и админский ключ: %+v", seen)
	}
}

func TestRouterHubAdminKey(t *testing.T) {
	cfg := &config.Config{
		RouterHubAPIKey: "rh-key",
		RouterHubAPIURL: "https://routerhub.si/v1",
		RouterHubModel:  "glm-5.3",
	}
	targets := DistinctKeyTargets(GlobalPoolChain(cfg), 0)
	if len(targets) != 1 || targets[0].Provider != RouterHub || !targets[0].AdminOnly {
		t.Fatalf("ожидали RouterHub admin-таргет, получили %+v", targets)
	}
	if targets[0].Model != "glm-5.3" {
		t.Fatalf("первой должна быть glm-5.3, получили %q", targets[0].Model)
	}
	p, ok := Find(cfg, RouterHub)
	if !ok || len(p.Models) < 2 || p.Models[1] != "qwen3.8-max" {
		t.Fatalf("ожидали запасную qwen3.8-max, получили %+v", p.Models)
	}
	for _, tg := range DefaultChain(cfg) {
		if tg.Provider == RouterHub {
			t.Fatal("RouterHub не должен попадать в обычную бесплатную цепочку")
		}
	}
	for _, tg := range PoolChain(cfg) {
		if tg.Provider == RouterHub {
			t.Fatal("RouterHub не должен попадать в пул конспектов")
		}
	}
}

func TestGlobalPoolChainAdminOnly(t *testing.T) {
	cfg := &config.Config{
		LLMAPIKey: "admin-key",
		LLMAPIURL: "https://cheaperinference.com/v1",
	}
	targets := DistinctKeyTargets(GlobalPoolChain(cfg), 0)
	if len(targets) != 1 || targets[0].Provider != Cheaper || !targets[0].AdminOnly {
		t.Fatalf("ожидали один админский cheaper-таргет, получили %+v", targets)
	}
}

func TestGlobalPoolChainUsesSharedKeysWithoutEnv(t *testing.T) {
	t.Cleanup(func() { RegisterSharedPoolKeys(nil) })
	RegisterSharedPoolKeys(func(provider string) []Key {
		if provider == Gemini {
			return []Key{{Label: "personal", Secret: "g-personal"}}
		}
		return nil
	})
	chain := GlobalPoolChain(&config.Config{})
	targets := DistinctKeyTargets(chain, 0)
	if len(targets) != 1 || targets[0].Provider != Gemini {
		t.Fatalf("ожидали один Gemini-ключ из общего пула, получили %+v", targets)
	}
}

func TestDistinctKeyTargetsKeepsOnePerSecret(t *testing.T) {
	chain := []Target{
		{Provider: Gemini, Model: "m1", KeyIdx: 0, key: "same"},
		{Provider: Gemini, Model: "m2", KeyIdx: 0, key: "same"},
		{Provider: OpenCode, Model: "m1", KeyIdx: 0, key: "other"},
	}
	got := DistinctKeyTargets(chain, 0)
	if len(got) != 2 {
		t.Fatalf("ожидали 2 разных ключа, получили %d: %+v", len(got), got)
	}
}

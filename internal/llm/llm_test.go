package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func TestEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.x.com":     "https://api.x.com/v1/chat/completions",
		"https://api.x.com/v1/": "https://api.x.com/v1/chat/completions",
		geminiURL:               geminiURL + "/chat/completions",
		"":                      "",
	} {
		if got := ChatEndpoint(in); got != want {
			t.Errorf("ChatEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func cfgAll() *config.Config {
	return &config.Config{
		GeminiAPIKeys:  []string{"Афанасьев:g1", "Костин:g2"},
		OpenCodeAPIKey: "oc",
		LLMAPIKey:      "ch", LLMAPIURL: "https://cheap/v1", LLMModel: "glm-5.3-flash",
	}
}

func TestDefaultChainExcludesCheaper(t *testing.T) {
	chain := DefaultChain(cfgAll())
	if len(chain) != len(GeminiModels)*2+len(OpenCodeModels) {
		t.Fatalf("длина цепочки %d", len(chain))
	}
	if chain[0].Provider != Gemini || chain[0].Model != GeminiModels[0] || chain[1].KeyIdx != 1 {
		t.Fatalf("начало цепочки: %v %v", chain[0], chain[1])
	}
	for _, x := range chain {
		if x.Provider == Cheaper {
			t.Fatal("cheaper попал в общую цепочку")
		}
	}
	// без бесплатных ключей — старый LLM_* как запасной
	only := DefaultChain(&config.Config{LLMAPIKey: "ch", LLMAPIURL: "https://cheap/v1", LLMModel: "m"})
	if len(only) != 1 || only[0].Provider != Cheaper {
		t.Fatalf("без бесплатных: %v", only)
	}
	// cheaperinference: muse первой, LLM_MODEL без повтора; чужой провайдер — один LLM_MODEL
	if got := cheaperModels("glm-5.3-flash", "https://api.cheaperinference.com/v1"); strings.Join(got, ",") != "muse-spark-1.3-contributor,glm-5.3-flash" {
		t.Fatalf("cheaperModels: %v", got)
	}
	if got := cheaperModels("kimi", "https://api.cheaperinference.com/v1"); len(got) != 3 || got[2] != "kimi" {
		t.Fatalf("cheaperModels + свой LLM_MODEL: %v", got)
	}
	if got := cheaperModels("", "https://other/v1"); len(got) != 0 {
		t.Fatalf("чужой провайдер без LLM_MODEL: %v", got)
	}
}

func cfgOwners() *config.Config {
	return &config.Config{
		GeminiAPIKeys:   []string{"Афанасьев:g1", "Агеев:g2", "Костин:g3"},
		OpenCodeAPIKeys: []string{"Иваныч:o1", "Агеев:o2"},
		LLMAPIKey:       "ch", LLMAPIURL: "https://api.cheaperinference.com/v1", LLMModel: "glm-5.3-flash",
	}
}

// Обычный человек — жёстко только свои ключи: выбор первым, потом свои же
// ключи на других моделях. Ни чужих ключей, ни cheaper.
func TestChainForUserOwnKeysOnly(t *testing.T) {
	cfg := cfgOwners()
	got := ChainFor(cfg, Choice{Provider: Gemini, Model: "gemini-3.6-flash", Key: 2}, false, "агеев")
	if len(got) == 0 || got[0].Provider != Gemini || got[0].Model != "gemini-3.6-flash" || got[0].KeyLabel != "Агеев" {
		t.Fatalf("начало: %v", got)
	}
	want := len(GeminiModels) + len(OpenCodeModels) // по ключу Агеева у каждого провайдера
	if len(got) != want {
		t.Fatalf("длина %d, ждал %d: %v", len(got), want, got)
	}
	for _, x := range got {
		if x.KeyLabel != "Агеев" || x.Provider == Cheaper {
			t.Fatalf("чужое в цепочке: %v", x)
		}
	}
	// выбрал чужой ключ — он не попадает, остаются свои
	got = ChainFor(cfg, Choice{Provider: Gemini, Key: 1}, false, "Агеев")
	for _, x := range got {
		if x.KeyLabel != "Агеев" {
			t.Fatalf("чужой ключ: %v", x)
		}
	}
	// cheaper не админу недоступен
	for _, x := range ChainFor(cfg, Choice{Provider: Cheaper}, false, "Агеев") {
		if x.Provider == Cheaper {
			t.Fatal("cheaper у не админа")
		}
	}
	// своих ключей нет — пусто
	if got := ChainFor(cfg, Choice{}, false, "Петров"); len(got) != 0 {
		t.Fatalf("без своих ключей: %v", got)
	}
	if got := ChainFor(cfg, Choice{Provider: Gemini}, false, ""); len(got) != 0 {
		t.Fatalf("без фамилии: %v", got)
	}
}

// Админ: выбор, потом cheaper c muse-spark-1.3-contributor, потом все ключи.
func TestChainForAdminFallback(t *testing.T) {
	cfg := cfgOwners()
	got := ChainFor(cfg, Choice{Provider: Gemini, Model: "gemini-3.6-flash", Key: 3}, true, "Амелин")
	if got[0].Provider != Gemini || got[0].KeyIdx != 2 || got[0].Model != "gemini-3.6-flash" {
		t.Fatalf("выбор: %v", got[0])
	}
	i := 0
	for i < len(got) && got[i].Provider == Gemini && got[i].Model == "gemini-3.6-flash" {
		i++
	}
	if i != 3 || got[i].Provider != Cheaper || got[i].Model != "muse-spark-1.3-contributor" || got[i+1].Model != "glm-5.3-flash" {
		t.Fatalf("после выбора ждал cheaper/muse: %v", got[i:])
	}
	total := len(GeminiModels)*3 + len(OpenCodeModels)*2 + 2
	if len(got) != total {
		t.Fatalf("админ получил не всё: %d из %d", len(got), total)
	}
	// без выбора — сразу cheaper/muse
	if got := ChainFor(cfg, Choice{}, true, ""); got[0].Provider != Cheaper || got[0].Model != "muse-spark-1.3-contributor" {
		t.Fatalf("админ без выбора: %v", got[0])
	}
	// неизвестный провайдер — как без выбора
	if got := ChainFor(cfg, Choice{Provider: "nope"}, true, ""); got[0].Provider != Cheaper {
		t.Fatalf("неизвестный провайдер: %v", got[0])
	}
}

func TestParseKeysAndPublic(t *testing.T) {
	keys := ParseKeys("Афанасьев:AIzaXXXX1111", "AQ.Ab8:tail", " ", "Агеев:AIzaXXXX1111", "oc_sk_abcd")
	if len(keys) != 3 || keys[0].Label != "Афанасьев" || keys[0].Secret != "AIzaXXXX1111" {
		t.Fatalf("keys = %+v", keys)
	}
	if keys[1].Label != "AQ.Ab8" || keys[2].Label != "ключ 3" {
		t.Fatalf("подписи: %+v", keys)
	}
	p := Provider{ID: Gemini, Keys: keys}
	pub := p.Public(false)
	if pub.Keys != nil || len(pub.KeyInfo) != 3 || pub.KeyInfo[0].Tail != "" {
		t.Fatalf("не админ видит хвосты: %+v", pub.KeyInfo)
	}
	if adm := p.Public(true); adm.KeyInfo[0].Tail != "…1111" {
		t.Fatalf("админ: %+v", adm.KeyInfo)
	}
	b, _ := json.Marshal(pub)
	if strings.Contains(string(b), "AIza") {
		t.Fatalf("секрет утёк в JSON: %s", b)
	}
}

func TestAttemptJSONRoundTrip(t *testing.T) {
	a := Attempt{Target: Target{Provider: Gemini, Model: "m", KeyIdx: 1, KeyLabel: "Агеев"}, Took: 1500 * time.Millisecond, Err: "429"}
	b, _ := json.Marshal(a)
	var back Attempt
	if err := json.Unmarshal(b, &back); err != nil || back.Target.KeyLabel != "Агеев" || back.Target.KeyIdx != 1 || back.Took != a.Took || back.Err != "429" {
		t.Fatalf("back=%+v err=%v (%s)", back, err, b)
	}
}

func TestAskFallsThrough(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("User-Agent") == "" || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("запрос: ua=%q path=%s", r.Header.Get("User-Agent"), r.URL.Path)
		}
		var req chatReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Model {
		case "busy":
			http.Error(w, `{"error":"quota for key secret-1"}`, http.StatusTooManyRequests)
		case "empty":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  "}}]}`))
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"готово"}}]}`))
		}
	}))
	defer srv.Close()
	chain := []Target{
		{Provider: "a", Model: "busy", url: srv.URL, key: "secret-1"},
		{Provider: "a", Model: "empty", url: srv.URL, key: "k"},
		{Provider: "b", Model: "good", url: srv.URL, key: "k"},
	}
	res, err := Ask(context.Background(), chain, "sys", "user")
	if err != nil || res.Text != "готово" || res.Used == nil || res.Used.Model != "good" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(res.Attempts) != 3 || res.Attempts[0].Err == "" || strings.Contains(res.Attempts[0].Err, "secret-1") {
		t.Fatalf("попытки: %+v", res.Attempts)
	}
	if calls.Load() != 3 {
		t.Fatalf("вызовов %d", calls.Load())
	}
	if _, err := Ask(context.Background(), chain[:1], "", "x"); err == nil {
		t.Fatal("все упали, а ошибки нет")
	}
	if _, err := Ask(context.Background(), nil, "", "x"); err != ErrNoProviders {
		t.Fatalf("пустая цепочка: %v", err)
	}
}

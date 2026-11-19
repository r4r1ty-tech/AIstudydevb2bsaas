package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestBuildChatPayloadReasoningEffort(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "x"}}
	cases := []struct {
		name   string
		target Target
		effort string
		want   string
	}{
		{"gemini hydrogen получает xhigh", Target{Provider: Gemini, Model: "gemini-4-hydrogen"}, "max", "xhigh"},
		{"gemini flash получает high", Target{Provider: Gemini, Model: "gemini-3.6-flash"}, "max", "high"},
		{"не-gemini получает high", Target{Provider: RouterHub, Model: "glm-5.3"}, "max", "high"},
		{"явный medium", Target{Provider: Gemini, Model: "gemini-3.6-flash"}, "medium", "medium"},
		{"xhigh у flash понижается", Target{Provider: Gemini, Model: "gemini-3.5-flash-lite"}, "xhigh", "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, extra := buildChatPayload(tc.target, msgs, GenOpts{ReasoningEffort: tc.effort})
			if !extra {
				t.Fatal("параметры не добавлены")
			}
			if got := p["reasoning_effort"]; got != tc.want {
				t.Fatalf("reasoning_effort=%v, ожидали %q", got, tc.want)
			}
			if p["model"] != tc.target.Model {
				t.Fatalf("model=%v", p["model"])
			}
			if _, ok := p["messages"]; !ok {
				t.Fatal("messages потерялись")
			}
		})
	}
}

func TestBuildChatPayloadOffAndExtras(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "x"}}
	p, extra := buildChatPayload(Target{Provider: Gemini, Model: "gemini-4-hydrogen"}, msgs, GenOpts{ReasoningEffort: "off"})
	if extra || p["reasoning_effort"] != nil {
		t.Fatalf("off не должен добавлять параметры: %+v", p)
	}

	temp := 0.0
	p, extra = buildChatPayload(Target{Provider: Gemini, Model: "m"}, msgs, GenOpts{Temperature: &temp, MaxTokens: 700, JSONMode: true})
	if !extra {
		t.Fatal("temperature/max_tokens/json не добавлены")
	}
	if p["temperature"] != 0.0 || p["max_tokens"] != 700 {
		t.Fatalf("temperature/max_tokens: %+v", p)
	}
	if _, ok := p["response_format"]; !ok {
		t.Fatal("response_format потерялся")
	}
}

func TestGenOptsFromContextMerge(t *testing.T) {
	t.Setenv("LLM_REASONING_EFFORT", "high")
	t.Setenv("LLM_TEMPERATURE", "0.3")
	t.Setenv("LLM_MAX_TOKENS", "1000")
	resetGenParamsCache()
	t.Cleanup(resetGenParamsCache)

	got := GenOptsFromContext(WithGenOpts(context.Background(), GenOpts{ReasoningEffort: "low"}))
	if got.ReasoningEffort != "low" {
		t.Fatalf("контекст должен переопределять effort, получили %q", got.ReasoningEffort)
	}
	if got.Temperature == nil || *got.Temperature != 0.3 || got.MaxTokens != 1000 {
		t.Fatalf("остальные поля должны прийти из env: %+v", got)
	}

	base := GenOptsFromContext(context.Background())
	if base.ReasoningEffort != "high" || base.MaxTokens != 1000 {
		t.Fatalf("без контекста берём env: %+v", base)
	}
}

// Провайдер, который не знает reasoning_effort: первый запрос с параметрами
// получает 400, повтор без них проходит, дальше параметры не шлются.
func TestGenParamsFallbackOnUnknownField(t *testing.T) {
	resetGenParamsCache()
	t.Cleanup(resetGenParamsCache)

	var calls, withParams atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["reasoning_effort"]; ok {
			withParams.Add(1)
			http.Error(w, `{"error":"unknown field reasoning_effort"}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	chain := []Target{{Provider: "gateway", Model: "m", url: srv.URL, key: "k"}}
	ctx := WithGenOpts(context.Background(), GenOpts{ReasoningEffort: "high"})
	res, err := Ask(ctx, chain, "sys", "user")
	if err != nil || res.Text != "ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if withParams.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("ожидали 1 запрос с параметрами и 1 без, получили with=%d calls=%d", withParams.Load(), calls.Load())
	}
	if _, err := Ask(ctx, chain, "sys", "user"); err != nil {
		t.Fatal(err)
	}
	if withParams.Load() != 1 {
		t.Fatal("после отказа параметры снова ушли")
	}
}

func TestGenParamsRejected(t *testing.T) {
	if !genParamsRejected(http.StatusBadRequest, []byte(`{"error":"unknown field reasoning_effort"}`)) {
		t.Fatal("400 про незнакомое поле — повод повторить без параметров")
	}
	if !genParamsRejected(http.StatusBadRequest, []byte(`{"error":"bad request"}`)) {
		t.Fatal("неконкретный 400 — тоже повторяем без параметров")
	}
	if genParamsRejected(http.StatusUnauthorized, []byte(`unknown reasoning field`)) {
		t.Fatal("401 — проблема ключа, а не параметров")
	}
	if genParamsRejected(http.StatusBadRequest, []byte(`{"error":"API key not valid"}`)) {
		t.Fatal("неверный ключ не должен отключать размышления")
	}
	if genParamsRejected(http.StatusBadRequest, []byte(`{"error":"model gemini-x not found"}`)) {
		t.Fatal("неизвестная модель не должна отключать размышления")
	}
}

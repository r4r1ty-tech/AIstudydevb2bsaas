package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func TestEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.x.com":     "https://api.x.com/v1/chat/completions",
		"https://api.x.com/v1/": "https://api.x.com/v1/chat/completions",
		GeminiURL:               GeminiURL + "/chat/completions",
		"":                      "",
	} {
		if got := ChatEndpoint(in); got != want {
			t.Errorf("ChatEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func cfgAll() *config.Config {
	return &config.Config{
		GeminiAPIKeys:  []string{"Медведев:g1", "Пушкин:g2"},
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
		if x.Provider == Cheaper || x.AdminOnly {
			t.Fatal("cheaper или AdminOnly попал в общую цепочку")
		}
	}
	// без бесплатных ключей — cheaper не попадает в DefaultChain
	only := DefaultChain(&config.Config{LLMAPIKey: "ch", LLMAPIURL: "https://cheap/v1", LLMModel: "m"})
	if len(only) != 0 {
		t.Fatalf("DefaultChain без бесплатных ключей вернул: %v", only)
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

func TestPoolChainExcludesCheaper(t *testing.T) {
	chain := PoolChain(cfgAll())
	if len(chain) != len(GeminiModels)*2+len(OpenCodeModels) {
		t.Fatalf("длина PoolChain %d", len(chain))
	}
	for _, x := range chain {
		if x.Provider != Gemini && x.Provider != OpenCode {
			t.Fatalf("в PoolChain попал посторонний провайдер: %s", x.Provider)
		}
		if x.Provider == Cheaper || x.AdminOnly {
			t.Fatalf("cheaper или AdminOnly попал в PoolChain: %v", x)
		}
	}

	// Когда задан ТОЛЬКО LLM_API_KEY (без бесплатных ключей) — PoolChain пуст
	onlyCheaper := &config.Config{
		LLMAPIKey: "ch_key",
		LLMAPIURL: "https://api.cheaperinference.com/v1",
		LLMModel:  "glm-5.3-flash",
	}
	if got := PoolChain(onlyCheaper); len(got) != 0 {
		t.Fatalf("PoolChain с только cheaper вернул таргеты: %v", got)
	}
	if got := DefaultChain(onlyCheaper); len(got) != 0 {
		t.Fatalf("DefaultChain с только cheaper вернул таргеты: %v", got)
	}
}

func TestPoolChainRandomOrderAndTargetIntegrity(t *testing.T) {
	cfg := &config.Config{
		GeminiAPIKeys: []string{
			"КлючА:sec_a",
			"КлючБ:sec_b",
			"КлючВ:sec_c",
			"КлючГ:sec_d",
		},
		OpenCodeAPIKeys: []string{
			"Zen1:zen_sec_1",
			"Zen2:zen_sec_2",
		},
		LLMAPIKey: "adm:adm_sec",
		LLMAPIURL: "https://api.cheaperinference.com/v1",
	}

	expectedGeminiKeys := []Key{
		{Label: "КлючА", Secret: "sec_a"},
		{Label: "КлючБ", Secret: "sec_b"},
		{Label: "КлючВ", Secret: "sec_c"},
		{Label: "КлючГ", Secret: "sec_d"},
	}

	// Проверяем 50 вызовов: целостность таргетов и случайность порядка ключей
	firstKeys := make(map[string]int)
	const runs = 50

	for run := 0; run < runs; run++ {
		chain := PoolChain(cfg)
		if len(chain) != len(GeminiModels)*4+len(OpenCodeModels)*2 {
			t.Fatalf("длина цепочки: %d", len(chain))
		}

		// Проверка целостности каждого таргета
		for _, target := range chain {
			if target.Provider == Cheaper || target.AdminOnly {
				t.Fatalf("запрещённый таргет в PoolChain: %v", target)
			}
			if target.Provider == Gemini {
				if target.KeyIdx < 0 || target.KeyIdx >= len(expectedGeminiKeys) {
					t.Fatalf("некорректный KeyIdx %d", target.KeyIdx)
				}
				exp := expectedGeminiKeys[target.KeyIdx]
				if target.KeyLabel != exp.Label {
					t.Fatalf("KeyLabel %q != ожидаемого %q для KeyIdx=%d", target.KeyLabel, exp.Label, target.KeyIdx)
				}
				if target.key != exp.Secret {
					t.Fatalf("секрет %q != ожидаемого %q для KeyIdx=%d", target.key, exp.Secret, target.KeyIdx)
				}
				if target.url != GeminiURL {
					t.Fatalf("url %q != GeminiURL %q", target.url, GeminiURL)
				}
			}
		}

		// Запоминаем первый ключ для первой модели Gemini (GeminiModels[0])
		firstTarget := chain[0]
		if firstTarget.Provider == Gemini && firstTarget.Model == GeminiModels[0] {
			firstKeys[firstTarget.KeyLabel]++
		}
	}

	// При 4 ключах и 50 запусках вероятность того, что всегда выпадает один и тот же ключ: (1/4)^49 ~ 0
	if len(firstKeys) < 2 {
		t.Fatalf("порядок ключей в PoolChain не случаен: распределение первых ключей %v", firstKeys)
	}
}

func cfgOwners() *config.Config {
	return &config.Config{
		GeminiAPIKeys:   []string{"Медведев:g1", "Зуев:g2", "Пушкин:g3"},
		OpenCodeAPIKeys: []string{"Семёныч:o1", "Зуев:o2"},
		LLMAPIKey:       "ch", LLMAPIURL: "https://api.cheaperinference.com/v1", LLMModel: "glm-5.3-flash",
	}
}

// Обычный человек — жёстко только свои ключи: выбор первым, потом свои же
// ключи на других моделях. Ни чужих ключей, ни cheaper.
func TestChainForUserOwnKeysOnly(t *testing.T) {
	cfg := cfgOwners()
	got := ChainFor(cfg, Choice{Provider: Gemini, Model: "gemini-3.6-flash", Key: 2}, false, "зуев")
	if len(got) == 0 || got[0].Provider != Gemini || got[0].Model != "gemini-3.6-flash" || got[0].KeyLabel != "Зуев" {
		t.Fatalf("начало: %v", got)
	}
	want := len(GeminiModels) + len(OpenCodeModels) // по ключу Зуева у каждого провайдера
	if len(got) != want {
		t.Fatalf("длина %d, ждал %d: %v", len(got), want, got)
	}
	for _, x := range got {
		if x.KeyLabel != "Зуев" || x.Provider == Cheaper {
			t.Fatalf("чужое в цепочке: %v", x)
		}
	}
	// выбрал чужой ключ — он не попадает, остаются свои
	got = ChainFor(cfg, Choice{Provider: Gemini, Key: 1}, false, "Зуев")
	for _, x := range got {
		if x.KeyLabel != "Зуев" {
			t.Fatalf("чужой ключ: %v", x)
		}
	}
	// cheaper не админу недоступен
	for _, x := range ChainFor(cfg, Choice{Provider: Cheaper}, false, "Зуев") {
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
	got := ChainFor(cfg, Choice{Provider: Gemini, Model: "gemini-3.6-flash", Key: 3}, true, "Белкин")
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
	keys := ParseKeys("Медведев:AIzaXXXX1111", "AQ.Ab8:tail", " ", "Зуев:AIzaXXXX1111", "oc_sk_abcd")
	if len(keys) != 3 || keys[0].Label != "Медведев" || keys[0].Secret != "AIzaXXXX1111" {
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
	a := Attempt{Target: Target{Provider: Gemini, Model: "m", KeyIdx: 1, KeyLabel: "Зуев"}, Took: 1500 * time.Millisecond, Err: "429"}
	b, _ := json.Marshal(a)
	var back Attempt
	if err := json.Unmarshal(b, &back); err != nil || back.Target.KeyLabel != "Зуев" || back.Target.KeyIdx != 1 || back.Took != a.Took || back.Err != "429" {
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

func TestAskUsageAndHook(t *testing.T) {
	t.Cleanup(func() { RegisterUsageHook(nil) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model == "busy" {
			w.Header().Set("Retry-After", "45")
			http.Error(w, `{"error":"rate limit"}`, http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":120,"completion_tokens":35}}`))
	}))
	defer srv.Close()

	type hookEvent struct {
		ownerID  int64
		status   int
		inTok    int
		outTok   int
		cooldown time.Time
	}
	var (
		mu     sync.Mutex
		events []hookEvent
	)
	RegisterUsageHook(func(tgt Target, status int, inTok, outTok int, cooldownUntil time.Time) {
		mu.Lock()
		events = append(events, hookEvent{
			ownerID:  tgt.OwnerID,
			status:   status,
			inTok:    inTok,
			outTok:   outTok,
			cooldown: cooldownUntil,
		})
		mu.Unlock()
	})

	pool := NewKeyPool()
	chain := []Target{
		{Provider: Gemini, Model: "busy", url: srv.URL, key: "k-busy", OwnerID: 11},
		{Provider: Gemini, Model: "good", url: srv.URL, key: "k-good", OwnerID: 22},
	}
	res, err := AskWithPool(context.Background(), pool, chain, "", "hi")
	if err != nil {
		t.Fatalf("AskWithPool: %v", err)
	}
	if res.InputTokens != 120 || res.OutputTokens != 35 {
		t.Fatalf("токены = %d/%d, ждали 120/35", res.InputTokens, res.OutputTokens)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("событий хука = %d, ждали 2: %+v", len(events), events)
	}
	if events[0].status != http.StatusTooManyRequests || events[0].ownerID != 11 || events[0].cooldown.IsZero() {
		t.Fatalf("первое событие (429): %+v", events[0])
	}
	if events[1].status != http.StatusOK || events[1].ownerID != 22 || events[1].inTok != 120 || events[1].outTok != 35 || !events[1].cooldown.IsZero() {
		t.Fatalf("второе событие (200): %+v", events[1])
	}
}

func TestPoolChainSharedKeysDeduplication(t *testing.T) {
	t.Cleanup(func() { RegisterSharedPoolKeys(nil) })

	RegisterSharedPoolKeys(func(provider string) []Key {
		switch provider {
		case Gemini:
			return []Key{
				{Label: "личный", Secret: "g1", OwnerID: 101}, // дубль с .env ключом Медведев:g1
				{Label: "личный", Secret: "g-shared", OwnerID: 202},
			}
		case OpenCode:
			return []Key{
				{Label: "личный", Secret: "oc-shared", OwnerID: 303},
			}
		default:
			return nil
		}
	})

	chain := PoolChain(cfgAll())
	wantLen := len(GeminiModels)*3 + len(OpenCodeModels)*2
	if len(chain) != wantLen {
		t.Fatalf("PoolChain длина = %d, ждали %d", len(chain), wantLen)
	}
	var sawDedupOwner, sawSharedGem, sawSharedOC bool
	for _, tgt := range chain {
		if tgt.Provider == Cheaper || tgt.AdminOnly {
			t.Fatalf("cheaper попал в PoolChain: %+v", tgt)
		}
		if tgt.MatchesSecret("g1") && tgt.OwnerID == 101 {
			sawDedupOwner = true
		}
		if tgt.MatchesSecret("g-shared") && tgt.OwnerID == 202 {
			sawSharedGem = true
		}
		if tgt.MatchesSecret("oc-shared") && tgt.OwnerID == 303 {
			sawSharedOC = true
		}
	}
	if !sawDedupOwner || !sawSharedGem || !sawSharedOC {
		t.Fatalf("флаги shared: dedup=%v gem=%v oc=%v", sawDedupOwner, sawSharedGem, sawSharedOC)
	}
}

func TestListModelsWithKeyAndTestProviderKey(t *testing.T) {
	origURL := GeminiURL
	t.Cleanup(func() { GeminiURL = origURL })

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[{"id":"models/gemini-4-hydrogen"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"работает"}}]}`))
	}))
	defer srv.Close()
	GeminiURL = srv.URL

	p, ok := BuiltinProvider(Gemini)
	if !ok {
		t.Fatal("BuiltinProvider(gemini) == false")
	}
	models, err := ListModelsWithKey(context.Background(), p, "user-secret-123")
	if err != nil || len(models) != 1 || models[0] != "gemini-4-hydrogen" || gotAuth != "Bearer user-secret-123" {
		t.Fatalf("ListModelsWithKey = (%v, %v), auth=%q", models, err, gotAuth)
	}

	res, err := TestProviderKey(context.Background(), &config.Config{}, Gemini, "probe-secret-999")
	if err != nil || res.Text != "работает" || gotAuth != "Bearer probe-secret-999" {
		t.Fatalf("TestProviderKey = (%+v, %v), auth=%q", res, err, gotAuth)
	}

	if _, err := TestProviderKey(context.Background(), cfgAll(), Cheaper, "adm"); err == nil {
		t.Fatal("TestProviderKey не должен разрешать cheaper")
	}
}

func TestProviderKeyIsolatedFromDefaultPoolAndUsageHook(t *testing.T) {
	origURL := GeminiURL
	t.Cleanup(func() {
		GeminiURL = origURL
		RegisterUsageHook(nil)
	})

	var hookCalls atomic.Int32
	RegisterUsageHook(func(Target, int, int, int, time.Time) {
		hookCalls.Add(1)
	})

	cooldownKey := "isolated-probe-cooldown-key-1"
	rateLimitKey := "isolated-probe-429-key-2"

	// Помещаем ключ в cooldown глобального defaultPool — TestProviderKey всё равно должен дойти до сервера.
	defaultPool.SetCooldown(cooldownKey, time.Now().Add(10*time.Minute))

	var gotModels []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		gotModels = append(gotModels, req.Model)
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer "+rateLimitKey {
			w.Header().Set("Retry-After", "120")
			http.Error(w, `{"error":"quota exceeded for `+rateLimitKey+`"}`, http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"работает"}}],"usage":{"prompt_tokens":11,"completion_tokens":4}}`))
	}))
	defer srv.Close()
	GeminiURL = srv.URL

	res, err := TestProviderKey(context.Background(), &config.Config{}, Gemini, cooldownKey)
	if err != nil || res.Text != "работает" {
		t.Fatalf("TestProviderKey при активном cooldown в defaultPool = (%+v, %v)", res, err)
	}
	if _, err := TestProviderKey(context.Background(), &config.Config{}, Gemini, rateLimitKey); err == nil {
		t.Fatal("ожидали ошибку при 429")
	} else if strings.Contains(err.Error(), rateLimitKey) {
		t.Fatalf("секрет утёк в ошибку: %v", err)
	} else if !strings.Contains(err.Error(), "429") {
		t.Fatalf("ожидали упоминание 429 в ошибке, получили: %v", err)
	}

	if _, active := KeyCooldownUntil(rateLimitKey); active {
		t.Fatal("TestProviderKey не должен записывать cooldown в defaultPool")
	}
	if hookCalls.Load() != 0 {
		t.Fatalf("TestProviderKey не должен вызывать usageHook, вызовов=%d", hookCalls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gotModels) != 2 || gotModels[0] != GeminiModels[0] || gotModels[1] != GeminiModels[0] {
		t.Fatalf("TestProviderKey должен проверять p.Models[0], получили: %v", gotModels)
	}
}

func TestChainForWithUserKeysOrderAndIsolation(t *testing.T) {
	cfg := cfgOwners()
	userKeys := map[string]Key{
		Gemini: {Label: "личный", Secret: "db-secret-зуев", OwnerID: 55},
	}
	chain := ChainForWithUserKeys(cfg, Choice{}, false, Owner{Surname: "Зуев"}, userKeys)
	if len(chain) == 0 || !chain[0].MatchesSecret("db-secret-зуев") || chain[0].OwnerID != 55 {
		t.Fatalf("личный ключ из БД должен идти первым: %+v", chain)
	}
	for _, tgt := range chain {
		if tgt.MatchesSecret("g1") || tgt.MatchesSecret("g3") || tgt.MatchesSecret("o1") || tgt.Provider == Cheaper {
			t.Fatalf("чужой ключ попал в цепочку: %+v", tgt)
		}
	}
	// У другого пользователя без личных ключей цепочка пустая.
	if other := ChainForWithUserKeys(cfg, Choice{}, false, Owner{Surname: "Петров"}, nil); len(other) != 0 {
		t.Fatalf("у Петрова не должно быть чужих ключей: %+v", other)
	}
}

// Подпись-фамилия даёт ключ только при заданной фамилии владельца; подпись из
// цифр — точный telegram_id, фамилия её не открывает.
func TestOwnsKeyBySurnameAndID(t *testing.T) {
	bySurname := Key{Label: "Зуев", Secret: "s"}
	byID := Key{Label: "1234567", Secret: "s"}
	if !OwnsKey(bySurname, Owner{Surname: "зуев"}) {
		t.Fatal("фамилия без учёта регистра должна совпасть")
	}
	if OwnsKey(bySurname, Owner{ID: 1234567}) {
		t.Fatal("без фамилии владельца ключ по подписи-фамилии не должен открываться")
	}
	if OwnsKey(bySurname, Owner{}) {
		t.Fatal("пустой владелец не должен получать ключи")
	}
	if !OwnsKey(byID, Owner{ID: 1234567}) {
		t.Fatal("подпись-цифры должна совпасть с telegram_id")
	}
	if OwnsKey(byID, Owner{ID: 7654321, Surname: "1234567"}) {
		t.Fatal("подпись-цифры не должна совпадать по другому id или фамилии")
	}
	if OwnsKey(byID, Owner{Surname: "1234567"}) {
		t.Fatal("подпись-цифры без telegram_id не должна открываться по фамилии")
	}
	if OwnsKey(Key{Label: "ключ 1", Secret: "s"}, Owner{ID: 1}) {
		t.Fatal("безымянный ключ не должен принадлежать никому")
	}
}

// «ё» и «е» в фамилии совпадают; регистр не важен.
func TestOwnsKeyNormalizesYoAndCase(t *testing.T) {
	if !OwnsKey(Key{Label: "Пётр", Secret: "s"}, Owner{Surname: "Петр"}) {
		t.Fatal("«Пётр» должен совпасть с «Петр»")
	}
	if !OwnsKey(Key{Label: "Петр", Secret: "s"}, Owner{Surname: "ПЁТР"}) {
		t.Fatal("«Петр» должен совпасть с «ПЁТР»")
	}
	if OwnsKey(Key{Label: "Пётр", Secret: "s"}, Owner{Surname: "Петров"}) {
		t.Fatal("разные фамилии не должны совпадать")
	}
}

// Package llm — провайдеры ИИ и цепочка с откатом. Все провайдеры — OpenAI-
// совместимый chat/completions. Цепочка перебирает (провайдер, ключ, модель)
// по порядку: 429, 5xx, таймаут или пустой ответ — берём следующий.
//
// Провайдеры:
//   - gemini — Google AI Studio (GEMINI_API_KEYS), бесплатный тариф: основная цепочка всех;
//   - opencode — OpenCode Zen (OPENCODE_API_KEY), бесплатные модели: запасной вариант;
//   - cheaper — LLM_API_* (cheaperinference: muse-spark-1.3-contributor → glm-5.3-flash): только админ.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

const (
	Gemini   = "gemini"
	OpenCode = "opencode"
	Cheaper  = "cheaper"

	geminiURL   = "https://generativelanguage.googleapis.com/v1beta/openai"
	openCodeURL = "https://opencode.ai/zen/v1"

	// userAgent: Zen за Cloudflare режет пустой/скриптовый UA (ошибка 1010).
	userAgent = "ssau-bot/1.0 (+https://ssau.r4r1ty.fun)"
)

// Модели по умолчанию, от лучшей к самой быстрой. Проверено 2026-11-14 на
// бесплатном тарифе: hydrogen решает лучше всех (~1 мин), 3.6-flash ~7 с,
// 3.5-flash-lite ~2 с; argon/carbon/pro на бесплатном тарифе отвечают 429.
var (
	GeminiModels = []string{"gemini-4-hydrogen", "gemini-3.6-flash", "gemini-3.5-flash-lite"}
	// Лимит бесплатных моделей Zen общий на аккаунт (FreeUsageLimitError,
	// Retry-After ~9 ч), а не на модель: запасные модели после 429 только
	// жгут запросы. Одна модель — та, что обычно отвечает.
	OpenCodeModels = []string{"glm-5.3-flash-free"}
	// CheaperModels — cheaperinference (только админ): обе дешёвые, умные и
	// видят картинки. Проверено 2026-11-14 на Шенноне-Фано и таблице кода с
	// картинки — обе верно. muse-spark-1.3-contributor ($0.085/$0.17 за 1M)
	// — первая, glm-5.3-flash ($0.03/$0.053) — следом.
	CheaperModels = []string{"muse-spark-1.3-contributor", "glm-5.3-flash"}
)

// cheaperModels — для cheaperinference CheaperModels и LLM_MODEL в конце,
// если его там нет; у другого провайдера за LLM_API_URL — один LLM_MODEL.
func cheaperModels(llmModel, url string) []string {
	var list []string
	if strings.Contains(strings.ToLower(url), "cheaperinference") {
		list = append(list, CheaperModels...)
	}
	list = append(list, strings.TrimSpace(llmModel))
	var out []string
	seen := map[string]bool{}
	for _, m := range list {
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// Key — ключ и подпись владельца («Агеев»). В env: «Подпись:ключ».
type Key struct {
	Label  string
	Secret string
}

// ParseKeys разбирает «Подпись:ключ» (подпись необязательна, без пробелов
// в ключе). Без подписи — «ключ N».
func ParseKeys(raw ...string) []Key {
	var out []Key
	seen := map[string]bool{}
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		label, secret := "", r
		if i := strings.Index(r, ":"); i > 0 && !strings.ContainsAny(r[:i], "/") {
			label, secret = strings.TrimSpace(r[:i]), strings.TrimSpace(r[i+1:])
		}
		if secret == "" || seen[secret] {
			continue
		}
		seen[secret] = true
		if label == "" {
			label = fmt.Sprintf("ключ %d", len(out)+1)
		}
		out = append(out, Key{Label: label, Secret: secret})
	}
	return out
}

// KeyInfo — что о ключе видно в панели. Хвост ключа — только админу.
type KeyInfo struct {
	N     int    `json:"n"`
	Label string `json:"label"`
	Tail  string `json:"tail,omitempty"`
}

// Provider — откуда брать ответы.
type Provider struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"-"`
	Keys      []Key     `json:"-"`
	KeyInfo   []KeyInfo `json:"keys"`
	Models    []string  `json:"models"`
	AdminOnly bool      `json:"admin_only"`
}

// Public — копия для панели: ключи без секретов, хвосты только админу.
func (p Provider) Public(admin bool) Provider {
	p.KeyInfo = make([]KeyInfo, 0, len(p.Keys))
	for i, k := range p.Keys {
		ki := KeyInfo{N: i + 1, Label: k.Label}
		if admin && len(k.Secret) > 4 {
			ki.Tail = "…" + k.Secret[len(k.Secret)-4:]
		}
		p.KeyInfo = append(p.KeyInfo, ki)
	}
	p.Keys = nil
	return p
}

// Target — один вызов: провайдер, номер ключа, модель.
type Target struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	KeyIdx   int    `json:"key"`
	KeyLabel string `json:"key_label"`
	url      string
	key      string
}

func (t Target) String() string {
	return fmt.Sprintf("%s/%s#%d(%s)", t.Provider, t.Model, t.KeyIdx+1, t.KeyLabel)
}

// Choice — выбор человека: провайдер, модель и ключ (номер с 1; 0 — любой).
// Пустой провайдер — общая цепочка.
type Choice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Key      int    `json:"key,omitempty"`
}

// Providers — все настроенные провайдеры.
func Providers(cfg *config.Config) []Provider {
	if cfg == nil {
		return nil
	}
	var out []Provider
	if keys := ParseKeys(cfg.GeminiAPIKeys...); len(keys) > 0 {
		out = append(out, Provider{ID: Gemini, Name: "Google AI Studio (бесплатно)", URL: geminiURL, Keys: keys, Models: GeminiModels})
	}
	if keys := ParseKeys(append(append([]string{}, cfg.OpenCodeAPIKeys...), cfg.OpenCodeAPIKey)...); len(keys) > 0 {
		out = append(out, Provider{ID: OpenCode, Name: "OpenCode Zen (бесплатные модели)", URL: openCodeURL, Keys: keys, Models: OpenCodeModels})
	}
	if k := strings.TrimSpace(cfg.LLMAPIKey); k != "" && strings.TrimSpace(cfg.LLMAPIURL) != "" {
		models := cheaperModels(cfg.LLMModel, cfg.LLMAPIURL)
		out = append(out, Provider{ID: Cheaper, Name: "Ключ админа LLM_API_* (только админ)", URL: cfg.LLMAPIURL,
			Keys: []Key{{Label: "админ", Secret: k}}, Models: models, AdminOnly: true})
	}
	return out
}

// Find — провайдер по id.
func Find(cfg *config.Config, id string) (Provider, bool) {
	for _, p := range Providers(cfg) {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

func target(p Provider, m string, i int) Target {
	return Target{Provider: p.ID, Model: m, KeyIdx: i, KeyLabel: p.Keys[i].Label, url: p.URL, key: p.Keys[i].Secret}
}

func targets(p Provider, models []string) []Target {
	var out []Target
	for _, m := range models {
		for i := range p.Keys {
			out = append(out, target(p, m, i))
		}
	}
	return out
}

// keyFirst — все ключи провайдера для модели, выбранный ключ (1..n) первым.
func keyFirst(p Provider, model string, key int) []Target {
	var out []Target
	if key >= 1 && key <= len(p.Keys) {
		out = append(out, target(p, model, key-1))
	}
	for i := range p.Keys {
		if i != key-1 {
			out = append(out, target(p, model, i))
		}
	}
	return out
}

// DefaultChain — общая бесплатная цепочка: gemini по всем моделям и ключам,
// потом opencode. cheaper сюда не входит, кроме случая, когда больше ничего
// не настроено (локальная разработка со старым .env).
func DefaultChain(cfg *config.Config) []Target {
	var out []Target
	var cheaper *Provider
	for _, p := range Providers(cfg) {
		if p.AdminOnly {
			pp := p
			cheaper = &pp
			continue
		}
		out = append(out, targets(p, p.Models)...)
	}
	if len(out) == 0 && cheaper != nil {
		out = targets(*cheaper, cheaper.Models)
	}
	return out
}

// OwnsKey — ключ подписан этим человеком (подпись = фамилия, без регистра).
func OwnsKey(k Key, owner string) bool {
	owner = strings.TrimSpace(owner)
	return owner != "" && strings.EqualFold(strings.TrimSpace(k.Label), owner)
}

// ChainFor — цепочка для тестов человека.
//
// Не админ — жёстко только свои ключи (подпись ключа = owner, его фамилия):
// выбранные модель и ключ первыми, потом его же ключи на остальных моделях.
// Чужих ключей и cheaper нет; своих ключей нет — пустая цепочка.
//
// Админ — его выбор первым, а если не ответил: cheaper (muse-spark-1.3-
// contributor первой), потом все ключи всех моделей.
func ChainFor(cfg *config.Config, ch Choice, admin bool, owner string) []Target {
	var out []Target
	seen := map[string]bool{}
	add := func(ts ...Target) {
		for _, t := range ts {
			k := fmt.Sprintf("%s|%s|%d", t.Provider, t.Model, t.KeyIdx)
			if !seen[k] {
				seen[k] = true
				out = append(out, t)
			}
		}
	}
	provs := Providers(cfg)

	// выбор человека
	if p, ok := Find(cfg, ch.Provider); ok && (admin || !p.AdminOnly) {
		model := strings.TrimSpace(ch.Model)
		if model == "" && len(p.Models) > 0 {
			model = p.Models[0]
		}
		if model != "" {
			for _, t := range keyFirst(p, model, ch.Key) {
				if admin || OwnsKey(p.Keys[t.KeyIdx], owner) {
					add(t)
				}
			}
		}
	}

	if !admin {
		for _, p := range provs {
			if p.AdminOnly {
				continue
			}
			for _, m := range p.Models {
				for i, k := range p.Keys {
					if OwnsKey(k, owner) {
						add(target(p, m, i))
					}
				}
			}
		}
		return out
	}

	for _, p := range provs {
		if p.AdminOnly {
			add(targets(p, p.Models)...)
		}
	}
	for _, p := range provs {
		if !p.AdminOnly {
			add(targets(p, p.Models)...)
		}
	}
	return out
}

// Attempt — как прошёл один вызов (для отчёта и журнала отладки).
type Attempt struct {
	Target Target        `json:"target"`
	Took   time.Duration `json:"took_ms"`
	Err    string        `json:"error,omitempty"`
}

func (a Attempt) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Key      int    `json:"key"`
		Label    string `json:"key_label"`
		TookMS   int64  `json:"took_ms"`
		Err      string `json:"error,omitempty"`
	}{a.Target.Provider, a.Target.Model, a.Target.KeyIdx + 1, a.Target.KeyLabel, a.Took.Milliseconds(), a.Err})
}

func (a *Attempt) UnmarshalJSON(b []byte) error {
	var v struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Key      int    `json:"key"`
		Label    string `json:"key_label"`
		TookMS   int64  `json:"took_ms"`
		Err      string `json:"error"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	a.Target = Target{Provider: v.Provider, Model: v.Model, KeyIdx: v.Key - 1, KeyLabel: v.Label}
	a.Took = time.Duration(v.TookMS) * time.Millisecond
	a.Err = v.Err
	return nil
}

// Result — ответ и путь к нему.
type Result struct {
	Text     string    `json:"text"`
	Used     *Target   `json:"used,omitempty"`
	Attempts []Attempt `json:"attempts"`
}

// ErrNoProviders — ни одного ключа не настроено.
var ErrNoProviders = errors.New("llm: не настроено ни одного провайдера")

// PerCallTimeout — сколько ждём один вызов; hydrogen думает до минуты.
var PerCallTimeout = 4 * time.Minute

var httpClient = &http.Client{}

// Ask идёт по цепочке до первого непустого ответа.
func Ask(ctx context.Context, chain []Target, sysPrompt, userPrompt string) (Result, error) {
	var res Result
	if len(chain) == 0 {
		return res, ErrNoProviders
	}
	var last error
	for _, t := range chain {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		start := time.Now()
		text, err := call(ctx, t, sysPrompt, userPrompt)
		a := Attempt{Target: t, Took: time.Since(start)}
		if err != nil {
			a.Err = err.Error()
			res.Attempts = append(res.Attempts, a)
			logx.Warnf("llm", "%s: %v (%s)", t, err, a.Took.Round(time.Millisecond))
			last = err
			continue
		}
		res.Attempts = append(res.Attempts, a)
		tt := t
		res.Used = &tt
		res.Text = text
		logx.Infof("llm", "%s: ok %d симв. за %s", t, len(text), a.Took.Round(time.Millisecond))
		return res, nil
	}
	return res, fmt.Errorf("llm: все %d вариантов не ответили, последний: %w", len(chain), last)
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func call(ctx context.Context, t Target, sysPrompt, userPrompt string) (string, error) {
	msgs := []chatMessage{}
	if strings.TrimSpace(sysPrompt) != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: sysPrompt})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: userPrompt})
	payload, err := json.Marshal(chatReq{Model: t.Model, Messages: msgs})
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, PerCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, ChatEndpoint(t.url), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+t.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", scrub(err.Error(), t.key)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("ответ оборвался: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", scrub(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, short(string(b), 300)), t.key)
	}
	var out chatResp
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("не JSON (%d байт): %w", len(b), err)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", errors.New("пустой ответ")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// ListModels — живой список моделей провайдера (GET /models), первым ключом.
func ListModels(ctx context.Context, p Provider) ([]string, error) {
	if len(p.Keys) == 0 {
		return nil, errors.New("нет ключа")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, modelsEndpoint(p.URL), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Keys[0].Secret)
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, scrub(err.Error(), p.Keys[0].Secret)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return nil, scrub(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, short(string(b), 200)), p.Keys[0].Secret)
	}
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(r.Data))
	for _, m := range r.Data {
		out = append(out, strings.TrimPrefix(m.ID, "models/"))
	}
	return out, nil
}

// Test — проверка выбора («Проверить»): ключ задан — только он, иначе ключи
// по очереди до первого ответа.
func Test(ctx context.Context, cfg *config.Config, ch Choice) (Result, error) {
	p, ok := Find(cfg, ch.Provider)
	if !ok {
		return Result{}, fmt.Errorf("нет провайдера %q", ch.Provider)
	}
	model := strings.TrimSpace(ch.Model)
	if model == "" && len(p.Models) > 0 {
		model = p.Models[0]
	}
	chain := keyFirst(p, model, ch.Key)
	if ch.Key >= 1 && ch.Key <= len(p.Keys) {
		chain = chain[:1]
	}
	return Ask(ctx, chain, "", "Ответь одним словом: работает")
}

// ChatEndpoint принимает base URL с /v1, без него и Google /v1beta/openai.
func ChatEndpoint(base string) string { return endpoint(base, "chat/completions") }

func modelsEndpoint(base string) string { return endpoint(base, "models") }

func endpoint(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/v1") || strings.HasSuffix(base, "/openai") {
		return base + "/" + path
	}
	return base + "/v1/" + path
}

func short(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// scrub — ключ не должен попасть в лог или в панель даже из текста ошибки.
func scrub(msg, key string) error {
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "***")
	}
	return errors.New(msg)
}

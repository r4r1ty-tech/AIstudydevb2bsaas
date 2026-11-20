// Package llm — провайдеры ИИ и цепочка с откатом. Все провайдеры — OpenAI-
// совместимый chat/completions. Цепочка перебирает (провайдер, ключ, модель)
// по порядку: 429, 5xx, таймаут или пустой ответ — берём следующий.
//
// Провайдеры:
//   - gemini — Google AI Studio (GEMINI_API_KEYS), бесплатный тариф: основная цепочка всех;
//   - opencode-go — OpenCode Go (OPENCODE_GO_API_KEY): deepseek/glm, платный шлюз;
//   - opencode — OpenCode Zen (OPENCODE_API_KEY): бесплатная модель недоступна, в цепочках не участвует;
//   - cheaper — LLM_API_* (cheaperinference: muse-spark-1.3-contributor → glm-5.3-flash): только админ.
package llm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

const (
	Gemini     = "gemini"
	OpenCode   = "opencode"
	OpenCodeGo = "opencode-go"
	Cheaper    = "cheaper"
	RouterHub  = "routerhub"

	// userAgent: Zen за Cloudflare режет пустой/скриптовый UA (ошибка 1010).
	userAgent = "ssau-bot/1.0 (+https://github.com/r4r1ty-tech/AIstudydevb2bsaas)"
)

var (
	// GeminiURL и OpenCodeURL — базовые адреса API провайдеров пула.
	// Переменные пакета позволяют перенаправлять запросы на тестовый сервер в тестах.
	GeminiURL     = "https://generativelanguage.googleapis.com/v1beta/openai"
	OpenCodeURL   = "https://opencode.ai/zen/v1"
	OpenCodeGoURL = "https://opencode.ai/zen/go/v1"
	RouterHubURL  = "https://routerhub.si/v1"
)

// openCodeSession — UUID сессии для x-opencode-session: OpenCode Go без него
// отвечает 400 {"type":"MissingSessionID"}. Один на процесс, значение любое.
var openCodeSession = newOpenCodeSession()

func newOpenCodeSession() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x-0000-4000-8000-%012x", time.Now().Unix(), time.Now().UnixNano()&0xffffffffffff)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // версия 4
	b[8] = (b[8] & 0x3f) | 0x80 // вариант RFC 4122
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// setCommonHeaders — заголовки, общие для chat/completions и /models.
func setCommonHeaders(req *http.Request, provider string) {
	req.Header.Set("User-Agent", userAgent)
	if provider == OpenCodeGo {
		req.Header.Set("x-opencode-session", openCodeSession)
	}
}

// Модели по умолчанию, от лучшей к самой быстрой. Проверено 2026-11-14 на
// бесплатном тарифе: hydrogen решает лучше всех (~1 мин), 3.6-flash ~7 с,
// 3.5-flash-lite ~2 с; argon/carbon/pro на бесплатном тарифе отвечают 429.
var (
	GeminiModels = []string{"gemini-4-hydrogen", "gemini-3.6-flash", "gemini-3.5-flash-lite"}
	// Бесплатная модель OpenCode Zen (glm-5.3-flash-free) отдаёт HTTP 400
	// «Model is unavailable» на всех ключах, поэтому её в пуле нет: провайдер
	// Zen остаётся в панели, но цепочки он не пополняет.
	OpenCodeModels = []string{}
	// OpenCode Go — OpenAI-совместимый шлюз (x-opencode-session обязателен).
	// Модели проверены с прода 2026-11-20: все три отвечают 200.
	OpenCodeGoModels = []string{"deepseek-v4.1-flash", "glm-5.3", "glm-5.3-flash"}
	// CheaperModels — cheaperinference (только админ): обе дешёвые, умные и
	// видят картинки. Проверено 2026-11-14 на Шенноне-Фано и таблице кода с
	// картинки — обе верно. muse-spark-1.3-contributor ($0.085/$0.17 за 1M)
	// — первая, glm-5.3-flash ($0.03/$0.053) — следом.
	CheaperModels = []string{"muse-spark-1.3-contributor", "glm-5.3-flash"}
	// RouterHub — платный ключ админа. GLM 5.3 ($0.15/$0.47 за 1M) — средний
	// баланс цена/качество; Qwen 3.8 Max ($0.20/$0.60) — запасной вариант.
	RouterHubModels = []string{"glm-5.3", "qwen3.8-max"}
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

// Key — ключ и подпись владельца («Зуев»). В env: «Подпись:ключ».
type Key struct {
	Label   string
	Secret  string
	OwnerID int64 `json:"-"`
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
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	KeyIdx    int    `json:"key"`
	KeyLabel  string `json:"key_label"`
	AdminOnly bool   `json:"admin_only,omitempty"`
	OwnerID   int64  `json:"-"`
	url       string
	key       string
}

// MatchesSecret проверяет, совпадает ли секрет таргета с переданным ключом, не раскрывая секрет наружу.
func (t Target) MatchesSecret(secret string) bool {
	return secret != "" && t.key == secret
}

// SecretHash возвращает SHA-256 от секрета таргета без раскрытия самого ключа.
func (t Target) SecretHash() ([32]byte, bool) {
	if t.key == "" {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(t.key)), true
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
		out = append(out, Provider{ID: Gemini, Name: "Google AI Studio (бесплатно)", URL: GeminiURL, Keys: keys, Models: GeminiModels})
	}
	if keys := ParseKeys(append(append([]string{}, cfg.OpenCodeAPIKeys...), cfg.OpenCodeAPIKey)...); len(keys) > 0 {
		out = append(out, Provider{ID: OpenCode, Name: "OpenCode Zen (бесплатные модели)", URL: OpenCodeURL, Keys: keys, Models: OpenCodeModels})
	}
	if keys := ParseKeys(append(append([]string{}, cfg.OpenCodeGoAPIKeys...), cfg.OpenCodeGoAPIKey)...); len(keys) > 0 {
		out = append(out, Provider{ID: OpenCodeGo, Name: "OpenCode Go", URL: OpenCodeGoURL, Keys: keys, Models: OpenCodeGoModels})
	}
	if k := strings.TrimSpace(cfg.LLMAPIKey); k != "" && strings.TrimSpace(cfg.LLMAPIURL) != "" {
		models := cheaperModels(cfg.LLMModel, cfg.LLMAPIURL)
		out = append(out, Provider{ID: Cheaper, Name: "Ключ админа LLM_API_* (только админ)", URL: cfg.LLMAPIURL,
			Keys: []Key{{Label: "админ", Secret: k}}, Models: models, AdminOnly: true})
	}
	if k := strings.TrimSpace(cfg.RouterHubAPIKey); k != "" {
		url := strings.TrimRight(strings.TrimSpace(cfg.RouterHubAPIURL), "/")
		if url == "" {
			url = RouterHubURL
		}
		out = append(out, Provider{ID: RouterHub, Name: "RouterHub (ключ админа)", URL: url,
			Keys: []Key{{Label: "админ RouterHub", Secret: k}}, Models: routerHubModels(cfg.RouterHubModel), AdminOnly: true})
	}
	return out
}

// routerHubModels ставит выбранную в env модель первой, затем запасные.
func routerHubModels(model string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(m string) {
		if m = strings.TrimSpace(m); m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	add(model)
	for _, m := range RouterHubModels {
		add(m)
	}
	return out
}

// BuiltinProvider возвращает метаданные бесплатного провайдера пула (Gemini или OpenCode) без ключей.
func BuiltinProvider(id string) (Provider, bool) {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case Gemini:
		return Provider{ID: Gemini, Name: "Google AI Studio (бесплатно)", URL: GeminiURL, Models: GeminiModels}, true
	case OpenCode:
		return Provider{ID: OpenCode, Name: "OpenCode Zen (бесплатные модели)", URL: OpenCodeURL, Models: OpenCodeModels}, true
	case OpenCodeGo:
		return Provider{ID: OpenCodeGo, Name: "OpenCode Go", URL: OpenCodeGoURL, Models: OpenCodeGoModels}, true
	default:
		return Provider{}, false
	}
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
	return Target{
		Provider:  p.ID,
		Model:     m,
		KeyIdx:    i,
		KeyLabel:  p.Keys[i].Label,
		AdminOnly: p.AdminOnly,
		OwnerID:   p.Keys[i].OwnerID,
		url:       p.URL,
		key:       p.Keys[i].Secret,
	}
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
// потом opencode. cheaper сюда не входит ни при каких условиях.
func DefaultChain(cfg *config.Config) []Target {
	var out []Target
	for _, p := range Providers(cfg) {
		if p.AdminOnly || p.ID == Cheaper {
			continue
		}
		out = append(out, targets(p, p.Models)...)
	}
	return out
}

func mergePoolKeys(base, extra []Key) []Key {
	out := make([]Key, 0, len(base)+len(extra))
	idxBySecret := make(map[string]int, len(base)+len(extra))
	for _, k := range base {
		sec := strings.TrimSpace(k.Secret)
		if sec == "" {
			continue
		}
		k.Secret = sec
		if _, exists := idxBySecret[sec]; exists {
			continue
		}
		idxBySecret[sec] = len(out)
		out = append(out, k)
	}
	for _, k := range extra {
		sec := strings.TrimSpace(k.Secret)
		if sec == "" {
			continue
		}
		k.Secret = sec
		if idx, exists := idxBySecret[sec]; exists {
			if out[idx].OwnerID == 0 && k.OwnerID != 0 {
				out[idx].OwnerID = k.OwnerID
			}
			continue
		}
		if strings.TrimSpace(k.Label) == "" {
			k.Label = fmt.Sprintf("ключ %d", len(out)+1)
		}
		idxBySecret[sec] = len(out)
		out = append(out, k)
	}
	return out
}

// PoolChain — цепочка для конспектов: только бесплатные провайдеры Gemini и OpenCode
// (никакого Cheaper/AdminOnly ни при каких условиях). Подмешивает расшаренные личные
// ключи из БД (если зарегистрированы через RegisterSharedPoolKeys) с дедупликацией по Secret.
// Для каждой модели порядок ключей перемешивается случайно на каждый вызов.
func PoolChain(cfg *config.Config) []Target {
	if cfg == nil {
		return nil
	}
	poolProviders := []Provider{
		{
			ID:     Gemini,
			Name:   "Google AI Studio (бесплатно)",
			URL:    GeminiURL,
			Keys:   mergePoolKeys(ParseKeys(cfg.GeminiAPIKeys...), sharedKeysFor(Gemini)),
			Models: GeminiModels,
		},
		{
			ID:     OpenCode,
			Name:   "OpenCode Zen (бесплатные модели)",
			URL:    OpenCodeURL,
			Keys:   mergePoolKeys(ParseKeys(append(append([]string{}, cfg.OpenCodeAPIKeys...), cfg.OpenCodeAPIKey)...), sharedKeysFor(OpenCode)),
			Models: OpenCodeModels,
		},
		{
			ID:     OpenCodeGo,
			Name:   "OpenCode Go",
			URL:    OpenCodeGoURL,
			Keys:   mergePoolKeys(ParseKeys(append(append([]string{}, cfg.OpenCodeGoAPIKeys...), cfg.OpenCodeGoAPIKey)...), sharedKeysFor(OpenCodeGo)),
			Models: OpenCodeGoModels,
		},
	}
	var out []Target
	for _, p := range poolProviders {
		if len(p.Keys) == 0 {
			continue
		}
		for _, m := range p.Models {
			indices := make([]int, len(p.Keys))
			for i := range indices {
				indices[i] = i
			}
			if len(indices) > 1 {
				mathrand.Shuffle(len(indices), func(i, j int) {
					indices[i], indices[j] = indices[j], indices[i]
				})
			}
			for _, idx := range indices {
				out = append(out, target(p, m, idx))
			}
		}
	}
	return out
}

// ProviderWithUserKey строит Target для личного ключа пользователя (только Gemini или OpenCode).
func ProviderWithUserKey(cfg *config.Config, provider, secret string, ownerID int64) (Target, bool) {
	_ = cfg
	provider = strings.ToLower(strings.TrimSpace(provider))
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return Target{}, false
	}
	p, ok := BuiltinProvider(provider)
	if !ok || len(p.Models) == 0 {
		return Target{}, false
	}
	p.Keys = []Key{{Label: "личный", Secret: secret, OwnerID: ownerID}}
	return target(p, p.Models[0], 0), true
}

// Owner — кому принадлежат ключи из .env с подписью. Подпись-фамилия совпадает
// с Surname, только если фамилия однозначна среди аккаунтов (это решает
// access.KeyOwner); подпись-цифры — точный telegram_id из ID.
type Owner struct {
	ID      int64
	Surname string
}

// OwnsKey — ключ подписан этим человеком. Подпись из одних цифр — точный
// telegram_id; иная подпись — фамилия без учёта регистра, и только если
// Owner.Surname задан (пустая фамилия никому не даёт ключей).
func OwnsKey(k Key, owner Owner) bool {
	label := strings.TrimSpace(k.Label)
	if label == "" {
		return false
	}
	if isDigits(label) {
		return owner.ID != 0 && label == strconv.FormatInt(owner.ID, 10)
	}
	surname := strings.TrimSpace(owner.Surname)
	return surname != "" && NormSurname(label) == NormSurname(surname)
}

// NormSurname — фамилия для сравнения: без пробелов по краям, без учёта
// регистра, «ё» как «е» (Пётров и Петров — одна фамилия).
func NormSurname(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "ё", "е")
}

// isDigits — строка непуста и состоит только из цифр.
func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func providersWithUserKeys(cfg *config.Config, userKeys map[string]Key) ([]Provider, map[string]int) {
	provs := Providers(cfg)
	dbIdx := make(map[string]int, 2)
	if len(userKeys) == 0 {
		return provs, dbIdx
	}
	byID := make(map[string]int, len(provs))
	for i, p := range provs {
		byID[p.ID] = i
	}
	for _, pid := range []string{Gemini, OpenCode} {
		uk, ok := userKeys[pid]
		secret := strings.TrimSpace(uk.Secret)
		if !ok || secret == "" {
			continue
		}
		uk.Secret = secret
		if strings.TrimSpace(uk.Label) == "" {
			uk.Label = "личный"
		}
		idx, exists := byID[pid]
		if !exists {
			bp, ok := BuiltinProvider(pid)
			if !ok {
				continue
			}
			byID[pid] = len(provs)
			provs = append(provs, bp)
			idx = len(provs) - 1
		}
		dbIdx[pid] = len(provs[idx].Keys)
		provs[idx].Keys = append(provs[idx].Keys, uk)
	}
	return provs, dbIdx
}

func findProviderInList(provs []Provider, id string) (Provider, bool) {
	for _, p := range provs {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// ChainFor — цепочка для тестов человека (без личных ключей из БД).
//
// Не админ — жёстко только свои ключи (подпись ключа = owner, его фамилия):
// выбранные модель и ключ первыми, потом его же ключи на остальных моделях.
// Чужих ключей и cheaper нет; своих ключей нет — пустая цепочка.
//
// Админ — его выбор первым, а если не ответил: cheaper (muse-spark-1.3-
// contributor первой), потом все ключи всех моделей.
func ChainFor(cfg *config.Config, ch Choice, admin bool, owner string) []Target {
	return ChainForWithUserKeys(cfg, ch, admin, Owner{Surname: owner}, nil)
}

// ChainForWithUserKeys строит цепочку с учётом личных ключей пользователя из БД.
// Личный ключ пользователя из БД ставится первым в его цепочке (после явного выбора)
// и никогда не попадает в цепочку другого пользователя.
func ChainForWithUserKeys(cfg *config.Config, ch Choice, admin bool, owner Owner, userKeys map[string]Key) []Target {
	var out []Target
	seen := map[string]bool{}
	add := func(ts ...Target) {
		for _, t := range ts {
			k := fmt.Sprintf("%s|%s|%d", t.Provider, t.Model, t.KeyIdx)
			if t.key != "" {
				k = fmt.Sprintf("%s|%s|%s", t.Provider, t.Model, t.key)
			}
			if !seen[k] {
				seen[k] = true
				out = append(out, t)
			}
		}
	}
	provs, dbIdx := providersWithUserKeys(cfg, userKeys)

	// Выбор человека.
	if p, ok := findProviderInList(provs, ch.Provider); ok && (admin || !p.AdminOnly) {
		model := strings.TrimSpace(ch.Model)
		if model == "" && len(p.Models) > 0 {
			model = p.Models[0]
		}
		if model != "" {
			dbKey, hasDB := dbIdx[p.ID]
			if ch.Key >= 1 && ch.Key <= len(p.Keys) {
				kIdx := ch.Key - 1
				if admin || OwnsKey(p.Keys[kIdx], owner) || (hasDB && kIdx == dbKey) {
					add(target(p, model, kIdx))
				}
			}
			if hasDB {
				add(target(p, model, dbKey))
			}
			for i, k := range p.Keys {
				if hasDB && i == dbKey {
					continue
				}
				if admin || OwnsKey(k, owner) {
					add(target(p, model, i))
				}
			}
		}
	}

	if !admin {
		for _, p := range provs {
			if p.AdminOnly {
				continue
			}
			if dbKey, hasDB := dbIdx[p.ID]; hasDB {
				for _, m := range p.Models {
					add(target(p, m, dbKey))
				}
			}
		}
		for _, p := range provs {
			if p.AdminOnly {
				continue
			}
			dbKey, hasDB := dbIdx[p.ID]
			for _, m := range p.Models {
				for i, k := range p.Keys {
					if hasDB && i == dbKey {
						continue
					}
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
			dbKey, hasDB := dbIdx[p.ID]
			for _, m := range p.Models {
				if hasDB {
					add(target(p, m, dbKey))
				}
				for i := range p.Keys {
					if hasDB && i == dbKey {
						continue
					}
					add(target(p, m, i))
				}
			}
		}
	}
	return out
}

// ChainForTest строит цепочку ровно для проверки выбранного провайдера/модели/ключа в панели,
// с учётом личных ключей из БД и без подмешивания чужих ключей для не-админа.
func ChainForTest(cfg *config.Config, ch Choice, admin bool, owner Owner, userKeys map[string]Key) []Target {
	provs, dbIdx := providersWithUserKeys(cfg, userKeys)
	p, ok := findProviderInList(provs, ch.Provider)
	if !ok || (p.AdminOnly && !admin) {
		return nil
	}
	model := strings.TrimSpace(ch.Model)
	if model == "" && len(p.Models) > 0 {
		model = p.Models[0]
	}
	if model == "" {
		return nil
	}
	dbKey, hasDB := dbIdx[p.ID]
	if ch.Key >= 1 && ch.Key <= len(p.Keys) {
		kIdx := ch.Key - 1
		if admin || OwnsKey(p.Keys[kIdx], owner) || (hasDB && kIdx == dbKey) {
			return []Target{target(p, model, kIdx)}
		}
		return nil
	}
	var out []Target
	if hasDB {
		out = append(out, target(p, model, dbKey))
	}
	for i, k := range p.Keys {
		if hasDB && i == dbKey {
			continue
		}
		if admin || OwnsKey(k, owner) {
			out = append(out, target(p, model, i))
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
	Text         string    `json:"text"`
	Used         *Target   `json:"used,omitempty"`
	Attempts     []Attempt `json:"attempts"`
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
}

// ErrNoProviders — ни одного ключа не настроено.
var ErrNoProviders = errors.New("llm: не настроено ни одного провайдера")

// PerCallTimeout — сколько ждём один вызов; hydrogen в режиме максимальных
// размышлений может думать несколько минут.
var PerCallTimeout = 6 * time.Minute

var httpClient = &http.Client{}

// TargetCallFunc выполняет один вызов к таргету цепочки.
// Возвращает текст ответа, HTTP-статус (0 если ответа не было),
// заголовки и тело ответа, а также ошибку.
type TargetCallFunc func(ctx context.Context, t Target) (text string, status int, hdr http.Header, body []byte, err error)

// Ask идёт по цепочке до первого непустого ответа, используя пул из контекста
// или глобальный пул по умолчанию.
func Ask(ctx context.Context, chain []Target, sysPrompt, userPrompt string) (Result, error) {
	return AskWithPool(ctx, poolFromContext(ctx), chain, sysPrompt, userPrompt)
}

// AskWithPool идёт по цепочке до первого непустого ответа с заданным пулом ключей.
func AskWithPool(ctx context.Context, pool *KeyPool, chain []Target, sysPrompt, userPrompt string) (Result, error) {
	return askChain(ctx, pool, chain, func(cctx context.Context, t Target) (string, int, http.Header, []byte, error) {
		return call(cctx, t, sysPrompt, userPrompt)
	})
}

func askChain(ctx context.Context, pool *KeyPool, chain []Target, fn TargetCallFunc) (Result, error) {
	var res Result
	if len(chain) == 0 {
		return res, ErrNoProviders
	}
	if pool == nil {
		pool = poolFromContext(ctx)
	}
	if fn == nil {
		return res, errors.New("llm: не задана функция вызова таргета")
	}

	var (
		last          error
		attempted     int
		busy          []Target
		earliestUntil time.Time
	)
	recordCooldown := func(until time.Time) {
		if until.IsZero() {
			return
		}
		if earliestUntil.IsZero() || until.Before(earliestUntil) {
			earliestUntil = until
		}
	}

	// Проход 1: пропускаем ключи в cooldown и пробуем неблокирующий захват слота,
	// чтобы при занятости одного ключа сразу переключиться на другой свободный ключ.
	for _, t := range chain {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if until := pool.CooldownUntil(t.key); !until.IsZero() {
			recordCooldown(until)
			continue
		}
		release, ok := pool.tryAcquire(t.key)
		if !ok {
			busy = append(busy, t)
			continue
		}
		attempted++
		done, err := runTarget(ctx, pool, t, release, fn, &res)
		if done {
			return res, nil
		}
		if err != nil {
			last = err
		}
	}

	// Проход 2: свободных ключей без ожидания не осталось — ждём слот у отложенных таргетов.
	for _, t := range busy {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if until := pool.CooldownUntil(t.key); !until.IsZero() {
			recordCooldown(until)
			continue
		}
		release, err := pool.acquire(ctx, t.key)
		if err != nil {
			return res, err
		}
		// Пока ожидали слот, другой запрос мог перевести ключ в cooldown.
		if until := pool.CooldownUntil(t.key); !until.IsZero() {
			release()
			recordCooldown(until)
			continue
		}
		attempted++
		done, callErr := runTarget(ctx, pool, t, release, fn, &res)
		if done {
			return res, nil
		}
		if callErr != nil {
			last = callErr
		}
	}

	if err := ctx.Err(); err != nil {
		return res, err
	}
	if attempted == 0 && !earliestUntil.IsZero() {
		wait := earliestUntil.Sub(pool.now())
		if wait < 0 {
			wait = 0
		}
		return res, fmt.Errorf("llm: все %d вариантов в cooldown (ближайший освободится через %s): %w", len(chain), formatCooldownWait(wait), ErrAllCooldown)
	}
	return res, fmt.Errorf("llm: все %d вариантов не ответили, последний: %w", len(chain), last)
}

func extractUsage(body []byte) (int, int) {
	if len(body) == 0 {
		return 0, 0
	}
	var out chatResp
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, 0
	}
	return out.Usage.PromptTokens, out.Usage.CompletionTokens
}

func runTarget(ctx context.Context, pool *KeyPool, t Target, release func(), fn TargetCallFunc, res *Result) (bool, error) {
	defer release()

	cctx, cancel := context.WithTimeout(ctx, PerCallTimeout)
	defer cancel()

	start := time.Now()
	text, status, hdr, body, err := fn(cctx, t)
	took := time.Since(start)
	a := Attempt{Target: t, Took: took}

	if status != http.StatusTooManyRequests && err == nil && (status == 0 || (status >= 200 && status < 300)) {
		inTok, outTok := extractUsage(body)
		res.Attempts = append(res.Attempts, a)
		tt := t
		res.Used = &tt
		res.Text = text
		res.InputTokens += inTok
		res.OutputTokens += outTok
		hookStatus := status
		if hookStatus == 0 {
			hookStatus = http.StatusOK
		}
		notifyUsage(t, hookStatus, inTok, outTok, time.Time{})
		logx.Infof("llm", "%s: ok %d симв. за %s", t, len(text), took.Round(time.Millisecond))
		return true, nil
	}

	var msg string
	if err != nil {
		msg = err.Error()
	} else {
		msg = fmt.Sprintf("HTTP %d: %s", status, short(string(body), 300))
	}
	scrubbed := scrub(msg, t.key)
	a.Err = scrubbed.Error()
	res.Attempts = append(res.Attempts, a)

	var cooldownUntil time.Time
	if status == http.StatusTooManyRequests {
		delay := pool.MarkRateLimited(t.key, hdr, body)
		cooldownUntil = pool.CooldownUntil(t.key)
		if cooldownUntil.IsZero() && delay > 0 {
			cooldownUntil = pool.now().Add(delay)
		}
		logx.Warnf("llm", "%s: %v (cooldown %s, %s)", t, scrubbed, formatCooldownWait(delay), took.Round(time.Millisecond))
	} else {
		logx.Warnf("llm", "%s: %v (%s)", t, scrubbed, took.Round(time.Millisecond))
	}
	if status > 0 {
		notifyUsage(t, status, 0, 0, cooldownUntil)
	}
	return false, scrubbed
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
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func call(ctx context.Context, t Target, sysPrompt, userPrompt string) (string, int, http.Header, []byte, error) {
	msgs := []chatMessage{}
	if strings.TrimSpace(sysPrompt) != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: sysPrompt})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: userPrompt})
	payload, extra := buildChatPayload(t, msgs, GenOptsFromContext(ctx))
	text, status, hdr, body, err := postJSON(ctx, t, payload)
	if extra && genParamsRejected(status, body) {
		// Провайдер не знает наших необязательных полей: повторяем без них
		// и запоминаем адрес, чтобы больше не тратить запрос впустую.
		markGenParamsUnsupported(t)
		payload, _ = buildChatPayload(t, msgs, GenOpts{})
		return postJSON(ctx, t, payload)
	}
	return text, status, hdr, body, err
}

// postJSON отправляет готовое тело chat/completions и разбирает ответ.
func postJSON(ctx context.Context, t Target, payload map[string]any) (string, int, http.Header, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", 0, nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ChatEndpoint(t.url), bytes.NewReader(raw))
	if err != nil {
		return "", 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.key)
	req.Header.Set("Content-Type", "application/json")
	setCommonHeaders(req, t.Provider)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, nil, nil, scrub(err.Error(), t.key)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", resp.StatusCode, resp.Header, nil, fmt.Errorf("ответ оборвался: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, resp.Header, b, scrub(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, short(string(b), 300)), t.key)
	}
	var out chatResp
	if err := json.Unmarshal(b, &out); err != nil {
		return "", resp.StatusCode, resp.Header, b, fmt.Errorf("не JSON (%d байт): %w", len(b), err)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", resp.StatusCode, resp.Header, b, errors.New("пустой ответ")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), resp.StatusCode, resp.Header, b, nil
}

// ListModels — живой список моделей провайдера (GET /models), первым ключом.
func ListModels(ctx context.Context, p Provider) ([]string, error) {
	if len(p.Keys) == 0 {
		return nil, errors.New("нет ключа")
	}
	return ListModelsWithKey(ctx, p, p.Keys[0].Secret)
}

// ListModelsWithKey — живой список моделей провайдера (GET /models) конкретным ключом.
func ListModelsWithKey(ctx context.Context, p Provider, secret string) ([]string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New("нет ключа")
	}
	if strings.TrimSpace(p.URL) == "" {
		return nil, errors.New("не задан URL провайдера")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, modelsEndpoint(p.URL), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	setCommonHeaders(req, p.ID)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, scrub(err.Error(), secret)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return nil, scrub(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, short(string(b), 200)), secret)
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

// TestProviderKey проверяет конкретный личный ключ (только Gemini или OpenCode)
// изолированным пробным вызовом по первой модели провайдера, не затрагивая
// глобальный KeyPool (cooldown) и статистику usageHook.
func TestProviderKey(ctx context.Context, cfg *config.Config, provider, secret string) (Result, error) {
	t, ok := ProviderWithUserKey(cfg, provider, secret, 0)
	if !ok {
		return Result{}, fmt.Errorf("провайдер %q недоступен для личного ключа", provider)
	}
	cctx, cancel := context.WithTimeout(ctx, PerCallTimeout)
	defer cancel()

	start := time.Now()
	text, _, _, body, err := call(cctx, t, "", "Ответь одним словом: работает")
	took := time.Since(start)
	if err != nil {
		scrubbed := scrub(err.Error(), t.key)
		return Result{
			Attempts: []Attempt{{Target: t, Took: took, Err: scrubbed.Error()}},
		}, scrubbed
	}
	inTok, outTok := extractUsage(body)
	tt := t
	return Result{
		Text:         text,
		Used:         &tt,
		Attempts:     []Attempt{{Target: t, Took: took}},
		InputTokens:  inTok,
		OutputTokens: outTok,
	}, nil
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
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}

// scrub — ключ не должен попасть в лог или в панель даже из текста ошибки.
func scrub(msg, key string) error {
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "***")
	}
	return errors.New(msg)
}

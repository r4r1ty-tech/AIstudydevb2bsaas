package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// MaxConcurrentPerKey — максимальное число одновременных запросов на один секрет ключа.
	MaxConcurrentPerKey = 2

	// DefaultRateLimitCooldown — задержка по умолчанию при HTTP 429 без Retry-After и retryDelay.
	DefaultRateLimitCooldown = 30 * time.Second

	// MaxRateLimitCooldown — максимальная длительность cooldown после HTTP 429.
	MaxRateLimitCooldown = 15 * time.Minute
)

// ErrAllCooldown возвращается, когда все варианты в цепочке сейчас находятся в cooldown.
var ErrAllCooldown = errors.New("llm: все ключи на паузе из-за лимита (429)")

var (
	reRetryDelayText = regexp.MustCompile(`(?i)retry_?delay["']?\s*[:=]\s*["']?([0-9]+(?:\.[0-9]+)?\s*(?:ms|s|m|h)?)`)
	reRetryInText    = regexp.MustCompile(`(?i)retry\s+in\s+([0-9]+(?:\.[0-9]+)?\s*(?:ms|s|m|h))`)
)

// KeyStatus описывает текущее состояние одного секрета ключа в пуле.
type KeyStatus struct {
	CooldownUntil time.Time     `json:"cooldown_until,omitempty"`
	Remaining     time.Duration `json:"remaining,omitempty"`
	InFlight      int           `json:"in_flight"`
}

type keyState struct {
	sem           chan struct{}
	inFlight      atomic.Int32
	cooldownUntil time.Time
}

// KeyPool управляет cooldown-паузами после HTTP 429 и семафорами одновременных
// запросов для каждого секрета API-ключа.
type KeyPool struct {
	mu        sync.Mutex
	keys      map[string]*keyState
	maxPerKey int
	nowFn     func() time.Time
}

var defaultPool = NewKeyPool()

// NewKeyPool создаёт изолированный пул ключей (удобно для параллельных тестов).
func NewKeyPool() *KeyPool {
	return &KeyPool{
		keys:      make(map[string]*keyState),
		maxPerKey: MaxConcurrentPerKey,
		nowFn:     time.Now,
	}
}

// DefaultPool возвращает глобальный пул ключей, используемый Ask и Test по умолчанию.
func DefaultPool() *KeyPool {
	return defaultPool
}

type poolContextKey struct{}

// WithPool привязывает пул ключей к контексту (для подмены в тестах).
func WithPool(ctx context.Context, pool *KeyPool) context.Context {
	return context.WithValue(ctx, poolContextKey{}, pool)
}

func poolFromContext(ctx context.Context) *KeyPool {
	if ctx != nil {
		if p, ok := ctx.Value(poolContextKey{}).(*KeyPool); ok && p != nil {
			return p
		}
	}
	return defaultPool
}

// SetNowFunc задаёт источник текущего времени для пула (для детерминированных тестов).
// Передача nil восстанавливает time.Now.
func (p *KeyPool) SetNowFunc(fn func() time.Time) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if fn == nil {
		p.nowFn = time.Now
		return
	}
	p.nowFn = fn
}

// SetPoolNowFunc задаёт источник времени для глобального пула по умолчанию.
func SetPoolNowFunc(fn func() time.Time) {
	defaultPool.SetNowFunc(fn)
}

func (p *KeyPool) now() time.Time {
	p.mu.Lock()
	fn := p.nowFn
	p.mu.Unlock()
	if fn == nil {
		return time.Now()
	}
	return fn()
}

func (p *KeyPool) stateForLocked(secret string) *keyState {
	if p.keys == nil {
		p.keys = make(map[string]*keyState)
	}
	if st, ok := p.keys[secret]; ok {
		return st
	}
	limit := p.maxPerKey
	if limit <= 0 {
		limit = MaxConcurrentPerKey
	}
	st := &keyState{sem: make(chan struct{}, limit)}
	p.keys[secret] = st
	return st
}

func (p *KeyPool) stateFor(secret string) *keyState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stateForLocked(secret)
}

// CooldownUntil возвращает время окончания активного cooldown для секрета ключа.
// Если ключ не на паузе или пауза уже истекла, возвращается нулевое time.Time.
func (p *KeyPool) CooldownUntil(secret string) time.Time {
	if p == nil || secret == "" {
		return time.Time{}
	}
	now := p.now()
	p.mu.Lock()
	st, ok := p.keys[secret]
	var until time.Time
	if ok {
		until = st.cooldownUntil
	}
	p.mu.Unlock()
	if until.After(now) {
		return until
	}
	return time.Time{}
}

// InFlight возвращает количество выполняющихся в данный момент запросов для секрета ключа.
func (p *KeyPool) InFlight(secret string) int {
	if p == nil || secret == "" {
		return 0
	}
	p.mu.Lock()
	st, ok := p.keys[secret]
	p.mu.Unlock()
	if !ok {
		return 0
	}
	return int(st.inFlight.Load())
}

// SetCooldown устанавливает cooldown до указанного времени, если оно позже текущего.
func (p *KeyPool) SetCooldown(secret string, until time.Time) {
	if p == nil || secret == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.stateForLocked(secret)
	if until.After(st.cooldownUntil) {
		st.cooldownUntil = until
	}
}

// MarkRateLimited вычисляет задержку по заголовкам и телу ответа 429,
// переводит ключ в cooldown и возвращает применённую длительность паузы.
func (p *KeyPool) MarkRateLimited(secret string, hdr http.Header, body []byte) time.Duration {
	if p == nil {
		return DefaultRateLimitCooldown
	}
	now := p.now()
	delay := parseRetryDelay(hdr, body, now)
	if secret != "" {
		p.SetCooldown(secret, now.Add(delay))
	}
	return delay
}

// Snapshot возвращает текущее состояние лимитов для переданного списка секретов.
func (p *KeyPool) Snapshot(secrets []string) map[string]KeyStatus {
	out := make(map[string]KeyStatus, len(secrets))
	if p == nil {
		return out
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		var ks KeyStatus
		if st, ok := p.keys[secret]; ok {
			ks.InFlight = int(st.inFlight.Load())
			if st.cooldownUntil.After(now) {
				ks.CooldownUntil = st.cooldownUntil
				ks.Remaining = st.cooldownUntil.Sub(now)
			}
		}
		out[secret] = ks
	}
	return out
}

// KeyCooldownUntil возвращает время выхода ключа из cooldown в глобальном пуле
// и признак того, что cooldown активен.
func KeyCooldownUntil(secret string) (time.Time, bool) {
	until := defaultPool.CooldownUntil(secret)
	return until, !until.IsZero()
}

// KeyInFlight возвращает количество текущих запросов для ключа в глобальном пуле.
func KeyInFlight(secret string) int {
	return defaultPool.InFlight(secret)
}

// PoolSnapshot возвращает снимок состояния лимитов глобального пула по переданным секретам.
func PoolSnapshot(secrets []string) map[string]KeyStatus {
	return defaultPool.Snapshot(secrets)
}

func newReleaseFunc(st *keyState) func() {
	st.inFlight.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			st.inFlight.Add(-1)
			<-st.sem
		})
	}
}

func (p *KeyPool) tryAcquire(secret string) (func(), bool) {
	if p == nil || secret == "" {
		return func() {}, true
	}
	st := p.stateFor(secret)
	select {
	case st.sem <- struct{}{}:
		return newReleaseFunc(st), true
	default:
		return nil, false
	}
}

func (p *KeyPool) acquire(ctx context.Context, secret string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || secret == "" {
		return func() {}, nil
	}
	st := p.stateFor(secret)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case st.sem <- struct{}{}:
		return newReleaseFunc(st), nil
	}
}

// parseRetryDelay извлекает время ожидания из заголовка Retry-After и тела ответа
// (форматы Gemini retryDelay / retry_delay / текст), выбирая максимум из найденных
// значений. При отсутствии обоих источников возвращает DefaultRateLimitCooldown,
// сверху ограничивает задержку значением MaxRateLimitCooldown.
func parseRetryDelay(hdr http.Header, body []byte, now time.Time) time.Duration {
	var maxDelay time.Duration
	consider := func(d time.Duration) {
		if d > maxDelay {
			maxDelay = d
		}
	}

	if hdr != nil {
		if ra := strings.TrimSpace(hdr.Get("Retry-After")); ra != "" {
			if d, ok := parseSecondsString(ra); ok {
				consider(d)
			} else if t, err := http.ParseTime(ra); err == nil {
				if d := t.Sub(now); d > 0 {
					consider(d)
				}
			}
		}
	}

	if len(body) > 0 {
		var parsed any
		if err := json.Unmarshal(body, &parsed); err == nil {
			extractRetryDelayFromJSON(parsed, consider)
		}
		text := string(body)
		for _, re := range []*regexp.Regexp{reRetryDelayText, reRetryInText} {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				if len(m) > 1 {
					if d, ok := parseDurationOrSeconds(m[1]); ok {
						consider(d)
					}
				}
			}
		}
	}

	if maxDelay <= 0 {
		return DefaultRateLimitCooldown
	}
	if maxDelay > MaxRateLimitCooldown {
		return MaxRateLimitCooldown
	}
	return maxDelay
}

func extractRetryDelayFromJSON(v any, consider func(time.Duration)) {
	switch val := v.(type) {
	case map[string]any:
		for k, sub := range val {
			if strings.EqualFold(k, "retryDelay") || strings.EqualFold(k, "retry_delay") {
				if d, ok := parseDelayAny(sub); ok {
					consider(d)
				}
				continue
			}
			extractRetryDelayFromJSON(sub, consider)
		}
	case []any:
		for _, item := range val {
			extractRetryDelayFromJSON(item, consider)
		}
	}
}

func parseDelayAny(v any) (time.Duration, bool) {
	switch val := v.(type) {
	case string:
		return parseDurationOrSeconds(val)
	case float64:
		return durationFromSeconds(val)
	case map[string]any:
		var secs, nanos float64
		if s, ok := val["seconds"]; ok {
			switch sv := s.(type) {
			case float64:
				secs = sv
			case string:
				if f, err := strconv.ParseFloat(strings.TrimSpace(sv), 64); err == nil {
					secs = f
				}
			}
		}
		if n, ok := val["nanos"]; ok {
			if nv, ok := n.(float64); ok {
				nanos = nv
			}
		}
		total := secs + nanos/1e9
		return durationFromSeconds(total)
	default:
		return 0, false
	}
}

func parseDurationOrSeconds(raw string) (time.Duration, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	// Убираем пробелы между числом и суффиксом единицы измерения (например, "12.5 s").
	s = strings.Join(strings.Fields(s), "")
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		if d > MaxRateLimitCooldown {
			return MaxRateLimitCooldown, true
		}
		return d, true
	}
	return parseSecondsString(s)
}

func parseSecondsString(raw string) (time.Duration, bool) {
	secs, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, false
	}
	return durationFromSeconds(secs)
}

func durationFromSeconds(secs float64) (time.Duration, bool) {
	if math.IsNaN(secs) || math.IsInf(secs, 0) || secs <= 0 {
		return 0, false
	}
	if secs >= MaxRateLimitCooldown.Seconds() {
		return MaxRateLimitCooldown, true
	}
	return time.Duration(secs * float64(time.Second)), true
}

func formatCooldownWait(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d >= time.Second {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Millisecond).String()
}

var (
	hooksMu        sync.RWMutex
	sharedKeysHook func(provider string) []Key
	usageHook      func(t Target, status int, inTok, outTok int, cooldownUntil time.Time)
)

// RegisterSharedPoolKeys регистрирует поставщика расшаренных личных ключей из БД для PoolChain.
// Передача nil отключает внешний источник ключей.
func RegisterSharedPoolKeys(fn func(provider string) []Key) {
	hooksMu.Lock()
	sharedKeysHook = fn
	hooksMu.Unlock()
}

func sharedKeysFor(provider string) []Key {
	hooksMu.RLock()
	fn := sharedKeysHook
	hooksMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(provider)
}

// RegisterUsageHook регистрирует обработчик завершённых HTTP-запросов (успех и 429).
// Передача nil отключает обработчик.
func RegisterUsageHook(fn func(t Target, status int, inTok, outTok int, cooldownUntil time.Time)) {
	hooksMu.Lock()
	usageHook = fn
	hooksMu.Unlock()
}

func notifyUsage(t Target, status int, inTok, outTok int, cooldownUntil time.Time) {
	hooksMu.RLock()
	fn := usageHook
	hooksMu.RUnlock()
	if fn != nil {
		fn(t, status, inTok, outTok, cooldownUntil)
	}
}

// String возвращает краткую человекочитаемую сводку статуса ключа.
func (s KeyStatus) String() string {
	if s.Remaining > 0 {
		return fmt.Sprintf("cooldown %s, активных %d", formatCooldownWait(s.Remaining), s.InFlight)
	}
	return fmt.Sprintf("готов, активных %d", s.InFlight)
}

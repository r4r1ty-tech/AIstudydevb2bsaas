package llm

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// GenOpts — необязательные параметры генерации, которые добавляются к
// OpenAI-совместимому chat/completions вместе с model и messages.
// Нулевое значение — «ничего не передавать»: провайдер применит свои дефолты.
type GenOpts struct {
	// ReasoningEffort — глубина размышлений: none/minimal/low/medium/high/xhigh/max.
	// "max" переводится в максимальный уровень конкретной модели (см. resolveEffort).
	ReasoningEffort string
	// Temperature — температура выборки; nil — не передавать.
	Temperature *float64
	// MaxTokens — лимит токенов ответа; 0 — не передавать.
	MaxTokens int
	// JSONMode — просить response_format=json_object.
	JSONMode bool
}

type genOptsKey struct{}

// WithGenOpts кладёт параметры генерации в контекст: ближайший вызов Ask/AskParts
// внутри этого контекста их использует.
func WithGenOpts(ctx context.Context, o GenOpts) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, genOptsKey{}, o)
}

// GenOptsFromContext возвращает параметры генерации из контекста или из env.
// Пустые поля контекстных параметров заполняются значениями из env, поэтому
// вызывающему достаточно задать только то, что он хочет изменить.
func GenOptsFromContext(ctx context.Context) GenOpts {
	base := defaultGenOpts()
	o, ok := ctx.Value(genOptsKey{}).(GenOpts)
	if !ok {
		return base
	}
	if o.ReasoningEffort == "" {
		o.ReasoningEffort = base.ReasoningEffort
	}
	if o.Temperature == nil {
		o.Temperature = base.Temperature
	}
	if o.MaxTokens == 0 {
		o.MaxTokens = base.MaxTokens
	}
	o.JSONMode = o.JSONMode || base.JSONMode
	return o
}

var (
	envOptsOnce sync.Once
	envOpts     GenOpts
)

func defaultGenOpts() GenOpts {
	envOptsOnce.Do(func() { envOpts = genOptsFromEnv() })
	return envOpts
}

// genOptsFromEnv читает LLM_REASONING_EFFORT, LLM_TEMPERATURE, LLM_MAX_TOKENS,
// LLM_JSON_MODE. По умолчанию — максимальные размышления.
func genOptsFromEnv() GenOpts {
	o := GenOpts{ReasoningEffort: strings.ToLower(strings.TrimSpace(os.Getenv("LLM_REASONING_EFFORT")))}
	if o.ReasoningEffort == "" {
		o.ReasoningEffort = "max"
	}
	if v := strings.TrimSpace(os.Getenv("LLM_TEMPERATURE")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 2 {
			o.Temperature = &f
		} else {
			logx.Warnf("llm", "LLM_TEMPERATURE=%q не число 0..2 — не передаю", v)
		}
	}
	if v := strings.TrimSpace(os.Getenv("LLM_MAX_TOKENS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			o.MaxTokens = n
		} else {
			logx.Warnf("llm", "LLM_MAX_TOKENS=%q не положительное число — не передаю", v)
		}
	}
	o.JSONMode = os.Getenv("LLM_JSON_MODE") == "1"
	return o
}

// resolveEffort переводит общее пожелание в допустимое значение reasoning_effort
// конкретной модели. Gemini 4 hydrogen/neon понимают xhigh, остальные — high;
// для сознательно выключенных размышлений возвращается пустая строка.
func resolveEffort(t Target, effort string) string {
	e := strings.ToLower(strings.TrimSpace(effort))
	switch e {
	case "", "off", "none", "0", "false":
		return ""
	case "max":
		if t.Provider == Gemini {
			if geminiSupportsXHigh(t.Model) {
				return "xhigh"
			}
			return "high"
		}
		return "high"
	case "xhigh":
		if t.Provider == Gemini && !geminiSupportsXHigh(t.Model) {
			return "high"
		}
		return "xhigh"
	case "minimal", "low", "medium", "high":
		return e
	default:
		logx.Warnf("llm", "неизвестный reasoning_effort %q — использую high", effort)
		return "high"
	}
}

func geminiSupportsXHigh(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "hydrogen") || strings.Contains(m, "neon")
}

// buildChatPayload собирает тело запроса. Второе значение — были ли добавлены
// необязательные параметры (нужно, чтобы понять, что откатывать при 400).
func buildChatPayload(t Target, msgs any, o GenOpts) (map[string]any, bool) {
	payload := map[string]any{"model": t.Model, "messages": msgs}
	extra := false
	if e := resolveEffort(t, o.ReasoningEffort); e != "" && !genParamsUnsupported(t) {
		payload["reasoning_effort"] = e
		extra = true
	}
	if o.Temperature != nil && !genParamsUnsupported(t) {
		payload["temperature"] = *o.Temperature
		extra = true
	}
	if o.MaxTokens > 0 && !genParamsUnsupported(t) {
		payload["max_tokens"] = o.MaxTokens
		extra = true
	}
	if o.JSONMode && !genParamsUnsupported(t) {
		payload["response_format"] = map[string]string{"type": "json_object"}
		extra = true
	}
	return payload, extra
}

// genParamsRejected — провайдер отверг запрос, и виноваты, скорее всего, наши
// необязательные параметры. Ошибки ключа, доступа и неизвестной модели
// исключаем: иначе один такой ответ навсегда отключил бы размышления.
func genParamsRejected(status int, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	s := strings.ToLower(string(body))
	for _, n := range []string{"api key", "api_key", "apikey", "authorization", "permission", "quota", "credential", "not found"} {
		if strings.Contains(s, n) {
			return false
		}
	}
	return true
}

// rejectedGenParams — адреса API, которым не нужно больше слать параметры
// генерации: один 400 запоминается до перезапуска процесса.
var rejectedGenParams sync.Map // url -> struct{}

func genParamsUnsupported(t Target) bool {
	_, ok := rejectedGenParams.Load(t.url)
	return ok
}

func markGenParamsUnsupported(t Target) {
	if _, loaded := rejectedGenParams.LoadOrStore(t.url, struct{}{}); !loaded {
		logx.Warnf("llm", "%s: API отверг параметры генерации — дальше шлю только model+messages", t)
	}
}

// resetGenParamsCache сбрасывает кэш отказов (тесты и перечитывание env).
func resetGenParamsCache() {
	rejectedGenParams.Range(func(k, _ any) bool {
		rejectedGenParams.Delete(k)
		return true
	})
	envOptsOnce = sync.Once{}
}

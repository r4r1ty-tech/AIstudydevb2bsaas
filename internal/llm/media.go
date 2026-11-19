package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

// PartKind задаёт тип фрагмента пользовательского мультимодального сообщения.
type PartKind string

const (
	// PartKindText — текстовый фрагмент.
	PartKindText PartKind = "text"
	// PartKindImage — изображение в виде data URL с base64.
	PartKindImage PartKind = "image_url"
	// PartKindAudio — аудиофрагмент в формате OpenAI input_audio.
	PartKindAudio PartKind = "input_audio"

	// MaxMediaPartBytes ограничивает размер одного бинарного вложения (20 МиБ).
	MaxMediaPartBytes = 20 << 20
)

// Part представляет один фрагмент контента пользовательского сообщения для LLM.
type Part struct {
	Kind   PartKind
	Text   string
	Data   []byte
	MIME   string
	Format string
}

// TextPart создаёт текстовую часть сообщения.
func TextPart(text string) Part {
	return Part{
		Kind: PartKindText,
		Text: text,
	}
}

// ImagePart создаёт часть сообщения с изображением.
// Если mime пуст или не относится к image/*, используется fallback "image/png".
func ImagePart(data []byte, mime string) Part {
	return Part{
		Kind: PartKindImage,
		Data: data,
		MIME: normalizeImageMIME(mime),
	}
}

// AudioPart создаёт часть сообщения с аудиозаписью.
// Формат нормализуется (например, "audio/mpeg" и ".mp3" -> "mp3", "audio/wav" -> "wav", "audio/ogg" -> "ogg").
func AudioPart(data []byte, format string) Part {
	return Part{
		Kind:   PartKindAudio,
		Data:   data,
		Format: normalizeAudioFormat(format),
	}
}

func normalizeImageMIME(mime string) string {
	m := strings.TrimSpace(strings.ToLower(mime))
	if idx := strings.Index(m, ";"); idx >= 0 {
		m = strings.TrimSpace(m[:idx])
	}
	if !strings.HasPrefix(m, "image/") || len(m) <= len("image/") {
		return "image/png"
	}
	return m
}

func normalizeAudioFormat(format string) string {
	f := strings.TrimSpace(strings.ToLower(format))
	if idx := strings.Index(f, ";"); idx >= 0 {
		f = strings.TrimSpace(f[:idx])
	}
	f = strings.TrimPrefix(f, ".")
	f = strings.TrimPrefix(f, "audio/")
	switch f {
	case "mpeg", "mp3":
		return "mp3"
	case "wav", "x-wav", "wave":
		return "wav"
	case "ogg", "opus", "oga":
		return "ogg"
	case "mp4", "m4a", "x-m4a", "aac":
		return "m4a"
	case "flac":
		return "flac"
	case "webm":
		return "webm"
	default:
		return f
	}
}

// Validate проверяет корректность и допустимый размер части сообщения.
func (p Part) Validate() error {
	switch p.Kind {
	case PartKindText:
		if strings.TrimSpace(p.Text) == "" {
			return errors.New("llm: пустая текстовая часть сообщения")
		}
		return nil
	case PartKindImage:
		if len(p.Data) == 0 {
			return errors.New("llm: пустые данные изображения")
		}
		if len(p.Data) > MaxMediaPartBytes {
			return fmt.Errorf("llm: размер изображения (%d байт) превышает лимит %d байт", len(p.Data), MaxMediaPartBytes)
		}
		return nil
	case PartKindAudio:
		if len(p.Data) == 0 {
			return errors.New("llm: пустые данные аудио")
		}
		if len(p.Data) > MaxMediaPartBytes {
			return fmt.Errorf("llm: размер аудио (%d байт) превышает лимит %d байт", len(p.Data), MaxMediaPartBytes)
		}
		if normalizeAudioFormat(p.Format) == "" {
			return errors.New("llm: не указан формат аудио")
		}
		return nil
	default:
		return fmt.Errorf("llm: неизвестный тип части сообщения %q", p.Kind)
	}
}

type textPartPayload struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type imageURLValue struct {
	URL string `json:"url"`
}

type imagePartPayload struct {
	Type     string        `json:"type"`
	ImageURL imageURLValue `json:"image_url"`
}

type inputAudioValue struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

type audioPartPayload struct {
	Type       string          `json:"type"`
	InputAudio inputAudioValue `json:"input_audio"`
}

func (p Part) toPayload() (any, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	switch p.Kind {
	case PartKindText:
		return textPartPayload{
			Type: string(PartKindText),
			Text: p.Text,
		}, nil
	case PartKindImage:
		mime := normalizeImageMIME(p.MIME)
		b64 := base64.StdEncoding.EncodeToString(p.Data)
		return imagePartPayload{
			Type: string(PartKindImage),
			ImageURL: imageURLValue{
				URL: "data:" + mime + ";base64," + b64,
			},
		}, nil
	case PartKindAudio:
		format := normalizeAudioFormat(p.Format)
		b64 := base64.StdEncoding.EncodeToString(p.Data)
		return audioPartPayload{
			Type: string(PartKindAudio),
			InputAudio: inputAudioValue{
				Data:   b64,
				Format: format,
			},
		}, nil
	default:
		return nil, fmt.Errorf("llm: неизвестный тип части сообщения %q", p.Kind)
	}
}

// MarshalJSON кодирует Part в OpenAI-совместимый формат элемента content.
func (p Part) MarshalJSON() ([]byte, error) {
	payload, err := p.toPayload()
	if err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}

// BuildContentParts валидирует срез частей и собирает JSON-массив content
// для OpenAI-совместимого API chat/completions.
func BuildContentParts(parts []Part) (json.RawMessage, error) {
	if len(parts) == 0 {
		return nil, errors.New("llm: не переданы части сообщения")
	}
	payloads := make([]any, 0, len(parts))
	for _, p := range parts {
		item, err := p.toPayload()
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, item)
	}
	raw, err := json.Marshal(payloads)
	if err != nil {
		return nil, fmt.Errorf("llm: сериализация мультимодального контента: %w", err)
	}
	return json.RawMessage(raw), nil
}

// SupportsVision сообщает, поддерживает ли модель данного провайдера обработку изображений.
func SupportsVision(provider, model string) bool {
	p := strings.TrimSpace(strings.ToLower(provider))
	m := strings.TrimSpace(strings.ToLower(model))

	if strings.HasPrefix(m, "gemini-") {
		return true
	}
	switch p {
	case Gemini:
		return true
	case OpenCode:
		return false
	case Cheaper:
		return strings.Contains(m, "muse-spark") ||
			strings.Contains(m, "glm-5.3-flash") ||
			strings.Contains(m, "vision") ||
			strings.Contains(m, "gpt-4o") ||
			strings.Contains(m, "claude-3") ||
			strings.Contains(m, "claude-sonnet") ||
			strings.Contains(m, "claude-opus")
	default:
		return false
	}
}

// SupportsAudio сообщает, поддерживает ли модель данного провайдера входное аудио (input_audio).
func SupportsAudio(provider, model string) bool {
	p := strings.TrimSpace(strings.ToLower(provider))
	m := strings.TrimSpace(strings.ToLower(model))

	if strings.HasPrefix(m, "gemini-") {
		return true
	}
	switch p {
	case Gemini:
		return true
	default:
		return false
	}
}

// MediaTargets отбирает из цепочки таргеты, поддерживающие нужные медиа-возможности.
func MediaTargets(chain []Target, needAudio, needVision bool) []Target {
	if !needAudio && !needVision {
		out := make([]Target, len(chain))
		copy(out, chain)
		return out
	}
	out := make([]Target, 0, len(chain))
	for _, t := range chain {
		if needAudio && !SupportsAudio(t.Provider, t.Model) {
			continue
		}
		if needVision && !SupportsVision(t.Provider, t.Model) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// GeminiPoolChain возвращает бесплатную цепочку Gemini со случайно перемешанными ключами
// (для мультимодальных задач с картинками и аудио, без OpenCode и Cheaper).
func GeminiPoolChain(cfg *config.Config) []Target {
	return MediaTargets(PoolChain(cfg), true, true)
}

type mediaMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type mediaChatReq struct {
	Model    string         `json:"model"`
	Messages []mediaMessage `json:"messages"`
}

type mediaChatResp struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func buildMediaMessages(sysPrompt string, content json.RawMessage) ([]mediaMessage, error) {
	msgs := make([]mediaMessage, 0, 2)
	if strings.TrimSpace(sysPrompt) != "" {
		sysJSON, err := json.Marshal(sysPrompt)
		if err != nil {
			return nil, fmt.Errorf("llm: сериализация системного промпта: %w", err)
		}
		msgs = append(msgs, mediaMessage{Role: "system", Content: sysJSON})
	}
	msgs = append(msgs, mediaMessage{Role: "user", Content: content})
	return msgs, nil
}

// AskParts отправляет мультимодальное сообщение по цепочке таргетов,
// используя пул ключей из контекста или глобальный пул по умолчанию.
func AskParts(ctx context.Context, chain []Target, sysPrompt string, parts []Part) (Result, error) {
	return AskPartsWithPool(ctx, poolFromContext(ctx), chain, sysPrompt, parts)
}

// AskPartsWithPool отправляет мультимодальное сообщение по цепочке таргетов
// с явным пулом ключей (cooldown при 429, ограничение параллельности и ротация ключей).
func AskPartsWithPool(ctx context.Context, pool *KeyPool, chain []Target, sysPrompt string, parts []Part) (Result, error) {
	content, err := BuildContentParts(parts)
	if err != nil {
		return Result{}, err
	}
	msgs, err := buildMediaMessages(sysPrompt, content)
	if err != nil {
		return Result{}, err
	}
	return askChain(ctx, pool, chain, func(callCtx context.Context, t Target) (string, int, http.Header, []byte, error) {
		return callMediaMessages(callCtx, t, msgs)
	})
}

// AskImage отправляет изображение с необязательным текстовым запросом по цепочке таргетов.
func AskImage(ctx context.Context, chain []Target, sysPrompt, prompt string, img []byte, mime string) (Result, error) {
	parts := make([]Part, 0, 2)
	if strings.TrimSpace(prompt) != "" {
		parts = append(parts, TextPart(prompt))
	}
	parts = append(parts, ImagePart(img, mime))
	return AskParts(ctx, chain, sysPrompt, parts)
}

// AskAudio отправляет аудиозапись с необязательным текстовым запросом по цепочке таргетов.
func AskAudio(ctx context.Context, chain []Target, sysPrompt, prompt string, audio []byte, format string) (Result, error) {
	parts := make([]Part, 0, 2)
	if strings.TrimSpace(prompt) != "" {
		parts = append(parts, TextPart(prompt))
	}
	parts = append(parts, AudioPart(audio, format))
	return AskParts(ctx, chain, sysPrompt, parts)
}

func callMediaMessages(ctx context.Context, t Target, msgs []mediaMessage) (string, int, http.Header, []byte, error) {
	payload, extra := buildChatPayload(t, msgs, GenOptsFromContext(ctx))
	text, status, hdr, body, err := postMediaJSON(ctx, t, payload)
	if extra && genParamsRejected(status, body) {
		markGenParamsUnsupported(t)
		payload, _ = buildChatPayload(t, msgs, GenOpts{})
		return postMediaJSON(ctx, t, payload)
	}
	return text, status, hdr, body, err
}

func postMediaJSON(ctx context.Context, t Target, payload map[string]any) (string, int, http.Header, []byte, error) {
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
	req.Header.Set("User-Agent", userAgent)

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

	var out mediaChatResp
	if err := json.Unmarshal(b, &out); err != nil {
		return "", resp.StatusCode, resp.Header, b, fmt.Errorf("не JSON (%d байт): %w", len(b), err)
	}
	if len(out.Choices) == 0 {
		return "", resp.StatusCode, resp.Header, b, errors.New("пустой ответ")
	}
	text, err := extractResponseText(out.Choices[0].Message.Content)
	if err != nil {
		return "", resp.StatusCode, resp.Header, b, err
	}
	if strings.TrimSpace(text) == "" {
		return "", resp.StatusCode, resp.Header, b, errors.New("пустой ответ")
	}
	return strings.TrimSpace(text), resp.StatusCode, resp.Header, b, nil
}

func extractResponseText(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	if trimmed[0] == '[' {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(trimmed, &parts); err != nil {
			return "", err
		}
		var sb strings.Builder
		for _, p := range parts {
			if p.Type != "" && p.Type != "text" && p.Type != "output_text" {
				continue
			}
			if p.Text != "" {
				sb.WriteString(p.Text)
			}
		}
		return sb.String(), nil
	}
	return "", errors.New("неожиданный формат content в ответе")
}

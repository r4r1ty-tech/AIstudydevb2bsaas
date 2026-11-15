package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// Расшифровка аудио через cheaper (админский LLM_API_KEY/LLM_API_URL, OpenAI-совместимый
// POST {база}/audio/transcriptions). Это единственное исключение из правила «cheaper в конспектах
// запрещён» (см. ValidateChain в llm.go): для текста, слайдов и Ask cheaper по-прежнему недоступен,
// а здесь ключ используется только для STT аудио.

const (
	// Длина фрагмента для cheaper: 10 мин при 48 кбит/с — около 3.6 МБ при лимите 20 МБ.
	cheaperSTTChunkDuration = 10 * time.Minute
	// Битрейт mp3 mono 16 кГц для фрагментов cheaper.
	cheaperSTTBitrate = "48k"
	// Сколько фрагментов расшифровываем параллельно.
	cheaperSTTParallel = 4
	// Повторов одного фрагмента при 429 и 5xx сверх первой попытки.
	cheaperSTTRetries = 2
	// Потолок ожидания по заголовку Retry-After.
	cheaperSTTMaxRetryAfter = time.Minute
	// Длина блока одной строки расшифровки (каждый блок получает метку [мм:сс] своего начала).
	cheaperSTTBlock = time.Minute
	// Таймаут одного HTTP-запроса к cheaper.
	cheaperSTTHTTPTimeout = 10 * time.Minute
	// Лимит размера одного загружаемого фрагмента.
	cheaperSTTMaxBytes = 20 << 20
	// Модель, если CHEAPER_STT_MODEL в конфиге пуст (обычно задаётся config.Load).
	cheaperSTTDefaultModel = "whisper-large-v3-turbo"
)

var (
	// cheaperRetryBackoff — базовая пауза между повторами без Retry-After (2 с, затем 4 с). Тесты сокращают.
	cheaperRetryBackoff = 2 * time.Second
	// sliceAudioCheaperFn режет аудио для cheaper. Тесты подменяют нарезку на фейковые файлы.
	sliceAudioCheaperFn = func(ctx context.Context, audioPath string, start, length time.Duration, outMP3 string) error {
		return sliceAudioFFmpegBitrate(ctx, audioPath, start, length, cheaperSTTBitrate, outMP3)
	}
)

// cheaperTranscript — ответ transcriptions с response_format=verbose_json (нужные поля).
type cheaperTranscript struct {
	Text     string           `json:"text"`
	Segments []cheaperSegment `json:"segments"`
}

type cheaperSegment struct {
	Start float64 `json:"start"`
	Text  string  `json:"text"`
}

// cheaperKeyMarker возвращает непустой маркер, если заданы и ключ, и адрес LLM_API_* (провайдер cheaper).
func cheaperKeyMarker(cfg *config.Config) string {
	if cfg == nil || strings.TrimSpace(cfg.LLMAPIKey) == "" || strings.TrimSpace(cfg.LLMAPIURL) == "" {
		return ""
	}
	return "cheaper"
}

// transcribeCheaper расшифровывает аудиозапись через cheaper: нарезка на фрагменты по 10 мин
// с перекрытием, параллельная отправка и склейка, как у Gemini.
func transcribeCheaper(ctx context.Context, cfg *config.Config, audioPath string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tag := lectureOf(ctx).tag()
	if cheaperKeyMarker(cfg) == "" {
		return "", errors.New("нет LLM_API_KEY или LLM_API_URL для cheaper")
	}

	st, err := os.Stat(audioPath)
	if err != nil {
		logx.Errorf("notes", "transcribeCheaper: %s stat %s: %v", tag, audioPath, err)
		return "", err
	}
	if st.Size() < 2048 {
		logx.Errorf("notes", "transcribeCheaper: %s аудио слишком короткое (%d байт)", tag, st.Size())
		return "", fmt.Errorf("аудио слишком короткое (%d байт)", st.Size())
	}

	dur, _, probeErr := probeAudioFn(ctx, audioPath)
	if probeErr != nil || dur <= 0 {
		return "", fmt.Errorf("cheaper stt: не удалось определить длительность аудио: %v", probeErr)
	}
	chunks := planAudioChunks(dur, cheaperSTTChunkDuration, geminiChunkOverlap)

	tmpDir, err := os.MkdirTemp("", "cheaper-stt-*")
	if err != nil {
		return "", fmt.Errorf("cheaper stt: временная директория: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	chunkPaths := make([]string, len(chunks))
	for i, ch := range chunks {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		outMP3 := filepath.Join(tmpDir, fmt.Sprintf("chunk_%03d.mp3", i))
		if err := sliceAudioCheaperFn(ctx, audioPath, ch.Start, ch.Length, outMP3); err != nil {
			return "", fmt.Errorf("cheaper stt: нарезка фрагмента %d: %w", i+1, err)
		}
		info, err := os.Stat(outMP3)
		if err != nil {
			return "", fmt.Errorf("cheaper stt: файл фрагмента %d: %w", i+1, err)
		}
		if info.Size() == 0 {
			return "", fmt.Errorf("cheaper stt: пустой фрагмент %d", i+1)
		}
		if info.Size() > cheaperSTTMaxBytes {
			return "", fmt.Errorf("cheaper stt: фрагмент %d (%d байт) превышает лимит %d байт", i+1, info.Size(), cheaperSTTMaxBytes)
		}
		chunkPaths[i] = outMP3
	}

	workers := min(len(chunks), cheaperSTTParallel)
	logx.Infof("notes", "transcribeCheaper: %s chunks=%d workers=%d dur=%s model=%s", tag, len(chunks), workers, dur.Round(time.Second), cheaperModel(cfg))

	results := make([]string, len(chunks))
	err = runChunks(ctx, len(chunks), workers, func(wctx context.Context, idx int) error {
		text, err := cheaperRecognize(wctx, cfg, chunkPaths[idx], chunks[idx].Start)
		if err != nil {
			return fmt.Errorf("cheaper stt: фрагмент %d: %w", idx+1, err)
		}
		results[idx] = text
		return nil
	})
	if err != nil {
		return "", err
	}

	stitched := stitchTranscripts(results)
	if strings.TrimSpace(stitched) == "" {
		return "", errors.New("cheaper stt: пустой результат расшифровки")
	}
	return stitched, nil
}

// cheaperRecognize отправляет один mp3-фрагмент в cheaper и возвращает строки вида [мм:сс].
// offset — начало фрагмента в записи, метки сдвигаются на него. Ответы 429 и 5xx повторяются
// до cheaperSTTRetries раз: пауза — Retry-After (не больше минуты), иначе линейный backoff.
// Ожидание прерывается отменой ctx.
func cheaperRecognize(ctx context.Context, cfg *config.Config, mp3Path string, offset time.Duration) (string, error) {
	tag := lectureOf(ctx).tag()
	data, err := os.ReadFile(mp3Path) //nolint:gosec // путь строит сам бот, не пользователь
	if err != nil {
		return "", fmt.Errorf("чтение фрагмента: %w", err)
	}
	if len(data) > cheaperSTTMaxBytes {
		return "", fmt.Errorf("фрагмент (%d байт) превышает лимит %d байт", len(data), cheaperSTTMaxBytes)
	}
	for attempt := 0; ; attempt++ {
		tr, status, retryAfter, err := cheaperPost(ctx, cfg, data)
		if err == nil {
			return formatCheaperTranscript(tr, offset)
		}
		retryable := status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
		if !retryable || attempt >= cheaperSTTRetries {
			return "", err
		}
		wait := cheaperRetryBackoff * time.Duration(attempt+1)
		if retryAfter > 0 {
			wait = min(retryAfter, cheaperSTTMaxRetryAfter)
		}
		logx.Warnf("notes", "cheaper stt: %s HTTP %d, повтор %d/%d через %s", tag, status, attempt+1, cheaperSTTRetries, wait)
		if err := sleepCtx(ctx, wait); err != nil {
			return "", err
		}
	}
}

// cheaperPost — одна попытка POST {LLM_API_URL}/audio/transcriptions (multipart, verbose_json).
// Возвращает HTTP-статус (0, если ответа не было) и Retry-After из ответа.
func cheaperPost(ctx context.Context, cfg *config.Config, data []byte) (cheaperTranscript, int, time.Duration, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="chunk.mp3"`)
	hdr.Set("Content-Type", "audio/mpeg")
	part, err := w.CreatePart(hdr)
	if err != nil {
		return cheaperTranscript{}, 0, 0, fmt.Errorf("cheaper stt: multipart: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return cheaperTranscript{}, 0, 0, fmt.Errorf("cheaper stt: multipart: %w", err)
	}
	for _, f := range [][2]string{
		{"model", cheaperModel(cfg)},
		{"language", "ru"},
		{"response_format", "verbose_json"},
	} {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return cheaperTranscript{}, 0, 0, fmt.Errorf("cheaper stt: поле %s: %w", f[0], err)
		}
	}
	if err := w.Close(); err != nil {
		return cheaperTranscript{}, 0, 0, fmt.Errorf("cheaper stt: multipart: %w", err)
	}

	endpoint := cheaperAudioEndpoint(cfg.LLMAPIURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return cheaperTranscript{}, 0, 0, fmt.Errorf("cheaper stt: запрос: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.LLMAPIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	logx.Debugf("notes", "cheaperPost: POST %s bytes=%d key=%s", endpoint, len(data), keyState(cfg.LLMAPIKey))
	cli := &http.Client{Timeout: cheaperSTTHTTPTimeout}
	res, err := cli.Do(req)
	if err != nil {
		return cheaperTranscript{}, 0, 0, fmt.Errorf("cheaper stt: запрос: %s", scrubKey(err.Error(), cfg.LLMAPIKey))
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return cheaperTranscript{}, 0, 0, fmt.Errorf("cheaper stt: ответ оборвался: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := scrubKey(truncate(string(b), 300), cfg.LLMAPIKey)
		retryAfter := parseRetryAfter(res.Header.Get("Retry-After"), time.Now())
		return cheaperTranscript{}, res.StatusCode, retryAfter, fmt.Errorf("cheaper stt HTTP %d: %s", res.StatusCode, msg)
	}
	var tr cheaperTranscript
	if err := json.Unmarshal(b, &tr); err != nil {
		return cheaperTranscript{}, res.StatusCode, 0, fmt.Errorf("cheaper stt: ответ не JSON (%d байт): %w", len(b), err)
	}
	return tr, res.StatusCode, 0, nil
}

// formatCheaperTranscript собирает строки с метками [мм:сс]: сегменты склеиваются в блоки длиной
// cheaperSTTBlock, каждый блок получает метку своего начала, сдвинутую на offset. Без сегментов
// весь текст помечается offset. Пустой результат — ошибка, как и у Gemini.
func formatCheaperTranscript(tr cheaperTranscript, offset time.Duration) (string, error) {
	var lines []string
	if len(tr.Segments) == 0 {
		if t := strings.TrimSpace(tr.Text); t != "" {
			lines = append(lines, cheaperStamp(offset)+" "+t)
		}
	} else {
		var (
			blockStart time.Duration
			started    bool
			parts      []string
		)
		flush := func() {
			if len(parts) > 0 {
				lines = append(lines, cheaperStamp(offset+blockStart)+" "+strings.Join(parts, " "))
				parts = nil
			}
		}
		for _, s := range tr.Segments {
			t := strings.TrimSpace(s.Text)
			if t == "" {
				continue
			}
			at := time.Duration(s.Start * float64(time.Second))
			if !started || at-blockStart >= cheaperSTTBlock {
				flush()
				blockStart = at
				started = true
			}
			parts = append(parts, t)
		}
		flush()
	}
	if len(lines) == 0 {
		return "", errors.New("cheaper stt: пустой текст")
	}
	return strings.Join(lines, "\n"), nil
}

// cheaperStamp форматирует метку времени как [мм:сс]; минуты могут быть больше 59, как в shiftTimestamps.
func cheaperStamp(d time.Duration) string {
	sec := int(d / time.Second)
	return fmt.Sprintf("[%02d:%02d]", sec/60, sec%60)
}

// cheaperAudioEndpoint строит адрес транскрибации тем же правилом, что и internal/llm:
// база с /v1 или /openai берётся как есть, без них добавляется /v1.
func cheaperAudioEndpoint(base string) string {
	chat := llm.ChatEndpoint(base)
	if chat == "" {
		return ""
	}
	return strings.TrimSuffix(chat, "chat/completions") + "audio/transcriptions"
}

// cheaperModel — модель из CHEAPER_STT_MODEL, а при пустом значении — модель по умолчанию.
func cheaperModel(cfg *config.Config) string {
	if m := strings.TrimSpace(cfg.CheaperSTTModel); m != "" {
		return m
	}
	return cheaperSTTDefaultModel
}

// parseRetryAfter разбирает Retry-After (секунды или HTTP-дата). 0, если заголовка нет или он не разобран.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if sec, err := strconv.Atoi(v); err == nil {
		if sec > 0 {
			return time.Duration(sec) * time.Second
		}
		return 0
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// sleepCtx ждёт d или отмены контекста.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// scrubKey убирает ключ из текста ошибки или ответа, чтобы он не попал в логи.
func scrubKey(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "***")
}

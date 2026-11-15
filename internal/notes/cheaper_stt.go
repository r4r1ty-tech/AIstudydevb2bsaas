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
//
// Общая часть для провайдеров с нарезкой на фрагменты (cheaper и Deepgram): transcribeChunkedSTT,
// sttWithRetry, formatSTTSegments. Она же используется как запасной путь для кусков Gemini.

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
	// Таймаут одного HTTP-запроса к cheaper.
	cheaperSTTHTTPTimeout = 10 * time.Minute
	// Лимит размера одного загружаемого фрагмента.
	cheaperSTTMaxBytes = 20 << 20
	// Модель, если CHEAPER_STT_MODEL в конфиге пуст (обычно задаётся config.Load).
	cheaperSTTDefaultModel = "whisper-large-v3-turbo"

	// Длина блока одной строки расшифровки (каждый блок получает метку [мм:сс] своего начала).
	sttBlockDuration = time.Minute
)

var (
	// cheaperRetryBackoff — базовая пауза между повторами без Retry-After (2 с, затем 4 с). Тесты сокращают.
	cheaperRetryBackoff = 2 * time.Second
	// sliceAudioCheaperFn режет аудио для cheaper. Тесты подменяют нарезку на фейковые файлы.
	sliceAudioCheaperFn = func(ctx context.Context, audioPath string, start, length time.Duration, outMP3 string) error {
		return sliceAudioFFmpegBitrate(ctx, audioPath, start, length, cheaperSTTBitrate, outMP3)
	}
)

// sttSegment — сегмент расшифровки: начало в секундах от начала фрагмента и текст.
type sttSegment struct {
	Start float64 `json:"start"`
	Text  string  `json:"text"`
}

// cheaperSegment — сегмент ответа verbose_json от cheaper.
type cheaperSegment = sttSegment

// cheaperTranscript — ответ transcriptions с response_format=verbose_json (нужные поля).
type cheaperTranscript struct {
	Text     string           `json:"text"`
	Segments []cheaperSegment `json:"segments"`
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
	if cheaperKeyMarker(cfg) == "" {
		return "", errors.New("нет LLM_API_KEY или LLM_API_URL для cheaper")
	}
	return transcribeChunkedSTT(ctx, audioPath, sttChunkedSpec{
		name:     "cheaper",
		chunkDur: cheaperSTTChunkDuration,
		parallel: cheaperSTTParallel,
		maxBytes: cheaperSTTMaxBytes,
		slice:    sliceAudioCheaperFn,
		recognize: func(wctx context.Context, mp3Path string, offset time.Duration) (string, error) {
			return cheaperRecognize(wctx, cfg, mp3Path, offset)
		},
		model: cheaperModel(cfg),
	})
}

// sttChunkedSpec описывает провайдера, который режет запись на mp3-фрагменты (cheaper, Deepgram).
type sttChunkedSpec struct {
	// name — имя провайдера для логов и ошибок: cheaper, deepgram.
	name string
	// chunkDur — длина фрагмента; соседние фрагменты перекрываются на geminiChunkOverlap.
	chunkDur time.Duration
	// parallel — сколько фрагментов расшифровываем одновременно.
	parallel int
	// maxBytes — лимит размера загружаемого фрагмента.
	maxBytes int
	// slice режет аудио на фрагмент. Тесты подменяют его через переменную провайдера.
	slice func(ctx context.Context, audioPath string, start, length time.Duration, outMP3 string) error
	// recognize расшифровывает один mp3-фрагмент; offset — его начало в записи.
	recognize func(ctx context.Context, mp3Path string, offset time.Duration) (string, error)
	// model — модель, только для лога.
	model string
}

// transcribeChunkedSTT расшифровывает запись через провайдера spec: нарезка на фрагменты с перекрытием,
// параллельная отправка через runChunks и склейка через stitchTranscripts, как у Gemini.
func transcribeChunkedSTT(ctx context.Context, audioPath string, spec sttChunkedSpec) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tag := lectureOf(ctx).tag()
	name := spec.name

	st, err := os.Stat(audioPath)
	if err != nil {
		logx.Errorf("notes", "%s stt: %s stat %s: %v", name, tag, audioPath, err)
		return "", err
	}
	if st.Size() < 2048 {
		logx.Errorf("notes", "%s stt: %s аудио слишком короткое (%d байт)", name, tag, st.Size())
		return "", fmt.Errorf("аудио слишком короткое (%d байт)", st.Size())
	}

	dur, _, probeErr := probeAudioFn(ctx, audioPath)
	if probeErr != nil || dur <= 0 {
		return "", fmt.Errorf("%s stt: не удалось определить длительность аудио: %v", name, probeErr)
	}
	chunks := planAudioChunks(dur, spec.chunkDur, geminiChunkOverlap)

	tmpDir, err := os.MkdirTemp("", name+"-stt-*")
	if err != nil {
		return "", fmt.Errorf("%s stt: временная директория: %w", name, err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	chunkPaths := make([]string, len(chunks))
	for i, ch := range chunks {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		outMP3 := filepath.Join(tmpDir, fmt.Sprintf("chunk_%03d.mp3", i))
		if err := spec.slice(ctx, audioPath, ch.Start, ch.Length, outMP3); err != nil {
			return "", fmt.Errorf("%s stt: нарезка фрагмента %d: %w", name, i+1, err)
		}
		info, err := os.Stat(outMP3)
		if err != nil {
			return "", fmt.Errorf("%s stt: файл фрагмента %d: %w", name, i+1, err)
		}
		if info.Size() == 0 {
			return "", fmt.Errorf("%s stt: пустой фрагмент %d", name, i+1)
		}
		if info.Size() > int64(spec.maxBytes) {
			return "", fmt.Errorf("%s stt: фрагмент %d (%d байт) превышает лимит %d байт", name, i+1, info.Size(), spec.maxBytes)
		}
		chunkPaths[i] = outMP3
	}

	workers := min(len(chunks), spec.parallel)
	logx.Infof("notes", "%s stt: %s chunks=%d workers=%d dur=%s model=%s", name, tag, len(chunks), workers, dur.Round(time.Second), spec.model)

	results := make([]string, len(chunks))
	err = runChunks(ctx, len(chunks), workers, func(wctx context.Context, idx int) error {
		text, err := spec.recognize(wctx, chunkPaths[idx], chunks[idx].Start)
		if err != nil {
			return fmt.Errorf("%s stt: фрагмент %d: %w", name, idx+1, err)
		}
		results[idx] = text
		return nil
	})
	if err != nil {
		return "", err
	}

	stitched := stitchTranscripts(results)
	if strings.TrimSpace(stitched) == "" {
		return "", fmt.Errorf("%s stt: пустой результат расшифровки", name)
	}
	return stitched, nil
}

// sttRetryPolicy — параметры повторов одного запроса к провайдеру расшифровки.
type sttRetryPolicy struct {
	// retries — повторов сверх первой попытки.
	retries int
	// backoff — базовая пауза без Retry-After; растёт линейно (backoff, 2·backoff, …).
	backoff time.Duration
	// maxRetryAfter — потолок ожидания по заголовку Retry-After.
	maxRetryAfter time.Duration
}

// sttAttemptFn — одна попытка запроса. status — HTTP-статус (0, если ответа не было), retryAfter — из заголовка.
type sttAttemptFn func() (text string, status int, retryAfter time.Duration, err error)

// sttWithRetry повторяет попытку при 429 и 5xx. Пауза — Retry-After (не больше p.maxRetryAfter),
// иначе линейный backoff. Ожидание прерывается отменой ctx. Остальные ошибки и успех возвращаются сразу.
func sttWithRetry(ctx context.Context, name string, p sttRetryPolicy, attempt sttAttemptFn) (string, error) {
	tag := lectureOf(ctx).tag()
	for n := 0; ; n++ {
		text, status, retryAfter, err := attempt()
		if err == nil {
			return text, nil
		}
		retryable := status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
		if !retryable || n >= p.retries {
			return "", err
		}
		wait := p.backoff * time.Duration(n+1)
		if retryAfter > 0 {
			wait = min(retryAfter, p.maxRetryAfter)
		}
		logx.Warnf("notes", "%s stt: %s HTTP %d, повтор %d/%d через %s", name, tag, status, n+1, p.retries, wait)
		if err := sleepCtx(ctx, wait); err != nil {
			return "", err
		}
	}
}

// readSTTChunk читает mp3-фрагмент и проверяет лимит размера.
func readSTTChunk(mp3Path string, maxBytes int) ([]byte, error) {
	data, err := os.ReadFile(mp3Path) //nolint:gosec // путь строит сам бот, не пользователь
	if err != nil {
		return nil, fmt.Errorf("чтение фрагмента: %w", err)
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("фрагмент (%d байт) превышает лимит %d байт", len(data), maxBytes)
	}
	return data, nil
}

// cheaperRecognize отправляет один mp3-фрагмент в cheaper и возвращает строки вида [мм:сс].
// offset — начало фрагмента в записи, метки сдвигаются на него. Повторы — sttWithRetry.
func cheaperRecognize(ctx context.Context, cfg *config.Config, mp3Path string, offset time.Duration) (string, error) {
	data, err := readSTTChunk(mp3Path, cheaperSTTMaxBytes)
	if err != nil {
		return "", err
	}
	return sttWithRetry(ctx, "cheaper", sttRetryPolicy{
		retries:       cheaperSTTRetries,
		backoff:       cheaperRetryBackoff,
		maxRetryAfter: cheaperSTTMaxRetryAfter,
	}, func() (string, int, time.Duration, error) {
		tr, status, retryAfter, err := cheaperPost(ctx, cfg, data)
		if err != nil {
			return "", status, retryAfter, err
		}
		text, err := formatCheaperTranscript(tr, offset)
		return text, status, 0, err
	})
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

// formatCheaperTranscript собирает строки расшифровки cheaper с метками [мм:сс] (см. formatSTTSegments).
func formatCheaperTranscript(tr cheaperTranscript, offset time.Duration) (string, error) {
	return formatSTTSegments(tr.Segments, tr.Text, offset)
}

// formatSTTSegments собирает строки с метками [мм:сс] для cheaper и Deepgram: сегменты склеиваются
// в блоки длиной sttBlockDuration, каждый блок получает метку своего начала, сдвинутую на offset.
// Без сегментов весь text помечается offset. Пустой результат — ошибка, как и у Gemini.
func formatSTTSegments(segs []sttSegment, text string, offset time.Duration) (string, error) {
	var lines []string
	if len(segs) == 0 {
		if t := strings.TrimSpace(text); t != "" {
			lines = append(lines, sttStamp(offset)+" "+t)
		}
	} else {
		var (
			blockStart time.Duration
			started    bool
			parts      []string
		)
		flush := func() {
			if len(parts) > 0 {
				lines = append(lines, sttStamp(offset+blockStart)+" "+strings.Join(parts, " "))
				parts = nil
			}
		}
		for _, s := range segs {
			t := strings.TrimSpace(s.Text)
			if t == "" {
				continue
			}
			at := time.Duration(s.Start * float64(time.Second))
			if !started || at-blockStart >= sttBlockDuration {
				flush()
				blockStart = at
				started = true
			}
			parts = append(parts, t)
		}
		flush()
	}
	if len(lines) == 0 {
		return "", errors.New("пустой текст")
	}
	return strings.Join(lines, "\n"), nil
}

// sttStamp форматирует метку времени как [мм:сс]; минуты могут быть больше 59, как в shiftTimestamps.
func sttStamp(d time.Duration) string {
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

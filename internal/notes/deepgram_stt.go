package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// Расшифровка аудио через Deepgram (DEEPGRAM_API_KEY, DEEPGRAM_API_URL, DEEPGRAM_MODEL).
// Запрос: POST {DEEPGRAM_API_URL}/v1/listen?model=…&language=ru&smart_format=true&punctuate=true&utterances=true,
// тело — сырые байты mp3-фрагмента, авторизация «Token <ключ>». Нарезка, параллельность и склейка —
// общие с cheaper (transcribeChunkedSTT), строки [мм:сс] — formatSTTSegments.

const (
	// Длина фрагмента для Deepgram: 10 мин при 48 кбит/с — около 3.6 МБ.
	deepgramSTTChunkDuration = 10 * time.Minute
	// Битрейт mp3 mono 16 кГц для фрагментов Deepgram.
	deepgramSTTBitrate = "48k"
	// Сколько фрагментов расшифровываем параллельно.
	deepgramSTTParallel = 4
	// Повторов одного фрагмента при 429 и 5xx сверх первой попытки.
	deepgramSTTRetries = 2
	// Потолок ожидания по заголовку Retry-After.
	deepgramSTTMaxRetryAfter = time.Minute
	// Таймаут одного HTTP-запроса к Deepgram.
	deepgramSTTHTTPTimeout = 10 * time.Minute
	// Лимит размера одного загружаемого фрагмента.
	deepgramSTTMaxBytes = 20 << 20
	// Модель, если DEEPGRAM_MODEL в конфиге пуст (обычно задаётся config.Load).
	deepgramSTTDefaultModel = "nova-3"
	// Язык расшифровки.
	deepgramSTTLanguage = "ru"
)

var (
	// deepgramRetryBackoff — базовая пауза между повторами без Retry-After (2 с, затем 4 с). Тесты сокращают.
	deepgramRetryBackoff = 2 * time.Second
	// sliceAudioDeepgramFn режет аудио для Deepgram. Тесты подменяют нарезку на фейковые файлы.
	sliceAudioDeepgramFn = func(ctx context.Context, audioPath string, start, length time.Duration, outMP3 string) error {
		return sliceAudioFFmpegBitrate(ctx, audioPath, start, length, deepgramSTTBitrate, outMP3)
	}
)

// deepgramResponse — нужные поля ответа /v1/listen.
type deepgramResponse struct {
	Results struct {
		Channels []struct {
			Alternatives []struct {
				Transcript string `json:"transcript"`
			} `json:"alternatives"`
		} `json:"channels"`
		Utterances []deepgramUtterance `json:"utterances"`
	} `json:"results"`
}

// deepgramUtterance — реплика с началом в секундах от начала фрагмента.
type deepgramUtterance struct {
	Start      float64 `json:"start"`
	Transcript string  `json:"transcript"`
}

// firstTranscript возвращает channels[0].alternatives[0].transcript или пустую строку.
func (r deepgramResponse) firstTranscript() string {
	if len(r.Results.Channels) == 0 || len(r.Results.Channels[0].Alternatives) == 0 {
		return ""
	}
	return r.Results.Channels[0].Alternatives[0].Transcript
}

// deepgramKeyMarker возвращает непустой маркер, только если заданы и ключ, и адрес Deepgram.
func deepgramKeyMarker(cfg *config.Config) string {
	if cfg == nil || strings.TrimSpace(cfg.DeepgramAPIKey) == "" || strings.TrimSpace(cfg.DeepgramAPIURL) == "" {
		return ""
	}
	return "deepgram"
}

// transcribeDeepgram расшифровывает аудиозапись через Deepgram: нарезка на фрагменты по 10 мин,
// до 4 параллельных запросов, склейка перекрытий, как у cheaper.
func transcribeDeepgram(ctx context.Context, cfg *config.Config, audioPath string) (string, error) {
	if deepgramKeyMarker(cfg) == "" {
		return "", errors.New("нет DEEPGRAM_API_KEY или DEEPGRAM_API_URL")
	}
	return transcribeChunkedSTT(ctx, audioPath, sttChunkedSpec{
		name:     "deepgram",
		chunkDur: deepgramSTTChunkDuration,
		parallel: deepgramSTTParallel,
		maxBytes: deepgramSTTMaxBytes,
		slice:    sliceAudioDeepgramFn,
		recognize: func(wctx context.Context, mp3Path string, offset time.Duration) (string, error) {
			return deepgramRecognize(wctx, cfg, mp3Path, offset)
		},
		model: deepgramModel(cfg),
	})
}

// deepgramRecognize отправляет один mp3-фрагмент в Deepgram и возвращает строки вида [мм:сс].
// offset — начало фрагмента в записи. Повторы при 429 и 5xx — sttWithRetry.
func deepgramRecognize(ctx context.Context, cfg *config.Config, mp3Path string, offset time.Duration) (string, error) {
	data, err := readSTTChunk(mp3Path, deepgramSTTMaxBytes)
	if err != nil {
		return "", err
	}
	return sttWithRetry(ctx, "deepgram", sttRetryPolicy{
		retries:       deepgramSTTRetries,
		backoff:       deepgramRetryBackoff,
		maxRetryAfter: deepgramSTTMaxRetryAfter,
	}, func() (string, int, time.Duration, error) {
		dr, status, retryAfter, err := deepgramPost(ctx, cfg, data)
		if err != nil {
			return "", status, retryAfter, err
		}
		segs := make([]sttSegment, 0, len(dr.Results.Utterances))
		for _, u := range dr.Results.Utterances {
			segs = append(segs, sttSegment{Start: u.Start, Text: u.Transcript})
		}
		text, err := formatSTTSegments(segs, dr.firstTranscript(), offset)
		return text, status, 0, err
	})
}

// deepgramPost — одна попытка POST /v1/listen. Возвращает HTTP-статус (0, если ответа не было)
// и Retry-After. Ключ нигде не попадает в текст ошибки.
func deepgramPost(ctx context.Context, cfg *config.Config, data []byte) (deepgramResponse, int, time.Duration, error) {
	endpoint := deepgramListenURL(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return deepgramResponse{}, 0, 0, fmt.Errorf("deepgram stt: запрос: %s", scrubKey(err.Error(), cfg.DeepgramAPIKey))
	}
	req.Header.Set("Authorization", "Token "+cfg.DeepgramAPIKey)
	req.Header.Set("Content-Type", "audio/mpeg")

	logx.Debugf("notes", "deepgramPost: POST %s bytes=%d key=%s", endpoint, len(data), keyState(cfg.DeepgramAPIKey))
	cli := &http.Client{Timeout: deepgramSTTHTTPTimeout}
	res, err := cli.Do(req)
	if err != nil {
		return deepgramResponse{}, 0, 0, fmt.Errorf("deepgram stt: запрос: %s", scrubKey(err.Error(), cfg.DeepgramAPIKey))
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return deepgramResponse{}, 0, 0, fmt.Errorf("deepgram stt: ответ оборвался: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := scrubKey(truncate(deepgramErrorText(b), 300), cfg.DeepgramAPIKey)
		retryAfter := parseRetryAfter(res.Header.Get("Retry-After"), time.Now())
		return deepgramResponse{}, res.StatusCode, retryAfter, fmt.Errorf("deepgram stt HTTP %d: %s", res.StatusCode, msg)
	}
	var dr deepgramResponse
	if err := json.Unmarshal(b, &dr); err != nil {
		return deepgramResponse{}, res.StatusCode, 0, fmt.Errorf("deepgram stt: ответ не JSON (%d байт): %w", len(b), err)
	}
	return dr, res.StatusCode, 0, nil
}

// deepgramListenURL строит адрес /v1/listen с параметрами расшифровки на русском языке.
func deepgramListenURL(cfg *config.Config) string {
	q := url.Values{}
	q.Set("model", deepgramModel(cfg))
	q.Set("language", deepgramSTTLanguage)
	q.Set("smart_format", "true")
	q.Set("punctuate", "true")
	q.Set("utterances", "true")
	return strings.TrimRight(strings.TrimSpace(cfg.DeepgramAPIURL), "/") + "/v1/listen?" + q.Encode()
}

// deepgramErrorText достаёт сообщение из тела ошибки Deepgram: {"err_msg":…} или {"message":…}.
// Если формат другой, возвращает тело как есть.
func deepgramErrorText(b []byte) string {
	var e struct {
		ErrMsg  string `json:"err_msg"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) == nil {
		if e.ErrMsg != "" {
			return e.ErrMsg
		}
		if e.Message != "" {
			return e.Message
		}
	}
	return string(b)
}

// deepgramModel — модель из DEEPGRAM_MODEL, а при пустом значении — модель по умолчанию.
func deepgramModel(cfg *config.Config) string {
	if m := strings.TrimSpace(cfg.DeepgramModel); m != "" {
		return m
	}
	return deepgramSTTDefaultModel
}

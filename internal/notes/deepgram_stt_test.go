package notes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
)

// stubDeepgramSlices подменяет нарезку Deepgram: фрагмент — файл с содержимым DG-<начало, с>.
func stubDeepgramSlices(t *testing.T) {
	t.Helper()
	orig := sliceAudioDeepgramFn
	t.Cleanup(func() { sliceAudioDeepgramFn = orig })
	sliceAudioDeepgramFn = func(ctx context.Context, audioPath string, start, length time.Duration, outMP3 string) error {
		return os.WriteFile(outMP3, []byte(fmt.Sprintf("DG-%d", int(start.Seconds()))), 0o600)
	}
}

// stubDeepgramBackoff убирает паузы повторов Deepgram.
func stubDeepgramBackoff(t *testing.T) {
	t.Helper()
	orig := deepgramRetryBackoff
	t.Cleanup(func() { deepgramRetryBackoff = orig })
	deepgramRetryBackoff = time.Millisecond
}

// dgReply пишет ответ /v1/listen: общий текст канала и реплики (utterances, может быть пусто).
func dgReply(w http.ResponseWriter, transcript string, utts ...deepgramUtterance) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"metadata": map[string]any{"duration": 60.0},
		"results": map[string]any{
			"channels":   []map[string]any{{"alternatives": []map[string]string{{"transcript": transcript}}}},
			"utterances": utts,
		},
	})
}

func TestDeepgramRequestShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/listen" {
			t.Errorf("запрос %s %s, ждали POST /v1/listen", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		for k, want := range map[string]string{
			"model":        "nova-3",
			"language":     "ru",
			"smart_format": "true",
			"punctuate":    "true",
			"utterances":   "true",
		} {
			if got := q.Get(k); got != want {
				t.Errorf("параметр %s = %q, ждали %q", k, got, want)
			}
		}
		if got := r.Header.Get("Authorization"); got != "Token dg-test" {
			t.Errorf("Authorization = %q, ждали Token dg-test", got)
		}
		if got := r.Header.Get("Content-Type"); got != "audio/mpeg" {
			t.Errorf("Content-Type = %q, ждали audio/mpeg", got)
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("чтение тела: %v", err)
		}
		if string(b) != "CHUNK-BYTES" {
			t.Errorf("тело = %q, ждали сырые байты фрагмента", b)
		}
		dgReply(w, "тест", deepgramUtterance{Start: 1, Transcript: "тест"})
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{DeepgramAPIKey: "dg-test", DeepgramAPIURL: srv.URL, DeepgramModel: "nova-3"}
	got, err := deepgramRecognize(context.Background(), cfg, writeTestFile(t, "CHUNK-BYTES"), 0)
	if err != nil {
		t.Fatalf("deepgramRecognize: %v", err)
	}
	if got != "[00:01] тест" {
		t.Fatalf("got %q", got)
	}
}

func TestDeepgramTimestampsAcrossChunks(t *testing.T) {
	stubDeepgramSlices(t)
	stubAudioProbe(t, 22*time.Minute) // фрагменты: 0, 592 с и 1184 с

	replies := map[string][]deepgramUtterance{
		"DG-0": {
			{Start: 0.5, Transcript: "Введение в теорию графов."},
			{Start: 65, Transcript: "Сначала определим вершины."},
		},
		"DG-592": {
			{Start: 2, Transcript: "Далее рассматриваем деревья поиска."},
			{Start: 70, Transcript: "Балансировка красно чёрных деревьев."},
		},
		"DG-1184": {
			{Start: 1, Transcript: "Хеш таблицы и коллизии."},
			{Start: 30, Transcript: "Конец лекции."},
		},
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		utts, ok := replies[string(b)]
		if !ok {
			http.Error(w, "неожиданный фрагмент "+string(b), http.StatusBadRequest)
			return
		}
		dgReply(w, "", utts...)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{DeepgramAPIKey: "dg-test", DeepgramAPIURL: srv.URL}
	got, err := transcribeDeepgram(context.Background(), cfg, lectureAudio(t))
	if err != nil {
		t.Fatalf("transcribeDeepgram: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("запросов %d, ждали 3 (по одному на фрагмент)", calls.Load())
	}
	ordered := []string{
		"[00:00] Введение в теорию графов.",
		"[01:05] Сначала определим вершины.",
		"[09:54] Далее рассматриваем деревья поиска.",
		"[11:02] Балансировка красно чёрных деревьев.",
		"[19:45] Хеш таблицы и коллизии. Конец лекции.",
	}
	last := -1
	for _, want := range ordered {
		i := strings.Index(got, want)
		if i < 0 {
			t.Fatalf("в результате нет %q:\n%s", want, got)
		}
		if i < last {
			t.Fatalf("строка %q не на своём месте:\n%s", want, got)
		}
		last = i
	}
}

func TestDeepgramEmptyUtterancesFallsBackToTranscript(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dgReply(w, "  весь текст фрагмента  ")
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{DeepgramAPIKey: "dg-test", DeepgramAPIURL: srv.URL}
	got, err := deepgramRecognize(context.Background(), cfg, writeTestFile(t, "CH"), 2*time.Minute)
	if err != nil {
		t.Fatalf("deepgramRecognize: %v", err)
	}
	if got != "[02:00] весь текст фрагмента" {
		t.Fatalf("got %q", got)
	}

	srvEmpty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dgReply(w, "   ")
	}))
	t.Cleanup(srvEmpty.Close)
	cfgEmpty := &config.Config{DeepgramAPIKey: "dg-test", DeepgramAPIURL: srvEmpty.URL}
	if _, err := deepgramRecognize(context.Background(), cfgEmpty, writeTestFile(t, "CH"), 0); err == nil {
		t.Fatal("пустая расшифровка должна быть ошибкой")
	}
}

func TestDeepgramRetry429HonorsRetryAfter(t *testing.T) {
	stubDeepgramBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"err_code":"TOO_MANY","err_msg":"rate limit"}`, http.StatusTooManyRequests)
			return
		}
		dgReply(w, "", deepgramUtterance{Start: 3, Transcript: "Повтор прошёл."})
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{DeepgramAPIKey: "dg-test", DeepgramAPIURL: srv.URL}
	start := time.Now()
	got, err := deepgramRecognize(context.Background(), cfg, writeTestFile(t, "DG-0"), 0)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("deepgramRecognize: %v", err)
	}
	if got != "[00:03] Повтор прошёл." {
		t.Fatalf("got %q", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("запросов %d, ждали 2 (429 и повтор)", calls.Load())
	}
	if elapsed < time.Second {
		t.Fatalf("Retry-After: 1 не выдержан, прошло %s", elapsed)
	}
}

func TestDeepgramErrorDoesNotLeakKey(t *testing.T) {
	stubDeepgramBackoff(t)
	t.Run("5xx: два повтора, ключ не попадает в ошибку", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"category":"UNAVAILABLE","message":"upstream down, echo dg-secret"}`))
		}))
		t.Cleanup(srv.Close)

		cfg := &config.Config{DeepgramAPIKey: "dg-secret", DeepgramAPIURL: srv.URL}
		_, err := deepgramRecognize(context.Background(), cfg, writeTestFile(t, "DG-0"), 0)
		if err == nil {
			t.Fatal("ждали ошибку после повторов")
		}
		if calls.Load() != 3 {
			t.Fatalf("запросов %d, ждали 3 (1 + 2 повтора)", calls.Load())
		}
		if !strings.Contains(err.Error(), "503") {
			t.Fatalf("в ошибке нет статуса: %v", err)
		}
		if strings.Contains(err.Error(), "dg-secret") {
			t.Fatalf("ключ попал в ошибку: %v", err)
		}
	})
	t.Run("400 не повторяется, текст err_msg попадает в ошибку", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"err_code":"INVALID_AUDIO","err_msg":"bad audio"}`))
		}))
		t.Cleanup(srv.Close)

		cfg := &config.Config{DeepgramAPIKey: "dg-secret", DeepgramAPIURL: srv.URL}
		_, err := deepgramRecognize(context.Background(), cfg, writeTestFile(t, "DG-0"), 0)
		if err == nil {
			t.Fatal("ждали ошибку на 400")
		}
		if calls.Load() != 1 {
			t.Fatalf("запросов %d, ждали 1", calls.Load())
		}
		if !strings.Contains(err.Error(), "bad audio") || strings.Contains(err.Error(), "dg-secret") {
			t.Fatalf("ошибка = %v", err)
		}
	})
}

func TestDeepgramCtxCancelInterruptsRetryWait(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "rate limit", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	time.AfterFunc(100*time.Millisecond, cancel)

	cfg := &config.Config{DeepgramAPIKey: "dg", DeepgramAPIURL: srv.URL}
	start := time.Now()
	_, err := deepgramRecognize(ctx, cfg, writeTestFile(t, "DG-0"), 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, ждали context.Canceled", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("отмена не прервала ожидание Retry-After 60 с: прошло %s", d)
	}
}

func TestDeepgramKeyMarkerAndModel(t *testing.T) {
	if deepgramKeyMarker(nil) != "" ||
		deepgramKeyMarker(&config.Config{DeepgramAPIURL: "https://api.deepgram.com"}) != "" ||
		deepgramKeyMarker(&config.Config{DeepgramAPIKey: "dg"}) != "" {
		t.Fatal("без ключа или без адреса Deepgram должен быть пропущен")
	}
	if deepgramKeyMarker(&config.Config{DeepgramAPIKey: "dg", DeepgramAPIURL: "https://api.deepgram.com"}) == "" {
		t.Fatal("с ключом и адресом Deepgram должен быть доступен")
	}

	if got := deepgramModel(&config.Config{}); got != deepgramSTTDefaultModel {
		t.Fatalf("модель по умолчанию = %q", got)
	}
	if got := deepgramModel(&config.Config{DeepgramModel: " nova-2 "}); got != "nova-2" {
		t.Fatalf("модель из конфига = %q", got)
	}
	if _, err := transcribeDeepgram(context.Background(), &config.Config{DeepgramAPIURL: "https://h"}, lectureAudio(t)); err == nil {
		t.Fatal("transcribeDeepgram без ключа должен возвращать ошибку")
	}
}

func TestDeepgramSkippedWithoutKey(t *testing.T) {
	var dgCalls, fishCalls atomic.Int32
	dgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dgCalls.Add(1)
		dgReply(w, "не должно быть")
	}))
	t.Cleanup(dgSrv.Close)
	fishSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fishCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"fish целиком"}`))
	}))
	t.Cleanup(fishSrv.Close)

	cfg := &config.Config{
		DeepgramAPIURL:   dgSrv.URL,
		FishStudioAPIKey: "fk", FishStudioAPIURL: fishSrv.URL,
	}
	got, err := Transcribe(context.Background(), cfg, lectureAudio(t))
	if err != nil || got != "fish целиком" {
		t.Fatalf("got %q, err %v", got, err)
	}
	if dgCalls.Load() != 0 {
		t.Fatalf("deepgram вызван без ключа: %d", dgCalls.Load())
	}
	if fishCalls.Load() != 1 {
		t.Fatalf("fish вызван %d раз, ждали 1", fishCalls.Load())
	}
}

// TestTranscribeOrderDeepgramFirst проверяет порядок deepgram → gemini → cheaper.
func TestTranscribeOrderDeepgramFirst(t *testing.T) {
	stubAudioProbe(t, 5*time.Minute)
	stubDeepgramSlices(t)
	stubCheaperSlices(t)
	stubDeepgramBackoff(t)
	origSlice := sliceAudioFn
	t.Cleanup(func() { sliceAudioFn = origSlice })
	// Нарезка Gemini падает целиком: это провал провайдера, а не одного фрагмента.
	sliceAudioFn = func(ctx context.Context, audioPath string, start, length time.Duration, outMP3 string) error {
		return errors.New("ffmpeg сломался")
	}
	origURL := llm.GeminiURL
	t.Cleanup(func() { llm.GeminiURL = origURL })

	t.Run("deepgram работает: cheaper не вызывается", func(t *testing.T) {
		var geminiCalls, dgCalls, cheaperCalls atomic.Int32
		geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			geminiCalls.Add(1)
			http.Error(w, "must not be called", http.StatusInternalServerError)
		}))
		t.Cleanup(geminiSrv.Close)
		llm.GeminiURL = geminiSrv.URL

		dgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dgCalls.Add(1)
			dgReply(w, "расшифровка deepgram целиком")
		}))
		t.Cleanup(dgSrv.Close)
		cheapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cheaperCalls.Add(1)
			verboseReply(w, "cheaper must not be called")
		}))
		t.Cleanup(cheapSrv.Close)

		cfg := &config.Config{
			GeminiAPIKeys:  []string{"K1:gk"},
			DeepgramAPIKey: "dg", DeepgramAPIURL: dgSrv.URL,
			LLMAPIKey: "ck", LLMAPIURL: cheapSrv.URL + "/v1",
		}
		ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
		got, err := Transcribe(ctx, cfg, lectureAudio(t))
		if err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		if got != "[00:00] расшифровка deepgram целиком" {
			t.Fatalf("got %q", got)
		}
		if geminiCalls.Load() != 0 || dgCalls.Load() != 1 || cheaperCalls.Load() != 0 {
			t.Fatalf("вызовы gemini=%d deepgram=%d cheaper=%d, ждали 0/1/0",
				geminiCalls.Load(), dgCalls.Load(), cheaperCalls.Load())
		}
	})

	t.Run("deepgram не справился: откат на cheaper", func(t *testing.T) {
		var dgCalls, cheaperCalls atomic.Int32
		geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "must not be called", http.StatusInternalServerError)
		}))
		t.Cleanup(geminiSrv.Close)
		llm.GeminiURL = geminiSrv.URL

		dgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dgCalls.Add(1)
			http.Error(w, `{"err_msg":"bad audio"}`, http.StatusBadRequest)
		}))
		t.Cleanup(dgSrv.Close)
		cheapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cheaperCalls.Add(1)
			verboseReply(w, "расшифровка cheaper целиком")
		}))
		t.Cleanup(cheapSrv.Close)

		cfg := &config.Config{
			GeminiAPIKeys:  []string{"K1:gk"},
			DeepgramAPIKey: "dg", DeepgramAPIURL: dgSrv.URL,
			LLMAPIKey: "ck", LLMAPIURL: cheapSrv.URL + "/v1",
		}
		ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
		got, err := Transcribe(ctx, cfg, lectureAudio(t))
		if err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		if got != "[00:00] расшифровка cheaper целиком" {
			t.Fatalf("got %q", got)
		}
		if dgCalls.Load() != 1 || cheaperCalls.Load() != 1 {
			t.Fatalf("вызовы deepgram=%d cheaper=%d, ждали 1/1", dgCalls.Load(), cheaperCalls.Load())
		}
	})
}

// TestGeminiChunkFallbackToDeepgram: кусок, который Gemini не расшифровал, уходит в Deepgram;
// cheaper и fish при этом не вызываются.
func TestGeminiChunkFallbackToDeepgram(t *testing.T) {
	var (
		mu           sync.Mutex
		geminiCounts = map[string]int{}
		dgCalls      atomic.Int32
		cheaperCalls atomic.Int32
		fishCalls    atomic.Int32
	)
	setupGeminiFailingChunk(t, geminiCounts, &mu)

	dgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dgCalls.Add(1)
		b, _ := io.ReadAll(r.Body)
		// Откат получает именно исходный фрагмент Gemini, а не файл другого куска.
		if string(b) != "MP3-CHUNK-592" {
			http.Error(w, "неожиданный фрагмент "+string(b), http.StatusBadRequest)
			return
		}
		dgReply(w, "", deepgramUtterance{Start: 2, Transcript: "Матрица смежности графа."}, deepgramUtterance{Start: 20, Transcript: "Её свойства."})
	}))
	t.Cleanup(dgSrv.Close)
	cheapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cheaperCalls.Add(1)
		verboseReply(w, "не должно быть")
	}))
	t.Cleanup(cheapSrv.Close)
	fishSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fishCalls.Add(1)
		http.Error(w, "fish must not be called", http.StatusInternalServerError)
	}))
	t.Cleanup(fishSrv.Close)

	cfg := &config.Config{
		GeminiAPIKeys:  []string{"K1:gk"},
		DeepgramAPIKey: "dg", DeepgramAPIURL: dgSrv.URL,
		LLMAPIKey: "ck", LLMAPIURL: cheapSrv.URL + "/v1",
		FishStudioAPIKey: "fk", FishStudioAPIURL: fishSrv.URL,
	}
	ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
	got, err := Transcribe(ctx, cfg, lectureAudio(t))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	for _, want := range []string{
		"[00:05] Введение в теорию графов.",
		"[09:50] Вершины и рёбра графа.",
		"[09:54] Матрица смежности графа. Её свойства.",
		"[19:45] Список рёбер и вершин.",
		"[21:04] Конец.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("в результате нет %q:\n%s", want, got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if geminiCounts["MP3-CHUNK-0"] != 1 || geminiCounts["MP3-CHUNK-1184"] != 1 {
		t.Fatalf("исправные фрагменты Gemini пересчитаны: %v", geminiCounts)
	}
	if dgCalls.Load() != 1 || cheaperCalls.Load() != 0 || fishCalls.Load() != 0 {
		t.Fatalf("вызовы deepgram=%d cheaper=%d fish=%d, ждали 1/0/0", dgCalls.Load(), cheaperCalls.Load(), fishCalls.Load())
	}
}

// TestGeminiChunkFallbackDeepgramFailsThenCheaper: Deepgram не справился с куском — пробуем cheaper.
func TestGeminiChunkFallbackDeepgramFailsThenCheaper(t *testing.T) {
	var (
		mu           sync.Mutex
		geminiCounts = map[string]int{}
		dgCalls      atomic.Int32
		cheaperCalls atomic.Int32
	)
	setupGeminiFailingChunk(t, geminiCounts, &mu)

	dgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dgCalls.Add(1)
		http.Error(w, `{"err_msg":"bad audio"}`, http.StatusBadRequest)
	}))
	t.Cleanup(dgSrv.Close)
	cheapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cheaperCalls.Add(1)
		tag, err := multipartFileTag(r)
		if err != nil || tag != "MP3-CHUNK-592" {
			http.Error(w, "неожиданный фрагмент "+tag, http.StatusBadRequest)
			return
		}
		verboseReply(w, "", cheaperSegment{Start: 2, Text: "Матрица смежности графа."}, cheaperSegment{Start: 20, Text: "Её свойства."})
	}))
	t.Cleanup(cheapSrv.Close)

	cfg := &config.Config{
		GeminiAPIKeys:  []string{"K1:gk"},
		DeepgramAPIKey: "dg", DeepgramAPIURL: dgSrv.URL,
		LLMAPIKey: "ck", LLMAPIURL: cheapSrv.URL + "/v1",
	}
	ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
	got, err := Transcribe(ctx, cfg, lectureAudio(t))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if !strings.Contains(got, "[09:54] Матрица смежности графа. Её свойства.") {
		t.Fatalf("в результате нет фрагмента cheaper:\n%s", got)
	}
	if dgCalls.Load() != 1 || cheaperCalls.Load() != 1 {
		t.Fatalf("вызовы deepgram=%d cheaper=%d, ждали 1/1", dgCalls.Load(), cheaperCalls.Load())
	}
}

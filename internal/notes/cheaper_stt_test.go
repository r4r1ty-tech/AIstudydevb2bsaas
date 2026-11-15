package notes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
)

// stubCheaperSlices подменяет нарезку cheaper: фрагмент — файл с содержимым CH-<начало, с>.
func stubCheaperSlices(t *testing.T) {
	t.Helper()
	orig := sliceAudioCheaperFn
	t.Cleanup(func() { sliceAudioCheaperFn = orig })
	sliceAudioCheaperFn = func(ctx context.Context, audioPath string, start, length time.Duration, outMP3 string) error {
		return os.WriteFile(outMP3, []byte(fmt.Sprintf("CH-%d", int(start.Seconds()))), 0o600)
	}
}

// stubGeminiSlices подменяет нарезку Gemini: фрагмент — файл с содержимым MP3-CHUNK-<начало, с>.
func stubGeminiSlices(t *testing.T) {
	t.Helper()
	orig := sliceAudioFn
	t.Cleanup(func() { sliceAudioFn = orig })
	sliceAudioFn = func(ctx context.Context, audioPath string, start, length time.Duration, outMP3 string) error {
		return os.WriteFile(outMP3, []byte(fmt.Sprintf("MP3-CHUNK-%d", int(start.Seconds()))), 0o600)
	}
}

// stubAudioProbe подменяет длительность записи.
func stubAudioProbe(t *testing.T, dur time.Duration) {
	t.Helper()
	orig := probeAudioFn
	t.Cleanup(func() { probeAudioFn = orig })
	probeAudioFn = func(ctx context.Context, path string) (time.Duration, float64, error) {
		return dur, -18.0, nil
	}
}

// stubBackoff убирает паузы повторов cheaper.
func stubBackoff(t *testing.T) {
	t.Helper()
	orig := cheaperRetryBackoff
	t.Cleanup(func() { cheaperRetryBackoff = orig })
	cheaperRetryBackoff = time.Millisecond
}

func writeTestFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "chunk.mp3")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func lectureAudio(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lecture.ogg")
	if err := os.WriteFile(p, []byte(strings.Repeat("a", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// verboseReply пишет ответ transcriptions в формате verbose_json.
func verboseReply(w http.ResponseWriter, text string, segs ...cheaperSegment) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cheaperTranscript{Text: text, Segments: segs})
}

// multipartFileTag читает содержимое поля file из multipart-запроса cheaper.
func multipartFileTag(r *http.Request) (string, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return "", err
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	return string(b), err
}

// geminiAudioTag достаёт содержимое аудио (input_audio, base64) из запроса Gemini.
func geminiAudioTag(r *http.Request) (string, error) {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", err
	}
	for _, m := range req.Messages {
		var parts []struct {
			Type       string `json:"type"`
			InputAudio struct {
				Data string `json:"data"`
			} `json:"input_audio"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "input_audio" {
				b, err := base64.StdEncoding.DecodeString(p.InputAudio.Data)
				return string(b), err
			}
		}
	}
	return "", errors.New("нет input_audio в запросе")
}

func writeGeminiReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"content": text}}},
	})
}

func TestCheaperTranscribeFormAndTimestamps(t *testing.T) {
	stubCheaperSlices(t)
	stubAudioProbe(t, 22*time.Minute) // фрагменты: 0, 592 с и 1184 с

	replies := map[string]cheaperTranscript{
		"CH-0": {Segments: []cheaperSegment{
			{Start: 0.5, Text: "Введение в теорию графов."},
			{Start: 65, Text: "Сначала определим вершины."},
		}},
		"CH-592": {Segments: []cheaperSegment{
			{Start: 2, Text: "Далее рассматриваем деревья поиска."},
			{Start: 70, Text: "Балансировка красно чёрных деревьев."},
		}},
		"CH-1184": {Segments: []cheaperSegment{
			{Start: 1, Text: "Хеш таблицы и коллизии."},
			{Start: 30, Text: "Конец лекции."},
		}},
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("запрос %s %s, ждали POST /v1/audio/transcriptions", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ck-test" {
			t.Errorf("Authorization = %q", got)
		}
		for k, want := range map[string]string{"model": "whisper-test", "language": "ru", "response_format": "verbose_json"} {
			if got := r.FormValue(k); got != want {
				t.Errorf("поле %s = %q, ждали %q", k, got, want)
			}
		}
		tag, err := multipartFileTag(r)
		if err != nil {
			t.Errorf("поле file: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rep, ok := replies[tag]
		if !ok {
			http.Error(w, "неожиданный фрагмент "+tag, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rep)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{LLMAPIKey: "ck-test", LLMAPIURL: srv.URL + "/v1", CheaperSTTModel: "whisper-test"}
	got, err := transcribeCheaper(context.Background(), cfg, lectureAudio(t))
	if err != nil {
		t.Fatalf("transcribeCheaper: %v", err)
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

func TestCheaperRetry429HonorsRetryAfter(t *testing.T) {
	stubBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limit"}`, http.StatusTooManyRequests)
			return
		}
		verboseReply(w, "", cheaperSegment{Start: 3, Text: "Повтор прошёл."})
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{LLMAPIKey: "ck-test", LLMAPIURL: srv.URL}
	start := time.Now()
	got, err := cheaperRecognize(context.Background(), cfg, writeTestFile(t, "CH-0"), 0)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("cheaperRecognize: %v", err)
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

func TestCheaperRetryGivesUpAndDoesNotLeakKey(t *testing.T) {
	stubBackoff(t)
	t.Run("5xx: одна попытка и два повтора, ключ не попадает в ошибку", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, "upstream down, echo ck-secret", http.StatusServiceUnavailable)
		}))
		t.Cleanup(srv.Close)

		cfg := &config.Config{LLMAPIKey: "ck-secret", LLMAPIURL: srv.URL}
		_, err := cheaperRecognize(context.Background(), cfg, writeTestFile(t, "CH-0"), 0)
		if err == nil {
			t.Fatal("ждали ошибку после повторов")
		}
		if calls.Load() != 3 {
			t.Fatalf("запросов %d, ждали 3 (1 + 2 повтора)", calls.Load())
		}
		if !strings.Contains(err.Error(), "503") {
			t.Fatalf("в ошибке нет статуса: %v", err)
		}
		if strings.Contains(err.Error(), "ck-secret") {
			t.Fatalf("ключ попал в ошибку: %v", err)
		}
	})
	t.Run("400 не повторяется", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, "bad request", http.StatusBadRequest)
		}))
		t.Cleanup(srv.Close)

		cfg := &config.Config{LLMAPIKey: "ck", LLMAPIURL: srv.URL}
		if _, err := cheaperRecognize(context.Background(), cfg, writeTestFile(t, "CH-0"), 0); err == nil {
			t.Fatal("ждали ошибку на 400")
		}
		if calls.Load() != 1 {
			t.Fatalf("запросов %d, ждали 1", calls.Load())
		}
	})
}

func TestCheaperCtxCancelInterruptsRetryWait(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "rate limit", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	time.AfterFunc(100*time.Millisecond, cancel)

	cfg := &config.Config{LLMAPIKey: "ck", LLMAPIURL: srv.URL}
	start := time.Now()
	_, err := cheaperRecognize(ctx, cfg, writeTestFile(t, "CH-0"), 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, ждали context.Canceled", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("отмена не прервала ожидание Retry-After 60 с: прошло %s", d)
	}
}

func TestCheaperFormatBlocks(t *testing.T) {
	got, err := formatCheaperTranscript(cheaperTranscript{Segments: []cheaperSegment{
		{Start: 0, Text: "a"},
		{Start: 5, Text: "b"},
		{Start: 61, Text: "c"},
		{Start: 62, Text: "d"},
	}}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if want := "[10:00] a b\n[11:01] c d"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	got, err = formatCheaperTranscript(cheaperTranscript{Text: " просто текст "}, 2*time.Minute)
	if err != nil || got != "[02:00] просто текст" {
		t.Fatalf("без сегментов: got %q, err %v", got, err)
	}

	if _, err := formatCheaperTranscript(cheaperTranscript{Text: "  "}, 0); err == nil {
		t.Fatal("пустая расшифровка должна быть ошибкой")
	}
}

func TestCheaperEndpointKeyMarkerAndModel(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.example.com":    "https://api.example.com/v1/audio/transcriptions",
		"https://api.example.com/v1": "https://api.example.com/v1/audio/transcriptions",
		"https://x.example/openai":   "https://x.example/openai/audio/transcriptions",
		"":                           "",
	} {
		if got := cheaperAudioEndpoint(base); got != want {
			t.Errorf("cheaperAudioEndpoint(%q) = %q, want %q", base, got, want)
		}
	}

	if cheaperKeyMarker(nil) != "" || cheaperKeyMarker(&config.Config{LLMAPIKey: "k"}) != "" ||
		cheaperKeyMarker(&config.Config{LLMAPIURL: "https://h"}) != "" {
		t.Fatal("без ключа или без адреса cheaper должен быть пропущен")
	}
	if cheaperKeyMarker(&config.Config{LLMAPIKey: "k", LLMAPIURL: "https://h"}) == "" {
		t.Fatal("с ключом и адресом cheaper должен быть доступен")
	}

	if got := cheaperModel(&config.Config{}); got != cheaperSTTDefaultModel {
		t.Fatalf("модель по умолчанию = %q", got)
	}
	if got := cheaperModel(&config.Config{CheaperSTTModel: " whisper-x "}); got != "whisper-x" {
		t.Fatalf("модель из конфига = %q", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("5", now); got != 5*time.Second {
		t.Errorf("секунды: %s", got)
	}
	if got := parseRetryAfter(now.Add(75*time.Second).Format(http.TimeFormat), now); got != 75*time.Second {
		t.Errorf("HTTP-дата: %s", got)
	}
	for _, v := range []string{"", "abc", "0", "-3", now.Add(-time.Minute).Format(http.TimeFormat)} {
		if got := parseRetryAfter(v, now); got != 0 {
			t.Errorf("parseRetryAfter(%q) = %s, ждали 0", v, got)
		}
	}
}

func TestCheaperSkippedWithoutKey(t *testing.T) {
	var cheaperCalls, fishCalls atomic.Int32
	cheapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cheaperCalls.Add(1)
		verboseReply(w, "не должно быть")
	}))
	t.Cleanup(cheapSrv.Close)
	fishSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fishCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"fish целиком"}`))
	}))
	t.Cleanup(fishSrv.Close)

	t.Run("адрес есть, ключа нет: cheaper пропускается", func(t *testing.T) {
		cheaperCalls.Store(0)
		fishCalls.Store(0)
		cfg := &config.Config{
			LLMAPIURL:        cheapSrv.URL + "/v1",
			FishStudioAPIKey: "fk", FishStudioAPIURL: fishSrv.URL,
		}
		got, err := Transcribe(context.Background(), cfg, lectureAudio(t))
		if err != nil || got != "fish целиком" {
			t.Fatalf("got %q, err %v", got, err)
		}
		if cheaperCalls.Load() != 0 {
			t.Fatalf("cheaper вызван без ключа: %d", cheaperCalls.Load())
		}
	})

	t.Run("кусок Gemini не расшифрован, ключа cheaper нет: сразу fish", func(t *testing.T) {
		cheaperCalls.Store(0)
		fishCalls.Store(0)
		stubAudioProbe(t, 22*time.Minute)
		stubGeminiSlices(t)
		origURL := llm.GeminiURL
		t.Cleanup(func() { llm.GeminiURL = origURL })
		geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "gemini unavailable", http.StatusServiceUnavailable)
		}))
		t.Cleanup(geminiSrv.Close)
		llm.GeminiURL = geminiSrv.URL

		cfg := &config.Config{
			GeminiAPIKeys:    []string{"K1:gk"},
			FishStudioAPIKey: "fk", FishStudioAPIURL: fishSrv.URL,
		}
		ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
		got, err := Transcribe(ctx, cfg, lectureAudio(t))
		if err != nil || got != "fish целиком" {
			t.Fatalf("got %q, err %v", got, err)
		}
		if cheaperCalls.Load() != 0 {
			t.Fatalf("cheaper вызван без ключа: %d", cheaperCalls.Load())
		}
	})
}

func TestTranscribeOrderGeminiThenCheaper(t *testing.T) {
	stubAudioProbe(t, 5*time.Minute)
	stubCheaperSlices(t)
	origSlice := sliceAudioFn
	t.Cleanup(func() { sliceAudioFn = origSlice })
	// Нарезка Gemini падает целиком: это провал провайдера, а не одного фрагмента.
	sliceAudioFn = func(ctx context.Context, audioPath string, start, length time.Duration, outMP3 string) error {
		return errors.New("ffmpeg сломался")
	}

	var geminiCalls, cheaperCalls, fishCalls atomic.Int32
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		geminiCalls.Add(1)
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	t.Cleanup(geminiSrv.Close)
	origURL := llm.GeminiURL
	t.Cleanup(func() { llm.GeminiURL = origURL })
	llm.GeminiURL = geminiSrv.URL

	cheapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cheaperCalls.Add(1)
		verboseReply(w, "расшифровка cheaper целиком")
	}))
	t.Cleanup(cheapSrv.Close)
	fishSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fishCalls.Add(1)
		http.Error(w, "fish must not be called", http.StatusInternalServerError)
	}))
	t.Cleanup(fishSrv.Close)

	cfg := &config.Config{
		GeminiAPIKeys:    []string{"K1:gk"},
		LLMAPIKey:        "ck",
		LLMAPIURL:        cheapSrv.URL + "/v1",
		FishStudioAPIKey: "fk", FishStudioAPIURL: fishSrv.URL,
	}
	ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
	got, err := Transcribe(ctx, cfg, lectureAudio(t))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "[00:00] расшифровка cheaper целиком" {
		t.Fatalf("got %q", got)
	}
	if geminiCalls.Load() != 0 || cheaperCalls.Load() != 1 || fishCalls.Load() != 0 {
		t.Fatalf("вызовы gemini=%d cheaper=%d fish=%d, ждали 0/1/0",
			geminiCalls.Load(), cheaperCalls.Load(), fishCalls.Load())
	}
}

// Общая настройка для тестов отката одного фрагмента Gemini: 22 мин = 3 фрагмента
// (0, 592 с, 1184 с); фрагмент с 592 с Gemini не расшифровывает.
func setupGeminiFailingChunk(t *testing.T, geminiCounts map[string]int, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	stubAudioProbe(t, 22*time.Minute)
	stubGeminiSlices(t)
	origURL := llm.GeminiURL
	t.Cleanup(func() { llm.GeminiURL = origURL })
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tag, err := geminiAudioTag(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		geminiCounts[tag]++
		mu.Unlock()
		switch tag {
		case "MP3-CHUNK-0":
			writeGeminiReply(w, "[00:05] Введение в теорию графов. [09:50] Вершины и рёбра графа.")
		case "MP3-CHUNK-1184":
			writeGeminiReply(w, "[00:01] Список рёбер и вершин. [01:20] Конец.")
		case "MP3-CHUNK-592":
			http.Error(w, "gemini chunk unavailable", http.StatusServiceUnavailable)
		default:
			http.Error(w, "unexpected "+tag, http.StatusBadRequest)
		}
	}))
	t.Cleanup(geminiSrv.Close)
	llm.GeminiURL = geminiSrv.URL
	return geminiSrv
}

func TestGeminiChunkFallbackToCheaper(t *testing.T) {
	var (
		mu           sync.Mutex
		geminiCounts = map[string]int{}
		cheaperCalls atomic.Int32
		fishCalls    atomic.Int32
	)
	setupGeminiFailingChunk(t, geminiCounts, &mu)

	cheapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cheaperCalls.Add(1)
		tag, err := multipartFileTag(r)
		if err != nil || tag != "MP3-CHUNK-592" {
			// Откат получает именно исходный фрагмент Gemini, а не файл другого куска.
			http.Error(w, "неожиданный фрагмент "+tag, http.StatusBadRequest)
			return
		}
		verboseReply(w, "", cheaperSegment{Start: 2, Text: "Матрица смежности графа."}, cheaperSegment{Start: 20, Text: "Её свойства."})
	}))
	t.Cleanup(cheapSrv.Close)
	fishSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fishCalls.Add(1)
		http.Error(w, "fish must not be called", http.StatusInternalServerError)
	}))
	t.Cleanup(fishSrv.Close)

	cfg := &config.Config{
		GeminiAPIKeys:    []string{"K1:gk"},
		LLMAPIKey:        "ck",
		LLMAPIURL:        cheapSrv.URL + "/v1",
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
	if cheaperCalls.Load() != 1 {
		t.Fatalf("cheaper вызван %d раз, ждали 1", cheaperCalls.Load())
	}
	if fishCalls.Load() != 0 {
		t.Fatalf("fish вызван %d раз, ждали 0", fishCalls.Load())
	}
}

func TestGeminiChunkFallbackFailsThenNextProvider(t *testing.T) {
	stubBackoff(t)
	stubCheaperSlices(t)
	var (
		mu           sync.Mutex
		geminiCounts = map[string]int{}
		cheaperCalls atomic.Int32
		fishCalls    atomic.Int32
	)
	setupGeminiFailingChunk(t, geminiCounts, &mu)

	cheapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cheaperCalls.Add(1)
		http.Error(w, "cheaper down", http.StatusInternalServerError)
	}))
	t.Cleanup(cheapSrv.Close)
	fishSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fishCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"fish целиком"}`))
	}))
	t.Cleanup(fishSrv.Close)

	cfg := &config.Config{
		GeminiAPIKeys:    []string{"K1:gk"},
		LLMAPIKey:        "ck",
		LLMAPIURL:        cheapSrv.URL + "/v1",
		FishStudioAPIKey: "fk", FishStudioAPIURL: fishSrv.URL,
	}
	ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
	got, err := Transcribe(ctx, cfg, lectureAudio(t))
	if err != nil {
		t.Fatalf("ожидался откат на fish, получена ошибка: %v", err)
	}
	if got != "fish целиком" {
		t.Fatalf("got %q", got)
	}
	if cheaperCalls.Load() < 3 {
		t.Fatalf("cheaper вызван %d раз, ждали минимум 3 (повторы фрагмента)", cheaperCalls.Load())
	}
	if fishCalls.Load() != 1 {
		t.Fatalf("fish вызван %d раз, ждали 1", fishCalls.Load())
	}
}

func TestCheaperSliceRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg не найден в PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe не найден в PATH")
	}
	src := filepath.Join(t.TempDir(), "src.wav")
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=5", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("генерация тестового аудио: %v: %s", err, out)
	}
	dst := filepath.Join(t.TempDir(), "part.mp3")
	if err := sliceAudioFFmpegBitrate(context.Background(), src, time.Second, 2*time.Second, cheaperSTTBitrate, dst); err != nil {
		t.Fatalf("нарезка: %v", err)
	}
	dur, _, err := probeAudioFn(context.Background(), dst)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if dur < 1900*time.Millisecond || dur > 2100*time.Millisecond {
		t.Fatalf("длительность фрагмента %s, ждали около 2 с", dur)
	}
}

func TestRunChunksFirstErrorCancelsRest(t *testing.T) {
	errBoom := errors.New("boom")
	var started atomic.Int32
	err := runChunks(context.Background(), 6, 2, func(ctx context.Context, idx int) error {
		started.Add(1)
		if idx == 0 {
			return errBoom
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, ждали boom", err)
	}
	if started.Load() > 2 {
		t.Fatalf("после ошибки запущено %d фрагментов, ждали не больше 2 (по числу воркеров)", started.Load())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = runChunks(ctx, 3, 2, func(ctx context.Context, idx int) error {
		t.Errorf("фрагмент %d запущен при отменённом контексте", idx)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, ждали context.Canceled", err)
	}
}

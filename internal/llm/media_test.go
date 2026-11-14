package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func TestMediaPartsSerializationAndNormalization(t *testing.T) {
	imgBytes := []byte{0x89, 0x50, 0x4E, 0x47}
	audioBytes := []byte("fake-audio-bytes")

	parts := []Part{
		TextPart("Распознай формулу и перескажи аудио"),
		ImagePart(imgBytes, "image/jpeg"),
		ImagePart(imgBytes, ""),
		ImagePart(imgBytes, "application/pdf"),
		AudioPart(audioBytes, "audio/mpeg"),
		AudioPart(audioBytes, ".mp3"),
		AudioPart(audioBytes, "audio/wav"),
		AudioPart(audioBytes, "audio/ogg; codecs=opus"),
	}

	raw, err := BuildContentParts(parts)
	if err != nil {
		t.Fatalf("BuildContentParts() вернул ошибку: %v", err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(raw) вернул ошибку: %v", err)
	}
	if len(decoded) != len(parts) {
		t.Fatalf("ожидали %d частей, получили %d", len(parts), len(decoded))
	}

	// Проверяем текст
	if decoded[0]["type"] != "text" || decoded[0]["text"] != "Распознай формулу и перескажи аудио" {
		t.Fatalf("неожиданная текстовая часть: %#v", decoded[0])
	}

	// Проверяем картинки и fallback image/png
	wantImageURLs := []string{
		"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(imgBytes),
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBytes),
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBytes),
	}
	for i, wantURL := range wantImageURLs {
		part := decoded[1+i]
		if part["type"] != "image_url" {
			t.Fatalf("часть %d: type = %v, ожидали image_url", 1+i, part["type"])
		}
		imgObj, ok := part["image_url"].(map[string]any)
		if !ok || imgObj["url"] != wantURL {
			t.Fatalf("часть %d: image_url = %#v, ожидали url=%q", 1+i, part["image_url"], wantURL)
		}
	}

	// Проверяем аудио и нормализацию форматов
	wantB64Audio := base64.StdEncoding.EncodeToString(audioBytes)
	wantFormats := []string{"mp3", "mp3", "wav", "ogg"}
	for i, wantFmt := range wantFormats {
		part := decoded[4+i]
		if part["type"] != "input_audio" {
			t.Fatalf("часть %d: type = %v, ожидали input_audio", 4+i, part["type"])
		}
		audioObj, ok := part["input_audio"].(map[string]any)
		if !ok {
			t.Fatalf("часть %d: input_audio имеет неожиданный тип %T", 4+i, part["input_audio"])
		}
		if audioObj["data"] != wantB64Audio {
			t.Fatalf("часть %d: data = %v, ожидали %v", 4+i, audioObj["data"], wantB64Audio)
		}
		if audioObj["format"] != wantFmt {
			t.Fatalf("часть %d: format = %v, ожидали %q", 4+i, audioObj["format"], wantFmt)
		}
	}

	// Проверяем также прямую сериализацию через Part.MarshalJSON
	singleJSON, err := json.Marshal(AudioPart(audioBytes, "audio/wav"))
	if err != nil {
		t.Fatalf("json.Marshal(AudioPart) вернул ошибку: %v", err)
	}
	if !strings.Contains(string(singleJSON), `"type":"input_audio"`) || !strings.Contains(string(singleJSON), `"format":"wav"`) {
		t.Fatalf("неожиданный JSON от Part.MarshalJSON: %s", string(singleJSON))
	}
}

func TestAskPartsAskImageAskAudioHTTP(t *testing.T) {
	imgBytes := []byte("png-image-payload")
	audioBytes := []byte("mp3-audio-payload")

	type capturedReq struct {
		Auth      string
		UserAgent string
		Model     string
		Messages  []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}

	var mu sync.Mutex
	var captured []capturedReq

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req capturedReq
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req.Auth = r.Header.Get("Authorization")
		req.UserAgent = r.Header.Get("User-Agent")

		mu.Lock()
		captured = append(captured, req)
		idx := len(captured)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if idx == 3 {
			// Проверяем также поддержку ответа в виде массива текстовых блоков
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":[{"type":"text","text":"Расшифровка аудио"}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  Готовый ответ LLM  "}}]}`))
	}))
	defer srv.Close()

	chain := []Target{
		{
			Provider: Gemini,
			Model:    "gemini-3.6-flash",
			KeyIdx:   0,
			KeyLabel: "тест",
			url:      srv.URL,
			key:      "secret-key-1",
		},
	}

	ctx := WithPool(context.Background(), NewKeyPool())

	// 1. AskParts (текст + картинка + аудио)
	res1, err := AskParts(ctx, chain, "Ты преподаватель.", []Part{
		TextPart("Разбери слайд и голос"),
		ImagePart(imgBytes, "image/webp"),
		AudioPart(audioBytes, ".mp3"),
	})
	if err != nil {
		t.Fatalf("AskParts() вернул ошибку: %v", err)
	}
	if res1.Text != "Готовый ответ LLM" {
		t.Fatalf("AskParts() Text = %q, ожидали %q", res1.Text, "Готовый ответ LLM")
	}

	// 2. AskImage (с пустым prompt — должен отправиться только image_url)
	res2, err := AskImage(ctx, chain, "Системный промпт", "", imgBytes, "")
	if err != nil {
		t.Fatalf("AskImage() вернул ошибку: %v", err)
	}
	if res2.Text != "Готовый ответ LLM" {
		t.Fatalf("AskImage() Text = %q", res2.Text)
	}

	// 3. AskAudio (с пользовательским prompt + аудио)
	res3, err := AskAudio(ctx, chain, "", "Сделай конспект", audioBytes, "audio/ogg")
	if err != nil {
		t.Fatalf("AskAudio() вернул ошибку: %v", err)
	}
	if res3.Text != "Расшифровка аудио" {
		t.Fatalf("AskAudio() Text = %q, ожидали %q", res3.Text, "Расшифровка аудио")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 3 {
		t.Fatalf("ожидали 3 HTTP-запроса, получили %d", len(captured))
	}

	// Проверяем 1-й запрос (AskParts)
	r1 := captured[0]
	if r1.Auth != "Bearer secret-key-1" || r1.UserAgent != userAgent || r1.Model != "gemini-3.6-flash" {
		t.Fatalf("некорректные метаданные запроса 1: %#v", r1)
	}
	if len(r1.Messages) != 2 || r1.Messages[0].Role != "system" || r1.Messages[1].Role != "user" {
		t.Fatalf("некорректная структура messages в запросе 1: %#v", r1.Messages)
	}
	var sysText string
	if err := json.Unmarshal(r1.Messages[0].Content, &sysText); err != nil || sysText != "Ты преподаватель." {
		t.Fatalf("системный промпт = %s, err=%v", string(r1.Messages[0].Content), err)
	}
	var userParts1 []map[string]any
	if err := json.Unmarshal(r1.Messages[1].Content, &userParts1); err != nil {
		t.Fatalf("unmarshal parts 1: %v", err)
	}
	if len(userParts1) != 3 {
		t.Fatalf("ожидали 3 части в AskParts, получили %d", len(userParts1))
	}

	// Проверяем 2-й запрос (AskImage с пустым prompt)
	var userParts2 []map[string]any
	if err := json.Unmarshal(captured[1].Messages[1].Content, &userParts2); err != nil {
		t.Fatalf("unmarshal parts 2: %v", err)
	}
	if len(userParts2) != 1 || userParts2[0]["type"] != "image_url" {
		t.Fatalf("AskImage без текстового prompt должен отправлять 1 image_url: %#v", userParts2)
	}
	imgObj := userParts2[0]["image_url"].(map[string]any)
	if !strings.HasPrefix(imgObj["url"].(string), "data:image/png;base64,") {
		t.Fatalf("ожидали fallback image/png, получили %v", imgObj["url"])
	}

	// Проверяем 3-й запрос (AskAudio без sysPrompt)
	r3 := captured[2]
	if len(r3.Messages) != 1 || r3.Messages[0].Role != "user" {
		t.Fatalf("AskAudio без sysPrompt должен содержать 1 user message, получили %#v", r3.Messages)
	}
	var userParts3 []map[string]any
	if err := json.Unmarshal(r3.Messages[0].Content, &userParts3); err != nil {
		t.Fatalf("unmarshal parts 3: %v", err)
	}
	if len(userParts3) != 2 || userParts3[0]["text"] != "Сделай конспект" {
		t.Fatalf("неожиданные части AskAudio: %#v", userParts3)
	}
	audioObj := userParts3[1]["input_audio"].(map[string]any)
	if audioObj["format"] != "ogg" {
		t.Fatalf("ожидали format=ogg, получили %v", audioObj["format"])
	}
}

func TestAskPartsWithPoolCooldownAndConcurrency(t *testing.T) {
	t.Run("429 переводит первый ключ в cooldown и переключает на второй", func(t *testing.T) {
		pool := NewKeyPool()
		var mu sync.Mutex
		curNow := time.Date(2026, 11, 14, 18, 0, 0, 0, time.UTC)
		pool.SetNowFunc(func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return curNow
		})

		var k1Calls, k2Calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("Authorization") {
			case "Bearer media-key-1":
				k1Calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"code":429,"details":[{"retryDelay":"25s"}]}}`))
			case "Bearer media-key-2":
				k2Calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ответ второго ключа"}}]}`))
			default:
				http.Error(w, "unknown key", http.StatusUnauthorized)
			}
		}))
		defer srv.Close()

		chain := []Target{
			{Provider: Gemini, Model: "gemini-3.6-flash", KeyIdx: 0, KeyLabel: "k1", url: srv.URL, key: "media-key-1"},
			{Provider: Gemini, Model: "gemini-3.6-flash", KeyIdx: 1, KeyLabel: "k2", url: srv.URL, key: "media-key-2"},
		}
		parts := []Part{TextPart("Что на фото?"), ImagePart([]byte("img"), "image/png")}

		res1, err := AskPartsWithPool(context.Background(), pool, chain, "", parts)
		if err != nil {
			t.Fatalf("первый AskPartsWithPool() ошибка: %v", err)
		}
		if res1.Used == nil || res1.Used.KeyLabel != "k2" {
			t.Fatalf("ожидали переключение на k2, получили %+v", res1.Used)
		}
		if until := pool.CooldownUntil("media-key-1"); until.Sub(curNow) != 25*time.Second {
			t.Fatalf("cooldown media-key-1 = %v, ожидали 25s", until.Sub(curNow))
		}

		// Повторный вызов до окончания cooldown не должен дёргать первый ключ
		_, err = AskPartsWithPool(context.Background(), pool, chain, "", parts)
		if err != nil {
			t.Fatalf("второй AskPartsWithPool() ошибка: %v", err)
		}
		if k1Calls.Load() != 1 || k2Calls.Load() != 2 {
			t.Fatalf("ожидали k1=1, k2=2, получили k1=%d, k2=%d", k1Calls.Load(), k2Calls.Load())
		}
	})

	t.Run("ограничение параллельности <= 2 на один ключ", func(t *testing.T) {
		pool := NewKeyPool()
		var inFlight, maxInFlight atomic.Int32
		gate := make(chan struct{})

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cur := inFlight.Add(1)
			for {
				prev := maxInFlight.Load()
				if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
					break
				}
			}
			<-gate
			inFlight.Add(-1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		}))
		defer srv.Close()

		chain := []Target{
			{Provider: Gemini, Model: "gemini-3.6-flash", KeyIdx: 0, KeyLabel: "single", url: srv.URL, key: "shared-media-key"},
		}
		parts := []Part{AudioPart([]byte("audio-data"), "mp3")}

		const workers = 6
		var wg sync.WaitGroup
		errs := make(chan error, workers)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := AskPartsWithPool(context.Background(), pool, chain, "", parts)
				errs <- err
			}()
		}

		deadline := time.After(2 * time.Second)
		for inFlight.Load() < MaxConcurrentPerKey {
			select {
			case <-deadline:
				close(gate)
				t.Fatalf("не дождались %d параллельных запросов, текущее=%d", MaxConcurrentPerKey, inFlight.Load())
			default:
				time.Sleep(5 * time.Millisecond)
			}
		}
		time.Sleep(30 * time.Millisecond)
		if got := maxInFlight.Load(); got > MaxConcurrentPerKey {
			close(gate)
			t.Fatalf("превышен лимит параллельности: maxInFlight=%d > %d", got, MaxConcurrentPerKey)
		}

		close(gate)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("неожиданная ошибка параллельного AskPartsWithPool: %v", err)
			}
		}
		if got := maxInFlight.Load(); got != MaxConcurrentPerKey {
			t.Fatalf("ожидали maxInFlight=%d, получили %d", MaxConcurrentPerKey, got)
		}
	})
}

func TestAskPartsValidationAndSecretScrub(t *testing.T) {
	var called atomic.Int32
	secret := "AIzaSyVerySecretKey123456"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid request for key=" + secret))
	}))
	defer srv.Close()

	chain := []Target{
		{Provider: Gemini, Model: "gemini-3.6-flash", KeyIdx: 0, KeyLabel: "k1", url: srv.URL, key: secret},
	}

	// Невалидные входы не должны отправлять HTTP-запрос
	badInputs := []struct {
		name  string
		parts []Part
	}{
		{name: "пустой срез parts", parts: nil},
		{name: "пустой текст", parts: []Part{TextPart("   ")}},
		{name: "пустое изображение", parts: []Part{TextPart("опиши"), ImagePart(nil, "image/png")}},
		{name: "пустое аудио", parts: []Part{AudioPart([]byte{}, "mp3")}},
		{name: "аудио без формата", parts: []Part{AudioPart([]byte("abc"), "")}},
		{name: "неизвестный тип части", parts: []Part{{Kind: PartKind("video")}}},
		{name: "превышение лимита размера", parts: []Part{ImagePart(make([]byte, MaxMediaPartBytes+1), "image/png")}},
	}
	for _, tc := range badInputs {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AskPartsWithPool(context.Background(), NewKeyPool(), chain, "", tc.parts)
			if err == nil {
				t.Fatalf("ожидали ошибку для %q, получили nil", tc.name)
			}
		})
	}
	if called.Load() != 0 {
		t.Fatalf("при невалидных входах не должно быть HTTP-запросов, было %d", called.Load())
	}

	// При ошибке от API секрет ключа должен быть вырезан
	res, err := AskPartsWithPool(context.Background(), NewKeyPool(), chain, "", []Part{ImagePart([]byte("x"), "image/png")})
	if err == nil {
		t.Fatal("ожидали ошибку HTTP 400")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("секрет ключа утёк в текст ошибки: %v", err)
	}
	if len(res.Attempts) != 1 || strings.Contains(res.Attempts[0].Err, secret) {
		t.Fatalf("секрет ключа утёк в Attempt.Err: %+v", res.Attempts)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("ожидали маскирование *** в ошибке: %v", err)
	}
}

func TestMediaCapabilitiesAndGeminiPoolChain(t *testing.T) {
	if !SupportsVision(Gemini, "gemini-3.6-flash") || !SupportsAudio(Gemini, "gemini-3.6-flash") {
		t.Fatal("Gemini должен поддерживать и vision, и audio")
	}
	if !SupportsVision(Cheaper, "muse-spark-1.3-contributor") || SupportsAudio(Cheaper, "muse-spark-1.3-contributor") {
		t.Fatal("Cheaper muse-spark должен поддерживать vision, но не audio")
	}
	if SupportsVision(OpenCode, "glm-5.3-flash-free") || SupportsAudio(OpenCode, "glm-5.3-flash-free") {
		t.Fatal("OpenCode glm-5.3-flash-free не должен помечаться как поддерживающий media")
	}

	cfg := &config.Config{
		GeminiAPIKeys:   []string{"Агеев:g1", "Иванов:g2"},
		OpenCodeAPIKeys: []string{"oc1"},
		LLMAPIKey:       "admin-secret",
		LLMAPIURL:       "https://cheaperinference.example/v1",
		LLMModel:        "glm-5.3-flash",
	}

	gemChain := GeminiPoolChain(cfg)
	if len(gemChain) != len(GeminiModels)*2 {
		t.Fatalf("GeminiPoolChain() длина = %d, ожидали %d", len(gemChain), len(GeminiModels)*2)
	}
	for _, tgt := range gemChain {
		if tgt.Provider != Gemini || tgt.AdminOnly {
			t.Fatalf("GeminiPoolChain() вернул не-Gemini таргет: %+v", tgt)
		}
	}

	mixed := append(DefaultChain(cfg), Target{Provider: Cheaper, Model: "muse-spark-1.3-contributor", AdminOnly: true})
	visionOnly := MediaTargets(mixed, false, true)
	if len(visionOnly) != len(GeminiModels)*2+1 {
		t.Fatalf("MediaTargets(vision) длина = %d, ожидали %d", len(visionOnly), len(GeminiModels)*2+1)
	}
	audioOnly := MediaTargets(mixed, true, false)
	if len(audioOnly) != len(GeminiModels)*2 {
		t.Fatalf("MediaTargets(audio) длина = %d, ожидали %d", len(audioOnly), len(GeminiModels)*2)
	}
}

func TestExtractResponseTextFiltersNonTextBlocks(t *testing.T) {
	raw := json.RawMessage(`[{"type":"thinking","text":"внутренние рассуждения"},{"type":"reasoning","text":"ещё шаги"},{"type":"text","text":"ответ"}]`)
	got, err := extractResponseText(raw)
	if err != nil {
		t.Fatalf("extractResponseText() неожиданная ошибка: %v", err)
	}
	if got != "ответ" {
		t.Fatalf("extractResponseText() = %q, ожидали %q", got, "ответ")
	}
}

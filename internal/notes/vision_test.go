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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
)

type capturedVisionReq struct {
	Model      string
	ImageCount int
	PromptText string
	Auth       string
	Path       string
}

func decodeVisionRequest(t *testing.T, r *http.Request) capturedVisionReq {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var payload struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal vision request: %v", err)
	}

	res := capturedVisionReq{
		Model: payload.Model,
		Auth:  r.Header.Get("Authorization"),
		Path:  r.URL.Path,
	}
	for _, msg := range payload.Messages {
		if msg.Role != "user" {
			continue
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(msg.Content, &parts); err != nil {
			t.Fatalf("unmarshal user content parts: %v", err)
		}
		for _, p := range parts {
			switch p.Type {
			case "text":
				if res.PromptText != "" {
					res.PromptText += "\n"
				}
				res.PromptText += p.Text
			case "image_url":
				res.ImageCount++
			}
		}
	}
	return res
}

func writeFakeSlides(t *testing.T, dir string, count int, withManifestText bool) {
	t.Helper()
	var manifest []slideManifestItem
	for i := 1; i <= count; i++ {
		name := fmt.Sprintf("%02d.png", i)
		data := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		item := slideManifestItem{
			Num:          i,
			File:         name,
			ShownOffsetS: i * 10,
			Timestamp:    fmt.Sprintf("[%02d:%02d]", (i*10)/60, (i*10)%60),
		}
		if withManifestText {
			item.Text = fmt.Sprintf("Текстовый слой слайда %d", i)
		}
		manifest = append(manifest, item)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slides.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDescribeSlidesBatchesAndPoolRetry(t *testing.T) {
	dir := t.TempDir()
	writeFakeSlides(t, dir, 10, true)

	var (
		mu         sync.Mutex
		reqs       []capturedVisionReq
		limitedKey string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creq := decodeVisionRequest(t, r)
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, creq)

		// Первый запрос на первом батче отвечает 429 с retryDelay — должен сработать повтор на другом ключе пула.
		if len(reqs) == 1 {
			limitedKey = creq.Auth
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"details":[{"retryDelay":"30s"}]}}`))
			return
		}
		if creq.Auth == limitedKey {
			t.Errorf("ключ %s в cooldown не должен вызываться повторно", limitedKey)
		}

		var reply strings.Builder
		switch creq.ImageCount {
		case 6:
			for i := 1; i <= 6; i++ {
				fmt.Fprintf(&reply, "=== Слайд %d ===\nVision описание слайда %d\n\n", i, i)
			}
		case 4:
			for i := 7; i <= 10; i++ {
				fmt.Fprintf(&reply, "=== Слайд %d ===\nVision описание слайда %d\n\n", i, i)
			}
		default:
			t.Fatalf("неожиданное число изображений в батче: %d", creq.ImageCount)
		}

		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": reply.String()}},
			},
		})
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	oldURL := llm.GeminiURL
	llm.GeminiURL = srv.URL
	t.Cleanup(func() { llm.GeminiURL = oldURL })

	pool := llm.NewKeyPool()
	ctx := llm.WithPool(context.Background(), pool)
	cfg := &config.Config{
		GeminiAPIKeys: []string{"first:key-one", "second:key-two"},
	}

	got, err := DescribeSlides(ctx, cfg, dir)
	if err != nil {
		t.Fatalf("DescribeSlides вернул ошибку: %v", err)
	}

	if len(reqs) != 3 {
		t.Fatalf("ожидалось 3 HTTP-запроса (429 + батч 1 + батч 2), получено %d", len(reqs))
	}
	if reqs[0].ImageCount != 6 || reqs[1].ImageCount != 6 || reqs[2].ImageCount != 4 {
		t.Fatalf("ожидались размеры батчей [6, 6, 4], получены [%d, %d, %d]",
			reqs[0].ImageCount, reqs[1].ImageCount, reqs[2].ImageCount)
	}
	if !strings.Contains(reqs[1].PromptText, "[00:10]") || !strings.Contains(reqs[2].PromptText, "[01:40]") {
		t.Fatalf("промпт батча должен содержать метки времени из slides.json")
	}

	for i := 1; i <= 10; i++ {
		ts := fmt.Sprintf("[%02d:%02d]", (i*10)/60, (i*10)%60)
		wantHeader := fmt.Sprintf("Слайд %d %s:", i, ts)
		wantBody := fmt.Sprintf("Vision описание слайда %d", i)
		if !strings.Contains(got, wantHeader) || !strings.Contains(got, wantBody) {
			t.Fatalf("в результате отсутствует слайд %d (%q / %q):\n%s", i, wantHeader, wantBody, got)
		}
	}
}

func TestDescribeSlidesFallbackToTextLayerWithoutKeys(t *testing.T) {
	t.Run("with_images", func(t *testing.T) {
		dir := t.TempDir()
		writeFakeSlides(t, dir, 3, true)

		got, err := DescribeSlides(context.Background(), &config.Config{}, dir)
		if err != nil {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
		for i := 1; i <= 3; i++ {
			ts := fmt.Sprintf("[%02d:%02d]", (i*10)/60, (i*10)%60)
			want := fmt.Sprintf("Слайд %d %s:\nТекстовый слой слайда %d", i, ts, i)
			if !strings.Contains(got, want) {
				t.Fatalf("ожидался текстовый слой %q, получено:\n%s", want, got)
			}
		}
	})

	t.Run("without_images_only_manifest", func(t *testing.T) {
		dir := t.TempDir()
		manifest := []slideManifestItem{
			{Num: 1, File: "01.png", Text: "Введение в алгоритмы", Timestamp: "[00:05]"},
			{Num: 2, File: "02.png", Text: "Асимптотика O(n log n)", ShownOffsetS: 125},
		}
		raw, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(dir, "slides.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}

		got, err := DescribeSlides(context.Background(), &config.Config{}, dir)
		if err != nil {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
		if !strings.Contains(got, "Слайд 1 [00:05]:\nВведение в алгоритмы") {
			t.Fatalf("нет первого слайда из slides.json:\n%s", got)
		}
		if !strings.Contains(got, "Слайд 2 [02:05]:\nАсимптотика O(n log n)") {
			t.Fatalf("нет второго слайда с вычисленной меткой времени:\n%s", got)
		}
	})
}

func TestDescribeSlidesFallbackToTextLayerOnBatchFailure(t *testing.T) {
	fastVisionRetry(t)
	dir := t.TempDir()
	writeFakeSlides(t, dir, 10, true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creq := decodeVisionRequest(t, r)
		if creq.ImageCount == 6 {
			http.Error(w, "upstream 500", http.StatusInternalServerError)
			return
		}
		var reply strings.Builder
		for i := 7; i <= 10; i++ {
			fmt.Fprintf(&reply, "=== Слайд %d ===\nVision батч 2 слайд %d\n\n", i, i)
		}
		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": reply.String()}},
			},
		})
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	oldURL := llm.GeminiURL
	llm.GeminiURL = srv.URL
	t.Cleanup(func() { llm.GeminiURL = oldURL })

	ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
	cfg := &config.Config{
		GeminiAPIKeys: []string{"k1:secret-1", "k2:secret-2"},
	}

	got, err := DescribeSlides(ctx, cfg, dir)
	if err != nil {
		t.Fatalf("ожидался fallback на текстовый слой без ошибки, получено: %v", err)
	}

	for i := 1; i <= 6; i++ {
		ts := fmt.Sprintf("[%02d:%02d]", (i*10)/60, (i*10)%60)
		want := fmt.Sprintf("Слайд %d %s:\nТекстовый слой слайда %d", i, ts, i)
		if !strings.Contains(got, want) {
			t.Fatalf("для упавшего батча 1 ожидался текстовый слой %q:\n%s", want, got)
		}
	}
	for i := 7; i <= 10; i++ {
		ts := fmt.Sprintf("[%02d:%02d]", (i*10)/60, (i*10)%60)
		want := fmt.Sprintf("Слайд %d %s:\nVision батч 2 слайд %d", i, ts, i)
		if !strings.Contains(got, want) {
			t.Fatalf("для батча 2 ожидался vision-текст %q:\n%s", want, got)
		}
	}
}

func TestDescribeSlidesAllFailedNoTextReturnsError(t *testing.T) {
	fastVisionRetry(t)
	dir := t.TempDir()
	writeFakeSlides(t, dir, 3, false)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusInternalServerError)
	}))
	defer srv.Close()

	oldURL := llm.GeminiURL
	llm.GeminiURL = srv.URL
	t.Cleanup(func() { llm.GeminiURL = oldURL })

	ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
	cfg := &config.Config{
		GeminiAPIKeys: []string{"k1:secret-1"},
	}

	got, err := DescribeSlides(ctx, cfg, dir)
	if err == nil {
		t.Fatalf("ожидалась ошибка при падении всех vision-ключей и отсутствии текстового слоя, получено got=%q", got)
	}
}

func TestDescribeSlidesExcludesCheaper(t *testing.T) {
	dir := t.TempDir()
	writeFakeSlides(t, dir, 2, false)

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := &config.Config{
		LLMAPIKey: "admin-secret",
		LLMAPIURL: srv.URL,
		LLMModel:  "gpt-4o",
	}

	got, err := DescribeSlides(context.Background(), cfg, dir)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if got != "" {
		t.Fatalf("ожидалась пустая строка без бесплатных vision-ключей и текста, получено %q", got)
	}
	if called {
		t.Fatal("DescribeSlides обратился к админскому LLMAPIURL (cheaper)")
	}
}

func TestDescribeSlidesContextCanceled(t *testing.T) {
	dir := t.TempDir()
	writeFakeSlides(t, dir, 10, false)

	ctx, cancel := context.WithCancel(llm.WithPool(context.Background(), llm.NewKeyPool()))
	defer cancel()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		cancel()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"=== Слайд 1 ===\nок"}}]}`))
	}))
	defer srv.Close()

	oldURL := llm.GeminiURL
	llm.GeminiURL = srv.URL
	t.Cleanup(func() { llm.GeminiURL = oldURL })

	cfg := &config.Config{
		GeminiAPIKeys: []string{"k1:secret-1"},
	}

	_, err := DescribeSlides(ctx, cfg, dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидался context.Canceled, получено: %v", err)
	}
	if calls != 1 {
		t.Fatalf("ожидался 1 вызов до отмены контекста, выполнено %d", calls)
	}
}

// fastVisionRetry убирает паузу между повторами батчей на время теста.
func fastVisionRetry(t *testing.T) {
	t.Helper()
	old := visionRetryDelay
	visionRetryDelay = time.Millisecond
	t.Cleanup(func() { visionRetryDelay = old })
}

// visionCallsPerAttempt — сколько HTTP-запросов уходит на одну попытку батча:
// AskParts перебирает все таргеты цепочки пула, по одному запросу на каждый.
func visionCallsPerAttempt(cfg *config.Config) int {
	return len(llm.MediaTargets(llm.PoolChain(cfg), false, true))
}

// visionReplyFor собирает ответ модели с заголовками «=== Слайд N ===» для номеров nums.
func visionReplyFor(prefix string, nums ...int) string {
	var sb strings.Builder
	for _, n := range nums {
		fmt.Fprintf(&sb, "=== Слайд %d ===\n%s %d\n\n", n, prefix, n)
	}
	return sb.String()
}

func writeVisionOK(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	resp, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"content": content}},
		},
	})
	_, _ = w.Write(resp)
}

// Батч из 6 слайдов падает на первой попытке (на всех таргетах), вторая попытка проходит: результат полный.
func TestDescribeSlidesRetriesFailedBatch(t *testing.T) {
	fastVisionRetry(t)
	dir := t.TempDir()
	writeFakeSlides(t, dir, 10, true)
	cfg := &config.Config{GeminiAPIKeys: []string{"k1:secret-1"}}
	perAttempt := int32(visionCallsPerAttempt(cfg))

	var bigCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creq := decodeVisionRequest(t, r)
		if creq.ImageCount == 6 {
			if bigCalls.Add(1) <= perAttempt {
				http.Error(w, "upstream 500", http.StatusInternalServerError)
				return
			}
			writeVisionOK(w, visionReplyFor("Vision повтор слайд", 1, 2, 3, 4, 5, 6))
			return
		}
		writeVisionOK(w, visionReplyFor("Vision повтор слайд", 7, 8, 9, 10))
	}))
	defer srv.Close()

	oldURL := llm.GeminiURL
	llm.GeminiURL = srv.URL
	t.Cleanup(func() { llm.GeminiURL = oldURL })

	ctx := llm.WithPool(context.Background(), llm.NewKeyPool())

	res, err := DescribeSlidesDetailed(ctx, cfg, dir)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.Incomplete || len(res.FailedSlides) != 0 {
		t.Fatalf("после успешного повтора результат должен быть полным, получено %+v", res)
	}
	// Первая попытка: perAttempt запросов, все с 500. Вторая: первый же таргет отвечает успехом.
	if n := bigCalls.Load(); n != perAttempt+1 {
		t.Fatalf("батч из 6 слайдов: ожидалось %d запросов (полная первая попытка с сбоями и успешный первый запрос повтора), выполнено %d", perAttempt+1, n)
	}
	for i := 1; i <= 10; i++ {
		want := fmt.Sprintf("Vision повтор слайд %d", i)
		if !strings.Contains(res.Text, want) {
			t.Fatalf("в результате нет %q:\n%s", want, res.Text)
		}
	}
}

// Батч из 6 слайдов падает на всех попытках: его слайды идут из текстового слоя, результат помечен неполным.
func TestDescribeSlidesIncompleteAfterRetries(t *testing.T) {
	fastVisionRetry(t)
	dir := t.TempDir()
	writeFakeSlides(t, dir, 10, true)
	cfg := &config.Config{GeminiAPIKeys: []string{"k1:secret-1"}}
	perAttempt := visionCallsPerAttempt(cfg)

	var bigCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creq := decodeVisionRequest(t, r)
		if creq.ImageCount == 6 {
			bigCalls.Add(1)
			http.Error(w, "upstream 500", http.StatusInternalServerError)
			return
		}
		writeVisionOK(w, visionReplyFor("Vision батч 2 слайд", 7, 8, 9, 10))
	}))
	defer srv.Close()

	oldURL := llm.GeminiURL
	llm.GeminiURL = srv.URL
	t.Cleanup(func() { llm.GeminiURL = oldURL })

	ctx := llm.WithPool(context.Background(), llm.NewKeyPool())

	res, err := DescribeSlidesDetailed(ctx, cfg, dir)
	if err != nil {
		t.Fatalf("ожидался fallback на текстовый слой без ошибки, получено: %v", err)
	}
	if !res.Incomplete {
		t.Fatalf("результат с упавшим батчем должен быть помечен Incomplete")
	}
	if got := fmt.Sprint(res.FailedSlides); got != "[1 2 3 4 5 6]" {
		t.Fatalf("FailedSlides = %s, ожидались слайды 1..6", got)
	}
	if n := int(bigCalls.Load()); n != visionBatchAttempts*perAttempt {
		t.Fatalf("упавший батч должен быть повторён до %d попыток по %d запросов, выполнено %d запросов",
			visionBatchAttempts, perAttempt, n)
	}
	for i := 1; i <= 6; i++ {
		ts := fmt.Sprintf("[%02d:%02d]", (i*10)/60, (i*10)%60)
		want := fmt.Sprintf("Слайд %d %s:\nТекстовый слой слайда %d", i, ts, i)
		if !strings.Contains(res.Text, want) {
			t.Fatalf("для упавшего батча ожидался текстовый слой %q:\n%s", want, res.Text)
		}
	}
	for i := 7; i <= 10; i++ {
		want := fmt.Sprintf("Vision батч 2 слайд %d", i)
		if !strings.Contains(res.Text, want) {
			t.Fatalf("для рабочего батча ожидался vision-текст %q:\n%s", want, res.Text)
		}
	}
}

// Отмена ctx во время паузы между повторами прерывает их сразу, а не после всех пауз.
func TestDescribeSlidesRetryRespectsContext(t *testing.T) {
	old := visionRetryDelay
	visionRetryDelay = time.Hour
	t.Cleanup(func() { visionRetryDelay = old })

	dir := t.TempDir()
	writeFakeSlides(t, dir, 3, false)
	cfg := &config.Config{GeminiAPIKeys: []string{"k1:secret-1"}}
	perAttempt := visionCallsPerAttempt(cfg)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "upstream 500", http.StatusInternalServerError)
	}))
	defer srv.Close()

	oldURL := llm.GeminiURL
	llm.GeminiURL = srv.URL
	t.Cleanup(func() { llm.GeminiURL = oldURL })

	ctx, cancel := context.WithTimeout(llm.WithPool(context.Background(), llm.NewKeyPool()), 300*time.Millisecond)
	defer cancel()

	_, err := DescribeSlidesDetailed(ctx, cfg, dir)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидался context.DeadlineExceeded из паузы повтора, получено: %v", err)
	}
	if n := int(calls.Load()); n != perAttempt {
		t.Fatalf("при отмене в паузе ожидалась одна попытка (%d запросов), выполнено %d", perAttempt, n)
	}
}

// Все ключи на паузе после 429: повтор сейчас ничего не даст, поэтому после первого отказа
// делается одна пауза, а на cooldown-ответе батч сдаётся без дальнейших пауз и запросов.
func TestDescribeSlidesStopsRetryOnAllCooldown(t *testing.T) {
	var pauses atomic.Int32
	oldPause := visionPause
	visionPause = func(ctx context.Context, d time.Duration) bool {
		pauses.Add(1)
		return true
	}
	t.Cleanup(func() { visionPause = oldPause })

	dir := t.TempDir()
	writeFakeSlides(t, dir, 3, false)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"details":[{"retryDelay":"30s"}]}}`))
	}))
	defer srv.Close()

	oldURL := llm.GeminiURL
	llm.GeminiURL = srv.URL
	t.Cleanup(func() { llm.GeminiURL = oldURL })

	ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
	cfg := &config.Config{GeminiAPIKeys: []string{"k1:secret-1"}}

	_, err := DescribeSlidesDetailed(ctx, cfg, dir)
	if !errors.Is(err, llm.ErrAllCooldown) {
		t.Fatalf("ожидался ErrAllCooldown, получено: %v", err)
	}
	if n := pauses.Load(); n != 1 {
		t.Fatalf("ожидалась одна пауза после первого отказа, получено %d", n)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("ключ на cooldown не должен вызываться повторно: ожидался 1 запрос, получено %d", n)
	}
}

func TestDescribeSlidesManifestPathTraversalIgnored(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(filepath.Dir(dir), "secret.png")
	if err := os.WriteFile(secretFile, make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}

	manifest := []slideManifestItem{
		{Num: 1, File: "../secret.png", Text: "Секрет", Timestamp: "[00:01]"},
		{Num: 2, File: "/etc/passwd", Text: "Пароли", Timestamp: "[00:02]"},
		{Num: 3, File: "03.png", Text: "Безопасный слайд", Timestamp: "[00:03]"},
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "slides.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := DescribeSlides(context.Background(), &config.Config{}, dir)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if strings.Contains(got, "Секрет") || strings.Contains(got, "Пароли") {
		t.Fatalf("небезопасные записи из slides.json не должны попадать в результат:\n%s", got)
	}
	if !strings.Contains(got, "Слайд 3 [00:03]:\nБезопасный слайд") {
		t.Fatalf("безопасный слайд должен остаться в результате:\n%s", got)
	}
}

func TestDescribeImagePoolAndFallbackEndpoint(t *testing.T) {
	t.Run("pool_first", func(t *testing.T) {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"описание из пула"}}]}`))
		}))
		defer srv.Close()

		oldURL := llm.GeminiURL
		llm.GeminiURL = srv.URL
		t.Cleanup(func() { llm.GeminiURL = oldURL })

		ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
		cfg := &config.Config{
			GeminiAPIKeys: []string{"k1:secret-1"},
			GroqAPIKey:    "groq-unused",
		}
		got, err := DescribeImage(ctx, cfg, make([]byte, 64), "image/png", "Что на рисунке?")
		if err != nil {
			t.Fatalf("DescribeImage: %v", err)
		}
		if got != "описание из пула" {
			t.Fatalf("got = %q", got)
		}
		if gotPath != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
		}
	})

	t.Run("fallback_to_groq_with_chat_endpoint", func(t *testing.T) {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"описание от groq"}}]}`))
		}))
		defer srv.Close()

		cfg := &config.Config{
			GroqAPIKey:      "gsk-test",
			GroqAPIURL:      srv.URL,
			GroqVisionModel: "m",
		}
		got, err := DescribeImage(context.Background(), cfg, make([]byte, 64), "image/png", "Опиши")
		if err != nil {
			t.Fatalf("DescribeImage: %v", err)
		}
		if got != "описание от groq" {
			t.Fatalf("got = %q", got)
		}
		if gotPath != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
		}
	})
}

func TestParseBatchSlideDescriptionsAndPartialFallback(t *testing.T) {
	reply := "Вступительный мусор\n=== Слайд 1 ===\nТекст 1\nСлайд 3 [00:30]:\nТекст 3"
	got := parseBatchSlideDescriptions(reply, []int{1, 2, 3})
	if got[1] != "Текст 1" {
		t.Fatalf("slide 1 = %q", got[1])
	}
	if _, ok := got[2]; ok {
		t.Fatalf("пропущенный слайд 2 не должен быть заполнен: %q", got[2])
	}
	if got[3] != "Текст 3" {
		t.Fatalf("slide 3 = %q", got[3])
	}

	single := parseBatchSlideDescriptions("Просто ответ без заголовка", []int{5})
	if single[5] != "Просто ответ без заголовка" {
		t.Fatalf("single slide fallback = %q", single[5])
	}
}

func TestFormatManifestTimestampAndSharedPoolVision(t *testing.T) {
	cases := []struct {
		name string
		item slideManifestItem
		want string
	}{
		{
			name: "timestamp_with_brackets",
			item: slideManifestItem{Timestamp: "[01:23]"},
			want: "[01:23]",
		},
		{
			name: "timestamp_without_brackets",
			item: slideManifestItem{Timestamp: "01:23"},
			want: "[01:23]",
		},
		{
			name: "positive_offset",
			item: slideManifestItem{ShownOffsetS: 125},
			want: "[02:05]",
		},
		{
			name: "zero_offset_with_first_shown_at",
			item: slideManifestItem{FirstShownAt: "2026-11-14T10:00:00Z", ShownOffsetS: 0},
			want: "[00:00]",
		},
		{
			name: "empty_when_never_shown",
			item: slideManifestItem{ShownOffsetS: 0},
			want: "",
		},
		{
			name: "negative_offset_ignored",
			item: slideManifestItem{FirstShownAt: "2026-11-14T10:00:00Z", ShownOffsetS: -5},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatManifestTimestamp(tc.item); got != tc.want {
				t.Fatalf("formatManifestTimestamp(%+v) = %q, want %q", tc.item, got, tc.want)
			}
		})
	}

	// Проверяем, что shared-ключи из пула распознаются как настроенный vision.
	cfg := &config.Config{}
	if hasVisionConfigured(cfg) {
		t.Fatal("без ключей vision не должен считаться настроенным")
	}
	t.Cleanup(func() { llm.RegisterSharedPoolKeys(nil) })
	llm.RegisterSharedPoolKeys(func(provider string) []llm.Key {
		if provider == llm.Gemini {
			return []llm.Key{{Label: "shared", Secret: "shared-secret", OwnerID: 1}}
		}
		return nil
	})
	if !hasVisionConfigured(cfg) {
		t.Fatal("при наличии shared-ключа Gemini в пуле vision должен считаться настроенным")
	}
}

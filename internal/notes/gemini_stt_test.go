package notes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
)

func TestPlanAudioChunks(t *testing.T) {
	t.Run("короткое 8 мин -> 1 кусок", func(t *testing.T) {
		chunks := planAudioChunks(8*time.Minute, geminiChunkDuration, geminiChunkOverlap)
		if len(chunks) != 1 {
			t.Fatalf("ожидался 1 кусок, получено %d: %+v", len(chunks), chunks)
		}
		if chunks[0].Start != 0 || chunks[0].Length != 8*time.Minute {
			t.Fatalf("неверный кусок: %+v", chunks[0])
		}
	})

	t.Run("среднее 25 мин -> 3 куска с шагом chunk-overlap и перекрытием 8с", func(t *testing.T) {
		dur := 25 * time.Minute
		chunks := planAudioChunks(dur, geminiChunkDuration, geminiChunkOverlap)
		if len(chunks) != 3 {
			t.Fatalf("ожидалось 3 куска, получено %d: %+v", len(chunks), chunks)
		}
		step := geminiChunkDuration - geminiChunkOverlap
		for i := 1; i < len(chunks); i++ {
			if chunks[i].Start-chunks[i-1].Start != step {
				t.Fatalf("кусок %d: неверный шаг %s (ожидалось %s)", i, chunks[i].Start-chunks[i-1].Start, step)
			}
			prevEnd := chunks[i-1].Start + chunks[i-1].Length
			overlap := prevEnd - chunks[i].Start
			if overlap != geminiChunkOverlap {
				t.Fatalf("кусок %d: перекрытие %s (ожидалось %s)", i, overlap, geminiChunkOverlap)
			}
		}
		last := chunks[len(chunks)-1]
		if last.Start+last.Length != dur {
			t.Fatalf("конец последнего куска %s != %s", last.Start+last.Length, dur)
		}
		if last.Length < geminiMinTailDuration {
			t.Fatalf("последний кусок слишком короткий: %s", last.Length)
		}
	})

	t.Run("длинное 90 мин", func(t *testing.T) {
		dur := 90 * time.Minute
		chunks := planAudioChunks(dur, geminiChunkDuration, geminiChunkOverlap)
		if len(chunks) < 9 {
			t.Fatalf("слишком мало кусков для 90 мин: %d", len(chunks))
		}
		step := geminiChunkDuration - geminiChunkOverlap
		for i, ch := range chunks {
			if ch.Length < geminiMinTailDuration {
				t.Fatalf("кусок %d короче %s: %s", i, geminiMinTailDuration, ch.Length)
			}
			if i > 0 {
				if ch.Start-chunks[i-1].Start != step {
					t.Fatalf("кусок %d: шаг %s != %s", i, ch.Start-chunks[i-1].Start, step)
				}
				prevEnd := chunks[i-1].Start + chunks[i-1].Length
				if prevEnd-ch.Start != geminiChunkOverlap {
					t.Fatalf("кусок %d: перекрытие %s != %s", i, prevEnd-ch.Start, geminiChunkOverlap)
				}
			}
		}
		last := chunks[len(chunks)-1]
		if last.Start+last.Length != dur {
			t.Fatalf("последний кусок заканчивается в %s вместо %s", last.Start+last.Length, dur)
		}
	})

	t.Run("короткий хвост (<15с) объединяется с предыдущим куском", func(t *testing.T) {
		dur := 19*time.Minute + 56*time.Second
		chunks := planAudioChunks(dur, geminiChunkDuration, geminiChunkOverlap)
		if len(chunks) != 2 {
			t.Fatalf("ожидалось 2 куска без короткого хвоста, получено %d: %+v", len(chunks), chunks)
		}
		last := chunks[len(chunks)-1]
		if last.Start+last.Length != dur {
			t.Fatalf("конец последнего куска %s != %s", last.Start+last.Length, dur)
		}
		if last.Length < geminiMinTailDuration {
			t.Fatalf("хвост короче %s: %s", geminiMinTailDuration, last.Length)
		}
	})
}

func TestShiftTimestamps(t *testing.T) {
	in := "[00:05] Добрый день.\n[09:58] Переходим к следующей теме."
	got := shiftTimestamps(in, 10*time.Minute)
	want := "[10:05] Добрый день.\n[19:58] Переходим к следующей теме."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	gotLong := shiftTimestamps("[15:00] Формула Ньютона-Лейбница и [1:05:12] пример.", 60*time.Minute)
	wantLong := "[75:00] Формула Ньютона-Лейбница и [125:12] пример."
	if gotLong != wantLong {
		t.Fatalf("got %q, want %q", gotLong, wantLong)
	}
}

func TestStitchTranscriptsOverlap(t *testing.T) {
	t.Run("перекрытие слов с метками времени на стыке", func(t *testing.T) {
		c1 := "[00:00] Сегодня мы разберём алгоритмы сортировки.\n[09:50] Бинарное дерево поиска и его балансировка."
		c2 := "[09:52] бинарное дерево поиска и его балансировка. [10:05] Вставка элемента занимает логарифмическое время."
		got := stitchTranscripts([]string{c1, c2})

		if strings.Count(strings.ToLower(got), "бинарное дерево поиска") != 1 {
			t.Fatalf("дубликат на стыке не удалён: %q", got)
		}
		for _, ts := range []string{"[00:00]", "[09:50]", "[10:05]"} {
			if !strings.Contains(got, ts) {
				t.Fatalf("потеряна метка времени %s в %q", ts, got)
			}
		}
		if !strings.Contains(got, "Вставка элемента занимает логарифмическое время.") {
			t.Fatalf("потерян текст второго куска: %q", got)
		}
	})

	t.Run("сохранение метки перед продолжением без новой метки", func(t *testing.T) {
		c1 := "[00:00] Начало лекции про графы и матрицы смежности."
		c2 := "[09:52] графы и матрицы смежности. Далее рассмотрим обход в ширину и [10:15] очередь."
		got := stitchTranscripts([]string{c1, c2})

		if strings.Count(strings.ToLower(got), "графы и матрицы смежности") != 1 {
			t.Fatalf("дубликат не удалён: %q", got)
		}
		for _, ts := range []string{"[00:00]", "[09:52]", "[10:15]"} {
			if !strings.Contains(got, ts) {
				t.Fatalf("метка %s не сохранена: %q", ts, got)
			}
		}
		if !strings.Contains(got, "Далее рассмотрим обход в ширину") {
			t.Fatalf("потерян текст после перекрытия: %q", got)
		}
	})

	t.Run("обрубленное первое слово на границе куска", func(t *testing.T) {
		c1 := "[00:00] Мы подробно рассматриваем алгоритмы быстрой сортировки"
		c2 := "[09:52] риваем алгоритмы быстрой сортировки и [10:01] их сложность."
		got := stitchTranscripts([]string{c1, c2})

		if strings.Contains(got, " риваем") {
			t.Fatalf("обрывок слова не удалён: %q", got)
		}
		if strings.Count(strings.ToLower(got), "алгоритмы быстрой сортировки") != 1 {
			t.Fatalf("дубликат не удалён: %q", got)
		}
		if !strings.Contains(got, "[10:01] их сложность.") {
			t.Fatalf("остаток с меткой потерян: %q", got)
		}
	})
}

func TestTranscribeGeminiParallelAndFallbackToPaid(t *testing.T) {
	origGeminiURL := llm.GeminiURL
	origProbe := probeAudioFn
	origSlice := sliceAudioFn
	t.Cleanup(func() {
		llm.GeminiURL = origGeminiURL
		probeAudioFn = origProbe
		sliceAudioFn = origSlice
	})

	audioPath := filepath.Join(t.TempDir(), "lecture.ogg")
	if err := os.WriteFile(audioPath, []byte(strings.Repeat("a", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("параллельная отправка 3 кусков, обработка 429 и отсутствие вызовов платного провайдера", func(t *testing.T) {
		var tempFiles []string
		var tempMu sync.Mutex

		probeAudioFn = func(ctx context.Context, path string) (time.Duration, float64, error) {
			return 22 * time.Minute, -18.0, nil
		}
		sliceAudioFn = func(ctx context.Context, path string, start, length time.Duration, outMP3 string) error {
			chunkTag := fmt.Sprintf("MP3-CHUNK-%d", int(start.Seconds()))
			if err := os.WriteFile(outMP3, []byte(chunkTag), 0o600); err != nil {
				return err
			}
			tempMu.Lock()
			tempFiles = append(tempFiles, outMP3)
			tempMu.Unlock()
			return nil
		}

		var (
			mu          sync.Mutex
			keyCalls    = map[string]int{}
			inFlight    atomic.Int32
			maxInFlight atomic.Int32
			k1RateLimit atomic.Bool
		)

		geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cur := inFlight.Add(1)
			for {
				prev := maxInFlight.Load()
				if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
					break
				}
			}
			defer inFlight.Add(-1)

			// Небольшая пауза, чтобы параллельные воркеры гарантированно пересеклись во времени.
			time.Sleep(40 * time.Millisecond)

			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			mu.Lock()
			keyCalls[auth]++
			mu.Unlock()

			// Первый вызов на ключ k1 возвращает 429, чтобы проверить cooldown и переключение на k2.
			if auth == "key-one" && k1RateLimit.CompareAndSwap(false, true) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, `{"error":"rate limit"}`, http.StatusTooManyRequests)
				return
			}

			var req struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			var audioTag string
			for _, m := range req.Messages {
				var parts []json.RawMessage
				if err := json.Unmarshal(m.Content, &parts); err != nil {
					continue
				}
				for _, raw := range parts {
					var part struct {
						Type       string `json:"type"`
						InputAudio struct {
							Data   string `json:"data"`
							Format string `json:"format"`
						} `json:"input_audio"`
					}
					if err := json.Unmarshal(raw, &part); err == nil && part.Type == "input_audio" {
						b, err := base64.StdEncoding.DecodeString(part.InputAudio.Data)
						if err == nil {
							audioTag = string(b)
						}
					}
				}
			}

			var reply string
			switch audioTag {
			case "MP3-CHUNK-0":
				reply = "[00:05] Введение в теорию графов. [09:54] Вершины и рёбра графа."
			case "MP3-CHUNK-592":
				reply = "[00:02] Вершины и рёбра графа. [05:10] Матрица смежности графа и её свойства."
			case "MP3-CHUNK-1184":
				reply = "[00:01] матрица смежности графа и её свойства. [01:20] Список рёбер."
			default:
				http.Error(w, "unexpected chunk: "+audioTag, http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]string{"content": reply}},
				},
			})
		}))
		t.Cleanup(geminiSrv.Close)
		llm.GeminiURL = geminiSrv.URL

		var paidCalls atomic.Int32
		paidFishSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paidCalls.Add(1)
			http.Error(w, "paid provider must not be called", http.StatusInternalServerError)
		}))
		t.Cleanup(paidFishSrv.Close)

		cfg := &config.Config{
			GeminiAPIKeys:    []string{"K1:key-one", "K2:key-two"},
			FishStudioAPIKey: "fish-paid-key",
			FishStudioAPIURL: paidFishSrv.URL,
		}
		ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
		got, err := Transcribe(ctx, cfg, audioPath)
		if err != nil {
			t.Fatalf("Transcribe failed: %v", err)
		}

		if paidCalls.Load() != 0 {
			t.Fatalf("платный провайдер вызван %d раз, ожидалось 0", paidCalls.Load())
		}
		if maxInFlight.Load() < 2 {
			t.Fatalf("ожидалась параллельная обработка (maxInFlight >= 2), получено %d", maxInFlight.Load())
		}
		mu.Lock()
		c1, c2 := keyCalls["key-one"], keyCalls["key-two"]
		mu.Unlock()
		if c1 == 0 || c2 == 0 {
			t.Fatalf("куски должны распределяться между разными ключами: key-one=%d, key-two=%d", c1, c2)
		}

		// Проверяем сдвиги меток времени (592с = 9м52с, 1184с = 19м44с) и удаление дублей на стыках.
		for _, wantTS := range []string{"[00:05]", "[09:54]", "[15:02]", "[21:04]"} {
			if !strings.Contains(got, wantTS) {
				t.Fatalf("в результате отсутствует метка %s: %q", wantTS, got)
			}
		}
		if strings.Count(strings.ToLower(got), "вершины и рёбра графа") != 1 {
			t.Fatalf("перекрытие 1-2 не склеено: %q", got)
		}
		if strings.Count(strings.ToLower(got), "матрица смежности графа и её свойства") != 1 {
			t.Fatalf("перекрытие 2-3 не склеено: %q", got)
		}

		// Проверяем, что временные файлы кусков удалены в defer.
		for _, p := range tempFiles {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("временный файл %s не удалён", p)
			}
		}
	})

	t.Run("откат на платный Fish при провале Gemini", func(t *testing.T) {
		probeAudioFn = func(ctx context.Context, path string) (time.Duration, float64, error) {
			return 5 * time.Minute, -20.0, nil
		}
		sliceAudioFn = func(ctx context.Context, path string, start, length time.Duration, outMP3 string) error {
			return os.WriteFile(outMP3, []byte("MP3-SINGLE"), 0o600)
		}

		geminiFailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "gemini unavailable", http.StatusServiceUnavailable)
		}))
		t.Cleanup(geminiFailSrv.Close)
		llm.GeminiURL = geminiFailSrv.URL

		var fishCalled atomic.Bool
		fishSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fishCalled.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"резервная расшифровка fish"}`))
		}))
		t.Cleanup(fishSrv.Close)

		cfg := &config.Config{
			GeminiAPIKeys:    []string{"K1:only-fail-key"},
			FishStudioAPIKey: "fish-key",
			FishStudioAPIURL: fishSrv.URL,
		}
		ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
		got, err := Transcribe(ctx, cfg, audioPath)
		if err != nil {
			t.Fatalf("ожидался fallback на Fish, получена ошибка: %v", err)
		}
		if !fishCalled.Load() {
			t.Fatalf("Fish не был вызван при провале Gemini")
		}
		if got != "резервная расшифровка fish" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("отсутствие ключей Gemini сразу переходит к Fish", func(t *testing.T) {
		fishSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"fish без gemini"}`))
		}))
		t.Cleanup(fishSrv.Close)

		cfg := &config.Config{
			FishStudioAPIKey: "fish-key",
			FishStudioAPIURL: fishSrv.URL,
		}
		got, err := Transcribe(context.Background(), cfg, audioPath)
		if err != nil {
			t.Fatalf("Transcribe failed: %v", err)
		}
		if got != "fish без gemini" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestTruncateUTF8(t *testing.T) {
	in := "Привет, мир и кириллица!"
	got := truncate(in, 6)
	if !utf8.ValidString(got) {
		t.Fatalf("невалидный UTF-8 после обрезки: %q", got)
	}
	if got != "Привет…" {
		t.Fatalf("got %q, want %q", got, "Привет…")
	}

	if gotZero := truncate(in, 0); gotZero != "" {
		t.Fatalf("при n=0 ожидалась пустая строка, получено %q", gotZero)
	}
	if gotExact := truncate("  Тест  ", 4); gotExact != "Тест" {
		t.Fatalf("got %q, want %q", gotExact, "Тест")
	}
}

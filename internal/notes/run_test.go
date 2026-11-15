package notes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// TestTranscriptGrowthThresholdAndValidation проверяет порог роста аудио: max(5 мин, 10% от cachedDur).
// При росте меньше порога кэш остаётся валидным, при росте больше или равном порогу — инвалидируется.
func TestTranscriptGrowthThresholdAndValidation(t *testing.T) {
	tests := []struct {
		name       string
		cachedDur  time.Duration
		dur        time.Duration
		wantThresh time.Duration
		wantValid  bool
	}{
		{
			name:       "30 минут: порог 5 мин, рост 0",
			cachedDur:  30 * time.Minute,
			dur:        30 * time.Minute,
			wantThresh: 5 * time.Minute,
			wantValid:  true,
		},
		{
			name:       "30 минут: порог 5 мин, рост 3 мин (< 5 мин)",
			cachedDur:  30 * time.Minute,
			dur:        33 * time.Minute,
			wantThresh: 5 * time.Minute,
			wantValid:  true,
		},
		{
			name:       "30 минут: порог 5 мин, рост 4 мин 59 сек (< 5 мин)",
			cachedDur:  30 * time.Minute,
			dur:        34*time.Minute + 59*time.Second,
			wantThresh: 5 * time.Minute,
			wantValid:  true,
		},
		{
			name:       "30 минут: порог 5 мин, рост ровно 5 мин (инвалидация)",
			cachedDur:  30 * time.Minute,
			dur:        35 * time.Minute,
			wantThresh: 5 * time.Minute,
			wantValid:  false,
		},
		{
			name:       "30 минут: порог 5 мин, рост 6 мин (> 5 мин, инвалидация)",
			cachedDur:  30 * time.Minute,
			dur:        36 * time.Minute,
			wantThresh: 5 * time.Minute,
			wantValid:  false,
		},
		{
			name:       "80 минут: 10% = 8 мин (порог 8 мин), рост 7 мин (< 8 мин)",
			cachedDur:  80 * time.Minute,
			dur:        87 * time.Minute,
			wantThresh: 8 * time.Minute,
			wantValid:  true,
		},
		{
			name:       "80 минут: порог 8 мин, рост ровно 8 мин (инвалидация)",
			cachedDur:  80 * time.Minute,
			dur:        88 * time.Minute,
			wantThresh: 8 * time.Minute,
			wantValid:  false,
		},
		{
			name:       "80 минут: порог 8 мин, рост 9 мин (инвалидация)",
			cachedDur:  80 * time.Minute,
			dur:        89 * time.Minute,
			wantThresh: 8 * time.Minute,
			wantValid:  false,
		},
		{
			name:       "Уменьшение длительности в пределах порога (40 -> 36 мин, разница 4 мин < 5 мин): кэш валиден",
			cachedDur:  40 * time.Minute,
			dur:        36 * time.Minute,
			wantThresh: 5 * time.Minute,
			wantValid:  true,
		},
		{
			name:       "Уменьшение длительности ровно на порог (40 -> 35 мин, разница 5 мин): инвалидация",
			cachedDur:  40 * time.Minute,
			dur:        35 * time.Minute,
			wantThresh: 5 * time.Minute,
			wantValid:  false,
		},
		{
			name:       "Существенное уменьшение длительности (40 -> 20 мин): инвалидация",
			cachedDur:  40 * time.Minute,
			dur:        20 * time.Minute,
			wantThresh: 5 * time.Minute,
			wantValid:  false,
		},
		{
			name:       "80 минут: порог 8 мин, уменьшение на 7 мин (< 8 мин): кэш валиден",
			cachedDur:  80 * time.Minute,
			dur:        73 * time.Minute,
			wantThresh: 8 * time.Minute,
			wantValid:  true,
		},
		{
			name:       "80 минут: порог 8 мин, уменьшение ровно на 8 мин: инвалидация",
			cachedDur:  80 * time.Minute,
			dur:        72 * time.Minute,
			wantThresh: 8 * time.Minute,
			wantValid:  false,
		},
		{
			name:       "Короткая запись 10 минут: 10% = 1 мин, но порог min 5 мин",
			cachedDur:  10 * time.Minute,
			dur:        14 * time.Minute,
			wantThresh: 5 * time.Minute,
			wantValid:  true,
		},
		{
			name:       "Короткая запись 10 минут: рост 5 мин -> инвалидация",
			cachedDur:  10 * time.Minute,
			dur:        15 * time.Minute,
			wantThresh: 5 * time.Minute,
			wantValid:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotThresh := transcriptGrowthThreshold(tt.cachedDur)
			if gotThresh != tt.wantThresh {
				t.Errorf("transcriptGrowthThreshold(%v) = %v, ожидалось %v", tt.cachedDur, gotThresh, tt.wantThresh)
			}
			gotValid := isTranscriptCacheValid(tt.dur, tt.cachedDur)
			if gotValid != tt.wantValid {
				t.Errorf("isTranscriptCacheValid(dur=%v, cached=%v) = %v, ожидалось %v", tt.dur, tt.cachedDur, gotValid, tt.wantValid)
			}
		})
	}
}

// TestComputeSlidesHash проверяет вычисление SHA-256 слайдов:
// неизменные картинки -> хэш совпадает (кэш хит);
// изменение байта, добавление слайда или манифеста slides.json -> хэш меняется (кэш мисс).
func TestComputeSlidesHash(t *testing.T) {
	slidesDir := t.TempDir()

	// 1. Пустая папка слайдов
	h0, count0, err := computeSlidesHash(slidesDir)
	if err != nil {
		t.Fatalf("computeSlidesHash на пустой папке: %v", err)
	}
	if h0 != "" || count0 != 0 {
		t.Fatalf("ожидался пустой хэш для пустой папки, получено h=%q count=%d", h0, count0)
	}

	// 2. Создаем две картинки
	f1 := filepath.Join(slidesDir, "01.png")
	f2 := filepath.Join(slidesDir, "02.jpg")
	if err := os.WriteFile(f1, []byte("картинка-1-пиксели"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("картинка-2-пиксели"), 0644); err != nil {
		t.Fatal(err)
	}

	h1, count1, err := computeSlidesHash(slidesDir)
	if err != nil {
		t.Fatalf("computeSlidesHash с 2 картинками: %v", err)
	}
	if count1 != 2 || h1 == "" {
		t.Fatalf("ожидалось 2 картинки и непустой хэш, получено count=%d h=%q", count1, h1)
	}

	// 3. Повторный вызов без изменений — хэш строго совпадает (кэш хит)
	h2, count2, err := computeSlidesHash(slidesDir)
	if err != nil || count2 != 2 || h2 != h1 {
		t.Fatalf("детерминированность хэша нарушена: h1=%s, h2=%s, err=%v", h1, h2, err)
	}

	// 4. Изменяем один байт в первой картинке — хэш обязан измениться (кэш мисс)
	if err := os.WriteFile(f1, []byte("картинка-1-изменена"), 0644); err != nil {
		t.Fatal(err)
	}
	h3, count3, err := computeSlidesHash(slidesDir)
	if err != nil || count3 != 2 {
		t.Fatalf("ошибка вычисления: %v", err)
	}
	if h3 == h1 {
		t.Fatalf("хэш не изменился после изменения байта картинки: h1=%s, h3=%s", h1, h3)
	}

	// 5. Возвращаем исходное содержимое f1 и добавляем третий слайд — хэш меняется
	_ = os.WriteFile(f1, []byte("картинка-1-пиксели"), 0644)
	f3 := filepath.Join(slidesDir, "03.webp")
	if err := os.WriteFile(f3, []byte("картинка-3-пиксели"), 0644); err != nil {
		t.Fatal(err)
	}
	h4, count4, err := computeSlidesHash(slidesDir)
	if err != nil || count4 != 3 {
		t.Fatalf("ошибка вычисления с 3 слайдами: %v", err)
	}
	if h4 == h1 {
		t.Fatalf("хэш не изменился после добавления нового слайда: h1=%s, h4=%s", h1, h4)
	}

	// 6. Добавляем slides.json (манифест слайдов) — хэш меняется
	manifest := filepath.Join(slidesDir, "slides.json")
	if err := os.WriteFile(manifest, []byte(`{"slides":["01.png","02.jpg","03.webp"]}`), 0644); err != nil {
		t.Fatal(err)
	}
	h5, _, err := computeSlidesHash(slidesDir)
	if err != nil || h5 == h4 {
		t.Fatalf("хэш обязан измениться при появлении slides.json: h4=%s, h5=%s", h4, h5)
	}

	// 7. Изменяем содержимое slides.json — хэш снова меняется
	if err := os.WriteFile(manifest, []byte(`{"updated":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	h6, _, err := computeSlidesHash(slidesDir)
	if err != nil || h6 == h5 {
		t.Fatalf("хэш обязан измениться при изменении slides.json: h5=%s, h6=%s", h5, h6)
	}
}

// TestStepSlidesCacheHit проверяет, что при совпадении хэша картинок описание берётся
// из кэша и DescribeSlides не вызывается.
func TestStepSlidesCacheHit(t *testing.T) {
	dir := t.TempDir()
	slidesDir := filepath.Join(dir, "slides")
	if err := os.MkdirAll(slidesDir, 0755); err != nil {
		t.Fatal(err)
	}
	slideFile := filepath.Join(slidesDir, "s1.png")
	if err := os.WriteFile(slideFile, []byte("пиксели слайда"), 0644); err != nil {
		t.Fatal(err)
	}

	hash, _, err := computeSlidesHash(slidesDir)
	if err != nil {
		t.Fatal(err)
	}

	// Заполняем кэш готовым описанием
	bc := &buildCache{
		SlidesHash: hash,
		SlidesDesc: "Слайд 1: закэшированная архитектура системы",
	}

	// Вызываем stepSlides с cfg = nil: без кэша DescribeSlides вернул бы пустую строку,
	// так как провайдеров нет. С кэшем должно вернуться описание из bc.
	resS, err := stepSlides(context.Background(), nil, dir, slidesDir, bc)
	res := resS.Text
	if err != nil {
		t.Fatalf("stepSlides вернул ошибку: %v", err)
	}
	if res != "Слайд 1: закэшированная архитектура системы" {
		t.Fatalf("ожидалось описание из кэша, получено: %q", res)
	}

	// Проверяем чтение из .slides_desc.txt, если в структуре bc поле пустое
	bcEmptyDesc := &buildCache{
		SlidesHash: hash,
	}
	if err := os.WriteFile(slidesDescPath(dir), []byte("Слайд 1: описание из файла диска"), 0644); err != nil {
		t.Fatal(err)
	}
	resFileS, err := stepSlides(context.Background(), nil, dir, slidesDir, bcEmptyDesc)
	resFile := resFileS.Text
	if err != nil {
		t.Fatalf("stepSlides вернул ошибку: %v", err)
	}
	if resFile != "Слайд 1: описание из файла диска" {
		t.Fatalf("ожидалось описание из .slides_desc.txt, получено: %q", resFile)
	}
}

// TestStepSlidesIncompleteNotCached проверяет, что неполное описание (vision упал на всех повторах)
// не кэшируется: при следующей сборке слайды пересчитываются и берётся полный vision-результат.
func TestStepSlidesIncompleteNotCached(t *testing.T) {
	fastVisionRetry(t)
	dir := t.TempDir()
	slidesDir := filepath.Join(dir, "slides")
	if err := os.MkdirAll(slidesDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeSlides(t, slidesDir, 3, true)
	cfg := &config.Config{GeminiAPIKeys: []string{"k1:secret-1"}}
	perAttempt := visionCallsPerAttempt(cfg)

	var (
		failing atomic.Bool
		calls   atomic.Int32
	)
	failing.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if failing.Load() {
			http.Error(w, "upstream 500", http.StatusInternalServerError)
			return
		}
		writeVisionOK(w, visionReplyFor("Vision полный слайд", 1, 2, 3))
	}))
	defer srv.Close()

	oldURL := llm.GeminiURL
	llm.GeminiURL = srv.URL
	t.Cleanup(func() { llm.GeminiURL = oldURL })

	ctx := llm.WithPool(context.Background(), llm.NewKeyPool())
	bc := &buildCache{}

	// Первая сборка: vision не справился, слайды пришли из текстового слоя.
	resS, err := stepSlides(ctx, cfg, dir, slidesDir, bc)
	if err != nil {
		t.Fatalf("stepSlides вернул ошибку: %v", err)
	}
	res := resS.Text
	if !resS.Incomplete || len(resS.FailedSlides) != 3 {
		t.Fatalf("неполный результат должен быть помечен Incomplete со слайдами 1..3: %+v", resS)
	}
	if !strings.Contains(res, "Текстовый слой слайда 1") {
		t.Fatalf("ожидался текстовый слой как запасной вариант:\n%s", res)
	}
	if bc.SlidesHash != "" || bc.SlidesDesc != "" {
		t.Fatalf("неполное описание не должно попасть в кэш: hash=%q", bc.SlidesHash)
	}
	if n := int(calls.Load()); n != visionBatchAttempts*perAttempt {
		t.Fatalf("ожидалось %d попыток vision по %d запросов, выполнено %d запросов", visionBatchAttempts, perAttempt, n)
	}

	// Вторая сборка: vision восстановился — слайды обязаны пересчитаться, а не отдаться из кэша.
	failing.Store(false)
	res2S, err := stepSlides(ctx, cfg, dir, slidesDir, bc)
	if err != nil {
		t.Fatalf("повторный stepSlides вернул ошибку: %v", err)
	}
	res2 := res2S.Text
	if res2S.Incomplete {
		t.Fatalf("полный пересчёт не должен быть помечен Incomplete")
	}
	if !strings.Contains(res2, "Vision полный слайд 1") {
		t.Fatalf("при пересчёте ожидалось полное vision-описание:\n%s", res2)
	}
	if bc.SlidesHash == "" {
		t.Fatalf("полный результат должен записаться в кэш")
	}
}

// TestStepNotesCacheHitAndInvalidation проверяет:
// 1. При совпадении хэша входа или при падении сборки на WritePDF конспект берётся с диска без вызова LLM.
// 2. При изменении входных данных (транскрипт/слайды) кэш инвалидируется и происходит повторная суммаризация.
func TestStepNotesCacheHitAndInvalidation(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "notes.md")
	readyMD := "## Введение\nГотовый конспект из предыдущего запуска."
	if err := os.WriteFile(mdPath, []byte(readyMD), 0644); err != nil {
		t.Fatal(err)
	}

	discipline := "Математический анализ"
	number := 1
	transcript := "сегодня разбираем пределы последовательностей"
	slidesText := "Слайд 1: определение предела по Коши"
	inputHash := computeSummarizeInputHash(discipline, number, transcript, slidesText)

	// Сценарий 1: падение на WritePDF (в кэше ещё нет SummarizeInputHash, но notes.md уже на диске)
	bc1 := &buildCache{}
	got1, err := stepNotes(context.Background(), nil, dir, mdPath, discipline, number, transcript, slidesText, bc1)
	if err != nil {
		t.Fatalf("stepNotes сценарий 1 ошибка: %v", err)
	}
	if got1 != readyMD {
		t.Fatalf("stepNotes сценарий 1 вернул %q, ожидалось %q", got1, readyMD)
	}
	if bc1.SummarizeInputHash != inputHash {
		t.Fatalf("bc.SummarizeInputHash не обновился: %q, ожидался %q", bc1.SummarizeInputHash, inputHash)
	}

	// Сценарий 2: повторный вызов при неизменном входе и заполненном кэше
	got2, err := stepNotes(context.Background(), nil, dir, mdPath, discipline, number, transcript, slidesText, bc1)
	if err != nil {
		t.Fatalf("stepNotes сценарий 2 ошибка: %v", err)
	}
	if got2 != readyMD {
		t.Fatalf("stepNotes сценарий 2 вернул %q, ожидалось %q", got2, readyMD)
	}

	// Сценарий 3: изменился транскрипт -> входной хэш изменился -> попытка повторной суммаризации
	newTranscript := "сегодня разбираем теорему Вейерштрасса"
	// С cfg = nil Summarize гарантированно вернёт ошибку отсутствия ключей ИИ,
	// что доказывает: кэш не был взят вслепую, произошёл реальный вызов Summarize!
	_, err3 := stepNotes(context.Background(), nil, dir, mdPath, discipline, number, newTranscript, slidesText, bc1)
	if err3 == nil || !strings.Contains(err3.Error(), "нет ключей ИИ") {
		t.Fatalf("при изменении входа ожидалась попытка вызова Summarize с ошибкой отсутствия ключей, получено: %v", err3)
	}
}

// TestStepPDFCacheHit проверяет пропуск генерации PDF, если PDF уже существует и валиден.
func TestStepPDFCacheHit(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "notes.pdf")
	pdfContent := "%PDF-1.4 фейковый pdf контент для теста"
	if err := os.WriteFile(pdfPath, []byte(pdfContent), 0644); err != nil {
		t.Fatal(err)
	}

	md := "## Конспект\nСодержимое лекции"
	mdHash := computeStringSHA256(md)

	// Если хэш совпадает — рендер пропускается даже при cfg = nil
	bc := &buildCache{
		NotesMDHash:  mdHash,
		PDFNotesHash: mdHash,
	}
	if err := stepPDF(context.Background(), nil, dir, pdfPath, "Заголовок", md, bc); err != nil {
		t.Fatalf("stepPDF с валидным кэшем вернул ошибку: %v", err)
	}

	// Если конспект изменился, stepPDF пытается перерендерить PDF (с невалидным cfg упадет)
	newMD := "## Конспект\nНовое содержимое"
	err := stepPDF(context.Background(), &config.Config{ChromeBin: "no-such-binary"}, dir, pdfPath, "Заголовок", newMD, bc)
	if err == nil {
		t.Fatalf("ожидалась ошибка рендера при изменившемся markdown")
	}
}

// TestBuildCachesAllStagesEndToEnd проверяет сквозную сборку лекции:
// Первый запуск выполняет этапы (STT, LLM, PDF) и заполняет .build_cache.json.
// Второй запуск (даже после отключения мок-сервера) берёт все данные из кэша без сетевых запросов.
func TestBuildCachesAllStagesEndToEnd(t *testing.T) {
	bin := chromeForPDF(t)

	// Мок-сервер для STT и LLM
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/asr"):
			_, _ = w.Write([]byte(`{"text":"лекция про алгоритмы сортировки и сложность O(n log n)"}`))
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"## Алгоритмы\nСортировка слиянием и быстрая сортировка."}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))

	oldURL := llm.OpenCodeURL
	llm.OpenCodeURL = api.URL
	t.Cleanup(func() { llm.OpenCodeURL = oldURL })

	root := t.TempDir()
	p := model.LecturePack{Discipline: "Алгоритмы", Number: 5, Dir: "Алгоритмы/лекция-5"}
	dir := filepath.Join(root, p.Dir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// Создаем тестовое аудио
	if err := os.WriteFile(archive.AudioFile(dir), []byte(strings.Repeat("a", 8192)), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		FishStudioAPIKey: "test-key", FishStudioAPIURL: api.URL,
		OpenCodeAPIKey: "test-key",
		ChromeBin:      bin,
	}

	// 1. Первый запуск Build: создаёт transcript.txt, notes.md, notes.pdf и .build_cache.json
	if err := Build(context.Background(), cfg, root, p); err != nil {
		t.Fatalf("первый запуск Build: %v", err)
	}

	// Проверяем созданные файлы
	cacheFile := buildCachePath(dir)
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("файл кэша .build_cache.json не найден: %v", err)
	}
	bc := loadBuildCache(dir)
	if bc.SummarizeInputHash == "" || bc.NotesMDHash == "" || bc.PDFNotesHash == "" {
		t.Fatalf("метаданные кэша не заполнены: %+v", bc)
	}

	// Закрываем мок-сервер!
	api.Close()

	// 2. Второй запуск Build: сервер выключен, если бы пошёл сетевой запрос — тест бы упал.
	// Но Build должен отработать быстро и успешно из кэша!
	if err := Build(context.Background(), cfg, root, p); err != nil {
		t.Fatalf("второй запуск Build из кэша завершился с ошибкой: %v", err)
	}

	// Проверяем целостность файлов конспекта и расшифровки
	tr, _ := os.ReadFile(archive.TranscriptFile(dir))
	if !strings.Contains(string(tr), "сортировки") {
		t.Fatalf("transcript повреждён: %q", tr)
	}
	md, _ := os.ReadFile(archive.NotesMD(dir))
	if !strings.Contains(string(md), "Сортировка слиянием") {
		t.Fatalf("notes.md повреждён: %q", md)
	}
	assertNoTempFiles(t, dir)
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") || strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("обнаружен оставшийся временный файл %q в %s", e.Name(), dir)
		}
	}
}

// TestWriteFileAtomic проверяет атомарную запись файла, права доступа, перезапись и отсутствие .tmp файлов.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "artifact.txt")

	if err := writeFileAtomic(target, []byte("первая версия"), 0644); err != nil {
		t.Fatalf("первая запись writeFileAtomic: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("чтение после первой записи: %v", err)
	}
	if string(got) != "первая версия" {
		t.Fatalf("содержимое = %q, ожидалось %q", got, "первая версия")
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0644 {
		t.Fatalf("права доступа = %v, ожидалось 0644", st.Mode().Perm())
	}

	if err := writeFileAtomic(target, []byte("вторая версия"), 0600); err != nil {
		t.Fatalf("перезапись writeFileAtomic: %v", err)
	}
	got2, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != "вторая версия" {
		t.Fatalf("содержимое после перезаписи = %q", got2)
	}
	st2, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Mode().Perm() != 0600 {
		t.Fatalf("права доступа после перезаписи = %v, ожидалось 0600", st2.Mode().Perm())
	}

	assertNoTempFiles(t, filepath.Dir(target))
}

// TestAudioSizeCompatibility проверяет проверку размера аудиофайла против подмены.
func TestAudioSizeCompatibility(t *testing.T) {
	tests := []struct {
		name      string
		audioSz   int64
		cachedSz  int64
		dur       time.Duration
		cachedDur time.Duration
		want      bool
	}{
		{"кэш размера не задан", 5000, 0, 30 * time.Minute, 30 * time.Minute, true},
		{"размер не изменился", 10000, 10000, 30 * time.Minute, 30 * time.Minute, true},
		{"уменьшение <= 20%", 8500, 10000, 30 * time.Minute, 30 * time.Minute, true},
		{"уменьшение > 20% (инвалидация)", 7900, 10000, 30 * time.Minute, 30 * time.Minute, false},
		{"рост > 20% при росте длительности допустим", 14000, 10000, 14 * time.Minute, 10 * time.Minute, true},
		{"рост > 20% без роста длительности (инвалидация)", 14000, 10000, 10 * time.Minute, 10 * time.Minute, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAudioSizeCompatible(tt.audioSz, tt.cachedSz, tt.dur, tt.cachedDur); got != tt.want {
				t.Errorf("isAudioSizeCompatible(%d, %d, %v, %v) = %v, ожидалось %v",
					tt.audioSz, tt.cachedSz, tt.dur, tt.cachedDur, got, tt.want)
			}
		})
	}
}

// TestStepTranscriptDeletedOrSilentAudioIgnoresCache проверяет, что при отсутствии audio.ogg
// или замене его на тишину (why != "") старый transcript.txt НЕ возвращается из кэша.
func TestStepTranscriptDeletedOrSilentAudioIgnoresCache(t *testing.T) {
	t.Run("удалённый audio.ogg игнорирует старый transcript.txt", func(t *testing.T) {
		for _, withCache := range []bool{false, true} {
			root := t.TempDir()
			p := model.LecturePack{Discipline: "Физика", Number: 1, Dir: "Физика/лекция-1"}
			dir := filepath.Join(root, p.Dir)
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}

			trPath := archive.TranscriptFile(dir)
			if err := os.WriteFile(trPath, []byte("старая расшифровка удалённой записи"), 0644); err != nil {
				t.Fatal(err)
			}

			bc := &buildCache{}
			if withCache {
				bc.AudioDurationSec = 1800
				bc.AudioSize = 100000
				if err := saveBuildCache(dir, bc); err != nil {
					t.Fatal(err)
				}
			}

			sttCalled := false
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sttCalled = true
				_, _ = w.Write([]byte(`{"text":"не должно вызываться"}`))
			}))
			defer api.Close()

			cfg := &config.Config{FishStudioAPIKey: "k", FishStudioAPIURL: api.URL}
			tr, empty, why, err := stepTranscript(context.Background(), cfg, dir, archive.AudioFile(dir), trPath, bc)
			if err != nil {
				t.Fatalf("stepTranscript(withCache=%v) ошибка: %v", withCache, err)
			}
			if !empty || why == "" || tr != "" {
				t.Fatalf("withCache=%v: ожидалось empty=true, why!=empty, tr=''; получено empty=%v why=%q tr=%q",
					withCache, empty, why, tr)
			}
			if sttCalled {
				t.Fatalf("STT не должен вызываться при отсутствии audio.ogg")
			}

			err = Build(context.Background(), cfg, root, p)
			if !errors.Is(err, ErrEmptyAudio) {
				t.Fatalf("Build с удалённым audio.ogg и старым transcript.txt: ожидалось ErrEmptyAudio, получено %v", err)
			}
		}
	})

	t.Run("замена audio.ogg на тишину игнорирует старый transcript.txt", func(t *testing.T) {
		root := t.TempDir()
		p := model.LecturePack{Discipline: "Физика", Number: 2, Dir: "Физика/лекция-2"}
		dir := filepath.Join(root, p.Dir)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}

		audioPath := archive.AudioFile(dir)
		ffmpegAudio(t, audioPath, "anullsrc=r=48000:cl=mono")

		trPath := archive.TranscriptFile(dir)
		if err := os.WriteFile(trPath, []byte(strings.Repeat("старая речь лекции ", 20)), 0644); err != nil {
			t.Fatal(err)
		}

		bc := &buildCache{
			AudioDurationSec: 70,
			AudioSize:        fileSize(audioPath),
		}
		if err := saveBuildCache(dir, bc); err != nil {
			t.Fatal(err)
		}

		apiHit := false
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiHit = true
			_, _ = w.Write([]byte(`{"text":"выдуманная фраза"}`))
		}))
		defer api.Close()

		cfg := &config.Config{
			FishStudioAPIKey: "k", FishStudioAPIURL: api.URL,
			LLMAPIKey: "k", LLMAPIURL: api.URL + "/v1", LLMModel: "m",
		}
		tr, empty, why, err := stepTranscript(context.Background(), cfg, dir, audioPath, trPath, bc)
		if err != nil {
			t.Fatalf("stepTranscript ошибка: %v", err)
		}
		if !empty || why == "" || tr != "" {
			t.Fatalf("ожидалось empty=true, why!=empty, tr=''; получено empty=%v why=%q tr=%q", empty, why, tr)
		}

		err = Build(context.Background(), cfg, root, p)
		if !errors.Is(err, ErrEmptyAudio) {
			t.Fatalf("Build с тишиной: ожидалось ErrEmptyAudio, получено %v", err)
		}
		if apiHit {
			t.Fatal("тишина при наличии старого transcript.txt не должна вызывать STT/LLM")
		}
	})
}

// TestStepTranscriptInvalidatesOnShorterAudioAndSizeDrop проверяет:
// 1. Замену audio.ogg на существенно более короткое валидное аудио (cachedDur - dur >= threshold) -> инвалидация кэша.
// 2. Значительное уменьшение размера файла при неизменной/неизвестной длительности -> инвалидация кэша.
// 3. Наличие transcript.txt от старой версии без .build_cache.json при валидном аудио -> использование кэша и сохранение метаданных.
func TestStepTranscriptInvalidatesOnShorterAudioAndSizeDrop(t *testing.T) {
	dir := t.TempDir()
	audioPath := archive.AudioFile(dir)
	ffmpegAudio(t, audioPath, "sine=frequency=440:sample_rate=48000") // 70 секунд валидного звука
	trPath := archive.TranscriptFile(dir)

	oldText := strings.TrimSpace(strings.Repeat("старый конспект прошлой длинной записи ", 10))
	newText := strings.TrimSpace(strings.Repeat("новая расшифровка укороченной валидной записи ", 10))

	var sttCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sttCalls++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"` + newText + `"}`))
	}))
	defer api.Close()
	cfg := &config.Config{FishStudioAPIKey: "k", FishStudioAPIURL: api.URL}

	// Сценарий 1: старый transcript.txt без .build_cache.json при валидном аудио -> кэш хит + сохранение базы
	if err := os.WriteFile(trPath, []byte(oldText), 0644); err != nil {
		t.Fatal(err)
	}
	bcLegacy := &buildCache{}
	gotLegacy, empty, why, err := stepTranscript(context.Background(), cfg, dir, audioPath, trPath, bcLegacy)
	if err != nil || empty || why != "" {
		t.Fatalf("legacy cache hit failed: empty=%v why=%q err=%v", empty, why, err)
	}
	if gotLegacy != strings.TrimSpace(oldText) || sttCalls != 0 {
		t.Fatalf("ожидался хит legacy-кэша без вызова STT, sttCalls=%d", sttCalls)
	}
	if bcLegacy.AudioDurationSec < 60 || bcLegacy.AudioSize <= 0 {
		t.Fatalf("legacy-кэш не заполнил метаданные: %+v", bcLegacy)
	}

	// Сценарий 2: в кэше длительность 10 минут, а новое валидное аудио — 70 секунд (разница > 5 мин) -> инвалидация
	bcShort := &buildCache{
		AudioDurationSec: (10 * time.Minute).Seconds(),
		AudioSize:        fileSize(audioPath),
	}
	gotNew, empty, why, err := stepTranscript(context.Background(), cfg, dir, audioPath, trPath, bcShort)
	if err != nil || empty || why != "" {
		t.Fatalf("stepTranscript после укорачивания аудио: empty=%v why=%q err=%v", empty, why, err)
	}
	if sttCalls != 1 || gotNew != newText {
		t.Fatalf("ожидалась повторная расшифровка (sttCalls=1), получено sttCalls=%d got=%q", sttCalls, gotNew)
	}
	if bcShort.AudioDurationSec > 120 {
		t.Fatalf("AudioDurationSec должен обновиться до ~70с, получено %v", bcShort.AudioDurationSec)
	}

	// Сценарий 3: уменьшение размера аудио > 20% при неизвестной длительности (Probe не читает фейк) -> инвалидация
	fakeDir := t.TempDir()
	fakeAudio := archive.AudioFile(fakeDir)
	fakeTr := archive.TranscriptFile(fakeDir)
	if err := os.WriteFile(fakeAudio, []byte(strings.Repeat("b", 4000)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakeTr, []byte(oldText), 0644); err != nil {
		t.Fatal(err)
	}
	bcSizeDrop := &buildCache{AudioSize: 10000}
	gotDrop, empty, why, err := stepTranscript(context.Background(), cfg, fakeDir, fakeAudio, fakeTr, bcSizeDrop)
	if err != nil || empty || why != "" {
		t.Fatalf("stepTranscript после падения размера: empty=%v why=%q err=%v", empty, why, err)
	}
	if sttCalls != 2 || gotDrop != newText {
		t.Fatalf("ожидалась повторная расшифровка при падении размера (sttCalls=2), получено sttCalls=%d", sttCalls)
	}
	assertNoTempFiles(t, dir)
	assertNoTempFiles(t, fakeDir)
}

// TestBuildAtomicWritesLeaveNoTempFiles проверяет, что полная сборка со слайдами и аудио
// записывает все артефакты (включая .slides_desc.txt) без оставшихся .tmp файлов.
func TestBuildAtomicWritesLeaveNoTempFiles(t *testing.T) {
	bin := chromeForPDF(t)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/asr"):
			_, _ = w.Write([]byte(`{"text":"лекция по распределённым системам"}`))
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			// Подходит и для vision, и для LLM-суммаризации
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"## Распределённые системы\nКонсенсус Raft и Paxos."}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	oldURL := llm.OpenCodeURL
	llm.OpenCodeURL = api.URL
	t.Cleanup(func() { llm.OpenCodeURL = oldURL })

	root := t.TempDir()
	p := model.LecturePack{Discipline: "Распределённые системы", Number: 4, Dir: "pack-atomic"}
	dir := filepath.Join(root, p.Dir)
	slidesDir := archive.SlidesDir(dir)
	if err := os.MkdirAll(slidesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive.AudioFile(dir), []byte(strings.Repeat("a", 8192)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slidesDir, "01.png"), []byte(strings.Repeat("png-slide-bytes-", 4)), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		FishStudioAPIKey: "test-key", FishStudioAPIURL: api.URL,
		GrokAPIKey: "test-key", GrokAPIURL: api.URL,
		OpenCodeAPIKey: "test-key",
		ChromeBin:      bin,
	}
	if err := Build(context.Background(), cfg, root, p); err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, f := range []string{
		archive.TranscriptFile(dir),
		slidesDescPath(dir),
		archive.NotesMD(dir),
		archive.NotesPDF(dir),
		buildCachePath(dir),
	} {
		st, err := os.Stat(f)
		if err != nil || st.Size() == 0 {
			t.Fatalf("артефакт %s отсутствует или пуст: err=%v", f, err)
		}
	}
	assertNoTempFiles(t, dir)
}

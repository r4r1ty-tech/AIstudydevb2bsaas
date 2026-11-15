package bbb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
)

// TestDDPCollectionSlideMergeAndFirstShownAt проверяет мерж полей DDP added/changed
// для коллекции slides: частичный changed не затирает ранее известные поля
// и фиксирует время первого показа слайда, не перезаписывая его при повторном показе.
func TestDDPCollectionSlideMergeAndFirstShownAt(t *testing.T) {
	s := &directSession{
		pres:      make(map[string]presMeta),
		slides:    make(map[string]slideMeta),
		allPres:   make(map[string]presMeta),
		allSlides: make(map[string]slideMeta),
		startedAt: time.Now().Add(-10 * time.Second),
	}

	var dummyVoice string

	// 1. Добавляем активную презентацию
	s.handleDDPCollection("added", map[string]any{
		"collection": "presentations",
		"id":         "p1",
		"fields": map[string]any{
			"id":      "pres-1",
			"name":    "lecture.pdf",
			"current": true,
		},
	}, "user1", &dummyVoice)

	// 2. Добавляем слайд 1 (сразу текущий)
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s1",
		"fields": map[string]any{
			"id":             "s1",
			"presentationId": "pres-1",
			"num":            1.0,
			"imageUri":       "/slides/1.png",
			"txtUri":         "/slides/1.txt",
			"content":        "Введение в курс",
			"current":        true,
		},
	}, "user1", &dummyVoice)

	s.presMu.Lock()
	sl1, ok1 := s.allSlides["s1"]
	s.presMu.Unlock()
	if !ok1 {
		t.Fatalf("слайд s1 не найден в allSlides")
	}
	if sl1.FirstShownAt.IsZero() {
		t.Fatalf("у текущего слайда s1 не зафиксирован FirstShownAt")
	}
	firstShownAt1 := sl1.FirstShownAt

	// 3. Добавляем слайд 2 (не текущий)
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s2",
		"fields": map[string]any{
			"id":             "s2",
			"presentationId": "pres-1",
			"num":            2.0,
			"imageUri":       "/slides/2.png",
			"txtUri":         "/slides/2.txt",
			"content":        "Теоретическая часть",
			"current":        false,
		},
	}, "user1", &dummyVoice)

	s.presMu.Lock()
	sl2, ok2 := s.allSlides["s2"]
	s.presMu.Unlock()
	if !ok2 {
		t.Fatalf("слайд s2 не найден в allSlides")
	}
	if !sl2.FirstShownAt.IsZero() {
		t.Fatalf("у неактивного слайда s2 FirstShownAt должен быть пустым")
	}

	// 4. Показ слайда 2: DDP changed присылает ТОЛЬКО {"current": true}
	s.handleDDPCollection("changed", map[string]any{
		"collection": "slides",
		"id":         "s2",
		"fields": map[string]any{
			"current": true,
		},
	}, "user1", &dummyVoice)

	s.presMu.Lock()
	sl2Updated := s.allSlides["s2"]
	s.presMu.Unlock()

	// Проверяем, что остальные поля не затерлись
	if sl2Updated.ImageURI != "/slides/2.png" {
		t.Errorf("imageUri затерся: %q, ожидалось %q", sl2Updated.ImageURI, "/slides/2.png")
	}
	if sl2Updated.TxtURI != "/slides/2.txt" {
		t.Errorf("txtUri затерся: %q, ожидалось %q", sl2Updated.TxtURI, "/slides/2.txt")
	}
	if sl2Updated.Num != 2 {
		t.Errorf("num затерся: %d, ожидалось 2", sl2Updated.Num)
	}
	if sl2Updated.Content != "Теоретическая часть" {
		t.Errorf("content затерся: %q, ожидалось %q", sl2Updated.Content, "Теоретическая часть")
	}
	if sl2Updated.PresentationID != "pres-1" {
		t.Errorf("presentationId затерся: %q, ожидалось %q", sl2Updated.PresentationID, "pres-1")
	}
	if !sl2Updated.Current {
		t.Errorf("current должен быть true")
	}
	if sl2Updated.FirstShownAt.IsZero() {
		t.Fatalf("FirstShownAt для слайда s2 не установился при changed current:true")
	}

	// 5. Переключаем обратно на слайд 1: FirstShownAt слайда 1 не должен перезаписаться
	time.Sleep(10 * time.Millisecond)
	s.handleDDPCollection("changed", map[string]any{
		"collection": "slides",
		"id":         "s1",
		"fields": map[string]any{
			"current": true,
		},
	}, "user1", &dummyVoice)

	s.presMu.Lock()
	sl1AfterReturn := s.allSlides["s1"]
	s.presMu.Unlock()

	if !sl1AfterReturn.FirstShownAt.Equal(firstShownAt1) {
		t.Fatalf("FirstShownAt слайда s1 был перезаписан: было %v, стало %v",
			firstShownAt1, sl1AfterReturn.FirstShownAt)
	}
}

// TestGrabSlidesTextDownloadAndManifest проверяет загрузку текста по TxtURI,
// сохранение контента из DDP и корректную генерацию slides.json.
func TestGrabSlidesTextDownloadAndManifest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/slide1.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})
	mux.HandleFunc("/slide1.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("   Текстовый слой первого слайда из TxtURI\n\n"))
	})
	mux.HandleFunc("/slide2.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})
	mux.HandleFunc("/slide2.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Текст из TxtURI, который не должен перезаписать DDP Content"))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := &directSession{
		client:      srv.Client(),
		baseURL:     srv.URL,
		inRoom:      true,
		pres:        make(map[string]presMeta),
		slides:      make(map[string]slideMeta),
		allPres:     make(map[string]presMeta),
		allSlides:   make(map[string]slideMeta),
		savedSlides: make(map[string]string),
		slidesSub:   make(chan struct{}),
		startedAt:   time.Now().Add(-125 * time.Second),
	}
	close(s.slidesSub)

	var dummyVoice string
	s.handleDDPCollection("added", map[string]any{
		"collection": "presentations",
		"id":         "p1",
		"fields":     map[string]any{"id": "pres-1", "name": "math.pdf", "current": true},
	}, "user1", &dummyVoice)

	// Слайд 1: пустой content -> должен загрузиться по txtUri
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s1",
		"fields": map[string]any{
			"id":             "s1",
			"presentationId": "pres-1",
			"num":            1.0,
			"imageUri":       "/slide1.png",
			"txtUri":         "/slide1.txt",
			"content":        "",
			"current":        true,
		},
	}, "user1", &dummyVoice)

	// Слайд 2: готовый content из DDP -> используется он
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s2",
		"fields": map[string]any{
			"id":             "s2",
			"presentationId": "pres-1",
			"num":            2.0,
			"imageUri":       "/slide2.png",
			"txtUri":         "/slide2.txt",
			"content":        "Формула Эйлера: e^(i*pi) + 1 = 0",
			"current":        false,
		},
	}, "user1", &dummyVoice)

	dir := t.TempDir()
	n, err := s.GrabSlides(ctx, dir)
	if err != nil {
		t.Fatalf("GrabSlides: %v", err)
	}
	if n != 2 {
		t.Fatalf("GrabSlides count = %d, want 2", n)
	}

	// Проверяем файлы картинок
	if _, err := os.Stat(filepath.Join(dir, "01.png")); err != nil {
		t.Fatalf("01.png не существует: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "02.png")); err != nil {
		t.Fatalf("02.png не существует: %v", err)
	}

	// Читаем манифест slides.json
	manifest, err := ReadSlideManifest(dir)
	if err != nil {
		t.Fatalf("ReadSlideManifest: %v", err)
	}
	if len(manifest) != 2 {
		t.Fatalf("manifest length = %d, want 2", len(manifest))
	}

	// Проверяем слайд 1
	m1 := manifest[0]
	if m1.Num != 1 || m1.File != "01.png" || m1.SlideID != "s1" || m1.PresID != "pres-1" {
		t.Errorf("m1 базовые поля некорректны: %+v", m1)
	}
	wantText1 := "Текстовый слой первого слайда из TxtURI"
	if m1.Text != wantText1 {
		t.Errorf("m1 text = %q, want %q", m1.Text, wantText1)
	}
	if m1.FirstShownAt == "" {
		t.Errorf("m1 FirstShownAt пустой")
	}
	if m1.ShownOffsetS < 120 {
		t.Errorf("m1 ShownOffsetS = %d, ожидалось >= 120", m1.ShownOffsetS)
	}
	if !strings.HasPrefix(m1.Timestamp, "[02:") {
		t.Errorf("m1 Timestamp = %q, ожидалось [02:..]", m1.Timestamp)
	}

	// Проверяем слайд 2
	m2 := manifest[1]
	if m2.Num != 2 || m2.File != "02.png" || m2.SlideID != "s2" {
		t.Errorf("m2 базовые поля некорректны: %+v", m2)
	}
	wantText2 := "Формула Эйлера: e^(i*pi) + 1 = 0"
	if m2.Text != wantText2 {
		t.Errorf("m2 text = %q, want %q", m2.Text, wantText2)
	}
	if m2.FirstShownAt != "" {
		t.Errorf("m2 FirstShownAt = %q, ожидалось пусто для не текущего слайда", m2.FirstShownAt)
	}
}

// TestGrabSlidesIncrementalManifestUpdate проверяет обновление slides.json при повторном
// вызове GrabSlides, когда слайд стал текущим позже.
func TestGrabSlidesIncrementalManifestUpdate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/s1.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})
	mux.HandleFunc("/s2.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := &directSession{
		client:      srv.Client(),
		baseURL:     srv.URL,
		inRoom:      true,
		pres:        make(map[string]presMeta),
		slides:      make(map[string]slideMeta),
		allPres:     make(map[string]presMeta),
		allSlides:   make(map[string]slideMeta),
		savedSlides: make(map[string]string),
		slidesSub:   make(chan struct{}),
		startedAt:   time.Now().Add(-60 * time.Second),
	}
	close(s.slidesSub)

	var dummyVoice string
	s.handleDDPCollection("added", map[string]any{
		"collection": "presentations",
		"id":         "p1",
		"fields":     map[string]any{"id": "pres-1", "name": "deck.pdf", "current": true},
	}, "user1", &dummyVoice)

	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s1",
		"fields": map[string]any{
			"id":             "s1",
			"presentationId": "pres-1",
			"num":            1.0,
			"imageUri":       "/s1.png",
			"content":        "Слайд один",
			"current":        true,
		},
	}, "user1", &dummyVoice)

	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s2",
		"fields": map[string]any{
			"id":             "s2",
			"presentationId": "pres-1",
			"num":            2.0,
			"imageUri":       "/s2.png",
			"content":        "Слайд два",
			"current":        false,
		},
	}, "user1", &dummyVoice)

	dir := t.TempDir()

	// Первый захват: оба файла скачаны, но s2 еще не показывался
	n1, err := s.GrabSlides(ctx, dir)
	if err != nil || n1 != 2 {
		t.Fatalf("первый GrabSlides: n=%d err=%v", n1, err)
	}

	f1Stat, err := os.Stat(filepath.Join(dir, "01.png"))
	if err != nil {
		t.Fatalf("01.png отсутствует: %v", err)
	}

	mf1, err := ReadSlideManifest(dir)
	if err != nil || len(mf1) != 2 {
		t.Fatalf("ReadSlideManifest 1: len=%d err=%v", len(mf1), err)
	}
	if mf1[0].FirstShownAt == "" {
		t.Errorf("у s1 FirstShownAt должен быть заполнен")
	}
	if mf1[1].FirstShownAt != "" {
		t.Errorf("у s2 FirstShownAt должен быть пуст")
	}

	// Эмулируем переключение лектора на слайд 2
	s.handleDDPCollection("changed", map[string]any{
		"collection": "slides",
		"id":         "s2",
		"fields": map[string]any{
			"current": true,
		},
	}, "user1", &dummyVoice)

	// Второй захват: новых картинок нет, но манифест должен обновиться!
	n2, err := s.GrabSlides(ctx, dir)
	if err != nil || n2 != 2 {
		t.Fatalf("второй GrabSlides: n=%d err=%v", n2, err)
	}

	// 01.png не должен перезаписываться
	f1After, _ := os.Stat(filepath.Join(dir, "01.png"))
	if f1After.ModTime() != f1Stat.ModTime() {
		t.Errorf("01.png был перезаписан при повторном захвате")
	}

	mf2, err := ReadSlideManifest(dir)
	if err != nil || len(mf2) != 2 {
		t.Fatalf("ReadSlideManifest 2: len=%d err=%v", len(mf2), err)
	}

	// У слайда 1 время не изменилось
	if mf2[0].FirstShownAt != mf1[0].FirstShownAt {
		t.Errorf("у s1 изменилось время первого показа: %q != %q",
			mf2[0].FirstShownAt, mf1[0].FirstShownAt)
	}

	// У слайда 2 появилось время первого показа
	if mf2[1].FirstShownAt == "" {
		t.Errorf("у s2 FirstShownAt должен быть заполнен после обновления")
	}
	if mf2[1].ShownOffsetS < 50 {
		t.Errorf("у s2 ShownOffsetS = %d, ожидалось >= 50", mf2[1].ShownOffsetS)
	}
	if !strings.HasPrefix(mf2[1].Timestamp, "[01:") {
		t.Errorf("у s2 Timestamp = %q, ожидалось [01:..]", mf2[1].Timestamp)
	}
	if mf2[1].Text != "Слайд два" {
		t.Errorf("у s2 Text = %q, ожидалось 'Слайд два'", mf2[1].Text)
	}
}

// TestDDPPresentationSwitchActivatesSlide проверяет, что слайды неактивной презентации
// не помечаются временем первого показа, пока презентация не станет активной.
func TestDDPPresentationSwitchActivatesSlide(t *testing.T) {
	s := &directSession{
		pres:      make(map[string]presMeta),
		slides:    make(map[string]slideMeta),
		allPres:   make(map[string]presMeta),
		allSlides: make(map[string]slideMeta),
		startedAt: time.Now().Add(-5 * time.Second),
	}
	var dummyVoice string

	// Презентация 1 активна
	s.handleDDPCollection("added", map[string]any{
		"collection": "presentations",
		"id":         "p1",
		"fields":     map[string]any{"id": "pres-1", "name": "p1.pdf", "current": true},
	}, "user1", &dummyVoice)
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s1",
		"fields": map[string]any{
			"id":             "s1",
			"presentationId": "pres-1",
			"num":            1.0,
			"imageUri":       "/p1/1.png",
			"current":        true,
		},
	}, "user1", &dummyVoice)

	// Добавляется презентация 2 (не активна), но у ее слайда current=true внутри презентации
	s.handleDDPCollection("added", map[string]any{
		"collection": "presentations",
		"id":         "p2",
		"fields":     map[string]any{"id": "pres-2", "name": "p2.pdf", "current": false},
	}, "user1", &dummyVoice)
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s2",
		"fields": map[string]any{
			"id":             "s2",
			"presentationId": "pres-2",
			"num":            1.0,
			"imageUri":       "/p2/1.png",
			"current":        true,
		},
	}, "user1", &dummyVoice)

	s.presMu.Lock()
	sl1 := s.allSlides["s1"]
	sl2 := s.allSlides["s2"]
	s.presMu.Unlock()

	if sl1.FirstShownAt.IsZero() {
		t.Errorf("sl1 должен быть показан сразу")
	}
	if !sl2.FirstShownAt.IsZero() {
		t.Errorf("sl2 не должен быть показан, пока презентация pres-2 не активна")
	}

	// Лектор переключает презентацию на pres-2
	s.handleDDPCollection("changed", map[string]any{
		"collection": "presentations",
		"id":         "p1",
		"fields":     map[string]any{"current": false},
	}, "user1", &dummyVoice)
	s.handleDDPCollection("changed", map[string]any{
		"collection": "presentations",
		"id":         "p2",
		"fields":     map[string]any{"current": true},
	}, "user1", &dummyVoice)

	s.presMu.Lock()
	sl2After := s.allSlides["s2"]
	s.presMu.Unlock()

	if sl2After.FirstShownAt.IsZero() {
		t.Errorf("sl2 должен был получить FirstShownAt после активации pres-2")
	}
}

// TestFetchSlideTextLimitsAndContentType проверяет фильтрацию небезопасного Content-Type
// и ограничение размера текстового слоя слайда до 128 KiB.
func TestFetchSlideTextLimitsAndContentType(t *testing.T) {
	hugeText := strings.Repeat("A", 256*1024)
	mux := http.NewServeMux()
	mux.HandleFunc("/html.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	})
	mux.HandleFunc("/img.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})
	mux.HandleFunc("/bin.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0x03})
	})
	mux.HandleFunc("/pdf.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4"))
	})
	mux.HandleFunc("/huge.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(hugeText))
	})
	mux.HandleFunc("/ok.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("  Чистый текст слайда  \n"))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := &directSession{
		client:  srv.Client(),
		baseURL: srv.URL,
	}
	ctx := context.Background()

	for _, uri := range []string{"/html.txt", "/img.txt", "/bin.txt", "/pdf.txt"} {
		if got := s.fetchSlideText(ctx, uri); got != "" {
			t.Errorf("fetchSlideText(%s) = %q, ожидалась пустая строка", uri, got)
		}
	}

	gotHuge := s.fetchSlideText(ctx, "/huge.txt")
	if len(gotHuge) != maxSlideTextBytes {
		t.Errorf("fetchSlideText(/huge.txt) len = %d, ожидалось %d", len(gotHuge), maxSlideTextBytes)
	}

	if gotOK := s.fetchSlideText(ctx, "/ok.txt"); gotOK != "Чистый текст слайда" {
		t.Errorf("fetchSlideText(/ok.txt) = %q, ожидалось %q", gotOK, "Чистый текст слайда")
	}
}

// TestGrabSlidesUnsafeFilenamesIgnored проверяет фильтрацию небезопасных имён файлов
// (path traversal и абсолютные пути) при чтении slides.json и .slides_meta.json.
func TestGrabSlidesUnsafeFilenamesIgnored(t *testing.T) {
	mux := http.NewServeMux()
	for _, path := range []string{"/s1.png", "/s2.png", "/s3.png"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNG)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	parent := t.TempDir()
	slidesDir := filepath.Join(parent, "slides")
	if err := os.MkdirAll(slidesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Файл вне каталога слайдов, на который пытается сослаться ../evil.png.
	if err := os.WriteFile(filepath.Join(parent, "evil.png"), []byte("outside"), 0o644); err != nil {
		t.Fatalf("WriteFile evil.png: %v", err)
	}

	badManifest := []SlideManifestItem{
		{Num: 1, File: "../evil.png", SlideID: "s1", PresID: "pres-1", ImageURI: "/s1.png"},
		{Num: 2, File: "/etc/passwd", SlideID: "s2", PresID: "pres-1", ImageURI: "/s2.png"},
		{Num: 3, File: "", SlideID: "s3", PresID: "pres-1", ImageURI: "/s3.png"},
	}
	if err := writeSlideManifestAtomic(filepath.Join(slidesDir, "slides.json"), badManifest); err != nil {
		t.Fatalf("writeSlideManifestAtomic: %v", err)
	}
	badMeta := map[string]string{
		"pres-1/s1": "../evil.png",
		"pres-1/s2": "/etc/passwd",
		"pres-1/s3": "../another.png",
	}
	metaBytes, _ := json.Marshal(badMeta)
	if err := os.WriteFile(filepath.Join(slidesDir, ".slides_meta.json"), metaBytes, 0o644); err != nil {
		t.Fatalf("WriteFile .slides_meta.json: %v", err)
	}

	s := &directSession{
		client:      srv.Client(),
		baseURL:     srv.URL,
		inRoom:      true,
		pres:        make(map[string]presMeta),
		slides:      make(map[string]slideMeta),
		allPres:     make(map[string]presMeta),
		allSlides:   make(map[string]slideMeta),
		savedSlides: make(map[string]string),
		slidesSub:   make(chan struct{}),
	}
	close(s.slidesSub)

	var dummyVoice string
	s.handleDDPCollection("added", map[string]any{
		"collection": "presentations",
		"id":         "p1",
		"fields":     map[string]any{"id": "pres-1", "name": "deck.pdf", "current": true},
	}, "user1", &dummyVoice)
	for i, sid := range []string{"s1", "s2", "s3"} {
		s.handleDDPCollection("added", map[string]any{
			"collection": "slides",
			"id":         sid,
			"fields": map[string]any{
				"id":             sid,
				"presentationId": "pres-1",
				"num":            float64(i + 1),
				"imageUri":       "/" + sid + ".png",
				"current":        i == 0,
			},
		}, "user1", &dummyVoice)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n, err := s.GrabSlides(ctx, slidesDir)
	if err != nil {
		t.Fatalf("GrabSlides: %v", err)
	}
	if n != 3 {
		t.Fatalf("GrabSlides count = %d, want 3", n)
	}

	manifest, err := ReadSlideManifest(slidesDir)
	if err != nil {
		t.Fatalf("ReadSlideManifest: %v", err)
	}
	if len(manifest) != 3 {
		t.Fatalf("manifest len = %d, want 3", len(manifest))
	}
	for _, it := range manifest {
		if !archive.IsSafeSlideFilename(it.File) {
			t.Errorf("небезопасное имя файла в итоговом slides.json: %q", it.File)
		}
		if _, err := os.Stat(filepath.Join(slidesDir, it.File)); err != nil {
			t.Errorf("ожидался локальный файл %s: %v", it.File, err)
		}
	}

	rawMeta, err := os.ReadFile(filepath.Join(slidesDir, ".slides_meta.json"))
	if err != nil {
		t.Fatalf("ReadFile .slides_meta.json: %v", err)
	}
	var savedMeta map[string]string
	if err := json.Unmarshal(rawMeta, &savedMeta); err != nil {
		t.Fatalf("Unmarshal .slides_meta.json: %v", err)
	}
	for k, v := range savedMeta {
		if !archive.IsSafeSlideFilename(v) {
			t.Errorf("небезопасное имя файла в итоговом .slides_meta.json[%q] = %q", k, v)
		}
	}
}

// TestGrabSlidesDoesNotBlockDDP проверяет, что во время медленной загрузки слайдов
// в GrabSlides вызов handleDDPCollection не блокируется на presMu.
func TestGrabSlidesDoesNotBlockDDP(t *testing.T) {
	reqStarted := make(chan struct{})
	releaseReq := make(chan struct{})
	var once sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc("/slow.png", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(reqStarted) })
		select {
		case <-releaseReq:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer func() {
		select {
		case <-releaseReq:
		default:
			close(releaseReq)
		}
	}()

	s := &directSession{
		client:      srv.Client(),
		baseURL:     srv.URL,
		inRoom:      true,
		pres:        make(map[string]presMeta),
		slides:      make(map[string]slideMeta),
		allPres:     make(map[string]presMeta),
		allSlides:   make(map[string]slideMeta),
		savedSlides: make(map[string]string),
		slidesSub:   make(chan struct{}),
	}
	close(s.slidesSub)

	var dummyVoice string
	s.handleDDPCollection("added", map[string]any{
		"collection": "presentations",
		"id":         "p1",
		"fields":     map[string]any{"id": "pres-1", "name": "deck.pdf", "current": true},
	}, "user1", &dummyVoice)
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "doc-1",
		"fields": map[string]any{
			"id":             "s1",
			"presentationId": "pres-1",
			"num":            1.0,
			"imageUri":       "/slow.png",
			"current":        true,
		},
	}, "user1", &dummyVoice)

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type grabRes struct {
		n   int
		err error
	}
	grabDone := make(chan grabRes, 1)
	go func() {
		n, err := s.GrabSlides(ctx, dir)
		grabDone <- grabRes{n: n, err: err}
	}()

	select {
	case <-reqStarted:
	case <-time.After(3 * time.Second):
		t.Fatalf("GrabSlides не начал HTTP-запрос за 3с")
	}

	ddpDone := make(chan struct{})
	go func() {
		var v string
		s.handleDDPCollection("added", map[string]any{
			"collection": "slides",
			"id":         "doc-2",
			"fields": map[string]any{
				"id":             "s2",
				"presentationId": "pres-1",
				"num":            2.0,
				"imageUri":       "/slow.png",
				"current":        false,
			},
		}, "user1", &v)
		close(ddpDone)
	}()

	select {
	case <-ddpDone:
	case <-time.After(300 * time.Millisecond):
		t.Fatalf("handleDDPCollection заблокирован во время HTTP-запроса в GrabSlides")
	}

	close(releaseReq)
	select {
	case res := <-grabDone:
		if res.err != nil || res.n != 1 {
			t.Fatalf("GrabSlides завершился с n=%d err=%v, want n=1 err=nil", res.n, res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("GrabSlides не завершился после разблокировки сервера")
	}
}

// TestGrabSlidesDocIDKeyConsistency проверяет, что при отличии DDP docID от fields["id"]
// обновление текстового слоя обновляет запись по docID и не плодит дубликатов в allSlides.
func TestGrabSlidesDocIDKeyConsistency(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/s1.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})
	mux.HandleFunc("/s1.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Текст по txtUri"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := &directSession{
		client:      srv.Client(),
		baseURL:     srv.URL,
		inRoom:      true,
		pres:        make(map[string]presMeta),
		slides:      make(map[string]slideMeta),
		allPres:     make(map[string]presMeta),
		allSlides:   make(map[string]slideMeta),
		savedSlides: make(map[string]string),
		slidesSub:   make(chan struct{}),
	}
	close(s.slidesSub)

	var dummyVoice string
	s.handleDDPCollection("added", map[string]any{
		"collection": "presentations",
		"id":         "p1",
		"fields":     map[string]any{"id": "pres-1", "name": "deck.pdf", "current": true},
	}, "user1", &dummyVoice)
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "meteor-doc-1",
		"fields": map[string]any{
			"id":             "slide-internal-1",
			"presentationId": "pres-1",
			"num":            1.0,
			"imageUri":       "/s1.png",
			"txtUri":         "/s1.txt",
			"current":        true,
		},
	}, "user1", &dummyVoice)

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n, err := s.GrabSlides(ctx, dir)
	if err != nil || n != 1 {
		t.Fatalf("GrabSlides: n=%d err=%v", n, err)
	}

	s.presMu.Lock()
	total := len(s.allSlides)
	slDoc, hasDoc := s.allSlides["meteor-doc-1"]
	_, hasInternal := s.allSlides["slide-internal-1"]
	s.presMu.Unlock()

	if total != 1 {
		t.Fatalf("len(s.allSlides) = %d, want 1", total)
	}
	if !hasDoc || hasInternal {
		t.Fatalf("несогласованные ключи allSlides: hasDoc=%v hasInternal=%v", hasDoc, hasInternal)
	}
	if slDoc.Content != "Текст по txtUri" {
		t.Errorf("slDoc.Content = %q, want %q", slDoc.Content, "Текст по txtUri")
	}
}

// TestGrabSlidesConcurrentSerialized проверяет, что параллельные вызовы GrabSlides
// сериализуются через slidesGrabMu и не скачивают один и тот же слайд дважды.
func TestGrabSlidesConcurrentSerialized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/s1.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := &directSession{
		client:      srv.Client(),
		baseURL:     srv.URL,
		inRoom:      true,
		pres:        make(map[string]presMeta),
		slides:      make(map[string]slideMeta),
		allPres:     make(map[string]presMeta),
		allSlides:   make(map[string]slideMeta),
		savedSlides: make(map[string]string),
		slidesSub:   make(chan struct{}),
	}
	close(s.slidesSub)

	var dummyVoice string
	s.handleDDPCollection("added", map[string]any{
		"collection": "presentations",
		"id":         "p1",
		"fields":     map[string]any{"id": "pres-1", "name": "deck.pdf", "current": true},
	}, "user1", &dummyVoice)
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s1",
		"fields": map[string]any{
			"id":             "s1",
			"presentationId": "pres-1",
			"num":            1.0,
			"imageUri":       "/s1.png",
			"current":        true,
		},
	}, "user1", &dummyVoice)

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	counts := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := s.GrabSlides(ctx, dir)
			errs <- err
			counts <- n
		}()
	}
	wg.Wait()
	close(errs)
	close(counts)

	for err := range errs {
		if err != nil {
			t.Fatalf("GrabSlides err: %v", err)
		}
	}
	for n := range counts {
		if n != 1 {
			t.Fatalf("GrabSlides count = %d, want 1", n)
		}
	}
}

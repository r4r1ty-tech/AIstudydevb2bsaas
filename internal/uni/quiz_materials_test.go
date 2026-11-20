package uni

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
)

// makeTestPDF собирает минимальный PDF с одной страницей текста.
func makeTestPDF(text string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, 6)
	obj := func(num int, body string) {
		offsets[num] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] "+
		"/Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>")
	obj(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	stream := fmt.Sprintf("BT /F1 12 Tf 20 100 Td (%s) Tj ET", text)
	obj(5, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	xref := b.Len()
	b.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

func TestExtractFileTextFormats(t *testing.T) {
	pdfText := extractFileText(makeTestPDF("Privet PDF materials"), "application/pdf")
	if !strings.Contains(pdfText, "Privet") {
		t.Fatalf("текст PDF не извлечён: %q", pdfText)
	}
	htmlText := extractFileText([]byte("<html><body><p>HTML текст лекции</p></body></html>"), "text/html")
	if !strings.Contains(htmlText, "HTML текст лекции") {
		t.Fatalf("текст HTML не извлечён: %q", htmlText)
	}
	plain := extractFileText([]byte("обычный текст без разметки"), "text/plain")
	if !strings.Contains(plain, "обычный текст") {
		t.Fatalf("обычный текст не прочитан: %q", plain)
	}
	if got := extractFileText([]byte{0, 1, 2, 3, 255}, "application/octet-stream"); got != "" {
		t.Fatalf("двоичный файл дал текст: %q", got)
	}
	if got := extractFileText([]byte("не pdf"), "application/pdf"); got != "" {
		t.Fatalf("битый PDF дал текст: %q", got)
	}
}

func TestCollectMoodleCourseTextCollapsesAndReadsFiles(t *testing.T) {
	pdfData := makeTestPDF("Folder lecture text")
	resData := makeTestPDF("Resource text")
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/course/view.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body><ul>
<li class="section course-section"><h3 class="sectionname">Введение</h3>
<div class="content">                                          Предел
          последовательности
          функции</div></li>
<li class="activity modtype_folder"><a class="activityname" href="%s/mod/folder/view.php?id=10">Папка лекций</a></li>
<li class="activity modtype_resource"><a class="activityname" href="%s/pluginfile.php/1/res.pdf">Методичка</a></li>
</ul></body></html>`, srv.URL, srv.URL)
	})
	mux.HandleFunc("/mod/folder/view.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body><ul>
<li><a href="%s/pluginfile.php/1/lecture.pdf">lecture.pdf</a></li>
<li><a href="%s/pluginfile.php/1/summary.html">summary.html</a></li>
<li><a href="/mod/folder/view.php?id=11">вложенная папка</a></li>
</ul></body></html>`, srv.URL, srv.URL)
	})
	mux.HandleFunc("/pluginfile.php/1/lecture.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdfData)
	})
	mux.HandleFunc("/pluginfile.php/1/res.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(resData)
	})
	mux.HandleFunc("/pluginfile.php/1/summary.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><div>     Конспект\n     из папки     </div></body></html>"))
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	cli, err := moodle.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	sy := &Syncer{}
	got := sy.collectMoodleCourseText(context.Background(), quizTag{TG: 1, CMID: 2}, cli, 55)

	for _, want := range []string{
		"Курс, Введение",
		"Предел последовательности функции",
		"Папка Moodle: Папка лекций / lecture.pdf",
		"Folder lecture text",
		"Папка Moodle: Папка лекций / summary.html",
		"Конспект из папки",
		"Файл Moodle: Методичка",
		"Resource text",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("в материалах нет %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("в материалах остались отступы:\n%s", got)
	}
	if n := utf8Len(got); n > 8000 {
		t.Fatalf("материалы Moodle раздулись до %d символов", n)
	}
}

func TestCollectMoodleCourseTextSurvivesFileErrors(t *testing.T) {
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/course/view.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body><ul>
<li class="section course-section"><h3 class="sectionname">Тема</h3><div>Короткий текст темы</div></li>
<li class="activity modtype_folder"><a class="activityname" href="%s/mod/folder/view.php?id=10">Битая папка</a></li>
<li class="activity modtype_resource"><a class="activityname" href="%s/pluginfile.php/1/broken.pdf">Битый файл</a></li>
</ul></body></html>`, srv.URL, srv.URL)
	})
	mux.HandleFunc("/mod/folder/view.php", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("/pluginfile.php/1/broken.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("not a pdf at all"))
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	cli, err := moodle.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	sy := &Syncer{}
	got := sy.collectMoodleCourseText(context.Background(), quizTag{TG: 1, CMID: 2}, cli, 55)
	if !strings.Contains(got, "Короткий текст темы") {
		t.Fatalf("материалы секции потерялись при ошибках файлов: %q", got)
	}
	if strings.Contains(got, "not a pdf") {
		t.Fatalf("битый PDF попал в материалы: %q", got)
	}
}

func TestCollectCourseMaterialsSlidesFallback(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatalf("setupStore: %v", err)
	}
	recDir := t.TempDir()
	relDir := filepath.Join("Матан", "лекция-2")
	fullDir := filepath.Join(recDir, relDir)
	if err := os.MkdirAll(filepath.Join(fullDir, "slides"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `[
		{"num": 2, "text": "     Второй   слайд\n\n с формулой   "},
		{"num": 1, "text": "Первый слайд"},
		{"num": 3, "text": ""}
	]`
	if err := os.WriteFile(filepath.Join(fullDir, "slides", "slides.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	pack, err := st.EnsurePack(model.Lesson{ID: 2, Discipline: "Матан", Date: "2026-11-15"}, "", relDir)
	if err != nil {
		t.Fatal(err)
	}
	pack.Dir = relDir
	pack.Status = model.PackDone
	if err := st.SavePack(pack); err != nil {
		t.Fatal(err)
	}

	sy := &Syncer{Store: st, Cfg: &config.Config{RecordingsDir: recDir}}
	got := sy.collectCourseMaterials(quizTag{TG: 1, CMID: 2}, "Матан (1 курс)")
	wantLabel := fmt.Sprintf("Слайды лекции Матан №%d", pack.Number)
	if !strings.Contains(got, wantLabel) {
		t.Fatalf("слайды не попали в материалы: %q", got)
	}
	first := strings.Index(got, "Первый слайд")
	second := strings.Index(got, "Второй слайд")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("слайды не по порядку num: %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("пробелы в слайдах не схлопнуты: %q", got)
	}
	if n := utf8Len(got); n > 300 {
		t.Fatalf("неожиданно длинные материалы из слайдов: %d", n)
	}
}

func TestCollectCourseMaterialsSlidesRespectLimit(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatalf("setupStore: %v", err)
	}
	recDir := t.TempDir()
	relDir := filepath.Join("Матан", "лекция-3")
	fullDir := filepath.Join(recDir, relDir)
	if err := os.MkdirAll(filepath.Join(fullDir, "slides"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `[{"num": 1, "text": "` + strings.Repeat("С", 20000) + `"}]`
	if err := os.WriteFile(filepath.Join(fullDir, "slides", "slides.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	pack, err := st.EnsurePack(model.Lesson{ID: 3, Discipline: "Матан", Date: "2026-11-16"}, "", relDir)
	if err != nil {
		t.Fatal(err)
	}
	pack.Dir = relDir
	pack.Status = model.PackDone
	if err := st.SavePack(pack); err != nil {
		t.Fatal(err)
	}

	sy := &Syncer{Store: st, Cfg: &config.Config{RecordingsDir: recDir}}
	got := sy.collectCourseMaterials(quizTag{TG: 1, CMID: 2}, "Матан (1 курс)")
	if n := utf8Len(got); n > materialsFileMaxChars+200 {
		t.Fatalf("слайды не обрезаны: %d символов", n)
	}
	wantLabel := fmt.Sprintf("Слайды лекции Матан №%d", pack.Number)
	if !strings.Contains(got, wantLabel) {
		t.Fatalf("нет подписи слайдов %q: %q", wantLabel, got)
	}
}

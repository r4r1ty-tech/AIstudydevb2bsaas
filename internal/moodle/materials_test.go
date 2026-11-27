package moodle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return string(b)
}

func fixtureHTML(t *testing.T, name, base string) string {
	t.Helper()
	return strings.ReplaceAll(readFixture(t, name), "BASE", strings.TrimRight(base, "/"))
}

func fixtureDoc(t *testing.T, name, base string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(fixtureHTML(t, name, base)))
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return doc
}

func findModule(t *testing.T, mods []CourseModule, cmid int64) CourseModule {
	t.Helper()
	for _, m := range mods {
		if m.CMID == cmid {
			return m
		}
	}
	t.Fatalf("модуль cmid=%d не найден среди %+v", cmid, mods)
	return CourseModule{}
}

func TestParseCourseOutlineFull(t *testing.T) {
	const base = "https://moodle.example.edu"
	course := Course{ID: 500, FullName: "Пример курса"}
	doc := fixtureDoc(t, "course_full.html", base)
	o := ParseCourseOutline(doc, course)

	if o.CourseID != 500 || o.Course != "Пример курса" {
		t.Fatalf("курс: %+v", o)
	}
	if len(o.Sections) != 4 {
		t.Fatalf("секций = %d, ожидали 4", len(o.Sections))
	}

	s0 := o.Sections[0]
	if s0.Num != 0 || s0.Name != "Общее" || !strings.Contains(s0.Summary, "Новости курса") {
		t.Errorf("секция 0: %+v", s0)
	}
	if len(s0.Modules) != 2 {
		t.Fatalf("модулей в секции 0 = %d: %+v", len(s0.Modules), s0.Modules)
	}
	forum := findModule(t, s0.Modules, 101)
	if forum.Kind != "forum" || forum.Name != "Форум «Вопросы»" || forum.Section != "Общее" || forum.SectionNum != 0 {
		t.Errorf("форум: %+v", forum)
	}
	if !strings.HasPrefix(forum.URL, base+"/mod/forum/view.php?id=101") {
		t.Errorf("URL форума: %q", forum.URL)
	}
	page := findModule(t, s0.Modules, 102)
	if page.Kind != "page" || page.Name != "Как оформить отчёт" {
		t.Errorf("страница: %+v", page)
	}
	if !strings.Contains(page.Description, "титульному листу") {
		t.Errorf("описание страницы: %q", page.Description)
	}
	if len(page.Files) != 1 || !strings.HasSuffix(page.Files[0], "/mod_page/content/0/format.pdf") {
		t.Errorf("файлы страницы: %v", page.Files)
	}

	s1 := o.Sections[1]
	if s1.Num != 1 || s1.Name != "1. Введение" || !strings.Contains(s1.Summary, "Первая тема") {
		t.Errorf("секция 1: %+v", s1)
	}
	if len(s1.Modules) != 8 {
		t.Fatalf("модулей в секции 1 = %d: %+v", len(s1.Modules), s1.Modules)
	}
	assign := findModule(t, s1.Modules, 201)
	if assign.Kind != model.MoodleAssign || assign.Name != "Лабораторная работа № 1" {
		t.Errorf("задание: %+v", assign)
	}
	if assign.Available || !strings.Contains(assign.AvailabilityNote, "Недоступно") {
		t.Errorf("ограничение задания: available=%t note=%q", assign.Available, assign.AvailabilityNote)
	}
	quiz := findModule(t, s1.Modules, 202)
	if quiz.Kind != model.MoodleQuiz || !quiz.Available || quiz.AvailabilityNote != "" {
		t.Errorf("тест: %+v", quiz)
	}
	lesson := findModule(t, s1.Modules, 204)
	if lesson.Kind != model.MoodleLesson || lesson.Name != "Урок: введение" {
		t.Errorf("урок: %+v", lesson)
	}
	folder := findModule(t, s1.Modules, 205)
	if folder.Kind != "folder" {
		t.Errorf("папка: %+v", folder)
	}
	hidden := findModule(t, s1.Modules, 206)
	if hidden.Kind != "resource" || hidden.Available || hidden.AvailabilityNote != "Скрыто" {
		t.Errorf("скрытый модуль: %+v", hidden)
	}
	if len(hidden.Files) != 1 || !strings.HasSuffix(hidden.Files[0], "/mod_resource/content/0/hidden.pdf") {
		t.Errorf("файлы скрытого модуля: %v", hidden.Files)
	}
	if w := findModule(t, s1.Modules, 207); w.Kind != model.MoodleWorkshop {
		t.Errorf("семинар: %+v", w)
	}
	if b := findModule(t, s1.Modules, 208); b.Kind != "book" {
		t.Errorf("книга: %+v", b)
	}

	s2 := o.Sections[2]
	if s2.Num != 2 || len(s2.Modules) != 0 || !strings.Contains(s2.Summary, "без материалов") {
		t.Errorf("пустая секция: %+v", s2)
	}
	s3 := o.Sections[3]
	if !s3.Hidden || s3.Name != "3. Скрытая секция" || len(s3.Modules) != 1 {
		t.Errorf("скрытая секция: %+v", s3)
	}
	if m := s3.Modules[0]; m.CMID != 301 || m.SectionNum != 3 {
		t.Errorf("модуль скрытой секции: %+v", m)
	}
}

// Старая и новая разметка секций/активностей поддерживаются по отдельности.
func TestParseCourseOutlineMarkupVariants(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`
<ul>
  <li class="section" data-sectionname="Старая">
    <div class="sectionname">Старая</div>
    <div class="summary">Старое summary</div>
    <ul>
      <li class="activity modtype_assign" id="module-1">
        <div class="activityname"><a href="/mod/assign/view.php?id=1"><span class="instancename">Задание<span class="accesshide"> Задание</span></span></a></div>
      </li>
    </ul>
  </li>
  <li class="course-section" data-sectionnum="7" data-sectionname="Новая">
    <div class="summarytext">Новое summary</div>
    <ul>
      <li class="activity modtype_url" data-id="2">
        <div class="activityinstance"><a href="/mod/url/view.php?id=2"><span class="instancename">Ссылка</span></a></div>
      </li>
      <li class="activity" data-id="3" data-modname="forum">
        <div class="activityinstance"><a href="/mod/forum/view.php?id=3"><span class="instancename">Форум</span></a></div>
      </li>
      <li class="activity" id="module-4">
        <div class="activityname"><a href="/mod/quiz/view.php?id=4"><span class="instancename">Тест без modtype</span></a></div>
      </li>
    </ul>
  </li>
</ul>`))
	if err != nil {
		t.Fatal(err)
	}
	o := ParseCourseOutline(doc, Course{ID: 1})
	if len(o.Sections) != 2 {
		t.Fatalf("секций = %d: %+v", len(o.Sections), o.Sections)
	}
	if s := o.Sections[0]; s.Name != "Старая" || !strings.Contains(s.Summary, "Старое summary") || len(s.Modules) != 1 {
		t.Errorf("старая секция: %+v", s)
	}
	if s := o.Sections[1]; s.Num != 7 || s.Name != "Новая" || !strings.Contains(s.Summary, "Новое summary") {
		t.Errorf("новая секция: %+v", s)
	}
	if m := o.Sections[1].Modules[0]; m.CMID != 2 || m.Kind != "url" || m.Name != "Ссылка" {
		t.Errorf("модуль без modtype в id: %+v", m)
	}
	if m := o.Sections[1].Modules[1]; m.CMID != 3 || m.Kind != "forum" || m.Name != "Форум" {
		t.Errorf("модуль с data-modname: %+v", m)
	}
	if m := o.Sections[1].Modules[2]; m.CMID != 4 || m.Kind != model.MoodleQuiz || m.Name != "Тест без modtype" {
		t.Errorf("модуль с типом из пути: %+v", m)
	}
}

// Номер секции не должен браться из data-sectionid (это PK в БД).
func TestSectionNumberIgnoresDBID(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`
<ul>
  <li class="section" id="section-4" data-sectionid="48219" data-sectionname="Четвёртая"></li>
  <li class="course-section" data-number="12" data-sectionid="48220" data-sectionname="Двенадцатая"></li>
  <li class="section" data-sectionid="48221" data-sectionname="Без номера"></li>
</ul>`))
	if err != nil {
		t.Fatal(err)
	}
	o := ParseCourseOutline(doc, Course{ID: 1})
	if len(o.Sections) != 3 {
		t.Fatalf("секций = %d", len(o.Sections))
	}
	if o.Sections[0].Num != 4 {
		t.Errorf("номер из id=section-4: %d", o.Sections[0].Num)
	}
	if o.Sections[1].Num != 12 {
		t.Errorf("номер из data-number: %d", o.Sections[1].Num)
	}
	if o.Sections[2].Num != 2 {
		t.Errorf("fallback-номер: %d", o.Sections[2].Num)
	}
	if o.Sections[0].ID != 48219 {
		t.Errorf("ID секции потерян: %d", o.Sections[0].ID)
	}
}

// У недоступного модуля без <a> в .activityname ссылка из .availabilityinfo
// не должна становиться URL/именем модуля.
func TestUnavailableModuleWithoutLink(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`
<ul>
  <li class="activity modtype_assign dimmed" id="module-9" data-id="9" data-activityname="Закрытое задание">
    <div class="activity-item" data-activityname="Закрытое задание">
      <div class="availabilityinfo isrestricted">Недоступно, пока не выполнено:
        <a href="/mod/assign/view.php?id=999">Лабораторная работа № 0</a></div>
    </div>
  </li>
</ul>`))
	if err != nil {
		t.Fatal(err)
	}
	o := ParseCourseOutline(doc, Course{ID: 1})
	if len(o.Sections) != 1 || len(o.Sections[0].Modules) != 1 {
		t.Fatalf("структура: %+v", o)
	}
	m := o.Sections[0].Modules[0]
	if m.CMID != 9 || m.Name != "Закрытое задание" {
		t.Errorf("модуль: %+v", m)
	}
	if m.URL != "" {
		t.Errorf("URL взят из .availabilityinfo: %q", m.URL)
	}
	if m.Available || !strings.Contains(m.AvailabilityNote, "Недоступно") {
		t.Errorf("доступность: %+v", m)
	}
}

// Parse* не паникуют на nil-документе.
func TestParseNilDoc(t *testing.T) {
	if o := ParseCourseOutline(nil, Course{ID: 7, FullName: "Курс"}); o.CourseID != 7 || o.Course != "Курс" || len(o.Sections) != 0 {
		t.Errorf("outline: %+v", o)
	}
	if p := ParsePage(nil); p.Name != "" || p.Content != "" || len(p.Files) != 0 {
		t.Errorf("page: %+v", p)
	}
	if f := ParseFolder(nil); f != nil {
		t.Errorf("folder: %+v", f)
	}
	if b := ParseBook(nil); b.Name != "" || len(b.Chapters) != 0 {
		t.Errorf("book: %+v", b)
	}
	if u := ParseURL(nil); u.URL != "" {
		t.Errorf("url: %+v", u)
	}
	if l := ParseLesson(nil); l.Name != "" || l.Status != "" {
		t.Errorf("lesson: %+v", l)
	}
}

// CourseActivities по-прежнему отдаёт только задания, тесты, уроки и семинары.
func TestCourseActivitiesStillFiltersKinds(t *testing.T) {
	doc := fixtureDoc(t, "course_full.html", "https://moodle.example.edu")
	acts := parseCourse(doc, Course{ID: 500, FullName: "Пример курса"})
	if len(acts) != 4 {
		t.Fatalf("активностей = %d, ожидали 4: %+v", len(acts), acts)
	}
	want := []string{model.MoodleAssign, model.MoodleQuiz, model.MoodleLesson, model.MoodleWorkshop}
	for i, kind := range want {
		if acts[i].Kind != kind {
			t.Errorf("активность %d: kind=%q, ожидали %q", i, acts[i].Kind, kind)
		}
	}
	for _, a := range acts {
		if a.Kind != model.MoodleAssign && a.Kind != model.MoodleQuiz &&
			a.Kind != model.MoodleLesson && a.Kind != model.MoodleWorkshop {
			t.Errorf("лишний тип в CourseActivities: %+v", a)
		}
	}
	if acts[0].CMID != 201 || acts[0].Name != "Лабораторная работа № 1" {
		t.Errorf("первое задание: %+v", acts[0])
	}
}

func TestParsePage(t *testing.T) {
	const base = "https://moodle.example.edu"
	p := ParsePage(fixtureDoc(t, "mod_page.html", base))
	if p.Name != "Как оформить отчёт" {
		t.Errorf("имя: %q", p.Name)
	}
	if !strings.Contains(p.Intro, "Вводный текст") {
		t.Errorf("введение: %q", p.Intro)
	}
	if !strings.Contains(p.Content, "Титульный лист") || !strings.Contains(p.Content, "шаблону кафедры") {
		t.Errorf("содержимое: %q", p.Content)
	}
	if len(p.Files) != 2 {
		t.Fatalf("файлов = %d: %+v", len(p.Files), p.Files)
	}
	byName := map[string]FolderEntry{}
	for _, f := range p.Files {
		byName[f.Name] = f
	}
	pdf, ok := byName["Методичка (PDF)"]
	if !ok || !strings.HasSuffix(pdf.URL, "/mod_page/content/0/format.pdf") {
		t.Errorf("PDF: %+v (все: %+v)", pdf, p.Files)
	}
	img, ok := byName["Схема оформления"]
	if !ok || !strings.HasSuffix(img.URL, "/mod_page/content/0/scheme.png") {
		t.Errorf("рисунок: %+v", img)
	}
}

func TestParseFolderNested(t *testing.T) {
	const base = "https://moodle.example.edu"
	files := ParseFolder(fixtureDoc(t, "mod_folder.html", base))
	if len(files) != 3 {
		t.Fatalf("файлов = %d: %+v", len(files), files)
	}
	want := []struct {
		name, folder, suffix string
	}{
		{"lecture.pdf", "", "/mod_folder/content/0/lecture.pdf?forcedownload=1"},
		{"lab.pdf", "sub", "/mod_folder/content/0/sub/lab.pdf"},
		{"notes.txt", "sub/deep", "/mod_folder/content/0/sub/deep/notes.txt"},
	}
	for i, w := range want {
		got := files[i]
		if got.Name != w.name || got.Folder != w.folder || !strings.HasSuffix(got.URL, w.suffix) {
			t.Errorf("файл %d: %+v, ожидали name=%q folder=%q suffix=%q", i, got, w.name, w.folder, w.suffix)
		}
	}
}

func TestParseBook(t *testing.T) {
	const base = "https://moodle.example.edu"
	b := ParseBook(fixtureDoc(t, "mod_book.html", base))
	if b.Name != "Учебное пособие" || !strings.Contains(b.Intro, "Краткое введение") {
		t.Errorf("книга: %+v", b)
	}
	if len(b.Chapters) != 3 {
		t.Fatalf("глав = %d: %+v", len(b.Chapters), b.Chapters)
	}
	wantIDs := []int64{11, 12, 13}
	for i, id := range wantIDs {
		if b.Chapters[i].ID != id {
			t.Errorf("глава %d: id=%d, ожидали %d", i, b.Chapters[i].ID, id)
		}
	}
	if b.Chapters[0].Title != "1. Основы" || b.Chapters[0].Number != 1 {
		t.Errorf("первая глава: %+v", b.Chapters[0])
	}
	if b.CurrentChapterID != 11 {
		t.Errorf("текущая глава: %d", b.CurrentChapterID)
	}
	if !strings.Contains(b.Content, "Первый абзац") || !strings.Contains(b.Content, "Второй абзац") {
		t.Errorf("текст главы: %q", b.Content)
	}
	if !strings.Contains(b.Chapters[1].URL, "chapterid=12") {
		t.Errorf("URL главы: %q", b.Chapters[1].URL)
	}
}

func TestParseURL(t *testing.T) {
	u := ParseURL(fixtureDoc(t, "mod_url.html", "https://moodle.example.edu"))
	if u.Name != "Полезная ссылка" || !strings.Contains(u.Intro, "Внешний ресурс") {
		t.Errorf("ссылка: %+v", u)
	}
	if u.URL != "https://example.org/material?id=42" {
		t.Errorf("цель: %q", u.URL)
	}
}

func TestParseLesson(t *testing.T) {
	l := ParseLesson(fixtureDoc(t, "mod_lesson.html", "https://moodle.example.edu"))
	if l.Name != "Урок: введение" || !strings.Contains(l.Intro, "Краткое описание урока") {
		t.Errorf("урок: %+v", l)
	}
	if !strings.Contains(l.Content, "Текст учебной страницы") {
		t.Errorf("содержимое: %q", l.Content)
	}
	if l.Status != model.MoodleDone {
		t.Errorf("статус: %q", l.Status)
	}
	if l.Grade != "8,00 из 10,00" {
		t.Errorf("оценка: %q", l.Grade)
	}
	info := strings.Join(l.Info, " | ")
	for _, want := range []string{"Выполнено страниц: 7 из 7", "Оценка: 8,00 из 10,00", "Затраченное время: 12 мин."} {
		if !strings.Contains(info, want) {
			t.Errorf("info без %q: %s", want, info)
		}
	}
}

// parseDetail для урока: даты, статус, оценка и краткое описание.
func TestParseDetailLesson(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Samara")
	if err != nil {
		t.Fatal(err)
	}
	d := parseDetail(fixtureDoc(t, "mod_lesson.html", "https://moodle.example.edu"), model.MoodleLesson, loc)
	if d.Opens == nil || d.Opens.Format("2006-01-02 15:04") != "2026-09-01 00:00" {
		t.Errorf("открытие: %v", d.Opens)
	}
	if d.Due == nil || d.Due.Format("2006-01-02 15:04") != "2026-12-25 23:59" {
		t.Errorf("закрытие: %v", d.Due)
	}
	if d.Status != model.MoodleDone {
		t.Errorf("статус: %q", d.Status)
	}
	todo := strings.Join(d.Todo, " | ")
	for _, want := range []string{"Выполнено страниц: 7 из 7", "Оценка: 8,00 из 10,00", "Описание: Краткое описание урока"} {
		if !strings.Contains(todo, want) {
			t.Errorf("todo без %q: %s", want, todo)
		}
	}
}

// Урок без оценки и с частичным прогрессом остаётся черновиком.
func TestLessonStatusPartial(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`
<div class="lessoninfo"><p>Выполнено страниц: 3 из 10</p></div>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := lessonStatus(doc); got != model.MoodleDraft {
		t.Errorf("статус = %q, ожидали draft", got)
	}
}

// Оценка «2,50 из 10,00» — не прогресс страниц: урок не становится done.
func TestLessonStatusIgnoresGradeAsProgress(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`
<div class="lessoninfo"><p>2,50 из 10,00</p></div>
<table class="generaltable"><tbody>
  <tr><th>Оценка</th><td>2,50 из 10,00</td></tr>
</tbody></table>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := lessonStatus(doc); got == model.MoodleDone {
		t.Errorf("оценка принята за прогресс: статус = %q", got)
	} else if got != model.MoodleSubmitted {
		t.Errorf("статус = %q, ожидали submitted", got)
	}
}

// Явное «не завершили» важнее числового прогресса.
func TestLessonStatusExplicitNotCompleted(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`
<div class="lessoninfo"><p>Выполнено страниц: 10 из 10</p></div>
<div class="alert alert-info">Вы не завершили урок.</div>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := lessonStatus(doc); got != model.MoodleTodo {
		t.Errorf("статус = %q, ожидали todo", got)
	}
}

type materialsServer struct {
	*httptest.Server
	chapterRequests atomic.Int32
}

func newMaterialsServer(t *testing.T) *materialsServer {
	t.Helper()
	s := &materialsServer{}
	mux := http.NewServeMux()
	base := func() string { return s.Server.URL + "/moodle" }

	mux.HandleFunc("/moodle/course/view.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixtureHTML(t, "course_full.html", base())))
	})
	mux.HandleFunc("/moodle/mod/page/view.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fixtureHTML(t, "mod_page.html", base())))
	})
	mux.HandleFunc("/moodle/mod/folder/view.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fixtureHTML(t, "mod_folder.html", base())))
	})
	mux.HandleFunc("/moodle/mod/lesson/view.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fixtureHTML(t, "mod_lesson.html", base())))
	})
	mux.HandleFunc("/moodle/mod/book/view.php", func(w http.ResponseWriter, r *http.Request) {
		chapter := r.URL.Query().Get("chapterid")
		if chapter == "" {
			_, _ = w.Write([]byte(fixtureHTML(t, "mod_book.html", base())))
			return
		}
		s.chapterRequests.Add(1)
		text := map[string]string{
			"11": "Первый абзац главы.",
			"12": "Текст второй главы.",
			"13": "Текст третьей главы.",
		}[chapter]
		active := func(id string) string {
			if id == chapter {
				return ` class="active" aria-current="page"`
			}
			return ""
		}
		html := `<html><body><h1>Учебное пособие</h1>
<div class="book_contents">
 <nav class="book_toc"><ul>
  <li` + active("11") + `><a href="` + base() + `/mod/book/view.php?id=401&amp;chapterid=11">1. Основы</a></li>
  <li` + active("12") + `><a href="` + base() + `/mod/book/view.php?id=401&amp;chapterid=12">2. Практика</a></li>
  <li` + active("13") + `><a href="` + base() + `/mod/book/view.php?id=401&amp;chapterid=13">3. Итоги</a></li>
 </ul></nav>
 <div class="book_content"><div class="no-overflow"><p>` + text + `</p></div></div>
</div></body></html>`
		_, _ = w.Write([]byte(html))
	})

	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestClientMaterials(t *testing.T) {
	srv := newMaterialsServer(t)
	c, err := New(srv.URL + "/moodle")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	outline, err := c.CourseOutline(ctx, Course{ID: 500, FullName: "Пример курса"})
	if err != nil {
		t.Fatalf("CourseOutline: %v", err)
	}
	if len(outline.Sections) != 4 || len(outline.Sections[1].Modules) != 8 {
		t.Fatalf("структура: %+v", outline)
	}
	assign := findModule(t, outline.Sections[1].Modules, 201)
	if !strings.HasPrefix(assign.URL, srv.URL+"/moodle/mod/assign/view.php") {
		t.Errorf("URL модуля не абсолютный: %q", assign.URL)
	}

	page, err := c.PageContent(ctx, 102)
	if err != nil {
		t.Fatalf("PageContent: %v", err)
	}
	if page.CMID != 102 || !strings.Contains(page.Content, "Титульный лист") || len(page.Files) != 2 {
		t.Errorf("страница: %+v", page)
	}
	if !strings.HasPrefix(page.Files[0].URL, srv.URL+"/moodle/pluginfile.php/") {
		t.Errorf("URL файла страницы: %q", page.Files[0].URL)
	}

	files, err := c.FolderFiles(ctx, 205)
	if err != nil {
		t.Fatalf("FolderFiles: %v", err)
	}
	if len(files) != 3 || files[2].Folder != "sub/deep" {
		t.Errorf("файлы папки: %+v", files)
	}

	book, err := c.BookContent(ctx, 401)
	if err != nil {
		t.Fatalf("BookContent: %v", err)
	}
	if len(book.Chapters) != 3 || book.CurrentChapterID != 11 || !strings.Contains(book.Content, "Первый абзац") {
		t.Errorf("книга: %+v", book)
	}
	if !strings.HasPrefix(book.Chapters[1].URL, srv.URL+"/moodle/mod/book/view.php") {
		t.Errorf("URL главы: %q", book.Chapters[1].URL)
	}

	all, err := c.BookContentAll(ctx, 401, 2)
	if err != nil {
		t.Fatalf("BookContentAll: %v", err)
	}
	if !strings.Contains(all.Chapters[0].Content, "Первый абзац") ||
		!strings.Contains(all.Chapters[1].Content, "Текст второй главы") {
		t.Errorf("обход глав: %+v", all.Chapters)
	}
	if all.Chapters[2].Content != "" {
		t.Errorf("лимит 2 не соблюдён: %+v", all.Chapters[2])
	}
	if !strings.Contains(all.Content, "Первый абзац") || !strings.Contains(all.Content, "Текст второй главы") {
		t.Errorf("общий текст книги: %q", all.Content)
	}
	if n := srv.chapterRequests.Load(); n != 2 {
		t.Errorf("запросов глав = %d, ожидали 2", n)
	}

	lesson, err := c.LessonContent(ctx, 204)
	if err != nil {
		t.Fatalf("LessonContent: %v", err)
	}
	if lesson.CMID != 204 || lesson.Status != model.MoodleDone || lesson.Grade != "8,00 из 10,00" ||
		!strings.Contains(lesson.Content, "Текст учебной страницы") {
		t.Errorf("урок: %+v", lesson)
	}
}

// URLContent не переходит по редиректу и не отправляет куки на внешний сайт.
func TestURLContentDoesNotFollowExternal(t *testing.T) {
	var externalHits atomic.Int32
	var externalCookie atomic.Value
	externalCookie.Store("")
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalHits.Add(1)
		externalCookie.Store(r.Header.Get("Cookie"))
		w.WriteHeader(http.StatusOK)
	}))
	defer ext.Close()

	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/moodle/mod/url/view.php", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("id") {
		case "999":
			http.Redirect(w, r, ext.URL+"/material", http.StatusFound)
		case "998":
			http.Redirect(w, r, srv.URL+"/moodle/login/index.php", http.StatusSeeOther)
		default:
			if ck, err := r.Cookie("MoodleSession"); err != nil || ck.Value != "good" {
				t.Errorf("Moodle-запрос без тестовой куки: %v", err)
			}
			_, _ = w.Write([]byte(fixtureHTML(t, "mod_url.html", srv.URL+"/moodle")))
		}
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(srv.URL + "/moodle")
	if err != nil {
		t.Fatal(err)
	}
	c.setSesskey("SK_SECRET")
	jarURL, err := url.Parse(srv.URL + "/moodle")
	if err != nil {
		t.Fatal(err)
	}
	c.s.Client.Jar.SetCookies(jarURL, []*http.Cookie{{Name: "MoodleSession", Value: "good", Path: "/"}})
	ctx := context.Background()

	u, err := c.URLContent(ctx, 203)
	if err != nil {
		t.Fatalf("URLContent: %v", err)
	}
	if u.URL != "https://example.org/material?id=42" || u.Redirect {
		t.Errorf("ссылка: %+v", u)
	}

	redirected, err := c.URLContent(ctx, 999)
	if err != nil {
		t.Fatalf("URLContent redirect: %v", err)
	}
	if !redirected.Redirect || redirected.URL != ext.URL+"/material" {
		t.Errorf("редирект: %+v", redirected)
	}
	if hits := externalHits.Load(); hits != 0 {
		t.Errorf("внешний сайт получил %d запросов, ожидали 0", hits)
	}
	if ck := externalCookie.Load().(string); ck != "" {
		t.Errorf("на внешний сайт ушли куки: %q", ck)
	}

	if _, err := c.URLContent(ctx, 998); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("редирект на вход: err=%v, ожидали ErrUnauthorized", err)
	}
}

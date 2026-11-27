package mcpapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
)

// materialCounters считает запросы к фейковому Moodle: тесты чтения урока,
// теста и задания проверяют, что инструменты не делают POST и не начинают
// попытки.
type materialCounters struct {
	mu               sync.Mutex
	posts            int
	attempts         int
	courseViews      int
	resourceViews    int
	rejectCourseView int
	failCourses      bool
	fake             *fakeMoodle
}

func (c *materialCounters) note(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.Method == http.MethodPost {
		c.posts++
	}
	path := r.URL.Path
	if path == "/moodle/mod/resource/view.php" {
		c.resourceViews++
	}
	if strings.Contains(path, "startattempt") || strings.Contains(path, "processattempt") ||
		strings.Contains(path, "submit") {
		c.attempts++
	}
}

// rejectNextCourseViewOnce заставляет следующий GET страницы курса уйти на
// форму входа: так проверяется перелогин после moodle.ErrUnauthorized.
func (c *materialCounters) rejectNextCourseViewOnce() {
	c.mu.Lock()
	c.rejectCourseView++
	c.mu.Unlock()
}

func (c *materialCounters) takeCourseViewReject() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rejectCourseView <= 0 {
		return false
	}
	c.rejectCourseView--
	return true
}

// killCookie делает сохранённую куку недействительной, чтобы Session.do после
// ErrUnauthorized действительно входил по паролю, а не восстанавливал куку.
func (c *materialCounters) killCookie() {
	c.fake.mu.Lock()
	c.fake.cookieDead = true
	c.fake.mu.Unlock()
}

func (c *materialCounters) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.posts, c.attempts, c.courseViews, c.resourceViews = 0, 0, 0, 0
}

func (c *materialCounters) noteCourseView() {
	c.mu.Lock()
	c.courseViews++
	c.mu.Unlock()
}

func (c *materialCounters) courseViewCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.courseViews
}

func (c *materialCounters) resourceViewCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resourceViews
}

// setCoursesFailing ломает AJAX курсов: поиск без course_id получает Note
// «курсы не получены» и не должен кэшировать такой ответ.
func (c *materialCounters) setCoursesFailing(fail bool) {
	c.mu.Lock()
	c.failCourses = fail
	c.mu.Unlock()
}

func (c *materialCounters) coursesFailing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failCourses
}

func (c *materialCounters) snapshot() (posts, attempts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.posts, c.attempts
}

// newMaterialTestServer поднимает фейковый Moodle с тремя курсами и всеми
// типами материалов. Логин и список курсов обслуживает существующий fakeMoodle.
func newMaterialTestServer(t *testing.T) (*httptest.Server, *materialCounters) {
	t.Helper()
	fake := newFakeMoodle()
	fake.coursesAfterAuth = []map[string]any{
		{"id": 1515, "fullname": "Теория информации", "shortname": "TI", "hidden": false},
		{"id": 1742, "fullname": "ТФЯГ", "shortname": "TFYAG", "hidden": false},
		{"id": 1807, "fullname": "Python", "shortname": "PY", "hidden": false},
		{"id": 1900, "fullname": "Большой курс", "shortname": "BIG", "hidden": false},
	}
	counters := &materialCounters{fake: fake}
	fallback := fake.handler()
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters.note(r)
		switch r.URL.Path {
		case "/moodle/course/view.php":
			counters.noteCourseView()
			if counters.takeCourseViewReject() {
				counters.killCookie()
				http.Redirect(w, r, "/moodle/login/index.php", http.StatusSeeOther)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(courseOutlinePage(r.URL.Query().Get("id"))))
		case "/moodle/mod/page/view.php":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(pageHTML(r.URL.Query().Get("id"))))
		case "/moodle/mod/lesson/view.php":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(lessonHTML()))
		case "/moodle/mod/resource/view.php":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("Методичка: энтропия и код Хаффмана."))
		case "/moodle/mod/folder/view.php":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(folderHTML()))
		case "/moodle/mod/book/view.php":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(bookHTML(r.URL.Query().Get("chapterid"))))
		case "/moodle/mod/url/view.php":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(urlHTML()))
		case "/moodle/mod/assign/view.php":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(assignHTML()))
		case "/moodle/mod/quiz/view.php":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(quizHTML()))
		case "/moodle/mod/workshop/view.php":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(workshopHTML()))
		case "/moodle/lib/ajax/service.php":
			if counters.coursesFailing() {
				http.Error(w, "ajax down", http.StatusInternalServerError)
				return
			}
			fallback.ServeHTTP(w, r)
		case "/moodle/pluginfile.php/1/mod_folder/content/0/lecture.txt":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("Текст лекции из папки."))
		default:
			fallback.ServeHTTP(w, r)
		}
	})
	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv, counters
}

func courseOutlinePage(id string) string {
	switch id {
	case "1515":
		return `<html><body><h1>Теория информации</h1><ul class="topics">
<li class="section course-section" data-sectionnum="0" data-sectionname="Теория информации">
  <h3 class="sectionname">Теория информации</h3>
  <div class="summarytext">Количество информации и энтропия источника.</div>
  <ul>
    <li class="activity modtype_page" data-id="65001">
      <div class="activityname"><a href="/moodle/mod/page/view.php?id=65001"><span class="instancename">Мера неопределённости</span></a></div>
      <div class="contentafterlink">Понятие из теории связи.</div>
    </li>
  </ul>
</li></ul></body></html>`
	case "1807":
		return `<html><body><h1>Python</h1><ul class="topics">
<li class="section course-section" data-sectionnum="0" data-sectionname="Программирование">
  <h3 class="sectionname">Программирование</h3>
  <div class="summarytext">Введение в программирование.</div>
  <ul>
    <li class="activity modtype_lesson" data-id="60904">
      <div class="activityinstance"><a href="/moodle/mod/lesson/view.php?id=60904"><span class="instancename">Урок Python</span></a></div>
    </li>
    <li class="activity modtype_page" data-id="63991">
      <div class="activityname"><a href="/moodle/mod/page/view.php?id=63991"><span class="instancename">Основы Python</span></a></div>
      <div class="contentafterlink">Введение в синтаксис.</div>
    </li>
  </ul>
</li></ul></body></html>`
	case "1742":
		return `<html><body><h1>ТФЯГ</h1><ul class="topics">
<li class="section course-section" data-sectionnum="0" data-sectionname="Введение">
  <h3 class="sectionname">Введение</h3>
  <div class="summarytext">Теория формальных языков и грамматик.</div>
  <ul>
    <li class="activity modtype_page" data-id="62991">
      <div class="activityname"><a href="/moodle/mod/page/view.php?id=62991"><span class="instancename">Энтропия и информация</span></a></div>
      <div class="contentafterlink">Страница про энтропию и информацию.</div>
    </li>
    <li class="activity modtype_resource" data-id="63209">
      <div class="activityname"><a href="/moodle/mod/resource/view.php?id=63209"><span class="instancename">Методичка</span></a></div>
    </li>
    <li class="activity modtype_book" data-id="64001">
      <div class="activityname"><a href="/moodle/mod/book/view.php?id=64001"><span class="instancename">Учебное пособие</span></a></div>
    </li>
    <li class="activity modtype_folder" data-id="64002">
      <div class="activityname"><a href="/moodle/mod/folder/view.php?id=64002"><span class="instancename">Материалы</span></a></div>
    </li>
    <li class="activity modtype_url" data-id="64003">
      <div class="activityname"><a href="/moodle/mod/url/view.php?id=64003"><span class="instancename">Полезная ссылка</span></a></div>
    </li>
    <li class="activity modtype_assign" data-id="64004">
      <div class="activityinstance"><a href="/moodle/mod/assign/view.php?id=64004"><span class="instancename">Лабораторная работа</span></a></div>
    </li>
    <li class="activity modtype_quiz" data-id="64005">
      <div class="activityinstance"><a href="/moodle/mod/quiz/view.php?id=64005"><span class="instancename">Тест по энтропии</span></a></div>
    </li>
    <li class="activity modtype_workshop" data-id="64006">
      <div class="activityinstance"><a href="/moodle/mod/workshop/view.php?id=64006"><span class="instancename">Семинар</span></a></div>
    </li>
  </ul>
</li></ul></body></html>`
	case "1900":
		var b strings.Builder
		b.WriteString(`<html><body><h1>Большой курс</h1><ul class="topics">
<li class="section course-section" data-sectionnum="0" data-sectionname="Много страниц">
  <h3 class="sectionname">Много страниц</h3>
  <div class="summarytext">Проверка deep-лимита.</div>
  <ul>`)
		for i := 0; i < 61; i++ {
			id := 71000 + i
			fmt.Fprintf(&b, `<li class="activity modtype_page" data-id="%d"><div class="activityname"><a href="/moodle/mod/page/view.php?id=%d"><span class="instancename">Страница %d</span></a></div></li>`, id, id, i+1)
		}
		b.WriteString(`</ul></li></ul></body></html>`)
		return b.String()
	default:
		return `<html><body><h1>Неизвестный курс</h1></body></html>`
	}
}

func pageHTML(id string) string {
	switch id {
	case "62991":
		return `<html><body><h1>Энтропия и информация</h1>
<div class="activity-description"><div class="no-overflow">Вводный текст страницы.</div></div>
<div id="page-content" class="generalbox"><div class="no-overflow">Энтропия — мера неопределённости. Формула Шеннона связывает вероятность и информацию.</div></div>
</body></html>`
	case "63991":
		return `<html><body><h1>Основы Python</h1>
<div id="page-content" class="generalbox"><div class="no-overflow">Python — язык программирования с динамической типизацией.</div></div>
</body></html>`
	case "65001":
		return `<html><body><h1>Мера неопределённости</h1>
<div id="page-content" class="generalbox"><div class="no-overflow">Энтропия — мера неопределённости случайной величины. Чем больше энтропия, тем больше информации несёт сообщение.</div></div>
</body></html>`
	case "69001":
		return `<html><body><h1>Огромная страница</h1>
<div id="page-content" class="generalbox"><div class="no-overflow">` + strings.Repeat("Я", 120000) + `</div></div>
</body></html>`
	default:
		return `<html><body><h1>Страница</h1></body></html>`
	}
}

func lessonHTML() string {
	return `<html><body><h1>Урок Python</h1>
<div class="activity-description"><div class="no-overflow">Краткое описание урока.</div></div>
<div class="lessoninfo"><p>Вы не завершили урок</p></div>
<div class="contents"><div class="no-overflow">Текст учебной страницы: печать приветствия на Python.</div></div>
<div class="generaltable"><table><tr><th>Оценка</th><td>-</td></tr></table></div>
</body></html>`
}

func folderHTML() string {
	return `<html><body><h1>Материалы</h1><ul>
<li><a href="/moodle/pluginfile.php/1/mod_folder/content/0/lecture.txt">lecture.txt</a></li>
<li><a href="/moodle/pluginfile.php/1/mod_folder/content/0/summary.html">summary.html</a></li>
</ul></body></html>`
}

func bookHTML(chapterID string) string {
	content := "Текст первой главы про алфавит и коды."
	next := `<li><a href="/moodle/mod/book/view.php?id=64001&amp;chapterid=2">2. Энтропия</a></li>`
	current := `<li class="active"><a aria-current="page" href="/moodle/mod/book/view.php?id=64001&amp;chapterid=1">1. Введение</a></li>`
	if chapterID == "2" {
		content = "Текст второй главы про энтропию источника."
		current = `<li class="active"><a aria-current="page" href="/moodle/mod/book/view.php?id=64001&amp;chapterid=2">2. Энтропия</a></li>`
		next = `<li><a href="/moodle/mod/book/view.php?id=64001&amp;chapterid=3">3. Коды</a></li>`
	}
	return `<html><body><h1>Учебное пособие</h1>
<div class="book_toc"><ul>` + current + next + `</ul></div>
<div class="book_content"><div class="no-overflow">` + content + `</div></div>
</body></html>`
}

func urlHTML() string {
	return `<html><body><h1>Полезная ссылка</h1>
<div class="activity-description"><div class="no-overflow">Внешний ресурс по теории информации.</div></div>
<div class="urlworkaround"><a href="https://external.example/material">Открыть материал</a></div>
</body></html>`
}

func assignHTML() string {
	return `<html><body><h1>Лабораторная работа</h1>
<div class="activity-description"><div class="no-overflow">Сдать отчёт по энтропии.</div></div>
<div data-region="activity-dates"><div><strong>Срок сдачи</strong> 15 ноября 2026, 15:00</div></div>
<div class="submissionstatustable"><table><tr><td class="submissionstatussubmitted">Отправлено</td></tr></table></div>
</body></html>`
}

func quizHTML() string {
	return `<html><body><h1>Тест по энтропии</h1>
<div class="activity-description"><div class="no-overflow">Проверка знаний по теории информации.</div></div>
<div class="quizinfo"><p>Разрешено попыток: 2</p></div>
</body></html>`
}

func workshopHTML() string {
	return `<html><body><h1>Семинар</h1>
<div class="activity-description"><div class="no-overflow">Обсуждение докладов.</div></div>
<div data-region="activity-dates"><div><strong>Срок сдачи</strong> 20 ноября 2026, 10:00</div></div>
</body></html>`
}

func newMaterialSession(t *testing.T, srvURL string) *Session {
	t.Helper()
	cfg := testConfig(t, srvURL)
	cfg.Login, cfg.Password = testLogin, testPassword
	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func callMaterialTool(t *testing.T, cs *mcp.ClientSession, ctx context.Context, name string, args map[string]any, out any) {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	decodeStructured(t, res, out)
}

func callToolError(t *testing.T, cs *mcp.ClientSession, ctx context.Context, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("%s: ожидалась ошибка, получено %+v", name, res.StructuredContent)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestMaterialToolsRegistered(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
		if tool.Name == "moodle_material_read" || tool.Name == "moodle_search" || tool.Name == "moodle_course_outline" {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Errorf("%s: нет ReadOnlyHint", tool.Name)
			}
		}
	}
	for _, want := range []string{"moodle_course_outline", "moodle_material_read", "moodle_search"} {
		if !names[want] {
			t.Fatalf("в tools/list нет %q: %v", want, names)
		}
	}
}

func TestCourseOutlineTool(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var out OutlineOutput
	callMaterialTool(t, cs, ctx, "moodle_course_outline", map[string]any{"course_id": 1742}, &out)
	if out.CourseID != 1742 || out.Course != "ТФЯГ" || len(out.Sections) != 1 {
		t.Fatalf("структура курса: %+v", out)
	}
	if out.Sections[0].Name != "Введение" || len(out.Sections[0].Modules) != 8 {
		t.Fatalf("секция: %+v", out.Sections[0])
	}
	if out.TotalModules != 8 {
		t.Fatalf("модулей = %d", out.TotalModules)
	}

	var filtered OutlineOutput
	callMaterialTool(t, cs, ctx, "moodle_course_outline", map[string]any{"course_id": 1742, "kind": "page"}, &filtered)
	if filtered.TotalModules != 1 || len(filtered.Sections) != 1 {
		t.Fatalf("фильтр по kind: %+v", filtered)
	}
	if m := filtered.Sections[0].Modules[0]; m.CMID != 62991 || m.Kind != "page" || m.Name != "Энтропия и информация" {
		t.Fatalf("модуль фильтра: %+v", m)
	}
}

func TestMaterialReadPageResolvesKind(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var out MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 62991, "course_id": 1742}, &out)
	if out.Kind != "page" || out.Name != "Энтропия и информация" {
		t.Fatalf("материал: %+v", out)
	}
	if !strings.Contains(out.Text, "Формула Шеннона") {
		t.Fatalf("текст страницы: %q", out.Text)
	}
	if out.CourseID != 1742 || out.Section != "Введение" {
		t.Fatalf("метаданные: %+v", out)
	}

	var limited MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 62991, "kind": "page", "max_chars": 20}, &limited)
	if n := len([]rune(limited.Text)); n != 20 {
		t.Fatalf("max_chars: %d рун", n)
	}
	if !limited.Truncated || limited.Next == "" {
		t.Fatalf("усечение не отмечено: %+v", limited)
	}
}

func TestMaterialReadResource(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var out MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 63209, "course_id": 1742, "kind": "resource"}, &out)
	if out.Kind != "resource" || !strings.Contains(out.Text, "код Хаффмана") {
		t.Fatalf("ресурс: %+v", out)
	}
}

func TestMaterialReadFolder(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var list MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 64002, "course_id": 1742, "kind": "folder"}, &list)
	if len(list.Files) != 2 || !strings.Contains(list.Text, "lecture.txt") {
		t.Fatalf("список папки: %+v", list)
	}

	var files MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 64002, "course_id": 1742, "kind": "folder", "include_files": true}, &files)
	if !strings.Contains(files.Text, "Текст лекции из папки") {
		t.Fatalf("текст файлов папки: %+v", files)
	}
}

func TestMaterialReadBook(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var out MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 64001, "course_id": 1742, "kind": "book"}, &out)
	if len(out.Chapters) != 2 || !strings.Contains(out.Text, "первой главы") {
		t.Fatalf("книга: %+v", out)
	}
	if !strings.Contains(out.Next, "chapterid=2") {
		t.Fatalf("подсказка следующей главы: %q", out.Next)
	}

	var all MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 64001, "course_id": 1742, "kind": "book", "all_chapters": true}, &all)
	if !strings.Contains(all.Text, "первой главы") || !strings.Contains(all.Text, "второй главы") {
		t.Fatalf("все главы: %+v", all)
	}
}

func TestMaterialReadURLDoesNotFollow(t *testing.T) {
	srv, counters := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	var out MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 64003, "course_id": 1742, "kind": "url"}, &out)
	if out.Text != "https://external.example/material" {
		t.Fatalf("цель ссылки: %q", out.Text)
	}
	if posts, attempts := counters.snapshot(); posts != 0 || attempts != 0 {
		t.Fatalf("ссылка вызвала POST=%d attempts=%d", posts, attempts)
	}
}

func TestMaterialReadLessonNoAttempt(t *testing.T) {
	srv, counters := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	var out MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 60904, "course_id": 1807}, &out)
	if out.Kind != "lesson" || out.Status != model.MoodleTodo {
		t.Fatalf("урок: %+v", out)
	}
	if !strings.Contains(out.Text, "печать приветствия") {
		t.Fatalf("текст урока: %q", out.Text)
	}
	if posts, attempts := counters.snapshot(); posts != 0 || attempts != 0 {
		t.Fatalf("урок вызвал POST=%d attempts=%d", posts, attempts)
	}
}

func TestMaterialReadAssignAndQuizNoAttempt(t *testing.T) {
	srv, counters := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	counters.reset()
	var assign MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 64004, "course_id": 1742, "kind": "assign"}, &assign)
	if assign.Status != model.MoodleSubmitted || assign.Due != "15.11.2026 15:00" {
		t.Fatalf("задание: %+v", assign)
	}
	if !strings.Contains(assign.Text, "Сдать отчёт") {
		t.Fatalf("описание задания: %q", assign.Text)
	}

	var quiz MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 64005, "course_id": 1742, "kind": "quiz"}, &quiz)
	if quiz.Status != model.MoodleTodo || !strings.Contains(quiz.Text, "Разрешено попыток") {
		t.Fatalf("тест: %+v", quiz)
	}

	var workshop MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 64006, "course_id": 1742, "kind": "workshop"}, &workshop)
	if workshop.Due != "20.11.2026 10:00" || !strings.Contains(workshop.Text, "докладов") {
		t.Fatalf("семинар: %+v", workshop)
	}
	if posts, attempts := counters.snapshot(); posts != 0 || attempts != 0 {
		t.Fatalf("задания вызвали POST=%d attempts=%d", posts, attempts)
	}
}

func TestMaterialReadUnknownModule(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	text := callToolError(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 999999})
	if !strings.Contains(text, "module_not_found") || !strings.Contains(text, "действие") {
		t.Fatalf("ошибка без кода/действия: %q", text)
	}
}

func TestMaterialReadBadInput(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	text := callToolError(t, cs, ctx, "moodle_material_read", map[string]any{"cmid": 0})
	if !strings.Contains(text, "bad_input") {
		t.Fatalf("ошибка без кода: %q", text)
	}
}

func TestMoodleSearchByNameAndDeepText(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var python SearchOutput
	callMaterialTool(t, cs, ctx, "moodle_search", map[string]any{"query": "Python", "course_id": 1807}, &python)
	if python.Total != 2 {
		t.Fatalf("совпадения Python: %+v", python)
	}
	for _, hit := range python.Results {
		if hit.Source != "name" || hit.CourseID != 1807 || hit.CMID == 0 {
			t.Fatalf("совпадение Python: %+v", hit)
		}
	}

	var shallow SearchOutput
	callMaterialTool(t, cs, ctx, "moodle_search", map[string]any{"query": "энтропия", "course_id": 1515}, &shallow)
	for _, hit := range shallow.Results {
		if hit.Source == "text" {
			t.Fatalf("без deep найден текст: %+v", hit)
		}
	}

	var deep SearchOutput
	callMaterialTool(t, cs, ctx, "moodle_search", map[string]any{"query": "энтропия", "course_id": 1515, "deep": true}, &deep)
	var textHit *SearchHit
	for i := range deep.Results {
		if deep.Results[i].Source == "text" {
			textHit = &deep.Results[i]
		}
	}
	if textHit == nil {
		t.Fatalf("deep не нашёл текст страницы: %+v", deep)
	}
	if textHit.CMID != 65001 || textHit.CourseID != 1515 || textHit.Section != "Теория информации" {
		t.Fatalf("deep-совпадение: %+v", textHit)
	}
	if !strings.Contains(strings.ToLower(textHit.Snippet), "энтропия") {
		t.Fatalf("сниппет без совпадения: %q", textHit.Snippet)
	}
	if n := len([]rune(textHit.Snippet)); n > searchSnippetRadius*2+20 {
		t.Fatalf("сниппет слишком длинный: %d рун", n)
	}
}

func TestMoodleSearchSectionAndKind(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var bySection SearchOutput
	callMaterialTool(t, cs, ctx, "moodle_search", map[string]any{"query": "формальных языков", "course_id": 1742}, &bySection)
	if bySection.Total != 1 || bySection.Results[0].Source != "section" {
		t.Fatalf("поиск по секции: %+v", bySection)
	}

	var byKind SearchOutput
	callMaterialTool(t, cs, ctx, "moodle_search", map[string]any{"query": "Тест", "course_id": 1742, "kind": "quiz"}, &byKind)
	if byKind.Total != 1 || byKind.Results[0].Kind != "quiz" {
		t.Fatalf("поиск с kind: %+v", byKind)
	}
}

func TestMoodleSearchRequiresCourseForDeep(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	text := callToolError(t, cs, ctx, "moodle_search", map[string]any{"query": "энтропия", "deep": true})
	if !strings.Contains(text, "course_required") {
		t.Fatalf("ошибка deep без курса: %q", text)
	}
}

func TestMaterialResources(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	templates, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, rt := range templates.ResourceTemplates {
		seen[rt.URITemplate] = true
	}
	for _, want := range []string{OutlineResourceTemplate, MaterialResourceTemplate} {
		if !seen[want] {
			t.Fatalf("в resources/templates/list нет %q: %v", want, seen)
		}
	}

	read, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "moodle://course/1742/outline"})
	if err != nil {
		t.Fatal(err)
	}
	var outline OutlineOutput
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &outline); err != nil {
		t.Fatalf("outline json: %v", err)
	}
	if outline.CourseID != 1742 || outline.TotalModules != 8 {
		t.Fatalf("ресурс структуры: %+v", outline)
	}

	read, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "moodle://material/62991"})
	if err != nil {
		t.Fatal(err)
	}
	var material MaterialOutput
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &material); err != nil {
		t.Fatalf("material json: %v", err)
	}
	if material.Kind != "page" || !strings.Contains(material.Text, "Формула Шеннона") {
		t.Fatalf("ресурс материала: %+v", material)
	}
}

func TestMaterialResourceBadURI(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "moodle://material/abc"}); err == nil {
		t.Fatal("ожидалась ошибка для нечислового cmid")
	}
}

// TestSnippetAround проверяет окно ±120 рун и многоточия.
func TestSnippetAround(t *testing.T) {
	long := strings.Repeat("а", 300) + "энтропия" + strings.Repeat("б", 300)
	got := snippetAround(long, "энтропия")
	if !strings.Contains(got, "энтропия") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("сниппет: %q", got)
	}
	if n := len([]rune(got)); n > searchSnippetRadius*2+16 {
		t.Fatalf("сниппет длиннее окна: %d", n)
	}
	if got := snippetAround("коротко", "нет"); got != "коротко" {
		t.Fatalf("без совпадения: %q", got)
	}
}

// TestSearchRetriesAfterUnauthorized: страница курса отдала форму входа,
// Session.do должен один раз перелогиниться и повторить поиск.
func TestSearchRetriesAfterUnauthorized(t *testing.T) {
	srv, counters := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)
	counters.reset()
	before := counters.fake.loginCount()
	counters.rejectNextCourseViewOnce()

	var out SearchOutput
	callMaterialTool(t, cs, ctx, "moodle_search", map[string]any{"query": "Тест", "course_id": 1742}, &out)
	if out.Total != 1 || out.Results[0].CMID != 64005 {
		t.Fatalf("поиск после перелогина: %+v", out)
	}
	if got := counters.fake.loginCount(); got != before+1 {
		t.Fatalf("логинов = %d, want %d", got, before+1)
	}
	if got := counters.courseViewCount(); got != 2 {
		t.Fatalf("страниц курса запрошено %d, want 2", got)
	}
}

// TestCourseOutlineRetriesAfterUnauthorized: то же для структуры курса.
func TestCourseOutlineRetriesAfterUnauthorized(t *testing.T) {
	srv, counters := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)
	counters.reset()
	before := counters.fake.loginCount()
	counters.rejectNextCourseViewOnce()

	var out OutlineOutput
	callMaterialTool(t, cs, ctx, "moodle_course_outline", map[string]any{"course_id": 1742}, &out)
	if out.CourseID != 1742 || out.TotalModules != 8 {
		t.Fatalf("структура после перелогина: %+v", out)
	}
	if got := counters.fake.loginCount(); got != before+1 {
		t.Fatalf("логинов = %d, want %d", got, before+1)
	}
	if got := counters.courseViewCount(); got != 2 {
		t.Fatalf("страниц курса запрошено %d, want 2", got)
	}
}

// TestMoodleSearchEmptyQuery: пустой запрос — ошибка с кодом bad_input.
func TestMoodleSearchEmptyQuery(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	text := callToolError(t, cs, ctx, "moodle_search", map[string]any{"query": "   "})
	if !strings.Contains(text, "bad_input") {
		t.Fatalf("ошибка пустого query: %q", text)
	}
}

// TestMoodleSearchDeepLimitNote: больше 60 материалов в deep-поиске не
// читаются, а в Note остаётся причина.
func TestMoodleSearchDeepLimitNote(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var out SearchOutput
	callMaterialTool(t, cs, ctx, "moodle_search", map[string]any{
		"query": "редкоеслово", "course_id": 1900, "deep": true,
	}, &out)
	if out.Total != 0 {
		t.Fatalf("неожиданные совпадения: %+v", out)
	}
	if !strings.Contains(out.Note, "60") {
		t.Fatalf("нет пометки о лимите deep: %q", out.Note)
	}
}

// TestMaterialReadCapsAtMaxRunes: текст режется до 100 000 рун, а Next
// сообщает о пределе, а не предлагает ещё больший max_chars.
func TestMaterialReadCapsAtMaxRunes(t *testing.T) {
	srv, _ := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var out MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{
		"cmid": 69001, "kind": "page", "max_chars": 200000,
	}, &out)
	if n := len([]rune(out.Text)); n != maxToolOutputRunes {
		t.Fatalf("текст = %d рун, want %d", n, maxToolOutputRunes)
	}
	if !out.Truncated {
		t.Fatal("усечение не отмечено")
	}
	if !strings.Contains(out.Next, "предел") || strings.Contains(out.Next, "max_chars=") {
		t.Fatalf("Next предлагает невозможное: %q", out.Next)
	}
}

// TestBuildOutlineCapsOutput: потолок 100k рун работает и для структуры курса.
func TestBuildOutlineCapsOutput(t *testing.T) {
	o := moodle.CourseOutline{
		CourseID: 1,
		Sections: []moodle.CourseSection{{
			Name: "s",
			Modules: []moodle.CourseModule{{
				CMID: 1, Kind: "page", Name: strings.Repeat("Я", maxToolOutputRunes+1),
			}},
		}},
	}
	out := buildOutline(o, "")
	if !out.Truncated || out.TotalModules != 0 {
		t.Fatalf("структура не усечена: truncated=%v modules=%d", out.Truncated, out.TotalModules)
	}
}

// TestSnippetAroundUnicodeCaseChange: İ/K меняют длину при lower; срез не
// должен уезжать от совпадения.
func TestSnippetAroundUnicodeCaseChange(t *testing.T) {
	text := strings.Repeat("İ", 200) + " цель " + strings.Repeat("K", 200)
	got := snippetAround(text, "цель")
	if !strings.Contains(got, "цель") {
		t.Fatalf("совпадение потеряно: %q", got)
	}
}

// TestCourseOutlineCacheHitAndRefresh: повторный outline берётся из кэша,
// refresh=true перечитывает страницу курса.
func TestCourseOutlineCacheHitAndRefresh(t *testing.T) {
	srv, counters := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	var first, second OutlineOutput
	callMaterialTool(t, cs, ctx, "moodle_course_outline", map[string]any{"course_id": 1742}, &first)
	callMaterialTool(t, cs, ctx, "moodle_course_outline", map[string]any{"course_id": 1742}, &second)
	if got := counters.courseViewCount(); got != 1 {
		t.Fatalf("страница курса запрошена %d раз, want 1 (кэш)", got)
	}
	if first.TotalModules != second.TotalModules || second.TotalModules != 8 {
		t.Fatalf("кэш вернул другой outline: %+v vs %+v", first, second)
	}

	var refreshed OutlineOutput
	callMaterialTool(t, cs, ctx, "moodle_course_outline", map[string]any{
		"course_id": 1742, "refresh": true,
	}, &refreshed)
	if got := counters.courseViewCount(); got != 2 {
		t.Fatalf("после refresh страница курса запрошена %d раз, want 2", got)
	}
}

// TestMaterialCacheHitAndRefresh: материал кэшируется по всем аргументам,
// refresh=true перечитывает файл.
func TestMaterialCacheHitAndRefresh(t *testing.T) {
	srv, counters := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	args := map[string]any{"cmid": 63209, "course_id": 1742, "kind": "resource"}
	var first, second MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", args, &first)
	callMaterialTool(t, cs, ctx, "moodle_material_read", args, &second)
	if got := counters.resourceViewCount(); got != 1 {
		t.Fatalf("ресурс запрошен %d раз, want 1 (кэш)", got)
	}
	if first.Text != second.Text || !strings.Contains(first.Text, "код Хаффмана") {
		t.Fatalf("кэш вернул другой материал: %q vs %q", first.Text, second.Text)
	}

	var refreshed MaterialOutput
	callMaterialTool(t, cs, ctx, "moodle_material_read", map[string]any{
		"cmid": 63209, "course_id": 1742, "kind": "resource", "refresh": true,
	}, &refreshed)
	if got := counters.resourceViewCount(); got != 2 {
		t.Fatalf("после refresh ресурс запрошен %d раз, want 2", got)
	}
}

// TestSearchCacheHitAndRefresh: результаты поиска кэшируются, refresh
// перечитывает структуру курса.
func TestSearchCacheHitAndRefresh(t *testing.T) {
	srv, counters := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	args := map[string]any{"query": "Тест", "course_id": 1742, "kind": "quiz"}
	var first, second SearchOutput
	callMaterialTool(t, cs, ctx, "moodle_search", args, &first)
	callMaterialTool(t, cs, ctx, "moodle_search", args, &second)
	if got := counters.courseViewCount(); got != 1 {
		t.Fatalf("поиск открывал курс %d раз, want 1 (кэш)", got)
	}
	if first.Total != 1 || second.Total != 1 {
		t.Fatalf("результаты поиска: %+v vs %+v", first, second)
	}

	var refreshed SearchOutput
	callMaterialTool(t, cs, ctx, "moodle_search", map[string]any{
		"query": "Тест", "course_id": 1742, "kind": "quiz", "refresh": true,
	}, &refreshed)
	if got := counters.courseViewCount(); got != 2 {
		t.Fatalf("после refresh курс открыт %d раз, want 2", got)
	}
}

// TestSearchDoesNotCacheFailedResult: пустой ответ с Note (курсы не получены)
// не кэшируется — следующий вызов повторяет запрос.
func TestSearchDoesNotCacheFailedResult(t *testing.T) {
	srv, counters := newMaterialTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	counters.setCoursesFailing(true)
	var failed SearchOutput
	callMaterialTool(t, cs, ctx, "moodle_search", map[string]any{"query": "Тест"}, &failed)
	if failed.Total != 0 || len(failed.Results) != 0 {
		t.Fatalf("при сбое курсов вернулись результаты: %+v", failed)
	}
	if failed.Note == "" {
		t.Fatal("сбой курсов не отмечен в Note")
	}

	counters.setCoursesFailing(false)
	var retried SearchOutput
	callMaterialTool(t, cs, ctx, "moodle_search", map[string]any{"query": "Тест"}, &retried)
	if retried.Total == 0 || len(retried.Results) == 0 {
		t.Fatalf("повторный поиск не выполнен, кэш сохранил сбой: %+v", retried)
	}
	if got := counters.courseViewCount(); got == 0 {
		t.Fatal("повторный поиск не обратился к Moodle")
	}
}

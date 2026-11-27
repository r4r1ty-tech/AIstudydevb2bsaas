package mcpapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// deadlineFake — фейковый Moodle с лентой событий и страницами элементов.
type deadlineFake struct {
	fake *fakeMoodle

	mu            sync.Mutex
	timelineCalls int
	events        []map[string]any
	details       map[int64]string
	attempts      int
	active        int
	peak          int
	delay         time.Duration
	rejectDetail  int
}

func (d *deadlineFake) setEvents(events []map[string]any) {
	d.mu.Lock()
	d.events = events
	d.mu.Unlock()
}

func (d *deadlineFake) setDetail(cmid int64, html string) {
	d.mu.Lock()
	d.details[cmid] = html
	d.mu.Unlock()
}

func (d *deadlineFake) timelineCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.timelineCalls
}

func (d *deadlineFake) attemptCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.attempts
}

func (d *deadlineFake) resetCounters() {
	d.mu.Lock()
	d.timelineCalls, d.attempts, d.peak = 0, 0, 0
	d.mu.Unlock()
}

// setDelay замедляет страницы модулей, чтобы параллельные запросы успевали
// пересечься и тест увидел пик одновременности.
func (d *deadlineFake) setDelay(v time.Duration) {
	d.mu.Lock()
	d.delay = v
	d.mu.Unlock()
}

// rejectNextDetailOnce заставляет следующую страницу модуля увести на форму
// входа: Session.do должен один раз перелогиниться и повторить ленту.
func (d *deadlineFake) rejectNextDetailOnce() {
	d.mu.Lock()
	d.rejectDetail++
	d.mu.Unlock()
}

func (d *deadlineFake) takeDetailReject() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rejectDetail <= 0 {
		return false
	}
	d.rejectDetail--
	return true
}

func (d *deadlineFake) peakConcurrency() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.peak
}

// newDeadlineTestServer поднимает fakeMoodle и перехватывает ленту событий и
// страницы модулей. Всё остальное (логин, курсы, sesskey) — у fakeMoodle.
func newDeadlineTestServer(t *testing.T) (*httptest.Server, *deadlineFake) {
	t.Helper()
	fake := newFakeMoodle()
	d := &deadlineFake{fake: fake, details: map[int64]string{}}
	fallback := fake.handler()
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			d.mu.Lock()
			if strings.Contains(r.URL.Path, "startattempt") || strings.Contains(r.URL.Path, "submit") {
				d.attempts++
			}
			d.mu.Unlock()
		}
		if r.URL.Path == "/moodle/lib/ajax/service.php" &&
			r.URL.Query().Get("info") == "core_calendar_get_action_events_by_timesort" {
			d.mu.Lock()
			d.timelineCalls++
			events := d.events
			d.mu.Unlock()
			payload, err := json.Marshal([]map[string]any{{"error": false, "data": map[string]any{"events": events}}})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(payload)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/moodle/mod/") {
			if d.takeDetailReject() {
				d.fake.mu.Lock()
				d.fake.cookieDead = true
				d.fake.mu.Unlock()
				http.Redirect(w, r, "/moodle/login/index.php", http.StatusSeeOther)
				return
			}
			id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
			d.mu.Lock()
			d.active++
			if d.active > d.peak {
				d.peak = d.active
			}
			delay := d.delay
			body, ok := d.details[id]
			d.mu.Unlock()
			defer func() {
				d.mu.Lock()
				d.active--
				d.mu.Unlock()
			}()
			if delay > 0 {
				time.Sleep(delay)
			}
			if ok {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(body))
				return
			}
		}
		fallback.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv, d
}

// ruMonthGenitive — месяцы в формате Moodle: «27 ноября 2026, 15:00».
var ruMonthGenitive = [...]string{
	"", "января", "февраля", "марта", "апреля", "мая", "июня",
	"июля", "августа", "сентября", "октября", "ноября", "декабря",
}

func ruDateTime(t time.Time) string {
	t = t.In(samaraLocation)
	return fmt.Sprintf("%d %s %d, %02d:%02d",
		t.Day(), ruMonthGenitive[int(t.Month())], t.Year(), t.Hour(), t.Minute())
}

func deadlineAssignHTML(due time.Time, submitted bool) string {
	status := ""
	if submitted {
		status = `<div class="submissionstatustable"><table><tr><td class="submissionstatussubmitted">Отправлено</td></tr></table></div>`
	}
	return `<html><body><h1>Лабораторная работа</h1>
<div class="activity-description"><div class="no-overflow">Сдать отчёт по энтропии.</div></div>
<div data-region="activity-dates"><div><strong>Срок сдачи</strong> ` + ruDateTime(due) + `</div></div>` +
		status + `</body></html>`
}

func deadlineQuizHTML(due time.Time) string {
	return `<html><body><h1>Тест по энтропии</h1>
<div data-region="activity-dates"><div><strong>Срок сдачи</strong> ` + ruDateTime(due) + `</div></div>
<div class="quizinfo"><p>Разрешено попыток: 2</p></div>
</body></html>`
}

func deadlineEvent(id, cmid, courseID int64, course, kind, name string, due time.Time) map[string]any {
	return map[string]any{
		"id":         id,
		"name":       name,
		"modulename": kind,
		"instance":   cmid,
		"timesort":   due.Unix(),
		"overdue":    false,
		"url":        fmt.Sprintf("/moodle/mod/%s/view.php?id=%d", kind, cmid),
		"course":     map[string]any{"id": courseID, "fullname": course},
	}
}

// deadlineSession собирает сессию и MCP-клиент на фейковом сервере.
func deadlineSession(t *testing.T, srv *httptest.Server, d *deadlineFake) (*mcp.ClientSession, context.Context) {
	t.Helper()
	sess := newMaterialSession(t, srv.URL)
	d.resetCounters()
	return connectMCP(t, sess)
}

// TestDeadlinesToolSortsAndReports: лента сортируется по сроку, поля заполнены,
// «закрывается» из названия убирается.
func TestDeadlinesToolSortsAndReports(t *testing.T) {
	srv, d := newDeadlineTestServer(t)
	now := time.Now().In(samaraLocation).Truncate(time.Minute)
	dueLate := now.Add(48 * time.Hour)
	dueSoon := now.Add(24 * time.Hour)
	dueLast := now.Add(72 * time.Hour)

	d.setEvents([]map[string]any{
		deadlineEvent(1, 64004, 1742, "ТФЯГ", "assign", "Лабораторная работа закрывается", dueLate),
		deadlineEvent(2, 64005, 1515, "Теория информации", "quiz", "Тест по энтропии", dueSoon),
		deadlineEvent(3, 64007, 1742, "ТФЯГ", "assign", "Эссе", dueLast),
		// Событие без cmid в ссылке пропускается.
		{"id": 4, "name": "Событие без модуля", "modulename": "", "timesort": dueSoon.Unix(), "url": "/moodle/mod/quiz/view.php"},
	})
	d.setDetail(64004, deadlineAssignHTML(dueLate, true))
	d.setDetail(64005, deadlineQuizHTML(dueSoon))
	d.setDetail(64007, deadlineAssignHTML(dueLast, false))

	cs, ctx := deadlineSession(t, srv, d)
	var out DeadlinesOutput
	callMaterialTool(t, cs, ctx, "moodle_deadlines", map[string]any{"days_ahead": 7}, &out)

	if out.Total != 3 || len(out.Items) != 3 {
		t.Fatalf("элементов = %d/%d, want 3: %+v", out.Total, len(out.Items), out.Items)
	}
	gotOrder := []int64{out.Items[0].CMID, out.Items[1].CMID, out.Items[2].CMID}
	wantOrder := []int64{64005, 64004, 64007}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("порядок = %v, want %v", gotOrder, wantOrder)
		}
	}

	quiz := out.Items[0]
	if quiz.Kind != "quiz" || quiz.CourseID != 1515 || quiz.Course != "Теория информации" {
		t.Fatalf("тест: %+v", quiz)
	}
	if quiz.Status != "todo" || !strings.Contains(quiz.Todo, "Разрешено попыток: 2") {
		t.Fatalf("статус/попытки теста: %+v", quiz)
	}
	if quiz.Due != dueSoon.Format(deadlineTimeLayout) {
		t.Fatalf("срок теста = %q, want %q", quiz.Due, dueSoon.Format(deadlineTimeLayout))
	}
	wantURL := srv.URL + "/moodle/mod/quiz/view.php?id=64005"
	if quiz.URL != wantURL {
		t.Fatalf("url теста = %q, want %q", quiz.URL, wantURL)
	}

	assign := out.Items[1]
	if assign.Title != "Лабораторная работа" || assign.Kind != "assign" || assign.CourseID != 1742 {
		t.Fatalf("задание: %+v", assign)
	}
	if assign.Status != "submitted" {
		t.Fatalf("статус задания = %q, want submitted", assign.Status)
	}
	if assign.Overdue || out.OverdueTotal != 0 {
		t.Fatalf("будущий срок помечен просроченным: %+v", assign)
	}
	if out.Timezone != deadlineTimezone || out.Now == "" {
		t.Fatalf("метаданные времени: %+v", out)
	}
	if got := d.attemptCount(); got != 0 {
		t.Fatalf("лента начала попытки: %d", got)
	}
}

// TestDeadlinesOverdueAndIncludeOverdue: прошедший срок помечается overdue, а
// include_overdue=false его убирает.
func TestDeadlinesOverdueAndIncludeOverdue(t *testing.T) {
	srv, d := newDeadlineTestServer(t)
	past := time.Now().In(samaraLocation).Truncate(time.Minute).Add(-2 * time.Hour)
	d.setEvents([]map[string]any{
		deadlineEvent(1, 64008, 1742, "ТФЯГ", "assign", "Просроченная работа", past),
	})
	d.setDetail(64008, deadlineAssignHTML(past, false))

	cs, ctx := deadlineSession(t, srv, d)
	var out DeadlinesOutput
	callMaterialTool(t, cs, ctx, "moodle_deadlines", map[string]any{"days_ahead": 7}, &out)
	if out.Total != 1 || !out.Items[0].Overdue || out.OverdueTotal != 1 {
		t.Fatalf("просрочка не отмечена: %+v", out)
	}
	if out.Items[0].Due != past.Format(deadlineTimeLayout) {
		t.Fatalf("срок = %q, want %q", out.Items[0].Due, past.Format(deadlineTimeLayout))
	}

	var noOverdue DeadlinesOutput
	callMaterialTool(t, cs, ctx, "moodle_deadlines", map[string]any{
		"days_ahead": 7, "include_overdue": false,
	}, &noOverdue)
	if noOverdue.Total != 0 {
		t.Fatalf("include_overdue=false оставил элементы: %+v", noOverdue.Items)
	}
	if noOverdue.IncludeOverdue {
		t.Fatal("include_overdue=false не отразился в ответе")
	}
}

// TestDeadlinesCourseFilter: course_id оставляет только элементы этого курса.
func TestDeadlinesCourseFilter(t *testing.T) {
	srv, d := newDeadlineTestServer(t)
	now := time.Now().In(samaraLocation).Truncate(time.Minute)
	d.setEvents([]map[string]any{
		deadlineEvent(1, 64004, 1742, "ТФЯГ", "assign", "Работа ТФЯГ", now.Add(24*time.Hour)),
		deadlineEvent(2, 64005, 1515, "Теория информации", "quiz", "Тест ТИ", now.Add(48*time.Hour)),
	})
	d.setDetail(64004, deadlineAssignHTML(now.Add(24*time.Hour), false))

	cs, ctx := deadlineSession(t, srv, d)
	var out DeadlinesOutput
	callMaterialTool(t, cs, ctx, "moodle_deadlines", map[string]any{
		"days_ahead": 7, "course_id": 1742,
	}, &out)
	if out.Total != 1 || out.Items[0].CMID != 64004 || out.Items[0].Course != "ТФЯГ" {
		t.Fatalf("фильтр по курсу: %+v", out)
	}
}

// TestDeadlinesCacheAndRefresh: повторный вызов берёт кэш, refresh перечитывает.
func TestDeadlinesCacheAndRefresh(t *testing.T) {
	srv, d := newDeadlineTestServer(t)
	now := time.Now().In(samaraLocation).Truncate(time.Minute)
	d.setEvents([]map[string]any{
		deadlineEvent(1, 64004, 1742, "ТФЯГ", "assign", "Работа", now.Add(24*time.Hour)),
	})
	d.setDetail(64004, deadlineAssignHTML(now.Add(24*time.Hour), false))

	cs, ctx := deadlineSession(t, srv, d)
	args := map[string]any{"days_ahead": 7}
	var first, second DeadlinesOutput
	callMaterialTool(t, cs, ctx, "moodle_deadlines", args, &first)
	callMaterialTool(t, cs, ctx, "moodle_deadlines", args, &second)
	if got := d.timelineCount(); got != 1 {
		t.Fatalf("лента запрошена %d раз, want 1 (кэш)", got)
	}
	if len(first.Items) != len(second.Items) || first.Total != second.Total {
		t.Fatalf("кэш вернул другой ответ: %+v vs %+v", first, second)
	}

	var refreshed DeadlinesOutput
	callMaterialTool(t, cs, ctx, "moodle_deadlines", map[string]any{
		"days_ahead": 7, "refresh": true,
	}, &refreshed)
	if got := d.timelineCount(); got != 2 {
		t.Fatalf("после refresh лента запрошена %d раз, want 2", got)
	}
}

// TestDeadlinesResource: moodle://deadlines отдаёт JSON того же ответа.
func TestDeadlinesResource(t *testing.T) {
	srv, d := newDeadlineTestServer(t)
	now := time.Now().In(samaraLocation).Truncate(time.Minute)
	d.setEvents([]map[string]any{
		deadlineEvent(1, 64004, 1742, "ТФЯГ", "assign", "Работа", now.Add(24*time.Hour)),
	})
	d.setDetail(64004, deadlineAssignHTML(now.Add(24*time.Hour), false))

	cs, ctx := deadlineSession(t, srv, d)
	list, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, res := range list.Resources {
		if res.URI == DeadlineResourceURI {
			found = true
			if res.MIMEType != "application/json" {
				t.Fatalf("mime = %q", res.MIMEType)
			}
		}
	}
	if !found {
		t.Fatalf("в resources/list нет %q", DeadlineResourceURI)
	}

	read, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: DeadlineResourceURI})
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Contents) != 1 || read.Contents[0].MIMEType != "application/json" {
		t.Fatalf("contents = %+v", read.Contents)
	}
	var out DeadlinesOutput
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &out); err != nil {
		t.Fatalf("resource json: %v", err)
	}
	if out.Total != 1 || len(out.Items) != 1 || out.Items[0].CMID != 64004 {
		t.Fatalf("ресурс сроков: %+v", out)
	}
	if out.Timezone != deadlineTimezone {
		t.Fatalf("timezone = %q", out.Timezone)
	}
}

// TestDeadlinesToolRegistered: инструмент виден и помечен только для чтения.
func TestDeadlinesToolRegistered(t *testing.T) {
	srv, _ := newDeadlineTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range list.Tools {
		if tool.Name != "moodle_deadlines" {
			continue
		}
		found = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatal("moodle_deadlines без ReadOnlyHint")
		}
	}
	if !found {
		t.Fatal("в tools/list нет moodle_deadlines")
	}
}

// TestAbsoluteMoodleURL: href со страницы Moodle становится абсолютным.
func TestAbsoluteMoodleURL(t *testing.T) {
	cases := []struct{ base, raw, want string }{
		{"https://do.ssau.ru/moodle", "/moodle/mod/assign/view.php?id=1", "https://do.ssau.ru/moodle/mod/assign/view.php?id=1"},
		{"https://do.ssau.ru/moodle", "/mod/assign/view.php?id=1", "https://do.ssau.ru/mod/assign/view.php?id=1"},
		{"https://do.ssau.ru/moodle", "mod/assign/view.php?id=1", "https://do.ssau.ru/moodle/mod/assign/view.php?id=1"},
		{"https://do.ssau.ru/moodle", "https://do.ssau.ru/x", "https://do.ssau.ru/x"},
		{"https://do.ssau.ru/moodle", "", ""},
	}
	for _, tc := range cases {
		if got := absoluteMoodleURL(tc.base, tc.raw); got != tc.want {
			t.Errorf("absoluteMoodleURL(%q, %q) = %q, want %q", tc.base, tc.raw, got, tc.want)
		}
	}
}

// TestDeadlinesLimitsParallelDetailRequests: больше четырёх элементов не
// превращаются в шквал — одновременно открывается не больше Limiter.Cap().
func TestDeadlinesLimitsParallelDetailRequests(t *testing.T) {
	srv, d := newDeadlineTestServer(t)
	now := time.Now().In(samaraLocation).Truncate(time.Minute)
	const items = 8
	events := make([]map[string]any, 0, items)
	for i := 0; i < items; i++ {
		cmid := int64(65000 + i)
		due := now.Add(time.Duration(i+1) * time.Hour)
		events = append(events, deadlineEvent(int64(i+1), cmid, 1742, "ТФЯГ", "assign",
			fmt.Sprintf("Работа %d", i+1), due))
		d.setDetail(cmid, deadlineAssignHTML(due, false))
	}
	d.setEvents(events)
	d.setDelay(10 * time.Millisecond)

	cs, ctx := deadlineSession(t, srv, d)
	var out DeadlinesOutput
	callMaterialTool(t, cs, ctx, "moodle_deadlines", map[string]any{"days_ahead": 30}, &out)
	if out.Total != items {
		t.Fatalf("элементов = %d, want %d", out.Total, items)
	}
	peak := d.peakConcurrency()
	if peak > defaultToolConcurrency {
		t.Fatalf("одновременных запросов к модулям %d, потолок %d", peak, defaultToolConcurrency)
	}
	if peak < 2 {
		t.Fatalf("страницы модулей не открывались параллельно: пик %d", peak)
	}
}

// TestDeadlinesRetriesAfterDetailLoginRedirect: страница модуля один раз
// уводит на вход — Session.do перелогинивается и повторяет ленту.
func TestDeadlinesRetriesAfterDetailLoginRedirect(t *testing.T) {
	srv, d := newDeadlineTestServer(t)
	now := time.Now().In(samaraLocation).Truncate(time.Minute)
	due := now.Add(24 * time.Hour)
	d.setEvents([]map[string]any{
		deadlineEvent(1, 64004, 1742, "ТФЯГ", "assign", "Работа", due),
	})
	d.setDetail(64004, deadlineAssignHTML(due, false))

	sess := newMaterialSession(t, srv.URL)
	d.resetCounters()
	before := d.fake.loginCount()
	d.rejectNextDetailOnce()
	cs, ctx := connectMCP(t, sess)

	var out DeadlinesOutput
	callMaterialTool(t, cs, ctx, "moodle_deadlines", map[string]any{"days_ahead": 7}, &out)
	if out.Total != 1 || out.Items[0].CMID != 64004 {
		t.Fatalf("лента после перелогина: %+v", out)
	}
	if got := d.fake.loginCount(); got != before+1 {
		t.Fatalf("логинов = %d, want %d", got, before+1)
	}
	if got := d.attemptCount(); got != 0 {
		t.Fatalf("перелогин запустил попытку теста: %d", got)
	}
}

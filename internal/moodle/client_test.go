package moodle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const courseHTML = `<ul>
<li class="section course-section" data-sectionname="1. Введение в АД"><ul>
 <li class="activity activity-wrapper assign modtype_assign hasinfo" id="module-63898" data-id="63898">
  <div class="activityname"><a href="BASE/mod/assign/view.php?id=63898"><span class="instancename">Лабораторная работа № 1 <span class="accesshide"> Задание</span></span></a></div></li>
 <li class="activity modtype_quiz" id="module-700"><div class="activityname"><a href="BASE/mod/quiz/view.php?id=700"><span class="instancename">Тест 1<span class="accesshide"> Тест</span></span></a></div></li>
 <li class="activity modtype_url" id="module-5"><div class="activityname"><a href="BASE/mod/url/view.php?id=5"><span class="instancename">Ссылка</span></a></div></li>
</ul></li></ul>`

const assignHTML = `<div data-region="activity-information" class="activity-information">
 <div data-region="activity-dates" class="activity-dates">
  <div><strong>Открыто с:</strong> пятница, 4 сентября 2026, 00:00</div>
  <div><strong>Срок сдачи:</strong> пятница, 20 ноября 2026, 23:59</div>
 </div>
 <div class="automatic-completion-conditions"><span class="badge alert-light"><strong>Сделать:</strong> Отправить ответ</span></div>
</div>
<div class="activity-description"><div class="no-overflow">Сделать диагностический анализ датасета.</div></div>
<div class="submissionstatustable"><table class="generaltable table-bordered"><tbody>
 <tr><th>Состояние ответа на задание</th><td class="submissionstatusdraft cell c1">Черновик (не отправлен)</td></tr>
 <tr><th>Оставшееся время</th><td>6 дн. 10 час.</td></tr>
</tbody></table></div>`

const quizHTML = `<div data-region="activity-dates" class="activity-dates">
 <div><strong>Открыто с:</strong> четверг, 24 сентября 2026, 08:00</div>
 <div><strong>Закрывается:</strong> воскресенье, 15 ноября 2026, 15:00</div></div>
<div class="quizinfo"><p>Разрешено попыток: 3</p><p>Ограничение по времени: 1 ч. 30 мин.</p></div>
<h3>Резюме ваших предыдущих попыток</h3>
<table class="generaltable generalbox quizreviewsummary mb-0"><tbody>
 <tr><th>Состояние</th><td>Завершены</td></tr><tr><th>Оценка</th><td>4,00 из 4,00 (100%)</td></tr></tbody></table>
<div id="feedback"><h3>Ваша итоговая оценка за этот тест: 4,00/4,00</h3></div>`

func fakeMoodle(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	logged := func(r *http.Request) bool {
		c, err := r.Cookie("MoodleSession")
		return err == nil && c.Value == "good"
	}
	mux.HandleFunc("/moodle/login/index.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: "MoodleSession", Value: "anon", Path: "/"})
			w.Write([]byte(`<form id="login" method="post" action="` + srv.URL + `/moodle/login/index.php">
<input type="hidden" name="logintoken" value="tok1"><input name="username"><input type="password" name="password"></form>`))
			return
		}
		r.ParseForm()
		if r.FormValue("logintoken") != "tok1" {
			t.Errorf("logintoken lost")
		}
		if r.FormValue("username") != "student" || r.FormValue("password") != "secret" {
			http.Redirect(w, r, "/moodle/login/index.php", http.StatusSeeOther)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "MoodleSession", Value: "good", Path: "/"})
		http.Redirect(w, r, "/moodle/my/", http.StatusSeeOther)
	})
	mux.HandleFunc("/moodle/my/", func(w http.ResponseWriter, r *http.Request) {
		if !logged(r) {
			http.Redirect(w, r, "/moodle/login/index.php", http.StatusSeeOther)
			return
		}
		w.Write([]byte(`<script>M.cfg = {"wwwroot":"x","sesskey":"SK123","userId":1};</script>`))
	})
	mux.HandleFunc("/moodle/lib/ajax/service.php", func(w http.ResponseWriter, r *http.Request) {
		if !logged(r) || r.URL.Query().Get("sesskey") != "SK123" {
			w.Write([]byte(`[{"error":true,"exception":{"errorcode":"servicerequireslogin","message":"x"}}]`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var req []struct {
			Method string         `json:"methodname"`
			Args   map[string]any `json:"args"`
		}
		json.Unmarshal(raw, &req)
		switch req[0].Method {
		case "core_course_get_enrolled_courses_by_timeline_classification":
			w.Write([]byte(`[{"error":false,"data":{"courses":[{"id":1862,"fullname":"Статистический анализ данных"},{"id":9,"fullname":"Скрытый","hidden":true}]}}]`))
		case "core_calendar_get_action_events_by_timesort":
			if req[0].Args["limitnum"].(float64) > 50 {
				t.Errorf("limitnum > 50")
			}
			w.Write([]byte(`[{"error":false,"data":{"events":[{"id":1,"name":"Тест 1 закрывается","modulename":"quiz","timesort":1794740400,"url":"` + srv.URL + `/moodle/mod/quiz/view.php?id=700","course":{"id":1862,"fullname":"Статистический анализ данных"}}]}}]`))
		default:
			w.Write([]byte(`[{"error":true,"exception":{"errorcode":"servicenotavailable"}}]`))
		}
	})
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !logged(r) {
				http.Redirect(w, r, "/moodle/login/index.php", http.StatusSeeOther)
				return
			}
			w.Write([]byte(strings.ReplaceAll(body, "BASE", srv.URL+"/moodle")))
		}
	}
	mux.HandleFunc("/moodle/course/view.php", page(courseHTML))
	mux.HandleFunc("/moodle/mod/assign/view.php", page(assignHTML))
	mux.HandleFunc("/moodle/mod/quiz/view.php", page(quizHTML))
	srv = httptest.NewServer(mux)
	return srv
}

func TestMoodleFlow(t *testing.T) {
	srv := fakeMoodle(t)
	defer srv.Close()
	ctx := context.Background()
	loc, _ := time.LoadLocation("Europe/Samara")

	c, _ := New(srv.URL + "/moodle")
	if err := c.Login(ctx, "student", "nope"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("bad login: %v", err)
	}
	if err := c.Login(ctx, "student", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}

	c2, _ := New(srv.URL + "/moodle")
	if err := c2.Check(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("empty session must be unauthorized: %v", err)
	}
	c2.Restore(c.Session())
	courses, err := c2.Courses(ctx)
	if err != nil || len(courses) != 1 || courses[0].ID != 1862 {
		t.Fatalf("courses %+v %v", courses, err)
	}
	evs, err := c2.Timeline(ctx, time.Now(), time.Time{})
	if err != nil || len(evs) != 1 || evs[0].CMID() != 700 {
		t.Fatalf("timeline %+v %v", evs, err)
	}
	acts, err := c2.CourseActivities(ctx, courses[0])
	if err != nil || len(acts) != 2 {
		t.Fatalf("activities %+v %v", acts, err)
	}
	a := acts[0]
	if a.CMID != 63898 || a.Kind != model.MoodleAssign || a.Name != "Лабораторная работа № 1" || a.Section != "1. Введение в АД" {
		t.Fatalf("assign activity %+v", a)
	}
	d, err := c2.ActivityDetail(ctx, a, loc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != model.MoodleDraft || d.Due == nil || d.Due.Format("2006-01-02 15:04") != "2026-11-20 23:59" || d.Opens == nil {
		t.Fatalf("assign detail %+v", d)
	}
	todo := strings.Join(d.Todo, " | ")
	for _, want := range []string{"Сделать: Отправить ответ", "Черновик", "Оставшееся время", "диагностический анализ"} {
		if !strings.Contains(todo, want) {
			t.Errorf("todo misses %q: %s", want, todo)
		}
	}
	q, err := c2.ActivityDetail(ctx, acts[1], loc)
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != model.MoodleSubmitted || q.Due == nil || q.Due.Format("2006-01-02 15:04") != "2026-11-15 15:00" {
		t.Fatalf("quiz detail %+v", q)
	}
	if !strings.Contains(strings.Join(q.Todo, " "), "Разрешено попыток: 3") {
		t.Fatalf("quiz todo %v", q.Todo)
	}
}

func TestParseRuDate(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Samara")
	got, ok := ParseRuDate("Закрывается: воскресенье, 15 ноября 2026, 15:00", loc)
	if !ok || got.Format(time.RFC3339) != "2026-11-15T15:00:00+04:00" {
		t.Fatalf("%v %v", got, ok)
	}
	if _, ok := ParseRuDate("скоро", loc); ok {
		t.Fatal("garbage parsed")
	}
}

func TestOfflineGradedAssignAndManualQuiz(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Samara")
	doc := func(h string) *goquery.Document {
		d, err := goquery.NewDocumentFromReader(strings.NewReader(h))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	a := parseDetail(doc(`<div class="submissionstatustable"><table class="generaltable"><tr><th>Состояние ответа на задание</th><td class="cell c1">Ответ на задание должен быть представлен вне сайта</td></tr>
<tr><th>Состояние оценивания</th><td class="submissionnotgraded">Не оценено</td></tr></table></div>
<div class="feedback"><table class="generaltable"><tr><th>Оценка</th><td>2,00 / 100,00</td></tr></table></div>`), model.MoodleAssign, loc)
	if a.Status != model.MoodleDone || !strings.Contains(strings.Join(a.Todo, " "), "вне сайта") {
		t.Fatalf("offline graded assign: %+v", a)
	}
	ungraded := parseDetail(doc(`<div class="submissionstatustable"><table><tr><th>Состояние ответа на задание</th><td>Ответ на задание должен быть представлен вне сайта</td></tr></table></div>
<table class="generaltable"><tr><th>Оценка</th><td>-</td></tr></table>`), model.MoodleAssign, loc)
	if ungraded.Status != model.MoodleTodo {
		t.Fatalf("ungraded must stay todo: %+v", ungraded)
	}
	q := parseDetail(doc(`<div class="box quizattempt">Вернуться к курсу</div><div id="feedback">Ваша итоговая оценка за этот элемента курса была скорректирована вручную.</div>`), model.MoodleQuiz, loc)
	if q.Status != model.MoodleSubmitted {
		t.Fatalf("manually graded quiz: %+v", q)
	}
	missed := parseDetail(doc(`<div class="quizinfo"><p>Разрешено попыток: 1</p></div><div class="box quizattempt">Вернуться к курсу</div>`), model.MoodleQuiz, loc)
	if missed.Status != model.MoodleTodo || !strings.Contains(strings.Join(missed.Todo, " "), "не открыт") {
		t.Fatalf("missed quiz: %+v", missed)
	}
}

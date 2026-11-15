package uni

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

var samara, _ = time.LoadLocation("Europe/Samara")

type fakeUni struct {
	srv        *httptest.Server
	lkLogins   atomic.Int32
	mdLogins   atomic.Int32
	mu         sync.Mutex
	events     string
	assignHTML string
	// quiz — обработчик /moodle/mod/quiz/*; nil — только заглушка view.php. Под mu.
	quiz http.Handler
}

const ttJSON = `{"currentYear":{"id":15,"startDate":"2026-08-31"},"lessons":[
 {"id":1,"weekday":{"id":5},"time":{"beginTime":"09:45","endTime":"11:20"},"discipline":{"name":"Теория информации"},"type":{"name":"Лекция"},
  "teachers":[{"name":"Иванов И И"}],"groups":[{"id":100,"subgroup":null}],"conference":{"url":"https://bbb.ssau.ru/b/room-1"},
  "weeks":[{"week":%[1]d,"isOnline":1}]}],
 "ietLessons":[{"id":7,"weekday":{"id":3},"time":{"beginTime":"13:30","endTime":"15:05"},"flows":[{"discipline":{"name":"ИОТ %[2]s"},"loadType":{"name":"Практика"}}],
  "weeks":[{"week":%[1]d,"isOnline":0,"room":{"name":"531а"},"building":{"name":"5"}}]}]}`

func newFake(t *testing.T) *fakeUni {
	f := &fakeUni{events: `[{"id":10,"title":"Старое","description":"было"}]`}
	mux := http.NewServeMux()
	// ---- ЛК: кука lk=<login> ----
	mux.HandleFunc("/account/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`<form method="POST"><input type="hidden" name="$ACTION_KEY" value="k"><input name="login"><input type="password" name="password"></form>`))
			return
		}
		r.ParseMultipartForm(1 << 20)
		f.lkLogins.Add(1)
		if r.FormValue("password") != "pw" {
			w.Write([]byte("Неверный логин и(или) пароль"))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "lk", Value: r.FormValue("login"), Path: "/"})
		w.WriteHeader(http.StatusSeeOther)
	})
	lkAuth := func(h func(w http.ResponseWriter, r *http.Request, who string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie("lk")
			if err != nil || c.Value == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r, c.Value)
		}
	}
	mux.HandleFunc("/api/proxy/personal/groups", lkAuth(func(w http.ResponseWriter, r *http.Request, _ string) {
		w.Write([]byte(`[{"id":100,"name":"6302"}]`))
	}))
	mux.HandleFunc("/api/proxy/statements/student/years", lkAuth(func(w http.ResponseWriter, r *http.Request, _ string) {
		w.Write([]byte(`[{"id":15,"name":"2026-2027"}]`))
	}))
	mux.HandleFunc("/api/proxy/timetable/get-timetable", lkAuth(func(w http.ResponseWriter, r *http.Request, who string) {
		var week int
		fmt.Sscan(r.URL.Query().Get("week"), &week)
		fmt.Fprintf(w, ttJSON, week, who)
	}))
	mux.HandleFunc("/api/proxy/events", lkAuth(func(w http.ResponseWriter, r *http.Request, _ string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Write([]byte(f.events))
	}))
	// ---- Moodle ----
	mlogged := func(r *http.Request) bool { c, err := r.Cookie("MoodleSession"); return err == nil && c.Value == "ok" }
	mux.HandleFunc("/moodle/login/index.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`<form id="login" method="post" action="` + f.srv.URL + `/moodle/login/index.php"><input type="hidden" name="logintoken" value="t"><input name="username"><input type="password" name="password"></form>`))
			return
		}
		r.ParseForm()
		f.mdLogins.Add(1)
		if r.FormValue("password") != "pw" {
			http.Redirect(w, r, "/moodle/login/index.php", http.StatusSeeOther)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "MoodleSession", Value: "ok", Path: "/"})
		http.Redirect(w, r, "/moodle/my/", http.StatusSeeOther)
	})
	mux.HandleFunc("/moodle/my/", func(w http.ResponseWriter, r *http.Request) {
		if !mlogged(r) {
			http.Redirect(w, r, "/moodle/login/index.php", http.StatusSeeOther)
			return
		}
		w.Write([]byte(`"sesskey":"S1"`))
	})
	mux.HandleFunc("/moodle/lib/ajax/service.php", func(w http.ResponseWriter, r *http.Request) {
		if !mlogged(r) {
			w.Write([]byte(`[{"error":true,"exception":{"errorcode":"servicerequireslogin"}}]`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var req []struct {
			M string `json:"methodname"`
		}
		json.Unmarshal(raw, &req)
		switch req[0].M {
		case "core_course_get_enrolled_courses_by_timeline_classification":
			w.Write([]byte(`[{"error":false,"data":{"courses":[{"id":5,"fullname":"ТФЯГ"}]}}]`))
		case "core_calendar_get_action_events_by_timesort":
			w.Write([]byte(`[{"error":false,"data":{"events":[{"id":1,"name":"Тест 2 закрывается","modulename":"quiz","timesort":1794740400,"url":"` + f.srv.URL + `/moodle/mod/quiz/view.php?id=900","course":{"id":5,"fullname":"ТФЯГ"}}]}}]`))
		}
	})
	mux.HandleFunc("/moodle/course/view.php", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<li class="activity modtype_assign" data-id="800"><div class="activityname"><a href="` + f.srv.URL + `/moodle/mod/assign/view.php?id=800"><span class="instancename">ДЗ 1</span></a></div></li>`))
	})
	mux.HandleFunc("/moodle/mod/assign/view.php", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Write([]byte(f.assignHTML))
	})
	mux.HandleFunc("/moodle/mod/quiz/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		h := f.quiz
		f.mu.Unlock()
		if h != nil {
			h.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/moodle/mod/quiz/view.php" {
			w.Write([]byte(`<div class="quizinfo"><p>Разрешено попыток: 1</p></div><div class="quizstartbuttondiv"></div>`))
			return
		}
		http.NotFound(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func setup(t *testing.T, f *fakeUni) (*Syncer, *store.Store, *[]string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := vault.Load("", filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	var mu sync.Mutex
	s := &Syncer{
		Store: st, Vault: v, Loc: samara, GroupID: 100, AdminID: 1,
		LKBase: f.srv.URL, MoodleBase: f.srv.URL + "/moodle",
		Notify: func(_ context.Context, id int64, text string) {
			mu.Lock()
			sent = append(sent, fmt.Sprintf("%d:%s", id, text))
			mu.Unlock()
		},
	}
	pageDelay = 0
	nowFn = func() time.Time { return time.Date(2026, 11, 13, 10, 0, 0, 0, samara) }
	t.Cleanup(func() { nowFn = time.Now })
	return s, st, &sent
}

func addAccount(t *testing.T, s *Syncer, id int64, kind, login, pass string) {
	t.Helper()
	sealed, _ := s.Vault.Seal(pass)
	if err := s.Store.PutAccount(model.Account{TelegramID: id, Kind: kind, Login: login, Secret: sealed}); err != nil {
		t.Fatal(err)
	}
}

func TestLessonsFromLK(t *testing.T) {
	f := newFake(t)
	s, st, _ := setup(t, f)
	if _, err := s.Lessons(context.Background()); err != ErrNoAccount {
		t.Fatalf("no accounts: %v", err)
	}
	addAccount(t, s, 2, model.AccountLK, "friend", "pw")
	addAccount(t, s, 1, model.AccountLK, "admin", "pw")
	addAccount(t, s, 3, model.AccountLK, "broken", "bad")

	ls, err := s.Lessons(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Группа берётся у админа (2 недели), ИОТ — у каждого свои; у «broken» вход не прошёл.
	var group, ietAdmin, ietFriend int
	for _, l := range ls {
		switch {
		case l.Owner == 0:
			group++
			if l.BBBURL != "https://bbb.ssau.ru/b/room-1" || !l.Online {
				t.Errorf("group lesson %+v", l)
			}
		case l.Owner == 1 && l.Discipline == "ИОТ admin":
			ietAdmin++
		case l.Owner == 2 && l.Discipline == "ИОТ friend":
			ietFriend++
		default:
			t.Errorf("unexpected %+v", l)
		}
	}
	if group != 2 || ietAdmin != 2 || ietFriend != 2 {
		t.Fatalf("group=%d iet admin=%d friend=%d", group, ietAdmin, ietFriend)
	}
	if ls[0].Date != "2026-11-13" { // неделя 11, пятница
		t.Fatalf("first date %s", ls[0].Date)
	}
	bad, _ := st.GetAccount(3, model.AccountLK)
	if bad.Status != model.AccountBadLogin {
		t.Fatalf("broken status %q", bad.Status)
	}
	okAcc, _ := st.GetAccount(1, model.AccountLK)
	if okAcc.Status != model.AccountOK || okAcc.Session == "" || okAcc.SyncedAt == nil {
		t.Fatalf("admin account %+v", okAcc)
	}
	// Второй прогон — по сохранённой сессии, без нового входа.
	before := f.lkLogins.Load()
	if _, err := s.Lessons(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.lkLogins.Load() != before {
		t.Fatalf("relogin despite saved session: %d -> %d", before, f.lkLogins.Load())
	}
}

func TestSyncMoodle(t *testing.T) {
	f := newFake(t)
	s, st, sent := setup(t, f)
	f.assignHTML = `<div data-region="activity-dates"><div><strong>Срок сдачи:</strong> пятница, 20 ноября 2026, 23:59</div></div>
<div class="automatic-completion-conditions"><span class="badge"><strong>Сделать:</strong> Отправить ответ</span></div>`
	addAccount(t, s, 1, model.AccountMoodle, "admin", "pw")
	acc, _ := st.GetAccount(1, model.AccountMoodle)
	if err := s.SyncMoodle(context.Background(), *acc, false); err != nil {
		t.Fatal(err)
	}
	items, _ := st.ListMoodleItems(1)
	if len(items) != 2 {
		t.Fatalf("items %+v", items)
	}
	byID := map[int64]model.MoodleItem{}
	for _, it := range items {
		byID[it.CMID] = it
	}
	a, q := byID[800], byID[900]
	if a.Kind != model.MoodleAssign || a.Due == nil || a.Due.In(samara).Format("02.01 15:04") != "20.11 23:59" || !strings.Contains(a.Todo, "Отправить ответ") || a.Course != "ТФЯГ" {
		t.Fatalf("assign %+v", a)
	}
	if q.Kind != model.MoodleQuiz || q.Due == nil || q.Title != "Тест 2" || q.Status != model.MoodleTodo {
		t.Fatalf("quiz (from timeline) %+v", q)
	}
	if len(*sent) != 0 {
		t.Fatalf("first sync must not announce: %v", *sent)
	}

	// Сдали задание — лёгкий синк перепроверяет его и видит статус.
	f.mu.Lock()
	f.assignHTML += `<table class="submissionstatustable"><tr><th>Состояние ответа на задание</th><td class="submissionstatussubmitted">Отправлено</td></tr></table>`
	f.mu.Unlock()
	acc, _ = st.GetAccount(1, model.AccountMoodle)
	if err := s.SyncMoodle(context.Background(), *acc, false); err != nil {
		t.Fatal(err)
	}
	items, _ = st.ListMoodleItems(1)
	for _, it := range items {
		if it.CMID == 800 && it.Status != model.MoodleSubmitted {
			t.Fatalf("status not refreshed: %+v", it)
		}
	}
	if f.mdLogins.Load() != 1 {
		t.Fatalf("moodle logins=%d, want 1 (session reused)", f.mdLogins.Load())
	}
}

func TestMoodleDue(t *testing.T) {
	s := &Syncer{}
	now := time.Now()
	old := now.Add(-time.Hour)
	fresh := now.Add(-time.Minute)
	cases := []struct {
		acc  model.Account
		want bool
	}{
		{model.Account{Status: model.AccountBadLogin}, false},
		{model.Account{Status: model.AccountPending}, true},
		{model.Account{Status: model.AccountOK, SyncedAt: &old}, true},
		{model.Account{Status: model.AccountOK, SyncedAt: &fresh}, false},
		{model.Account{Status: model.AccountError, UpdatedAt: fresh}, false},
		{model.Account{Status: model.AccountError, UpdatedAt: old}, true},
	}
	for i, c := range cases {
		if got := s.moodleDue(c.acc, now); got != c.want {
			t.Errorf("case %d: %v want %v", i, got, c.want)
		}
	}
}

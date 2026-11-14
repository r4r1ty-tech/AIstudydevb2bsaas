package lk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const loginPage = `<html><body><form action="" encType="multipart/form-data" method="POST">
<input type="hidden" name="$ACTION_REF_1"/>
<input type="hidden" name="$ACTION_1:0" value="{&quot;id&quot;:&quot;abc&quot;,&quot;bound&quot;:&quot;$@1&quot;}"/>
<input type="hidden" name="$ACTION_KEY" value="k1"/>
<input type="hidden" name="returnUrl" value=""/>
<input id="login" name="login"/><input id="password" type="password" name="password"/>
</form></body></html>`

func fakeLK(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/account/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(loginPage))
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart: %v", err)
		}
		if r.FormValue("$ACTION_KEY") != "k1" || !strings.Contains(r.FormValue("$ACTION_1:0"), `"id":"abc"`) {
			t.Errorf("server action fields lost: %v", r.MultipartForm.Value)
		}
		if r.FormValue("login") != "student" || r.FormValue("password") != "secret" {
			w.Write([]byte(`<html>[{"error":"Неверный логин и(или) пароль"}]</html>`))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "sess", Value: "ok", Path: "/", HttpOnly: true})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("home")) })
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie("sess"); err != nil || c.Value != "ok" {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/proxy/personal/groups", authed(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":531023239,"name":"6302-090301D"}]`))
	}))
	mux.HandleFunc("/api/proxy/statements/student/years", authed(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":14,"name":"2025-2026"},{"id":15,"name":"2026-2027"}]`))
	}))
	mux.HandleFunc("/api/proxy/timetable/get-timetable", authed(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("yearId") != "15" || r.URL.Query().Get("week") != "12" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		w.Write([]byte(timetableJSON))
	}))
	return httptest.NewServer(mux)
}

const timetableJSON = `{
 "currentYear":{"id":15,"startDate":"2026-08-31","weeks":52},
 "hasSession":false,
 "lessons":[
  {"id":1,"weekday":{"id":6},"time":{"beginTime":"17:00","endTime":"18:35"},
   "discipline":{"id":9,"name":"Статистический анализ данных"},"type":{"id":3,"name":"Практика"},
   "teachers":[{"id":1,"name":"Иванов Иван Иванович"}],
   "groups":[{"id":531023239,"name":"6302","subgroup":2}],
   "conference":{"id":5,"url":"https://bbb.ssau.ru/b/abc-def"},
   "weeks":[{"week":11,"isOnline":0,"room":{"name":"101"},"building":{"name":"5"}},{"week":12,"isOnline":1,"room":null,"building":null}]},
  {"id":2,"weekday":{"id":1},"time":{"beginTime":"09:45","endTime":"11:20"},
   "discipline":{"name":"Теория информации"},"type":{"name":"Лекция"},
   "teachers":[],"groups":[{"id":531023229,"subgroup":null},{"id":531023239,"subgroup":null}],
   "conference":null,
   "weeks":[{"week":12,"isOnline":0,"room":{"name":"531а"},"building":{"name":"5"}}]},
  {"id":3,"weekday":{"id":2},"time":{"beginTime":"08:00","endTime":"09:35"},
   "discipline":{"name":"Не на этой неделе"},"type":{"name":"Лекция"},"groups":[],
   "weeks":[{"week":11,"isOnline":0}]}
 ],
 "ietLessons":[
  {"id":9308,"weekday":{"id":3},"time":{"beginTime":"13:30","endTime":"15:05"},"type":{"name":"Практика"},
   "teachers":[{"name":"Петров П П"}],
   "flows":[{"subgroup":null,"discipline":{"name":"ДОП 1. Цифровая безопасность"},"loadType":{"name":"Практика"}}],
   "weeks":[{"week":12,"isOnline":0,"room":{"name":"531а"},"building":{"name":"5"}}]}
 ],
 "sfc":[]
}`

func TestLoginAndTimetable(t *testing.T) {
	srv := fakeLK(t)
	defer srv.Close()
	ctx := context.Background()

	c, _ := New(srv.URL)
	if err := c.Login(ctx, "student", "wrong"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("bad login: %v", err)
	}
	if err := c.Login(ctx, "student", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}
	saved := c.Session()
	if !strings.Contains(saved, "sess") {
		t.Fatalf("session not exported: %q", saved)
	}

	// Новая сессия из сохранённых кук — без повторного входа.
	c2, _ := New(srv.URL)
	if _, err := c2.Groups(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("no cookies must be 401: %v", err)
	}
	c2.Restore(saved)
	years, err := c2.Years(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Samara")
	yid, ok := CurrentYearID(years, time.Date(2026, 11, 13, 0, 0, 0, 0, loc))
	if !ok || yid != 15 {
		t.Fatalf("year %d %v", yid, ok)
	}
	tt, err := c2.Timetable(ctx, yid, 12, 531023239)
	if err != nil {
		t.Fatal(err)
	}
	ls, err := tt.ToLessons(12, 531023239, 777, loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 3 {
		raw, _ := json.MarshalIndent(ls, "", " ")
		t.Fatalf("lessons=%d %s", len(ls), raw)
	}
	stat := ls[0]
	// неделя 12 = 2026-08-31 + 77 дней (пн 16.11), суббота — 21.11
	if stat.Date != "2026-11-21" || stat.Start != "17:00" || !stat.Online || stat.Subgroup != 2 ||
		stat.BBBURL != "https://bbb.ssau.ru/b/abc-def" || stat.Place != "Online" || stat.Owner != 0 {
		t.Fatalf("stat lesson %+v", stat)
	}
	lec := ls[1]
	if lec.Date != "2026-11-16" || lec.Subgroup != 0 || lec.Online || lec.Place != "531а - 5 корпус" || lec.BBBURL != "" {
		t.Fatalf("lecture %+v", lec)
	}
	iet := ls[2]
	if iet.Owner != 777 || iet.Discipline != "ДОП 1. Цифровая безопасность" || iet.Date != "2026-11-18" || iet.Type != "Практика" {
		t.Fatalf("iet %+v", iet)
	}
	if again, _ := tt.ToLessons(12, 531023239, 0, loc); len(again) != 2 {
		t.Fatalf("owner=0 must skip IET: %d", len(again))
	}
}

func TestWeekOf(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Samara")
	for day, want := range map[string]int{"2026-08-31": 1, "2026-09-06": 1, "2026-09-07": 2, "2026-11-13": 11, "2026-11-16": 12, "2026-08-01": 1} {
		d, _ := time.ParseInLocation("2006-01-02", day, loc)
		got, err := WeekOf("2026-08-31", d.Add(15*time.Hour), loc)
		if err != nil || got != want {
			t.Errorf("%s: %d %v want %d", day, got, err, want)
		}
	}
}

func TestParseFullName(t *testing.T) {
	page := []byte(`self.__next_f.push([1,"3d:[\"$\",\"$L6a\",null,{\"name\":\"Алексей\",\"secondname\":\"Сергеевич\",\"fullName\":\"Белкин  Алексей Сергеевич\",\"avatarSrc\":\"\"}]"])`)
	if got, err := parseFullName(page); err != nil || got != "Белкин Алексей Сергеевич" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseFullName([]byte(`<form><input name="password"></form>`)); err != ErrUnauthorized {
		t.Fatalf("страница входа: %v", err)
	}
	if _, err := parseFullName([]byte(`<html></html>`)); err == nil {
		t.Fatal("без fullName нет ошибки")
	}
}

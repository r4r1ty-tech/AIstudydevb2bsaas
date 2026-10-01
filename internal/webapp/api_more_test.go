package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

const testPassword = "panel-secret"

func newServer(t *testing.T, refresh RefreshFunc) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(&config.Config{
		ListenAddr:    config.DefaultListen,
		Timezone:      config.DefaultTimezone,
		PanelPassword: testPassword,
		AdminID:       config.DefaultAdminID,
		GroupID:       config.DefaultGroupID,
		GroupCode:     config.DefaultGroupCode,
		Whitelist:     append([]int64(nil), config.DefaultWhitelist...),
	}, st, refresh, "")
	return s, st
}

func call(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("X-Panel-Password", testPassword)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func seedLesson(t *testing.T, st *store.Store, online bool) model.Lesson {
	t.Helper()
	now := time.Now().UTC()
	l := model.Lesson{
		Date: now.Format("2006-01-02"), Start: "10:00", End: "11:35",
		Begin: now.Add(-10 * time.Minute), Finish: now.Add(80 * time.Minute),
		Discipline: "Матан", Teacher: "Иванов", Online: online, Type: "Лекция",
	}
	if err := st.ReplaceLessons([]model.Lesson{l}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListLessons()
	if err != nil || len(got) != 1 {
		t.Fatalf("lessons: %v %v", got, err)
	}
	return got[0]
}

func TestHandleNow(t *testing.T) {
	s, st := newServer(t, nil)
	lesson := seedLesson(t, st, true)
	if err := st.SetLessonBBB(lesson.ID, "https://bbb.ssau.ru/b/room"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUser(&model.User{TelegramID: 1074442235, FIO: "Админ", Onboarded: true, Enabled: true, Subgroup: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPresence(model.Presence{TelegramID: 1074442235, LessonID: lesson.ID, State: model.PresenceRoom, Message: "join"}); err != nil {
		t.Fatal(err)
	}

	rec := call(t, s, http.MethodGet, "/api/now", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("now: %d %s", rec.Code, rec.Body.String())
	}
	var resp nowResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Current == nil || resp.Current.BBBURL != "https://bbb.ssau.ru/b/room" {
		t.Fatalf("current = %+v", resp.Current)
	}
	found := false
	for _, a := range resp.Accounts {
		if a.TelegramID == 1074442235 && a.InBBB == model.PresenceRoom {
			found = true
		}
	}
	if !found {
		t.Fatalf("accounts = %+v", resp.Accounts)
	}
}

func TestHandlePeopleAndPatch(t *testing.T) {
	s, st := newServer(t, nil)
	if err := st.UpsertUser(&model.User{TelegramID: 1074442235, FIO: "Старое", Onboarded: true, Enabled: true, Subgroup: 1}); err != nil {
		t.Fatal(err)
	}

	rec := call(t, s, http.MethodGet, "/api/people", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Старое") {
		t.Fatalf("people: %d %s", rec.Code, rec.Body.String())
	}

	rec = call(t, s, http.MethodPost, "/api/people/1074442235",
		`{"fio":"Новое Имя","subgroup":2,"extra_words":"лаба","enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	u, err := st.GetUser(1074442235)
	if err != nil || u == nil {
		t.Fatal(err)
	}
	if u.FIO != "Новое Имя" || u.Subgroup != 2 || u.Enabled {
		t.Fatalf("user = %+v", u)
	}

	rec = call(t, s, http.MethodPost, "/api/people/999", `{"fio":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
	rec = call(t, s, http.MethodPost, "/api/people/1074442235", `{bad json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", rec.Code)
	}
	rec = call(t, s, http.MethodPost, "/api/people/notanumber", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestHandlePeoplePatchDisableToday(t *testing.T) {
	s, st := newServer(t, nil)
	if err := st.UpsertUser(&model.User{TelegramID: 1074442235, FIO: "A", Onboarded: true, Enabled: true, Subgroup: 1}); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodPost, "/api/people/1074442235", `{"disable_today":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	u, _ := st.GetUser(1074442235)
	if u == nil || u.DisabledUntil == nil {
		t.Fatalf("disabled_until not set: %+v", u)
	}
}

func TestHandleLessonsAndBBB(t *testing.T) {
	s, st := newServer(t, nil)
	lesson := seedLesson(t, st, true)

	rec := call(t, s, http.MethodGet, "/api/lessons", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("lessons: %d", rec.Code)
	}

	rec = call(t, s, http.MethodPost, "/api/bbb",
		`{"lesson_id":`+jsonInt(lesson.ID)+`,"url":"https://bbb.ssau.ru/b/abc"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("bbb set: %d %s", rec.Code, rec.Body.String())
	}
	if got := st.GetLessonBBB(lesson.ID); got != "https://bbb.ssau.ru/b/abc" {
		t.Fatalf("stored = %q", got)
	}

	rec = call(t, s, http.MethodPost, "/api/bbb", `{"lesson_id":1,"url":"http://evil/b/x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad host: %d", rec.Code)
	}
	rec = call(t, s, http.MethodPost, "/api/bbb", `{"url":"https://bbb.ssau.ru/b/x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
}

func TestHandleParserAndRefresh(t *testing.T) {
	called := false
	s, st := newServer(t, func(context.Context) (model.ParseRun, error) {
		called = true
		return model.ParseRun{At: time.Now().UTC(), OK: true, LessonCount: 3}, nil
	})

	rec := call(t, s, http.MethodGet, "/api/parser", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("parser empty: %d", rec.Code)
	}

	rec = call(t, s, http.MethodPost, "/api/parser/refresh", "")
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("refresh: %d called=%v %s", rec.Code, called, rec.Body.String())
	}

	if err := st.SaveParseRun(model.ParseRun{At: time.Now().UTC(), OK: true, Status: "ok", LessonCount: 5}); err != nil {
		t.Fatal(err)
	}
	rec = call(t, s, http.MethodGet, "/api/parser", "")
	if !strings.Contains(rec.Body.String(), "\"lesson_count\":5") {
		t.Fatalf("parser: %s", rec.Body.String())
	}
}

func TestHandleLogs(t *testing.T) {
	s, st := newServer(t, nil)
	if err := st.AddEvent(model.Event{At: time.Now().UTC(), Type: model.EventJoin, Message: "x"}); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodGet, "/api/logs?limit=10", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "join") {
		t.Fatalf("logs: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, s, http.MethodGet, "/api/logs?limit=-1", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}
}

func TestHandleSettingsGet(t *testing.T) {
	s, _ := newServer(t, nil)
	rec := call(t, s, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"group_id":531023229`) {
		t.Fatalf("group from env expected: %s", rec.Body.String())
	}

	// Группа задаётся в env, запись из панели убрана.
	rec = call(t, s, http.MethodPost, "/api/settings", `{"group_id":42}`)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("settings post: %d", rec.Code)
	}
}

func TestHandleIndex(t *testing.T) {
	s, _ := newServer(t, nil)
	rec := call(t, s, http.MethodGet, "/", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("index: %d", rec.Code)
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestHandleLessonsShowsInheritedRoom(t *testing.T) {
	s, st := newServer(t, nil)
	lesson := seedLesson(t, st, true)
	if err := st.SetBBB(model.BBBRoomKey(lesson.Discipline, lesson.Teacher, lesson.Type), "https://bbb.ssau.ru/b/room"); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodGet, "/api/lessons", "")
	var resp lessonsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Lessons) != 1 || resp.Lessons[0].BBBURL != "https://bbb.ssau.ru/b/room" {
		t.Fatalf("lessons = %+v", resp.Lessons)
	}

	rec = call(t, s, http.MethodPost, "/api/bbb", `{"lesson_id":99999,"url":"https://bbb.ssau.ru/b/x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown lesson: %d %s", rec.Code, rec.Body.String())
	}
	if err := st.SetLessonBBB(lesson.ID, ""); err != nil {
		t.Fatal(err)
	}
	rec = call(t, s, http.MethodPost, "/api/bbb", `{"lesson_id":`+jsonInt(lesson.ID)+`,"url":"https://bbb.ssau.ru/b/new"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set: %d", rec.Code)
	}
	if b, _ := st.GetBBB(model.BBBRoomKey(lesson.Discipline, lesson.Teacher, lesson.Type)); b == nil || b.URL != "https://bbb.ssau.ru/b/new" {
		t.Fatalf("room not updated from panel: %+v", b)
	}
}

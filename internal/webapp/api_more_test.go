package webapp

import (
	"context"
	"encoding/json"
	"fmt"
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
		AdminID:       testAdminID,
		GroupID:       testGroupID,
		GroupCode:     testGroupCode,
		Whitelist:     append([]int64(nil), testWhitelist...),
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
	if err := st.UpsertUser(&model.User{TelegramID: 700000000, FIO: "Админ", Onboarded: true, Enabled: true, Subgroup: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPresence(model.Presence{TelegramID: 700000000, LessonID: lesson.ID, State: model.PresenceRoom, Message: "join"}); err != nil {
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
		if a.TelegramID == 700000000 && a.InBBB == model.PresenceRoom {
			found = true
		}
	}
	if !found {
		t.Fatalf("accounts = %+v", resp.Accounts)
	}
}

func TestHandlePeopleAndPatch(t *testing.T) {
	s, st := newServer(t, nil)
	if err := st.UpsertUser(&model.User{TelegramID: 700000000, FIO: "Старое", Onboarded: true, Enabled: true, Subgroup: 1}); err != nil {
		t.Fatal(err)
	}

	rec := call(t, s, http.MethodGet, "/api/people", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Старое") {
		t.Fatalf("people: %d %s", rec.Code, rec.Body.String())
	}

	rec = call(t, s, http.MethodPost, "/api/people/700000000",
		`{"fio":"Новое Имя","subgroup":2,"extra_words":"лаба","enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	u, err := st.GetUser(700000000)
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
	rec = call(t, s, http.MethodPost, "/api/people/700000000", `{bad json`)
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
	if err := st.UpsertUser(&model.User{TelegramID: 700000000, FIO: "A", Onboarded: true, Enabled: true, Subgroup: 1}); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodPost, "/api/people/700000000", `{"disable_today":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	u, _ := st.GetUser(700000000)
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
	if !strings.Contains(rec.Body.String(), `"group_id":100000001`) {
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

func TestLessonAndRoomOwnerIsolation(t *testing.T) {
	s, st, key, _ := newAdminServer(t)
	s.recordingsDir = t.TempDir()
	studentA := testWhitelist[1]
	studentB := testWhitelist[2]
	if err := st.UpsertUser(&model.User{TelegramID: studentA, FIO: "Студент А", Onboarded: true, Enabled: true, Subgroup: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUser(&model.User{TelegramID: studentB, FIO: "Студент Б", Onboarded: true, Enabled: true, Subgroup: 1}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().In(s.loc)
	today := now.Format("2006-01-02")
	begin := now.Add(time.Hour)
	finish := begin.Add(90 * time.Minute)
	if err := st.ReplaceLessons([]model.Lesson{
		{
			Date: today, Start: begin.Format("15:04"), End: finish.Format("15:04"),
			Begin: begin, Finish: finish,
			Discipline: "Общая лекция", Teacher: "Общий", Type: "Лекция", Online: true, Owner: 0,
		},
		{
			Date: today, Start: begin.Format("15:04"), End: finish.Format("15:04"),
			Begin: begin, Finish: finish,
			Discipline: "ИОТ Студента А", Teacher: "Препод А", Type: "Лекция", Online: true, Owner: studentA,
		},
		{
			Date: today, Start: begin.Format("15:04"), End: finish.Format("15:04"),
			Begin: begin, Finish: finish,
			Discipline: "ИОТ Студента Б", Teacher: "Препод Б", Type: "Лекция", Online: true, Owner: studentB,
		},
	}); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListLessons()
	if err != nil || len(all) != 3 {
		t.Fatalf("ListLessons: %v %+v", err, all)
	}
	var lessonShared, lessonA, lessonB model.Lesson
	for _, l := range all {
		switch l.Owner {
		case 0:
			lessonShared = l
		case studentA:
			lessonA = l
		case studentB:
			lessonB = l
		}
	}
	idA, idB := lessonA.ID, lessonB.ID
	if err := st.SetLessonBBB(idA, "https://bbb.ssau.ru/b/secret-a"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLessonBBB(idB, "https://bbb.ssau.ru/b/secret-b"); err != nil {
		t.Fatal(err)
	}

	packShared, err := st.EnsurePack(lessonShared, "https://bbb.ssau.ru/b/shared-rec", s.recordingsDir)
	if err != nil {
		t.Fatal(err)
	}
	packA, err := st.EnsurePack(lessonA, "https://bbb.ssau.ru/b/secret-a", s.recordingsDir)
	if err != nil {
		t.Fatal(err)
	}
	packB, err := st.EnsurePack(lessonB, "https://bbb.ssau.ru/b/secret-b", s.recordingsDir)
	if err != nil {
		t.Fatal(err)
	}

	// /api/lessons: студент А видит общую и свою пары, но не пару и не BBB студента Б.
	rec := reqAs(s, key, studentA, http.MethodGet, "/api/lessons", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/lessons: %d %s", rec.Code, rec.Body.String())
	}
	var lResp lessonsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lResp); err != nil {
		t.Fatal(err)
	}
	if len(lResp.Lessons) != 2 {
		t.Fatalf("ожидали 2 пары в /api/lessons, получили %d: %+v", len(lResp.Lessons), lResp.Lessons)
	}
	for _, l := range lResp.Lessons {
		if l.ID == idB || l.Owner == studentB {
			t.Fatalf("чужой ИОТ попал в /api/lessons: %+v", l)
		}
	}
	for _, b := range lResp.BBB {
		if b.Key == model.BBBLessonKey(idB) {
			t.Fatalf("чужая ссылка BBB попала в /api/lessons: %+v", b)
		}
	}

	// /api/rooms: нет комнаты чужого ИОТ.
	rec = reqAs(s, key, studentA, http.MethodGet, "/api/rooms", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/rooms: %d %s", rec.Code, rec.Body.String())
	}
	var rResp roomsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &rResp); err != nil {
		t.Fatal(err)
	}
	if len(rResp.Rooms) != 2 {
		t.Fatalf("ожидали 2 комнаты в /api/rooms, получили %d: %+v", len(rResp.Rooms), rResp.Rooms)
	}
	for _, rm := range rResp.Rooms {
		if rm.Discipline == "ИОТ Студента Б" {
			t.Fatalf("чужой ИОТ попал в /api/rooms: %+v", rm)
		}
	}

	// /api/day и /api/week: только общая и своя пары.
	rec = reqAs(s, key, studentA, http.MethodGet, "/api/day?date="+today, "")
	var dResp dayResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &dResp); err != nil {
		t.Fatal(err)
	}
	if len(dResp.Lessons) != 2 {
		t.Fatalf("ожидали 2 пары в /api/day, получили %d", len(dResp.Lessons))
	}
	for _, l := range dResp.Lessons {
		if l.ID == idB {
			t.Fatalf("чужой ИОТ попал в /api/day: %+v", l)
		}
	}

	rec = reqAs(s, key, studentA, http.MethodGet, "/api/week?from="+today, "")
	var wResp weekResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &wResp); err != nil {
		t.Fatal(err)
	}
	for _, d := range wResp.Days {
		for _, l := range d.Lessons {
			if l.ID == idB {
				t.Fatalf("чужой ИОТ попал в /api/week: %+v", l)
			}
		}
	}

	// Чужой lesson_id нельзя трогать через /api/intent и /api/bbb (в т.ч. через key:"lesson:<id>" и чужую комнату).
	if c := reqAs(s, key, studentA, http.MethodPost, "/api/intent", fmt.Sprintf(`{"lesson_id":%d,"decision":"yes"}`, idB)).Code; c != http.StatusNotFound {
		t.Fatalf("/api/intent для чужого ИОТ: %d, ждали 404", c)
	}
	if c := reqAs(s, key, studentA, http.MethodPost, "/api/bbb", fmt.Sprintf(`{"lesson_id":%d,"url":"https://bbb.ssau.ru/b/x"}`, idB)).Code; c != http.StatusNotFound {
		t.Fatalf("/api/bbb для чужого ИОТ: %d, ждали 404", c)
	}
	if c := reqAs(s, key, studentA, http.MethodPost, "/api/bbb", fmt.Sprintf(`{"lesson_id":0,"key":"lesson:%d","url":"https://bbb.ssau.ru/b/x"}`, idB)).Code; c != http.StatusForbidden {
		t.Fatalf("/api/bbb key=lesson:<idB> для чужого ИОТ: %d, ждали 403", c)
	}
	if c := reqAs(s, key, studentA, http.MethodPost, "/api/bbb", fmt.Sprintf(`{"lesson_id":0,"key":%q,"url":"https://bbb.ssau.ru/b/x"}`, model.BBBRoomKey(lessonB.Discipline, lessonB.Teacher, lessonB.Type))).Code; c != http.StatusForbidden {
		t.Fatalf("/api/bbb чужой room key: %d, ждали 403", c)
	}
	if c := reqAs(s, key, studentA, http.MethodPost, "/api/bbb", fmt.Sprintf(`{"lesson_id":0,"key":%q,"url":"https://bbb.ssau.ru/b/shared-room"}`, model.BBBRoomKey(lessonShared.Discipline, lessonShared.Teacher, lessonShared.Type))).Code; c != http.StatusOK {
		t.Fatalf("/api/bbb общий room key: %d, ждали 200", c)
	}
	if c := reqAs(s, key, studentA, http.MethodPost, "/api/bbb", fmt.Sprintf(`{"lesson_id":0,"key":"lesson:%d","url":"https://bbb.ssau.ru/b/own-a"}`, idA)).Code; c != http.StatusOK {
		t.Fatalf("/api/bbb свой key=lesson:<idA>: %d, ждали 200", c)
	}

	// /api/lessons: recordings не содержит пак чужого ИОТ.
	if len(lResp.Recordings) != 2 {
		t.Fatalf("ожидали 2 записи в /api/lessons, получили %d: %+v", len(lResp.Recordings), lResp.Recordings)
	}
	for _, r := range lResp.Recordings {
		if r.Rel == packB.Dir {
			t.Fatalf("пак чужого ИОТ попал в /api/lessons: %+v", r)
		}
	}
	_ = packShared
	_ = packA

	// /api/notes: студент А видит общий и свой паки, но не пак студента Б.
	checkNotesNoB := func(stage string) {
		t.Helper()
		rec := reqAs(s, key, studentA, http.MethodGet, "/api/notes", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/notes (%s): %d %s", stage, rec.Code, rec.Body.String())
		}
		var nResp notesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &nResp); err != nil {
			t.Fatal(err)
		}
		if nResp.Total != 2 {
			t.Fatalf("/api/notes (%s): ожидали 2 пака, получили %d: %+v", stage, nResp.Total, nResp.Subjects)
		}
		for _, subj := range nResp.Subjects {
			if subj.Discipline == lessonB.Discipline {
				t.Fatalf("/api/notes (%s): чужой ИОТ-пак попал в выдачу: %+v", stage, subj)
			}
		}
	}
	checkNotesNoB("active")

	// Алиас в pack_lessons и удаление ИОТ-урока (pruneLesson) не должны открывать чужой пак.
	if err := st.BindLessonToPack(888001, packB.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceLessons([]model.Lesson{lessonShared, lessonA}); err != nil {
		t.Fatal(err)
	}
	checkNotesNoB("after prune + pack_lessons alias")

	rec = reqAs(s, key, studentA, http.MethodGet, "/api/lessons", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/lessons after prune: %d %s", rec.Code, rec.Body.String())
	}
	var lAfter lessonsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &lAfter); err != nil {
		t.Fatal(err)
	}
	for _, r := range lAfter.Recordings {
		if r.Rel == packB.Dir {
			t.Fatalf("чужой ИОТ-пак после prune попал в /api/lessons: %+v", r)
		}
	}

	// Админ по-прежнему видит все 3 пака в /api/notes.
	rec = reqAs(s, key, testAdminID, http.MethodGet, "/api/notes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/notes admin: %d %s", rec.Code, rec.Body.String())
	}
	var nAdmin notesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &nAdmin); err != nil {
		t.Fatal(err)
	}
	if nAdmin.Total != 3 {
		t.Fatalf("/api/notes admin: ожидали 3 пака, получили %d", nAdmin.Total)
	}
}

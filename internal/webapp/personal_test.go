package webapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// demoCall ходит в демо-панель без пароля: в демо вход в API открыт.
func demoCall(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func newDemoServer(t *testing.T) *Server {
	t.Helper()
	return NewDemo(&config.Config{ListenAddr: config.DefaultListen, Timezone: config.DefaultTimezone})
}

func getDay(t *testing.T, call func(method, path, body string) *httptest.ResponseRecorder) dayResponse {
	t.Helper()
	rec := call(http.MethodGet, "/api/day", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/day: %d %s", rec.Code, rec.Body)
	}
	var day dayResponse
	decodeBody(t, rec, &day)
	return day
}

// liveOnline — онлайн-пары дня, которые бот ещё может затронуть: моя подгруппа, не прошли.
func liveOnline(day dayResponse) []lessonView {
	var out []lessonView
	for _, l := range day.Lessons {
		if l.Online && l.Mine && l.Phase != phasePast {
			out = append(out, l)
		}
	}
	return out
}

func decisionByID(day dayResponse, id int64) string {
	for _, l := range day.Lessons {
		if l.ID == id {
			return l.Decision
		}
	}
	return ""
}

func TestDemoReadEndpoints(t *testing.T) {
	s := newDemoServer(t)
	for _, p := range []string{"/api/day", "/api/week", "/api/deadlines", "/api/notes", "/api/me"} {
		rec := demoCall(t, s, http.MethodGet, p, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", p, rec.Code, rec.Body)
		}
	}

	var day dayResponse
	decodeBody(t, demoCall(t, s, http.MethodGet, "/api/day", ""), &day)
	if !day.Demo || len(day.Lessons) == 0 {
		t.Fatalf("day: demo=%v lessons=%d", day.Demo, len(day.Lessons))
	}

	var dl deadlinesResponse
	decodeBody(t, demoCall(t, s, http.MethodGet, "/api/deadlines", ""), &dl)
	if len(dl.Deadlines) == 0 || !dl.Moodle.Connected {
		t.Fatalf("deadlines: connected=%v n=%d", dl.Moodle.Connected, len(dl.Deadlines))
	}
}

func TestDemoIntentChangesDay(t *testing.T) {
	s := newDemoServer(t)
	call := func(m, p, b string) *httptest.ResponseRecorder { return demoCall(t, s, m, p, b) }
	live := liveOnline(getDay(t, call))
	if len(live) == 0 {
		t.Fatal("в демо нет онлайн-пар впереди")
	}
	id := live[0].ID

	for _, dec := range []string{decisionYes, decisionNo, decisionAuto} {
		body, _ := json.Marshal(map[string]any{"lesson_id": id, "decision": dec})
		rec := call(http.MethodPost, "/api/intent", string(body))
		if rec.Code != http.StatusOK {
			t.Fatalf("intent %s: %d %s", dec, rec.Code, rec.Body)
		}
		var resp intentResponse
		decodeBody(t, rec, &resp)
		if resp.Lesson.Decision != dec {
			t.Fatalf("intent response %s: decision=%q", dec, resp.Lesson.Decision)
		}
		if got := decisionByID(getDay(t, call), id); got != dec {
			t.Fatalf("after %s GET /api/day shows %q", dec, got)
		}
	}

	rec := call(http.MethodPost, "/api/intent", `{"lesson_id":`+jsonInt(id)+`,"decision":"maybe"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad decision: %d %s", rec.Code, rec.Body)
	}
}

func TestDemoDayMode(t *testing.T) {
	s := newDemoServer(t)
	call := func(m, p, b string) *httptest.ResponseRecorder { return demoCall(t, s, m, p, b) }
	checkDayMode(t, call)
}

// checkDayMode гоняет none -> all -> auto и проверяет решения по живым онлайн-парам.
func checkDayMode(t *testing.T, call func(method, path, body string) *httptest.ResponseRecorder) {
	t.Helper()
	for _, c := range []struct{ mode, want, wantMode string }{
		{"none", decisionNo, dayModeNone},
		{"all", decisionYes, dayModeAll},
		{"auto", decisionAuto, dayModeAuto},
	} {
		rec := call(http.MethodPost, "/api/day", `{"mode":"`+c.mode+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("mode %s: %d %s", c.mode, rec.Code, rec.Body)
		}
		var resp dayResponse
		decodeBody(t, rec, &resp)
		if resp.Mode != c.wantMode {
			t.Fatalf("mode %s: day.mode=%q, want %q", c.mode, resp.Mode, c.wantMode)
		}
		live := liveOnline(getDay(t, call))
		if len(live) == 0 {
			t.Fatal("нет онлайн-пар впереди")
		}
		for _, l := range live {
			if l.Decision != c.want {
				t.Fatalf("mode %s: lesson %d decision=%q, want %q", c.mode, l.ID, l.Decision, c.want)
			}
		}
	}
	if rec := call(http.MethodPost, "/api/day", `{"mode":"weird"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad mode: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/api/day", `{"mode":"all","date":"31-12-2026"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad date: %d %s", rec.Code, rec.Body)
	}
}

// seedMyLessons кладёт в базу две онлайн-пары на сегодня (по часам панели):
// идущую и ту, что начнётся позже.
func seedMyLessons(t *testing.T, s *Server) []model.Lesson {
	t.Helper()
	now := s.now()
	date := now.Format(dateLayout)
	mk := func(begin, finish time.Time, subj string) model.Lesson {
		return model.Lesson{
			Date: date, Start: begin.Format("15:04"), End: finish.Format("15:04"),
			Begin: begin.UTC(), Finish: finish.UTC(), Discipline: subj, Teacher: "Иванов",
			Online: true, Type: "Лекция",
		}
	}
	return []model.Lesson{
		mk(now.Add(-10*time.Minute), now.Add(80*time.Minute), "Матан"),
		mk(now.Add(2*time.Hour), now.Add(3*time.Hour+30*time.Minute), "Физика"),
	}
}

func TestPersonalIntentWritesJoinIntents(t *testing.T) {
	s, st := newServer(t, nil)
	if err := st.ReplaceLessons(seedMyLessons(t, s)); err != nil {
		t.Fatal(err)
	}
	lessons, err := st.ListLessons()
	if err != nil || len(lessons) != 2 {
		t.Fatalf("lessons: %v %v", lessons, err)
	}
	id := lessons[0].ID
	admin := s.cfg.AdminID

	for _, c := range []struct {
		dec  string
		want model.JoinDecision
	}{
		{decisionYes, model.JoinYes},
		{decisionNo, model.JoinNo},
		{decisionAuto, model.JoinPending},
	} {
		rec := call(t, s, http.MethodPost, "/api/intent", `{"lesson_id":`+jsonInt(id)+`,"decision":"`+c.dec+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("intent %s: %d %s", c.dec, rec.Code, rec.Body)
		}
		in, err := st.GetIntent(admin, id)
		if err != nil || in == nil {
			t.Fatalf("intent %s: GetIntent = %v %v", c.dec, in, err)
		}
		if in.Decision != c.want {
			t.Fatalf("intent %s: decision=%q, want %q", c.dec, in.Decision, c.want)
		}
	}

	rec := call(t, s, http.MethodPost, "/api/intent", `{"lesson_id":`+jsonInt(id)+`,"decision":"maybe"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad decision: %d", rec.Code)
	}
	if rec := call(t, s, http.MethodPost, "/api/intent", `{"lesson_id":9999,"decision":"yes"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown lesson: %d", rec.Code)
	}
}

func TestPersonalDayModeOnStore(t *testing.T) {
	s, st := newServer(t, nil)
	if err := st.ReplaceLessons(seedMyLessons(t, s)); err != nil {
		t.Fatal(err)
	}
	checkDayMode(t, func(m, p, b string) *httptest.ResponseRecorder { return call(t, s, m, p, b) })
}

func TestPersonalDeadlinesAndMoodleStub(t *testing.T) {
	s, _ := newServer(t, nil)

	rec := call(t, s, http.MethodGet, "/api/deadlines", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deadlines: %d %s", rec.Code, rec.Body)
	}
	var dl deadlinesResponse
	decodeBody(t, rec, &dl)
	if len(dl.Deadlines) != 0 || dl.Moodle.Connected {
		t.Fatalf("deadlines: connected=%v n=%d", dl.Moodle.Connected, len(dl.Deadlines))
	}
	if !strings.Contains(rec.Body.String(), `"deadlines":[]`) {
		t.Fatalf("deadlines должен быть [], а не null: %s", rec.Body)
	}

	const secret = "Sup3r-Secret-Pass"
	rec = call(t, s, http.MethodPost, "/api/moodle/login", `{"login":"student","password":"`+secret+`"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("moodle login: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("пароль попал в ответ: %s", rec.Body)
	}
}

func TestApiAccessRule(t *testing.T) {
	mk := func(password, allowIPs string) *Server {
		return New(&config.Config{
			ListenAddr:    config.DefaultListen,
			Timezone:      config.DefaultTimezone,
			PanelPassword: password,
			PanelAllowIPs: allowIPs,
		}, nil, nil, "")
	}
	get := func(s *Server, password string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
		r.RemoteAddr = "192.0.2.10:5555"
		if password != "" {
			r.Header.Set("X-Panel-Password", password)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		return rec.Code
	}

	if got := get(mk("pw", ""), "pw"); got != http.StatusOK {
		t.Errorf("верный пароль: %d", got)
	}
	if got := get(mk("pw", ""), "nope"); got != http.StatusUnauthorized {
		t.Errorf("неверный пароль: %d", got)
	}
	if got := get(mk("pw", ""), ""); got != http.StatusUnauthorized {
		t.Errorf("пароль не передан: %d", got)
	}
	if got := get(mk("", "192.0.2.0/24"), ""); got != http.StatusOK {
		t.Errorf("без пароля, IP в списке: %d", got)
	}
	if got := get(mk("", "198.51.100.0/24"), ""); got != http.StatusForbidden {
		t.Errorf("без пароля, IP вне списка: %d", got)
	}
	if got := get(mk("", ""), ""); got != http.StatusUnauthorized {
		t.Errorf("ни пароля, ни списка, не демо: %d", got)
	}
}

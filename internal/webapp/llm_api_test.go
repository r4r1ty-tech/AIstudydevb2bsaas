package webapp

import (
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
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/panelpass"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/quizlog"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

func newAdminServer(t *testing.T) (*Server, *store.Store, []byte, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bot.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	keyFile := filepath.Join(dir, "secret.key")
	s := New(&config.Config{
		ListenAddr: config.DefaultListen, Timezone: config.DefaultTimezone,
		AdminID: config.DefaultAdminID, Whitelist: append([]int64(nil), config.DefaultWhitelist...),
		SecretKeyFile: keyFile, DBPath: dbPath, AdminLoginSuffix: "528",
		GeminiAPIKeys: []string{"Медведев:g1secret", "Пушкин:g2secret"}, OpenCodeAPIKey: "oc",
		LLMAPIKey: "ch", LLMAPIURL: "https://cheap/v1", LLMModel: "glm-5.3-flash",
	}, st, nil, "")
	v, err := vault.Load("", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return s, st, v.Derive(panelpass.Label), dbPath
}

func reqAs(s *Server, key []byte, id int64, method, path, body string) *httptest.ResponseRecorder {
	pw, _ := panelpass.Issue(key, id, time.Now())
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Panel-Password", pw)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func TestAdminByLoginSuffix(t *testing.T) {
	s, st, key, _ := newAdminServer(t)
	friend := config.DefaultWhitelist[1]
	if c := reqAs(s, key, friend, http.MethodGet, "/api/admin/llm/people", "").Code; c != http.StatusForbidden {
		t.Fatalf("без логина на 528: %d, ждали 403", c)
	}
	if err := st.PutAccount(model.Account{TelegramID: friend, Kind: model.AccountMoodle, Login: "2021-000123"}); err != nil {
		t.Fatal(err)
	}
	var set settingsResponse
	_ = json.Unmarshal(reqAs(s, key, friend, http.MethodGet, "/api/settings", "").Body.Bytes(), &set)
	if !set.IsAdmin {
		t.Fatal("логин на 528 не дал админа")
	}
	if c := reqAs(s, key, friend, http.MethodGet, "/api/admin/llm/people", "").Code; c != http.StatusOK {
		t.Fatalf("/api/admin/llm/people: %d", c)
	}
}

func TestLLMChoiceRoundTrip(t *testing.T) {
	s, _, key, _ := newAdminServer(t)
	admin := config.DefaultAdminID
	friend := config.DefaultWhitelist[1]

	var st llmState
	_ = json.Unmarshal(reqAs(s, key, admin, http.MethodGet, "/api/llm", "").Body.Bytes(), &st)
	if len(st.Providers) != 3 || !st.Admin || !strings.HasPrefix(st.Chain[0], "gemini/") {
		t.Fatalf("админ по умолчанию: %+v", st)
	}
	if k := st.Providers[0].KeyInfo; len(k) != 2 || k[0].Label != "Медведев" || k[0].Tail == "" {
		t.Fatalf("ключи у админа: %+v", k)
	}

	// друг: cheaper не видит, хвостов ключей не видит, но выбирает ключ сам
	rec := reqAs(s, key, friend, http.MethodGet, "/api/llm", "")
	st = llmState{}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if len(st.Providers) != 2 || st.Admin || st.Providers[0].KeyInfo[1].Tail != "" || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("друг: %s", rec.Body)
	}
	if c := reqAs(s, key, friend, http.MethodPost, "/api/llm/choice", `{"provider":"cheaper"}`).Code; c != http.StatusBadRequest {
		t.Fatalf("друг выбрал cheaper: %d", c)
	}
	rec = reqAs(s, key, friend, http.MethodPost, "/api/llm/choice", `{"provider":"gemini","model":"gemini-3.6-flash","key":2}`)
	st = llmState{}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != http.StatusOK || st.Chain[0] != "gemini/gemini-3.6-flash#2(Пушкин)" || st.Chain[1] != "gemini/gemini-3.6-flash#1(Медведев)" {
		t.Fatalf("выбор друга: %d %+v", rec.Code, st.Chain[:2])
	}
	if c := reqAs(s, key, friend, http.MethodPost, "/api/llm/choice", `{"provider":"gemini","key":9}`).Code; c != http.StatusBadRequest {
		t.Fatalf("несуществующий ключ: %d", c)
	}

	// админ: cheaper можно
	rec = reqAs(s, key, admin, http.MethodPost, "/api/llm/choice", `{"provider":"cheaper","model":"kimi-k3"}`)
	st = llmState{}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if !strings.HasPrefix(st.Chain[0], "cheaper/kimi-k3#1") || !strings.HasPrefix(st.Default[0], "gemini/") {
		t.Fatalf("админ cheaper: %+v", st.Chain[:2])
	}

	// админ видит выбор всех
	var people struct{ People []personLLM }
	_ = json.Unmarshal(reqAs(s, key, admin, http.MethodGet, "/api/admin/llm/people", "").Body.Bytes(), &people)
	found := false
	for _, p := range people.People {
		if p.TelegramID == friend && p.Choice.Key == 2 && strings.Contains(p.First, "Пушкин") {
			found = true
		}
	}
	if !found {
		t.Fatalf("в списке людей нет выбора друга: %+v", people.People)
	}
}

func TestQuizRunsAndResolve(t *testing.T) {
	s, st, key, dbPath := newAdminServer(t)
	admin := config.DefaultAdminID
	if err := quizlog.Write(quizlog.Dir(dbPath), &quizlog.Run{TelegramID: admin, CMID: 777, Status: quizlog.Failed,
		Pages: []quizlog.Page{{Raw: "сырой ответ"}}}); err != nil {
		t.Fatal(err)
	}
	var list struct{ Runs []quizlog.Run }
	_ = json.Unmarshal(reqAs(s, key, admin, http.MethodGet, "/api/admin/quiz-runs", "").Body.Bytes(), &list)
	if len(list.Runs) != 1 || list.Runs[0].Pages[0].Raw != "" {
		t.Fatalf("список: %+v", list.Runs)
	}
	var full quizlog.Run
	_ = json.Unmarshal(reqAs(s, key, admin, http.MethodGet, "/api/admin/quiz-runs/"+list.Runs[0].Name, "").Body.Bytes(), &full)
	if full.Pages[0].Raw != "сырой ответ" {
		t.Fatalf("целиком: %+v", full)
	}
	if c := reqAs(s, key, admin, http.MethodGet, "/api/admin/quiz-runs/..%2Fbot.db", "").Code; c != http.StatusNotFound {
		t.Fatalf("выход из папки: %d", c)
	}
	_ = st.SetSetting("quiz_solved:700000000:777", "x")
	if c := reqAs(s, key, admin, http.MethodPost, "/api/admin/quiz/resolve", `{"cmid":777}`).Code; c != http.StatusOK {
		t.Fatalf("resolve: %d", c)
	}
	if v, _, _ := st.GetSetting("quiz_solved:700000000:777"); v != "" {
		t.Fatal("отметка «решён» не снята")
	}
	if v, _, _ := st.GetSetting("quiz_force:700000000:777"); v == "" {
		t.Fatal("нет флага «пройти»")
	}
	if v, _, _ := st.GetSetting("moodle_kick:700000000"); v == "" {
		t.Fatal("нет пинка синка")
	}
	// «Пройти» доступно любому — для своего теста
	friend := config.DefaultWhitelist[1]
	if c := reqAs(s, key, friend, http.MethodPost, "/api/quiz/force", `{"cmid":55}`).Code; c != http.StatusOK {
		t.Fatalf("force: %d", c)
	}
	if v, _, _ := st.GetSetting(fmt.Sprintf("quiz_force:%d:55", friend)); v == "" {
		t.Fatal("нет флага «пройти» у друга")
	}
	if c := reqAs(s, key, friend, http.MethodPost, "/api/admin/quiz/resolve", `{"cmid":55}`).Code; c != http.StatusForbidden {
		t.Fatalf("друг перерешивает чужое: %d", c)
	}
}

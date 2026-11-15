package webapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
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
		SecretKeyFile: keyFile, DBPath: dbPath,
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
		t.Fatalf("обычный пользователь: %d, ждали 403", c)
	}
	if err := st.PutAccount(model.Account{TelegramID: friend, Kind: model.AccountMoodle, Login: "2021-000123"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAccount(model.Account{TelegramID: friend, Kind: model.AccountLK, Login: "2021-000123"}); err != nil {
		t.Fatal(err)
	}
	var set settingsResponse
	_ = json.Unmarshal(reqAs(s, key, friend, http.MethodGet, "/api/settings", "").Body.Bytes(), &set)
	if set.IsAdmin {
		t.Fatal("суффикс логина 528 не должен давать права админа")
	}
	if c := reqAs(s, key, friend, http.MethodGet, "/api/admin/llm/people", "").Code; c != http.StatusForbidden {
		t.Fatalf("/api/admin/llm/people при логине на 528: %d, ждали 403", c)
	}
	var stLLM llmState
	_ = json.Unmarshal(reqAs(s, key, friend, http.MethodGet, "/api/llm", "").Body.Bytes(), &stLLM)
	if stLLM.Admin {
		t.Fatal("/api/llm Admin=true для логина на 528")
	}
	for _, p := range stLLM.Providers {
		if p.ID == llm.Cheaper {
			t.Fatalf("не-админ с логином 528 видит cheaper: %+v", stLLM.Providers)
		}
	}
	for _, c := range stLLM.Chain {
		if strings.HasPrefix(c, llm.Cheaper+"/") {
			t.Fatalf("не-админ с логином 528 получил cheaper в цепочке: %v", stLLM.Chain)
		}
	}
	if c := reqAs(s, key, friend, http.MethodPost, "/api/llm/choice", `{"provider":"cheaper"}`).Code; c != http.StatusBadRequest {
		t.Fatalf("выбор cheaper при логине 528: %d, ждали 400", c)
	}
	if c := reqAs(s, key, config.DefaultAdminID, http.MethodGet, "/api/admin/llm/people", "").Code; c != http.StatusOK {
		t.Fatalf("настоящий AdminID: %d, ждали 200", c)
	}
}

func TestLLMChoiceRoundTrip(t *testing.T) {
	s, store, key, _ := newAdminServer(t)
	admin := config.DefaultAdminID
	friend := config.DefaultWhitelist[1]
	// ключи подписаны фамилией: у друга Пушкина свой ключ gemini #2
	_ = store.SetSetting(fmt.Sprintf("lk_name:%d", friend), "Пушкин Иван Петрович")

	var st llmState
	_ = json.Unmarshal(reqAs(s, key, admin, http.MethodGet, "/api/llm", "").Body.Bytes(), &st)
	// админ без выбора — сразу cheaper, потом все ключи
	if len(st.Providers) != 3 || !st.Admin || !strings.HasPrefix(st.Chain[0], "cheaper/") || !strings.HasPrefix(st.Chain[1], "gemini/") {
		t.Fatalf("админ по умолчанию: %+v", st)
	}
	if k := st.Providers[0].KeyInfo; len(k) != 2 || k[0].Label != "Медведев" || k[0].Tail == "" {
		t.Fatalf("ключи у админа: %+v", k)
	}

	// друг: cheaper и чужие ключи не видит, хвостов нет — только свой ключ
	rec := reqAs(s, key, friend, http.MethodGet, "/api/llm", "")
	st = llmState{}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if len(st.Providers) != 1 || st.Admin || len(st.Providers[0].KeyInfo) != 1 || st.Providers[0].KeyInfo[0].N != 2 ||
		st.Providers[0].KeyInfo[0].Tail != "" || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "Медведев\"") {
		t.Fatalf("друг: %s", rec.Body)
	}
	for _, c := range st.Chain {
		if !strings.Contains(c, "(Пушкин)") {
			t.Fatalf("в цепочке друга чужой ключ: %v", st.Chain)
		}
	}
	if c := reqAs(s, key, friend, http.MethodPost, "/api/llm/choice", `{"provider":"cheaper"}`).Code; c != http.StatusBadRequest {
		t.Fatalf("друг выбрал cheaper: %d", c)
	}
	rec = reqAs(s, key, friend, http.MethodPost, "/api/llm/choice", `{"provider":"gemini","model":"gemini-3.6-flash","key":2}`)
	st = llmState{}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != http.StatusOK || st.Chain[0] != "gemini/gemini-3.6-flash#2(Пушкин)" || st.Chain[1] != "gemini/gemini-4-hydrogen#2(Пушкин)" || len(st.Chain) != 3 {
		t.Fatalf("выбор друга: %d %+v", rec.Code, st.Chain)
	}
	if c := reqAs(s, key, friend, http.MethodPost, "/api/llm/choice", `{"provider":"gemini","key":1}`).Code; c != http.StatusForbidden {
		t.Fatalf("чужой ключ: %d", c)
	}
	if c := reqAs(s, key, friend, http.MethodPost, "/api/llm/choice", `{"provider":"opencode"}`).Code; c != http.StatusForbidden {
		t.Fatalf("провайдер без своих ключей: %d", c)
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

func TestQuizForceAll(t *testing.T) {
	s, st, key, _ := newAdminServer(t)
	admin := config.DefaultAdminID
	friend := config.DefaultWhitelist[1]
	if err := st.PutAccount(model.Account{TelegramID: friend, Kind: model.AccountMoodle, Login: "2021-000123", Status: model.AccountOK}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	day := 24 * time.Hour
	items := []model.MoodleItem{
		{CMID: 101, Kind: model.MoodleQuiz, Title: "В работе", Due: at(2 * day), Status: model.MoodleTodo},
		{CMID: 102, Kind: model.MoodleQuiz, Title: "Незавершённая попытка", Status: model.MoodleDraft},
		{CMID: 103, Kind: model.MoodleQuiz, Title: "Сдан", Due: at(-day), Status: model.MoodleSubmitted},
		{CMID: 104, Kind: model.MoodleAssign, Title: "Задание, не тест", Due: at(day), Status: model.MoodleTodo},
		{CMID: 105, Kind: model.MoodleQuiz, Title: "Прошлый год, вне окна", Due: at(-200 * day), Status: model.MoodleTodo},
	}
	if err := st.ReplaceMoodleItems(friend, items); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(fmt.Sprintf("quiz_solved:%d:101", friend), "x"); err != nil {
		t.Fatal(err)
	}

	var got struct {
		OK    bool `json:"ok"`
		Count int  `json:"count"`
	}
	rec := reqAs(s, key, friend, http.MethodPost, "/api/quiz/force-all", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Count != 2 {
		t.Fatalf("force-all: %d %s", rec.Code, rec.Body)
	}
	for _, cmid := range []int64{101, 102} {
		if v, _, _ := st.GetSetting(fmt.Sprintf("quiz_force:%d:%d", friend, cmid)); v == "" {
			t.Errorf("нет флага у cmid %d", cmid)
		}
	}
	for _, cmid := range []int64{103, 104, 105} {
		if v, _, _ := st.GetSetting(fmt.Sprintf("quiz_force:%d:%d", friend, cmid)); v != "" {
			t.Errorf("лишний флаг у cmid %d", cmid)
		}
	}
	if v, _, _ := st.GetSetting(fmt.Sprintf("quiz_solved:%d:101", friend)); v != "" {
		t.Error("отметка «решён» не снята")
	}
	if v, _, _ := st.GetSetting(fmt.Sprintf("moodle_kick:%d", friend)); v == "" {
		t.Error("нет пинка синка")
	}

	// у админа Moodle не подключён: ноль и без пинка
	got = struct {
		OK    bool `json:"ok"`
		Count int  `json:"count"`
	}{}
	rec = reqAs(s, key, admin, http.MethodPost, "/api/quiz/force-all", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Count != 0 {
		t.Fatalf("без Moodle: %d %s", rec.Code, rec.Body)
	}
	if v, _, _ := st.GetSetting(fmt.Sprintf("moodle_kick:%d", admin)); v != "" {
		t.Error("пинок без постановки")
	}
}

func TestDemoHidesRealKeysAndBlocksLLMProbes(t *testing.T) {
	fullCfg := &config.Config{
		ListenAddr:       config.DefaultListen,
		Timezone:         config.DefaultTimezone,
		BotToken:         "real-bot-token",
		PanelPassword:    "real-panel-pass",
		LLMAPIKey:        "real-cheap-key",
		LLMAPIURL:        "https://real-cheap.example/v1",
		LLMModel:         "real-model",
		GeminiAPIKeys:    []string{"Медведев:real-gem-1"},
		OpenCodeAPIKey:   "real-oc-1",
		OpenCodeAPIKeys:  []string{"real-oc-2"},
		AssemblyAPIKey:   "real-asm",
		FishStudioAPIKey: "real-fish",
		GrokAPIKey:       "real-grok",
		GroqAPIKey:       "real-groq",
		GitHubToken:      "real-gh",
		SecretKey:        "real-sec",
		SecretKeyFile:    "/tmp/real-secret.key",
		TavilyAPIKey:     "real-tavily",
		ExaAPIKey:        "real-exa",
		DBPath:           "/tmp/real.db",
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	origGem, origOC := llm.GeminiURL, llm.OpenCodeURL
	llm.GeminiURL, llm.OpenCodeURL = srv.URL, srv.URL
	t.Cleanup(func() {
		llm.GeminiURL, llm.OpenCodeURL = origGem, origOC
	})

	s := NewDemo(fullCfg)
	if fullCfg.BotToken != "real-bot-token" || fullCfg.LLMAPIKey != "real-cheap-key" || len(fullCfg.GeminiAPIKeys) != 1 {
		t.Fatalf("NewDemo изменил исходный конфиг: %+v", fullCfg)
	}
	if s.cfg.BotToken != "" || s.cfg.PanelPassword != "" ||
		s.cfg.LLMAPIKey != "" || s.cfg.LLMAPIURL != "" || s.cfg.LLMModel != "" ||
		len(s.cfg.GeminiAPIKeys) != 0 || s.cfg.OpenCodeAPIKey != "" || len(s.cfg.OpenCodeAPIKeys) != 0 ||
		s.cfg.AssemblyAPIKey != "" || s.cfg.FishStudioAPIKey != "" ||
		s.cfg.GrokAPIKey != "" || s.cfg.GroqAPIKey != "" ||
		s.cfg.GitHubToken != "" || s.cfg.SecretKey != "" || s.cfg.SecretKeyFile != "" ||
		s.cfg.TavilyAPIKey != "" || s.cfg.ExaAPIKey != "" || s.cfg.DBPath != "" {
		t.Fatalf("в конфиге демо остались секреты или пути БД: %+v", s.cfg)
	}
	if len(llm.Providers(s.cfg)) != 0 || len(llm.DefaultChain(s.cfg)) != 0 {
		t.Fatalf("в демо не должно быть провайдеров из .env: %+v", llm.Providers(s.cfg))
	}

	checks := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/llm/test", `{"provider":"gemini"}`},
		{http.MethodPut, "/api/llm/keys/gemini", `{"keys":["secret-key"]}`},
		{http.MethodDelete, "/api/llm/keys/gemini", ``},
		{http.MethodGet, "/api/llm/models?provider=gemini", ``},
	}
	for _, tc := range checks {
		rec := demoCall(t, s, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: код %d, ждали 403 (%s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "демо") {
			t.Fatalf("%s %s: ожидали русское сообщение про демо-режим, получили %s", tc.method, tc.path, rec.Body.String())
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("демо-режим сделал %d сетевых запросов к LLM", hits.Load())
	}
}

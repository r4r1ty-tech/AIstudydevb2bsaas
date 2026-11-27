package mcpapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

const (
	testLogin    = "student"
	testPassword = "s3cret-pass"
	testSesskey  = "SESSKEY123"
)

// fakeMoodle — минимальная имитация Moodle: форма входа, /my/ с sesskey и
// AJAX-метод курсов. Куки «good» — живая сессия, «stale» — протухшая.
type fakeMoodle struct {
	mu               sync.Mutex
	logins           int
	coursesCalls     int
	emptyUntilLogin  bool
	cookieDead       bool
	rejectNextAJAX   int
	coursesAfterAuth []map[string]any
}

func newFakeMoodle() *fakeMoodle {
	return &fakeMoodle{coursesAfterAuth: []map[string]any{
		{"id": 11, "fullname": "Математический анализ", "shortname": "MA", "hidden": false, "viewurl": "https://do.ssau.ru/moodle/course/view.php?id=11"},
		{"id": 12, "fullname": "Теория информации", "shortname": "TI", "hidden": false, "viewurl": "https://do.ssau.ru/moodle/course/view.php?id=12"},
		{"id": 13, "fullname": "Физика", "shortname": "PH", "hidden": true, "viewurl": "https://do.ssau.ru/moodle/course/view.php?id=13"},
		{"id": 14, "fullname": "История", "shortname": "HIS", "hidden": false, "viewurl": "https://do.ssau.ru/moodle/course/view.php?id=14"},
		{"id": 15, "fullname": "Программирование", "shortname": "PR", "hidden": false, "viewurl": "https://do.ssau.ru/moodle/course/view.php?id=15"},
	}}
}

func (f *fakeMoodle) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func (f *fakeMoodle) authRequired(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie("MoodleSession")
	f.mu.Lock()
	dead := f.cookieDead
	f.mu.Unlock()
	if err == nil && c.Value == "good" && !dead {
		return true
	}
	http.Redirect(w, r, "/moodle/login/index.php", http.StatusSeeOther)
	return false
}

func (f *fakeMoodle) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/moodle/login/index.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if r.FormValue("username") != testLogin || r.FormValue("password") != testPassword {
				_, _ = w.Write([]byte(loginPage))
				return
			}
			f.mu.Lock()
			f.logins++
			f.cookieDead = false
			f.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "MoodleSession", Value: "good", Path: "/moodle/"})
			http.Redirect(w, r, "/moodle/my/", http.StatusSeeOther)
			return
		}
		_, _ = w.Write([]byte(loginPage))
	})
	mux.HandleFunc("/moodle/my/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authRequired(w, r) {
			return
		}
		_, _ = w.Write([]byte(`<html><script>{"sesskey":"` + testSesskey + `"}</script></html>`))
	})
	mux.HandleFunc("/moodle/lib/ajax/service.php", func(w http.ResponseWriter, r *http.Request) {
		if !f.authRequired(w, r) {
			return
		}
		if r.URL.Query().Get("sesskey") != testSesskey {
			_, _ = w.Write([]byte(`[{"error":true,"exception":{"errorcode":"invalidsesskey","message":"Invalid sesskey"}}]`))
			return
		}
		f.mu.Lock()
		reject := f.rejectNextAJAX > 0
		if reject {
			f.rejectNextAJAX--
			f.cookieDead = true
		}
		f.coursesCalls++
		empty := f.emptyUntilLogin && f.logins == 0
		courses := f.coursesAfterAuth
		f.mu.Unlock()
		if reject {
			_, _ = w.Write([]byte(`[{"error":true,"exception":{"errorcode":"invalidsesskey","message":"Invalid sesskey"}}]`))
			return
		}
		if empty {
			courses = nil
		}
		payload, err := json.Marshal([]map[string]any{{
			"error": false,
			"data":  map[string]any{"courses": courses},
		}})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	})
	return mux
}

const loginPage = `<html><body>
<form id="login" action="/moodle/login/index.php" method="post">
<input type="hidden" name="logintoken" value="tok">
<input type="text" name="username" value="">
<input type="password" name="password" value="">
<input type="submit" value="Вход">
</form>
</body></html>`

func testConfig(t *testing.T, serverURL string) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{
		BaseURL:     serverURL + "/moodle",
		StateDir:    dir,
		SessionFile: filepath.Join(dir, SessionFileName),
		Timeout:     5 * time.Second,
	}
}

func writeCookie(t *testing.T, cfg Config, value string) {
	t.Helper()
	raw := fmt.Sprintf(`[{"n":"MoodleSession","v":%q}]`, value)
	if err := os.WriteFile(cfg.SessionFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestConfigDefaultsMCP проверяет значения по умолчанию и пути session.json.
func TestConfigDefaultsMCP(t *testing.T) {
	home := t.TempDir()
	for _, key := range []string{
		"MOODLE_MCP_BASE_URL", "MOODLE_MCP_STATE_DIR", "MOODLE_MCP_DB",
		"MOODLE_MCP_SECRET_KEY_FILE", "MOODLE_MCP_LOGIN", "MOODLE_MCP_PASSWORD",
		"MOODLE_MCP_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", home)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Fatalf("base url = %q", cfg.BaseURL)
	}
	wantDir := filepath.Join(home, DefaultStateDirName)
	if cfg.StateDir != wantDir {
		t.Fatalf("state dir = %q, want %q", cfg.StateDir, wantDir)
	}
	if cfg.SessionFile != filepath.Join(wantDir, SessionFileName) {
		t.Fatalf("session file = %q", cfg.SessionFile)
	}
	if cfg.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %s", cfg.Timeout)
	}
}

// TestConfigOverridesMCP проверяет env, «~» и разбор таймаута.
func TestConfigOverridesMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOODLE_MCP_BASE_URL", "https://example.test/moodle/")
	t.Setenv("MOODLE_MCP_STATE_DIR", "~/state")
	t.Setenv("MOODLE_MCP_DB", "~/bot.db")
	t.Setenv("MOODLE_MCP_SECRET_KEY_FILE", "~/secret.key")
	t.Setenv("MOODLE_MCP_LOGIN", testLogin)
	t.Setenv("MOODLE_MCP_PASSWORD", testPassword)
	t.Setenv("MOODLE_MCP_TIMEOUT", "90s")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://example.test/moodle" {
		t.Fatalf("base url = %q", cfg.BaseURL)
	}
	if cfg.StateDir != filepath.Join(home, "state") {
		t.Fatalf("state dir = %q", cfg.StateDir)
	}
	if cfg.DBPath != filepath.Join(home, "bot.db") {
		t.Fatalf("db = %q", cfg.DBPath)
	}
	if cfg.SecretKeyFile != filepath.Join(home, "secret.key") {
		t.Fatalf("key = %q", cfg.SecretKeyFile)
	}
	if cfg.Timeout != 90*time.Second {
		t.Fatalf("timeout = %s", cfg.Timeout)
	}
}

// TestConfigBadTimeoutMCP: опечатка в таймауте не должна молча превращаться в 40s.
func TestConfigBadTimeoutMCP(t *testing.T) {
	t.Setenv("MOODLE_MCP_TIMEOUT", "soon")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("ожидалась ошибка для MOODLE_MCP_TIMEOUT=soon")
	}
}

// TestSessionLoginWritesCookieMCP: без кук сессия входит по env-паролю,
// сохраняет session.json с правами 0600 и не пишет в него пароль.
func TestSessionLoginWritesCookieMCP(t *testing.T) {
	fake := newFakeMoodle()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	cfg := testConfig(t, srv.URL)
	cfg.Login, cfg.Password = testLogin, testPassword

	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.loginCount(); got != 1 {
		t.Fatalf("логинов = %d, want 1", got)
	}
	st := sess.State(context.Background())
	if !st.Authenticated || st.Login != testLogin {
		t.Fatalf("state = %+v", st)
	}
	info, err := os.Stat(cfg.SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("права session.json = %o, want 600", got)
	}
	raw, err := os.ReadFile(cfg.SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testPassword) {
		t.Fatal("пароль попал в session.json")
	}
	courses, err := sess.Courses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(courses) != 5 {
		t.Fatalf("курсов = %d, want 5", len(courses))
	}
	if courses[0].ShortName != "MA" || courses[0].Name != "Математический анализ" {
		t.Fatalf("курс = %+v", courses[0])
	}
}

// TestSessionRestoresCookieMCP: живая кука используется без повторного входа.
func TestSessionRestoresCookieMCP(t *testing.T) {
	fake := newFakeMoodle()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	cfg := testConfig(t, srv.URL)
	cfg.Login, cfg.Password = testLogin, testPassword
	writeCookie(t, cfg, "good")

	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.loginCount(); got != 0 {
		t.Fatalf("логинов = %d, want 0", got)
	}
	if st := sess.State(context.Background()); !st.Authenticated {
		t.Fatalf("state = %+v", st)
	}
}

// TestSessionReloginOnStaleCookieMCP: протухшая кука ведёт к входу по паролю.
func TestSessionReloginOnStaleCookieMCP(t *testing.T) {
	fake := newFakeMoodle()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	cfg := testConfig(t, srv.URL)
	cfg.Login, cfg.Password = testLogin, testPassword
	writeCookie(t, cfg, "stale")

	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.loginCount(); got != 1 {
		t.Fatalf("логинов = %d, want 1", got)
	}
	if st := sess.State(context.Background()); !st.Authenticated {
		t.Fatalf("state = %+v", st)
	}
}

// TestSessionZeroCoursesForcesLoginMCP: Check проходит, Courses пуст — нужен
// принудительный вход по паролю (известная особенность Moodle).
func TestSessionZeroCoursesForcesLoginMCP(t *testing.T) {
	fake := newFakeMoodle()
	fake.emptyUntilLogin = true
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	cfg := testConfig(t, srv.URL)
	cfg.Login, cfg.Password = testLogin, testPassword
	writeCookie(t, cfg, "good")

	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.loginCount(); got != 1 {
		t.Fatalf("логинов = %d, want 1", got)
	}
	courses, err := sess.Courses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(courses) != 5 {
		t.Fatalf("курсов = %d, want 5", len(courses))
	}
	if st := sess.State(context.Background()); !st.Authenticated {
		t.Fatalf("state = %+v", st)
	}
}

// TestSessionNoCredentialsMCP: без кук и без пароля сервер не падает, а честно
// сообщает, что вход невозможен.
func TestSessionNoCredentialsMCP(t *testing.T) {
	fake := newFakeMoodle()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	cfg := testConfig(t, srv.URL)

	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := sess.State(context.Background())
	if st.Authenticated {
		t.Fatalf("state = %+v", st)
	}
	if len(st.Warnings) == 0 {
		t.Fatal("ожидалось предупреждение о невозможности входа")
	}
	if _, err := sess.Courses(context.Background()); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}
}

// TestSessionDBCredentialsMCP: сессия и пароль расшифровываются из bot.db,
// протухшая сессия перелогинивается паролем из базы.
func TestSessionDBCredentialsMCP(t *testing.T) {
	fake := newFakeMoodle()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	cfg := testConfig(t, srv.URL)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bot.db")
	v, keyPath := testVault(t)
	sealedPass, err := v.Seal(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	sealedSession, err := v.Seal(`[{"n":"MoodleSession","v":"stale"}]`)
	if err != nil {
		t.Fatal(err)
	}
	st, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(`CREATE TABLE accounts (
		telegram_id INTEGER PRIMARY KEY, kind TEXT NOT NULL,
		login TEXT, secret TEXT, session TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(`INSERT INTO accounts (telegram_id, kind, login, secret, session) VALUES (1, 'moodle', ?, ?, ?)`,
		testLogin, sealedPass, sealedSession); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.DBPath, cfg.SecretKeyFile = dbPath, keyPath

	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.loginCount(); got != 1 {
		t.Fatalf("логинов = %d, want 1", got)
	}
	if st := sess.State(context.Background()); !st.Authenticated {
		t.Fatalf("state = %+v", st)
	}
}

// TestServerToolsListAndCallsMCP — дымовой тест MCP: tools/list содержит оба
// инструмента, moodle_status и moodle_courses отвечают.
func TestServerToolsListAndCallsMCP(t *testing.T) {
	fake := newFakeMoodle()
	srvHTTP := httptest.NewServer(fake.handler())
	defer srvHTTP.Close()
	cfg := testConfig(t, srvHTTP.URL)
	cfg.Login, cfg.Password = testLogin, testPassword
	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server := NewServer(sess)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	list, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"moodle_status", "moodle_courses"} {
		if !names[want] {
			t.Fatalf("в tools/list нет %q: %+v", want, names)
		}
	}

	statusRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "moodle_status"})
	if err != nil {
		t.Fatal(err)
	}
	var status StatusOutput
	decodeStructured(t, statusRes, &status)
	if !status.Authenticated || status.Login != testLogin || status.BaseURL != cfg.BaseURL {
		t.Fatalf("status = %+v", status)
	}
	if status.LastRefresh == "" {
		t.Fatal("last_refresh пуст")
	}

	coursesRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "moodle_courses",
		Arguments: map[string]any{"limit": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	var courses CoursesOutput
	decodeStructured(t, coursesRes, &courses)
	if courses.Total != 5 || len(courses.Courses) != 2 {
		t.Fatalf("courses = %+v", courses)
	}
}

func decodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("structured content: %v (%s)", err, raw)
	}
}

// connectMCP поднимает in-process MCP-клиент к серверу на тестовой сессии.
func connectMCP(t *testing.T, sess *Session) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	server := NewServer(sess)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
		cancel()
	})
	return clientSession, ctx
}

// TestLoadDBAccountReadOnlyMCP: MCP читает bot.db только на чтение. Файл
// переведён в 0444, SQLite не должен ни мигрировать, ни создавать journal/WAL.
func TestLoadDBAccountReadOnlyMCP(t *testing.T) {
	v, keyPath := testVault(t)
	sealedPass, err := v.Seal(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	sealedSession, err := v.Seal(`[{"n":"MoodleSession","v":"good"}]`)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bot.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE accounts (
		telegram_id INTEGER PRIMARY KEY, kind TEXT NOT NULL,
		login TEXT, secret TEXT, session TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (telegram_id, kind, login, secret, session) VALUES (1, 'moodle', ?, ?, ?)`,
		testLogin, sealedPass, sealedSession); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dbPath, 0o444); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	entriesBefore, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	login, password, session, warnings, err := loadDBAccount(dbPath, keyPath)
	if err != nil {
		t.Fatalf("read-only чтение: %v", err)
	}
	if login != testLogin || password != testPassword || session == "" {
		t.Fatalf("аккаунт: login=%q pass_задан=%v session_задан=%v", login, password != "", session != "")
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}

	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("bot.db изменилась при чтении")
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o444 {
		t.Fatalf("права базы = %o, want 444", got)
	}
	entriesAfter, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesBefore) != len(entriesAfter) {
		t.Fatalf("в каталоге появились файлы: было %d, стало %d", len(entriesBefore), len(entriesAfter))
	}
	for i := range entriesBefore {
		if entriesBefore[i].Name() != entriesAfter[i].Name() {
			t.Fatalf("файлы каталога изменились: %q → %q", entriesBefore[i].Name(), entriesAfter[i].Name())
		}
	}
}

func testVault(t *testing.T) (*vault.Vault, string) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	path := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return v, path
}

// TestServerCoursesResourceMCP: ресурс moodle://courses виден в resources/list
// и отдаёт те же 5 курсов в JSON.
func TestServerCoursesResourceMCP(t *testing.T) {
	fake := newFakeMoodle()
	srvHTTP := httptest.NewServer(fake.handler())
	defer srvHTTP.Close()
	cfg := testConfig(t, srvHTTP.URL)
	cfg.Login, cfg.Password = testLogin, testPassword
	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	clientSession, ctx := connectMCP(t, sess)

	list, err := clientSession.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, res := range list.Resources {
		if res.URI == CoursesResourceURI {
			found = true
			if res.MIMEType != "application/json" {
				t.Fatalf("mime = %q", res.MIMEType)
			}
		}
	}
	if !found {
		t.Fatalf("в resources/list нет %q", CoursesResourceURI)
	}

	read, err := clientSession.ReadResource(ctx, &mcp.ReadResourceParams{URI: CoursesResourceURI})
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Contents) != 1 {
		t.Fatalf("contents = %d", len(read.Contents))
	}
	if read.Contents[0].MIMEType != "application/json" {
		t.Fatalf("mime = %q", read.Contents[0].MIMEType)
	}
	var out CoursesOutput
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &out); err != nil {
		t.Fatalf("resource json: %v", err)
	}
	if out.Total != 5 || len(out.Courses) != 5 {
		t.Fatalf("resource courses = %+v", out)
	}
}

// TestSessionRetryOnUnauthorizedMCP: если запрос получил invalidsesskey,
// Session.do сбрасывает сессию, перелогинивается и повторяет запрос.
func TestSessionRetryOnUnauthorizedMCP(t *testing.T) {
	fake := newFakeMoodle()
	srvHTTP := httptest.NewServer(fake.handler())
	defer srvHTTP.Close()
	cfg := testConfig(t, srvHTTP.URL)
	cfg.Login, cfg.Password = testLogin, testPassword
	writeCookie(t, cfg, "good")
	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.loginCount(); got != 0 {
		t.Fatalf("до теста логинов = %d, want 0", got)
	}
	fake.mu.Lock()
	fake.rejectNextAJAX = 1
	fake.mu.Unlock()

	courses, err := sess.Courses(context.Background())
	if err != nil {
		t.Fatalf("повтор после ErrUnauthorized: %v", err)
	}
	if len(courses) != 5 {
		t.Fatalf("курсов = %d, want 5", len(courses))
	}
	if got := fake.loginCount(); got != 1 {
		t.Fatalf("логинов = %d, want 1", got)
	}
	if st := sess.State(context.Background()); !st.Authenticated {
		t.Fatalf("state после повтора = %+v", st)
	}
}

// TestSessionConcurrentMCP: State и Courses можно звать параллельно, вход и
// клиент сериализованы мьютексом. Полезно под -race.
func TestSessionConcurrentMCP(t *testing.T) {
	fake := newFakeMoodle()
	srvHTTP := httptest.NewServer(fake.handler())
	defer srvHTTP.Close()
	cfg := testConfig(t, srvHTTP.URL)
	cfg.Login, cfg.Password = testLogin, testPassword
	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var failures atomic.Int64
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if i%2 == 0 {
					if st := sess.State(context.Background()); !st.Authenticated {
						failures.Add(1)
					}
					continue
				}
				courses, err := sess.Courses(context.Background())
				if err != nil || len(courses) != 5 {
					failures.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()
	if n := failures.Load(); n != 0 {
		t.Fatalf("параллельных сбоев: %d", n)
	}
}

// TestCoursesLimitBoundsMCP: <=0 — 20, >100 — 100.
func TestCoursesLimitBoundsMCP(t *testing.T) {
	cases := map[int]int{
		-100: 20, -1: 20, 0: 20, 1: 1, 20: 20, 99: 99, 100: 100, 101: 100, 5000: 100,
	}
	for in, want := range cases {
		if got := coursesLimit(in); got != want {
			t.Errorf("coursesLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

// TestLoadConfigFilePasswordPermsMCP: конфиг с паролем обязан быть 0600.
func TestLoadConfigFilePasswordPermsMCP(t *testing.T) {
	home := t.TempDir()
	clearMCPEnv(t)
	t.Setenv("HOME", home)
	path := filepath.Join(home, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"login":"student","password":"json-secret"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfigFile(path); err == nil {
		t.Fatal("ожидалась ошибка для конфига с паролем и правами 0644")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != "json-secret" {
		t.Fatalf("password = %q", cfg.Password)
	}
}

// TestLoadConfigFileMergesEnvCredsMCP: логин из env + пароль из JSON — это
// валидная пара, проверка должна идти после слияния. Пути из файла
// раскрываются от домашнего каталога.
func TestLoadConfigFileMergesEnvCredsMCP(t *testing.T) {
	home := t.TempDir()
	clearMCPEnv(t)
	t.Setenv("HOME", home)
	t.Setenv("MOODLE_MCP_LOGIN", testLogin)
	path := filepath.Join(home, "mcp.json")
	data := `{"password":"json-secret","db":"~/bot.db","secret_key_file":"~/secret.key"}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Login != testLogin || cfg.Password != "json-secret" {
		t.Fatalf("creds = %q/%q", cfg.Login, cfg.Password)
	}
	if cfg.DBPath != filepath.Join(home, "bot.db") {
		t.Fatalf("db = %q", cfg.DBPath)
	}
	if cfg.SecretKeyFile != filepath.Join(home, "secret.key") {
		t.Fatalf("key = %q", cfg.SecretKeyFile)
	}
}

func clearMCPEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"MOODLE_MCP_BASE_URL", "MOODLE_MCP_STATE_DIR", "MOODLE_MCP_DB",
		"MOODLE_MCP_SECRET_KEY_FILE", "MOODLE_MCP_LOGIN", "MOODLE_MCP_PASSWORD",
		"MOODLE_MCP_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
}

// lockedBuffer — потокобезопасный stderr для дочернего процесса.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestStdioSmokeMCP собирает cmd/mcp, запускает его по stdio и проверяет, что
// клиент видит инструменты, ресурс, 5 курсов и что логи не попали в stdout
// (иначе парсер JSON-RPC упал бы). session.json — 0600, logs — в state-dir.
func TestStdioSmokeMCP(t *testing.T) {
	if testing.Short() {
		t.Skip("stdio smoke собирает бинарник")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go недоступен в PATH")
	}
	fake := newFakeMoodle()
	httpSrv := httptest.NewServer(fake.handler())
	defer httpSrv.Close()

	bin := filepath.Join(t.TempDir(), "mcp")
	build := exec.Command(goBin, "build", "-o", bin, "./cmd/mcp")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	stateDir := t.TempDir()
	cmd := exec.Command(bin, "-transport", "stdio", "-state-dir", stateDir)
	cmd.Env = append(os.Environ(),
		"MOODLE_MCP_BASE_URL="+httpSrv.URL+"/moodle",
		"MOODLE_MCP_LOGIN="+testLogin,
		"MOODLE_MCP_PASSWORD="+testPassword,
		"MOODLE_MCP_DB=",
		"MOODLE_MCP_SECRET_KEY_FILE=",
		"MOODLE_MCP_TIMEOUT=5s",
		"LOG_DIR=",
		"LOG_FILE=",
		"LOG_LEVEL=warn",
		"HOME="+t.TempDir(),
	)
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "stdio-smoke", Version: "0"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("stdio connect: %v\nstderr:\n%s", err, stderr.String())
	}
	defer clientSession.Close()

	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"moodle_status", "moodle_courses"} {
		if !names[want] {
			t.Fatalf("в tools/list нет %q", want)
		}
	}

	resources, err := clientSession.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, res := range resources.Resources {
		if res.URI == CoursesResourceURI {
			found = true
		}
	}
	if !found {
		t.Fatalf("в resources/list нет %q", CoursesResourceURI)
	}

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "moodle_courses",
		Arguments: map[string]any{"limit": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out CoursesOutput
	decodeStructured(t, res, &out)
	if out.Total != 5 || len(out.Courses) != 3 {
		t.Fatalf("courses = %+v", out)
	}

	info, err := os.Stat(filepath.Join(stateDir, SessionFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("права session.json = %o, want 600", got)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "logs", "mcp.log")); err != nil {
		t.Fatalf("подробный лог не в state-dir: %v", err)
	}
}

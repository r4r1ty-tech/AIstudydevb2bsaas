package webapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/panelpass"
)

// fakeClock — время, которым управляет тест (сдвигаем на дни вперёд).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const day = 24 * time.Hour

// newSessionServer — панель с ключом и управляемыми часами.
func newSessionServer(t *testing.T) (*Server, []byte, *fakeClock) {
	t.Helper()
	s, key := newPassServer(t)
	clk := &fakeClock{t: time.Now().Truncate(time.Second)}
	s.clock = clk.now
	return s, key, clk
}

func findCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func login(t *testing.T, s *Server, pw string) *httptest.ResponseRecorder {
	t.Helper()
	return callPw(s, http.MethodPost, "/api/login", pw)
}

// callCookie — запрос только с кукой сессии, без пароля.
func callCookie(s *Server, method, path, tok string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if tok != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func loginAs(t *testing.T, s *Server, key []byte, clk *fakeClock, id int64) string {
	t.Helper()
	pw, _ := panelpass.Issue(key, id, clk.now())
	rec := login(t, s, pw)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	c := findCookie(rec, sessionCookie)
	if c == nil || c.Value == "" {
		t.Fatal("login не поставил куку")
	}
	return c.Value
}

func TestLoginSetsCookieAndGrantsAccess(t *testing.T) {
	s, key, clk := newSessionServer(t)
	friend := config.DefaultWhitelist[1]
	pw, _ := panelpass.Issue(key, friend, clk.now())

	rec := login(t, s, pw)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	c := findCookie(rec, sessionCookie)
	if c == nil {
		t.Fatal("нет куки")
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("атрибуты куки: %+v", c)
	}
	if want := int(sessionTTL.Seconds()); c.MaxAge != want {
		t.Fatalf("Max-Age = %d, ждали %d", c.MaxAge, want)
	}
	if len(c.Value) < 40 {
		t.Fatalf("токен слишком короткий: %d", len(c.Value))
	}

	// В базе — только хэш токена, привязанный к тому же Telegram id.
	if sess, _ := s.st.GetWebSession(c.Value, clk.now()); sess != nil {
		t.Fatal("в базе лежит сам токен, а не хэш")
	}
	sess, err := s.st.GetWebSession(hashToken(c.Value), clk.now())
	if err != nil || sess == nil || sess.TelegramID != friend {
		t.Fatalf("сессия в базе: %+v %v", sess, err)
	}

	// Без пароля, только кука.
	rec = callCookie(s, http.MethodGet, "/api/me", c.Value, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/me по куке: %d %s", rec.Code, rec.Body)
	}
	var me meView
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me.TelegramID != friend {
		t.Fatalf("вошёл как %d, ждали %d", me.TelegramID, friend)
	}
	var set settingsResponse
	rec = callCookie(s, http.MethodGet, "/api/settings", c.Value, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &set)
	if !set.Session || set.IsAdmin {
		t.Fatalf("settings: session=%v is_admin=%v", set.Session, set.IsAdmin)
	}
	// Админская логика не сломана: друг по куке служебное не видит.
	if code := callCookie(s, http.MethodGet, "/api/people", c.Value, nil).Code; code != http.StatusForbidden {
		t.Fatalf("/api/people по куке друга: %d, ждали 403", code)
	}
}

func TestLoginAdminSessions(t *testing.T) {
	s, key, clk := newSessionServer(t)
	for name, tok := range map[string]string{
		"личный пароль админа": loginAs(t, s, key, clk, config.DefaultAdminID),
		"общий PANEL_PASSWORD": func() string {
			rec := login(t, s, testPassword)
			c := findCookie(rec, sessionCookie)
			if rec.Code != http.StatusOK || c == nil {
				t.Fatalf("login общим паролем: %d", rec.Code)
			}
			return c.Value
		}(),
	} {
		if code := callCookie(s, http.MethodGet, "/api/people", tok, nil).Code; code != http.StatusOK {
			t.Fatalf("%s: /api/people по куке %d, ждали 200", name, code)
		}
	}
}

func TestLoginRejectsBadPassword(t *testing.T) {
	s, key, clk := newSessionServer(t)
	stranger, _ := panelpass.Issue(key, 42, clk.now())
	old, _ := panelpass.Issue(key, config.DefaultWhitelist[1], clk.now().Add(-3*time.Hour))
	for _, pw := range []string{stranger, old, "abcd-efgh", ""} {
		rec := login(t, s, pw)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("пароль %q: %d, ждали 401", pw, rec.Code)
		}
		if findCookie(rec, sessionCookie) != nil {
			t.Fatalf("пароль %q: кука поставлена", pw)
		}
	}
}

func TestSessionRejectsUnknownCookie(t *testing.T) {
	s, _, _ := newSessionServer(t)
	rec := callCookie(s, http.MethodGet, "/api/me", "forged-token", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("левая кука: %d, ждали 401", rec.Code)
	}
	if c := findCookie(rec, sessionCookie); c == nil || c.MaxAge >= 0 {
		t.Fatalf("левая кука должна стираться: %+v", c)
	}
}

func TestSessionExpires(t *testing.T) {
	s, key, clk := newSessionServer(t)
	friend := config.DefaultWhitelist[1]
	tok := loginAs(t, s, key, clk, friend)
	tok2 := loginAs(t, s, key, clk, friend)

	// Прямо перед концом срока кука ещё работает (при этом она продлевается).
	clk.advance(sessionTTL - time.Minute)
	if code := callCookie(s, http.MethodGet, "/api/me", tok, nil).Code; code != http.StatusOK {
		t.Fatalf("за минуту до конца: %d", code)
	}

	// Вторая сессия никого не продлевала и после срока мертва.
	clk.advance(2 * time.Minute)
	rec := callCookie(s, http.MethodGet, "/api/me", tok2, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("после срока: %d, ждали 401", rec.Code)
	}
	if c := findCookie(rec, sessionCookie); c == nil || c.MaxAge >= 0 {
		t.Fatalf("просроченная кука должна стираться: %+v", c)
	}
	// Заголовок со свежим паролем при этом по-прежнему пускает.
	pw, _ := panelpass.Issue(key, friend, clk.now())
	if code := callPw(s, http.MethodGet, "/api/me", pw).Code; code != http.StatusOK {
		t.Fatalf("пароль в заголовке: %d", code)
	}
}

func TestSessionSlidingRenewal(t *testing.T) {
	s, key, clk := newSessionServer(t)
	friend := config.DefaultWhitelist[1]
	start := clk.now()
	tok := loginAs(t, s, key, clk, friend)
	hash := hashToken(tok)
	expires := func() time.Time {
		sess, err := s.st.GetWebSession(hash, clk.now())
		if err != nil || sess == nil {
			t.Fatalf("сессии нет: %v", err)
		}
		return sess.ExpiresAt
	}

	// Больше половины срока впереди — не трогаем и куку не перевыпускаем.
	clk.advance(10 * day)
	rec := callCookie(s, http.MethodGet, "/api/me", tok, nil)
	if rec.Code != http.StatusOK || findCookie(rec, sessionCookie) != nil {
		t.Fatalf("+10д: код %d, кука %v", rec.Code, findCookie(rec, sessionCookie))
	}
	if got := expires(); !got.Equal(start.Add(sessionTTL)) {
		t.Fatalf("+10д: срок сдвинулся на %s", got)
	}

	// Осталось меньше половины — срок продлевается, кука перевыпускается (токен тот же).
	clk.advance(10 * day)
	rec = callCookie(s, http.MethodGet, "/api/me", tok, nil)
	c := findCookie(rec, sessionCookie)
	if rec.Code != http.StatusOK || c == nil {
		t.Fatalf("+20д: код %d, кука %v", rec.Code, c)
	}
	if c.Value != tok || c.MaxAge != int(sessionTTL.Seconds()) || !c.HttpOnly || !c.Secure {
		t.Fatalf("перевыпущенная кука: %+v", c)
	}
	if got, want := expires(), start.Add(20*day+sessionTTL); !got.Equal(want) {
		t.Fatalf("+20д: срок %s, ждали %s", got, want)
	}

	// Абсолютный потолок: сессию каждые 14 дней дёргают (+34, +48, +62, +76 дней),
	// но дальше 90 дней от входа срок не уходит и перевыпускать куку уже нечем.
	for i := 0; i < 4; i++ {
		clk.advance(14 * day)
		if code := callCookie(s, http.MethodGet, "/api/me", tok, nil).Code; code != http.StatusOK {
			t.Fatalf("раунд %d: %d", i, code)
		}
	}
	if got, limit := expires(), start.Add(sessionMaxAge); !got.Equal(limit) {
		t.Fatalf("срок %s, ждали потолок %s", got, limit)
	}
	rec = callCookie(s, http.MethodGet, "/api/me", tok, nil)
	if rec.Code != http.StatusOK || findCookie(rec, sessionCookie) != nil {
		t.Fatalf("у потолка: код %d, кука должна не перевыпускаться", rec.Code)
	}
	clk.advance(15 * day) // +91 день
	if code := callCookie(s, http.MethodGet, "/api/me", tok, nil).Code; code != http.StatusUnauthorized {
		t.Fatalf("после потолка: %d, ждали 401", code)
	}
}

// Рядом с границей продления запросы идут параллельно: токен один, ни один не
// должен получить 401, а срок — выйти ровно на одно значение.
func TestSessionRenewalParallel(t *testing.T) {
	s, key, clk := newSessionServer(t)
	tok := loginAs(t, s, key, clk, config.DefaultWhitelist[1])
	clk.advance(20 * day)

	var wg sync.WaitGroup
	codes := make(chan int, 24)
	for i := 0; i < cap(codes); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- callCookie(s, http.MethodGet, "/api/me", tok, nil).Code
		}()
	}
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != http.StatusOK {
			t.Fatalf("параллельный запрос: %d", c)
		}
	}
	sess, _ := s.st.GetWebSession(hashToken(tok), clk.now())
	if sess == nil || !sess.ExpiresAt.Equal(clk.now().Add(sessionTTL)) {
		t.Fatalf("после параллельного продления: %+v", sess)
	}
}

func TestLogout(t *testing.T) {
	s, key, clk := newSessionServer(t)
	friend := config.DefaultWhitelist[1]
	tok := loginAs(t, s, key, clk, friend)
	other := loginAs(t, s, key, clk, friend) // второе устройство

	rec := callCookie(s, http.MethodPost, "/api/logout", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	if c := findCookie(rec, sessionCookie); c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Fatalf("logout не стёр куку: %+v", c)
	}
	if sess, _ := s.st.GetWebSession(hashToken(tok), clk.now()); sess != nil {
		t.Fatal("сессия осталась в базе")
	}
	if code := callCookie(s, http.MethodGet, "/api/me", tok, nil).Code; code != http.StatusUnauthorized {
		t.Fatalf("после выхода: %d, ждали 401", code)
	}
	if code := callCookie(s, http.MethodGet, "/api/me", other, nil).Code; code != http.StatusOK {
		t.Fatalf("другое устройство после чужого выхода: %d, ждали 200", code)
	}
	// Повторный выход и выход без куки не падают.
	if code := callCookie(s, http.MethodPost, "/api/logout", tok, nil).Code; code != http.StatusOK {
		t.Fatalf("повторный logout: %d", code)
	}
	if code := callCookie(s, http.MethodPost, "/api/logout", "", nil).Code; code != http.StatusOK {
		t.Fatalf("logout без куки: %d", code)
	}
}

// Убрали из вайтлиста — вход по куке перестаёт работать.
func TestSessionRevokedWhenRemovedFromWhitelist(t *testing.T) {
	s, key, clk := newSessionServer(t)
	friend := config.DefaultWhitelist[1]
	tok := loginAs(t, s, key, clk, friend)

	var rest []int64
	for _, id := range s.cfg.Whitelist {
		if id != friend {
			rest = append(rest, id)
		}
	}
	s.cfg.Whitelist = rest
	if code := callCookie(s, http.MethodGet, "/api/me", tok, nil).Code; code != http.StatusUnauthorized {
		t.Fatalf("после удаления из вайтлиста: %d, ждали 401", code)
	}
	if sess, _ := s.st.GetWebSession(hashToken(tok), clk.now()); sess != nil {
		t.Fatal("сессия осталась в базе")
	}
}

func TestSessionCookieCrossSiteWrites(t *testing.T) {
	s, key, clk := newSessionServer(t)
	tok := loginAs(t, s, key, clk, config.DefaultWhitelist[1])

	cross := map[string]string{"Sec-Fetch-Site": "cross-site"}
	if code := callCookie(s, http.MethodPost, "/api/me", tok, cross).Code; code != http.StatusForbidden {
		t.Fatalf("POST с чужого сайта по куке: %d, ждали 403", code)
	}
	if code := callCookie(s, http.MethodGet, "/api/me", tok, cross).Code; code != http.StatusOK {
		t.Fatalf("GET с чужого сайта: %d, ждали 200", code)
	}
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	if code := callCookie(s, http.MethodPost, "/api/me", tok, same).Code; code == http.StatusForbidden {
		t.Fatalf("POST со своего сайта: %d", code)
	}
	// Старый способ (заголовок с паролем) от Sec-Fetch-Site не зависит.
	pw, _ := panelpass.Issue(key, config.DefaultWhitelist[1], clk.now())
	r := httptest.NewRequest(http.MethodPost, "/api/me", nil)
	r.Header.Set("X-Panel-Password", pw)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Fatalf("пароль в заголовке: %d", rec.Code)
	}
}

// Новый вход в браузере, где уже есть кука, заменяет старую сессию.
func TestLoginReplacesPreviousCookieSession(t *testing.T) {
	s, key, clk := newSessionServer(t)
	old := loginAs(t, s, key, clk, config.DefaultWhitelist[1])

	pw, _ := panelpass.Issue(key, config.DefaultAdminID, clk.now())
	r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	r.Header.Set("X-Panel-Password", pw)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: old})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	fresh := findCookie(rec, sessionCookie)
	if rec.Code != http.StatusOK || fresh == nil || fresh.Value == old {
		t.Fatalf("повторный вход: %d %+v", rec.Code, fresh)
	}
	if code := callCookie(s, http.MethodGet, "/api/me", old, nil).Code; code != http.StatusUnauthorized {
		t.Fatalf("старая кука после нового входа: %d, ждали 401", code)
	}
}

func TestLoginWithoutStore(t *testing.T) {
	// Без базы сессии не выдаются, но и не падают.
	s := New(&config.Config{
		ListenAddr: config.DefaultListen, Timezone: config.DefaultTimezone,
		PanelPassword: testPassword, AdminID: config.DefaultAdminID,
	}, nil, nil, "")
	rec := callPw(s, http.MethodPost, "/api/login", testPassword)
	if rec.Code != http.StatusServiceUnavailable || findCookie(rec, sessionCookie) != nil {
		t.Fatalf("login без базы: %d", rec.Code)
	}
}

func loginFromIP(s *Server, ip, pw string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	r.RemoteAddr = ip + ":1234"
	if pw != "" {
		r.Header.Set("X-Panel-Password", pw)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func TestLoginBruteForceRateLimit(t *testing.T) {
	s, key, clk := newSessionServer(t)
	goodPw, _ := panelpass.Issue(key, config.DefaultAdminID, clk.now())
	attackerIP := "198.51.100.10"
	otherIP := "198.51.100.11"

	for i := 0; i < loginMaxFailures; i++ {
		rec := loginFromIP(s, attackerIP, "wrong-password")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("попытка #%d: код %d, ждали 401", i+1, rec.Code)
		}
	}

	// После порога даже правильный пароль с этого IP блокируется с 429.
	rec := loginFromIP(s, attackerIP, goodPw)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("ожидали 429 после %d ошибок, получили %d (%s)", loginMaxFailures, rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("отсутствует заголовок Retry-After при 429")
	}

	// Другой IP не затронут.
	if other := loginFromIP(s, otherIP, goodPw); other.Code != http.StatusOK {
		t.Fatalf("другой IP не должен быть заблокирован: %d", other.Code)
	}

	// После окончания блокировки вход снова разрешён, счётчик сбрасывается.
	clk.advance(loginBlockDuration + time.Second)
	if ok := loginFromIP(s, attackerIP, goodPw); ok.Code != http.StatusOK {
		t.Fatalf("после снятия блокировки ожидали 200, получили %d (%s)", ok.Code, ok.Body.String())
	}

	// Частичные неудачи (меньше лимита) сбрасываются успешным входом.
	for i := 0; i < loginMaxFailures-2; i++ {
		if fail := loginFromIP(s, attackerIP, "wrong-password"); fail.Code != http.StatusUnauthorized {
			t.Fatalf("частичная ошибка #%d: %d", i+1, fail.Code)
		}
	}
	if ok := loginFromIP(s, attackerIP, goodPw); ok.Code != http.StatusOK {
		t.Fatalf("успешный вход до порога: %d", ok.Code)
	}
	for i := 0; i < loginMaxFailures-1; i++ {
		if fail := loginFromIP(s, attackerIP, "wrong-password"); fail.Code != http.StatusUnauthorized {
			t.Fatalf("новая серия ошибок #%d не должна была сразу блокировать: %d", i+1, fail.Code)
		}
	}

	// Очистка протухших записей срабатывает при обращении после интервала очистки.
	clk.advance(loginFailWindow + loginCleanupInterval + time.Second)
	if ok := loginFromIP(s, "198.51.100.12", goodPw); ok.Code != http.StatusOK {
		t.Fatalf("чистый IP: %d", ok.Code)
	}
	s.loginLim.mu.Lock()
	_, exists := s.loginLim.entries[attackerIP]
	s.loginLim.mu.Unlock()
	if exists {
		t.Fatalf("устаревшая запись для %s не удалена из лимитера", attackerIP)
	}
}

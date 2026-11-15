package webapp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// Вход по куке: после пароля из бота (или общего PANEL_PASSWORD) сервер выдаёт
// случайный токен в HttpOnly-куке, в базе лежит только его sha256. Срок
// скользящий: пока человек заходит, сессия продлевается, но не дальше
// sessionMaxAge от момента входа.
const (
	sessionCookie = "panel_session"
	sessionTTL    = 30 * 24 * time.Hour
	sessionMaxAge = 90 * 24 * time.Hour

	loginMaxFailures     = 5
	loginFailWindow      = 10 * time.Minute
	loginBlockDuration   = 5 * time.Minute
	loginCleanupInterval = 5 * time.Minute
)

type loginFailEntry struct {
	failures     int
	firstFailAt  time.Time
	blockedUntil time.Time
}

type loginLimiter struct {
	mu          sync.Mutex
	entries     map[string]*loginFailEntry
	lastCleanup time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: make(map[string]*loginFailEntry)}
}

func (l *loginLimiter) cleanupLocked(now time.Time) {
	if len(l.entries) == 0 {
		return
	}
	if !l.lastCleanup.IsZero() && now.Sub(l.lastCleanup) < loginCleanupInterval && len(l.entries) < 256 {
		return
	}
	for k, e := range l.entries {
		blockExpired := e.blockedUntil.IsZero() || !e.blockedUntil.After(now)
		windowExpired := now.Sub(e.firstFailAt) >= loginFailWindow
		if blockExpired && windowExpired {
			delete(l.entries, k)
		}
	}
	l.lastCleanup = now
}

// tryAttempt атомарно проверяет блок и занимает попытку входа по ключу: под
// одним мьютексом смотрит, заблокирован ли ключ, и если нет — сразу засчитывает
// попытку. Счётчик растёт до проверки пароля, поэтому параллельные запросы не
// проскакивают мимо порога. Верный вход потом снимает записи через reset, так
// что неудачей считается каждая попытка, которая не завершилась успехом.
// blocked — ключ сейчас заблокирован, retry — сколько ещё ждать.
func (l *loginLimiter) tryAttempt(key string, now time.Time) (blocked bool, retry time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked(now)
	e, ok := l.entries[key]
	if ok && e.blockedUntil.After(now) {
		return true, e.blockedUntil.Sub(now)
	}
	if !ok || now.Sub(e.firstFailAt) >= loginFailWindow {
		e = &loginFailEntry{firstFailAt: now}
		l.entries[key] = e
	}
	e.failures++
	if e.failures >= loginMaxFailures {
		e.blockedUntil = now.Add(loginBlockDuration)
	}
	return false, 0
}

// reset снимает записи ключа после верного входа.
func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

func (s *Server) loginClientKey(r *http.Request) string {
	trustProxy := s.cfg != nil && s.cfg.PanelTrustProxy
	if ip, ok := clientIP(r, trustProxy); ok {
		return ip.String()
	}
	return "unknown"
}

// writeBlocked отвечает 429 с Retry-After: клиент по IP временно заблокирован
// за перебор паролей.
func writeBlocked(w http.ResponseWriter, retry time.Duration) {
	sec := int(math.Ceil(retry.Seconds()))
	if sec < 1 {
		sec = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(sec))
	writeErr(w, http.StatusTooManyRequests, "слишком много неудачных попыток входа, попробуйте позже")
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func setSessionCookie(w http.ResponseWriter, tok string, expires, now time.Time) {
	maxAge := int(expires.Sub(now).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// panelAllowed — у человека всё ещё есть право на панель (вайтлист или админ).
// Убрали из вайтлиста — его сессии перестают работать.
func (s *Server) panelAllowed(id int64) bool {
	for _, x := range s.panelIDs() {
		if x == id {
			return true
		}
	}
	return false
}

// sessionFromCookie узнаёт человека по куке. Заодно продлевает сессию и
// перевыпускает куку, когда до конца осталось меньше половины срока; токен
// остаётся тот же, поэтому параллельные запросы друг другу не мешают.
// Мёртвую куку (нет в базе, срок вышел, доступ отозван) стирает в ответе.
func (s *Server) sessionFromCookie(w http.ResponseWriter, r *http.Request) (int64, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return 0, false
	}
	if s.st == nil {
		return 0, false
	}
	now := s.clock()
	hash := hashToken(c.Value)
	sess, err := s.st.GetWebSession(hash, now)
	if err != nil {
		logx.Errorf("webapp", "session: get: %v", err)
		return 0, false
	}
	if sess == nil {
		clearSessionCookie(w)
		return 0, false
	}
	if !s.panelAllowed(sess.TelegramID) {
		logx.Warnf("panel", "session: tg=%d больше не в списке, сессия снята", sess.TelegramID)
		_ = s.st.DeleteWebSession(hash)
		clearSessionCookie(w)
		return 0, false
	}
	if sess.ExpiresAt.Sub(now) < sessionTTL/2 {
		want := now.Add(sessionTTL)
		if limit := sess.CreatedAt.Add(sessionMaxAge); want.After(limit) {
			want = limit
		}
		if want.After(sess.ExpiresAt) {
			if err := s.st.ExtendWebSession(hash, want); err != nil {
				logx.Errorf("webapp", "session: extend: %v", err)
			} else {
				setSessionCookie(w, c.Value, want, now)
			}
		}
	}
	return sess.TelegramID, true
}

// startSession создаёт новую сессию для id и ставит куку. Если пришла кука
// другого человека, её сессия снимается: на этом браузере теперь новый вход.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, id int64) (time.Time, error) {
	if s.st == nil {
		return time.Time{}, fmt.Errorf("нет базы")
	}
	now := s.clock()
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = s.st.DeleteWebSession(hashToken(c.Value))
	}
	tok, err := newToken()
	if err != nil {
		return time.Time{}, err
	}
	expires := now.Add(sessionTTL)
	if err := s.st.CreateWebSession(hashToken(tok), id, now, expires); err != nil {
		return time.Time{}, err
	}
	setSessionCookie(w, tok, expires, now)
	return expires, nil
}

// handleLogin — вход по паролю: личному часовому из бота (по нему понятно, кто
// вошёл) или общему PANEL_PASSWORD (админ). Пароль идёт в тех же заголовках,
// что и раньше; в ответ — кука с долгой сессией, пароль больше не нужен.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.demo {
		writeJSON(w, map[string]any{"ok": true, "demo": true})
		return
	}
	id, ok, blockedFor := s.passwordAttempt(r)
	if blockedFor > 0 {
		logx.Warnf("panel", "login: rate limited client=%s", s.loginClientKey(r))
		writeBlocked(w, blockedFor)
		return
	}
	if !ok {
		logx.Warnf("panel", "login: bad password client=%s", s.loginClientKey(r))
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	expires, err := s.startSession(w, r, id)
	if err != nil {
		logx.Errorf("webapp", "login: tg=%d: %v", id, err)
		writeErr(w, http.StatusServiceUnavailable, "сессия не создалась")
		return
	}
	logx.Infof("panel", "login: tg=%d сессия до %s", id, expires.In(s.loc).Format("2006-01-02"))
	writeJSON(w, map[string]any{"ok": true, "expires_at": expires.UTC().Format(time.RFC3339)})
}

// handleLogout — выход: сессия удаляется на сервере, кука стирается. Работает
// и с просроченной сессией, поэтому стоит вне requireAccess.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" && s.st != nil {
		if err := s.st.DeleteWebSession(hashToken(c.Value)); err != nil {
			logx.Errorf("webapp", "logout: %v", err)
		}
	}
	clearSessionCookie(w)
	writeJSON(w, map[string]any{"ok": true})
}

// crossSite — браузер сам говорит, что запрос пришёл с чужого сайта. Для
// изменяющих запросов по куке такие отбиваем (поверх SameSite=Lax).
func crossSite(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return false
	}
	return true
}

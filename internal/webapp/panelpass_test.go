package webapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/panelpass"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// newPassServer — панель с ключом (SECRET_KEY_FILE) и общим паролем админа.
func newPassServer(t *testing.T) (*Server, []byte) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	keyFile := filepath.Join(dir, "secret.key")
	s := New(&config.Config{
		ListenAddr:    config.DefaultListen,
		Timezone:      config.DefaultTimezone,
		PanelPassword: testPassword,
		AdminID:       config.DefaultAdminID,
		Whitelist:     append([]int64(nil), config.DefaultWhitelist...),
		SecretKeyFile: keyFile,
	}, st, nil, "")
	v, err := vault.Load("", keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return s, v.Derive(panelpass.Label)
}

func callPw(s *Server, method, path, pw string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-Panel-Password", pw)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func TestPersonalPasswordIdentifiesUser(t *testing.T) {
	s, key := newPassServer(t)
	friend := config.DefaultWhitelist[1]
	pw, _ := panelpass.Issue(key, friend, time.Now())

	rec := callPw(s, http.MethodGet, "/api/me", pw)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/me: %d %s", rec.Code, rec.Body)
	}
	var me meView
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me.TelegramID != friend {
		t.Fatalf("вошёл как %d, ждали %d", me.TelegramID, friend)
	}

	var set settingsResponse
	_ = json.Unmarshal(callPw(s, http.MethodGet, "/api/settings", pw).Body.Bytes(), &set)
	if set.IsAdmin {
		t.Fatal("друг видит себя админом")
	}
	for _, path := range []string{"/api/people", "/api/logs", "/api/parser", "/api/test"} {
		if c := callPw(s, http.MethodGet, path, pw).Code; c != http.StatusForbidden {
			t.Fatalf("%s для друга: %d, ждали 403", path, c)
		}
	}
}

func TestAdminPasswords(t *testing.T) {
	s, key := newPassServer(t)
	own, _ := panelpass.Issue(key, config.DefaultAdminID, time.Now())
	for _, pw := range []string{own, testPassword} {
		var set settingsResponse
		rec := callPw(s, http.MethodGet, "/api/settings", pw)
		_ = json.Unmarshal(rec.Body.Bytes(), &set)
		if rec.Code != http.StatusOK || !set.IsAdmin {
			t.Fatalf("админ (%d символов): %d is_admin=%v", len(pw), rec.Code, set.IsAdmin)
		}
		if c := callPw(s, http.MethodGet, "/api/people", pw).Code; c != http.StatusOK {
			t.Fatalf("/api/people для админа: %d", c)
		}
	}
}

func TestPersonalPasswordRejected(t *testing.T) {
	s, key := newPassServer(t)
	stranger, _ := panelpass.Issue(key, 42, time.Now())
	old, _ := panelpass.Issue(key, config.DefaultWhitelist[1], time.Now().Add(-3*time.Hour))
	for _, pw := range []string{stranger, old, "abcd-efgh", ""} {
		if c := callPw(s, http.MethodGet, "/api/me", pw).Code; c != http.StatusUnauthorized {
			t.Fatalf("пароль %q: %d, ждали 401", pw, c)
		}
	}
}

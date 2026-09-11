package webapp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func TestPasswordOK(t *testing.T) {
	if !passwordOK("secret", "secret") {
		t.Fatal("same password should pass")
	}
	if passwordOK("secret", "Secret") {
		t.Fatal("mismatch should fail")
	}
	if passwordOK("secret", "") {
		t.Fatal("empty configured password should fail")
	}
	if passwordOK("", "secret") {
		t.Fatal("empty input should fail")
	}
	if passwordOK("ab", "abc") {
		t.Fatal("different length should fail")
	}
}

func TestPasswordFromRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/now", nil)
	r.Header.Set("X-Panel-Password", "  abc  ")
	if got := passwordFromRequest(r); got != "abc" {
		t.Fatalf("header = %q", got)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/now", nil)
	r.Header.Set("Authorization", "Bearer xyz")
	if got := passwordFromRequest(r); got != "xyz" {
		t.Fatalf("bearer = %q", got)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/now", nil)
	if got := passwordFromRequest(r); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestRequirePassword(t *testing.T) {
	s := New(&config.Config{
		ListenAddr:    config.DefaultListen,
		Timezone:      config.DefaultTimezone,
		PanelPassword: "panel-secret",
		GroupCode:     "6301-090301D",
	}, nil, nil, "")

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no password: %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Header.Set("X-Panel-Password", "wrong")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Header.Set("X-Panel-Password", "panel-secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		body, _ := io.ReadAll(rec.Body)
		t.Fatalf("ok password: %d %s", rec.Code, body)
	}
	if !strings.Contains(rec.Body.String(), "6301-090301D") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

package websess

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Кука на подпути (Moodle: Path=/moodle/) должна выгружаться и подниматься.
func TestExportCookieOnSubpath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moodle/login" {
			http.SetCookie(w, &http.Cookie{Name: "MoodleSession", Value: "abc", Path: "/moodle/"})
			return
		}
		c, err := r.Cookie("MoodleSession")
		if err != nil || c.Value != "abc" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	s, _ := New(srv.URL+"/moodle", 0)
	if _, _, err := s.Get(t.Context(), "/login", ""); err != nil {
		t.Fatal(err)
	}
	raw := s.Export()
	if !strings.Contains(raw, "MoodleSession") {
		t.Fatalf("cookie not exported: %q", raw)
	}
	s2, _ := New(srv.URL+"/moodle", 0)
	s2.Import(raw)
	resp, _, err := s2.Get(t.Context(), "/my/", "")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("restored session rejected: %v %v", resp.StatusCode, err)
	}
}

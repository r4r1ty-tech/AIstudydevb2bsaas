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

func TestURLDoesNotDuplicateBasePath(t *testing.T) {
	s, err := New("https://do.ssau.ru/moodle/", 0)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		rel  string
		want string
	}{
		{"/login/index.php", "https://do.ssau.ru/moodle/login/index.php"},
		{"/moodle/mod/quiz/view.php?id=5", "https://do.ssau.ru/moodle/mod/quiz/view.php?id=5"},
		{"/moodle", "https://do.ssau.ru/moodle"},
		{"/moodle/", "https://do.ssau.ru/moodle/"},
		{"/moodle?foo=1", "https://do.ssau.ru/moodle?foo=1"},
		{"/moodle-other/page", "https://do.ssau.ru/moodle/moodle-other/page"},
		{"https://other.example/x", "https://other.example/x"},
	}
	for _, tc := range cases {
		if got := s.URL(tc.rel); got != tc.want {
			t.Errorf("URL(%q) = %q, want %q", tc.rel, got, tc.want)
		}
	}
}

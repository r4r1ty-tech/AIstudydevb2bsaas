package websess

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

func TestDoLogsMaskedRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/index.php" && r.Method == http.MethodPost {
			http.Redirect(w, r, "/my/", http.StatusSeeOther)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	var buf bytes.Buffer
	logx.SetDebugSink(&buf, nil)
	defer logx.SetDebugSink(nil, nil)

	s, _ := New(srv.URL, 0)
	s.Name = "moodle"
	form := url.Values{"username": {"student"}, "password": {"СуперСекрет"}, "logintoken": {"tok123"}}
	req, _ := s.Request(context.Background(), http.MethodPost, "/login/index.php", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, _, err := s.Do(req); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(context.Background(), "/lib/ajax/service.php?sesskey=abc&info=x", ""); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, leak := range []string{"СуперСекрет", "tok123", "sesskey=abc"} {
		if strings.Contains(out, leak) {
			t.Fatalf("секрет %q в логе:\n%s", leak, out)
		}
	}
	for _, want := range []string{"moodle: POST /login/index.php форма{", "username=student", "password=***", "→ 200 → /my/ редиректов=1", "sesskey=%2A%2A%2A"} {
		if !strings.Contains(out, want) {
			t.Fatalf("нет %q в логе:\n%s", want, out)
		}
	}
}

package websess

import (
	"bytes"
	"context"
	"mime/multipart"
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

// TestFormForLogMasksQuizAnswers: поля ответов Moodle не попадают в debug-лог
// ни в urlencoded, ни в multipart форме.
func TestFormForLogMasksQuizAnswers(t *testing.T) {
	form := url.Values{
		"attempt":           {"999"},
		"q607183:1_choice0": {"1"},
		"q607206:2_answer":  {"секретный ответ"},
		"q607209:3_-submit": {"Проверить"},
	}
	req, err := http.NewRequest(http.MethodPost, "http://moodle/mod/quiz/processattempt.php", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	got := formForLog(req)
	if strings.Contains(got, "секретный ответ") || strings.Contains(got, "Проверить") {
		t.Fatalf("ответ вопроса в логе: %s", got)
	}
	for _, want := range []string{"q607183:1_choice0=***", "q607206:2_answer=***", "q607209:3_-submit=***", "attempt=999"} {
		if !strings.Contains(got, want) {
			t.Fatalf("нет %q в логе: %s", want, got)
		}
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"attempt":        "999",
		"q123:1_answer":  "мой ответ",
		"q123:1_choice0": "1",
		"slots":          "1,2",
	} {
		if err := mw.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err = http.NewRequest(http.MethodPost, "http://moodle/mod/quiz/processattempt.php", bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	got = formForLog(req)
	if strings.Contains(got, "мой ответ") {
		t.Fatalf("multipart-ответ в логе: %s", got)
	}
	for _, want := range []string{"q123:1_answer=***", "q123:1_choice0=***", "attempt=999", "slots=1,2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("нет %q в логе: %s", want, got)
		}
	}
}

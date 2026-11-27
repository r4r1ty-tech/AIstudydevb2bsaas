package websess

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

// Content-Length больше потолка — отказ до чтения тела.
func TestDoWithLimitContentLengthTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096))
	}))
	defer srv.Close()

	s, err := New(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp, body, err := s.DoWithLimit(mustRequest(t, s, srv.URL+"/file.bin"), 100, allowHost(srv.URL))
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
	if body != nil {
		t.Fatalf("body = %d байт, ожидали nil", len(body))
	}
	if resp == nil || resp.ContentLength != 4096 {
		t.Fatalf("resp.ContentLength = %v, ожидали 4096", resp)
	}
}

// Тело без Content-Length (chunked) больше потолка — отказ по длине чтения.
func TestDoWithLimitChunkedTooLarge(t *testing.T) {
	const limit = 1000
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		chunk := bytes.Repeat([]byte("a"), 256)
		for written := 0; written <= limit; written += len(chunk) {
			_, _ = w.Write(chunk)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	s, err := New(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, body, err := s.DoWithLimit(mustRequest(t, s, srv.URL+"/chunked.bin"), limit, allowHost(srv.URL))
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
	if body != nil {
		t.Fatalf("body = %d байт, ожидали nil", len(body))
	}
}

// Ровно потолок — это ещё не «слишком много».
func TestDoWithLimitExactLimit(t *testing.T) {
	const limit = 1024
	payload := bytes.Repeat([]byte("b"), limit)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	s, err := New(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp, body, err := s.DoWithLimit(mustRequest(t, s, srv.URL+"/exact.bin"), limit, allowHost(srv.URL))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.StatusCode != http.StatusOK || len(body) != limit {
		t.Fatalf("status=%d, len(body)=%d, ожидали 200 и %d", resp.StatusCode, len(body), limit)
	}
}

// Редирект на чужой хост блокируется до перехода: второй сервер не получает запрос.
func TestDoWithLimitBlocksForeignRedirect(t *testing.T) {
	var foreignHits int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&foreignHits, 1)
		_, _ = w.Write([]byte("secret"))
	}))
	defer foreign.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+portOf(t, foreign.URL)+"/steal", http.StatusFound)
	}))
	defer srv.Close()

	s, err := New(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.DoWithLimit(mustRequest(t, s, srv.URL+"/redirect"), 1024, allowHost(srv.URL))
	if err == nil {
		t.Fatal("ожидали ошибку на редиректе на чужой хост")
	}
	if got := atomic.LoadInt32(&foreignHits); got != 0 {
		t.Fatalf("чужой сервер получил %d запросов, ожидали 0", got)
	}
}

// checkURL вызывается и для исходного адреса, и для каждого редиректа.
func TestDoWithLimitCallsCheckURLOnEveryHop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/next", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	s, err := New(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls int32
	check := func(u *url.URL) error {
		atomic.AddInt32(&calls, 1)
		return allowHost(srv.URL)(u)
	}
	if _, _, err := s.DoWithLimit(mustRequest(t, s, srv.URL+"/start"), 1024, check); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("checkURL вызван %d раз, ожидали 2 (запрос + редирект)", got)
	}
}

// Ошибка checkURL на исходном адресе не отправляет запрос вовсе.
func TestDoWithLimitRejectsBeforeRequest(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	s, err := New(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	deny := errors.New("host denied")
	_, _, err = s.DoWithLimit(mustRequest(t, s, srv.URL+"/x"), 1024, func(*url.URL) error { return deny })
	if !errors.Is(err, deny) {
		t.Fatalf("err = %v, ожидали обёрнутую ошибку checkURL", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("сервер получил %d запросов, ожидали 0", got)
	}
}

func mustRequest(t *testing.T, s *Session, rawURL string) *http.Request {
	t.Helper()
	req, err := s.Request(t.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// allowHost — checkURL, разрешающий только указанный origin.
func allowHost(raw string) func(*url.URL) error {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return func(got *url.URL) error {
		if got.Scheme != "http" && got.Scheme != "https" {
			return errors.New("bad scheme")
		}
		if got.Host != u.Host {
			return errors.New("foreign host")
		}
		return nil
	}
}

func portOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

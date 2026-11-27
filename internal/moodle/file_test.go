package moodle

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websess"
)

func newFileClient(t *testing.T, base string) *Client {
	t.Helper()
	c, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Файл с родного хоста: allowlist по умолчанию, метаданные и имя из pluginfile.php.
func TestFetchFileLimitedAllowsBaseHost(t *testing.T) {
	payload := []byte("%PDF-1.7 test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	c := newFileClient(t, srv.URL)
	if got := c.BaseHost(); got != "127.0.0.1" {
		t.Fatalf("BaseHost() = %q, ожидали 127.0.0.1", got)
	}
	raw := srv.URL + "/pluginfile.php/1/mod_resource/content/3/lecture.pdf"
	res, err := c.FetchFileLimited(context.Background(), raw, FileLimitDefault, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(res.Data, payload) {
		t.Errorf("Data = %q, ожидали %q", res.Data, payload)
	}
	if res.ContentType != "application/pdf" {
		t.Errorf("ContentType = %q", res.ContentType)
	}
	if res.Filename != "lecture.pdf" {
		t.Errorf("Filename = %q, ожидали lecture.pdf", res.Filename)
	}
	if res.FinalURL != raw {
		t.Errorf("FinalURL = %q, ожидали %q", res.FinalURL, raw)
	}
	if res.Size != int64(len(payload)) {
		t.Errorf("Size = %d, ожидали %d", res.Size, len(payload))
	}
}

// Имя из pluginfile.php раскодируется (%20 и кириллица).
func TestFetchFileLimitedDecodesFilename(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	c := newFileClient(t, srv.URL)
	raw := srv.URL + "/pluginfile.php/1/mod_resource/content/1/%D0%BB%D0%B5%D0%BA%D1%86%D0%B8%D1%8F%201.pdf"
	res, err := c.FetchFileLimited(context.Background(), raw, FileLimitDefault, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Filename != "лекция 1.pdf" {
		t.Fatalf("Filename = %q, ожидали %q", res.Filename, "лекция 1.pdf")
	}
}

// Чужой хост отклоняется до запроса — сервер его не видит.
func TestFetchFileLimitedRejectsForeignHost(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("secret"))
	}))
	defer srv.Close()

	c := newFileClient(t, "http://moodle.invalid")
	_, err := c.FetchFileLimited(context.Background(), srv.URL+"/pluginfile.php/1/x.pdf", FileLimitDefault, nil)
	if err == nil {
		t.Fatal("ожидали ошибку для чужого хоста")
	}
	if !errors.Is(err, ErrForbiddenHost) {
		t.Fatalf("err = %v, ожидали ErrForbiddenHost", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("чужой сервер получил %d запросов, ожидали 0", got)
	}
}

// Явный allowlist разрешает дополнительный хост (сравнение без порта).
func TestFetchFileLimitedAllowsExplicitHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := newFileClient(t, "http://moodle.invalid")
	if _, err := c.FetchFileLimited(context.Background(), srv.URL+"/pluginfile.php/1/x.txt", FileLimitDefault, []string{"127.0.0.1"}); err != nil {
		t.Fatalf("err = %v, ожидали успех для хоста из allowlist", err)
	}
}

// Кросс-хостовый редирект блокируется: второй сервер запросов не получает.
func TestFetchFileLimitedRejectsCrossHostRedirect(t *testing.T) {
	var foreignHits int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&foreignHits, 1)
		_, _ = w.Write([]byte("secret"))
	}))
	defer foreign.Close()
	foreignURL, err := url.Parse(foreign.URL)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Тот же сервер, но другое имя хоста — уже чужой.
		http.Redirect(w, r, "http://localhost:"+foreignURL.Port()+"/pluginfile.php/1/steal.pdf", http.StatusFound)
	}))
	defer srv.Close()

	c := newFileClient(t, srv.URL)
	_, err = c.FetchFileLimited(context.Background(), srv.URL+"/pluginfile.php/1/x.pdf", FileLimitDefault, nil)
	if err == nil {
		t.Fatal("ожидали ошибку на кросс-хостовом редиректе")
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, не ожидали ErrUnauthorized", err)
	}
	if !errors.Is(err, ErrForbiddenHost) {
		t.Fatalf("err = %v, ожидали ErrForbiddenHost", err)
	}
	if got := atomic.LoadInt32(&foreignHits); got != 0 {
		t.Fatalf("чужой сервер получил %d запросов, ожидали 0", got)
	}
}

// Редирект на страницу входа — протухшая сессия: ErrUnauthorized и сброс sesskey.
func TestFetchFileLimitedLoginRedirect(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/login/index.php") {
			_, _ = w.Write([]byte(`<html><form id="login"></form></html>`))
			return
		}
		http.Redirect(w, r, srv.URL+"/login/index.php", http.StatusSeeOther)
	}))
	defer srv.Close()

	c := newFileClient(t, srv.URL)
	c.setSesskey("STALE_KEY")
	_, err := c.FetchFileLimited(context.Background(), srv.URL+"/pluginfile.php/1/x.pdf", FileLimitDefault, nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, ожидали ErrUnauthorized", err)
	}
	c.mu.Lock()
	key := c.sesskey
	c.mu.Unlock()
	if key != "" {
		t.Fatalf("sesskey = %q, ожидали пусто", key)
	}
}

// Файл на 9 МБ проходит: прежний потолок 8 МБ его бы обрезал.
func TestFetchFileLimitedNineMegabytes(t *testing.T) {
	const size = 9 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		chunk := bytes.Repeat([]byte("z"), 1<<20)
		for written := 0; written < size; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := newFileClient(t, srv.URL)
	res, err := c.FetchFileLimited(context.Background(), srv.URL+"/pluginfile.php/1/mod_folder/content/0/archive.zip", FileLimitDefault, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Size != size || len(res.Data) != size {
		t.Fatalf("Size = %d, len(Data) = %d, ожидали %d", res.Size, len(res.Data), size)
	}
}

// Файл больше 25 МБ — ErrBodyTooLarge.
func TestFetchFileLimitedTooLarge(t *testing.T) {
	const limit = 25 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		chunk := bytes.Repeat([]byte("z"), 1<<20)
		for written := 0; written <= limit; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := newFileClient(t, srv.URL)
	_, err := c.FetchFileLimited(context.Background(), srv.URL+"/pluginfile.php/1/big.bin", limit, nil)
	if !errors.Is(err, websess.ErrBodyTooLarge) {
		t.Fatalf("err = %v, ожидали ErrBodyTooLarge", err)
	}
}

// Не-2xx статус — типизированная HTTPStatusError, чтобы MCP-слой отличал
// 403/404 без разбора текста ошибки.
func TestFetchFileLimitedHTTPStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := newFileClient(t, srv.URL)
	_, err := c.FetchFileLimited(context.Background(), srv.URL+"/pluginfile.php/1/missing.pdf", FileLimitDefault, nil)
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v, ожидали HTTPStatusError", err)
	}
	if statusErr.Status != http.StatusNotFound {
		t.Fatalf("Status = %d, ожидали 404", statusErr.Status)
	}
}

// Не-http(s) схемы и protocol-relative адреса отклоняются до склейки с Base:
// запрос на BaseHost не уходит.
func TestFetchFileLimitedRejectsUnsafeSchemes(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := newFileClient(t, srv.URL)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		"ftp://evil.example/pluginfile.php/1/x.pdf",
		"ftp://" + base.Host + "/pluginfile.php/1/x.pdf",
		"file:///etc/passwd",
		"HTTP://evil.example/pluginfile.php/1/x.pdf",
		"//evil.example/pluginfile.php/1/x.pdf",
	}
	for _, raw := range cases {
		_, err := c.FetchFileLimited(context.Background(), raw, FileLimitDefault, nil)
		if !errors.Is(err, ErrForbiddenHost) {
			t.Errorf("%q: err = %v, ожидали ErrForbiddenHost", raw, err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("BaseHost получил %d запросов, ожидали 0", got)
	}
}

// Схема в верхнем регистре допустима, если хост свой: нормализуем до запроса.
func TestFetchFileLimitedUppercaseSchemeBaseHost(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := newFileClient(t, srv.URL)
	raw := "HTTP://" + base.Host + "/pluginfile.php/1/mod_resource/content/0/x.txt"
	res, err := c.FetchFileLimited(context.Background(), raw, FileLimitDefault, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if gotPath != "/pluginfile.php/1/mod_resource/content/0/x.txt" {
		t.Fatalf("путь запроса = %q", gotPath)
	}
	if res.Filename != "x.txt" {
		t.Fatalf("Filename = %q", res.Filename)
	}
}

func TestHostKeyNormalizes(t *testing.T) {
	cases := map[string]string{
		"Example.COM:443": "example.com",
		"example.com.":    "example.com",
		"[::1]:8443":      "::1",
		"[::1]":           "::1",
		"127.0.0.1":       "127.0.0.1",
	}
	for raw, want := range cases {
		if got := hostKey(raw); got != want {
			t.Errorf("hostKey(%q) = %q, ожидали %q", raw, got, want)
		}
	}
}

// Имя санитизируется: разделители и «..» не должны попасть в путь записи.
func TestFilenameFromURLSanitizes(t *testing.T) {
	u, err := url.Parse("https://moodle.example/pluginfile.php/1/mod_resource/content/0/%2E%2E%2Fetc%2Fpasswd")
	if err != nil {
		t.Fatal(err)
	}
	got := filenameFromURL(u)
	if strings.ContainsAny(got, `/\`) || got == ".." {
		t.Fatalf("Filename = %q, ожидали безопасное имя", got)
	}
	cases := map[string]string{
		"../etc/passwd": ".._etc_passwd",
		"a/b\\c":        "a_b_c",
		"..":            "",
		"/":             "",
		"file.pdf":      "file.pdf",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, ожидали %q", in, got, want)
		}
	}
}

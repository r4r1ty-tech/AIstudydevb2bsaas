package moodle

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websess"
)

// FileLimitDefault — обычный потолок скачивания файла Moodle (25 МБ).
const FileLimitDefault = 25 << 20

// ErrForbiddenHost — файл лежит на чужом хосте, схема не http(s) или адрес
// protocol-relative («//host»). Такие адреса отклоняются до запроса.
var ErrForbiddenHost = errors.New("moodle: file: host or scheme not allowed")

// HTTPStatusError — Moodle ответил на запрос файла кодом не 2xx.
type HTTPStatusError struct {
	URL    string // замаскированный адрес: без sesskey и токенов
	Status int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("moodle: file %s: status %d", e.URL, e.Status)
}

// FileResult — скачанный файл Moodle.
type FileResult struct {
	Data        []byte
	ContentType string
	Filename    string
	FinalURL    string
	Size        int64
}

// BaseHost — хост Moodle без порта.
func (c *Client) BaseHost() string {
	if c == nil || c.s == nil || c.s.Base == nil {
		return ""
	}
	return c.s.Base.Hostname()
}

// FetchFileLimited скачивает файл Moodle только GET-ом, не больше maxBytes и
// только с разрешённых хостов. Пустой allowedHosts — только хост Moodle.
// Сравнение хостов без порта и без учёта регистра; разрешены только http/https.
// Редирект на страницу входа — ErrUnauthorized. maxBytes <= 0 — FileLimitDefault.
func (c *Client) FetchFileLimited(ctx context.Context, rawURL string, maxBytes int64, allowedHosts []string) (FileResult, error) {
	if maxBytes <= 0 {
		maxBytes = FileLimitDefault
	}

	allowed := allowedHosts
	if len(allowed) == 0 {
		allowed = []string{c.BaseHost()}
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, h := range allowed {
		if k := hostKey(h); k != "" {
			allowedSet[k] = true
		}
	}
	// Абсолютный адрес проверяем до склейки с Base: Session.URL считает
	// внешним только префикс в нижнем регистре, иначе ftp://, HTTP:// или
	// //host ушли бы на BaseHost под видом относительного пути.
	target, err := fileTarget(rawURL, allowedSet)
	if err != nil {
		return FileResult{}, err
	}
	path := rawURL
	if target != nil {
		path = target.String()
	}
	req, err := c.s.Request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return FileResult{}, err
	}

	check := func(u *url.URL) error {
		if u == nil {
			return errors.New("moodle: file: empty url")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("%w: scheme %q", ErrForbiddenHost, u.Scheme)
		}
		if !allowedSet[hostKey(u.Hostname())] {
			return fmt.Errorf("%w: host %q", ErrForbiddenHost, u.Hostname())
		}
		if isLoginURL(u.String()) {
			return ErrUnauthorized
		}
		return nil
	}

	logURL := websess.MaskURL(req.URL)
	resp, data, err := c.s.DoWithLimit(req, maxBytes, check)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			c.setSesskey("")
			return FileResult{}, ErrUnauthorized
		}
		return FileResult{}, fmt.Errorf("moodle: file %s: %w", logURL, err)
	}
	if onLoginPage(resp) {
		c.setSesskey("")
		return FileResult{}, ErrUnauthorized
	}
	if resp.StatusCode/100 != 2 {
		logx.Debugf(logComponent, "файл %s: статус %d", logURL, resp.StatusCode)
		return FileResult{}, &HTTPStatusError{URL: logURL, Status: resp.StatusCode}
	}
	final := req.URL
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL
	}
	out := FileResult{
		Data:        data,
		ContentType: resp.Header.Get("Content-Type"),
		Filename:    filenameFromURL(final),
		FinalURL:    final.String(),
		Size:        int64(len(data)),
	}
	logx.Debugf(logComponent, "файл %s: тип %q, %d байт", websess.MaskURL(final), out.ContentType, out.Size)
	return out, nil
}

// fileTarget проверяет исходный адрес до подстановки Base. Возвращает
// абсолютный URL (nil — относительный путь, его можно склеить с Base).
// Отклоняет не-http(s) схемы, protocol-relative «//host» и чужие хосты.
func fileTarget(rawURL string, allowedSet map[string]bool) (*url.URL, error) {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return nil, errors.New("moodle: file: empty url")
	}
	if strings.HasPrefix(raw, "//") {
		return nil, fmt.Errorf("%w: protocol-relative url", ErrForbiddenHost)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("moodle: file: bad url: %w", err)
	}
	if u.Scheme == "" {
		return nil, nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q", ErrForbiddenHost, u.Scheme)
	}
	if !allowedSet[hostKey(u.Hostname())] {
		return nil, fmt.Errorf("%w: host %q", ErrForbiddenHost, u.Hostname())
	}
	return u, nil
}

// hostKey — хост для сравнения: нижний регистр, без порта и без скобок IPv6.
func hostKey(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(raw); err == nil {
		raw = h
	}
	raw = strings.TrimSuffix(raw, ".")
	return strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
}

// filenameFromURL — безопасное имя файла из последнего сегмента пути
// (pluginfile.php/…), с раскодированием %XX.
func filenameFromURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	p := strings.Trim(u.EscapedPath(), "/")
	if p == "" {
		return ""
	}
	seg := p[strings.LastIndex(p, "/")+1:]
	name, err := url.PathUnescape(seg)
	if err != nil {
		name = seg
	}
	return sanitizeFilename(name)
}

// sanitizeFilename убирает разделители и управляющие символы, чтобы имя
// годилось для записи на диск: «../etc/passwd» → «.._etc_passwd».
func sanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\':
			return '_'
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if strings.Trim(name, "._") == "" {
		return ""
	}
	return name
}

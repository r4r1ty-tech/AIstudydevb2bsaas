// Package websess — HTTP-клиент «как браузер» с куками, которые можно выгрузить
// в строку и поднять обратно: ЛК и Moodle держат сессию между синками, а не
// логинятся каждый раз.
package websess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"

	"golang.org/x/net/publicsuffix"
)

const (
	UserAgent  = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	acceptLang = "ru-RU,ru;q=0.9,en;q=0.8"
	maxBody    = 8 << 20
)

type Session struct {
	Name   string // подпись в подробном логе
	Base   *url.URL
	Client *http.Client
	jar    http.CookieJar
}

// New — сессия для одного сайта. base — origin с путём (https://do.ssau.ru/moodle).
func New(base string, timeout time.Duration) (*Session, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("websess: bad base %q", base)
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("websess: jar: %w", err)
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Session{
		Base:   u,
		jar:    jar,
		Client: &http.Client{Timeout: timeout, Jar: jar},
	}, nil
}

// URL склеивает путь сайта: "/login/index.php" -> base + путь.
// Не дублирует s.Base.Path, если path уже начинается с этого префикса.
func (s *Session) URL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	basePath := strings.TrimRight(s.Base.Path, "/")
	if basePath != "" && strings.HasPrefix(path, basePath) {
		rest := path[len(basePath):]
		if rest == "" || rest[0] == '/' || rest[0] == '?' || rest[0] == '#' {
			u := *s.Base
			u.Path = ""
			u.RawPath = ""
			u.RawQuery = ""
			u.Fragment = ""
			return strings.TrimRight(u.String(), "/") + path
		}
	}
	return s.Base.String() + "/" + strings.TrimLeft(path, "/")
}

// Request собирает запрос с заголовками браузера.
func (s *Session) Request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.URL(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Language", acceptLang)
	return req, nil
}

// Do выполняет запрос и читает тело целиком (с потолком). Каждый запрос —
// строка в подробном логе (метод, путь, форма, статус, редиректы, время,
// размер; пароли/sesskey/токены замаскированы). Ответ с ошибкой или
// выкидыванием на страницу входа сохраняется снимком целиком.
func (s *Session) Do(req *http.Request) (*http.Response, []byte, error) {
	verbose := logx.Verbose()
	start := time.Now()
	form := ""
	if verbose {
		form = formForLog(req)
	}
	redirects := 0
	resp, err := s.client(&redirects).Do(req)
	comp := s.name()
	if err != nil {
		logx.Warnf(comp, "%s %s%s → ошибка за %s: %v", req.Method, MaskURL(req.URL), form, time.Since(start).Round(time.Millisecond), err)
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if verbose {
		final := ""
		if resp.Request != nil && resp.Request.URL.String() != req.URL.String() {
			final = " → " + MaskURL(resp.Request.URL)
		}
		logx.Debugf(comp, "%s %s%s → %d%s редиректов=%d %s %dБ за %s%s", req.Method, MaskURL(req.URL), form, resp.StatusCode, final,
			redirects, resp.Header.Get("Content-Type"), len(body), time.Since(start).Round(time.Millisecond), cookieNames(resp))
		kicked := resp.Request != nil && strings.Contains(resp.Request.URL.Path, "/login") && !strings.Contains(req.URL.Path, "/login")
		if resp.StatusCode >= 400 || kicked {
			logx.Dump(comp+"-http", fmt.Sprintf("%d-%s", resp.StatusCode, strings.Trim(req.URL.Path, "/")), body)
		}
	}
	if err != nil {
		return resp, nil, err
	}
	return resp, body, nil
}

// Name — подпись в логе: «moodle», «lk».
func (s *Session) name() string {
	if s.Name != "" {
		return s.Name
	}
	return "websess"
}

// client — тот же клиент, но со счётчиком редиректов для лога.
func (s *Session) client(n *int) *http.Client {
	c := *s.Client
	prev := c.CheckRedirect
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		*n = len(via)
		if prev != nil {
			return prev(r, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &c
}

var secretKeys = []string{"password", "pass", "sesskey", "logintoken", "token", "wstoken", "key", "secret", "authtoken", "sessiontoken"}

func secretKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range secretKeys {
		if k == s || strings.HasSuffix(k, "_"+s) || strings.HasSuffix(k, "["+s+"]") {
			return true
		}
	}
	return false
}

// MaskURL — адрес для лога без секретов в query.
func MaskURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	q := c.Query()
	for k := range q {
		if secretKey(k) {
			q.Set(k, "***")
		}
	}
	c.RawQuery = q.Encode()
	if c.RawQuery == "" {
		return c.Path
	}
	return c.Path + "?" + c.RawQuery
}

// formForLog — поля формы POST (urlencoded) для лога, секреты замаскированы.
// multipart (ответы теста) — имена и значения тоже: это и надо дебажить.
func formForLog(req *http.Request) string {
	if req.Method == http.MethodGet || req.GetBody == nil {
		return ""
	}
	rc, err := req.GetBody()
	if err != nil {
		return ""
	}
	defer rc.Close()
	raw, _ := io.ReadAll(io.LimitReader(rc, 64<<10))
	ct := req.Header.Get("Content-Type")
	var parts []string
	switch {
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		vals, err := url.ParseQuery(string(raw))
		if err != nil {
			return fmt.Sprintf(" форма=%dБ", len(raw))
		}
		for k, vs := range vals {
			for _, v := range vs {
				if secretKey(k) {
					v = "***"
				}
				parts = append(parts, k+"="+clip(v, 200))
			}
		}
	case strings.HasPrefix(ct, "multipart/form-data"):
		_, params, err := mime.ParseMediaType(ct)
		if err != nil {
			return fmt.Sprintf(" форма=%dБ", len(raw))
		}
		mr := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			v, _ := io.ReadAll(io.LimitReader(part, 4096))
			k := part.FormName()
			val := string(v)
			if secretKey(k) {
				val = "***"
			}
			parts = append(parts, k+"="+clip(val, 200))
		}
	case strings.Contains(ct, "json"):
		return " json=" + clip(maskJSON(string(raw)), 2000)
	default:
		return fmt.Sprintf(" тело=%dБ", len(raw))
	}
	sort.Strings(parts)
	return " форма{" + strings.Join(parts, " ") + "}"
}

var jsonSecret = regexp.MustCompile(`(?i)"(password|sesskey|token|logintoken|secret)"\s*:\s*"[^"]*"`)

func maskJSON(s string) string { return jsonSecret.ReplaceAllString(s, `"$1":"***"`) }

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func cookieNames(resp *http.Response) string {
	var names []string
	for _, c := range resp.Cookies() {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		return ""
	}
	return " set-cookie=" + strings.Join(names, ",")
}

// Get — GET с Accept, тело и финальный ответ (после редиректов).
func (s *Session) Get(ctx context.Context, path, accept string) (*http.Response, []byte, error) {
	req, err := s.Request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return s.Do(req)
}

type storedCookie struct {
	Name    string    `json:"n"`
	Value   string    `json:"v"`
	Path    string    `json:"p,omitempty"`
	Expires time.Time `json:"e,omitempty"`
}

// Export — куки сайта одной строкой (для шифрования и записи в базу).
func (s *Session) Export() string {
	// Кука Moodle живёт на пути «/moodle/», а «/moodle» без слэша под него не подходит.
	root := *s.Base
	root.Path = strings.TrimRight(root.Path, "/") + "/"
	cs := s.jar.Cookies(&root)
	out := make([]storedCookie, 0, len(cs))
	for _, c := range cs {
		out = append(out, storedCookie{Name: c.Name, Value: c.Value})
	}
	if len(out) == 0 {
		return ""
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// Import поднимает куки из Export. Кривая строка — пустая сессия, не ошибка.
func (s *Session) Import(raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	var in []storedCookie
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return
	}
	cs := make([]*http.Cookie, 0, len(in))
	for _, c := range in {
		cs = append(cs, &http.Cookie{Name: c.Name, Value: c.Value, Path: "/"})
	}
	s.jar.SetCookies(&url.URL{Scheme: s.Base.Scheme, Host: s.Base.Host, Path: "/"}, cs)
}

// Clear выкидывает все куки (перед новым входом).
func (s *Session) Clear() error {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return err
	}
	s.jar = jar
	s.Client.Jar = jar
	return nil
}

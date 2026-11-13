// Package websess — HTTP-клиент «как браузер» с куками, которые можно выгрузить
// в строку и поднять обратно: ЛК и Moodle держат сессию между синками, а не
// логинятся каждый раз.
package websess

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

const (
	UserAgent  = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	acceptLang = "ru-RU,ru;q=0.9,en;q=0.8"
	maxBody    = 8 << 20
)

type Session struct {
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
func (s *Session) URL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
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

// Do выполняет запрос и читает тело целиком (с потолком).
func (s *Session) Do(req *http.Request) (*http.Response, []byte, error) {
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp, nil, err
	}
	return resp, body, nil
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

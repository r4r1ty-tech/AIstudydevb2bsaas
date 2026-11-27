package mcpapp

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// EnvBearerToken — переменная окружения с токеном для HTTP-транспорта.
	// Пустой токен допустим только на loopback-адресе.
	EnvBearerToken = "MOODLE_MCP_BEARER_TOKEN"

	// DefaultHTTPAddr — адрес HTTP-транспорта по умолчанию (только локально).
	DefaultHTTPAddr = "127.0.0.1:8090"
	// DefaultHTTPPath — путь MCP-эндпоинта по умолчанию.
	DefaultHTTPPath = "/mcp"
	// HealthPath — путь проверки живости, без аутентификации и секретов.
	HealthPath = "/healthz"

	// defaultMaxRequestBodyBytes ограничивает тело запроса: JSON-RPC с
	// аргументами инструментов заметно меньше.
	defaultMaxRequestBodyBytes = 4 << 20

	// defaultReadHeaderTimeout защищает от slowloris на заголовках.
	defaultReadHeaderTimeout = 10 * time.Second
	// defaultReadTimeout — чтение тела запроса (тело маленькое).
	defaultReadTimeout = 30 * time.Second
	// defaultWriteTimeout — ответ; вызовы Moodle могут идти десятки секунд.
	defaultWriteTimeout = 5 * time.Minute
	// defaultIdleTimeout — простой keep-alive соединения.
	defaultIdleTimeout = 2 * time.Minute
)

// HTTPOptions — настройки HTTP-транспорта MCP.
//
// Addr используется только для проверки безопасности (нужен ли токен) и для
// http.Server.Addr; сам слушатель создаёт вызывающий код.
type HTTPOptions struct {
	// Addr — адрес прослушивания, например "127.0.0.1:8090".
	Addr string
	// Path — путь MCP-эндпоинта, по умолчанию "/mcp".
	Path string
	// BearerToken — ожидаемый токен. Пустое значение означает чтение из
	// MOODLE_MCP_BEARER_TOKEN. Токен никогда не логируется.
	BearerToken string
	// ExtraOrigins — дополнительные разрешённые Origin: либо полные
	// источники ("https://example.org:8443"), либо имена хостов.
	ExtraOrigins []string
	// Logger — логгер HTTP-слоя; по умолчанию stderr, уровень Warn.
	Logger *slog.Logger
	// JSONResponse отдаёт application/json вместо text/event-stream.
	JSONResponse bool

	// Таймауты; нулевые значения заменяются разумными по умолчанию.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	// MaxRequestBodyBytes ограничивает тело запроса; <=0 — значение по умолчанию.
	MaxRequestBodyBytes int64
}

// ValidateHTTPOptions заполняет значения по умолчанию и проверяет, что
// конфигурация безопасна: при не-loopback адресе токен обязателен.
//
// Функция меняет opts на месте, поэтому вызывающий код получает
// нормализованные значения для NewHTTPHandler и NewHTTPServer.
func ValidateHTTPOptions(opts *HTTPOptions) error {
	if opts == nil {
		return errors.New("mcpapp: пустые HTTP-настройки")
	}
	if err := opts.normalize(); err != nil {
		return err
	}
	return nil
}

func (o *HTTPOptions) normalize() error {
	if o.Addr == "" {
		o.Addr = DefaultHTTPAddr
	}
	host, _, err := net.SplitHostPort(o.Addr)
	if err != nil {
		return fmt.Errorf("mcpapp: некорректный http-адрес %q: %w", o.Addr, err)
	}
	loopback := isLoopbackHost(host)

	if o.Path == "" {
		o.Path = DefaultHTTPPath
	}
	if !strings.HasPrefix(o.Path, "/") {
		o.Path = "/" + o.Path
	}
	if len(o.Path) > 1 {
		o.Path = strings.TrimRight(o.Path, "/")
	}
	if strings.ContainsAny(o.Path, " \t\r\n") || strings.Contains(o.Path, "?") {
		return fmt.Errorf("mcpapp: некорректный http-путь %q", o.Path)
	}
	if o.Path == "/" {
		return errors.New("mcpapp: http-путь не может быть корнем")
	}
	if o.Path == HealthPath {
		return fmt.Errorf("mcpapp: путь %q зарезервирован под healthz", HealthPath)
	}

	o.BearerToken = strings.TrimSpace(o.BearerToken)
	if o.BearerToken == "" {
		o.BearerToken = strings.TrimSpace(os.Getenv(EnvBearerToken))
	}
	if err := checkBearerTokenFormat(o.BearerToken); err != nil {
		return err
	}
	if o.BearerToken == "" && !loopback {
		return fmt.Errorf("mcpapp: адрес %q не loopback, нужен %s", o.Addr, EnvBearerToken)
	}

	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	if o.ReadHeaderTimeout <= 0 {
		o.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if o.ReadTimeout <= 0 {
		o.ReadTimeout = defaultReadTimeout
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = defaultWriteTimeout
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = defaultIdleTimeout
	}
	if o.MaxRequestBodyBytes <= 0 {
		o.MaxRequestBodyBytes = defaultMaxRequestBodyBytes
	}
	if _, err := newOriginChecker(o.ExtraOrigins); err != nil {
		return err
	}
	return nil
}

// checkBearerTokenFormat отклоняет токен с пробелами/управляющими символами:
// такой нельзя передать в заголовке Authorization. Сам токен в ошибку не
// попадает.
func checkBearerTokenFormat(token string) error {
	for _, r := range token {
		if r <= 0x20 || r == 0x7f {
			return fmt.Errorf("mcpapp: %s содержит пробелы или управляющие символы", EnvBearerToken)
		}
	}
	return nil
}

// isLoopbackHost сообщает, указывает ли хост только на петлю. Пустой хост
// (все интерфейсы) и DNS-имена, кроме localhost, считаются внешними.
func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// NewHTTPHandler собирает HTTP-обработчик MCP: /mcp (Streamable HTTP без
// серверных сессий) и /healthz.
//
// Вне loopback-адреса обязателен Bearer-токен. Запросы с заголовком Origin
// пропускаются только с localhost/127.0.0.1/[::1] (плюс ExtraOrigins), что
// вместе со встроенной защитой SDK закрывает DNS rebinding.
func NewHTTPHandler(server *mcp.Server, opts HTTPOptions) (http.Handler, error) {
	if server == nil {
		return nil, errors.New("mcpapp: nil MCP-сервер")
	}
	if err := opts.normalize(); err != nil {
		return nil, err
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:           true,
			JSONResponse:        opts.JSONResponse,
			Logger:              opts.Logger,
			MaxRequestBodyBytes: opts.MaxRequestBodyBytes,
		},
	)

	checkOrigin, err := newOriginChecker(opts.ExtraOrigins)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle(opts.Path, requireBearerToken(opts.BearerToken, opts.Logger, checkOrigin(mcpHandler)))
	mux.HandleFunc(HealthPath, healthz)
	return securityHeaders(mux), nil
}

// NewHTTPServer создаёт http.Server с таймаутами из opts.
func NewHTTPServer(handler http.Handler, opts HTTPOptions) *http.Server {
	if opts.ReadHeaderTimeout <= 0 {
		opts.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = defaultReadTimeout
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultWriteTimeout
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = defaultIdleTimeout
	}
	return &http.Server{
		Addr:              opts.Addr,
		Handler:           handler,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		ReadTimeout:       opts.ReadTimeout,
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
		MaxHeaderBytes:    1 << 16,
	}
}

// requireBearerToken пропускает запросы только с точным Bearer-токеном.
// Пустой token отключает проверку (допустимо только на loopback).
func requireBearerToken(token string, logger *slog.Logger, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Несколько заголовков Authorization — неоднозначность: разные
		// прокси/клиенты выберут разные значения, поэтому отклоняем целиком.
		values := r.Header.Values("Authorization")
		var got string
		ok := false
		if len(values) == 1 {
			got, ok = bearerFromHeader(values[0])
		}
		if !ok || !tokenEqual(got, token) {
			logger.Warn("mcpapp: отклонён запрос без верного bearer-токена",
				"method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerFromHeader разбирает "Bearer <token>" без учёта регистра схемы.
func bearerFromHeader(header string) (string, bool) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(rest)
	if token == "" {
		return "", false
	}
	return token, true
}

// tokenEqual сравнивает токены за постоянное время. Хэширование уравнивает
// длины, чтобы сравнение не зависело от длины секрета.
func tokenEqual(got, want string) bool {
	gotSum := sha256.Sum256([]byte(got))
	wantSum := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) == 1
}

// originChecker проверяет Origin браузерных запросов.
type originChecker struct {
	hosts   map[string]struct{}
	origins map[string]struct{}
}

// newOriginChecker собирает allowlist: localhost/127.0.0.1/[::1] плюс extra.
func newOriginChecker(extra []string) (func(http.Handler) http.Handler, error) {
	chk := &originChecker{
		hosts: map[string]struct{}{
			"localhost": {},
			"127.0.0.1": {},
			"::1":       {},
		},
		origins: make(map[string]struct{}),
	}
	for _, raw := range extra {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "://") {
			u, err := url.Parse(raw)
			if err != nil || u.Hostname() == "" {
				return nil, fmt.Errorf("mcpapp: некорректный origin %q", raw)
			}
			chk.origins[normalizeOrigin(u)] = struct{}{}
			continue
		}
		// Голое имя хоста или host:port — разрешаем любой порт.
		host := raw
		if h, _, err := net.SplitHostPort(raw); err == nil {
			host = h
		}
		chk.hosts[strings.ToLower(strings.Trim(host, "[]"))] = struct{}{}
	}
	return chk.middleware, nil
}

func (c *originChecker) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			// Браузер всегда присылает Origin на cross-origin POST. Его
			// отсутствие при cross-site режиме — тоже отказ.
			if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !c.allowed(origin) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (c *originChecker) allowed(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if _, ok := c.hosts[host]; ok {
		return true
	}
	_, ok := c.origins[normalizeOrigin(u)]
	return ok
}

// normalizeOrigin приводит источник к "scheme://host[:port]" в нижнем регистре.
func normalizeOrigin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if port := u.Port(); port != "" {
		return scheme + "://" + net.JoinHostPort(host, port)
	}
	return scheme + "://" + host
}

// healthz отвечает 200 без аутентификации и без секретов.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// securityHeaders добавляет базовые заголовки безопасности ко всем ответам.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

package mcpapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// ErrNoCredentials — нет ни сохранённого пароля, ни пары из env, а сессия
// недействительна, так что войти нечем.
var ErrNoCredentials = errors.New("mcpapp: нет логина и пароля Moodle")

const maxWarnings = 20

// Course — компактное представление курса для MCP-ответа.
type Course struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ShortName string `json:"shortname"`
	Hidden    bool   `json:"hidden"`
	URL       string `json:"url"`
}

// State — снимок состояния сессии для moodle_status.
type State struct {
	Authenticated bool
	Login         string
	BaseURL       string
	LastRefresh   time.Time
	Warnings      []string
}

// Session держит один клиент Moodle и сериализует вход/запросы. Два инструмента
// не должны логиниться одновременно: mu покрывает и ensure, и сам запрос.
type Session struct {
	cfg    Config
	client *moodle.Client

	// caches и limit общие для всех инструментов: структура курса и материал
	// кэшируются на TTL, а запросы к Moodle ограничены семафором.
	caches *toolCaches
	limit  *Limiter

	mu            sync.Mutex
	login         string
	password      string
	seedSession   string // расшифрованная сессия из bot.db, если файла кук ещё нет
	authenticated bool
	lastRefresh   time.Time
	warnings      []string
}

// NewSession готовит клиент и пытается восстановить или создать сессию.
//
// Ошибки конфигурации (битая база, отсутствующий ключ vault, каталог состояния)
// возвращаются сразу. Сетевые сбои Moodle не роняют сервер: они попадают в
// warnings, а инструменты повторят попытку при вызове.
func NewSession(ctx context.Context, cfg Config) (*Session, error) {
	// Известное ограничение: internal/moodle.New жёстко задаёт HTTP-timeout 40s.
	// cfg.Timeout больше 40s действует только как deadline контекста и не
	// расширяет HTTP-клиент; исправление — в T3/T4 вместе с internal/moodle.
	client, err := moodle.New(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("mcpapp: клиент Moodle: %w", err)
	}
	s := &Session{
		cfg:    cfg,
		client: client,
		caches: newToolCaches(),
		limit:  NewLimiter(defaultToolConcurrency),
	}
	if cfg.DBPath != "" {
		login, password, session, warnings, err := loadDBAccount(cfg.DBPath, cfg.SecretKeyFile)
		if err != nil {
			return nil, err
		}
		s.login, s.password, s.seedSession = login, password, session
		for _, w := range warnings {
			s.warn(w)
		}
	}
	switch {
	case cfg.Login != "" && cfg.Password != "":
		s.login, s.password = cfg.Login, cfg.Password
	case cfg.Login != "" || cfg.Password != "":
		s.warn("MOODLE_MCP_LOGIN и MOODLE_MCP_PASSWORD заданы не вместе — использую данные из базы")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("mcpapp: каталог состояния %s: %w", cfg.StateDir, err)
	}
	loginCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if err := s.ensureLocked(loginCtx); err != nil {
		s.warn("старт: " + err.Error())
		logx.Warnf("mcpapp", "старт: сессия не готова: %v", err)
	} else {
		logx.Infof("mcpapp", "старт: сессия Moodle готова")
	}
	return s, nil
}

// State возвращает состояние сессии, при необходимости пытаясь войти.
func (s *Session) State(ctx context.Context) State {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(ctx); err != nil {
		s.warn("сессия: " + err.Error())
		logx.Warnf("mcpapp", "статус: %v", err)
	}
	return State{
		Authenticated: s.authenticated,
		Login:         s.login,
		BaseURL:       s.cfg.BaseURL,
		LastRefresh:   s.lastRefresh,
		Warnings:      append([]string(nil), s.warnings...),
	}
}

// Courses возвращает все курсы студента, включая скрытые (поле hidden).
func (s *Session) Courses(ctx context.Context) ([]Course, error) {
	var out []Course
	err := s.do(ctx, func(ctx context.Context) error {
		courses, err := s.fetchCoursesLocked(ctx)
		if err != nil {
			return err
		}
		out = courses
		return nil
	})
	return out, err
}

// WithClient выполняет fn с клиентом Moodle под общим мьютексом сессии.
// Повтор после протухшей сессии делает тот же do: новый клиент не создаётся,
// параллельные инструменты не логинятся одновременно.
func (s *Session) WithClient(ctx context.Context, fn func(context.Context, *moodle.Client) error) error {
	return s.do(ctx, func(ctx context.Context) error {
		return fn(ctx, s.client)
	})
}

// do выполняет запрос под общим мьютексом. Протухшая посреди работы сессия
// сбрасывается, после чего выполняется один повторный вход и повтор запроса.
func (s *Session) do(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLocked(ctx); err != nil {
		return err
	}
	err := fn(ctx)
	if !errors.Is(err, moodle.ErrUnauthorized) {
		return err
	}
	logx.Debugf("mcpapp", "сессия протухла во время запроса — повторный вход")
	s.authenticated = false
	s.client.Restore("") // сбрасывает sesskey, куки остаются до Login
	if lerr := s.ensureLocked(ctx); lerr != nil {
		return lerr
	}
	err = fn(ctx)
	if errors.Is(err, moodle.ErrUnauthorized) {
		s.authenticated = false
	}
	return err
}

// ensureLocked восстанавливает сессию из кук или входит по паролю.
// Вызывается только под s.mu.
func (s *Session) ensureLocked(ctx context.Context) error {
	if s.authenticated {
		return nil
	}
	raw := s.readCookieFile()
	if raw == "" {
		raw = s.seedSession
	}
	if raw != "" {
		s.client.Restore(raw)
		if err := s.client.Check(ctx); err != nil {
			logx.Debugf("mcpapp", "сохранённая сессия не прошла проверку: %v", err)
			if ctx.Err() != nil {
				return fmt.Errorf("mcpapp: проверка сессии: %w", err)
			}
		} else {
			courses, cerr := s.fetchCoursesLocked(ctx)
			switch {
			case cerr == nil && len(courses) > 0:
				s.markAuthenticated()
				logx.Debugf("mcpapp", "сессия восстановлена из кук, курсов %d", len(courses))
				return nil
			case cerr == nil:
				// Известная особенность Moodle: Check проходит, а Courses пуст.
				s.markAuthenticated()
				s.warn("по сохранённой сессии Moodle вернул 0 курсов")
				if !s.hasPassword() {
					s.warn("перелогин невозможен: пароль не сохранён")
					return nil
				}
				s.warn("сессия восстановлена, но курсов 0 — выполняю вход по паролю")
				s.authenticated = false // проверим вход заново
			case errors.Is(cerr, moodle.ErrUnauthorized):
				s.authenticated = false
			default:
				s.authenticated = false
				return fmt.Errorf("mcpapp: курсы: %w", cerr)
			}
		}
	}
	if !s.hasPassword() {
		return ErrNoCredentials
	}
	if err := s.client.Login(ctx, s.login, s.password); err != nil {
		s.authenticated = false
		return fmt.Errorf("mcpapp: вход: %w", err)
	}
	s.authenticated = false // флаги — только после успешного чтения курсов
	s.seedSession = ""
	s.saveCookie(s.client.Session())
	courses, cerr := s.fetchCoursesLocked(ctx)
	if cerr != nil {
		return fmt.Errorf("mcpapp: курсы после входа: %w", cerr)
	}
	s.markAuthenticated()
	logx.Infof("mcpapp", "вход в Moodle выполнен, сессия сохранена")
	if len(courses) == 0 {
		s.warn("после входа Moodle вернул 0 курсов")
	}
	return nil
}

// markAuthenticated фиксирует рабочую сессию; вызывается только после того,
// как fetchCoursesLocked вернул ошибку nil.
func (s *Session) markAuthenticated() {
	s.authenticated = true
	s.lastRefresh = time.Now()
}

func (s *Session) hasPassword() bool {
	return strings.TrimSpace(s.login) != "" && s.password != ""
}

// fetchCoursesLocked читает курсы тем же AJAX-методом, что и internal/moodle,
// но сохраняет shortname, который нужен MCP-ответу.
func (s *Session) fetchCoursesLocked(ctx context.Context) ([]Course, error) {
	var res struct {
		Courses []struct {
			ID        int64  `json:"id"`
			FullName  string `json:"fullname"`
			ShortName string `json:"shortname"`
			Hidden    bool   `json:"hidden"`
			ViewURL   string `json:"viewurl"`
		} `json:"courses"`
	}
	err := s.client.Ajax(ctx, "core_course_get_enrolled_courses_by_timeline_classification", map[string]any{
		"offset": 0, "limit": 0, "classification": "all", "sort": "fullname",
	}, &res)
	if err != nil {
		return nil, err
	}
	out := make([]Course, 0, len(res.Courses))
	for _, cr := range res.Courses {
		viewURL := cr.ViewURL
		if viewURL == "" {
			viewURL = strings.TrimRight(s.cfg.BaseURL, "/") + "/course/view.php?id=" + strconv.FormatInt(cr.ID, 10)
		}
		out = append(out, Course{
			ID:        cr.ID,
			Name:      cr.FullName,
			ShortName: cr.ShortName,
			Hidden:    cr.Hidden,
			URL:       viewURL,
		})
	}
	logx.Debugf("mcpapp", "курсы: %d", len(out))
	return out, nil
}

// loadDBAccount читает первый moodle-аккаунт из bot.db и расшифровывает сессию
// и пароль ключом vault. База открывается строго read-only: MCP не должен
// запускать миграции и вообще что-либо писать в чужую bot.db (readonly-копия
// иначе падает на migrate/UPDATE). Проблемы с отдельными полями —
// предупреждения, а не отказ: env-пара может оказаться рабочей.
func loadDBAccount(dbPath, keyPath string) (login, password, session string, warnings []string, err error) {
	if keyPath == "" {
		return "", "", "", nil, fmt.Errorf("mcpapp: MOODLE_MCP_DB задан, но MOODLE_MCP_SECRET_KEY_FILE пуст")
	}
	if _, statErr := os.Stat(dbPath); statErr != nil {
		return "", "", "", nil, fmt.Errorf("mcpapp: база %s: %w", dbPath, statErr)
	}
	if _, statErr := os.Stat(keyPath); statErr != nil {
		return "", "", "", nil, fmt.Errorf("mcpapp: ключ vault %s: %w", keyPath, statErr)
	}
	v, err := vault.Load("", keyPath)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("mcpapp: ключ vault: %w", err)
	}
	// mode=ro + query_only(1): даже случайный UPDATE/PRAGMA-init не пройдёт.
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(8000)")
	if err != nil {
		return "", "", "", nil, fmt.Errorf("mcpapp: открыть базу %s: %w", dbPath, err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			logx.Warnf("mcpapp", "закрыть базу: %v", cerr)
		}
	}()
	var loginRaw, secret, sess sql.NullString
	rowErr := db.QueryRow(`SELECT login, secret, session FROM accounts WHERE kind = 'moodle' ORDER BY telegram_id LIMIT 1`).
		Scan(&loginRaw, &secret, &sess)
	if errors.Is(rowErr, sql.ErrNoRows) {
		return "", "", "", []string{"в базе нет аккаунта Moodle"}, nil
	}
	if rowErr != nil {
		return "", "", "", nil, fmt.Errorf("mcpapp: чтение accounts из %s: %w", dbPath, rowErr)
	}
	login = strings.TrimSpace(loginRaw.String)
	if secret.Valid && secret.String != "" {
		var oerr error
		password, oerr = v.Open(secret.String)
		if oerr != nil {
			password = ""
			warnings = append(warnings, "пароль Moodle из базы не расшифровался")
		}
	}
	if sess.Valid && sess.String != "" {
		var oerr error
		session, oerr = v.Open(sess.String)
		if oerr != nil {
			session = ""
			warnings = append(warnings, "сессия Moodle из базы не расшифровалась")
		}
	}
	logx.Debugf("mcpapp", "аккаунт Moodle из базы: логин задан=%v, пароль задан=%v, сессия задана=%v",
		login != "", password != "", session != "")
	return login, password, session, warnings, nil
}

// readCookieFile читает куки из state dir. Битый или недоступный файл — это
// отсутствие кук, а не отказ сервера.
func (s *Session) readCookieFile() string {
	path := s.cfg.SessionFile
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		s.warn("session.json не прочитан: " + err.Error())
		return ""
	}
	if info.Mode().Perm()&0o077 != 0 {
		if cerr := os.Chmod(path, 0o600); cerr != nil {
			s.warn("session.json: не удалось выставить права 0600")
		} else {
			s.warn("session.json: права были шире 0600, исправлены")
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		s.warn("session.json не прочитан: " + err.Error())
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// saveCookie атомарно пишет куки в session.json с правами 0600.
func (s *Session) saveCookie(raw string) {
	if raw == "" {
		return
	}
	dir := filepath.Dir(s.cfg.SessionFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.warn("session.json не сохранён: " + err.Error())
		return
	}
	tmp, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		s.warn("session.json не сохранён: " + err.Error())
		return
	}
	name := tmp.Name()
	defer func() {
		_ = os.Remove(name)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		s.warn("session.json не сохранён: " + err.Error())
		return
	}
	if _, err := tmp.WriteString(raw); err != nil {
		_ = tmp.Close()
		s.warn("session.json не сохранён: " + err.Error())
		return
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		s.warn("session.json не сохранён: " + err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		s.warn("session.json не сохранён: " + err.Error())
		return
	}
	if err := os.Rename(name, s.cfg.SessionFile); err != nil {
		s.warn("session.json не сохранён: " + err.Error())
		return
	}
	logx.Debugf("mcpapp", "сессия сохранена в %s", s.cfg.SessionFile)
}

// warn добавляет предупреждение без дублей, сохраняя последние maxWarnings.
// Вызывается под s.mu либо до публикации сессии наружу.
func (s *Session) warn(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	for _, w := range s.warnings {
		if w == msg {
			return
		}
	}
	s.warnings = append(s.warnings, msg)
	if len(s.warnings) > maxWarnings {
		s.warnings = s.warnings[len(s.warnings)-maxWarnings:]
	}
}

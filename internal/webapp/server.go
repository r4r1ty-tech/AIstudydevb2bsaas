package webapp

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

//go:embed static
var staticFS embed.FS

type RefreshFunc func(ctx context.Context) (model.ParseRun, error)

type Server struct {
	cfg           *config.Config
	src           Source
	moodle        Moodle
	refresh       RefreshFunc
	recordingsDir string
	loc           *time.Location
	clock         func() time.Time
	demo          bool
	allow         []netip.Prefix
	allowErr      error
	handler       http.Handler
}

// New — панель поверх настоящей базы. st может быть nil (тесты статики и пароля).
func New(cfg *config.Config, st *store.Store, refresh RefreshFunc, recordingsDir string) *Server {
	logx.Debugf("webapp", "New: enter cfg_nil=%v store_nil=%v refresh_nil=%v recordings_dir=%q", cfg == nil, st == nil, refresh == nil, recordingsDir)
	var src Source
	if st != nil {
		src = st
	}
	var md Moodle = moodleStub{}
	if st != nil && cfg != nil && (cfg.SecretKey != "" || cfg.SecretKeyFile != "") {
		if v, err := vault.Load(cfg.SecretKey, cfg.SecretKeyFile); err != nil {
			logx.Errorf("webapp", "New: vault: %v — Moodle в панели выключен", err)
		} else {
			md = &storeMoodle{st: st, v: v, owner: cfg.AdminID, lkBase: cfg.LKBaseURL, moodleBase: cfg.MoodleBaseURL}
		}
	}
	return buildServer(cfg, src, md, refresh, recordingsDir, nil, false)
}

func buildServer(cfg *config.Config, src Source, moodle Moodle, refresh RefreshFunc, recordingsDir string, clock func() time.Time, demo bool) *Server {
	if cfg == nil {
		cfg = &config.Config{
			ListenAddr: config.DefaultListen,
			Timezone:   config.DefaultTimezone,
			AdminID:    config.DefaultAdminID,
			Whitelist:  append([]int64(nil), config.DefaultWhitelist...),
		}
	}
	if recordingsDir == "" {
		recordingsDir = cfg.RecordingsDir
	}
	if moodle == nil {
		moodle = moodleStub{}
	}
	if clock == nil {
		clock = time.Now
	}
	s := &Server{
		cfg:           cfg,
		src:           src,
		moodle:        moodle,
		refresh:       refresh,
		recordingsDir: recordingsDir,
		loc:           loadLocation(cfg.Timezone),
		clock:         clock,
		demo:          demo,
	}
	s.allow, s.allowErr = ParseAllowList(cfg.PanelAllowIPs)
	if s.allowErr != nil {
		logx.Errorf("webapp", "New: PANEL_ALLOW_IPS: %v; панель закрыта для всех", s.allowErr)
	}
	s.handler = s.routes()
	logx.Debugf("webapp", "New: exit listen=%q tz=%s whitelist=%d demo=%v allow=%d", s.cfg.ListenAddr, s.loc.String(), len(s.cfg.Whitelist), demo, len(s.allow))
	return s
}

func loadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil || loc == nil {
		logx.Warnf("webapp", "New: timezone %q load failed: %v; falling back to %q", name, err, config.DefaultTimezone)
		loc, err = time.LoadLocation(config.DefaultTimezone)
	}
	if err != nil || loc == nil {
		logx.Warnf("webapp", "New: default timezone load failed: %v; using fixed Samara +04", err)
		loc = time.FixedZone("Samara", 4*3600)
	}
	return loc
}

func (s *Server) now() time.Time {
	return s.clock().In(s.loc)
}

func (s *Server) Handler() http.Handler {
	logx.Debugf("webapp", "Handler: -> http.Handler")
	return s.handler
}

func (s *Server) Listen(ctx context.Context) error {
	logx.Debugf("webapp", "Listen: enter")
	if ctx == nil {
		ctx = context.Background()
	}
	addr := s.cfg.ListenAddr
	if addr == "" {
		addr = config.DefaultListen
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	logx.Infof("webapp", "Listen: server starting on %s", addr)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logx.Infof("webapp", "Listen: context done, shutting down")
		shCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := srv.Shutdown(shCtx); err != nil {
			logx.Errorf("webapp", "Listen: shutdown: %v", err)
			if cerr := srv.Close(); cerr != nil {
				logx.Errorf("webapp", "Listen: close after failed shutdown: %v", cerr)
			}
			return err
		}
		err := <-errCh
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			logx.Infof("webapp", "Listen: stopped cleanly")
			return nil
		}
		logx.Errorf("webapp", "Listen: serve after shutdown: %v", err)
		return err
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			logx.Infof("webapp", "Listen: server closed")
			return nil
		}
		logx.Errorf("webapp", "Listen: serve: %v", err)
		return err
	}
}

func (s *Server) routes() http.Handler {
	logx.Debugf("webapp", "routes: registering handlers")
	if s.allowErr != nil {
		err := s.allowErr
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logx.Warnf("panel", "allow: список IP не разобран (%v), отказ %s %s", err, r.Method, r.URL.Path)
			http.Error(w, "forbidden: PANEL_ALLOW_IPS", http.StatusForbidden)
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.Handle("GET /static/", staticHeaders(http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(staticFS, "static/img/icon-32.png")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(data)
	})

	api := http.NewServeMux()
	// Пульт (вкладка «Служебное»).
	api.HandleFunc("GET /api/now", s.handleNow)
	api.HandleFunc("GET /api/people", s.handlePeople)
	api.HandleFunc("POST /api/people/{id}", s.handlePeoplePatch)
	api.HandleFunc("GET /api/lessons", s.handleLessons)
	api.HandleFunc("POST /api/bbb", s.handleBBB)
	api.HandleFunc("GET /api/parser", s.handleParser)
	api.HandleFunc("POST /api/parser/refresh", s.handleParserRefresh)
	api.HandleFunc("GET /api/logs", s.handleLogs)
	api.HandleFunc("GET /api/settings", s.handleSettingsGet)
	api.HandleFunc("GET /api/test", s.handleTestGet)
	api.HandleFunc("POST /api/test", s.handleTestPost)
	// Личная панель владельца.
	api.HandleFunc("GET /api/day", s.handleDay)
	api.HandleFunc("POST /api/day", s.handleDayMode)
	api.HandleFunc("GET /api/week", s.handleWeek)
	api.HandleFunc("POST /api/intent", s.handleIntent)
	api.HandleFunc("GET /api/deadlines", s.handleDeadlines)
	api.HandleFunc("POST /api/moodle/login", s.handleMoodleLogin)
	api.HandleFunc("POST /api/moodle/logout", s.handleMoodleLogout)
	api.HandleFunc("GET /api/notes", s.handleNotes)
	api.HandleFunc("GET /api/me", s.handleMeGet)
	api.HandleFunc("POST /api/me", s.handleMePost)
	api.HandleFunc("GET /api/rooms", s.handleRooms)

	mux.Handle("/api/", s.requireAccess(api))
	logx.Debugf("webapp", "routes: done")
	return AllowIPs(s.allow, s.cfg.PanelTrustProxy, logRequests(mux))
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	logx.Debugf("webapp", "statusWriter.WriteHeader: code=%d", code)
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	if err != nil {
		logx.Debugf("webapp", "statusWriter.Write: status=%d bytes=%d err=%v", w.status, n, err)
	}
	return n, err
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logx.Debugf("webapp", "logRequests: %s %s start", r.Method, r.URL.Path)
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		line := fmt.Sprintf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
		if sw.status >= 400 {
			logx.Warnf("panel", "%s", line)
			return
		}
		logx.Debugf("panel", "%s", line)
	})
}

// staticHeaders: шрифты кешируются надолго (имена файлов не меняются вместе
// с содержимым редко, а весят они больше всего), остальное — no-store, чтобы
// после деплоя сразу подхватывались свежие скрипты и стили.
func staticHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logx.Debugf("webapp", "staticHeaders: %s %s", r.Method, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, ".woff2"):
			w.Header().Set("Content-Type", "font/woff2")
			w.Header().Set("Cache-Control", "public, max-age=2592000")
		case strings.HasSuffix(r.URL.Path, ".js"):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
		case strings.HasSuffix(r.URL.Path, ".webmanifest"):
			w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasSuffix(r.URL.Path, ".png"), strings.HasSuffix(r.URL.Path, ".webp"):
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasSuffix(r.URL.Path, ".svg"):
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Cache-Control", "no-store")
		default:
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	logx.Debugf("webapp", "handleIndex: %s %s", r.Method, r.URL.Path)
	data, err := fs.ReadFile(staticFS, "static/index.html")
	if err != nil {
		logx.Errorf("webapp", "handleIndex: read static/index.html: %v", err)
		http.Error(w, "index missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		logx.Errorf("webapp", "handleIndex: write response: %v", err)
	}
	logx.Debugf("webapp", "handleIndex: served %d bytes", len(data))
}

// apiAllowed — правило входа в API: пароль задан и совпал; или пароля нет,
// но панель закрыта непустым PANEL_ALLOW_IPS (чужие IP уже отсёк AllowIPs);
// или демо. Ни пароля, ни списка IP — отказ.
func (s *Server) apiAllowed(r *http.Request) bool {
	if s.demo {
		return true
	}
	want := ""
	if s.cfg != nil {
		want = s.cfg.PanelPassword
	}
	if want != "" {
		return passwordOK(passwordFromRequest(r), want)
	}
	return len(s.allow) > 0
}

func (s *Server) requireAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logx.Debugf("webapp", "requireAccess: %s %s demo=%v allow=%d", r.Method, r.URL.Path, s.demo, len(s.allow))
		if !s.apiAllowed(r) {
			logx.Warnf("webapp", "requireAccess: auth failed %s %s", r.Method, r.URL.Path)
			logx.Warnf("panel", "bad password %s", r.URL.Path)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	logx.Debugf("webapp", "writeJSON: enter")
	writeJSONStatus(w, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	logx.Debugf("webapp", "writeJSONStatus: code=%d", code)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		logx.Errorf("webapp", "writeJSONStatus: encode response code=%d: %v", code, err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	logx.Debugf("webapp", "writeErr: code=%d msg=%q", code, msg)
	writeJSONStatus(w, code, map[string]string{"error": msg})
}

func decodeJSON(r io.Reader, dst any) error {
	logx.Debugf("webapp", "decodeJSON: enter")
	err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(dst)
	if err != nil {
		logx.Debugf("webapp", "decodeJSON: error: %v", err)
	}
	return err
}

// bbbPrefix — единственный разрешённый вид ссылки на комнату. Отдаётся в
// /api/settings, чтобы адрес вуза не был зашит в саму страницу.
const bbbPrefix = "https://bbb.ssau.ru/b/"

func bbbHostOK(u string) bool {
	logx.Debugf("webapp", "bbbHostOK: url=%q", u)
	u = strings.TrimSpace(u)
	ok := strings.HasPrefix(u, bbbPrefix)
	logx.Debugf("webapp", "bbbHostOK: url=%q -> %v", u, ok)
	return ok
}

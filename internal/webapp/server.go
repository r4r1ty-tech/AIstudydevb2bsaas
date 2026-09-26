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
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

//go:embed static
var staticFS embed.FS

type RefreshFunc func(ctx context.Context) (model.ParseRun, error)

type Server struct {
	cfg           *config.Config
	st            *store.Store
	refresh       RefreshFunc
	recordingsDir string
	loc           *time.Location
	handler       http.Handler
}

func New(cfg *config.Config, st *store.Store, refresh RefreshFunc, recordingsDir string) *Server {
	logx.Debugf("webapp", "New: enter cfg_nil=%v store_nil=%v refresh_nil=%v recordings_dir=%q", cfg == nil, st == nil, refresh == nil, recordingsDir)
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
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil || loc == nil {
		logx.Warnf("webapp", "New: timezone %q load failed: %v; falling back to %q", cfg.Timezone, err, config.DefaultTimezone)
		loc, err = time.LoadLocation(config.DefaultTimezone)
	}
	if err != nil || loc == nil {
		logx.Warnf("webapp", "New: default timezone load failed: %v; using fixed Samara +04", err)
		loc = time.FixedZone("Samara", 4*3600)
	}

	s := &Server{
		cfg:           cfg,
		st:            st,
		refresh:       refresh,
		recordingsDir: recordingsDir,
		loc:           loc,
	}
	s.handler = s.routes()
	logx.Debugf("webapp", "New: exit listen=%q tz=%s whitelist=%d", s.cfg.ListenAddr, s.loc.String(), len(s.cfg.Whitelist))
	return s
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.Handle("GET /static/", noCache(http.FileServer(http.FS(staticFS))))

	api := http.NewServeMux()
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

	mux.Handle("/api/", s.requirePassword(api))
	logx.Debugf("webapp", "routes: done")
	return logRequests(mux)
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

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logx.Debugf("webapp", "noCache: %s %s", r.Method, r.URL.Path)
		w.Header().Set("Cache-Control", "no-store")
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

func (s *Server) requirePassword(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := ""
		if s.cfg != nil {
			want = s.cfg.PanelPassword
		}
		got := passwordFromRequest(r)
		logx.Debugf("webapp", "requirePassword: %s %s password_present=%v configured=%v", r.Method, r.URL.Path, got != "", want != "")
		if !passwordOK(got, want) {
			logx.Warnf("webapp", "requirePassword: auth failed %s %s", r.Method, r.URL.Path)
			logx.Warnf("panel", "bad password %s", r.URL.Path)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		logx.Debugf("webapp", "requirePassword: authorized %s %s", r.Method, r.URL.Path)
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

func bbbHostOK(u string) bool {
	logx.Debugf("webapp", "bbbHostOK: url=%q", u)
	u = strings.TrimSpace(u)
	ok := strings.HasPrefix(u, "https://bbb.ssau.ru/b/")
	logx.Debugf("webapp", "bbbHostOK: url=%q -> %v", u, ok)
	return ok
}

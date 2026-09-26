package webapp

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
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
		loc, err = time.LoadLocation(config.DefaultTimezone)
	}
	if err != nil || loc == nil {
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
	return s
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) Listen(ctx context.Context) error {
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

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := srv.Shutdown(shCtx); err != nil {
			_ = srv.Close()
			return err
		}
		err := <-errCh
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) routes() http.Handler {
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
	api.HandleFunc("GET /api/test", s.handleTestGet)
	api.HandleFunc("POST /api/test", s.handleTestPost)
	api.HandleFunc("GET /api/settings", s.handleSettingsGet)
	api.HandleFunc("POST /api/settings", s.handleSettingsPost)

	mux.Handle("/api/", s.requirePassword(api))
	return mux
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(staticFS, "static/index.html")
	if err != nil {
		http.Error(w, "index missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) requirePassword(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := ""
		if s.cfg != nil {
			want = s.cfg.PanelPassword
		}
		if !passwordOK(passwordFromRequest(r), want) {
			log.Printf("webapp auth: bad password %s", r.URL.Path)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSONStatus(w, code, map[string]string{"error": msg})
}

func decodeJSON(r io.Reader, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r, 1<<20))
	return dec.Decode(dst)
}

func bbbHostOK(u string) bool {
	u = strings.TrimSpace(u)
	return strings.HasPrefix(u, "https://bbb.ssau.ru/b/")
}

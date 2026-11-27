// Command mcp — MCP-сервер для Moodle do.ssau.ru: stdio или Streamable HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/mcpapp"
)

// httpShutdownTimeout — сколько ждём активные запросы при остановке.
const httpShutdownTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		logx.Errorf("mcp", "%v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	transport := flag.String("transport", "stdio", "транспорт: stdio или http")
	httpAddr := flag.String("http-addr", mcpapp.DefaultHTTPAddr, "адрес HTTP-транспорта")
	httpPath := flag.String("http-path", mcpapp.DefaultHTTPPath, "путь MCP-эндпоинта для http")
	configPath := flag.String("config", "", "JSON-конфиг поверх MOODLE_MCP_* (необязательно)")
	stateDir := flag.String("state-dir", "", "каталог состояния (переопределяет MOODLE_MCP_STATE_DIR)")
	recordingsDir := flag.String("recordings-dir", "", "каталог записей и конспектов (переопределяет MOODLE_MCP_RECORDINGS_DIR)")
	flag.Parse()

	if *transport != "stdio" && *transport != "http" {
		return fmt.Errorf("mcp: неизвестный транспорт %q (доступны stdio и http)", *transport)
	}

	cfg, err := mcpapp.LoadConfigFile(*configPath)
	if err != nil {
		return err
	}
	cfg.SetStateDir(*stateDir)
	cfg.SetRecordingsDir(*recordingsDir)

	// Настройки HTTP проверяем до входа в Moodle: без токена на внешнем адресе
	// сервер не должен даже начинать сетевую работу.
	httpOpts := mcpapp.HTTPOptions{Addr: *httpAddr, Path: *httpPath}
	if *transport == "http" {
		if err := mcpapp.ValidateHTTPOptions(&httpOpts); err != nil {
			return err
		}
	}

	setupLogs(cfg)
	sess, err := mcpapp.NewSession(ctx, cfg)
	if err != nil {
		return err
	}
	srv := mcpapp.NewServer(sess)

	switch *transport {
	case "stdio":
		return runStdio(ctx, cfg, srv)
	default:
		return runHTTP(ctx, srv, httpOpts)
	}
}

// runStdio обслуживает MCP по stdin/stdout до отмены ctx.
func runStdio(ctx context.Context, cfg mcpapp.Config, srv *mcp.Server) error {
	logx.Infof("mcp", "stdio: запуск, state=%s, база=%v", cfg.StateDir, cfg.DBPath != "")
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			logx.Infof("mcp", "stdio: остановка по сигналу")
			return nil
		}
		return fmt.Errorf("mcp: сервер: %w", err)
	}
	return nil
}

// runHTTP поднимает Streamable HTTP-сервер и корректно останавливает его по ctx.
func runHTTP(ctx context.Context, srv *mcp.Server, opts mcpapp.HTTPOptions) error {
	handler, err := mcpapp.NewHTTPHandler(srv, opts)
	if err != nil {
		return err
	}
	httpSrv := mcpapp.NewHTTPServer(handler, opts)
	// При отмене ctx активные запросы (в том числе вызовы Moodle) получают
	// отменённый контекст и завершаются, не дожидаясь httpShutdownTimeout.
	httpSrv.BaseContext = func(net.Listener) context.Context { return ctx }

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return fmt.Errorf("mcp: слушать %s: %w", opts.Addr, err)
	}
	auth := "включена"
	if opts.BearerToken == "" {
		auth = "выключена (loopback)"
	}
	logx.Infof("mcp", "http: слушаю %s, path=%s, bearer-аутентификация %s", ln.Addr(), opts.Path, auth)

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("mcp: http-сервер: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("mcp: остановка http-сервера: %w", err)
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("mcp: http-сервер: %w", err)
		}
		logx.Infof("mcp", "http: остановка по сигналу")
		return nil
	}
}

// setupLogs включает logx, но не позволяет ему создавать logs/ в произвольном
// CWD. Если LOG_DIR не задан явно, подробный лог (и снимки) уходят в
// <state-dir>/logs. Основной вывод логов всё равно идёт в stderr.
func setupLogs(cfg mcpapp.Config) {
	if os.Getenv("LOG_DIR") == "" {
		if err := os.MkdirAll(cfg.StateDir, 0o700); err == nil {
			_ = os.Setenv("LOG_DIR", filepath.Join(cfg.StateDir, "logs"))
		}
	}
	logx.Setup()
}

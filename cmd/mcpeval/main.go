package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runWithArgs(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "mcpeval: %v\n", err)
		os.Exit(1)
	}
}

// runWithArgs разбирает флаги и выполняет прогон. Отдельная функция нужна,
// чтобы тесты могли проверить код возврата без запуска процесса.
func runWithArgs(ctx context.Context, args []string) error {
	var cfg runConfig
	fs := flag.NewFlagSet("mcpeval", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.Transport, "transport", "stdio", "транспорт: stdio или http")
	fs.StringVar(&cfg.Server, "server", filepath.Join("bin", "mcp"), "бинарник MCP-сервера для stdio")
	fs.StringVar(&cfg.Endpoint, "url", "http://127.0.0.1:8090/mcp", "MCP-эндпоинт для http")
	fs.StringVar(&cfg.Bearer, "bearer", os.Getenv("MOODLE_MCP_BEARER_TOKEN"), "Bearer-токен для http (по умолчанию MOODLE_MCP_BEARER_TOKEN)")
	fs.StringVar(&cfg.TasksPath, "tasks", filepath.Join("evals", "moodle_tasks.json"), "файл задач")
	fs.StringVar(&cfg.OutPath, "out", "", "куда записать JSON-отчёт (пусто — в stdout)")
	fs.DurationVar(&cfg.StepTimeout, "timeout", 120*time.Second, "таймаут одного шага")
	fs.BoolVar(&cfg.Verbose, "v", false, "печатать каждый шаг")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch cfg.Transport {
	case "stdio":
		if strings.TrimSpace(cfg.Server) == "" {
			return errors.New("для stdio нужен -server")
		}
	case "http":
		if strings.TrimSpace(cfg.Endpoint) == "" {
			return errors.New("для http нужен -url")
		}
	default:
		return fmt.Errorf("неизвестный транспорт %q (stdio или http)", cfg.Transport)
	}
	if cfg.StepTimeout <= 0 {
		return errors.New("таймаут шага должен быть больше нуля")
	}

	tf, err := loadTasks(cfg.TasksPath)
	if err != nil {
		return err
	}
	rep, err := execute(ctx, cfg, tf)
	if err != nil {
		return err
	}
	if err := writeReport(cfg.OutPath, rep); err != nil {
		return err
	}
	if rep.Summary.Failed > 0 || !rep.InvariantsOK {
		return fmt.Errorf("провалено задач %d из %d, инварианты: %v",
			rep.Summary.Failed, rep.Summary.TotalTasks, rep.InvariantsOK)
	}
	return nil
}

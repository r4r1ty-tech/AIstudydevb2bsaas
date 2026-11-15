package notes

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// Печать конспекта идёт через Typst. Шаблон main.typ рисует Markdown пакетом
// cmarker 0.1.10 (MIT, Sabrina Jewson), формулы LaTeX переводит mitex 0.2.7
// (Apache-2.0, Myriad-Dreamin и соавторы). Оба пакета из Typst Universe лежат
// в репозитории и вшиты в бинарник. При печати они распаковываются во временный
// каталог, сеть typst не нужна. Шрифты (Libertinus Serif, New Computer Modern)
// тоже вшиты в typst, поэтому кириллица не зависит от шрифтов сервера.
//
//go:embed typst/main.typ typst/scan.typ typst/check.typ typst/packages
var typstFS embed.FS

// pdfTimeout ограничивает печать: зависший typst не должен держать сборку
// конспекта (и остановку воркера) вечно.
var pdfTimeout = 3 * time.Minute

// typstBin — путь к бинарнику typst из TYPST_BIN; без него берётся typst из PATH.
func typstBin() string {
	if b := strings.TrimSpace(os.Getenv("TYPST_BIN")); b != "" {
		return b
	}
	return "typst"
}

// WritePDF печатает конспект в pdfPath. typst настраивается переменной окружения
// TYPST_BIN, а не полями конфига.
func WritePDF(ctx context.Context, title, md, pdfPath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	started := time.Now()
	// RECORDINGS_DIR на VDS относительный: typst получает абсолютный путь,
	// чтобы результат не зависел от рабочего каталога.
	abs, err := filepath.Abs(pdfPath)
	if err != nil {
		logx.Errorf("notes", "WritePDF: abs %s: %v", pdfPath, err)
		return fmt.Errorf("WritePDF: abs %s: %w", pdfPath, err)
	}
	pdfPath = abs

	work, err := os.MkdirTemp("", "ssau-notes-pdf-")
	if err != nil {
		logx.Errorf("notes", "WritePDF: tmp dir: %v", err)
		return fmt.Errorf("WritePDF: tmp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	if err := writeTypstWorkspace(work, title, md); err != nil {
		logx.Errorf("notes", "WritePDF: workspace: %v", err)
		return fmt.Errorf("WritePDF: workspace: %w", err)
	}

	// Старый файл не должен сойти за свежий, если печать молча не сработала.
	_ = os.Remove(pdfPath)
	out := filepath.Join(work, "out.pdf")
	err = typstCompile(ctx, work, "main.typ", out)
	if err != nil && ctx.Err() == nil && !errors.Is(err, errPrintTimeout) {
		// Одна формула, которую mitex не разобрал, роняет весь документ.
		// Такие формулы находим и печатаем исходником, остальной конспект собираем.
		if bad := badFormulas(ctx, work); len(bad) > 0 {
			logx.Warnf("notes", "WritePDF: mitex не разобрал формул: %d, печатаю их исходником", len(bad))
			if werr := writeJSONList(filepath.Join(work, "bad.json"), bad); werr != nil {
				return fmt.Errorf("WritePDF: bad.json: %w", werr)
			}
			err = typstCompile(ctx, work, "main.typ", out)
		}
	}
	if err != nil {
		logx.Errorf("notes", "WritePDF: typst failed: %v", err)
		return err
	}

	data, err := os.ReadFile(out) //nolint:gosec // путь строит сам бот, не пользователь
	if err != nil || len(data) < 100 {
		logx.Errorf("notes", "WritePDF: pdf не записался path=%s read_err=%v", pdfPath, err)
		return errors.New("pdf не записался")
	}
	if err := os.WriteFile(pdfPath, data, 0o644); err != nil { //nolint:gosec // итоговый файл читают другие сервисы
		logx.Errorf("notes", "WritePDF: write %s: %v", pdfPath, err)
		return fmt.Errorf("WritePDF: write %s: %w", pdfPath, err)
	}
	logx.Infof("notes", "WritePDF: pdf built path=%s bytes=%d за %s", pdfPath, len(data), time.Since(started).Round(time.Millisecond))
	return nil
}

// writeTypstWorkspace раскладывает шаблоны, пакеты и исходник конспекта в dir.
// Пакеты лежат в dir/preview/<имя>/<версия>, как их ищет typst через --package-path.
func writeTypstWorkspace(dir, title, md string) error {
	files := map[string]string{
		"title.txt": title,
		"notes.md":  md,
		"bad.json":  "[]",
	}
	for _, name := range []string{"main.typ", "scan.typ", "check.typ"} {
		data, err := typstFS.ReadFile("typst/" + name)
		if err != nil {
			return err
		}
		files[name] = string(data)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			return err
		}
	}
	return fs.WalkDir(typstFS, "typst/packages", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "typst/packages" {
			return nil
		}
		dst := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(p, "typst/packages/")))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o750)
		}
		data, err := typstFS.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o600)
	})
}

// typstCompile собирает main в out внутри каталога dir. Сеть не нужна:
// пакеты уже в dir/preview. Системные шрифты не сканируем, чтобы результат
// не зависел от шрифтов сервера.
func typstCompile(ctx context.Context, dir, main, out string) error {
	_, err := runTypst(ctx, dir, "compile",
		"--package-path", dir, "--root", dir, "--ignore-system-fonts",
		main, out)
	return err
}

// badFormulas возвращает формулы конспекта, которые mitex не может разобрать.
// Список сверяется по одной формуле за проход, и только при сбое сборки.
// Если все формулы разбираются, пустой результат означает: ошибка не из-за формул.
func badFormulas(ctx context.Context, dir string) []string {
	raw, err := runTypst(ctx, dir, "eval",
		"--package-path", dir, "--root", dir, "--ignore-system-fonts",
		"--in", "scan.typ", "query(<tex>).map(it => it.value)")
	if err != nil {
		logx.Warnf("notes", "badFormulas: scan: %v", err)
		return nil
	}
	var all []string
	if err := json.Unmarshal([]byte(raw), &all); err != nil {
		logx.Warnf("notes", "badFormulas: scan json: %v", err)
		return nil
	}
	seen := make(map[string]bool, len(all))
	uniq := all[:0]
	for _, f := range all {
		if !seen[f] {
			seen[f] = true
			uniq = append(uniq, f)
		}
	}
	if formulasOK(ctx, dir, uniq) {
		return nil
	}
	var bad []string
	for _, f := range uniq {
		if ctx.Err() != nil {
			break
		}
		if !formulasOK(ctx, dir, []string{f}) {
			bad = append(bad, f)
		}
	}
	return bad
}

// formulasOK проверяет, что mitex разбирает весь список формул (check.typ).
func formulasOK(ctx context.Context, dir string, formulas []string) bool {
	if err := writeJSONList(filepath.Join(dir, "formulas.json"), formulas); err != nil {
		logx.Warnf("notes", "formulasOK: write: %v", err)
		return false
	}
	return typstCompile(ctx, dir, "check.typ", filepath.Join(dir, "check.pdf")) == nil
}

// writeJSONList пишет список строк JSON-массивом. nil пишем как [].
func writeJSONList(path string, list []string) error {
	if list == nil {
		list = []string{}
	}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// runTypst запускает typst в каталоге dir и возвращает его stdout.
// Ограничен pdfTimeout. Вся процессная группа убивается при отмене и по
// завершении, чтобы не оставлять сирот.
func runTypst(ctx context.Context, dir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, typstBin(), args...) //nolint:gosec // бинарник typst из конфига, аргументы строит бот
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil || cmd.Process.Pid <= 0 {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 10 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if cmd.Process != nil && cmd.Process.Pid > 0 {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("print-to-pdf: %w", ctx.Err())
	}
	if cctx.Err() != nil {
		logx.Errorf("notes", "runTypst: %s не уложился в %s: %q", args[0], pdfTimeout, truncate(stderr.String(), 400))
		return "", fmt.Errorf("%w %s", errPrintTimeout, pdfTimeout)
	}
	if err != nil {
		msg := truncate(strings.TrimSpace(stderr.String()), 400)
		if msg == "" {
			return "", fmt.Errorf("print-to-pdf: %w", err)
		}
		return "", fmt.Errorf("print-to-pdf: %s: %w", msg, err)
	}
	return stdout.String(), nil
}

// errPrintTimeout — typst не уложился в pdfTimeout. Повторный поиск формул после таймаута
// запускал бы typst ещё раз на весь таймаут, поэтому его отличаем от ошибки разбора.
var errPrintTimeout = errors.New("print-to-pdf: таймаут")

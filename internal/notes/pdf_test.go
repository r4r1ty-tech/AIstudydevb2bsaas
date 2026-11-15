package notes

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// typstForPDF пропускает тест, если typst не найден. Путь берётся из TYPST_BIN, иначе из PATH.
func typstForPDF(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath(typstBin())
	if err != nil {
		t.Skip("no typst for print-to-pdf: " + err.Error())
	}
	return p
}

func TestWritePDFErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TYPST_BIN", filepath.Join(dir, "no-such-typst"))
	if err := WritePDF(context.Background(), "t", "md", filepath.Join(dir, "a.pdf")); err == nil {
		t.Fatal("missing typst must fail")
	}

	fake := filepath.Join(dir, "fake-typst")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPST_BIN", fake)
	if err := WritePDF(context.Background(), "t", "md", filepath.Join(dir, "b.pdf")); err == nil || !strings.Contains(err.Error(), "не записался") {
		t.Fatalf("typst that writes nothing: %v", err)
	}

	typstForPDF(t)
	t.Setenv("TYPST_BIN", "typst")
	if err := WritePDF(context.Background(), "t", "md", filepath.Join(dir, "missing", "c.pdf")); err == nil {
		t.Fatal("unwritable output dir must fail")
	}
}

// RECORDINGS_DIR на VDS относительный: путь должен работать относительно cwd.
func TestWritePDFRelativePath(t *testing.T) {
	typstForPDF(t)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("recordings/x", 0o755); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join("recordings", "x", "notes.pdf")
	if err := WritePDF(context.Background(), "Тема", "## Раздел\n\nтекст", pdf); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(pdf); err != nil || st.Size() < 100 {
		t.Fatalf("pdf: %v", err)
	}
}

// Текст конспекта — данные, а не код шаблона: #-команды и HTML печатаются буквально.
func TestWritePDFMarkdownIsData(t *testing.T) {
	typstForPDF(t)
	pdf := filepath.Join(t.TempDir(), "notes.pdf")
	md := "Текст с #read(\"/etc/passwd\") и #panic(\"x\").\n\n<!--raw-typst #panic(\"y\")-->\n\n![внешняя](https://example.org/a.png)\n"
	if err := WritePDF(context.Background(), "Тема", md, pdf); err != nil {
		t.Fatalf("конспект с #-командами не напечатался: %v", err)
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return
	}
	out, err := exec.Command("pdftotext", "-layout", pdf, "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "#panic") || !strings.Contains(string(out), "[изображение") {
		t.Errorf("текст конспекта напечатан не как данные: %q", out)
	}
}

func TestWriteTypstWorkspace(t *testing.T) {
	dir := t.TempDir()
	md := "## Раздел\n\nтекст"
	if err := writeTypstWorkspace(dir, "Тема", md); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.typ", "scan.typ", "check.typ", "title.txt", "notes.md", "bad.json",
		"preview/cmarker/0.1.10/typst.toml", "preview/cmarker/0.1.10/LICENSE",
		"preview/mitex/0.2.7/typst.toml", "preview/mitex/0.2.7/LICENSE", "preview/mitex/0.2.7/mitex.wasm"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("нет %s в рабочем каталоге: %v", name, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "notes.md"))
	if err != nil || string(got) != md {
		t.Fatalf("notes.md = %q, %v", got, err)
	}
}

// Зависший typst без результата: ошибка по таймауту, а не вечное ожидание.
func TestWritePDFHungTypst(t *testing.T) {
	old := pdfTimeout
	pdfTimeout = time.Second
	t.Cleanup(func() { pdfTimeout = old })
	dir := t.TempDir()
	hung := filepath.Join(dir, "hung-typst")
	if err := os.WriteFile(hung, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPST_BIN", hung)
	if err := WritePDF(context.Background(), "t", "md", filepath.Join(dir, "b.pdf")); err == nil || !strings.Contains(err.Error(), "таймаут") {
		t.Fatalf("зависший typst: %v", err)
	}
}

func TestWritePDFCancelKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	fake := filepath.Join(dir, "spawn-typst")
	script := "#!/bin/sh\nsleep 60 </dev/null >/dev/null 2>&1 &\necho $! > '" + pidFile + "'\nwait\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPST_BIN", fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- WritePDF(ctx, "t", "md", filepath.Join(dir, "c.pdf"))
	}()

	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil && pid > 0 {
				childPID = pid
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("дочерний процесс fake-typst не успел записать pid")
	}

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ожидалась ошибка context.Canceled, получено: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WritePDF не завершился после отмены контекста")
	}

	// Проверяем, что фоновый дочерний процесс в процессной группе тоже убит.
	dead := false
	for i := 0; i < 20; i++ {
		if err := syscall.Kill(childPID, 0); errors.Is(err, syscall.ESRCH) {
			dead = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !dead {
		_ = syscall.Kill(childPID, syscall.SIGKILL)
		t.Fatalf("сиротский дочерний процесс pid=%d остался жив после отмены WritePDF", childPID)
	}
}

// Якорные ссылки Markdown дают метки Typst. Несуществующая метка роняет весь typst-документ,
// поэтому ссылки на якоря печатаются текстом (safe-link), а сборка не падает.
func TestWritePDFAnchorLinks(t *testing.T) {
	typstForPDF(t)
	pdf := filepath.Join(t.TempDir(), "notes.pdf")
	md := "# Раздел\n\nСм. [а](#нет) и [б](#section), внешняя [в](https://example.org).\n"
	if err := WritePDF(context.Background(), "Тема", md, pdf); err != nil {
		t.Fatalf("ссылка на якорь уронила сборку PDF: %v", err)
	}
	if st, err := os.Stat(pdf); err != nil || st.Size() < 100 {
		t.Fatalf("pdf: %v", err)
	}
}

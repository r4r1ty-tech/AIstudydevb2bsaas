package logx

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatorKeepsCap(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRotator(filepath.Join(dir, "rasp.log"), 1000, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 99) + "\n"
	for i := 0; i < 100; i++ { // 10 КБ при потолке 1 КБ
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.Close()
	names, _ := filepath.Glob(filepath.Join(dir, "rasp.log*"))
	if len(names) != 4 { // текущий + 3 сжатых
		t.Fatalf("файлы: %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "rasp.log.4.gz")); !os.IsNotExist(err) {
		t.Fatal("лишний кусок не удалён")
	}
	f, _ := os.Open(filepath.Join(dir, "rasp.log.1.gz"))
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	if len(b) == 0 || len(b) > 1000 || !strings.HasPrefix(string(b), "xxx") {
		t.Fatalf("сжатый кусок: %d байт", len(b))
	}
}

func TestDebugSinkGetsDebugAtInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	SetDebugSink(&buf, &Dumper{Dir: t.TempDir(), MaxBytes: 1 << 20})
	defer SetDebugSink(nil, nil)
	Debugf("moodle", "GET %s", "/mod/quiz/view.php")
	if !strings.Contains(buf.String(), "[DEBUG] moodle: GET /mod/quiz/view.php") {
		t.Fatalf("debug не попал в подробный лог: %q", buf.String())
	}
	if !Verbose() {
		t.Fatal("Verbose при подробном логе")
	}
	p := Dump("quiz-html", "777-p0.html", []byte("<html>"))
	if p == "" || !strings.HasSuffix(p, ".gz") {
		t.Fatalf("dump: %q", p)
	}
}

func TestDumperPrunesOldest(t *testing.T) {
	d := &Dumper{Dir: t.TempDir(), MaxBytes: 4000}
	data := bytes.Repeat([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 250}, 300) // плохо жмётся
	for i := 0; i < 41; i++ {
		if d.Dump("k", "x", append(data, byte(i))) == "" {
			t.Fatal("dump не записан")
		}
	}
	var total int64
	_ = filepath.Walk(d.Dir, func(_ string, info os.FileInfo, _ error) error {
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if total > 4000+2000 { // между чистками может набежать немного сверху
		t.Fatalf("снимки не чистятся: %d байт", total)
	}
}

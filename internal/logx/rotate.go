package logx

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Rotator — файл лога с потолком: дорос до Max — уходит в .1.gz (старые
// сдвигаются), хранится Keep сжатых кусков. Текст лога жмётся раз в 8–15,
// так что Max×Keep — потолок с большим запасом.
type Rotator struct {
	Path string
	Max  int64
	Keep int

	mu   sync.Mutex
	f    *os.File
	size int64
	wg   sync.WaitGroup
}

func NewRotator(path string, max int64, keep int) (*Rotator, error) {
	if max <= 0 {
		max = 50 << 20
	}
	if keep <= 0 {
		keep = 10
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	r := &Rotator{Path: path, Max: max, Keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotator) open() error {
	f, err := os.OpenFile(r.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	r.f = f
	r.size = 0
	if st != nil {
		r.size = st.Size()
	}
	return nil
}

func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size+int64(len(p)) > r.Max && r.size > 0 {
		r.rotate()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate: текущий файл → .1 (сырой), сжатие в фоне в .1.gz; .N.gz сдвигаются.
func (r *Rotator) rotate() {
	_ = r.f.Close()
	r.f = nil
	r.wg.Wait() // прошлое сжатие должно закончиться, иначе сдвиг его потеряет
	for i := r.Keep; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d.gz", r.Path, i)
		if i == r.Keep {
			_ = os.Remove(from)
			continue
		}
		_ = os.Rename(from, fmt.Sprintf("%s.%d.gz", r.Path, i+1))
	}
	raw := r.Path + ".1"
	if err := os.Rename(r.Path, raw); err == nil {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			if err := gzipFile(raw, raw+".gz"); err == nil {
				_ = os.Remove(raw)
			}
		}()
	}
	if err := r.open(); err != nil {
		fmt.Fprintf(os.Stderr, "logx: reopen %s: %v\n", r.Path, err)
	}
}

func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wg.Wait()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(out, gzip.BestSpeed)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// Dumper — снимки целиком (HTML страниц теста, промпты и ответы ИИ):
// <dir>/<kind>/<время>-<name>.gz, общий потолок MaxBytes, старые удаляются.
type Dumper struct {
	Dir      string
	MaxBytes int64

	mu     sync.Mutex
	writes int
}

var dumpName = strings.NewReplacer("/", "_", "\\", "_", " ", "_", ":", "_", "..", "_")

// Dump пишет снимок и возвращает путь (пусто — не записали).
func (d *Dumper) Dump(kind, name string, data []byte) string {
	if d == nil || d.Dir == "" {
		return ""
	}
	dir := filepath.Join(d.Dir, dumpName.Replace(kind))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return ""
	}
	path := filepath.Join(dir, time.Now().Format("20060102-150405.000000")+"-"+dumpName.Replace(name)+".gz")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return ""
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	_, werr := zw.Write(data)
	cerr := zw.Close()
	ferr := f.Close()
	if werr != nil || cerr != nil || ferr != nil {
		_ = os.Remove(path)
		return ""
	}
	d.mu.Lock()
	d.writes++
	prune := d.writes%20 == 1
	d.mu.Unlock()
	if prune {
		d.prune()
	}
	return path
}

type dumpFile struct {
	path string
	mod  time.Time
	size int64
}

func (d *Dumper) prune() {
	if d.MaxBytes <= 0 {
		return
	}
	var files []dumpFile
	var total int64
	_ = filepath.Walk(d.Dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		files = append(files, dumpFile{p, info.ModTime(), info.Size()})
		total += info.Size()
		return nil
	})
	if total <= d.MaxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= d.MaxBytes*9/10 {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

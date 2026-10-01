package bbb

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// Hogs is a refcounted freeze of RAM hogs (Cursor on this VDS) around a live BBB room.
type Hogs interface {
	Hold()
	Release()
	Reset()
}

type nopHogs struct{}

func (nopHogs) Hold()    {}
func (nopHogs) Release() {}
func (nopHogs) Reset()   {}

type procHogs struct {
	procDir string
	mu      sync.Mutex
	n       int
	paused  []int
}

func newProcHogs() *procHogs {
	logx.Debugf("bbb", "newProcHogs: procDir=/proc")
	// Список замороженных PID жил только в памяти прошлого процесса: если bbb
	// убили во время пары (OOM, SIGKILL), Cursor так и остался в SIGSTOP.
	if n := thawHogs(stoppedHogs("/proc", os.Getpid(), os.Getppid())); n > 0 {
		logx.Warnf("bbb", "lecture pause: разморозил %d процессов, оставшихся от прошлого запуска", n)
	}
	return &procHogs{procDir: "/proc"}
}

// stoppedHogs — hog-процессы в состоянии T (stopped).
func stoppedHogs(procDir string, self, ppid int) []int {
	var out []int
	for _, pid := range listHogPIDs(procDir, self, ppid) {
		raw, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "stat"))
		if err != nil {
			continue
		}
		// pid (comm) S ... — comm может содержать скобки и пробелы, режем по последней ')'.
		i := bytes.LastIndexByte(raw, ')')
		if i < 0 || i+2 >= len(raw) {
			continue
		}
		if raw[i+2] == 'T' {
			out = append(out, pid)
		}
	}
	return out
}

func (g *procHogs) Hold() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	logx.Debugf("bbb", "Hogs.Hold: n=%d paused=%d", g.n, len(g.paused))
	if g.n != 1 {
		return
	}
	cleanTmpCache()
	g.paused = freezeHogs(g.procDir)
	logx.Infof("bbb", "lecture pause: froze %d procs", len(g.paused))
}

func (g *procHogs) Release() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n == 0 {
		logx.Debugf("bbb", "Hogs.Release: n=0, nothing to release")
		return
	}
	g.n--
	logx.Debugf("bbb", "Hogs.Release: n=%d paused=%d", g.n, len(g.paused))
	if g.n != 0 {
		return
	}
	n := thawHogs(g.paused)
	g.paused = nil
	logx.Infof("bbb", "lecture pause: thawed %d procs", n)
}

func (g *procHogs) Reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n == 0 && len(g.paused) == 0 {
		logx.Debugf("bbb", "Hogs.Reset: already clean")
		return
	}
	n := thawHogs(g.paused)
	g.paused = nil
	g.n = 0
	logx.Infof("bbb", "lecture pause: reset, thawed %d procs", n)
}

func cleanTmpCache() {
	dir := filepath.Join(os.TempDir(), "cursor-sandbox-cache")
	if err := os.RemoveAll(dir); err != nil {
		logx.Debugf("bbb", "cleanTmpCache: %s: %v", dir, err)
	}
}

func freezeHogs(procDir string) []int {
	self := os.Getpid()
	ppid := os.Getppid()
	var out []int
	pids := listHogPIDs(procDir, self, ppid)
	logx.Debugf("bbb", "freezeHogs: candidates=%d self=%d ppid=%d", len(pids), self, ppid)
	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
			logx.Debugf("bbb", "freezeHogs: stop pid=%d: %v", pid, err)
			continue
		}
		out = append(out, pid)
	}
	logx.Debugf("bbb", "freezeHogs: frozen=%d", len(out))
	return out
}

func thawHogs(pids []int) int {
	n := 0
	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
			logx.Debugf("bbb", "thawHogs: cont pid=%d: %v", pid, err)
			continue
		}
		n++
	}
	return n
}

func listHogPIDs(procDir string, self, ppid int) []int {
	ents, err := os.ReadDir(procDir)
	if err != nil {
		logx.Warnf("bbb", "listHogPIDs: read %s: %v", procDir, err)
		return nil
	}
	var out []int
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 || pid == self || pid == ppid {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procDir, e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		if !isHogCmdline(raw) {
			continue
		}
		out = append(out, pid)
	}
	sort.Ints(out)
	logx.Debugf("bbb", "listHogPIDs: %s -> %v", procDir, out)
	return out
}

func isHogCmdline(raw []byte) bool {
	s := string(bytes.ReplaceAll(raw, []byte{0}, []byte{' '}))
	s = strings.ToLower(s)
	if strings.Contains(s, "cursor-agent") {
		return true
	}
	if strings.Contains(s, "cursor-server") || strings.Contains(s, ".cursor-server/") {
		return true
	}
	return false
}

package bbb

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	return &procHogs{procDir: "/proc"}
}

func (g *procHogs) Hold() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	if g.n != 1 {
		return
	}
	cleanTmpCache()
	g.paused = freezeHogs(g.procDir)
	log.Printf("bbb: lecture pause: froze %d procs", len(g.paused))
}

func (g *procHogs) Release() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n == 0 {
		return
	}
	g.n--
	if g.n != 0 {
		return
	}
	n := thawHogs(g.paused)
	g.paused = nil
	log.Printf("bbb: lecture pause: thawed %d procs", n)
}

func (g *procHogs) Reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n == 0 && len(g.paused) == 0 {
		return
	}
	n := thawHogs(g.paused)
	g.paused = nil
	g.n = 0
	log.Printf("bbb: lecture pause: reset, thawed %d procs", n)
}

func cleanTmpCache() {
	_ = os.RemoveAll(filepath.Join(os.TempDir(), "cursor-sandbox-cache"))
}

func freezeHogs(procDir string) []int {
	self := os.Getpid()
	ppid := os.Getppid()
	var out []int
	for _, pid := range listHogPIDs(procDir, self, ppid) {
		if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
			continue
		}
		out = append(out, pid)
	}
	return out
}

func thawHogs(pids []int) int {
	n := 0
	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
			continue
		}
		n++
	}
	return n
}

func listHogPIDs(procDir string, self, ppid int) []int {
	ents, err := os.ReadDir(procDir)
	if err != nil {
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

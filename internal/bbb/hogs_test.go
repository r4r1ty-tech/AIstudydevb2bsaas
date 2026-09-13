package bbb

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeCmd(t *testing.T, proc, pid, cmdline string) {
	t.Helper()
	dir := filepath.Join(proc, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(cmdline), 0)
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsHogCmdline(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"/root/.cursor-server/bin/linux-x64/node out/server-main.js", true},
		{"cursor-agent worker start --worker-dir /root/dev", true},
		{"/opt/ssau-bot/bbb", false},
		{"/opt/ssau-bot/tg", false},
		{"/usr/local/bin/cloudflared tunnel --url http://127.0.0.1:8080", false},
		{"/usr/bin/chromium --headless=new", false},
	}
	for _, c := range cases {
		got := isHogCmdline([]byte(c.in))
		if got != c.want {
			t.Errorf("%q: got %v want %v", c.in, got, c.want)
		}
	}
}

func TestListHogPIDs(t *testing.T) {
	proc := t.TempDir()
	writeCmd(t, proc, "1", "/sbin/init")
	writeCmd(t, proc, "10", "/opt/ssau-bot/bbb")
	writeCmd(t, proc, "11", "/opt/ssau-bot/tg")
	writeCmd(t, proc, "42", "/root/.cursor-server/bin/node server-main.js")
	writeCmd(t, proc, "43", "cursor-agent worker start")
	writeCmd(t, proc, "44", "/usr/local/bin/cloudflared tunnel")

	got := listHogPIDs(proc, 10, 1)
	want := []int{42, 43}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestHogsRefcountNoop(t *testing.T) {
	var h Hogs = nopHogs{}
	h.Hold()
	h.Hold()
	h.Release()
	h.Release()
	h.Reset()
}

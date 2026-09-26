package proxyrelay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFile(t *testing.T) {
	dir := t.TempDir()
	if got, err := ParseFile(""); got != nil || err != nil {
		t.Fatalf("empty path = %v %v", got, err)
	}
	if got, err := ParseFile(filepath.Join(dir, "missing")); got != nil || err != nil {
		t.Fatalf("missing file = %v %v", got, err)
	}
	p := filepath.Join(dir, "proxies.txt")
	if err := os.WriteFile(p, []byte("# comment\nuser:pass@10.0.0.1:1080\n\n10.0.0.2:1081\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ParseFile(p)
	if err != nil || len(got) != 2 {
		t.Fatalf("ParseFile = %v %v", got, err)
	}
	if err := os.WriteFile(p, []byte("not a proxy at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFile(p); err == nil {
		t.Fatal("garbage must fail")
	}
	if _, err := ParseFile(dir); err == nil {
		t.Fatal("a directory is not a proxy list")
	}
}

func TestSlotAccessorsAndRecovery(t *testing.T) {
	list, err := ParseLines(strings.NewReader("u:p@127.0.0.1:1\n"))
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPool(list)
	t.Cleanup(func() { _ = pool.Close() })
	s, err := pool.Next()
	if err != nil || s == nil {
		t.Fatal(err)
	}
	if s.Proxy().Addr() != "127.0.0.1:1" || !strings.Contains(s.Redacted(), "127.0.0.1:1") || strings.Contains(s.Redacted(), ":p@") {
		t.Fatalf("accessors: %+v %q", s.Proxy(), s.Redacted())
	}
	if !strings.HasPrefix(s.LocalURL(), "socks5://127.0.0.1:") || s.LocalAddr() == "" {
		t.Fatalf("LocalURL = %q", s.LocalURL())
	}
	for i := 0; i < maxSlotFails; i++ {
		s.Fail()
	}
	if !s.Dead() {
		t.Fatal("slot must die after maxSlotFails")
	}
	s.OK()
	if s.Dead() || s.Fails() != 0 {
		t.Fatal("OK must revive the slot")
	}
	s.Fail()
	s.Reset()
	if s.Fails() != 0 {
		t.Fatal("Reset must clear fails")
	}
	var nilSlot *Slot
	nilSlot.OK()
	nilSlot.Reset()
	nilSlot.Fail()
	if nilSlot.Fails() != 0 || nilSlot.Dead() {
		t.Fatal("nil slot")
	}
}

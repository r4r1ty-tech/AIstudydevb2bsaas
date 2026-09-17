package proxyrelay

import (
	"strings"
	"testing"
)

func TestParseEntry(t *testing.T) {
	p, err := ParseEntry("8BGSF6xC:ErJASaZYtmQi@45.134.181.159:5501")
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	if p.Host != "45.134.181.159" || p.Port != "5501" || p.User != "8BGSF6xC" || p.Pass != "ErJASaZYtmQi" {
		t.Fatalf("unexpected: %+v", p)
	}
	if p.Redacted() != "45.134.181.159:5501" {
		t.Fatalf("redacted leaked creds: %q", p.Redacted())
	}
}

func TestParseEntryNoCreds(t *testing.T) {
	p, err := ParseEntry("127.0.0.1:1080")
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	if p.User != "" || p.Pass != "" || p.Host != "127.0.0.1" || p.Port != "1080" {
		t.Fatalf("unexpected: %+v", p)
	}
}

func TestParseEntryBad(t *testing.T) {
	for _, s := range []string{"", "no-at-sign", "user:pass@host", "user:pass@host:notnum", "a@b:c"} {
		if _, err := ParseEntry(s); err == nil {
			t.Fatalf("expected error for %q", s)
		}
	}
}

func TestParseLinesSkipsComments(t *testing.T) {
	in := "# comment\n\n" +
		"a:b@1.2.3.4:1080\n" +
		"  c:d@5.6.7.8:1081  \n"
	list, err := ParseLines(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseLines: %v", err)
	}
	if len(list) != 2 || list[0].Host != "1.2.3.4" || list[1].Host != "5.6.7.8" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestPoolRotation(t *testing.T) {
	pool := NewPool([]Proxy{{Host: "127.0.0.1", Port: "1"}, {Host: "127.0.0.1", Port: "2"}})
	defer func() { _ = pool.Close() }()
	if pool.Len() != 2 {
		t.Fatalf("len=%d", pool.Len())
	}
	a, _ := pool.Next()
	b, _ := pool.Next()
	c, _ := pool.Next()
	if a.Redacted() != "127.0.0.1:1" || b.Redacted() != "127.0.0.1:2" || c.Redacted() != "127.0.0.1:1" {
		t.Fatalf("rotation wrong: %s %s %s", a.Redacted(), b.Redacted(), c.Redacted())
	}
}

func TestPoolSkipsDead(t *testing.T) {
	pool := NewPool([]Proxy{{Host: "127.0.0.1", Port: "1"}, {Host: "127.0.0.1", Port: "2"}})
	defer func() { _ = pool.Close() }()
	s1, err := pool.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	for i := 0; i < maxSlotFails; i++ {
		s1.Fail()
	}
	if !s1.Dead() {
		t.Fatalf("slot should be dead after %d fails", maxSlotFails)
	}
	for i := 0; i < 4; i++ {
		s, err := pool.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if s == s1 {
			t.Fatalf("dead slot handed out again")
		}
		if s.Redacted() != "127.0.0.1:2" {
			t.Fatalf("unexpected live slot %s", s.Redacted())
		}
	}
}

func TestPoolAllDeadGoesDirect(t *testing.T) {
	pool := NewPool([]Proxy{{Host: "127.0.0.1", Port: "1"}})
	defer func() { _ = pool.Close() }()
	s, _ := pool.Next()
	for i := 0; i < maxSlotFails; i++ {
		s.Fail()
	}
	got, err := pool.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil (direct) when all dead, got %s", got.Redacted())
	}
}

func TestNilPoolNext(t *testing.T) {
	var pool *Pool
	s, err := pool.Next()
	if err != nil || s != nil {
		t.Fatalf("nil pool: %v %v", s, err)
	}
	if pool.Len() != 0 {
		t.Fatalf("nil pool len")
	}
}

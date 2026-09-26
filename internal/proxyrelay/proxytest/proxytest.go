// Package proxytest is a fake upstream SOCKS5 proxy (user/password auth)
// for tests. Every CONNECT goes to Target, whatever host was asked for, so a
// browser can reach a local test server by a non-loopback name.
package proxytest

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

type Upstream struct {
	Addr   string
	User   string
	Pass   string
	Target string // host:port every CONNECT is sent to; "" = the asked host

	// Hosts records the host:port of every CONNECT that passed auth.
	mu    sync.Mutex
	hosts []string
	// Rejected counts connections refused for bad credentials.
	Rejected atomic.Int64
}

// Start listens on 127.0.0.1 and closes with the test.
func Start(t testing.TB, user, pass, target string) *Upstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &Upstream{Addr: ln.Addr().String(), User: user, Pass: pass, Target: target}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go u.handle(c)
		}
	}()
	return u
}

// Entry is the proxies.txt line for this upstream with the given password.
func (u *Upstream) Entry(pass string) string {
	return u.User + ":" + pass + "@" + u.Addr
}

func (u *Upstream) Hosts() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.hosts...)
}

func (u *Upstream) handle(c net.Conn) {
	defer c.Close()
	head := make([]byte, 2)
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	if _, err := io.ReadFull(c, make([]byte, int(head[1]))); err != nil {
		return
	}
	if _, err := c.Write([]byte{0x05, 0x02}); err != nil {
		return
	}
	user, pass, ok := readAuth(c)
	if !ok {
		return
	}
	if user != u.User || pass != u.Pass {
		u.Rejected.Add(1)
		_, _ = c.Write([]byte{0x01, 0x01})
		return
	}
	if _, err := c.Write([]byte{0x01, 0x00}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	host, ok := readAddr(c, req[3])
	if !ok {
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return
	}
	asked := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	u.mu.Lock()
	u.hosts = append(u.hosts, asked)
	u.mu.Unlock()
	target := u.Target
	if target == "" {
		target = asked
	}
	up, err := net.Dial("tcp", target)
	if err != nil {
		_, _ = c.Write([]byte{0x05, 0x05, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	if _, err := c.Write([]byte{0x05, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
}

func readAuth(c net.Conn) (string, string, bool) {
	ab := make([]byte, 2)
	if _, err := io.ReadFull(c, ab); err != nil {
		return "", "", false
	}
	user := make([]byte, int(ab[1]))
	if _, err := io.ReadFull(c, user); err != nil {
		return "", "", false
	}
	pl := make([]byte, 1)
	if _, err := io.ReadFull(c, pl); err != nil {
		return "", "", false
	}
	pass := make([]byte, int(pl[0]))
	if _, err := io.ReadFull(c, pass); err != nil {
		return "", "", false
	}
	return string(user), string(pass), true
}

func readAddr(c net.Conn, atyp byte) (string, bool) {
	var n int
	switch atyp {
	case 0x01:
		n = 4
	case 0x04:
		n = 16
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return "", false
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return "", false
		}
		return string(b), true
	default:
		return "", false
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		return "", false
	}
	return net.IP(b).String(), true
}

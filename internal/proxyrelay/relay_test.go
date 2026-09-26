package proxyrelay

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// fakeUpstream is a minimal SOCKS5 server that requires user/pass auth and
// dials the requested target directly. It stands in for the paid proxy.
func fakeUpstream(t *testing.T, user, pass string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go upstreamHandle(c, user, pass)
		}
	}()
	return ln.Addr().String()
}

func upstreamHandle(c net.Conn, user, pass string) {
	defer c.Close()
	head := make([]byte, 2)
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if _, err := c.Write([]byte{0x05, 0x02}); err != nil {
		return
	}
	ab := make([]byte, 2)
	if _, err := io.ReadFull(c, ab); err != nil {
		return
	}
	u := make([]byte, int(ab[1]))
	if _, err := io.ReadFull(c, u); err != nil {
		return
	}
	pl := make([]byte, 1)
	if _, err := io.ReadFull(c, pl); err != nil {
		return
	}
	p := make([]byte, int(pl[0]))
	if _, err := io.ReadFull(c, p); err != nil {
		return
	}
	if string(u) != user || string(p) != pass {
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
	host, err := readSocksAddr(c, req[3])
	if err != nil {
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	up, err := net.Dial("tcp", target)
	if err != nil {
		_, _ = c.Write([]byte{0x05, 0x05, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	if _, err := c.Write([]byte{0x05, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	pipe(c, up)
}

func echoServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close(); wg.Wait() }
}

func TestRelayEndToEnd(t *testing.T) {
	upAddr := fakeUpstream(t, "user1", "pass1")
	host, port, _ := net.SplitHostPort(upAddr)

	echoAddr, stopEcho := echoServer(t)
	defer stopEcho()

	pool := NewPool([]Proxy{{Host: host, Port: port, User: "user1", Pass: "pass1"}})
	defer func() { _ = pool.Close() }()

	slot, err := pool.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if slot.LocalAddr() == "" {
		t.Fatalf("no local relay address")
	}

	dialer, err := xproxy.SOCKS5("tcp", slot.LocalAddr(), nil, xproxy.Direct)
	if err != nil {
		t.Fatalf("local dialer: %v", err)
	}
	conn, err := dialer.Dial("tcp", echoAddr)
	if err != nil {
		t.Fatalf("dial through relay: %v", err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	msg := []byte("ping-through-socks")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("echo mismatch: %q", got)
	}
}

func TestRelayBadCredentials(t *testing.T) {
	upAddr := fakeUpstream(t, "user1", "pass1")
	host, port, _ := net.SplitHostPort(upAddr)
	echoAddr, stopEcho := echoServer(t)
	defer stopEcho()

	pool := NewPool([]Proxy{{Host: host, Port: port, User: "user1", Pass: "WRONG"}})
	defer func() { _ = pool.Close() }()
	slot, err := pool.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	dialer, err := xproxy.SOCKS5("tcp", slot.LocalAddr(), nil, xproxy.Direct)
	if err != nil {
		t.Fatalf("local dialer: %v", err)
	}
	if _, err := dialer.Dial("tcp", echoAddr); err == nil {
		t.Fatalf("expected auth failure through relay")
	}
}

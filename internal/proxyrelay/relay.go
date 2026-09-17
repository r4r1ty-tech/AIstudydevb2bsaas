package proxyrelay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	xproxy "golang.org/x/net/proxy"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// Pool hands out proxies round-robin, one per browser tab/session.
type Pool struct {
	slots []*Slot
	next  atomic.Uint64
}

func NewPool(proxies []Proxy) *Pool {
	slots := make([]*Slot, 0, len(proxies))
	for _, p := range proxies {
		slots = append(slots, &Slot{p: p})
	}
	logx.Infof("proxyrelay", "NewPool: proxies=%d", len(slots))
	return &Pool{slots: slots}
}

func (pool *Pool) Len() int {
	if pool == nil {
		return 0
	}
	return len(pool.slots)
}

// Next returns the next proxy in rotation and starts its local relay on demand.
func (pool *Pool) Next() (*Slot, error) {
	if pool == nil || len(pool.slots) == 0 {
		logx.Debugf("proxyrelay", "Next: no proxies")
		return nil, nil
	}
	n := pool.next.Add(1)
	s := pool.slots[int(n-1)%len(pool.slots)]
	if err := s.start(); err != nil {
		return nil, err
	}
	return s, nil
}

func (pool *Pool) Close() error {
	if pool == nil {
		return nil
	}
	var first error
	for _, s := range pool.slots {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
	}
	logx.Debugf("proxyrelay", "Close: done slots=%d", len(pool.slots))
	return first
}

// Slot is one upstream proxy plus its local no-auth SOCKS5 relay.
type Slot struct {
	p Proxy

	mu   sync.Mutex
	ln   net.Listener
	dial xproxy.Dialer
	addr string
	err  error
}

func (s *Slot) Proxy() Proxy      { return s.p }
func (s *Slot) Redacted() string  { return s.p.Redacted() }
func (s *Slot) LocalAddr() string { return s.addr }

// LocalURL is what Chrome gets as its per-context proxy server.
func (s *Slot) LocalURL() string {
	if s == nil || s.addr == "" {
		return ""
	}
	return "socks5://" + s.addr
}

func (s *Slot) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return nil
	}
	if s.err != nil {
		return s.err
	}
	dial, err := xproxy.SOCKS5("tcp", s.p.Addr(), &xproxy.Auth{User: s.p.User, Password: s.p.Pass}, xproxy.Direct)
	if err != nil {
		s.err = err
		logx.Errorf("proxyrelay", "start upstream=%s: dialer: %v", s.p.Redacted(), err)
		return fmt.Errorf("proxyrelay: %s: %w", s.p.Redacted(), err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.err = err
		logx.Errorf("proxyrelay", "start upstream=%s: listen: %v", s.p.Redacted(), err)
		return fmt.Errorf("proxyrelay: listen: %w", err)
	}
	s.dial = dial
	s.ln = ln
	s.addr = ln.Addr().String()
	upstream := s.p.Redacted()
	go serve(ln, dial, upstream)
	logx.Infof("proxyrelay", "relay up upstream=%s local=%s", upstream, s.addr)
	return nil
}

func (s *Slot) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	err := s.ln.Close()
	s.ln = nil
	if err != nil {
		logx.Warnf("proxyrelay", "Close %s: %v", s.p.Redacted(), err)
	}
	return err
}

func serve(ln net.Listener, dial xproxy.Dialer, upstream string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logx.Warnf("proxyrelay", "serve %s: accept: %v", upstream, err)
			} else {
				logx.Debugf("proxyrelay", "serve %s: closed", upstream)
			}
			return
		}
		go handle(c, dial, upstream)
	}
}

func handle(c net.Conn, dial xproxy.Dialer, upstream string) {
	defer c.Close()
	target, err := socks5Accept(c)
	if err != nil {
		logx.Debugf("proxyrelay", "handle %s: handshake: %v", upstream, err)
		return
	}
	up, err := dial.Dial("tcp", target)
	if err != nil {
		logx.Warnf("proxyrelay", "handle %s: dial %s: %v", upstream, target, err)
		socks5Reply(c, 0x05)
		return
	}
	defer up.Close()
	if err := socks5Reply(c, 0x00); err != nil {
		logx.Debugf("proxyrelay", "handle %s: reply: %v", upstream, err)
		return
	}
	logx.Debugf("proxyrelay", "handle %s: connected %s", upstream, target)
	pipe(c, up)
}

func socks5Accept(c net.Conn) (string, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(c, head); err != nil {
		return "", err
	}
	if head[0] != 0x05 {
		return "", fmt.Errorf("socks version %d", head[0])
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", err
	}
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return "", err
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return "", err
	}
	if req[1] != 0x01 {
		return "", fmt.Errorf("socks cmd %d", req[1])
	}
	host, err := readSocksAddr(c, req[3])
	if err != nil {
		return "", err
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(c, portBuf); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf)
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func readSocksAddr(c net.Conn, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return "", err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		return string(b), nil
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	default:
		return "", fmt.Errorf("socks atyp %d", atyp)
	}
}

func socks5Reply(c net.Conn, code byte) error {
	_, err := c.Write([]byte{0x05, code, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return err
}

func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := io.Copy(b, a); err != nil {
			logx.Debugf("proxyrelay", "pipe a->b: %v", err)
		}
		if tc, ok := b.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := io.Copy(a, b); err != nil {
			logx.Debugf("proxyrelay", "pipe b->a: %v", err)
		}
		if tc, ok := a.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	wg.Wait()
}

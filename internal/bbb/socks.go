package bbb

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/net/proxy"
)

type socksBridge struct {
	ln       net.Listener
	upstream string
	user     string
	pass     string
	once     sync.Once
}

func startSOCKSBridge(spec string) (*socksBridge, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	host, user, pass, err := parseSOCKS5(spec)
	if err != nil {
		return nil, err
	}
	if user == "" && pass == "" {
		return nil, nil // chrome can use host directly
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("socks listen: %w", err)
	}
	b := &socksBridge{ln: ln, upstream: host, user: user, pass: pass}
	go b.serve()
	return b, nil
}

func (b *socksBridge) Addr() string {
	if b == nil || b.ln == nil {
		return ""
	}
	return b.ln.Addr().String()
}

func (b *socksBridge) Close() error {
	if b == nil {
		return nil
	}
	var err error
	b.once.Do(func() {
		err = b.ln.Close()
	})
	return err
}

func (b *socksBridge) serve() {
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		go b.handle(c)
	}
}

func (b *socksBridge) handle(c net.Conn) {
	defer c.Close()
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 5 {
		return
	}
	nmethods := int(hdr[1])
	if nmethods > 0 {
		if _, err := io.ReadFull(c, make([]byte, nmethods)); err != nil {
			return
		}
	}
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil || req[0] != 5 || req[1] != 1 {
		return
	}
	dest, err := readSOCKSAddr(c, req[3])
	if err != nil {
		return
	}

	auth := &proxy.Auth{User: b.user, Password: b.pass}
	dialer, err := proxy.SOCKS5("tcp", b.upstream, auth, proxy.Direct)
	if err != nil {
		return
	}
	up, err := dialer.Dial("tcp", dest)
	if err != nil {
		_, _ = c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	go io.Copy(up, c)
	_, _ = io.Copy(c, up)
}

func readSOCKSAddr(r io.Reader, atyp byte) (string, error) {
	switch atyp {
	case 1:
		var ip [4]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return "", err
		}
		var port [2]byte
		if _, err := io.ReadFull(r, port[:]); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s:%d", net.IP(ip[:]).String(), binary.BigEndian.Uint16(port[:])), nil
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(r, n[:]); err != nil {
			return "", err
		}
		host := make([]byte, n[0])
		if _, err := io.ReadFull(r, host); err != nil {
			return "", err
		}
		var port [2]byte
		if _, err := io.ReadFull(r, port[:]); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s:%d", string(host), binary.BigEndian.Uint16(port[:])), nil
	case 4:
		var ip [16]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return "", err
		}
		var port [2]byte
		if _, err := io.ReadFull(r, port[:]); err != nil {
			return "", err
		}
		return fmt.Sprintf("[%s]:%d", net.IP(ip[:]).String(), binary.BigEndian.Uint16(port[:])), nil
	default:
		return "", fmt.Errorf("socks atyp %d", atyp)
	}
}

func parseSOCKS5(spec string) (host, user, pass string, err error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return "", "", "", fmt.Errorf("empty socks")
	}
	if !strings.Contains(s, "://") {
		s = "socks5://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "", "", err
	}
	host = u.Host
	if host == "" {
		return "", "", "", fmt.Errorf("socks host empty")
	}
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	return host, user, pass, nil
}

func chromeProxyURL(spec string, bridge *socksBridge) (string, error) {
	if strings.TrimSpace(spec) == "" {
		return "", nil
	}
	if bridge != nil {
		return "socks5://" + bridge.Addr(), nil
	}
	host, _, _, err := parseSOCKS5(spec)
	if err != nil {
		return "", err
	}
	return "socks5://" + host, nil
}

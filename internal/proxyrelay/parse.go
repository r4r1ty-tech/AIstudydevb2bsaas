package proxyrelay

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// Proxy is one upstream SOCKS5 endpoint.
type Proxy struct {
	Host string
	Port string
	User string
	Pass string
}

// Redacted is a safe label for logs: host:port only.
func (p Proxy) Redacted() string {
	return p.Host + ":" + p.Port
}

func (p Proxy) Addr() string {
	return p.Host + ":" + p.Port
}

func (p Proxy) valid() bool {
	if strings.TrimSpace(p.Host) == "" || strings.TrimSpace(p.Port) == "" {
		return false
	}
	if _, err := strconv.Atoi(p.Port); err != nil {
		return false
	}
	return true
}

// ParseLines reads login:password@ip:port per line; blank lines and #comments skipped.
// Кривая строка пропускается с ошибкой в логе — остальные прокси работают.
// Ошибка — только если кривые все строки.
func ParseLines(r io.Reader) ([]Proxy, error) {
	logx.Debugf("proxyrelay", "ParseLines: enter")
	out := make([]Proxy, 0)
	var firstErr error
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		p, err := ParseEntry(raw)
		if err != nil {
			logx.Errorf("proxyrelay", "ParseLines: line=%d пропущена: %v", line, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("proxyrelay: строка %d: %w", line, err)
			}
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	if err := sc.Err(); err != nil {
		logx.Errorf("proxyrelay", "ParseLines: scan: %v", err)
		return nil, fmt.Errorf("proxyrelay: чтение: %w", err)
	}
	logx.Debugf("proxyrelay", "ParseLines: out count=%d", len(out))
	return out, nil
}

// ParseEntry parses a single login:password@ip:port (credentials optional).
func ParseEntry(raw string) (Proxy, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Proxy{}, fmt.Errorf("пустая запись")
	}
	cred, hostport := "", s
	if at := strings.LastIndex(s, "@"); at >= 0 {
		cred, hostport = s[:at], s[at+1:]
	}
	var user, pass string
	if cred != "" {
		i := strings.Index(cred, ":")
		if i < 0 {
			return Proxy{}, fmt.Errorf("нет : в логине/пароле")
		}
		user, pass = cred[:i], cred[i+1:]
	}
	host := hostport
	port := ""
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		host, port = hostport[:i], hostport[i+1:]
	}
	p := Proxy{Host: strings.TrimSpace(host), Port: strings.TrimSpace(port), User: user, Pass: pass}
	if !p.valid() {
		return Proxy{}, fmt.Errorf("не разобрал адрес %q", hostport)
	}
	logx.Debugf("proxyrelay", "ParseEntry: %q -> %s user_present=%v", raw, p.Redacted(), p.User != "")
	return p, nil
}

// ParseFile loads a proxy list from path. Missing/empty file means no proxies.
func ParseFile(path string) ([]Proxy, error) {
	logx.Debugf("proxyrelay", "ParseFile: path=%q", path)
	if strings.TrimSpace(path) == "" {
		logx.Debugf("proxyrelay", "ParseFile: empty path -> none")
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			logx.Debugf("proxyrelay", "ParseFile: no file %s", path)
			return nil, nil
		}
		logx.Errorf("proxyrelay", "ParseFile: open %s: %v", path, err)
		return nil, fmt.Errorf("proxyrelay: открыть %s: %w", path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			logx.Warnf("proxyrelay", "ParseFile: close %s: %v", path, cerr)
		}
	}()
	proxies, err := ParseLines(f)
	if err != nil {
		return nil, err
	}
	logx.Infof("proxyrelay", "ParseFile: path=%s count=%d", path, len(proxies))
	return proxies, nil
}

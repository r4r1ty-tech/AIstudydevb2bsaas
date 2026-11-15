package webapp

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// ParseAllowList разбирает список IP и CIDR, разделённых запятыми, пробелами
// или переводами строк. Одиночный IP превращается в префикс /32 (/128 для IPv6).
// Пустая строка даёт пустой список и nil, кривой элемент - ошибку с его именем.
func ParseAllowList(raw string) ([]netip.Prefix, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	var out []netip.Prefix
	for _, f := range fields {
		if strings.Contains(f, "/") {
			p, err := netip.ParsePrefix(f)
			if err != nil {
				return nil, fmt.Errorf("allow list: неверный CIDR %q: %w", f, err)
			}
			addr := p.Addr()
			if addr.Is4In6() {
				// ::ffff:a.b.c.d/N -> a.b.c.d/(N-96), чтобы сравнение шло по IPv4.
				if p.Bits() < 96 {
					return nil, fmt.Errorf("allow list: неверный CIDR %q: префикс короче /96 для IPv4-mapped адреса", f)
				}
				p = netip.PrefixFrom(addr.Unmap(), p.Bits()-96)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("allow list: неверный IP %q: %w", f, err)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// AllowIPs пускает запрос к next только если IP клиента входит в allow.
// Пустой allow - фича выключена. При trustProxy и loopback-RemoteAddr клиент
// берётся из самого правого элемента X-Forwarded-For; иначе заголовок игнорируется.
func AllowIPs(allow []netip.Prefix, trustProxy bool, next http.Handler) http.Handler {
	if len(allow) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, ok := clientIP(r, trustProxy)
		if ok && ipAllowed(allow, ip) {
			next.ServeHTTP(w, r)
			return
		}
		who := "unknown"
		if ok {
			who = ip.String()
		}
		logx.Warnf("panel", "allow: отказ ip=%s method=%s path=%s", who, r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden\n"))
	})
}

func ipAllowed(allow []netip.Prefix, ip netip.Addr) bool {
	for _, p := range allow {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP определяет адрес клиента. Второе значение false, если разобрать не удалось.
func clientIP(r *http.Request, trustProxy bool) (netip.Addr, bool) {
	remote, ok := parseRemoteAddr(r.RemoteAddr)
	if !ok {
		return netip.Addr{}, false
	}
	if trustProxy && remote.IsLoopback() {
		if xff := lastXFF(r.Header.Values("X-Forwarded-For")); xff != "" {
			a, err := netip.ParseAddr(xff)
			if err != nil {
				return netip.Addr{}, false
			}
			return a.Unmap(), true
		}
	}
	return remote, true
}

func parseRemoteAddr(s string) (netip.Addr, bool) {
	host := s
	if h, _, err := net.SplitHostPort(s); err == nil {
		host = h
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // зона IPv6 (fe80::1%eth0)
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// lastXFF возвращает самый правый непустой элемент X-Forwarded-For
// (несколько заголовков склеиваются по порядку).
func lastXFF(values []string) string {
	parts := strings.Split(strings.Join(values, ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if p := strings.TrimSpace(parts[i]); p != "" {
			return p
		}
	}
	return ""
}

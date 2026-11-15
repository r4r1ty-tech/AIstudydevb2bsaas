package webapp

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestParseAllowList(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr string
	}{
		{"пустая строка", "", nil, ""},
		{"только пробелы", " \n\t ", nil, ""},
		{"один IPv4", "203.0.113.7", []string{"203.0.113.7/32"}, ""},
		{"CIDR", "10.0.0.0/8", []string{"10.0.0.0/8"}, ""},
		{"CIDR с хвостом хоста маскируется", "10.1.2.3/8", []string{"10.0.0.0/8"}, ""},
		{"IPv6", "::1", []string{"::1/128"}, ""},
		{"IPv6 CIDR", "2001:db8::/32", []string{"2001:db8::/32"}, ""},
		{"IPv4-mapped как IPv4", "::ffff:1.2.3.4", []string{"1.2.3.4/32"}, ""},
		{"смешанные разделители", "203.0.113.7, 10.0.0.0/8 ::1\n192.168.0.1,,\t2001:db8::/32",
			[]string{"203.0.113.7/32", "10.0.0.0/8", "::1/128", "192.168.0.1/32", "2001:db8::/32"}, ""},
		{"кривой IP", "1.2.3.4, 999.1.1.1", nil, "999.1.1.1"},
		{"кривой CIDR", "10.0.0.0/33", nil, "10.0.0.0/33"},
		{"мусор", "abc", nil, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAllowList(tt.raw)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ожидалась ошибка с %q, получили nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ошибка %q не называет элемент %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i, p := range got {
				if p.String() != tt.want[i] {
					t.Errorf("[%d] got %s, want %s", i, p, tt.want[i])
				}
			}
		})
	}
}

func mustAllow(t *testing.T, raw string) []netip.Prefix {
	t.Helper()
	l, err := ParseAllowList(raw)
	if err != nil {
		t.Fatalf("ParseAllowList(%q): %v", raw, err)
	}
	return l
}

func TestAllowIPs(t *testing.T) {
	tests := []struct {
		name       string
		allow      string
		trustProxy bool
		remote     string
		xff        []string
		wantCode   int
	}{
		{"разрешённый IP", "203.0.113.7", false, "203.0.113.7:5555", nil, 200},
		{"разрешённый IP из CIDR", "10.0.0.0/8", false, "10.9.8.7:1", nil, 200},
		{"чужой IP", "203.0.113.7", false, "8.8.8.8:5555", nil, 403},
		{"чужой IP вне CIDR", "10.0.0.0/8", false, "11.0.0.1:1", nil, 403},
		{"пустой список пропускает всех", "", false, "8.8.8.8:1", nil, 200},
		{"пустой список пропускает даже мусорный адрес", "", true, "garbage", nil, 200},
		{"IPv6 разрешён", "2001:db8::/32", false, "[2001:db8::5]:80", nil, 200},
		{"IPv6 чужой", "2001:db8::/32", false, "[2001:dead::5]:80", nil, 403},
		{"IPv4-mapped разрешён", "1.2.3.4", false, "[::ffff:1.2.3.4]:80", nil, 200},
		{"IPv4-mapped чужой", "1.2.3.4", false, "[::ffff:1.2.3.5]:80", nil, 403},
		{"IPv4-mapped в CIDR", "1.2.3.0/24", false, "[::ffff:1.2.3.200]:80", nil, 200},
		{"неразбираемый RemoteAddr", "1.2.3.4", false, "garbage", nil, 403},
		{"пустой RemoteAddr", "1.2.3.4", false, "", nil, 403},

		{"trustProxy loopback берёт XFF", "203.0.113.7", true, "127.0.0.1:4000", []string{"203.0.113.7"}, 200},
		{"trustProxy loopback IPv6", "203.0.113.7", true, "[::1]:4000", []string{"203.0.113.7"}, 200},
		{"trustProxy loopback чужой XFF", "203.0.113.7", true, "127.0.0.1:4000", []string{"8.8.8.8"}, 403},
		{"trustProxy берёт правый элемент (разрешён)", "203.0.113.7", true, "127.0.0.1:4000",
			[]string{"8.8.8.8, 203.0.113.7"}, 200},
		{"trustProxy правый элемент чужой, левый разрешён", "203.0.113.7", true, "127.0.0.1:4000",
			[]string{"203.0.113.7, 8.8.8.8"}, 403},
		{"trustProxy правый элемент из нескольких заголовков", "203.0.113.7", true, "127.0.0.1:4000",
			[]string{"8.8.8.8", "203.0.113.7"}, 200},
		{"trustProxy XFF с IPv4-mapped", "1.2.3.4", true, "127.0.0.1:4000", []string{"::ffff:1.2.3.4"}, 200},
		{"trustProxy XFF мусор", "203.0.113.7", true, "127.0.0.1:4000", []string{"not-an-ip"}, 403},
		{"trustProxy без XFF: сам loopback не в списке", "203.0.113.7", true, "127.0.0.1:4000", nil, 403},
		{"trustProxy без XFF: loopback в списке", "127.0.0.1", true, "127.0.0.1:4000", nil, 200},

		{"XFF игнорируется при не-loopback (подделка)", "203.0.113.7", true, "8.8.8.8:4000",
			[]string{"203.0.113.7"}, 403},
		{"XFF не-loopback, RemoteAddr разрешён", "203.0.113.7", true, "203.0.113.7:4000",
			[]string{"8.8.8.8"}, 200},
		{"XFF игнорируется при trustProxy=false (подделка)", "203.0.113.7", false, "127.0.0.1:4000",
			[]string{"203.0.113.7"}, 403},
		{"trustProxy=false: решает RemoteAddr", "127.0.0.1", false, "127.0.0.1:4000",
			[]string{"8.8.8.8"}, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			h := AllowIPs(mustAllow(t, tt.allow), tt.trustProxy, next)

			req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
			req.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tt.wantCode)
			}
			if wantCalled := tt.wantCode == 200; called != wantCalled {
				t.Fatalf("next called = %v, want %v", called, wantCalled)
			}
			if tt.wantCode == http.StatusForbidden {
				if body := rec.Body.String(); body != "forbidden\n" {
					t.Fatalf("body = %q, want %q", body, "forbidden\n")
				}
			}
		})
	}
}

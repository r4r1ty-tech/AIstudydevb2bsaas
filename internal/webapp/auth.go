package webapp

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

func passwordFromRequest(r *http.Request) string {
	logx.Debugf("webapp", "passwordFromRequest: enter")
	if r == nil {
		logx.Debugf("webapp", "passwordFromRequest: nil request -> empty")
		return ""
	}
	if v := strings.TrimSpace(r.Header.Get("X-Panel-Password")); v != "" {
		logx.Debugf("webapp", "passwordFromRequest: X-Panel-Password present len=%d", len(v))
		return v
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		logx.Debugf("webapp", "passwordFromRequest: Authorization Bearer present len=%d", len(auth)-len(prefix))
		return strings.TrimSpace(auth[len(prefix):])
	}
	logx.Debugf("webapp", "passwordFromRequest: no credentials header present")
	return ""
}

func passwordOK(got, want string) bool {
	logx.Debugf("webapp", "passwordOK: got_present=%v want_present=%v got_len=%d want_len=%d", got != "", want != "", len(got), len(want))
	if want == "" || got == "" {
		logx.Debugf("webapp", "passwordOK: -> false (empty side)")
		return false
	}
	if len(got) != len(want) {
		logx.Debugf("webapp", "passwordOK: -> false (length mismatch)")
		return false
	}
	ok := subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
	logx.Debugf("webapp", "passwordOK: -> %v", ok)
	return ok
}

package bbb

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

func FindChrome(explicit string) string {
	logx.Debugf("bbb", "FindChrome: explicit=%q env=%q", explicit, os.Getenv("CHROME_BIN"))
	for _, c := range []string{
		explicit,
		os.Getenv("CHROME_BIN"),
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/google-chrome",
	} {
		if c == "" {
			continue
		}
		fi, err := os.Stat(c)
		if err == nil && !fi.IsDir() {
			abs, err := filepath.Abs(c)
			if err == nil {
				logx.Debugf("bbb", "FindChrome: found %s", abs)
				return abs
			}
			logx.Debugf("bbb", "FindChrome: abs %s: %v", c, err)
			return c
		}
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			logx.Debugf("bbb", "FindChrome: lookpath %s -> %s", name, p)
			return p
		}
	}
	logx.Warnf("bbb", "FindChrome: chromium not found")
	return ""
}

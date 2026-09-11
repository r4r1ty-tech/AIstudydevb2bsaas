package bbb

import (
	"os"
	"os/exec"
	"path/filepath"
)

func FindChrome(explicit string) string {
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
				return abs
			}
			return c
		}
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

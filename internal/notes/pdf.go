package notes

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// pdfTimeout caps headless Chromium: a hung print must not hold the notes
// job (and the worker shutdown) forever.
const pdfTimeout = 3 * time.Minute

func WritePDF(cfg *config.Config, title, md, pdfPath string) error {
	// RECORDINGS_DIR на VDS относительный, а file://recordings/... Chromium
	// читает как хост «recordings» и отвечает ERR_INVALID_URL.
	abs, err := filepath.Abs(pdfPath)
	if err != nil {
		logx.Errorf("notes", "WritePDF: abs %s: %v", pdfPath, err)
		return fmt.Errorf("WritePDF: abs %s: %w", pdfPath, err)
	}
	pdfPath = abs
	htmlPath := strings.TrimSuffix(pdfPath, filepath.Ext(pdfPath)) + ".html"
	page := markdownHTML(title, md)
	if err := os.WriteFile(htmlPath, []byte(page), 0644); err != nil {
		logx.Errorf("notes", "WritePDF: write html %s: %v", htmlPath, err)
		return fmt.Errorf("WritePDF: write html %s: %w", htmlPath, err)
	}
	bin := ""
	if cfg != nil {
		bin = cfg.ChromeBin
	}
	if bin == "" {
		bin = "chromium"
	}
	// Свой профиль: печать не должна делить каталог с Chromium, сидящим на паре.
	profile, err := os.MkdirTemp("", "ssau-pdf-")
	if err != nil {
		logx.Errorf("notes", "WritePDF: temp profile: %v", err)
		return fmt.Errorf("WritePDF: temp profile: %w", err)
	}
	defer os.RemoveAll(profile)
	// Старый файл не должен сойти за свежий, если печать молча не сработала.
	_ = os.Remove(pdfPath)
	ctx, cancel := context.WithTimeout(context.Background(), pdfTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--no-pdf-header-footer",
		"--user-data-dir="+profile,
		"--print-to-pdf="+pdfPath,
		(&url.URL{Scheme: "file", Path: filepath.ToSlash(htmlPath)}).String(),
	)
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		logx.Errorf("notes", "WritePDF: print-to-pdf не уложился в %s bin=%s", pdfTimeout, bin)
		return fmt.Errorf("print-to-pdf: таймаут %s", pdfTimeout)
	}
	if err != nil {
		logx.Errorf("notes", "WritePDF: print-to-pdf failed bin=%s out=%q: %v", bin, strings.TrimSpace(string(out)), err)
		return fmt.Errorf("print-to-pdf: %s: %w", strings.TrimSpace(string(out)), err)
	}
	st, err := os.Stat(pdfPath)
	if err != nil || st.Size() < 100 {
		logx.Errorf("notes", "WritePDF: pdf не записался path=%s stat_err=%v out=%q", pdfPath, err, truncate(string(out), 400))
		return fmt.Errorf("pdf не записался")
	}
	logx.Infof("notes", "WritePDF: pdf built path=%s bytes=%d", pdfPath, st.Size())
	return nil
}

var mdRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

// markdownHTML renders the LLM Markdown into a printable page. Raw HTML in
// the Markdown is dropped by goldmark, so the model cannot inject markup.
func markdownHTML(title, md string) string {
	var body bytes.Buffer
	if err := mdRenderer.Convert([]byte(md), &body); err != nil {
		logx.Warnf("notes", "markdownHTML: render: %v — печатаю как текст", err)
		body.Reset()
		body.WriteString("<pre>" + html.EscapeString(md) + "</pre>")
	}
	t := html.EscapeString(title)
	out := `<!doctype html><html lang="ru"><head><meta charset="utf-8">
<title>` + t + `</title>
<style>
body{font-family:DejaVu Sans,Liberation Sans,sans-serif;max-width:820px;margin:24px auto;line-height:1.45;color:#111;font-size:14px}
h1{font-size:22px}h2{font-size:18px;margin-top:22px}h3{font-size:16px}h4{font-size:14px}
h1,h2,h3,h4{break-after:avoid}
table{border-collapse:collapse;margin:10px 0}
th,td{border:1px solid #999;padding:4px 8px;text-align:left;vertical-align:top}
code,pre{font-family:DejaVu Sans Mono,Liberation Mono,monospace;font-size:12.5px}
pre{background:#f3f3f3;padding:8px;white-space:pre-wrap}
blockquote{border-left:3px solid #bbb;margin-left:0;padding-left:12px;color:#333}
</style></head><body>
<h1>` + t + `</h1>
<article>` + body.String() + `</article>
</body></html>`
	return out
}

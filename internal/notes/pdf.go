package notes

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

func WritePDF(cfg *config.Config, title, md, pdfPath string) error {
	htmlPath := strings.TrimSuffix(pdfPath, filepath.Ext(pdfPath)) + ".html"
	logx.Debugf("notes", "WritePDF: start title=%q md_chars=%d pdf=%s html=%s", title, len(md), pdfPath, htmlPath)
	page := markdownHTML(title, md)
	if err := os.WriteFile(htmlPath, []byte(page), 0644); err != nil {
		logx.Errorf("notes", "WritePDF: write html %s: %v", htmlPath, err)
		return fmt.Errorf("WritePDF: write html %s: %w", htmlPath, err)
	}
	logx.Debugf("notes", "WritePDF: html written bytes=%d", len(page))
	bin := ""
	if cfg != nil {
		bin = cfg.ChromeBin
	}
	if bin == "" {
		bin = "chromium"
	}
	logx.Debugf("notes", "WritePDF: chrome bin=%s", bin)
	cmd := exec.Command(bin,
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--no-pdf-header-footer",
		"--print-to-pdf="+pdfPath,
		"file://"+htmlPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		logx.Errorf("notes", "WritePDF: print-to-pdf failed bin=%s out=%q: %v", bin, strings.TrimSpace(string(out)), err)
		return fmt.Errorf("print-to-pdf: %s: %w", strings.TrimSpace(string(out)), err)
	}
	logx.Debugf("notes", "WritePDF: chromium done out=%q", strings.TrimSpace(string(out)))
	st, err := os.Stat(pdfPath)
	if err != nil || st.Size() < 100 {
		logx.Errorf("notes", "WritePDF: pdf не записался path=%s stat_err=%v", pdfPath, err)
		return fmt.Errorf("pdf не записался")
	}
	logx.Infof("notes", "WritePDF: pdf built path=%s bytes=%d", pdfPath, st.Size())
	return nil
}

func markdownHTML(title, md string) string {
	logx.Debugf("notes", "markdownHTML: title_len=%d md_chars=%d", len(title), len(md))
	esc := html.EscapeString(md)
	esc = strings.ReplaceAll(esc, "\n", "<br>\n")
	t := html.EscapeString(title)
	out := `<!doctype html><html lang="ru"><head><meta charset="utf-8">
<title>` + t + `</title>
<style>
body{font-family:DejaVu Sans,Liberation Sans,sans-serif;max-width:820px;margin:24px auto;line-height:1.45;color:#111}
h1{font-size:22px}
</style></head><body>
<h1>` + t + `</h1>
<article>` + esc + `</article>
</body></html>`
	logx.Debugf("notes", "markdownHTML: html_chars=%d", len(out))
	return out
}

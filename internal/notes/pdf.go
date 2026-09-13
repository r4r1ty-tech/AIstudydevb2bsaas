package notes

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
)

func WritePDF(cfg *config.Config, title, md, pdfPath string) error {
	htmlPath := strings.TrimSuffix(pdfPath, filepath.Ext(pdfPath)) + ".html"
	page := markdownHTML(title, md)
	if err := os.WriteFile(htmlPath, []byte(page), 0644); err != nil {
		return err
	}
	bin := ""
	if cfg != nil {
		bin = cfg.ChromeBin
	}
	if bin == "" {
		bin = "chromium"
	}
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
		return fmt.Errorf("print-to-pdf: %s: %w", strings.TrimSpace(string(out)), err)
	}
	st, err := os.Stat(pdfPath)
	if err != nil || st.Size() < 100 {
		return fmt.Errorf("pdf не записался")
	}
	return nil
}

func markdownHTML(title, md string) string {
	esc := html.EscapeString(md)
	esc = strings.ReplaceAll(esc, "\n", "<br>\n")
	t := html.EscapeString(title)
	return `<!doctype html><html lang="ru"><head><meta charset="utf-8">
<title>` + t + `</title>
<style>
body{font-family:DejaVu Sans,Liberation Sans,sans-serif;max-width:820px;margin:24px auto;line-height:1.45;color:#111}
h1{font-size:22px}
</style></head><body>
<h1>` + t + `</h1>
<article>` + esc + `</article>
</body></html>`
}

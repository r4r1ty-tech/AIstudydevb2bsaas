package tg

import (
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

const htmlMode = "HTML"

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func esc(s string) string {
	logx.Debugf("tg", "esc: len=%d", len(s))
	return htmlEscaper.Replace(s)
}

func bold(s string) string {
	logx.Debugf("tg", "bold: len=%d", len(s))
	return "<b>" + esc(s) + "</b>"
}

func italic(s string) string {
	logx.Debugf("tg", "italic: len=%d", len(s))
	return "<i>" + esc(s) + "</i>"
}

func code(s string) string {
	logx.Debugf("tg", "code: len=%d", len(s))
	return "<code>" + esc(s) + "</code>"
}

func hlink(url, text string) string {
	logx.Debugf("tg", "hlink: url_len=%d text_len=%d", len(url), len(text))
	return `<a href="` + esc(url) + `">` + esc(text) + `</a>`
}

func dashOr(s string) string {
	logx.Debugf("tg", "dashOr: raw=%q", strings.TrimSpace(s))
	s = strings.TrimSpace(s)
	if s == "" {
		return "—"
	}
	return s
}

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

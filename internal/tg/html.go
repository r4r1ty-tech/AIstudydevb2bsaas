package tg

import "strings"

const htmlMode = "HTML"

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func esc(s string) string {
	return htmlEscaper.Replace(s)
}

func bold(s string) string {
	return "<b>" + esc(s) + "</b>"
}

func italic(s string) string {
	return "<i>" + esc(s) + "</i>"
}

func code(s string) string {
	return "<code>" + esc(s) + "</code>"
}

func hlink(url, text string) string {
	return `<a href="` + esc(url) + `">` + esc(text) + `</a>`
}

func dashOr(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "—"
	}
	return s
}

func knownCommand(text string) bool {
	name := commandName(text)
	if name == "" {
		return false
	}
	switch name {
	case "start", "panel", "test", "help", "today", "notes",
		"settings", "words", "link", "links", "cancel":
		return true
	default:
		return false
	}
}

func commandName(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	text = text[1:]
	if i := strings.IndexAny(text, " \n\t"); i >= 0 {
		text = text[:i]
	}
	if i := strings.IndexByte(text, '@'); i >= 0 {
		text = text[:i]
	}
	return strings.ToLower(strings.TrimSpace(text))
}

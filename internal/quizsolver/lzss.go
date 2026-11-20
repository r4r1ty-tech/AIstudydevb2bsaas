package quizsolver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// LZSSToken — запись [0'символ'] (литерал) или [1<смещение,длина>] (ссылка).
type LZSSToken struct {
	Literal bool
	Char    rune
	Offset  int
	Length  int
}

var reLZSSLiteral = regexp.MustCompile(`\[\s*0\s*'([^']*)'\s*\]`)
var reLZSSCopy = regexp.MustCompile(`\[\s*1\s*<\s*(\d+)\s*,\s*(\d+)\s*>\s*\]`)
var reLZSSAny = regexp.MustCompile(`\[\s*[01]\s*(?:'[^']*'|<\s*\d+\s*,\s*\d+\s*>)\s*\]`)

// EncodeLZSSTokens жадно кодирует сообщение: совпадение от 2 символов —
// ссылка, иначе литерал. Смещение отсчитывается назад от текущей позиции.
func EncodeLZSS(msg string, window, buffer int) (string, bool) {
	r := []rune(msg)
	if len(r) == 0 || window <= 0 || buffer <= 0 {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(r); {
		maxLen := min(buffer, len(r)-i)
		bestLen, bestOff := 0, 0
		for s := i - 1; s >= 0 && i-s <= window; s-- {
			l := 0
			for l < maxLen && r[s+l] == r[i+l] {
				l++
			}
			if l > bestLen {
				bestLen, bestOff = l, i-s
			}
		}
		if bestLen >= 2 {
			fmt.Fprintf(&b, "[1<%d,%d>]", bestOff, bestLen)
			i += bestLen
			continue
		}
		fmt.Fprintf(&b, "[0'%c']", r[i])
		i++
	}
	code := b.String()
	back, ok := DecodeLZSS(code, window, buffer)
	if !ok || back != msg {
		return "", false
	}
	return code, true
}

// DecodeLZSS разбирает и декодирует записи LZSS с проверкой ссылок.
func DecodeLZSS(code string, window, buffer int) (string, bool) {
	ms := reLZSSAny.FindAllStringIndex(code, -1)
	if len(ms) == 0 {
		return "", false
	}
	rest := []byte(code)
	for _, m := range ms {
		for i := m[0]; i < m[1]; i++ {
			rest[i] = ' '
		}
	}
	if strings.TrimSpace(string(rest)) != "" {
		return "", false
	}
	var out []rune
	for _, m := range ms {
		rec := code[m[0]:m[1]]
		if lm := reLZSSLiteral.FindStringSubmatch(rec); lm != nil {
			ch := []rune(lm[1])
			if len(ch) != 1 {
				return "", false
			}
			out = append(out, ch[0])
			continue
		}
		cm := reLZSSCopy.FindStringSubmatch(rec)
		if cm == nil {
			return "", false
		}
		off, err1 := strconv.Atoi(cm[1])
		ln, err2 := strconv.Atoi(cm[2])
		if err1 != nil || err2 != nil || ln < 2 {
			return "", false
		}
		if off <= 0 || off > window || off > len(out) {
			return "", false
		}
		if buffer > 0 && ln > buffer {
			return "", false
		}
		for k := 0; k < ln; k++ {
			out = append(out, out[len(out)-off])
		}
	}
	return string(out), true
}

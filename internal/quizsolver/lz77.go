package quizsolver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// LZ77Token — запись <смещение,длина,символ>: смещение назад от текущей
// позиции, длина совпадения и символ, идущий после совпадения.
type LZ77Token struct {
	Offset int
	Length int
	Next   rune
}

var reLZ77Token = regexp.MustCompile(`[<(](\d+)\s*,\s*(\d+)\s*,\s*([^>)]*)[>)]`)

// EncodeLZ77Tokens жадно ищет самое длинное совпадение в окне window и
// возвращает поток записей. Смещение отсчитывается назад от текущей позиции,
// без совпадения — <0,0,символ>.
func EncodeLZ77Tokens(msg string, window, buffer int) ([]LZ77Token, bool) {
	r := []rune(msg)
	if len(r) == 0 || window <= 0 || buffer <= 0 {
		return nil, false
	}
	toks := make([]LZ77Token, 0, len(r))
	for i := 0; i < len(r); {
		maxLen := min(buffer, len(r)-i)
		bestLen, bestOff := 0, 0
		// Ближние совпадения проверяются первыми: при равной длине выбираем
		// минимальное смещение — так запись короче и результат стабильнее.
		for s := i - 1; s >= 0 && i-s <= window; s-- {
			l := 0
			for l < maxLen && r[s+l] == r[i+l] {
				l++
			}
			if l > bestLen {
				bestLen, bestOff = l, i-s
			}
		}
		// Последний символ сообщения всегда должен попасть в запись как
		// «следующий символ», поэтому совпадение до конца укорачиваем.
		if bestLen > 0 && bestLen == len(r)-i {
			bestLen--
		}
		if bestLen == 0 {
			toks = append(toks, LZ77Token{Next: r[i]})
			i++
			continue
		}
		toks = append(toks, LZ77Token{Offset: bestOff, Length: bestLen, Next: r[i+bestLen]})
		i += bestLen + 1
	}
	return toks, true
}

// ParseLZ77 разбирает поток записей <смещение,длина,символ>.
func ParseLZ77(code string) ([]LZ77Token, bool) {
	ms := reLZ77Token.FindAllStringSubmatchIndex(code, -1)
	if len(ms) == 0 {
		return nil, false
	}
	var toks []LZ77Token
	rest := []byte(code)
	for _, m := range ms {
		off, err1 := strconv.Atoi(code[m[2]:m[3]])
		ln, err2 := strconv.Atoi(code[m[4]:m[5]])
		if err1 != nil || err2 != nil {
			return nil, false
		}
		ch := []rune(code[m[6]:m[7]])
		if len(ch) != 1 {
			return nil, false
		}
		toks = append(toks, LZ77Token{Offset: off, Length: ln, Next: ch[0]})
		for i := m[0]; i < m[1]; i++ {
			rest[i] = ' '
		}
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, false
	}
	return toks, true
}

// DecodeLZ77Tokens восстанавливает сообщение по потоку записей. window <= 0
// означает «размер словаря не задан», buffer <= 0 — «буфер не ограничен».
func DecodeLZ77Tokens(toks []LZ77Token, window, buffer int) (string, bool) {
	if len(toks) == 0 {
		return "", false
	}
	var out []rune
	for _, t := range toks {
		if t.Length < 0 || (buffer > 0 && t.Length > buffer) {
			return "", false
		}
		if t.Length == 0 {
			if t.Offset != 0 {
				return "", false
			}
		} else {
			if t.Offset <= 0 || (window > 0 && t.Offset > window) || t.Offset > len(out) {
				return "", false
			}
			for k := 0; k < t.Length; k++ {
				out = append(out, out[len(out)-t.Offset])
			}
		}
		out = append(out, t.Next)
	}
	return string(out), true
}

// FormatLZ77 склеивает записи в формат <смещение,длина,символ>.
func FormatLZ77(toks []LZ77Token) string {
	var b strings.Builder
	for _, t := range toks {
		fmt.Fprintf(&b, "<%d,%d,%c>", t.Offset, t.Length, t.Next)
	}
	return b.String()
}

// EncodeLZ77 сжимает сообщение и проверяет себя декодированием.
func EncodeLZ77(msg string, window, buffer int) (string, bool) {
	toks, ok := EncodeLZ77Tokens(msg, window, buffer)
	if !ok {
		return "", false
	}
	code := FormatLZ77(toks)
	back, ok := DecodeLZ77Tokens(toks, window, buffer)
	if !ok || back != msg {
		return "", false
	}
	return code, true
}

// DecodeLZ77 разбирает и декодирует сообщение с проверкой словаря и индексов.
func DecodeLZ77(code string, window, buffer int) (string, bool) {
	toks, ok := ParseLZ77(code)
	if !ok {
		return "", false
	}
	return DecodeLZ77Tokens(toks, window, buffer)
}

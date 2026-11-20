package quizsolver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// LZ78Token — запись [номер,символ]; Terminal — запись [номер] без символа,
// когда остаток сообщения целиком совпал со словарной фразой.
type LZ78Token struct {
	Index    int
	Char     rune
	Terminal bool
}

var reLZ78Token = regexp.MustCompile(`\[\s*(\d+)\s*,?\s*([^\]]*)\]`)

// EncodeLZ78Tokens кодирует сообщение; dictLimit — предел числа словарных
// записей (<= 0 — без предела).
func EncodeLZ78Tokens(msg string, dictLimit int) ([]LZ78Token, bool) {
	r := []rune(msg)
	if len(r) == 0 {
		return nil, false
	}
	dict := map[string]int{}
	entries := 0
	toks := make([]LZ78Token, 0, len(r))
	for i := 0; i < len(r); {
		bestIdx, bestLen := 0, 0
		for l := 1; l <= len(r)-i; l++ {
			if idx, ok := dict[string(r[i:i+l])]; ok {
				bestIdx, bestLen = idx, l
			}
		}
		if bestLen == len(r)-i {
			toks = append(toks, LZ78Token{Index: bestIdx, Terminal: true})
			break
		}
		ch := r[i+bestLen]
		toks = append(toks, LZ78Token{Index: bestIdx, Char: ch})
		if dictLimit <= 0 || entries < dictLimit {
			entries++
			dict[string(r[i:i+bestLen+1])] = entries
		}
		i += bestLen + 1
	}
	return toks, true
}

// ParseLZ78 разбирает записи [номер,символ] и [номер].
func ParseLZ78(code string) ([]LZ78Token, bool) {
	ms := reLZ78Token.FindAllStringSubmatchIndex(code, -1)
	if len(ms) == 0 {
		return nil, false
	}
	var toks []LZ78Token
	rest := []byte(code)
	for _, m := range ms {
		idx, err := strconv.Atoi(code[m[2]:m[3]])
		if err != nil {
			return nil, false
		}
		sym := strings.TrimSpace(code[m[4]:m[5]])
		ch := []rune(sym)
		if len(ch) > 1 {
			return nil, false
		}
		t := LZ78Token{Index: idx, Terminal: len(ch) == 0}
		if !t.Terminal {
			t.Char = ch[0]
		}
		toks = append(toks, t)
		for i := m[0]; i < m[1]; i++ {
			rest[i] = ' '
		}
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, false
	}
	return toks, true
}

// DecodeLZ78Tokens восстанавливает сообщение; dictLimit — предел словаря,
// при котором новые записи перестают добавляться (<= 0 — без предела).
func DecodeLZ78Tokens(toks []LZ78Token, dictLimit int) (string, bool) {
	if len(toks) == 0 {
		return "", false
	}
	dict := []string{""} // индекс 0 — пустая фраза, записи нумеруются с 1
	var out strings.Builder
	for i, t := range toks {
		if t.Index < 0 || t.Index >= len(dict) {
			return "", false
		}
		if t.Terminal {
			// Запись [номер] допустима только последней.
			if t.Index == 0 || i != len(toks)-1 {
				return "", false
			}
			out.WriteString(dict[t.Index])
			break
		}
		word := dict[t.Index] + string(t.Char)
		out.WriteString(word)
		if dictLimit <= 0 || len(dict)-1 < dictLimit {
			dict = append(dict, word)
		}
	}
	return out.String(), true
}

// FormatLZ78 склеивает записи; comma — писать ли запятую между номером и символом.
func FormatLZ78(toks []LZ78Token, comma bool) string {
	var b strings.Builder
	for _, t := range toks {
		if t.Terminal {
			fmt.Fprintf(&b, "[%d]", t.Index)
			continue
		}
		if comma {
			fmt.Fprintf(&b, "[%d,%c]", t.Index, t.Char)
		} else {
			fmt.Fprintf(&b, "[%d%c]", t.Index, t.Char)
		}
	}
	return b.String()
}

// EncodeLZ78 сжимает сообщение и проверяет себя декодированием.
func EncodeLZ78(msg string, dictLimit int, comma bool) (string, bool) {
	toks, ok := EncodeLZ78Tokens(msg, dictLimit)
	if !ok {
		return "", false
	}
	back, ok := DecodeLZ78Tokens(toks, dictLimit)
	if !ok || back != msg {
		return "", false
	}
	return FormatLZ78(toks, comma), true
}

// DecodeLZ78 разбирает и декодирует сообщение с проверкой словаря и индексов.
func DecodeLZ78(code string, dictLimit int) (string, bool) {
	toks, ok := ParseLZ78(code)
	if !ok {
		return "", false
	}
	return DecodeLZ78Tokens(toks, dictLimit)
}

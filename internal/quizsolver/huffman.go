package quizsolver

import (
	"regexp"
	"sort"
	"strings"
)

// CodePair — символ и его двоичный код из условия.
type CodePair struct {
	Symbol rune
	Code   string
}

var (
	reQuotedPair = regexp.MustCompile(`'([^']*)'\s*([01]+)`)
	reDoublePair = regexp.MustCompile(`"([^"]*)"\s*([01]+)`)
	rePlainPair  = regexp.MustCompile(`(?m)(?:^|[\s,;])([^\s,;:=<>0-9]{1,2})\s*[-:=]?\s*([01]{1,})`)
)

// ParseQuotedPairs разбирает трассу адаптивного кодирования вида
// 'R'0'T'01100: символ в кавычках и выданный для него код.
func ParseQuotedPairs(s string) ([]CodePair, bool) {
	re := reQuotedPair
	if len(re.FindAllStringSubmatchIndex(s, -1)) == 0 {
		re = reDoublePair
	}
	ms := re.FindAllStringSubmatchIndex(s, -1)
	if len(ms) == 0 {
		return nil, false
	}
	rest := []byte(s)
	var pairs []CodePair
	for _, m := range ms {
		sym := []rune(s[m[2]:m[3]])
		if len(sym) != 1 {
			return nil, false
		}
		pairs = append(pairs, CodePair{Symbol: sym[0], Code: s[m[4]:m[5]]})
		for i := m[0]; i < m[1]; i++ {
			rest[i] = ' '
		}
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, false
	}
	return pairs, true
}

// DecodeHuffmanTrace декодирует условие-трассу адаптивного Хаффмана: каждая
// запись «'символ'код» — очередной символ сообщения и его код на этот момент.
// Текст — последовательность этих символов; коды проверяются на корректность.
func DecodeHuffmanTrace(s string) (string, bool) {
	pairs, ok := ParseQuotedPairs(s)
	if !ok || len(pairs) < 2 {
		return "", false
	}
	seen := map[rune]bool{}
	var b strings.Builder
	for _, p := range pairs {
		if p.Code == "" || seen[p.Symbol] {
			return "", false
		}
		seen[p.Symbol] = true
		b.WriteRune(p.Symbol)
	}
	return b.String(), true
}

// ParseCodeTable разбирает таблицу «символ — код». Поддерживаются формы
// «'А'0», «"А"0», «А 0», «А-0», «А: 0», «А=0».
func ParseCodeTable(s string) ([]CodePair, bool) {
	pairs, ok := ParseQuotedPairs(s)
	if ok {
		return pairs, true
	}
	var out []CodePair
	for _, m := range rePlainPair.FindAllStringSubmatch(s, -1) {
		sym := []rune(m[1])
		if len(sym) != 1 {
			continue
		}
		out = append(out, CodePair{Symbol: sym[0], Code: m[2]})
	}
	if len(out) < 2 {
		return nil, false
	}
	return out, true
}

// DecodeBits декодирует битовую строку по таблице код → символ.
func DecodeBits(pairs []CodePair, bits string) (string, bool) {
	if len(pairs) == 0 || bits == "" {
		return "", false
	}
	// Префиксное свойство: иначе разбор неоднозначен.
	sorted := make([]CodePair, len(pairs))
	copy(sorted, pairs)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i].Code) < len(sorted[j].Code) })
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if strings.HasPrefix(sorted[j].Code, sorted[i].Code) {
				return "", false
			}
		}
	}
	lookup := make(map[string]rune, len(pairs))
	for _, p := range pairs {
		if _, dup := lookup[p.Code]; dup {
			return "", false
		}
		lookup[p.Code] = p.Symbol
	}
	var out strings.Builder
	for i := 0; i < len(bits); {
		found := false
		for l := len(bits) - i; l >= 1; l-- {
			if sym, ok := lookup[bits[i:i+l]]; ok {
				out.WriteRune(sym)
				i += l
				found = true
				break
			}
		}
		if !found {
			return "", false
		}
	}
	return out.String(), true
}

// EncodeByTable кодирует текст по заданной таблице и проверяет себя обратным
// декодированием.
func EncodeByTable(text string, pairs []CodePair) (string, bool) {
	if text == "" || len(pairs) == 0 {
		return "", false
	}
	lookup := make(map[rune]string, len(pairs))
	for _, p := range pairs {
		if _, dup := lookup[p.Symbol]; dup {
			return "", false
		}
		lookup[p.Symbol] = p.Code
	}
	var b strings.Builder
	for _, r := range text {
		code, ok := lookup[r]
		if !ok {
			return "", false
		}
		b.WriteString(code)
	}
	back, ok := DecodeBits(pairs, b.String())
	if !ok || back != text {
		return "", false
	}
	return b.String(), true
}

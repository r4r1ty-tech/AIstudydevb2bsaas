// Package quizsolver — детерминированные решатели задач по теории информации
// для тестов Moodle. Функции пакета чистые: без сети, базы и глобального
// состояния. Модель привлекается только для извлечения параметров, а здесь
// ответ считается кодом и обязательно проверяется самопроверкой.
package quizsolver

import (
	"regexp"
	"strconv"
	"strings"
)

// Result — проверенный ответ и алгоритм, которым он получен.
type Result struct {
	Answer    string
	Algorithm string
}

var (
	reDictSize = regexp.MustCompile(`(?i)словар\w*[^\d]{0,12}(\d+)`)
	reBufSize  = regexp.MustCompile(`(?i)буфер\w*[^\d]{0,12}(\d+)`)

	reTailDict = regexp.MustCompile(`(?i)\s+(?:размер\s+)?(?:словар|буфер)\w*`)
	// \b в Go работает по ASCII, для кириллицы границу слова он не видит.
	reTailWrite   = regexp.MustCompile(`(?i)\s+запишите`)
	reTailFormat  = regexp.MustCompile(`(?i)\s+формат\s+`)
	reTailExample = regexp.MustCompile(`(?i)\s+пример\s+`)
	reTailAnswer  = regexp.MustCompile(`(?i)\s+ответ`)

	reDecode = regexp.MustCompile(`(?i)декодир|расшифр|восстанов`)
	reEncode = regexp.MustCompile(`(?i)сжать|сжатие|закодир|кодирован`)

	reNoSpace   = regexp.MustCompile(`(?i)без\s+пробел`)
	reWithSpace = regexp.MustCompile(`(?i)с\s+пробел`)
)

// payload возвращает содержательную часть условия после первого двоеточия,
// отрезая служебный хвост («Словарь - …», «Размер буфера - …», «Запишите …»).
func payload(cond string) (string, bool) {
	i := strings.Index(cond, ":")
	if i < 0 {
		return "", false
	}
	rest := cond[i+1:]
	cut := len(rest)
	for _, re := range []*regexp.Regexp{reTailDict, reTailWrite, reTailFormat, reTailExample, reTailAnswer} {
		if m := re.FindStringIndex(rest); m != nil && m[0] < cut {
			cut = m[0]
		}
	}
	out := strings.TrimSpace(rest[:cut])
	return out, out != ""
}

// dictSize разбирает «словарь N символов».
func dictSize(cond string) (int, bool) { return sizeOf(reDictSize, cond) }

// bufSize разбирает «размер буфера M символов».
func bufSize(cond string) (int, bool) { return sizeOf(reBufSize, cond) }

func sizeOf(re *regexp.Regexp, cond string) (int, bool) {
	m := re.FindStringSubmatch(cond)
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// isDecode — условие требует декодирования. Проверяется раньше сжатия:
// «декодировать сообщение, сжатое алгоритмом» содержит оба глагола.
func isDecode(cond string) bool { return reDecode.MatchString(cond) }

// isEncode — условие требует сжатия/кодирования.
func isEncode(cond string) bool { return reEncode.MatchString(cond) }

// applyCase приводит ответ к регистру, который просит условие.
func applyCase(s, cond string) string {
	l := strings.ToLower(cond)
	switch {
	case strings.Contains(l, "заглавными"), strings.Contains(l, "прописными"),
		strings.Contains(l, "большими"), strings.Contains(l, "верхн"):
		return strings.ToUpper(s)
	case strings.Contains(l, "строчными"), strings.Contains(l, "маленькими"),
		strings.Contains(l, "нижн"):
		return strings.ToLower(s)
	}
	return s
}

// recordSep — разделитель между записями сжатого сообщения.
func recordSep(cond string) string {
	if reNoSpace.MatchString(cond) {
		return ""
	}
	if reWithSpace.MatchString(cond) {
		return " "
	}
	return ""
}

// hammingParityOdd — в условии задана нечётная чётность.
func hammingParityOdd(cond string) bool {
	l := strings.ToLower(cond)
	return strings.Contains(l, "нечёт") || strings.Contains(l, "нечет")
}

// bitRun собирает строку из нулей и единиц, допуская пробелы-разделители.
func bitRun(s string) (string, bool) {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '0', '1':
			b.WriteRune(r)
		case ' ', '\t', '\n', '\r':
		default:
			return "", false
		}
	}
	if b.Len() == 0 {
		return "", false
	}
	return b.String(), true
}

// pickBits ищет в тексте самый длинный фрагмент из 0 и 1.
func pickBits(s string) (string, bool) {
	trimmed := strings.Trim(s, " \t\r\n.,;:!?")
	if bits, ok := bitRun(trimmed); ok && strings.ContainsAny(bits, "01") {
		return bits, true
	}
	var best string
	ambiguous := false
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, ".,;:!?()[]")
		bits, ok := bitRun(f)
		if !ok {
			continue
		}
		switch {
		case len(bits) > len(best):
			best, ambiguous = bits, false
		case len(bits) == len(best) && bits != best:
			ambiguous = true
		}
	}
	if ambiguous || best == "" {
		return "", false
	}
	return best, true
}

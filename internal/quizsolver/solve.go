package quizsolver

import (
	"regexp"
	"strings"
)

// Solve разбирает условие теста и, если задача распознана однозначно,
// возвращает проверенный ответ. Второй результат false — решатель не берётся:
// тогда слот остаётся модели.
func Solve(question string) (Result, bool) {
	q := strings.Join(strings.Fields(question), " ")
	if q == "" {
		return Result{}, false
	}
	switch {
	case reLZ77.MatchString(q):
		return solveLZ(q, 77)
	case reLZ78.MatchString(q):
		return solveLZ(q, 78)
	case reLZSS.MatchString(q):
		return solveLZ(q, 0)
	}
	// Хаффман: только когда в условии есть таблица/трасса «символ'код'».
	if strings.Contains(strings.ToLower(q), "хаффман") {
		return solveHuffman(q)
	}
	return Result{}, false
}

var (
	// «LZ7 7», «LZ77», «LZS S» — текст из Moodle иногда рвётся тегом.
	reLZ77 = regexp.MustCompile(`(?i)\bLZ\s*7\s*7\b`)
	reLZ78 = regexp.MustCompile(`(?i)\bLZ\s*7\s*8\b`)
	reLZSS = regexp.MustCompile(`(?i)\bLZ\s*S\s*S\b`)

	// reRecordComma — формат записи задан как «[число,буква]».
	reRecordComma = regexp.MustCompile(`(?i)\[\s*число\s*,`)
)

// solveLZ решает задачи LZ77/LZ78/LZSS: algo 77, 78 или 0 для LZSS.
func solveLZ(question string, algo int) (Result, bool) {
	body, ok := payload(question)
	if !ok {
		return Result{}, false
	}
	window, hasWindow := dictSize(question)
	buffer, hasBuffer := bufSize(question)
	decode := isDecode(question)
	if !decode && !isEncode(question) {
		return Result{}, false
	}

	switch algo {
	case 78:
		if decode {
			msg, ok := DecodeLZ78(body, window)
			if !ok {
				return Result{}, false
			}
			return Result{Answer: applyCase(msg, question), Algorithm: "LZ78"}, true
		}
		if !hasWindow {
			return Result{}, false
		}
		code, ok := EncodeLZ78(strings.TrimSpace(body), window, reRecordComma.MatchString(question))
		if !ok {
			return Result{}, false
		}
		return Result{Answer: applyCase(code, question), Algorithm: "LZ78"}, true

	case 77, 0:
		name := "LZ77"
		if algo == 0 {
			name = "LZSS"
		}
		if decode {
			if algo == 0 {
				msg, ok := DecodeLZSS(body, window, buffer)
				if !ok {
					return Result{}, false
				}
				return Result{Answer: applyCase(msg, question), Algorithm: name}, true
			}
			msg, ok := DecodeLZ77(body, window, buffer)
			if !ok {
				return Result{}, false
			}
			return Result{Answer: applyCase(msg, question), Algorithm: name}, true
		}
		if !hasWindow || !hasBuffer {
			return Result{}, false
		}
		msg := strings.TrimSpace(body)
		if algo == 0 {
			code, ok := EncodeLZSS(msg, window, buffer)
			if !ok {
				return Result{}, false
			}
			return Result{Answer: applyCase(code, question), Algorithm: name}, true
		}
		code, ok := EncodeLZ77(msg, window, buffer)
		if !ok {
			return Result{}, false
		}
		return Result{Answer: applyCase(code, question), Algorithm: name}, true
	}
	return Result{}, false
}

// solveHuffman декодирует трассу адаптивного Хаффмана («'R'0'T'01100») или
// битовую строку по заданной таблице кодов. Сжатие без готовой таблицы
// (адаптивный метод по одному примеру) не берётся: формат неоднозначен.
func solveHuffman(question string) (Result, bool) {
	body, ok := payload(question)
	if !ok || isEncode(question) {
		return Result{}, false
	}
	if msg, ok := DecodeHuffmanTrace(body); ok {
		return Result{Answer: applyCase(msg, question), Algorithm: "Хаффман (трасса)"}, true
	}
	pairs, ok := ParseCodeTable(body)
	if !ok {
		return Result{}, false
	}
	bits, ok := pickBits(body)
	if !ok {
		return Result{}, false
	}
	msg, ok := DecodeBits(pairs, bits)
	if !ok {
		return Result{}, false
	}
	return Result{Answer: applyCase(msg, question), Algorithm: "Хаффман (таблица)"}, true
}

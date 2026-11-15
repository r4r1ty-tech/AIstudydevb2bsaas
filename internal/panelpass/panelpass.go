// Package panelpass — личные пароли входа в панель. У каждого из вайтлиста
// свой пароль, он меняется раз в час. Ничего не хранится: пароль — HMAC от
// секретного ключа, Telegram id и номера часа, поэтому бот (выдаёт) и панель
// (проверяет) считают его одинаково, а по паролю панель понимает, кто вошёл.
package panelpass

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"strconv"
	"strings"
	"time"
)

// Label — назначение ключа для vault.Derive.
const Label = "panel-pass/v1"

// Period — как часто меняется пароль. Принимается пароль текущего и
// предыдущего часа, так что выданный пароль живёт от часа до двух.
const Period = time.Hour

// Без 0/o, 1/l/i — чтобы пароль можно было переписать руками.
const alphabet = "23456789abcdefghjkmnpqrstuvwxyz"

const codeLen = 8

func window(t time.Time) int64 { return t.Unix() / int64(Period/time.Second) }

func code(key []byte, id, w int64) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strconv.FormatInt(id, 10) + "|" + strconv.FormatInt(w, 10)))
	sum := m.Sum(nil)
	var b strings.Builder
	for i := 0; i < codeLen; i++ {
		b.WriteByte(alphabet[int(sum[i])%len(alphabet)])
		if i == codeLen/2-1 {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Issue выдаёт пароль для id на момент now и время, до которого он принимается.
func Issue(key []byte, id int64, now time.Time) (string, time.Time) {
	w := window(now)
	until := time.Unix((w+2)*int64(Period/time.Second), 0).In(now.Location())
	return code(key, id, w), until
}

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer("-", "", " ", "").Replace(s)
}

// Who ищет среди ids того, чей пароль совпал с pw (текущий или прошлый час).
func Who(key []byte, ids []int64, pw string, now time.Time) (int64, bool) {
	got := normalize(pw)
	if len(key) == 0 || len(got) != codeLen {
		return 0, false
	}
	w := window(now)
	var found int64
	for _, id := range ids {
		for _, ww := range []int64{w, w - 1} {
			want := normalize(code(key, id, ww))
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 && found == 0 {
				found = id
			}
		}
	}
	return found, found != 0
}

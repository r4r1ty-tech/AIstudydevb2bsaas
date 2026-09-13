package wake

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func Vocab(users []model.User) []string {
	var all []string
	for _, u := range users {
		all = append(all, u.WakeList()...)
	}
	return model.ParseWakeWords(strings.Join(all, " "))
}

func Message(discipline, word string) string {
	return fmt.Sprintf("на паре «%s»: %s", discipline, word)
}

func Match(transcript string, vocab []string) []string {
	tokens := tokenize(transcript)
	if len(tokens) == 0 || len(vocab) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(vocab))
	for _, w := range vocab {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		want[w] = struct{}{}
	}
	var out []string
	seen := make(map[string]struct{})
	for _, tok := range tokens {
		if _, ok := want[tok]; !ok {
			continue
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	return out
}

func tokenize(s string) []string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteByte(' ')
	}
	return model.ParseWakeWords(b.String())
}

func WhoGets(word string, users []model.User) []model.User {
	word = strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		return nil
	}
	out := make([]model.User, 0)
	for _, u := range users {
		if hasWord(u.WakeList(), word) {
			out = append(out, u)
		}
	}
	return out
}

func hasWord(list []string, word string) bool {
	for _, w := range list {
		if w == word {
			return true
		}
	}
	return false
}

func wantsJoin(intent *model.JoinIntent) bool {
	return intent == nil || intent.Decision != model.JoinNo
}

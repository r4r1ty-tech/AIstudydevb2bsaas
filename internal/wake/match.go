package wake

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func Vocab(users []model.User) []string {
	logx.Debugf("wake", "Vocab: enter users=%d", len(users))
	var all []string
	for _, u := range users {
		all = append(all, u.WakeList()...)
	}
	out := model.ParseWakeWords(strings.Join(all, " "))
	logx.Debugf("wake", "Vocab: exit words=%d %v", len(out), out)
	return out
}

func Message(discipline, word string) string {
	logx.Debugf("wake", "Message: discipline=%q word=%q", discipline, word)
	msg := fmt.Sprintf("на паре «%s»: %s", discipline, word)
	logx.Debugf("wake", "Message: exit %q", msg)
	return msg
}

func Match(transcript string, vocab []string) []string {
	logx.Debugf("wake", "Match: enter transcript=%d bytes vocab=%d", len(transcript), len(vocab))
	tokens := tokenize(transcript)
	if len(tokens) == 0 || len(vocab) == 0 {
		logx.Debugf("wake", "Match: exit no tokens=%d vocab=%d", len(tokens), len(vocab))
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
	logx.Debugf("wake", "Match: exit tokens=%d want=%d matched=%v", len(tokens), len(want), out)
	return out
}

func tokenize(s string) []string {
	logx.Debugf("wake", "tokenize: enter %d bytes", len(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteByte(' ')
	}
	out := model.ParseWakeWords(b.String())
	logx.Debugf("wake", "tokenize: exit tokens=%d %v", len(out), out)
	return out
}

func WhoGets(word string, users []model.User) []model.User {
	logx.Debugf("wake", "WhoGets: enter word=%q users=%d", word, len(users))
	word = strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		logx.Debugf("wake", "WhoGets: empty word")
		return nil
	}
	out := make([]model.User, 0)
	for _, u := range users {
		if hasWord(u.WakeList(), word) {
			out = append(out, u)
		}
	}
	logx.Debugf("wake", "WhoGets: exit word=%q matched=%d", word, len(out))
	return out
}

func hasWord(list []string, word string) bool {
	logx.Debugf("wake", "hasWord: enter word=%q list=%d", word, len(list))
	for _, w := range list {
		if w == word {
			logx.Debugf("wake", "hasWord: hit %q", word)
			return true
		}
	}
	logx.Debugf("wake", "hasWord: miss %q", word)
	return false
}

package wake

import (
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestVocabMergesUsers(t *testing.T) {
	t.Parallel()
	v := Vocab([]model.User{
		{FIO: "Иванов Иван", ExtraWords: []string{"лаба"}},
		{FIO: "Петров Пётр", ExtraWords: []string{"лаба", "зачёт"}},
	})
	joined := "," + strings.Join(v, ",") + ","
	for _, w := range append([]string{"иванов", "петров", "лаба", "зачёт"}, model.CommonWakeWords...) {
		if !strings.Contains(joined, ","+strings.ToLower(w)+",") {
			t.Errorf("vocab %v misses %q", v, w)
		}
	}
	if strings.Count(joined, ",лаба,") != 1 {
		t.Errorf("duplicates in %v", v)
	}
	if Vocab(nil) != nil && len(Vocab(nil)) != 0 {
		t.Error("no users → empty vocab")
	}
}

func TestMessageAndNoop(t *testing.T) {
	t.Parallel()
	if got := Message("Сети", "контрольная"); got != "на паре «Сети»: контрольная" {
		t.Errorf("Message = %q", got)
	}
	if got := (noopRecognizer{}).Push([]byte{1, 2}, 16000); got != nil {
		t.Errorf("noop Push = %v", got)
	}
	if newDefaultRecognizer() == nil {
		t.Error("default recognizer")
	}
}

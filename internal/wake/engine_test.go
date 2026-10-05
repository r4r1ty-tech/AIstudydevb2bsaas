package wake

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestMatchBasicWords(t *testing.T) {
	t.Parallel()
	vocab := []string{"тест", "контрольная", "мудл", "moodle"}
	got := Match("Завтра будет ТЕСТ и контрольная в Moodle", vocab)
	want := map[string]bool{"тест": true, "контрольная": true, "moodle": true}
	if len(got) != 3 {
		t.Fatalf("%v", got)
	}
	for _, w := range got {
		if !want[w] {
			t.Fatalf("unexpected %q in %v", w, got)
		}
	}
	if hits := Match("лабораторная работа", vocab); len(hits) != 0 {
		t.Fatalf("lab should not hit: %v", hits)
	}
}

func TestWhoGetsCommonAndSurname(t *testing.T) {
	t.Parallel()
	ivan := modelUser(1, "Иванов Иван")
	petr := modelUser(2, "Петров Пётр")
	users := []model.User{ivan, petr}

	common := WhoGets("тест", users)
	if len(common) != 2 {
		t.Fatalf("common: %d", len(common))
	}
	own := WhoGets("иванов", users)
	if len(own) != 1 || own[0].TelegramID != 1 {
		t.Fatalf("surname: %+v", own)
	}
	if n := WhoGets("петров", users); len(n) != 1 || n[0].TelegramID != 2 {
		t.Fatalf("petrov")
	}
}

func TestEngineCooldown(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	e := NewEngine()
	e.Rec = &FakeRecognizer{Frags: []string{"тест контрольная"}}
	e.Now = func() time.Time { return now }
	pcm := make([]byte, e.needBytes(16000))
	vocab := []string{"тест", "контрольная"}
	hits := e.Feed(pcm, 16000, vocab)
	if len(hits) != 2 {
		t.Fatalf("first: %+v", hits)
	}
	again := e.Feed(pcm, 16000, vocab)
	if len(again) != 0 {
		t.Fatalf("cooldown: %+v", again)
	}
	now = now.Add(e.Cooldown + time.Second)
	third := e.Feed(pcm, 16000, vocab)
	if len(third) != 2 {
		t.Fatalf("after cool: %+v", third)
	}
}

func TestProcRecognizerFakeScript(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "wake.py")
	body := "import sys\n" +
		"data = sys.stdin.buffer.read(4000)\n" +
		"if data:\n" +
		"    sys.stdout.write('тест мудл\\n')\n" +
		"    sys.stdout.flush()\n" +
		"sys.stdin.buffer.read()\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	rec, err := startCmd(exec.Command("python3", script))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	e := NewEngine()
	e.Rec = rec
	e.Cooldown = time.Millisecond
	pcm := make([]byte, e.needBytes(16000))
	var hits []Hit
	for i := 0; i < 20 && len(hits) == 0; i++ {
		hits = append(hits, e.Feed(pcm, 16000, []string{"тест", "мудл"})...)
		time.Sleep(20 * time.Millisecond)
	}
	found := map[string]bool{}
	for _, h := range hits {
		found[h.Word] = true
	}
	if !found["тест"] || !found["мудл"] {
		t.Fatalf("hits=%+v", hits)
	}
}

func modelUser(id int64, fio string) model.User {
	return model.User{TelegramID: id, FIO: fio, Enabled: true, Onboarded: true}
}

func TestMatchWordForms(t *testing.T) {
	t.Parallel()
	vocab := []string{"тест", "контрольная", "алексей", "амелин"}
	for text, want := range map[string]string{
		"на следующей неделе пишем контрольную": "контрольная",
		"после контрольной работы":              "контрольная",
		"будет два теста":                       "тест",
		"результаты тестов выложу":              "тест",
		"спросим алексея":                       "алексей",
		"амелина сегодня нет":                   "амелин",
	} {
		if got := Match(text, vocab); len(got) != 1 || got[0] != want {
			t.Errorf("%q: got %v, want [%s]", text, got, want)
		}
	}
	// Ровно те слова, на которых ложно срабатывала закрытая грамматика 05.10.
	for _, text := range []string{
		"модульное тестирование потом интеграционное тестирование",
		"у тебя вот этот контроллер будет принимать купюры",
		"тестовый стенд и контроль качества",
		"александр ответит",
	} {
		if got := Match(text, vocab); len(got) != 0 {
			t.Errorf("%q: ложное срабатывание %v", text, got)
		}
	}
	if got := Match("тест тесты контрольная контрольную", vocab); len(got) != 2 {
		t.Errorf("формы одного слова должны давать одно срабатывание: %v", got)
	}
}

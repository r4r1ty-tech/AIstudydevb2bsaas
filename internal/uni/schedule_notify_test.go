package uni

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// countSent — сколько отправленных сообщений содержат подстроку sub.
func countSent(sent []string, sub string) int {
	n := 0
	for _, m := range sent {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

func TestQuizSoonSentOnceAndOnOpensChange(t *testing.T) {
	f := newFake(t)
	s, _, sent := setup(t, f)
	s.Cfg = &config.Config{AdminID: 1, LLMAPIKey: "soon-test-key", LLMAPIURL: "http://127.0.0.1:1", LLMModel: "fake_model"}
	ctx := context.Background()

	assign := model.MoodleItem{CMID: 800, Kind: model.MoodleAssign, Title: "ДЗ 1", Course: "ТФЯГ", Status: model.MoodleTodo}
	quiz := watchQuiz(900, at(14, 45, 0), at(15, 10, 0), "")
	prev := []model.MoodleItem{assign}
	cur := []model.MoodleItem{assign, quiz}

	s.announceUpcomingQuizzes(ctx, 1, prev, cur)
	s.announceUpcomingQuizzes(ctx, 1, prev, cur) // повтор того же синка — без новой отправки
	if len(*sent) != 1 {
		t.Fatalf("после двух одинаковых синков отправок %d, want 1: %v", len(*sent), *sent)
	}
	msg := (*sent)[0]
	for _, want := range []string{
		"Скоро тест", "Тест 2", "ТФЯГ", "откроется 14.11 в 14:45", "закроется 14.11 в 15:10",
		"(окно 25 мин)", "Бот решит сам сразу после открытия.", "Открыть в Moodle",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("в сообщении нет %q: %s", want, msg)
		}
	}

	// Открытие сдвинулось: новое сообщение один раз.
	moved := watchQuiz(900, at(14, 50, 0), at(15, 15, 0), "")
	s.announceUpcomingQuizzes(ctx, 1, cur, []model.MoodleItem{assign, moved})
	s.announceUpcomingQuizzes(ctx, 1, []model.MoodleItem{assign, moved}, []model.MoodleItem{assign, moved})
	if n := countSent(*sent, "откроется 14.11 в 14:50"); n != 1 {
		t.Fatalf("сообщение после сдвига открытия %d раз, want 1: %v", n, *sent)
	}
	if len(*sent) != 2 {
		t.Fatalf("всего отправок %d, want 2", len(*sent))
	}
}

// Тесты, которые уже были в базе без изменений, не объявляем (нет рассылки всех предстоящих разом).
// Сданные тесты и тесты с открытием в прошлом — тоже.
func TestQuizSoonIgnoresKnownFinishedAndPast(t *testing.T) {
	f := newFake(t)
	s, _, sent := setup(t, f)
	s.Cfg = &config.Config{AdminID: 1}
	ctx := context.Background()

	known := watchQuiz(900, at(14, 45, 0), at(15, 10, 0), "")
	s.announceUpcomingQuizzes(ctx, 2, []model.MoodleItem{known}, []model.MoodleItem{known})

	done := watchQuiz(901, at(14, 45, 0), at(15, 10, 0), "")
	done.Status = model.MoodleDone
	s.announceUpcomingQuizzes(ctx, 2, []model.MoodleItem{}, []model.MoodleItem{done})

	started := watchQuiz(902, time.Date(2026, 11, 12, 9, 0, 0, 0, samara), at(15, 10, 0), "") // открылся позавчера
	s.announceUpcomingQuizzes(ctx, 2, []model.MoodleItem{}, []model.MoodleItem{started})

	if len(*sent) != 0 {
		t.Fatalf("лишние уведомления: %v", *sent)
	}
}

func TestQuizSoonWithoutAIKeyAsksToSolveYourself(t *testing.T) {
	f := newFake(t)
	s, _, sent := setup(t, f)
	s.Cfg = &config.Config{AdminID: 1} // ключей ИИ нет
	s.announceUpcomingQuizzes(context.Background(), 3, []model.MoodleItem{}, []model.MoodleItem{watchQuiz(900, at(14, 45, 0), at(15, 10, 0), "")})
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "Ключа ИИ нет — пройди сам или добавь ключ в панели.") {
		t.Fatalf("сообщение без ключа: %v", *sent)
	}
}

// «Скоро тест» не больше scheduleNotifyCap за синк; остальные уйдут на следующих синках.
func TestQuizSoonCapPerSync(t *testing.T) {
	f := newFake(t)
	s, _, sent := setup(t, f)
	s.Cfg = &config.Config{AdminID: 1}
	var cur []model.MoodleItem
	for i := 0; i < 7; i++ {
		cur = append(cur, watchQuiz(int64(900+i), at(14, 45, 0), at(15, 10, 0), ""))
	}
	s.announceUpcomingQuizzes(context.Background(), 4, []model.MoodleItem{}, cur)
	if len(*sent) != scheduleNotifyCap {
		t.Fatalf("отправок %d, want %d", len(*sent), scheduleNotifyCap)
	}
	s.announceUpcomingQuizzes(context.Background(), 4, []model.MoodleItem{}, cur)
	if len(*sent) != 7 {
		t.Fatalf("после второго синка отправок %d, want 7", len(*sent))
	}
}

// Напоминание за 15 минут: раз на (tg, cmid, открытие); при сдвиге открытия — снова.
func TestQuizReminderOncePerOpens(t *testing.T) {
	f := newFake(t)
	s, _, sent := setup(t, f)
	ctx := context.Background()
	quiz := watchQuiz(900, at(14, 45, 0), at(15, 10, 0), "")
	finished := watchQuiz(901, at(14, 45, 0), at(15, 10, 0), "")
	finished.Status = model.MoodleSubmitted

	steps := []struct {
		now  time.Time
		item model.MoodleItem
		want int
	}{
		{at(14, 29, 59), quiz, 0}, // ещё рано
		{at(14, 30, 0), quiz, 1},  // за 15 минут
		{at(14, 31, 0), quiz, 1},  // повтора нет
		{at(14, 44, 59), quiz, 1},
		{at(14, 46, 0), quiz, 1}, // уже открыт — не напоминаем
		{at(14, 30, 0), finished, 1},
	}
	for _, sp := range steps {
		s.remindUpcomingQuizzes(ctx, 1, []model.MoodleItem{sp.item}, sp.now)
		if got := countSent(*sent, "Через 15 минут откроется тест"); got != sp.want {
			t.Fatalf("в %s напоминаний %d, want %d", sp.now.Format("15:04:05"), got, sp.want)
		}
	}

	// Открытие перенесли на 15:00: напоминание за 15 минут до нового времени.
	moved := watchQuiz(900, at(15, 0, 0), at(15, 25, 0), "")
	s.remindUpcomingQuizzes(ctx, 1, []model.MoodleItem{moved}, at(14, 46, 0))
	s.remindUpcomingQuizzes(ctx, 1, []model.MoodleItem{moved}, at(14, 50, 0))
	if got := countSent(*sent, "Через 15 минут откроется тест"); got != 2 {
		t.Fatalf("после переноса напоминаний %d, want 2", got)
	}
}

func TestDeadlineMovedOncePerNewDue(t *testing.T) {
	f := newFake(t)
	s, _, sent := setup(t, f)
	ctx := context.Background()

	assign := func(day int) model.MoodleItem {
		d := time.Date(2026, 11, day, 23, 59, 0, 0, samara)
		return model.MoodleItem{CMID: 800, Kind: model.MoodleAssign, Title: "ДЗ 1", Course: "ТФЯГ", Status: model.MoodleTodo, Due: &d}
	}
	prev := []model.MoodleItem{assign(20)}
	cur := []model.MoodleItem{assign(21)}
	s.announceDeadlineMoves(ctx, 1, prev, cur)
	s.announceDeadlineMoves(ctx, 1, prev, cur)
	if len(*sent) != 1 {
		t.Fatalf("отправок %d, want 1: %v", len(*sent), *sent)
	}
	if msg := (*sent)[0]; !strings.Contains(msg, "Срок изменился") || !strings.Contains(msg, "было 20.11.2026 23:59 → стало 21.11.2026 23:59") {
		t.Fatalf("текст: %s", msg)
	}

	// Сданное задание, у которого сдвинулся срок, не объявляем.
	doneOld := assign(20)
	doneNew := assign(21)
	doneNew.Status = model.MoodleSubmitted
	s.announceDeadlineMoves(ctx, 1, []model.MoodleItem{doneOld}, []model.MoodleItem{doneNew})
	// Тест, который ещё не открылся: о сроке скажет «Скоро тест», тут — тишина.
	quizOld := watchQuiz(900, at(14, 45, 0), at(15, 10, 0), "")
	quizNew := watchQuiz(900, at(14, 45, 0), at(15, 40, 0), "")
	s.announceDeadlineMoves(ctx, 1, []model.MoodleItem{quizOld}, []model.MoodleItem{quizNew})
	if len(*sent) != 1 {
		t.Fatalf("лишние уведомления о сроке: %v", *sent)
	}
}

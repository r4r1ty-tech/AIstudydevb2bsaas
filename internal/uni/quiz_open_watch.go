package uni

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// Наблюдатель открытия тестов.
//
// Обычный синк Moodle идёт раз в 30 минут, а тест может быть открыт всего 25 минут,
// поэтому «Открылся тест» мог прийти поздно или не прийти. Наблюдатель раз в quizWatchTick
// смотрит сохранённые тесты всех аккаунтов Moodle и делает синк аккаунта:
//   - за quizWatchLead до открытия — один раз, чтобы подхватить сдвиг времени открытия;
//   - от Opens до min(Opens+quizWatchSpan, Due) — проверки по слотам длиной quizWatchSlot:
//     номер слота считается от времени (now-Opens)/quizWatchSlot, а не по счётчику попыток.
//     Слот считается выполненным только после успешного синка, поэтому сбой (429, таймаут,
//     перелогин) не сжигает слот: следующий тик повторит синк. Проверки идут, пока Moodle
//     не покажет тест открытым (метка quizwatch_seen).
//
// Синк перечитывает тест (withReread), а detectAndHandleOpenedQuizzes шлёт «Открылся тест»
// и ставит решение в очередь. Напоминание за quizRemindLead — в schedule_notify.go, отдельно
// от синков, чтобы медленная отправка в Telegram не задерживала тик.
//
// Метки в settings (ключи quizwatch_*:<tg>:<cmid>:<opens unix>) не дают повторить выполненное
// действие, в том числе после перезапуска процесса. Аккаунт синхронизируется одним потоком:
// тот же tryLockAccount, что у обычного обхода. Занятый аккаунт пробуется на следующем тике.

var (
	quizWatchTick = 20 * time.Second
	quizWatchLead = time.Minute
	// quizWatchSlot — длина слота проверки; quizWatchSpan — проверки идут не дольше этого от Opens.
	quizWatchSlot = 40 * time.Second
	quizWatchSpan = 10 * time.Minute
)

// quizOpenNow — тест открыт для попыток по данным Moodle на момент now: страница не пишет
// «не открыт для попыток», время открытия прошло и срок ещё впереди.
func quizOpenNow(it model.MoodleItem, now time.Time) bool {
	return !strings.Contains(it.Todo, model.QuizNotOpenNote) &&
		(it.Opens == nil || !it.Opens.After(now)) &&
		(it.Due == nil || it.Due.After(now))
}

// quizWatchAction — одно действие наблюдателя над тестом.
type quizWatchAction struct {
	item model.MoodleItem
	base string // tg:cmid:opens — общий префикс ключей действия
	pre  bool   // синк за минуту до открытия; иначе — проверка слота slot
	slot int    // номер слота от времени открытия (с нуля)
}

// rereadKey — ключ контекста: какие cmid перечитать при инкрементальном синке.
type rereadKey struct{}

// withReread кладёт в контекст набор cmid, которые синк обязан перечитать, даже если
// обычная лента их не отметила (наблюдатель знает, что тест должен открыться).
func withReread(ctx context.Context, cmids map[int64]bool) context.Context {
	return context.WithValue(ctx, rereadKey{}, cmids)
}

func rereadSet(ctx context.Context) map[int64]bool {
	m, _ := ctx.Value(rereadKey{}).(map[int64]bool)
	return m
}

// RunQuizOpenWatch — фоновый цикл наблюдателя (запускается из cmd/rasp рядом с Run).
func (s *Syncer) RunQuizOpenWatch(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	t := time.NewTicker(quizWatchTick)
	defer t.Stop()
	for {
		s.quizWatchTick(ctx, &wg)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// quizWatchTick — один проход наблюдателя по всем аккаунтам Moodle. Синки и напоминания
// уходят в горутины, wg ждёт их завершения.
func (s *Syncer) quizWatchTick(ctx context.Context, wg *sync.WaitGroup) {
	if s.Store == nil {
		return
	}
	accs, err := s.Store.ListAccounts(model.AccountMoodle)
	if err != nil {
		logx.Warnf("uni", "quiz-watch: аккаунты Moodle: %v", err)
		return
	}
	now := s.now()
	type reminder struct {
		tg    int64
		items []model.MoodleItem
	}
	var reminders []reminder
	for _, acc := range accs {
		if ctx.Err() != nil {
			return
		}
		if acc.Status == model.AccountBadLogin {
			continue
		}
		items, err := s.Store.ListMoodleItems(acc.TelegramID)
		if err != nil {
			logx.Warnf("uni", "quiz-watch tg=%d: элементы: %v", acc.TelegramID, err)
			continue
		}
		reminders = append(reminders, reminder{tg: acc.TelegramID, items: items})
		plan := s.planQuizWatch(acc.TelegramID, items, now)
		if len(plan) == 0 {
			continue
		}
		// Аккаунт занят обычным синком или прошлым наблюдением: проверим на следующем тике.
		if !s.tryLockAccount(acc.TelegramID) {
			continue
		}
		reread := make(map[int64]bool, len(plan))
		for _, a := range plan {
			reread[a.item.CMID] = true
		}
		wg.Add(1)
		go func(acc model.Account, plan []quizWatchAction) {
			defer wg.Done()
			defer s.unlockAccount(acc.TelegramID)
			s.runQuizWatch(ctx, acc, plan, reread)
		}(acc, plan)
	}
	if len(reminders) > 0 {
		// Отправка напоминаний в Telegram может быть медленной: тик её не ждёт.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, r := range reminders {
				if ctx.Err() != nil {
					return
				}
				s.remindUpcomingQuizzes(ctx, r.tg, r.items, now)
			}
		}()
	}
}

// planQuizWatch — что наблюдателю нужно сделать с тестами аккаунта сейчас.
// Берутся только тесты не сданные, с временем открытия и сроком в будущем.
func (s *Syncer) planQuizWatch(tg int64, items []model.MoodleItem, now time.Time) []quizWatchAction {
	var plan []quizWatchAction
	for _, it := range items {
		if it.Kind != model.MoodleQuiz || it.Finished() || it.Opens == nil || it.Due == nil || !it.Due.After(now) {
			continue
		}
		opens := *it.Opens
		base := fmt.Sprintf("%d:%d:%d", tg, it.CMID, opens.Unix())
		end := opens.Add(quizWatchSpan)
		if it.Due.Before(end) {
			end = *it.Due
		}
		switch {
		case now.Before(opens):
			if now.Before(opens.Add(-quizWatchLead)) || s.settingSet(quizWatchPreKey(base)) {
				continue
			}
			plan = append(plan, quizWatchAction{item: it, base: base, pre: true})
		case now.Before(end):
			if s.settingSet(quizWatchSeenKey(base)) {
				continue
			}
			slot := int(now.Sub(opens) / quizWatchSlot)
			if slot <= s.watchSlotDone(base) {
				continue
			}
			plan = append(plan, quizWatchAction{item: it, base: base, slot: slot})
		}
	}
	return plan
}

func quizWatchPreKey(base string) string  { return "quizwatch_pre:" + base }
func quizWatchSlotKey(base string) string { return "quizwatch_slot:" + base }
func quizWatchSeenKey(base string) string { return "quizwatch_seen:" + base }

// runQuizWatch делает один синк аккаунта по плану. Метки действий ставятся только после
// успешного синка: при ошибке ничего не отмечено и следующий тик повторит проверку.
func (s *Syncer) runQuizWatch(ctx context.Context, acc model.Account, plan []quizWatchAction, reread map[int64]bool) {
	for _, a := range plan {
		if a.pre {
			logx.Infof("uni", "quiz-watch tg=%d cmid=%d %q: синк за минуту до открытия (открытие %s)",
				acc.TelegramID, a.item.CMID, a.item.Title, fmtAt(a.item.Opens, s.loc()))
			continue
		}
		logx.Infof("uni", "quiz-watch tg=%d cmid=%d %q: проверка открытия, слот %d (открытие %s)",
			acc.TelegramID, a.item.CMID, a.item.Title, a.slot, fmtAt(a.item.Opens, s.loc()))
	}
	ctx = withTrigger(ctx, "наблюдатель за открытием тестов")
	ctx = withReread(ctx, reread)
	if err := s.watchPacing(ctx); err != nil {
		return
	}
	if err := s.syncWatched(ctx, acc); err != nil {
		if ctx.Err() == nil {
			logx.Warnf("uni", "quiz-watch tg=%d: синк не удался, повтор на следующем тике: %v", acc.TelegramID, err)
		}
		return
	}
	items, err := s.Store.ListMoodleItems(acc.TelegramID)
	if err != nil {
		logx.Warnf("uni", "quiz-watch tg=%d: элементы после синка: %v", acc.TelegramID, err)
		return
	}
	byCM := make(map[int64]model.MoodleItem, len(items))
	for _, it := range items {
		byCM[it.CMID] = it
	}
	now := s.now()
	for _, a := range plan {
		if a.pre {
			s.markWatch(quizWatchPreKey(a.base), "1")
			continue
		}
		s.markWatch(quizWatchSlotKey(a.base), strconv.Itoa(a.slot))
		it, ok := byCM[a.item.CMID]
		if ok && quizOpenNow(it, now) {
			s.markWatch(quizWatchSeenKey(a.base), "1")
			logx.Infof("uni", "quiz-watch tg=%d cmid=%d: тест открыт по данным Moodle, слот %d", acc.TelegramID, a.item.CMID, a.slot)
			continue
		}
		logx.Infof("uni", "quiz-watch tg=%d cmid=%d: Moodle ещё показывает тест закрытым, слот %d", acc.TelegramID, a.item.CMID, a.slot)
	}
}

// markWatch пишет метку наблюдателя; ошибку записи пишет в журнал.
func (s *Syncer) markWatch(key, val string) {
	if err := s.Store.SetSetting(key, val); err != nil {
		logx.Warnf("uni", "quiz-watch: метка %s не записана: %v", key, err)
	}
}

// syncWatched — синк аккаунта для наблюдателя: тестовая подмена или обычный SyncMoodle (инкрементальный).
func (s *Syncer) syncWatched(ctx context.Context, acc model.Account) error {
	if s.watchSync != nil {
		return s.watchSync(ctx, acc)
	}
	return s.SyncMoodle(ctx, acc, false)
}

// watchPacing — темп синков наблюдателя: не чаще QUIZ_MOODLE_RPS в секунду на процесс.
// Вход по сохранённой сессии не требуется; когда сессия протухла, вход идёт обычным путём синка.
func (s *Syncer) watchPacing(ctx context.Context) error {
	s.mu.Lock()
	if s.watchPace == nil {
		s.watchPace = newMoodlePacer(QuizMoodleRPS())
	}
	p := s.watchPace
	s.mu.Unlock()
	return p.wait(ctx)
}

// settingSet — ключ в settings уже записан (метка действия или уведомления).
func (s *Syncer) settingSet(key string) bool {
	v, ok, err := s.Store.GetSetting(key)
	return err == nil && ok && v != ""
}

// watchSlotDone — последний выполненный слот проверки открытия; -1, если проверок ещё не было.
func (s *Syncer) watchSlotDone(base string) int {
	v, ok, err := s.Store.GetSetting(quizWatchSlotKey(base))
	if err != nil || !ok || v == "" {
		return -1
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}
	return n
}

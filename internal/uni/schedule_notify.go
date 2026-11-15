package uni

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// Уведомления о расписании Moodle: «Скоро тест», напоминание за 15 минут, «Срок изменился».
// Каждое уходит один раз на своё сочетание полей (ключи в settings), а за один синк одного
// типа — не больше scheduleNotifyCap, как «Новое в Moodle».

// quizRemindLead — за сколько до открытия напомнить о тесте.
const quizRemindLead = 15 * time.Minute

// scheduleNotifyCap — максимум сообщений одного типа за один синк.
const scheduleNotifyCap = 5

// announceUpcomingQuizzes — «Скоро тест»: тест появился с временем открытия в будущем, или у него
// сменились открытие или срок. Раз на (tg, cmid, открытие, срок). Тесты, которые уже были в базе
// без изменений, не объявляем: после обновления бот не рассылает все предстоящие тесты разом.
func (s *Syncer) announceUpcomingQuizzes(ctx context.Context, tg int64, prev, cur []model.MoodleItem) {
	prevBy := indexMoodle(prev)
	now := s.now()
	n := 0
	for _, it := range cur {
		if it.Kind != model.MoodleQuiz || it.Finished() || it.Opens == nil || !it.Opens.After(now) {
			continue
		}
		if old, ok := prevBy[it.CMID]; ok && sameTime(old.Opens, it.Opens) && sameTime(old.Due, it.Due) {
			continue
		}
		key := fmt.Sprintf("quiz_soon:%d:%d:%d:%d", tg, it.CMID, it.Opens.Unix(), unixOr0(it.Due))
		if s.settingSet(key) {
			continue
		}
		if n >= scheduleNotifyCap {
			break
		}
		n++
		s.markSetting(key)
		if it.Opens.Sub(now) <= quizRemindLead {
			// Открытие уже в пределах напоминания: время и так в этом сообщении, второе не шлём.
			s.markSetting(quizRemindKey(tg, it))
		}
		s.notify(ctx, tg, quizSoonText(it, s.loc(), s.hasQuizAI(tg)))
	}
}

// remindUpcomingQuizzes — напоминание за quizRemindLead до открытия: «Через 15 минут откроется тест».
// Раз на (tg, cmid, открытие). Вызывается наблюдателем на каждом тике.
func (s *Syncer) remindUpcomingQuizzes(ctx context.Context, tg int64, items []model.MoodleItem, now time.Time) {
	for _, it := range items {
		if it.Kind != model.MoodleQuiz || it.Finished() || it.Opens == nil || (it.Due != nil && !it.Due.After(now)) {
			continue
		}
		opens := *it.Opens
		if !now.Before(opens) || now.Before(opens.Add(-quizRemindLead)) {
			continue
		}
		key := quizRemindKey(tg, it)
		if s.settingSet(key) {
			continue
		}
		s.markSetting(key)
		s.notify(ctx, tg, "⏳ <b>Через 15 минут откроется тест</b>\n"+FormatItem(it, now, s.loc()))
	}
}

// announceDeadlineMoves — «Срок изменился»: у незаконченного задания или теста сдвинулся срок.
// Раз на (tg, cmid, новый срок). Тест, который ещё не открылся, здесь не объявляем:
// он уже попал в «Скоро тест» с новым сроком.
func (s *Syncer) announceDeadlineMoves(ctx context.Context, tg int64, prev, cur []model.MoodleItem) {
	prevBy := indexMoodle(prev)
	loc := s.loc()
	now := s.now()
	n := 0
	for _, it := range cur {
		if it.Finished() || it.Due == nil || !it.Due.After(now) {
			continue
		}
		old, ok := prevBy[it.CMID]
		if !ok || old.Finished() || old.Due == nil || sameTime(old.Due, it.Due) {
			continue
		}
		if it.Kind == model.MoodleQuiz && it.Opens != nil && it.Opens.After(now) {
			continue
		}
		key := fmt.Sprintf("due_moved:%d:%d:%d", tg, it.CMID, it.Due.Unix())
		if s.settingSet(key) {
			continue
		}
		if n >= scheduleNotifyCap {
			break
		}
		n++
		s.markSetting(key)
		s.notify(ctx, tg, "📅 <b>Срок изменился</b>\nбыло "+fmtAt(old.Due, loc)+" → стало "+fmtAt(it.Due, loc)+"\n"+FormatItem(it, now, loc))
	}
}

// quizSoonText — текст «Скоро тест»: название, курс, окно открытия и что сделает бот.
func quizSoonText(it model.MoodleItem, loc *time.Location, hasAI bool) string {
	var b strings.Builder
	b.WriteString("🗓 <b>Скоро тест</b>\n")
	b.WriteString("<b>" + html.EscapeString(quizTitle(it)) + "</b>\n")
	if it.Course != "" {
		b.WriteString(html.EscapeString(it.Course) + "\n")
	}
	b.WriteString("откроется " + it.Opens.In(loc).Format("02.01 в 15:04"))
	if it.Due != nil {
		b.WriteString(", закроется " + it.Due.In(loc).Format("02.01 в 15:04"))
		if d := it.Due.Sub(*it.Opens); d > 0 {
			b.WriteString(" (окно " + windowText(d) + ")")
		}
	}
	b.WriteString("\n")
	if hasAI {
		b.WriteString("Бот решит сам сразу после открытия.")
	} else {
		b.WriteString("Ключа ИИ нет — пройди сам или добавь ключ в панели.")
	}
	if it.URL != "" {
		b.WriteString("\n<a href=\"" + html.EscapeString(it.URL) + "\">Открыть в Moodle</a>")
	}
	return b.String()
}

// hasQuizAI — у аккаунта есть хоть один ключ ИИ для решения тестов (та же цепочка, что у решателя).
func (s *Syncer) hasQuizAI(tg int64) bool {
	cfg := s.getCfg()
	if cfg == nil || s.Store == nil {
		return false
	}
	return len(access.QuizChainWithVault(cfg, s.Store, s.Vault, tg)) > 0
}

// markSetting ставит метку уведомления в settings; ошибку записи пишет в журнал.
func (s *Syncer) markSetting(key string) {
	if err := s.Store.SetSetting(key, "1"); err != nil {
		logx.Warnf("uni", "метка %s не записана: %v", key, err)
	}
}

func quizRemindKey(tg int64, it model.MoodleItem) string {
	return fmt.Sprintf("quiz_remind:%d:%d:%d", tg, it.CMID, it.Opens.Unix())
}

func quizTitle(it model.MoodleItem) string {
	if strings.TrimSpace(it.Title) == "" {
		return fmt.Sprintf("тест %d", it.CMID)
	}
	return it.Title
}

// windowText — длительность окна открытия: «25 мин», «1 ч», «1 ч 30 мин».
func windowText(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	if m < 60 {
		return fmt.Sprintf("%d мин", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d ч", m/60)
	}
	return fmt.Sprintf("%d ч %d мин", m/60, m%60)
}

func indexMoodle(items []model.MoodleItem) map[int64]model.MoodleItem {
	m := make(map[int64]model.MoodleItem, len(items))
	for _, it := range items {
		m[it.CMID] = it
	}
	return m
}

// sameTime сравнивает моменты с точностью до секунды; nil равен только nil.
func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Unix() == b.Unix()
}

func unixOr0(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}

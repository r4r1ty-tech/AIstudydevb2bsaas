package uni

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/quizlog"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websearch"
)

const (
	quizPageWorkers      = 4
	quizTimeReserve      = 75 * time.Second
	quizFastModeTimeLeft = 3 * time.Minute
	quizWaveBudget       = 45 * time.Second
)

var (
	timeLeftColonRe = regexp.MustCompile(`\b(\d+):(\d{1,2})(?::(\d{1,2}))?\b`)
	timeLeftUnitRe  = regexp.MustCompile(`(\d+)\s*([a-zа-яё]+)`)
)

// parseTimeLeft разбирает остаток времени из таймера Moodle (#quiz-time-left / #quiz-timer).
func parseTimeLeft(raw string) (time.Duration, bool) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return 0, false
	}
	if m := timeLeftColonRe.FindStringSubmatch(s); len(m) == 4 {
		a, err1 := strconv.Atoi(m[1])
		b, err2 := strconv.Atoi(m[2])
		if err1 == nil && err2 == nil {
			if m[3] != "" {
				sec, err3 := strconv.Atoi(m[3])
				if err3 == nil && b < 60 && sec < 60 {
					return time.Duration(a)*time.Hour + time.Duration(b)*time.Minute + time.Duration(sec)*time.Second, true
				}
			} else if b < 60 {
				return time.Duration(a)*time.Minute + time.Duration(b)*time.Second, true
			}
		}
	}
	var total time.Duration
	matched := false
	for _, m := range timeLeftUnitRe.FindAllStringSubmatch(s, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		unit := m[2]
		switch {
		case unit == "д" || unit == "d" || strings.HasPrefix(unit, "дн") || strings.HasPrefix(unit, "ден") || strings.HasPrefix(unit, "day"):
			total += time.Duration(n) * 24 * time.Hour
			matched = true
		case unit == "ч" || unit == "h" || strings.HasPrefix(unit, "час") || strings.HasPrefix(unit, "hr") || strings.HasPrefix(unit, "hour"):
			total += time.Duration(n) * time.Hour
			matched = true
		case unit == "м" || unit == "m" || strings.HasPrefix(unit, "мин") || strings.HasPrefix(unit, "min"):
			total += time.Duration(n) * time.Minute
			matched = true
		case unit == "с" || unit == "s" || strings.HasPrefix(unit, "сек") || strings.HasPrefix(unit, "sec"):
			total += time.Duration(n) * time.Second
			matched = true
		}
	}
	if !matched {
		return 0, false
	}
	return total, true
}

func (s *Syncer) getCfg() *config.Config {
	if s.Cfg != nil {
		return s.Cfg
	}
	cfg, err := config.Load()
	if err == nil {
		return cfg
	}
	return nil
}

// detectAndHandleOpenedQuizzes проверяет, какие тесты стали открытыми для попыток,
// уведомляет пользователя и ставит парсинг или авторешение через LLM в пул решения (scheduleQuiz).
// Сам не решает: возвращается сразу, синк аккаунта не ждёт решения.
func (s *Syncer) detectAndHandleOpenedQuizzes(ctx context.Context, acc model.Account, c *moodle.Client, prev, cur []model.MoodleItem) {
	wakeKicked := isWakeKicked(ctx)
	if !wakeKicked && s.Store != nil {
		wakeKickVal, ok, _ := s.Store.GetSetting(WakeKickKey)
		wakeKicked = ok && wakeKickVal != ""
	}

	// «Пройти» из панели: решаем сейчас, даже если тест просрочен или
	// по нашим эвристикам «не открыт» — пусть Moodle сам скажет, можно ли.
	forced := s.takeForced(acc.TelegramID, prev, cur)
	for _, it := range forced {
		// «Пройти» — ручная попытка: сбрасывает счётчик неудач.
		s.resetQuizFailures(acc.TelegramID, it.CMID)
		queued, err := s.dispatchQuiz(ctx, acc, c, it, true)
		switch {
		case err != nil:
			s.notify(ctx, acc.TelegramID, "<b>Тест не поставлен в очередь решения</b>\n<b>Тест:</b> "+
				html.EscapeString(it.Title)+"\nНажми «Пройти» ещё раз чуть позже.")
		case !queued:
			s.notify(ctx, acc.TelegramID, "<b>Тест уже решается или стоит в очереди</b>\n<b>Тест:</b> "+
				html.EscapeString(it.Title)+"\nДождись отчёта.")
		}
	}

	// При первой инициализации аккаунта (когда в базе еще нет элементов и нет wake-кика)
	// уведомления не рассылаются, чтобы не спамить всей историей курсов.
	if len(prev) == 0 && !wakeKicked {
		return
	}
	done := map[int64]bool{}
	for _, it := range forced {
		done[it.CMID] = true
	}

	prevMap := make(map[int64]model.MoodleItem, len(prev))
	for _, p := range prev {
		prevMap[p.CMID] = p
	}

	now := s.now()

	for _, it := range cur {
		if it.Kind != model.MoodleQuiz || it.Finished() || done[it.CMID] {
			continue
		}

		if !quizOpenNow(it, now) {
			continue
		}

		p, existed := prevMap[it.CMID]
		wasClosed := !existed || strings.Contains(p.Todo, "не открыт для попыток") || (p.Opens != nil && p.Opens.After(now))
		// Прошлая попытка решения упала (ИИ не ответил) — пробуем снова на
		// каждом обходе, без повторного «Открылся тест».
		failedBefore := false
		if v, ok, _ := s.Store.GetSetting(quizFailKey(acc.TelegramID, it.CMID)); ok && v != "" {
			failedBefore = true
		}
		if wasClosed || wakeKicked {
			s.notify(ctx, acc.TelegramID, "<b>Открылся тест в Moodle!</b>\n"+FormatItem(it, now, s.loc()))
		}
		if wasClosed || wakeKicked || failedBefore {
			// Ошибка постановки (например, база занята) не теряет тест: метка «ожидает повтора»
			// заставит следующий синк поставить его снова (failedBefore), без нового уведомления.
			if _, err := s.dispatchQuiz(ctx, acc, c, it, false); err != nil {
				logx.Warnf("uni", "quiz tg=%d cmid=%d: постановка не удалась, повтор на следующем синке: %v",
					acc.TelegramID, it.CMID, err)
				s.markQuizPending(acc.TelegramID, it.CMID)
			}
		}
	}
}

// quizMaxAttempts — сколько неудачных попыток решения подряд допускается без ручного «Пройти».
// Дальше авто-постановка не идёт, пользователю приходит одно сообщение.
const quizMaxAttempts = 6

// scheduleQuiz ставит решение теста в фоновый пул (семафор MOODLE_QUIZ_WORKERS).
// Единственная защита от двойного решения — блокировка (tg, cmid): берётся здесь и снимается
// по окончании решения. Если тест уже решается или ждёт в очереди — пропуск с логом.
// Авто-постановка (не forced) не идёт, пока тест не исчерпал quizMaxAttempts неудач.
// Возвращает false при пропуске.
func (s *Syncer) scheduleQuiz(ctx context.Context, acc model.Account, c *moodle.Client, it model.MoodleItem, forced bool) bool {
	qid := strconv.FormatInt(it.CMID, 10)
	if !forced && s.quizFailCount(acc.TelegramID, it.CMID) >= quizMaxAttempts {
		logx.Infof("uni", "quiz tg=%d cmid=%d: %d неудачных попыток подряд, авто-решение не ставлю, ждём «Пройти»",
			acc.TelegramID, it.CMID, quizMaxAttempts)
		return false
	}
	if !s.tryLockQuiz(acc.TelegramID, qid) {
		logx.Infof("uni", "quiz tg=%d cmid=%d: тест уже решается или ждёт в очереди, пропуск", acc.TelegramID, it.CMID)
		return false
	}
	// Решение идёт на своём клиенте и сессии: общий *moodle.Client синка в фоне не используется.
	// Сессию передаём зашифрованной (как в базе), чтобы не входить по паролю без нужды.
	sess := acc
	if s.Vault != nil {
		if raw := c.Session(); raw != "" {
			if sealed, err := s.Vault.Seal(raw); err == nil {
				sess.Session = sealed
			}
		}
	}
	sem := s.getQuizSem()
	logx.Infof("uni", "quiz tg=%d cmid=%d %q: в очереди решения, forced=%v", acc.TelegramID, it.CMID, it.Title, forced)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.unlockQuiz(acc.TelegramID, qid)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// Остановка до старта — не неудача решения: счётчик не растёт, повтор на следующем синке.
			s.markQuizPending(acc.TelegramID, it.CMID)
			logx.Infof("uni", "quiz tg=%d cmid=%d: остановка до начала решения, повтор на следующем синке", acc.TelegramID, it.CMID)
			return
		}
		defer func() { <-sem }()
		// Последний defer: паника в решении не роняет процесс, тест помечается неудачей и повторится.
		defer func() {
			if r := recover(); r != nil {
				logx.Errorf("uni", "quiz tg=%d cmid=%d: паника в решении: %v\n%s", acc.TelegramID, it.CMID, r, debug.Stack())
				s.markQuizRetry(ctx, acc.TelegramID, it)
			}
		}()
		s.solveQuiz(ctx, sess, it, forced)
	}()
	return true
}

// solveQuiz решает один тест в фоне: вход в Moodle на своём клиенте (withSession), затем processQuizErr.
// Протухшая сессия (ErrUnauthorized) уходит из fn в withSession: он перелогинивается и вызывает fn снова.
// Если тест решён — аккаунту ставится пинок на синк, чтобы статусы в базе обновились.
func (s *Syncer) solveQuiz(ctx context.Context, acc model.Account, it model.MoodleItem, forced bool) {
	c, err := s.moodleClient()
	if err != nil {
		logx.Warnf("uni", "quiz tg=%d cmid=%d: клиент Moodle: %v", acc.TelegramID, it.CMID, err)
		s.markQuizRetry(ctx, acc.TelegramID, it)
		return
	}
	solved := false
	// Воркер очереди входит через общий ограничитель темпа; в inline-режиме клиент обычный.
	var auth authClient = c
	if s.quizPacer != nil {
		auth = pacedMoodle{Client: c, pace: s.paceMoodle}
	}
	err = s.withSession(ctx, acc, auth, moodle.ErrBadLogin, moodle.ErrUnauthorized, func() error {
		var authErr error
		solved, authErr = s.processQuizErr(ctx, acc, c, it, forced)
		return authErr
	})
	if err != nil {
		if ctx.Err() != nil {
			s.markQuizPending(acc.TelegramID, it.CMID)
			return
		}
		logx.Warnf("uni", "quiz tg=%d cmid=%d: вход в Moodle для решения не удался: %v", acc.TelegramID, it.CMID, err)
		s.markQuizRetry(ctx, acc.TelegramID, it)
		if forced {
			title := it.Title
			if title == "" {
				title = fmt.Sprintf("тест %d", it.CMID)
			}
			s.notify(ctx, acc.TelegramID, fmt.Sprintf("<b>⚠️ Не получилось пройти тест</b>\n<b>Тест:</b> %s\n<b>Почему:</b> вход в Moodle не удался",
				html.EscapeString(title)))
		}
		return
	}
	if solved && ctx.Err() == nil {
		// Пинок на полный синк: статусы теста в базе обновятся сразу, а не через 30 минут.
		// Повторного решения это не вызовет: processQuizErr выходит по quiz_solved.
		if err := Kick(s.Store, acc.TelegramID); err != nil {
			logx.Warnf("uni", "quiz tg=%d cmid=%d: пинок после решения: %v", acc.TelegramID, it.CMID, err)
		}
	}
}

// quizFailCount — сколько неудачных попыток решения подряд записано в quiz_fail.
func (s *Syncer) quizFailCount(tg, cmid int64) int {
	n := 0
	if v, ok, _ := s.Store.GetSetting(quizFailKey(tg, cmid)); ok {
		_, _ = fmt.Sscan(v, &n)
	}
	return n
}

// markQuizRetry — неудачная попытка решения: увеличивает счётчик quiz_fail (он же ставит повтор
// на следующем синке) и возвращает число неудач. На quizMaxAttempts шлёт пользователю одно сообщение.
func (s *Syncer) markQuizRetry(ctx context.Context, tg int64, it model.MoodleItem) int {
	n := s.quizFailCount(tg, it.CMID) + 1
	_ = s.Store.SetSetting(quizFailKey(tg, it.CMID), strconv.Itoa(n))
	if n == quizMaxAttempts {
		title := it.Title
		if title == "" {
			title = fmt.Sprintf("тест %d", it.CMID)
		}
		s.notify(ctx, tg, fmt.Sprintf("<b>Не смог решить тест за %d попыток</b>\n<b>Тест:</b> %s\nНажми „Пройти“ в панели, чтобы попробовать ещё раз.",
			quizMaxAttempts, html.EscapeString(title)))
	}
	return n
}

// markQuizPending — тест нужно решить снова, но неудачи решения не было (остановка, очередь).
// Счётчик не растёт; флаг повтора ставится, если его нет, — следующий синк снова поставит тест.
func (s *Syncer) markQuizPending(tg, cmid int64) {
	if v, ok, _ := s.Store.GetSetting(quizFailKey(tg, cmid)); !ok || v == "" {
		_ = s.Store.SetSetting(quizFailKey(tg, cmid), "0")
	}
}

// resetQuizFailures — ручной «Пройти» сбрасывает счётчик неудач.
func (s *Syncer) resetQuizFailures(tg, cmid int64) {
	_ = s.Store.SetSetting(quizFailKey(tg, cmid), "")
}

// QuizForceKey — «пройти принудительно» (кнопка «Пройти» в панели).
func QuizForceKey(telegramID, cmid int64) string {
	return fmt.Sprintf("quiz_force:%d:%d", telegramID, cmid)
}

// takeForced — тесты с флагом «Пройти» (флаг снимается). Ищем только среди
// текущих и прошлых элементов: флаг на cmid, которого там нет, ждёт, пока тест
// появится в Moodle-списке.
func (s *Syncer) takeForced(id int64, prev, cur []model.MoodleItem) []model.MoodleItem {
	byID := map[int64]model.MoodleItem{}
	for _, it := range prev {
		byID[it.CMID] = it
	}
	for _, it := range cur {
		byID[it.CMID] = it
	}
	var out []model.MoodleItem
	for cmid, it := range byID {
		v, ok, err := s.Store.GetSetting(QuizForceKey(id, cmid))
		if err != nil || !ok || v == "" {
			continue
		}
		_ = s.Store.SetSetting(QuizForceKey(id, cmid), "")
		if it.Kind == "" {
			it.Kind = model.MoodleQuiz
		}
		out = append(out, it)
	}
	return out
}

// processQuiz обрабатывает тест: парсит вопросы, собирает материалы курса
// и при наличии ключа LLM решает тест и отправляет отчёт, либо присылает текст вопросов.
// forced — «Пройти» из панели: почему не вышло, человеку сообщаем всегда.
// processQuizErr — то же, что processQuiz, но возвращает ErrUnauthorized (сессия протухла при
// открытии теста или старте попытки), не отмечая неудачу и не сообщая пользователю: решение
// перезапустит solveQuiz после входа. Блокировку (tg, cmid) не берёт: её держит scheduleQuiz.
// solved — true, только если попытка завершена (FinishAttempt) и тест помечен решённым.
func (s *Syncer) processQuizErr(ctx context.Context, acc model.Account, c *moodle.Client, it model.MoodleItem, forced bool) (solved bool, authErr error) {
	tag := quizTag{TG: acc.TelegramID, CMID: it.CMID, Page: quizNoPage}

	logx.Infof("uni", "quiz %s: вход: курс=%q название=%q forced=%v", tag, it.Course, it.Title, forced)
	solvedKey := fmt.Sprintf("quiz_solved:%d:%d", acc.TelegramID, it.CMID)
	if v, ok, err := s.Store.GetSetting(solvedKey); err == nil && ok && v != "" {
		logx.Infof("uni", "quiz %s: выход: тест уже решён ранее (%s), пропуск", tag, v)
		return
	}
	refuse := func(why string) {
		if !forced {
			return
		}
		title := it.Title
		if title == "" {
			title = fmt.Sprintf("тест %d", it.CMID)
		}
		s.notify(ctx, acc.TelegramID, fmt.Sprintf("<b>⚠️ Не получилось пройти тест</b>\n<b>Тест:</b> %s\n<b>Почему:</b> %s",
			html.EscapeString(title), html.EscapeString(why)))
	}

	// Временные сбои (страница теста, старт попытки) отмечаем в quiz_fail,
	// чтобы следующий обход повторил тихо; закрытый тест — не сбой.
	markRetry := func() { s.markQuizRetry(ctx, acc.TelegramID, it) }

	info, err := c.InspectQuiz(ctx, it.CMID)
	if err != nil {
		if errors.Is(err, moodle.ErrUnauthorized) {
			return false, err
		}
		markRetry()
		logx.Infof("uni", "quiz %s: выход: ошибка — страница теста не открылась: %v", tag, err)
		refuse("не открылась страница теста: " + err.Error())
		return
	}
	if !info.CanStart && info.InProgressAttemptID == 0 {
		// Тест закрыт — повторять нечего.
		_ = s.Store.SetSetting(quizFailKey(acc.TelegramID, it.CMID), "")
		logx.Infof("uni", "quiz %s: выход: тест закрыт для попыток (can_start=false, незавершённой попытки нет)", tag)
		refuse("Moodle не даёт начать попытку (тест закрыт или попытки кончились)")
		return
	}

	// Ключ ИИ и открытая попытка решают, стартовать ли новую попытку: без ключа новую не начинаем.
	cfg := s.getCfg()
	var chain []llm.Target
	if cfg != nil {
		// Тесты решаем из общего пула: ключи .env + расшаренные личные ключи
		// + ключ админа. Если пул пуст, остаётся личная цепочка пользователя.
		chain = access.QuizPoolChain(cfg)
		if len(chain) == 0 {
			chain = access.QuizChainWithVault(cfg, s.Store, s.Vault, acc.TelegramID)
		}
	}
	hasLLM := len(chain) > 0
	open := info.InProgressAttemptID > 0
	if !hasLLM && !open {
		known := false
		if forced {
			n, nerr := s.Store.CountVerifiedForCMID(it.CMID)
			known = nerr == nil && n > 0
		}
		if !forced || !known {
			logx.Infof("uni", "quiz %s: выход: ключа ИИ нет и открытой попытки нет — новую не начинаю", tag)
			s.noKeyNotice(ctx, acc.TelegramID, it, forced)
			return
		}
		logx.Infof("uni", "quiz %s: ключа ИИ нет, но «Пройти» и в базе есть ответы по тесту — начинаю попытку", tag)
	}

	// Старт попытки — через общий ограничитель темпа (только у воркера очереди).
	if perr := s.paceMoodle(ctx); perr != nil {
		// Остановка на ограничителе: попытка не начата, это не неудача решения.
		return false, nil
	}
	attempt, err := c.StartOrResumeAttempt(ctx, it.CMID)
	if err != nil {
		if errors.Is(err, moodle.ErrUnauthorized) {
			return false, err
		}
		markRetry()
		logx.Infof("uni", "quiz %s: выход: ошибка — попытка не началась: %v", tag, err)
		refuse("попытка не началась: " + err.Error())
		return
	}
	attemptStarted := time.Now()

	pages := []*moodle.AttemptPage{attempt}
	cur := attempt
	var pageErr error
	sequentialNav := false
	seenPages := map[int]bool{cur.Page: true}
	for cur.NextPage > 0 {
		next, err := c.GetAttemptPage(ctx, it.CMID, cur.AttemptID, cur.NextPage)
		if err != nil {
			logx.Warnf("uni", "quiz %s: страница попытки %d: %v", tag, cur.NextPage, err)
			pageErr = fmt.Errorf("не загрузилась страница %d попытки: %w", cur.NextPage+1, err)
			break
		}
		if next.Page != cur.NextPage || next.Page < 0 || seenPages[next.Page] {
			logx.Infof("uni", "quiz %s: последовательная навигация (ожидалась страница %d, пришла %d)", tag, cur.NextPage, next.Page)
			sequentialNav = true
			break
		}
		seenPages[next.Page] = true
		pages = append(pages, next)
		cur = next
	}

	run := &quizlog.Run{At: s.now(), TelegramID: acc.TelegramID, CMID: it.CMID, Course: it.Course, Title: it.Title}
	var chainNames []string
	for _, t := range chain {
		run.Chain = append(run.Chain, t.String())
		chainNames = append(chainNames, t.String())
	}
	questions := 0
	for _, p := range pages {
		questions += len(p.Questions)
	}
	logx.Infof("uni", "quiz %s: страниц %d (sequential=%v), вопросов %d, цепочка ИИ (%d): [%s], ошибка загрузки страниц: %v",
		tag, len(pages), sequentialNav, questions, len(chain), strings.Join(chainNames, " > "), pageErr)
	started := time.Now()
	saveRun := func() {
		run.TookMS = time.Since(started).Milliseconds()
		if cfg == nil || cfg.DBPath == "" {
			return
		}
		if err := quizlog.Write(quizlog.Dir(cfg.DBPath), run); err != nil {
			logx.Warnf("uni", "quiz %s: журнал: %v", tag, err)
		}
	}

	// Не все страницы попытки загрузились — не сдаём частично.
	if pageErr != nil {
		run.Status = quizlog.Failed
		run.Error = pageErr.Error()
		saveRun()
		logx.Infof("uni", "quiz %s: выход: ошибка — %v", tag, pageErr)
		s.reportQuizFailure(ctx, acc.TelegramID, it, pageErr, false)
		return
	}

	// Ключа ИИ нет, но все вопросы теста найдены в базе проверенных ответов — решаем без ИИ.
	baseOnly := !hasLLM && s.baseCoversPages(pages)
	if baseOnly {
		logx.Infof("uni", "quiz %s: ключа ИИ нет, все вопросы теста в базе проверенных ответов — решаю из базы", tag)
	}
	if !hasLLM && !baseOnly {
		// Ключ ИИ не задан: отправляем полное содержание теста пользователю
		parsedKey := fmt.Sprintf("quiz_parsed:%d:%d", acc.TelegramID, it.CMID)
		if v, ok, err := s.Store.GetSetting(parsedKey); err == nil && ok && v != "" {
			logx.Infof("uni", "quiz %s: выход: ИИ не настроен, текст теста уже отправлялся (%s), пропуск", tag, v)
			return // уже отправляли текст вопросов, не спамим
		}

		var b strings.Builder
		fmt.Fprintf(&b, "<b>Открыт тест: %s</b>\n", html.EscapeString(it.Title))
		fmt.Fprintf(&b, "<b>Курс:</b> %s\n", html.EscapeString(it.Course))
		if !open {
			b.WriteString("<b>Внимание:</b> попытка уже открыта в Moodle, ответы не отправлены — закончи её сам.\n")
		}
		b.WriteString("\n")
		b.WriteString("<b>Содержание теста:</b>\n")
		for _, p := range pages {
			for _, q := range p.Questions {
				fmt.Fprintf(&b, "\n<b>Вопрос %d</b> (%s):\n%s\n", q.Slot, q.QType, html.EscapeString(q.Text))
				if q.Prompt != "" {
					fmt.Fprintf(&b, "<i>%s</i>\n", html.EscapeString(q.Prompt))
				}
				if len(q.Choices) > 0 {
					for _, ch := range q.Choices {
						fmt.Fprintf(&b, "- [%s] %s\n", ch.Value, html.EscapeString(ch.Text))
					}
				}
				if len(q.MatchOptions) > 0 {
					for _, mo := range q.MatchOptions {
						fmt.Fprintf(&b, "- %s -> [выбор из вариантов]\n", html.EscapeString(mo.Question))
					}
				}
			}
		}
		if it.URL != "" {
			fmt.Fprintf(&b, "\n<a href=\"%s\">Открыть тест в Moodle</a>", html.EscapeString(it.URL))
		}
		s.notify(ctx, acc.TelegramID, b.String())
		if err := s.Store.SetSetting(parsedKey, s.now().Format(time.RFC3339)); err != nil {
			logx.Warnf("uni", "quiz %s: сохранить отметку «текст отправлен»: %v", tag, err)
		}
		logx.Infof("uni", "quiz %s: выход: ИИ не настроен, отправлено содержание теста (вопросов %d)", tag, questions)
		run.Status = quizlog.Parsed
		saveRun()
		return
	}

	// Бюджет времени по таймеру Moodle: оставляем резерв quizTimeReserve на сохранение ответов.
	timeLeft, hasTimeLeft := parseTimeLeft(attempt.TimeLeft)
	var deadline time.Time
	initialFast := false
	if hasTimeLeft {
		if timeLeft <= quizFastModeTimeLeft {
			initialFast = true
		}
		if timeLeft > quizTimeReserve {
			deadline = attemptStarted.Add(timeLeft - quizTimeReserve)
		} else {
			initialFast = true
		}
		logx.Infof("uni", "quiz %s: таймер %q (%s), быстрый_режим=%v, резерв=%s",
			tag, attempt.TimeLeft, timeLeft, initialFast, quizTimeReserve)
	}
	solveCtx := ctx
	var solveCancel context.CancelFunc = func() {}
	if !deadline.IsZero() {
		solveCtx, solveCancel = context.WithDeadline(ctx, deadline)
	}
	defer solveCancel()

	// Авторешение теста через LLM. В свободной навигации страницы решаются параллельно
	// и отправляются в Moodle по возрастанию индекса сразу по мере готовности, чтобы
	// при сбое или нехватке времени уже решённый прогресс не пропал.
	materials := s.collectCourseMaterials(tag, it.Course)
	if materials == "" {
		// Локальных конспектов нет (запись сорвалась/старая пара) — читаем
		// текст курса и страниц-лекций прямо из Moodle.
		materials = s.collectMoodleCourseText(ctx, tag, c, it.CourseID)
	}
	ws := s.webSearcher()

	var (
		allAnswers map[int]moodle.QuizAnswer
		usedModels map[string]bool
		sentCount  int
		runErr     error
	)
	if sequentialNav {
		pages, allAnswers, usedModels, sentCount, runErr = s.solveAndSubmitSeqNav(
			ctx, solveCtx, tag, c, ws, chain, it.Course, materials, attempt, initialFast, deadline, run,
		)
	} else {
		allAnswers, usedModels, sentCount, runErr = s.solveAndSubmitFreeNav(
			ctx, solveCtx, tag, c, ws, chain, it.Course, materials, pages, initialFast, deadline, run,
		)
	}

	if runErr != nil {
		run.Status = quizlog.Failed
		run.Error = runErr.Error()
		saveRun()
		logx.Infof("uni", "quiz %s: выход: ошибка — %v", tag, runErr)
		if ctx.Err() != nil {
			// Прервано остановкой, а не сбоем решения: без уведомления и без счётчика, повтор на следующем синке.
			s.markQuizPending(acc.TelegramID, it.CMID)
			return false, nil
		}
		if sentCount > 0 {
			// Часть страниц уже в Moodle, попытка не завершена: причину берёт runQuizJob для last_error задачи.
			_ = s.Store.SetSetting(quizPartialKey(acc.TelegramID, it.CMID),
				fmt.Sprintf("отправлено страниц %d из %d", sentCount, len(pages)))
		}
		s.reportQuizFailure(ctx, acc.TelegramID, it, runErr, sentCount > 0)
		return
	}

	// FinishAttempt — только если все страницы решены и отправлены (runErr == nil).
	// Пропущенная страница (см. solveAndSubmitFreeNav) даёт runErr: попытка не завершается,
	// тест не помечается решённым, а quiz_fail ставит повтор на следующем синке.
	result, err := c.FinishAttempt(ctx, it.CMID, attempt.AttemptID)
	if err != nil {
		logx.Warnf("uni", "quiz %s: завершение попытки: %v", tag, err)
		run.Status = quizlog.Failed
		run.Error = "завершение попытки: " + err.Error()
		saveRun()
		logx.Infof("uni", "quiz %s: выход: ошибка — попытку не удалось завершить: %v", tag, err)
		s.reportQuizFailure(ctx, acc.TelegramID, it, fmt.Errorf("ответы сохранены, но попытку не удалось завершить: %w", err), true)
		return
	}
	newRight, newWrong := recordVerifiedAnswers(ctx, s.Store, c, it.CMID, attempt.AttemptID, acc.TelegramID, it.Course)
	learned := newRight+newWrong > 0
	rin := newQuizReportInput(it, pages, allAnswers, run.Pages, result, usedModels, materials != "", !hasLLM)

	// Полная оценка «X из Y» — тест решён. Оценка не разобрана — как раньше, решён.
	n, d, graded := moodle.GradeFraction(result.Grade)
	if !graded || n >= d {
		_ = s.Store.SetSetting(quizFailKey(acc.TelegramID, it.CMID), "")
		run.Status = quizlog.Solved
		run.Grade, run.Feedback = result.Grade, result.Feedback
		saveRun()
		if err := s.Store.SetSetting(solvedKey, s.now().Format(time.RFC3339)); err != nil {
			logx.Warnf("uni", "quiz %s: сохранить отметку «решён»: %v", tag, err)
		}
		logx.Infof("uni", "quiz %s: выход: успех — оценка=%q, модели=%v, за %s",
			tag, result.Grade, usedModelList(usedModels), time.Since(started).Round(time.Millisecond))
		rin.Solved = true
		s.notify(ctx, acc.TelegramID, buildQuizReport(rin))
		return true, nil
	}

	// Оценка неполная: попытка завершена, тест НЕ помечается решённым. Повтор новой попыткой —
	// только если попытки остались и (метод «Высшая оценка» или ноль баллов). Иначе — «Пройти».
	run.Status = quizlog.Partial
	run.Grade, run.Feedback = result.Grade, result.Feedback
	saveRun()
	info, ierr := c.InspectQuiz(ctx, it.CMID)
	if errors.Is(ierr, moodle.ErrUnauthorized) {
		return false, err
	}
	if ierr != nil {
		logx.Warnf("uni", "quiz %s: попытки и метод оценивания не прочитаны: %v", tag, ierr)
		info = nil
	}
	fails := s.quizFailCount(acc.TelegramID, it.CMID)
	retry, note := retryDecision(info, n, learned, fails)
	if retry && fails >= quizMaxAttempts {
		retry = false
		note = fmt.Sprintf("автоповторов подряд уже было %d, дальше — по «Пройти»", quizMaxAttempts)
	}
	rin.Retry = retry
	rin.Note = note
	rin.setAttempts(info)
	logx.Infof("uni", "quiz %s: выход: оценка неполная (%q), повтор=%t, попыток осталось=%d, метод=%q, причина=%q",
		tag, result.Grade, retry, rin.AttemptsLeft, rin.Method, note)

	if retry {
		s.markQuizRetry(ctx, acc.TelegramID, it)
		s.notify(ctx, acc.TelegramID, buildQuizReport(rin))
		return s.processQuizErr(ctx, acc, c, it, forced)
	}
	_ = s.Store.SetSetting(quizFailKey(acc.TelegramID, it.CMID), "")
	s.notify(ctx, acc.TelegramID, buildQuizReport(rin))
	return false, nil
}

// processQuiz — processQuizErr без ошибки сессии: для вызовов, которые не перезапускают вход (тесты).
func (s *Syncer) processQuiz(ctx context.Context, acc model.Account, c *moodle.Client, it model.MoodleItem, forced bool) bool {
	solved, _ := s.processQuizErr(ctx, acc, c, it, forced)
	return solved
}

type pageSolveResult struct {
	idx     int
	answers map[int]moodle.QuizAnswer
	usedKey string
	pl      quizlog.Page
	err     error
}

func pageOptsForWave(initialFast bool, deadline time.Time, remPages, workers int) solvePageOpts {
	fast := initialFast
	budget := time.Duration(0)
	if !deadline.IsZero() {
		left := time.Until(deadline)
		if left < 0 {
			left = 0
		}
		if workers < 1 {
			workers = 1
		}
		if remPages < 1 {
			remPages = 1
		}
		wavesLeft := (remPages + workers - 1) / workers
		// В одной волне страницы решаются параллельно, поэтому бюджет
		// считается на волну, а не на каждую страницу отдельно.
		if left <= 0 {
			budget = -1 // дедлайн уже прошёл: время на ИИ не тратим
		} else {
			budget = left / time.Duration(wavesLeft)
		}
		if !fast && (left < quizFastModeRemaining || left < time.Duration(wavesLeft)*quizWaveBudget) {
			fast = true
		}
	}
	return solvePageOpts{
		FastMode: fast,
		Deadline: deadline,
		Budget:   budget,
	}
}

func (s *Syncer) solveSinglePage(ctx context.Context, tag quizTag, idx int, p *moodle.AttemptPage, c *moodle.Client, ws *websearch.Searcher, chain []llm.Target, course, materials string, opts solvePageOpts) pageSolveResult {
	ptag := tag
	ptag.Page = p.Page
	answers, res, pl, err := s.solvePageWithOpts(ctx, ptag, c, ws, chain, course, materials, p.Questions, opts)
	pl.Page = p.Page
	var usedKey string
	if res.Used != nil {
		pl.Used = res.Used.String()
		usedKey = res.Used.Provider + "/" + res.Used.Model
	}
	if err == nil && len(p.Questions) > 0 && len(answers) == 0 {
		err = fmt.Errorf("ИИ не дал ни одного ответа")
	}
	if err != nil {
		pl.Error = err.Error()
		logx.Warnf("uni", "quiz %s: решение страницы: %v", ptag, err)
	}
	logx.Debugf("uni", "quiz %s: страница решена: ответов %d из %d, модель %s, ошибка=%v",
		ptag, len(answers), len(p.Questions), pl.Used, err)
	pl.Answers = map[int]string{}
	for _, q := range p.Questions {
		if a, ok := answers[q.Slot]; ok {
			pl.Answers[q.Slot] = formatAnswerText(q, a)
		}
	}
	return pageSolveResult{
		idx:     idx,
		answers: answers,
		usedKey: usedKey,
		pl:      pl,
		err:     err,
	}
}

// solveSinglePageSafe — solveSinglePage с перехватом паники, для горутины страницы и для
// последовательной навигации. Паника решения одной страницы не роняет процесс: результат с ошибкой
// всё равно возвращается и уходит в resultsCh, иначе основной цикл отправки зависнет.
func (s *Syncer) solveSinglePageSafe(ctx context.Context, tag quizTag, idx int, p *moodle.AttemptPage, c *moodle.Client, ws *websearch.Searcher, chain []llm.Target, course, materials string, opts solvePageOpts) (res pageSolveResult) {
	defer func() {
		if r := recover(); r != nil {
			ptag := tag
			ptag.Page = p.Page
			logx.Errorf("uni", "quiz %s: паника при решении страницы: %v\n%s", ptag, r, debug.Stack())
			res = pageSolveResult{
				idx: idx,
				pl:  quizlog.Page{Page: p.Page, Error: fmt.Sprint(r)},
				err: fmt.Errorf("паника при решении страницы: %v", r),
			}
		}
	}()
	if s.solveHook != nil {
		s.solveHook(idx)
	}
	return s.solveSinglePage(ctx, tag, idx, p, c, ws, chain, course, materials, opts)
}

func submitPageAnswers(ctx context.Context, tag quizTag, c *moodle.Client, page *moodle.AttemptPage, answers map[int]moodle.QuizAnswer) (*moodle.AttemptPage, error) {
	var checkSlots []int
	for _, q := range page.Questions {
		if q.HasSubmitButton {
			checkSlots = append(checkSlots, q.Slot)
		}
	}
	for _, slot := range checkSlots {
		updated, err := c.SubmitAnswers(ctx, page, answers, slot)
		if err != nil {
			logx.Warnf("uni", "quiz %s: проверка вопроса slot=%d: %v", tag, slot, err)
			return nil, fmt.Errorf("проверка вопроса %d: %w", slot, err)
		}
		if updated != nil {
			page = updated
		}
	}
	updated, err := c.SubmitAnswers(ctx, page, answers, 0)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func collectRunPages(run *quizlog.Run, results []*pageSolveResult) {
	for _, r := range results {
		if r != nil {
			run.Pages = append(run.Pages, r.pl)
		}
	}
}

// solveAndSubmitFreeNav решает страницы свободной навигации параллельно (не больше quizParallelism)
// и отправляет ответы в Moodle строго по возрастанию индекса, по мере готовности.
//
// Ошибка одной страницы не останавливает остальные: страница, которую ИИ не решил или чьи ответы
// Moodle не принял, пропускается — остаётся без ответа, причина пишется в журнал и в run.Pages,
// а следующие страницы решаются и отправляются как обычно.
// Если пропущена хотя бы одна страница, возвращается ошибка. Тогда processQuiz не вызывает
// FinishAttempt: попытка остаётся открытой, Moodle закроет её по таймеру сам, тест не помечается
// решённым, а quiz_fail ставит повтор на следующем синке (при повторе страницы решаются заново).
// sent — сколько страниц успешно отправлено в Moodle.
func (s *Syncer) solveAndSubmitFreeNav(
	ctx, solveCtx context.Context,
	tag quizTag,
	c *moodle.Client,
	ws *websearch.Searcher,
	chain []llm.Target,
	course, materials string,
	pages []*moodle.AttemptPage,
	initialFast bool,
	deadline time.Time,
	run *quizlog.Run,
) (map[int]moodle.QuizAnswer, map[string]bool, int, error) {
	allAnswers := make(map[int]moodle.QuizAnswer)
	usedModels := make(map[string]bool)
	n := len(pages)
	if n == 0 {
		return allAnswers, usedModels, 0, nil
	}

	workers := quizParallelism(chain, n)
	jobs := make(chan int, n)
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)

	// Буфер на все страницы: горутины решения не ждут отправку в Moodle.
	resultsCh := make(chan pageSolveResult, n)
	var remaining atomic.Int64
	remaining.Store(int64(n))

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if solveCtx.Err() != nil {
					resultsCh <- pageSolveResult{
						idx: idx,
						pl:  quizlog.Page{Page: pages[idx].Page, Error: solveCtx.Err().Error()},
						err: solveCtx.Err(),
					}
					continue
				}
				rem := int(remaining.Add(-1)) + 1
				opts := pageOptsForWave(initialFast, deadline, rem, workers)
				resultsCh <- s.solveSinglePageSafe(solveCtx, tag, idx, pages[idx], c, ws, chain, course, materials, opts)
			}
		}()
	}

	// results[i] — результат страницы с индексом i; в порядке возрастания отправляем готовые.
	results := make([]*pageSolveResult, n)
	var skipped []int // номера страниц (с 1), которые не решены или не сохранены
	var firstErr error
	sent := 0
	for next := 0; next < n; next++ {
		for results[next] == nil {
			r := <-resultsCh
			cp := r
			results[r.idx] = &cp
		}
		r := results[next]
		if r.usedKey != "" {
			usedModels[r.usedKey] = true
		}
		for slot, ans := range r.answers {
			allAnswers[slot] = ans
		}

		ptag := tag
		ptag.Page = pages[next].Page
		if r.err != nil {
			logx.Warnf("uni", "quiz %s: страница %d пропущена, ответ не отправлен, остальные идут дальше: %v", ptag, next+1, r.err)
			skipped = append(skipped, next+1)
			if firstErr == nil {
				firstErr = fmt.Errorf("страница %d не решена: %w", next+1, r.err)
			}
			continue
		}
		if _, subErr := submitPageAnswers(ctx, ptag, c, pages[next], r.answers); subErr != nil {
			logx.Warnf("uni", "quiz %s: страница %d не сохранилась в Moodle, пропущена, остальные идут дальше: %v", ptag, next+1, subErr)
			r.pl.Error = "ответы не сохранились в Moodle: " + subErr.Error()
			skipped = append(skipped, next+1)
			if firstErr == nil {
				firstErr = fmt.Errorf("ответы страницы %d не сохранились в Moodle: %w", next+1, subErr)
			}
			continue
		}
		sent++
		logx.Debugf("uni", "quiz %s: ответы страницы %d отправлены в Moodle", tag, pages[next].Page)
	}

	wg.Wait()
	collectRunPages(run, results)
	if len(skipped) > 0 {
		return allAnswers, usedModels, sent, fmt.Errorf("пропущены страницы %v, попытку не завершаю: %w", skipped, firstErr)
	}
	return allAnswers, usedModels, sent, nil
}

// quizParallelism — сколько страниц одновременно решается: не больше quizPageWorkers и не больше
// llm.MaxConcurrentPerKey запросов на каждый уникальный ключ цепочки. Ключи различаем по SecretHash,
// один и тот же ключ в нескольких моделях цепочки считается один раз.
func quizParallelism(chain []llm.Target, pages int) int {
	keys := make(map[[32]byte]bool, len(chain))
	for _, t := range chain {
		if h, ok := t.SecretHash(); ok {
			keys[h] = true
		}
	}
	nKeys := max(len(keys), 1)
	return max(1, min(quizPageWorkers, pages, llm.MaxConcurrentPerKey*nKeys))
}

func (s *Syncer) solveAndSubmitSeqNav(
	ctx, solveCtx context.Context,
	tag quizTag,
	c *moodle.Client,
	ws *websearch.Searcher,
	chain []llm.Target,
	course, materials string,
	firstPage *moodle.AttemptPage,
	initialFast bool,
	deadline time.Time,
	run *quizlog.Run,
) ([]*moodle.AttemptPage, map[int]moodle.QuizAnswer, map[string]bool, int, error) {
	var pages []*moodle.AttemptPage
	allAnswers := make(map[int]moodle.QuizAnswer)
	usedModels := make(map[string]bool)
	seen := make(map[int]bool)

	cur := firstPage
	sentCount := 0
	for idx := 0; ; idx++ {
		if seen[cur.Page] {
			return pages, allAnswers, usedModels, sentCount, fmt.Errorf("повторная страница %d при последовательной навигации", cur.Page)
		}
		seen[cur.Page] = true
		pages = append(pages, cur)

		opts := pageOptsForWave(initialFast, deadline, 1, 1)
		if !deadline.IsZero() && (opts.Budget == 0 || opts.Budget > quizSeqPageBudget) {
			// При последовательной навигации следующая страница идёт только
			// после этой, поэтому одну страницу нельзя отдавать на весь остаток.
			opts.Budget = quizSeqPageBudget
		}
		r := s.solveSinglePageSafe(solveCtx, tag, idx, cur, c, ws, chain, course, materials, opts)
		run.Pages = append(run.Pages, r.pl)
		if r.usedKey != "" {
			usedModels[r.usedKey] = true
		}
		for slot, ans := range r.answers {
			allAnswers[slot] = ans
		}
		if r.err != nil {
			return pages, allAnswers, usedModels, sentCount, r.err
		}

		ptag := tag
		ptag.Page = cur.Page
		updated, subErr := submitPageAnswers(ctx, ptag, c, cur, r.answers)
		if subErr != nil {
			logx.Warnf("uni", "quiz %s: сохранение ответов страницы: %v", ptag, subErr)
			return pages, allAnswers, usedModels, sentCount, fmt.Errorf("ответы страницы %d не сохранились в Moodle: %w", idx+1, subErr)
		}
		sentCount++
		logx.Debugf("uni", "quiz %s: ответы страницы %d отправлены в Moodle", tag, cur.Page)

		if updated == nil || updated.Page < 0 || cur.NextPage < 0 {
			break
		}
		cur = updated
	}
	return pages, allAnswers, usedModels, sentCount, nil
}

func (s *Syncer) collectCourseMaterials(tag quizTag, course string) string {
	recRoot := config.DefaultRecordings
	if cfg := s.getCfg(); cfg != nil && cfg.RecordingsDir != "" {
		recRoot = cfg.RecordingsDir
	}

	const maxTotalChars = 32000
	var b strings.Builder
	var used []string
	written := 0
	seen := map[string]bool{}
	appendFile := func(path, label string) int {
		if seen[path] || written >= maxTotalChars {
			return 0
		}
		//nolint:gosec // путь из контролируемого каталога записей
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return 0
		}
		seen[path] = true
		header := fmt.Sprintf("\n--- %s (%s) ---\n", label, filepath.Dir(path))
		headerRunes := utf8.RuneCountInString(header)
		remain := maxTotalChars - written - headerRunes - 1
		if remain <= 0 {
			return 0
		}
		runes := []rune(string(data))
		if len(runes) > remain {
			runes = runes[:remain]
		}
		b.WriteString(header)
		b.WriteString(string(runes))
		b.WriteString("\n")
		written += headerRunes + len(runes) + 1
		return len(runes)
	}

	if s.Store != nil {
		packs, err := s.Store.ListPacks()
		if err != nil {
			logx.Warnf("uni", "quiz %s: список лекций: %v", tag, err)
		}
		for _, p := range packs {
			if !matchDiscipline(p.Discipline, course) {
				logx.Debugf("uni", "quiz %s: материалы: пакет id=%d «%s» №%d не относится к курсу %q, пропуск", tag, p.ID, p.Discipline, p.Number, course)
				continue
			}
			notesRunes, transRunes := 0, 0
			if p.Dir != "" {
				packDir := p.Dir
				if !filepath.IsAbs(packDir) {
					packDir = filepath.Join(recRoot, packDir)
				}
				notesRunes = appendFile(filepath.Join(packDir, "notes.md"), fmt.Sprintf("Конспект лекции %s №%d", p.Discipline, p.Number))
				transRunes = appendFile(filepath.Join(packDir, "transcript.txt"), fmt.Sprintf("Транскрипт лекции %s №%d", p.Discipline, p.Number))
			}
			logx.Debugf("uni", "quiz %s: материалы: пакет id=%d «%s» №%d: конспект %d симв., транскрипт %d симв.",
				tag, p.ID, p.Discipline, p.Number, notesRunes, transRunes)
			if notesRunes+transRunes > 0 {
				used = append(used, fmt.Sprintf("%s №%d", p.Discipline, p.Number))
			}
			if written >= maxTotalChars {
				break
			}
		}
	}

	// Даже если пакета нет в БД (запись сорвалась, старая папка, ручной конспект),
	// читаем notes.md/transcript.txt из каталога записей по названию дисциплины.
	if written < maxTotalChars && recRoot != "" {
		_ = filepath.WalkDir(recRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if written >= maxTotalChars {
				return fs.SkipAll
			}
			if d.IsDir() {
				rel, _ := filepath.Rel(recRoot, path)
				if rel != "." && strings.Count(rel, string(os.PathSeparator)) > 4 {
					return fs.SkipDir
				}
				return nil
			}
			name := d.Name()
			if name != "notes.md" && name != "transcript.txt" {
				return nil
			}
			if !pathMatchesCourse(path, course) {
				return nil
			}
			label := "Конспект лекции (файл)"
			if name == "transcript.txt" {
				label = "Транскрипт лекции (файл)"
			}
			if n := appendFile(path, label); n > 0 {
				used = append(used, filepath.Dir(path))
			}
			return nil
		})
	}

	res := b.String()
	if runes := []rune(res); len(runes) > maxTotalChars {
		res = string(runes[:maxTotalChars])
	}
	if len(used) == 0 {
		logx.Infof("uni", "quiz %s: материалы курса: 0 симв. — подходящих конспектов/транскриптов не нашлось", tag)
	} else {
		logx.Infof("uni", "quiz %s: материалы курса: %d симв. из [%s]", tag, utf8.RuneCountInString(res), strings.Join(used, ", "))
	}
	return res
}

// collectMoodleCourseText — лёгкий фолбэк, когда локальных конспектов нет:
// читает текст секций курса и до трёх страниц-лекций (modtype_page) из Moodle.
// Ошибки не валят решение теста — просто остаёмся без этого источника.
func (s *Syncer) collectMoodleCourseText(ctx context.Context, tag quizTag, c *moodle.Client, courseID int64) string {
	if c == nil || courseID == 0 {
		return ""
	}
	doc, _, err := c.PageWithURL(ctx, fmt.Sprintf("/course/view.php?id=%d", courseID))
	if err != nil {
		logx.Debugf("uni", "quiz %s: материалы Moodle: курс %d не открылся: %v", tag, courseID, err)
		return ""
	}
	const maxChars = 16000
	var b strings.Builder
	seen := map[string]bool{}
	add := func(label, text string) bool {
		text = strings.TrimSpace(text)
		if text == "" || seen[text] {
			return true
		}
		seen[text] = true
		remaining := maxChars - utf8.RuneCountInString(b.String())
		if remaining <= 0 {
			return false
		}
		if r := []rune(text); len(r) > remaining {
			text = string(r[:remaining]) + "\n…"
		}
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", label, text)
		return utf8.RuneCountInString(b.String()) < maxChars
	}
	doc.Find("li.section, li.course-section").EachWithBreak(func(_ int, sec *goquery.Selection) bool {
		title := cleanStr(sec.Find(".sectionname, h3").First().Text())
		if title == "" {
			title = "секция курса"
		}
		sec.Find("script, style").Remove()
		return add("Курс, "+title, sec.Text())
	})
	// Страницы-лекции: берём первые три, чтобы не раздувать промпт.
	n := 0
	doc.Find("li.activity.modtype_page a.activityname, li.activity.modtype_page a.activityinstance").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		if n >= 3 || utf8.RuneCountInString(b.String()) >= maxChars {
			return false
		}
		href := strings.TrimSpace(a.AttrOr("href", ""))
		if href == "" {
			return true
		}
		n++
		page, _, err := c.PageWithURL(ctx, href)
		if err != nil {
			logx.Debugf("uni", "quiz %s: материалы Moodle: страница %s: %v", tag, href, err)
			return true
		}
		page.Find("script, style, nav, header, footer").Remove()
		text := cleanStr(page.Find("#region-main, .course-content").First().Text())
		add("Лекция Moodle: "+cleanStr(a.Text()), text)
		return true
	})
	res := strings.TrimSpace(b.String())
	if runes := []rune(res); len(runes) > maxChars {
		res = string(runes[:maxChars])
	}
	if res != "" {
		logx.Infof("uni", "quiz %s: материалы Moodle: %d симв.", tag, utf8.RuneCountInString(res))
	}
	return res
}

// pathMatchesCourse — грубая проверка, что путь записи относится к дисциплине:
// два значимых слова из названия курса или название целиком.
func pathMatchesCourse(path, course string) bool {
	p := strings.ToLower(path)
	c := strings.ToLower(cleanStr(course))
	if c == "" {
		return false
	}
	if strings.Contains(p, c) {
		return true
	}
	words := strings.FieldsFunc(c, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	matched := 0
	for _, w := range words {
		if len([]rune(w)) >= 4 && strings.Contains(p, w) {
			matched++
		}
	}
	return matched >= 2
}

// usedModelList — модели, которые отвечали на тест, по алфавиту (для лога).
func usedModelList(m map[string]bool) string {
	var ms []string
	for k := range m {
		ms = append(ms, k)
	}
	sort.Strings(ms)
	return strings.Join(ms, ", ")
}

func matchDiscipline(discipline, course string) bool {
	d := strings.ToLower(cleanStr(discipline))
	c := strings.ToLower(cleanStr(course))
	if d == "" || c == "" {
		return false
	}
	if strings.Contains(c, d) || strings.Contains(d, c) {
		return true
	}
	words := strings.FieldsFunc(d, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	matched := 0
	for _, w := range words {
		if len([]rune(w)) >= 4 && strings.Contains(c, w) {
			matched++
		}
	}
	return matched >= 2
}

func cleanStr(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func formatAnswerText(q moodle.QuizQuestion, ans moodle.QuizAnswer) string {
	switch {
	case q.QType == "shortanswer" || q.QType == "numerical":
		if ans.Text != "" {
			return ans.Text
		}
		return "(пусто)"
	case len(q.Choices) > 0 && q.Choices[0].IsMulti:
		var labels []string
		for _, ch := range q.Choices {
			for _, selected := range ans.Choices {
				if selected == ch.Value || selected == ch.Name || selected == ch.Text {
					labels = append(labels, ch.Text)
					break
				}
			}
		}
		if len(labels) > 0 {
			return strings.Join(labels, ", ")
		}
		return "(не выбрано)"
	case len(q.Choices) > 0 && !q.Choices[0].IsMulti:
		for _, ch := range q.Choices {
			if ch.Value == ans.Choice || ch.Name == ans.Choice || ch.Text == ans.Choice {
				return ch.Text
			}
		}
		if ans.Choice != "" {
			return ans.Choice
		}
		return "(не выбрано)"
	case len(q.MatchOptions) > 0:
		var pairs []string
		for _, mo := range q.MatchOptions {
			if val, ok := ans.Matches[mo.Name]; ok {
				pairs = append(pairs, fmt.Sprintf("%s -> %s", mo.Question, val))
			}
		}
		if len(pairs) > 0 {
			return strings.Join(pairs, "; ")
		}
		return "(не сопоставлено)"
	}
	if ans.Text != "" {
		return ans.Text
	}
	return "(ответ сохранен)"
}

func quizFailKey(id, cmid int64) string { return fmt.Sprintf("quiz_fail:%d:%d", id, cmid) }

// reportQuizFailure — «проблема с тестом» на первой неудаче. Счётчик неудач растёт в markQuizRetry;
// на quizMaxAttempts пользователь получает сообщение о лимите. sent — часть ответов уже ушла в Moodle
// (тогда текст «ничего не отправлял» неверен).
func (s *Syncer) reportQuizFailure(ctx context.Context, id int64, it model.MoodleItem, err error, sent bool) {
	if s.markQuizRetry(ctx, id, it) != 1 {
		return
	}
	var b strings.Builder
	b.WriteString("<b>⚠️ Проблема с тестом</b>\n")
	fmt.Fprintf(&b, "<b>Тест:</b> %s (%s)\n", html.EscapeString(it.Title), html.EscapeString(it.Course))
	fmt.Fprintf(&b, "<b>Что случилось:</b> %s\n", html.EscapeString(shortErr(err)))
	if sent {
		b.WriteString("Часть ответов уже сохранена в Moodle, попытку не завершал. Попробую ещё раз при следующем обновлении Moodle.")
	} else {
		b.WriteString("Попытку не завершал, пустые ответы не отправлял. Попробую ещё раз при следующем обновлении Moodle.")
	}
	if it.URL != "" {
		fmt.Fprintf(&b, "\n<a href=\"%s\">Открыть тест в Moodle</a>", html.EscapeString(it.URL))
	}
	s.notify(ctx, id, b.String())
}

// quizUnlimitedRetries — сколько автоповторов подряд допускается, если попыток не ограничено.
const quizUnlimitedRetries = 3

// retryDecision решает, запускать ли новую попытку после неполной оценки n баллов (n — числитель).
// Повтор — только если появилось новое знание в базе (learned), попытки остались и:
//   - ноль баллов: метод «Высшая оценка» или «Последняя попытка»;
//   - ненулевая оценка: только «Высшая оценка» (при «Последней» и «Первой»/«Средней»
//     новая попытка может понизить оценку).
//
// При неограниченном числе попыток автоповторов не больше quizUnlimitedRetries подряд (fails —
// текущий счётчик quiz_fail). info == nil — попытки не прочитаны, повтора нет.
func retryDecision(info *moodle.QuizInfo, n float64, learned bool, fails int) (bool, string) {
	if info == nil {
		return false, "не удалось прочитать попытки на странице теста — проверь в Moodle"
	}
	if info.AttemptsLeft() == 0 {
		return false, "попыток не осталось"
	}
	if !learned {
		return false, "в базе нет нового знания по этому тесту — повтор бессмысленен"
	}
	if info.Allowed == 0 && fails >= quizUnlimitedRetries {
		return false, fmt.Sprintf("попытки не ограничены, автоповторов %d подряд уже было", quizUnlimitedRetries)
	}
	method := strings.ToLower(info.GradingMethod)
	highest := strings.Contains(method, "высш")
	last := strings.Contains(method, "последн")
	if n == 0 {
		if highest || last {
			return true, ""
		}
		return false, fmt.Sprintf("метод оценивания «%s»: при нуле баллов повтор не запускаю", info.GradingMethod)
	}
	if highest {
		return true, ""
	}
	return false, fmt.Sprintf("метод оценивания «%s»: повтор не запускаю, новая попытка может понизить оценку", info.GradingMethod)
}

// noKeyNotice — тест открыт, а решать некому: нет ключа ИИ и студент не начал попытку сам.
// Без «Пройти» пишем один раз по тесту, чтобы синк не спамил.
func (s *Syncer) noKeyNotice(ctx context.Context, tg int64, it model.MoodleItem, forced bool) {
	key := fmt.Sprintf("quiz_nokey:%d:%d", tg, it.CMID)
	if !forced {
		if v, ok, _ := s.Store.GetSetting(key); ok && v != "" {
			return
		}
		_ = s.Store.SetSetting(key, s.now().Format(time.RFC3339))
	}
	title := it.Title
	if title == "" {
		title = fmt.Sprintf("тест %d", it.CMID)
	}
	s.notify(ctx, tg, fmt.Sprintf("<b>Открыт тест, решать некому</b>\n<b>Тест:</b> %s\nНужен ключ ИИ в панели или пройди тест сам.",
		html.EscapeString(title)))
}

// quizReportQuestion — строка отчёта по вопросу.
type quizReportQuestion struct {
	Slot   int
	Kind   string
	Text   string
	Answer string
	Mark   string // «верно», «неверно», пусто — отметки нет
}

// quizReportInput — всё, что показывает отчёт о попытке решения.
type quizReportInput struct {
	Title, Course, URL string
	Grade              string
	Method             string
	Solved             bool
	Retry              bool
	Note               string
	// AttemptsLeft: -1 — без ограничений, -2 — не прочитано, иначе сколько осталось.
	AttemptsLeft  int
	Models        []string
	FromBaseNoKey bool
	Confidence    []float64
	Materials     bool
	Search        string
	FromBase      int
	ByAI          int
	Questions     []quizReportQuestion
}

// quizReportMaxRunes — предел текста отчёта; длинный список вопросов обрезается с пометкой.
const quizReportMaxRunes = 3500

// newQuizReportInput собирает отчёт из результатов попытки и страниц журнала.
func newQuizReportInput(it model.MoodleItem, pages []*moodle.AttemptPage, answers map[int]moodle.QuizAnswer,
	runPages []quizlog.Page, result *moodle.QuizResult, usedModels map[string]bool, materials, noKey bool) quizReportInput {
	in := quizReportInput{
		Title:         it.Title,
		Course:        it.Course,
		URL:           it.URL,
		AttemptsLeft:  -2,
		FromBaseNoKey: noKey,
		Materials:     materials,
	}
	if result != nil {
		in.Grade = result.Grade
	}
	for m := range usedModels {
		in.Models = append(in.Models, m)
	}
	sort.Strings(in.Models)

	searchState := "нет"
	for _, rp := range runPages {
		in.FromBase += len(rp.FromBase)
		for _, c := range rp.Confidence {
			in.Confidence = append(in.Confidence, c)
		}
		for _, sr := range rp.Search {
			switch {
			case strings.Contains(sr.Error, "капча"):
				searchState = "капча"
			case sr.Hits > 0 && searchState != "капча":
				searchState = "работал"
			}
		}
	}
	in.Search = searchState

	marks := map[int]string{}
	if result != nil {
		for _, qr := range result.Questions {
			st := strings.ToLower(qr.Status)
			switch {
			case strings.Contains(st, "неверн"), strings.Contains(st, "частично"):
				marks[qr.Slot] = "неверно"
			case strings.Contains(st, "верн"):
				marks[qr.Slot] = "верно"
			}
		}
	}
	total := 0
	for _, p := range pages {
		for _, q := range p.Questions {
			total++
			in.Questions = append(in.Questions, quizReportQuestion{
				Slot:   q.Slot,
				Kind:   quizKindLabel(q),
				Text:   q.Text,
				Answer: formatAnswerText(q, answers[q.Slot]),
				Mark:   marks[q.Slot],
			})
		}
	}
	in.ByAI = total - in.FromBase
	if in.ByAI < 0 {
		in.ByAI = 0
	}
	return in
}

// setAttempts переносит число оставшихся попыток и метод из разбора view.php.
func (in *quizReportInput) setAttempts(info *moodle.QuizInfo) {
	if info == nil {
		in.AttemptsLeft = -2
		return
	}
	in.AttemptsLeft = info.AttemptsLeft()
	in.Method = info.GradingMethod
}

// quizKindLabel — тип вопроса по-русски для отчёта.
func quizKindLabel(q moodle.QuizQuestion) string {
	switch {
	case q.QType == "shortanswer" || q.QType == "numerical":
		return "короткий ответ"
	case len(q.MatchOptions) > 0:
		return "соответствие"
	case len(q.Choices) > 0 && q.Choices[0].IsMulti:
		return "несколько вариантов"
	case q.QType == "truefalse":
		return "верно/неверно"
	case len(q.Choices) > 0:
		return "один вариант"
	}
	return q.QType
}

// clipRunes обрезает строку до n рун (с многоточием), переводы строк сводятся к пробелам.
func clipRunes(s string, n int) string {
	return truncRunes(cleanStr(s), n)
}

// nextStep — что делать дальше, одной строкой для отчёта.
func (in quizReportInput) nextStep() string {
	left := ""
	if in.AttemptsLeft > 0 {
		left = fmt.Sprintf(", осталось попыток %d", in.AttemptsLeft)
	}
	switch {
	case in.Retry:
		return "повтор будет автоматически" + left + "."
	case in.AttemptsLeft == 0:
		return "попыток не осталось."
	}
	s := "нажми „Пройти“" + left + "."
	if in.Note != "" {
		s += " " + in.Note + "."
	}
	return s
}

// buildQuizReport — текст отчёта в Telegram (HTML). Все подстановки экранируются.
func buildQuizReport(in quizReportInput) string {
	esc := html.EscapeString
	var b strings.Builder
	grade := in.Grade
	if grade == "" {
		grade = "—"
	}
	switch {
	case in.Solved && in.Grade != "":
		fmt.Fprintf(&b, "<b>✅ Тест решён: %s</b>\n", esc(in.Grade))
	case in.Solved:
		b.WriteString("<b>✅ Тест решён</b>\n")
	default:
		fmt.Fprintf(&b, "<b>Тест Moodle: оценка %s</b>\n", esc(grade))
	}
	fmt.Fprintf(&b, "<b>Курс:</b> %s\n", esc(in.Course))
	fmt.Fprintf(&b, "<b>Тест:</b> %s\n", esc(in.Title))
	if in.Method != "" {
		fmt.Fprintf(&b, "<b>Оценка:</b> %s (метод: %s)\n", esc(grade), esc(in.Method))
	} else {
		fmt.Fprintf(&b, "<b>Оценка:</b> %s\n", esc(grade))
	}
	if !in.Solved {
		fmt.Fprintf(&b, "<b>Дальше:</b> %s\n", esc(in.nextStep()))
	}

	model := "—"
	switch {
	case in.FromBaseNoKey:
		model = "нет ключа ИИ, все ответы из базы"
	case len(in.Models) > 0:
		model = strings.Join(in.Models, ", ")
	}
	fmt.Fprintf(&b, "<b>Модель:</b> %s\n", esc(model))
	conf := "—"
	if len(in.Confidence) > 0 {
		sum := 0.0
		for _, c := range in.Confidence {
			sum += c
		}
		conf = fmt.Sprintf("%.2f", sum/float64(len(in.Confidence)))
	}
	fmt.Fprintf(&b, "<b>Уверенность модели:</b> %s\n", conf)
	materials := "нет"
	if in.Materials {
		materials = "есть"
	}
	fmt.Fprintf(&b, "<b>Материалы курса:</b> %s\n", materials)
	fmt.Fprintf(&b, "<b>Веб-поиск:</b> %s\n", esc(in.Search))
	fmt.Fprintf(&b, "<b>Ответы:</b> из базы %d, через ИИ %d\n", in.FromBase, in.ByAI)
	if len(in.Questions) > 0 {
		b.WriteString("\n<b>Вопросы:</b>\n")
	}

	footer := ""
	if in.URL != "" {
		footer = fmt.Sprintf("\n<a href=\"%s\">Открыть тест в Moodle</a>", esc(in.URL))
	}
	for i, q := range in.Questions {
		mark := ""
		if q.Mark != "" {
			mark = " — " + q.Mark
		}
		entry := fmt.Sprintf("<b>Вопрос %d</b> (%s)%s: «%s»\n<pre>%s</pre>\n",
			q.Slot, esc(q.Kind), esc(mark), esc(clipRunes(q.Text, 120)), esc(q.Answer))
		if utf8.RuneCountInString(b.String()+entry+footer) > quizReportMaxRunes {
			fmt.Fprintf(&b, "… и ещё %d вопросов\n", len(in.Questions)-i)
			break
		}
		b.WriteString(entry)
	}
	b.WriteString(footer)
	return b.String()
}

// Package uni синхронизирует данные универа по сохранённым аккаунтам:
// расписание из ЛК, задания и тесты из Moodle.
// Работает в процессе rasp; бот и панель читают результат из sqlite.
package uni

import (
	"context"
	"errors"
	"fmt"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websearch"
	"html"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/lk"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// ErrNoAccount — нет ни одного рабочего аккаунта ЛК: расписание берём с сайта.
var ErrNoAccount = errors.New("uni: no lk account")

var (
	kickTick      = 5 * time.Second
	moodleEvery   = 30 * time.Minute
	moodleFull    = 6 * time.Hour
	retryAfterErr = 10 * time.Minute
	pageDelay     = 150 * time.Millisecond
	nowFn         = time.Now
)

type Syncer struct {
	Store   *store.Store
	Vault   *vault.Vault
	Loc     *time.Location
	GroupID int64 // группа бота; у ЛК её id тот же, что на ssau.ru/rasp
	AdminID int64
	// Notify шлёт пользователю HTML-сообщение в Telegram.
	Notify func(ctx context.Context, telegramID int64, text string)

	LKBase     string // для тестов
	MoodleBase string

	Cfg *config.Config
	// Web — веб-поиск решателя тестов; nil — собрать из Cfg.
	Web *websearch.Searcher
	// DescribeImage — текст рисунка вопроса (для тестов); nil — Moodle + vision.
	DescribeImage func(ctx context.Context, src string) (string, error)

	MoodleWorkers int // переопределение MOODLE_WORKERS (если > 0)
	QuizWorkers   int // переопределение MOODLE_QUIZ_WORKERS (если > 0)
	// QuizMode — QuizModeService или QuizModeInline; пусто — из QUIZ_MODE (см. quiz_service.go).
	QuizMode     string
	QuizParallel int // переопределение QUIZ_MAX_PARALLEL у воркера очереди (если > 0)

	mu          sync.Mutex
	lastFull    map[int64]time.Time
	lkPrev      map[int64][]model.Lesson // прошлый разбор пар по tg — для диффа в журнале
	runningAcc  map[int64]bool           // активные аккаунты, синхронизируемые прямо сейчас
	runningQuiz map[string]bool          // активные тесты (tg:quizID), решаемые прямо сейчас
	sem         chan struct{}            // семафор параллельных воркеров Moodle
	quizSem     chan struct{}            // семафор параллельного решения тестов (пул решения)
	quizPacer   *moodlePacer             // темп входов и стартов попыток воркера очереди; nil — без ограничения
	// solveHook — тестовый крючок: вызывается в горутине страницы перед решением (паника в нём
	// проверяет перехват в solveSinglePageSafe). В проде nil.
	solveHook func(idx int)
	wg        sync.WaitGroup // ожидание завершения фоновых воркеров
}

func (s *Syncer) now() time.Time { return nowFn().In(s.loc()) }

func (s *Syncer) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.UTC
}

func (s *Syncer) notify(ctx context.Context, id int64, text string) {
	if s.Notify != nil {
		s.Notify(ctx, id, text)
	}
}

// ---------- сессии ----------

// session поднимает сохранённые куки, а если они протухли — входит по паролю.
// После любого исхода пишет статус аккаунта.
type authClient interface {
	Session() string
	Restore(string)
	Login(ctx context.Context, login, password string) error
}

func (s *Syncer) withSession(ctx context.Context, acc model.Account, c authClient, badLogin, unauthorized error, fn func() error) error {
	if s.Vault == nil {
		return errors.New("uni: no vault")
	}
	if raw, err := s.Vault.Open(acc.Session); err == nil {
		c.Restore(raw)
	} else if acc.Session != "" {
		logx.Warnf("uni", "%s tg=%d: сохранённая сессия не открылась: %v", acc.Kind, acc.TelegramID, err)
	}
	login := func() error {
		pass, err := s.Vault.Open(acc.Secret)
		if err != nil {
			return fmt.Errorf("uni: decrypt password: %w", err)
		}
		return c.Login(ctx, acc.Login, pass)
	}
	err := errors.New("no session")
	if acc.Session != "" {
		err = fn()
		if err == nil {
			logx.Debugf("uni", "%s tg=%d: работа по сохранённой сессии", acc.Kind, acc.TelegramID)
		}
	}
	if acc.Session == "" || errors.Is(err, unauthorized) {
		why := "сохранённой сессии нет"
		if acc.Session != "" {
			why = "сохранённая сессия протухла"
		}
		logx.Infof("uni", "%s tg=%d (%s): новый вход по логину, %s", acc.Kind, acc.TelegramID, acc.Login, why)
		err = login()
		switch {
		case err == nil:
			logx.Infof("uni", "%s tg=%d (%s): вход по логину выполнен", acc.Kind, acc.TelegramID, acc.Login)
			err = fn()
		case errors.Is(err, badLogin):
			logx.Warnf("uni", "%s tg=%d (%s): вход отклонён сайтом (неверный логин или пароль)", acc.Kind, acc.TelegramID, acc.Login)
		default:
			logx.Warnf("uni", "%s tg=%d (%s): вход по логину не удался: %v", acc.Kind, acc.TelegramID, acc.Login, err)
		}
	}
	if sess, serr := s.Vault.Seal(c.Session()); serr == nil && sess != "" {
		if perr := s.Store.SetAccountSession(acc.TelegramID, acc.Kind, sess); perr != nil {
			logx.Warnf("uni", "%s tg=%d: сохранить сессию: %v", acc.Kind, acc.TelegramID, perr)
		} else {
			logx.Debugf("uni", "%s tg=%d: сессия сохранена", acc.Kind, acc.TelegramID)
		}
	} else if serr != nil {
		logx.Warnf("uni", "%s tg=%d: сессия не зашифровалась: %v", acc.Kind, acc.TelegramID, serr)
	}
	switch {
	case err == nil:
		s.setStatus(acc, model.AccountOK, "")
	case errors.Is(err, badLogin):
		s.setStatus(acc, model.AccountBadLogin, "Сайт не принял логин или пароль")
	case ctx.Err() != nil:
	default:
		s.setStatus(acc, model.AccountError, shortErr(err))
	}
	return err
}

func (s *Syncer) setStatus(acc model.Account, status, msg string) {
	if err := s.Store.SetAccountStatus(acc.TelegramID, acc.Kind, status, msg); err != nil {
		logx.Warnf("uni", "set status: %v", err)
	}
}

func shortErr(err error) string {
	m := err.Error()
	if r := []rune(m); len(r) > 200 {
		m = string(r[:200])
	}
	return m
}

func (s *Syncer) usable(kind string) ([]model.Account, error) {
	all, err := s.Store.ListAccounts(kind)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, a := range all {
		if a.Status != model.AccountBadLogin {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TelegramID == s.AdminID && out[j].TelegramID != s.AdminID })
	return out, nil
}

// ---------- расписание ----------

// Lessons — расписание группы на эту и следующую неделю из ЛК плюс пары ИОТ
// каждого, кто подключил ЛК. Для rasp.Refresher.Source.
func (s *Syncer) Lessons(ctx context.Context) ([]model.Lesson, error) {
	accs, err := s.usable(model.AccountLK)
	if err != nil {
		return nil, err
	}
	if len(accs) == 0 {
		return nil, ErrNoAccount
	}
	var out []model.Lesson
	var firstErr error
	group := false
	for _, acc := range accs {
		ls, err := s.accountLessons(ctx, acc, !group)
		if err != nil {
			logx.Warnf("uni", "lk tg=%d (%s): синк расписания не удался: %v", acc.TelegramID, acc.Login, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !group {
			group = true
			out = append(out, ls...)
			continue
		}
		for _, l := range ls { // у остальных берём только их ИОТ
			if l.Owner != 0 {
				out = append(out, l)
			}
		}
	}
	if !group {
		return nil, firstErr
	}
	return out, nil
}

func (s *Syncer) lkClient() (*lk.Client, error) { return lk.New(s.LKBase) }

func (s *Syncer) accountLessons(ctx context.Context, acc model.Account, withGroup bool) ([]model.Lesson, error) {
	began := time.Now()
	c, err := s.lkClient()
	if err != nil {
		return nil, err
	}
	logx.Infof("uni", "lk tg=%d (%s): старт обхода расписания, своя группа=%v, триггер: %s",
		acc.TelegramID, acc.Login, withGroup, syncTrigger(ctx, "расписание: обход rasp"))
	var out []model.Lesson
	var weeks, counts []int
	err = s.withSession(ctx, acc, c, lk.ErrBadLogin, lk.ErrUnauthorized, func() error {
		out = nil
		weeks, counts = nil, nil
		groups, err := c.Groups(ctx)
		if err != nil {
			return err
		}
		if len(groups) == 0 {
			return errors.New("в ЛК нет группы")
		}
		s.refreshLKName(ctx, c, acc.TelegramID)
		gid := groups[0].ID
		for _, g := range groups {
			if g.ID == s.GroupID {
				gid = g.ID
			}
		}
		years, err := c.Years(ctx)
		if err != nil {
			return err
		}
		now := s.now()
		yid, ok := lk.CurrentYearID(years, now)
		if !ok {
			return errors.New("в ЛК нет учебного года")
		}
		start, err := s.yearStart(ctx, c, yid, gid)
		if err != nil {
			return err
		}
		week, err := lk.WeekOf(start, now, s.loc())
		if err != nil {
			return err
		}
		logx.Debugf("uni", "lk tg=%d: группа id=%d, учебный год id=%d, неделя %d (год с %s)", acc.TelegramID, gid, yid, week, start)
		for _, w := range []int{week, week + 1} {
			tt, err := c.Timetable(ctx, yid, w, gid)
			if err != nil {
				return err
			}
			ls, err := tt.ToLessons(w, gid, acc.TelegramID, s.loc())
			if err != nil {
				return err
			}
			n := 0
			for _, l := range ls {
				if withGroup || l.Owner != 0 {
					out = append(out, l)
					n++
				}
			}
			weeks = append(weeks, w)
			counts = append(counts, n)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	added, removed, changed := s.diffLessons(acc.TelegramID, s.prevLessons(acc.TelegramID), out)
	s.mu.Lock()
	if s.lkPrev == nil {
		s.lkPrev = map[int64][]model.Lesson{}
	}
	s.lkPrev[acc.TelegramID] = out
	s.mu.Unlock()
	perWeek := make([]string, 0, len(weeks))
	for i, w := range weeks {
		perWeek = append(perWeek, fmt.Sprintf("неделя %d: пар %d", w, counts[i]))
	}
	logx.Infof("uni", "lk tg=%d (%s): готово за %s, %s, диф: +%d -%d ~%d",
		acc.TelegramID, acc.Login, time.Since(began).Round(time.Millisecond), strings.Join(perWeek, ", "), added, removed, changed)
	return out, nil
}

func (s *Syncer) prevLessons(telegramID int64) []model.Lesson {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lkPrev[telegramID]
}

// diffLessons пишет в журнал, что изменилось в парах человека с прошлого обхода.
// Первый обход после старта процесса — всё «новое», поэтому строки пойдут в DEBUG.
func (s *Syncer) diffLessons(tg int64, prev, cur []model.Lesson) (added, removed, changed int) {
	logf := logx.Infof
	if len(prev) == 0 {
		logf = logx.Debugf
	}
	pm, cm := groupLessons(prev), groupLessons(cur)
	keySet := map[string]bool{}
	for k := range pm {
		keySet[k] = true
	}
	for k := range cm {
		keySet[k] = true
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p, c := pm[k], cm[k]
		for i := 0; i < len(p) || i < len(c); i++ {
			switch {
			case i >= len(p):
				added++
				logf("uni", "lk tg=%d: + новая пара %s", tg, lessonTag(c[i]))
			case i >= len(c):
				removed++
				logf("uni", "lk tg=%d: - пара пропала %s", tg, lessonTag(p[i]))
			default:
				if ch := lessonChanges(p[i], c[i]); len(ch) > 0 {
					changed++
					logf("uni", "lk tg=%d: ~ %s: %s", tg, lessonTag(c[i]), strings.Join(ch, "; "))
				}
			}
		}
	}
	return added, removed, changed
}

// groupLessons — пары по дню, предмету, виду занятия, подгруппе и владельцу (ИОТ), по времени.
func groupLessons(ls []model.Lesson) map[string][]model.Lesson {
	m := map[string][]model.Lesson{}
	for _, l := range ls {
		k := fmt.Sprintf("%s|%s|%s|%d|%d", l.Date, l.Discipline, l.Type, l.Subgroup, l.Owner)
		m[k] = append(m[k], l)
	}
	for _, v := range m {
		sort.SliceStable(v, func(i, j int) bool { return v[i].Begin.Before(v[j].Begin) })
	}
	return m
}

func lessonTag(l model.Lesson) string {
	who := "группа"
	if l.Owner != 0 {
		who = "ИОТ"
	}
	tag := fmt.Sprintf("%s %s-%s %q (%s, %s)", l.Date, l.Start, l.End, l.Discipline, l.Type, who)
	if l.Subgroup != 0 {
		tag += fmt.Sprintf(", подгруппа %d", l.Subgroup)
	}
	return tag
}

// lessonChanges — что поменялось в той же паре: время, аудитория, ссылка BBB, преподаватель, онлайн.
// Сами ссылки в журнал не пишем: только факт появления, пропажи или смены.
func lessonChanges(p, c model.Lesson) []string {
	var ch []string
	if p.Start != c.Start || p.End != c.End {
		ch = append(ch, fmt.Sprintf("время %s-%s → %s-%s", p.Start, p.End, c.Start, c.End))
	}
	if p.Place != c.Place {
		ch = append(ch, fmt.Sprintf("аудитория %q → %q", p.Place, c.Place))
	}
	switch {
	case (p.BBBURL == "") != (c.BBBURL == ""):
		if c.BBBURL != "" {
			ch = append(ch, "ссылка BBB появилась")
		} else {
			ch = append(ch, "ссылка BBB пропала")
		}
	case p.BBBURL != c.BBBURL:
		ch = append(ch, "ссылка BBB изменилась")
	}
	if p.Teacher != c.Teacher {
		ch = append(ch, fmt.Sprintf("преподаватель %q → %q", p.Teacher, c.Teacher))
	}
	if p.Online != c.Online {
		ch = append(ch, fmt.Sprintf("онлайн %v → %v", p.Online, c.Online))
	}
	return ch
}

// LKNameKey — ФИО человека из ЛК (страница /account), по Telegram id.
func LKNameKey(telegramID int64) string { return "lk_name:" + strconv.FormatInt(telegramID, 10) }

func lkNameAtKey(telegramID int64) string { return "lk_name_at:" + strconv.FormatInt(telegramID, 10) }

// refreshLKName — раз в сутки берёт ФИО из ЛК: оно связывает человека в
// панели, боте и журналах. Пустое «имя для BBB» заполняется им же.
func (s *Syncer) refreshLKName(ctx context.Context, c *lk.Client, id int64) {
	now := s.now()
	if v, ok, _ := s.Store.GetSetting(lkNameAtKey(id)); ok && v != "" {
		if at, err := time.Parse(time.RFC3339, v); err == nil && now.Sub(at) < 24*time.Hour {
			return
		}
	}
	name, err := c.FullName(ctx)
	if err != nil {
		logx.Warnf("uni", "lk name tg=%d: %v", id, err)
		return
	}
	_ = s.Store.SetSetting(LKNameKey(id), name)
	_ = s.Store.SetSetting(lkNameAtKey(id), now.Format(time.RFC3339))
	if u, err := s.Store.GetUser(id); err == nil && u != nil && strings.TrimSpace(u.FIO) == "" {
		if err := s.Store.SetFIO(id, name); err != nil {
			logx.Warnf("uni", "lk name -> fio tg=%d: %v", id, err)
		}
	}
	logx.Infof("uni", "lk name tg=%d: %s", id, name)
}

// yearStart — дата начала учебного года (кэш в settings, чтобы не дёргать лишнюю неделю).
func (s *Syncer) yearStart(ctx context.Context, c *lk.Client, yearID, groupID int64) (string, error) {
	key := "lk_year_start:" + strconv.FormatInt(yearID, 10)
	if v, ok, err := s.Store.GetSetting(key); err == nil && ok && v != "" {
		return v, nil
	}
	tt, err := c.Timetable(ctx, yearID, 1, groupID)
	if err != nil {
		return "", err
	}
	start := tt.CurrentYear.StartDate
	if start == "" {
		return "", errors.New("ЛК не отдал начало года")
	}
	if err := s.Store.SetSetting(key, start); err != nil {
		logx.Warnf("uni", "cache year start: %v", err)
	}
	logx.Debugf("uni", "год id=%d: начало %s (из ЛК, закэшировано)", yearID, start)
	return start, nil
}

// ---------- Moodle ----------

func (s *Syncer) moodleClient() (*moodle.Client, error) { return moodle.New(s.MoodleBase) }

// SyncMoodle обновляет задания пользователя. full — обойти все курсы целиком;
// иначе перепроверить только ленту сроков и несданное с близким сроком.
func (s *Syncer) SyncMoodle(ctx context.Context, acc model.Account, full bool) (err error) {
	began := time.Now()
	trigger := syncTrigger(ctx, "прямой вызов")
	defer func() {
		if err != nil && ctx.Err() == nil {
			logx.Warnf("uni", "moodle tg=%d (%s): синк не удался за %s (full=%v, триггер: %s): %v",
				acc.TelegramID, acc.Login, time.Since(began).Round(time.Millisecond), full, trigger, err)
		}
	}()
	c, err := s.moodleClient()
	if err != nil {
		return err
	}
	prev, err := s.Store.ListMoodleItems(acc.TelegramID)
	if err != nil {
		return err
	}
	if len(prev) == 0 {
		full = true
	}
	logx.Infof("uni", "moodle tg=%d (%s): старт синка, full=%v, триггер: %s, в базе элементов %d",
		acc.TelegramID, acc.Login, full, trigger, len(prev))
	var items []model.MoodleItem
	err = s.withSession(ctx, acc, c, moodle.ErrBadLogin, moodle.ErrUnauthorized, func() error {
		var err error
		items, err = s.collectMoodle(ctx, c, acc.TelegramID, prev, full)
		if err != nil {
			return err
		}

		// Решение тестов уходит в отдельный пул (scheduleQuiz), синк аккаунта не ждёт его
		// и освобождает слот сразу. Защита от двойного решения — блокировка (tg, cmid) в scheduleQuiz.
		s.detectAndHandleOpenedQuizzes(ctx, acc, c, prev, items)
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.Store.ReplaceMoodleItems(acc.TelegramID, items); err != nil {
		return err
	}
	if full {
		s.mu.Lock()
		if s.lastFull == nil {
			s.lastFull = map[int64]time.Time{}
		}
		s.lastFull[acc.TelegramID] = s.now()
		s.mu.Unlock()
	}
	added, removed, changed := s.logMoodleDiff(acc.TelegramID, prev, items)
	if len(prev) > 0 {
		s.announceNew(ctx, acc.TelegramID, prev, items)
	}
	tasks, tests, done := 0, 0, 0
	for _, it := range items {
		switch it.Kind {
		case model.MoodleAssign:
			tasks++
		case model.MoodleQuiz:
			tests++
		}
		if it.Finished() {
			done++
		}
	}
	logx.Infof("uni", "moodle tg=%d (%s): готово за %s, full=%v, триггер: %s; элементов %d (заданий %d, тестов %d, сдано %d), диф: +%d -%d ~%d",
		acc.TelegramID, acc.Login, time.Since(began).Round(time.Millisecond), full, trigger, len(items), tasks, tests, done, added, removed, changed)
	return nil
}

func (s *Syncer) collectMoodle(ctx context.Context, c *moodle.Client, tg int64, prev []model.MoodleItem, full bool) ([]model.MoodleItem, error) {
	now := s.now()
	courses, err := c.Courses(ctx)
	if err != nil {
		return nil, err
	}
	logx.Debugf("uni", "moodle tg=%d: курсов %d, полный обход=%v", tg, len(courses), full)
	courseName := map[int64]string{}
	for _, cr := range courses {
		courseName[cr.ID] = cr.FullName
	}
	events, err := c.Timeline(ctx, now.AddDate(0, 0, -14), time.Time{})
	if err != nil {
		return nil, err
	}

	prevBy := map[int64]model.MoodleItem{}
	for _, p := range prev {
		prevBy[p.CMID] = p
	}
	byCM := map[int64]model.MoodleItem{}
	var acts []moodle.Activity
	if full {
		for _, cr := range courses {
			list, err := c.CourseActivities(ctx, cr)
			if err != nil {
				if errors.Is(err, moodle.ErrUnauthorized) || ctx.Err() != nil {
					return nil, err
				}
				logx.Warnf("uni", "moodle tg=%d: курс id=%d %q: %v (сохраняем старые задания курса)", tg, cr.ID, cr.FullName, err)
				for _, p := range prev {
					if (cr.ID != 0 && p.CourseID == cr.ID) || (cr.FullName != "" && p.Course == cr.FullName) {
						byCM[p.CMID] = p
					}
				}
				continue
			}
			logx.Debugf("uni", "moodle tg=%d: курс id=%d %q: элементов %d", tg, cr.ID, cr.FullName, len(list))
			acts = append(acts, list...)
			sleep(ctx, pageDelay)
		}
	} else {
		logx.Debugf("uni", "moodle tg=%d: быстрый режим: курсы не обходятся, перепроверяются лента и несданное со сроком", tg)
		for _, p := range prev {
			byCM[p.CMID] = p
		}
	}
	// Лента: несданное с ближайшими сроками — добавляем то, чего не нашли в курсах.
	inTimeline := map[int64]moodle.Event{}
	known := map[int64]bool{}
	for _, a := range acts {
		known[a.CMID] = true
	}
	for _, p := range prev {
		if !full {
			known[p.CMID] = true
		}
	}
	for cmid := range byCM {
		known[cmid] = true
	}
	withCM, fromTL := 0, 0
	for _, e := range events {
		cm := e.CMID()
		if cm == 0 {
			continue
		}
		withCM++
		inTimeline[cm] = e
		if !known[cm] {
			known[cm] = true
			fromTL++
			course := e.Course.FullName
			if course == "" {
				course = courseName[e.Course.ID]
			}
			logx.Debugf("uni", "moodle tg=%d: из ленты добавлен cmid=%d тип=%s %q", tg, cm, e.ModuleName, e.Name)
			acts = append(acts, moodle.Activity{CMID: cm, CourseID: e.Course.ID, Course: course, Kind: e.ModuleName,
				Name: strings.TrimSpace(strings.TrimSuffix(e.Name, " закрывается")), URL: e.URL})
		}
	}
	logx.Debugf("uni", "moodle tg=%d: лента: событий с модулем %d, добавлено из ленты %d", tg, withCM, fromTL)
	if !full {
		// Перепроверяем: всё из ленты и несданное со сроком в окне −3…+14 дней.
		for _, p := range prev {
			_, inTL := inTimeline[p.CMID]
			near := p.Due != nil && p.Due.After(now.AddDate(0, 0, -3)) && p.Due.Before(now.AddDate(0, 0, 14))
			if inTL || (!p.Finished() && near) {
				acts = append(acts, moodle.Activity{CMID: p.CMID, CourseID: p.CourseID, Course: p.Course,
					Section: p.Section, Kind: p.Kind, Name: p.Title, URL: p.URL})
			}
		}
	}

	seen := map[int64]bool{}
	for _, a := range acts {
		if seen[a.CMID] {
			continue
		}
		seen[a.CMID] = true
		d, err := c.ActivityDetail(ctx, a, s.loc())
		if err != nil {
			if errors.Is(err, moodle.ErrUnauthorized) || ctx.Err() != nil {
				return nil, err
			}
			logx.Warnf("uni", "moodle tg=%d: элемент cmid=%d тип=%s не разобран: %v", tg, a.CMID, a.Kind, err)
			if old, ok := prevBy[a.CMID]; ok {
				byCM[a.CMID] = old // оставляем прежнее
			}
			continue
		}
		item := model.MoodleItem{
			CMID: a.CMID, CourseID: a.CourseID, Course: a.Course, Section: a.Section,
			Kind: a.Kind, Title: a.Name, URL: a.URL, Opens: d.Opens, Due: d.Due, Status: d.Status,
			Todo: strings.Join(d.Todo, "\n"),
		}
		if item.Course == "" {
			item.Course = courseName[a.CourseID]
		}
		if e, ok := inTimeline[a.CMID]; ok && item.Due == nil && e.TimeSort > 0 {
			t := time.Unix(e.TimeSort, 0).In(s.loc())
			item.Due = &t
		}
		if old, ok := prevBy[a.CMID]; ok && item.Section == "" {
			item.Section = old.Section
		}
		byCM[a.CMID] = item
		sleep(ctx, pageDelay)
	}
	out := make([]model.MoodleItem, 0, len(byCM))
	for _, it := range byCM {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CMID < out[j].CMID })
	return out, nil
}

// logMoodleDiff пишет в журнал, что изменилось в заданиях человека с прошлого синка:
// появились, пропали, сменили название, статус, открытие, срок или условия.
// Первый синк (в базе пусто) — всё «новое», поэтому строки уходят в DEBUG.
func (s *Syncer) logMoodleDiff(tg int64, prev, cur []model.MoodleItem) (added, removed, changed int) {
	logf := logx.Infof
	if len(prev) == 0 {
		logf = logx.Debugf
	}
	prevBy := make(map[int64]model.MoodleItem, len(prev))
	for _, p := range prev {
		prevBy[p.CMID] = p
	}
	curBy := make(map[int64]bool, len(cur))
	for _, it := range cur {
		curBy[it.CMID] = true
		old, ok := prevBy[it.CMID]
		if !ok {
			added++
			logf("uni", "moodle tg=%d: + новый элемент %s, статус %s, срок %s", tg, itemTag(it), it.Status, fmtAt(it.Due, s.loc()))
			continue
		}
		if ch := s.moodleChanges(old, it); len(ch) > 0 {
			changed++
			logf("uni", "moodle tg=%d: ~ %s: %s", tg, itemTag(it), strings.Join(ch, "; "))
		}
	}
	for _, p := range prev {
		if !curBy[p.CMID] {
			removed++
			logx.Infof("uni", "moodle tg=%d: - элемент пропал %s (был статус %s, срок %s)", tg, itemTag(p), p.Status, fmtAt(p.Due, s.loc()))
		}
	}
	return added, removed, changed
}

// moodleChanges — что поменялось в элементе: название, статус, открытие, срок, условия (было → стало).
func (s *Syncer) moodleChanges(old, cur model.MoodleItem) []string {
	var ch []string
	if old.Title != cur.Title {
		ch = append(ch, fmt.Sprintf("название %q → %q", old.Title, cur.Title))
	}
	if old.Status != cur.Status {
		ch = append(ch, fmt.Sprintf("статус %s → %s", old.Status, cur.Status))
	}
	if a, b := fmtAt(old.Opens, s.loc()), fmtAt(cur.Opens, s.loc()); a != b {
		ch = append(ch, fmt.Sprintf("открытие %s → %s", a, b))
	}
	if a, b := fmtAt(old.Due, s.loc()), fmtAt(cur.Due, s.loc()); a != b {
		ch = append(ch, fmt.Sprintf("срок %s → %s", a, b))
	}
	if old.Todo != cur.Todo {
		ch = append(ch, fmt.Sprintf("условия %q → %q", oneLine(old.Todo), oneLine(cur.Todo)))
	}
	return ch
}

func itemTag(it model.MoodleItem) string {
	return fmt.Sprintf("cmid=%d %s %q (%s)", it.CMID, it.Kind, it.Title, it.Course)
}

func fmtAt(t *time.Time, loc *time.Location) string {
	if t == nil {
		return "-"
	}
	return t.In(loc).Format("02.01.2006 15:04")
}

// oneLine — многострочный текст в одну строку, не длиннее 160 символов (для журнала).
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " | ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}

// announceNew — новое задание со сроком в будущем: короткое сообщение.
func (s *Syncer) announceNew(ctx context.Context, id int64, prev, cur []model.MoodleItem) {
	old := map[int64]bool{}
	for _, p := range prev {
		old[p.CMID] = true
	}
	now := s.now()
	n := 0
	for _, it := range cur {
		if it.Kind == model.MoodleQuiz {
			continue // открывшиеся тесты обрабатываются в detectAndHandleOpenedQuizzes
		}
		if old[it.CMID] || it.Finished() || it.Due == nil || it.Due.Before(now) {
			continue
		}
		if n++; n > 5 {
			break
		}
		s.notify(ctx, id, "🆕 <b>Новое в Moodle</b>\n"+FormatItem(it, now, s.loc()))
	}
}

// FormatItem — карточка задания для Telegram (HTML).
func FormatItem(it model.MoodleItem, now time.Time, loc *time.Location) string {
	var b strings.Builder
	b.WriteString("<b>" + html.EscapeString(it.Title) + "</b>")
	b.WriteString(" · " + KindLabel(it.Kind))
	b.WriteString("\n" + html.EscapeString(it.Course))
	if it.Due != nil {
		b.WriteString("\n⏰ до " + it.Due.In(loc).Format("02.01 15:04") + " (" + Until(*it.Due, now) + ")")
	}
	b.WriteString("\n" + StatusLabel(it.Status))
	if t := strings.TrimSpace(it.Todo); t != "" {
		lines := strings.Split(t, "\n")
		if len(lines) > 4 {
			lines = lines[:4]
		}
		for _, l := range lines {
			b.WriteString("\n• " + html.EscapeString(l))
		}
	}
	if it.URL != "" {
		b.WriteString("\n<a href=\"" + html.EscapeString(it.URL) + "\">Открыть в Moodle</a>")
	}
	return b.String()
}

func KindLabel(k string) string {
	switch k {
	case model.MoodleQuiz:
		return "тест"
	case model.MoodleAssign:
		return "задание"
	case model.MoodleLesson:
		return "лекция-урок"
	case model.MoodleWorkshop:
		return "семинар"
	}
	return k
}

func StatusLabel(s string) string {
	switch s {
	case model.MoodleDraft:
		return "✏️ черновик / попытка не завершена"
	case model.MoodleSubmitted:
		return "✅ отправлено"
	case model.MoodleDone:
		return "✅ выполнено"
	}
	return "❗ не сдано"
}

// Until — «через 2 д 3 ч» / «просрочено 5 ч».
func Until(due, now time.Time) string {
	d := due.Sub(now)
	prefix := "через "
	if d < 0 {
		prefix, d = "просрочено ", -d
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%s%d д %d ч", prefix, days, hours)
	case hours > 0:
		return fmt.Sprintf("%s%d ч %d мин", prefix, hours, mins)
	default:
		return fmt.Sprintf("%s%d мин", prefix, mins)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
